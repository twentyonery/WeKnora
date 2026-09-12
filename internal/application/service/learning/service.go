// Package learning implements the learning navigator (topic 4): it relates a
// person's memory signals to the wiki knowledge network, derives a mastery
// score per page, and recommends the next nodes to learn.
//
// Design notes live in topic4 docs; the short version: mastery is computed
// only from observable behavior (answers citing a document, page views,
// recurring interest topics), never from a model's opinion, and every signal
// decays with time so the profile tracks what the person currently knows.
package learning

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/application/service/memory"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrNoLearningScope means the request carries no principal; the handler maps
// it to 401 for the same reason the memory handler does.
var ErrNoLearningScope = memory.ErrNoMemoryScope

// Signal weights. A doc citation (an answer actually drew on the material) is
// the strongest signal; a page view is deliberate learning; a topic match is
// the weakest because a shared keyword is not engagement.
const (
	weightDoc   = 0.35
	weightView  = 0.25
	weightTopic = 0.15
)

// halfLife controls how fast signals fade. Fourteen days means a month-old
// burst of activity contributes a quarter of its weight — long enough that a
// normal study cadence accumulates, short enough that mastery tracks drift.
const halfLife = 14 * 24 * time.Hour

// recommendation bounds.
const (
	defaultRecommendLimit = 5
	maxRecommendLimit     = 20
)

// Service is the learning navigator implementation.
type Service struct {
	repo interfaces.LearningRepository
}

// NewLearningService creates the learning navigator service.
func NewLearningService(repo interfaces.LearningRepository) *Service {
	return &Service{repo: repo}
}

// Profile recomputes and returns the caller's mastery profile for one KB.
func (s *Service) Profile(ctx context.Context, kbID string) (*types.LearningProfile, error) {
	if kbID == "" {
		return nil, errors.New("learning: kb_id is required")
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return nil, err
	}
	tenantID := scope.TenantID

	pages, err := s.repo.ListWikiPages(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	if err := s.refreshSourceIndex(ctx, tenantID, kbID, pages); err != nil {
		return nil, err
	}

	affinity, err := s.repo.ListDocAffinity(ctx, scope, kbID)
	if err != nil {
		return nil, err
	}
	topics, err := s.repo.ListTopicStats(ctx, scope)
	if err != nil {
		return nil, err
	}
	events, err := s.repo.ListEvents(ctx, scope, kbID)
	if err != nil {
		return nil, err
	}

	nodes, states := s.compute(ctx, scope, kbID, pages, affinity, topics, events)
	if err := s.repo.UpsertStates(ctx, scope, states); err != nil {
		return nil, err
	}

	recs := recommend(nodes, defaultRecommendLimit)
	return &types.LearningProfile{
		KnowledgeBaseID: kbID,
		Nodes:           nodes,
		Recommendations: recs,
		ComputedAt:      time.Now(),
	}, nil
}

// Recommend returns the top frontier nodes without recomputing persisted
// states twice: it runs the same pipeline as Profile.
func (s *Service) Recommend(ctx context.Context, kbID string, limit int) ([]types.LearningRecommendation, error) {
	profile, err := s.Profile(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxRecommendLimit {
		limit = maxRecommendLimit
	}
	recs := recommend(profile.Nodes, limit)
	return recs, nil
}

// RecordPageView appends a view event. Failures are logged and swallowed by
// the handler: a telemetry write must never break reading a page.
func (s *Service) RecordPageView(ctx context.Context, kbID, slug string) error {
	if kbID == "" || slug == "" {
		return errors.New("learning: kb_id and slug are required")
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	return s.repo.AppendEvents(ctx, scope, []types.LearningNodeEvent{{
		ID:              uuid.New().String(),
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: kbID,
		Slug:            slug,
		SignalType:      types.LearningSignalView,
		Weight:          1,
		OccurredAt:      now,
		CreatedAt:       now,
	}})
}

// RecordAnswerExposure maps cited documents onto the pages that cite them and
// appends doc-signal events. It is best-effort: callers on the answer path
// log failures and continue.
func (s *Service) RecordAnswerExposure(ctx context.Context, refs []types.MemoryDocAffinity) error {
	if len(refs) == 0 {
		return nil
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	events := make([]types.LearningNodeEvent, 0, len(refs))
	for _, ref := range refs {
		if ref.KnowledgeID == "" || ref.KnowledgeBaseID == "" {
			continue
		}
		mappings, err := s.repo.ListPageSourceIndex(ctx, scope.TenantID, ref.KnowledgeBaseID)
		if err != nil {
			return err
		}
		byDoc := map[string][]types.LearningPageSourceIndex{}
		for _, m := range mappings {
			if m.KnowledgeID == ref.KnowledgeID {
				byDoc[ref.KnowledgeID] = append(byDoc[ref.KnowledgeID], m)
			}
		}
		pages := byDoc[ref.KnowledgeID]
		for _, m := range pages {
			events = append(events, types.LearningNodeEvent{
				ID:              uuid.New().String(),
				TenantID:        scope.TenantID,
				SubjectID:       scope.SubjectID,
				KnowledgeBaseID: ref.KnowledgeBaseID,
				Slug:            m.Slug,
				SignalType:      types.LearningSignalDoc,
				Weight:          1,
				OccurredAt:      now,
				CreatedAt:       now,
			})
		}
	}
	return s.repo.AppendEvents(ctx, scope, events)
}

// DeleteProfile removes the caller's learning rows. An empty kbID clears every
// KB in the workspace. It deliberately keeps memory_doc_affinity: the doc
// affinity counter is memory infrastructure shared with retrieval ranking, and
// the learning profile is a derived view — deleting the view must not silently
// change retrieval behavior.
func (s *Service) DeleteProfile(ctx context.Context, kbID string) error {
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return err
	}
	return s.repo.DeleteProfile(ctx, scope, kbID)
}

// refreshSourceIndex rebuilds the document→page mapping from wiki_pages
// SourceRefs. Format per ref is "<knowledge_id>|<title>"; the split recovers
// the id. Each document's signal is split evenly across its citing pages so a
// summary page citing 40 documents cannot dominate.
func (s *Service) refreshSourceIndex(
	ctx context.Context, tenantID uint64, kbID string, pages []types.WikiPage,
) error {
	perDoc := map[string]int{}
	type pair struct{ slug, knowledgeID string }
	pairs := make([]pair, 0)
	for _, p := range pages {
		for _, ref := range p.SourceRefs {
			knowledgeID := ref
			if i := strings.IndexByte(ref, '|'); i >= 0 {
				knowledgeID = ref[:i]
			}
			if knowledgeID == "" {
				continue
			}
			pairs = append(pairs, pair{slug: p.Slug, knowledgeID: knowledgeID})
			perDoc[knowledgeID]++
		}
	}
	rows := make([]types.LearningPageSourceIndex, 0, len(pairs))
	for _, pr := range pairs {
		rows = append(rows, types.LearningPageSourceIndex{
			ID:              uuid.New().String(),
			TenantID:        tenantID,
			KnowledgeBaseID: kbID,
			KnowledgeID:     pr.knowledgeID,
			Slug:            pr.slug,
			RefWeight:       1.0 / float64(perDoc[pr.knowledgeID]),
		})
	}
	return s.repo.ReplacePageSourceIndex(ctx, tenantID, kbID, rows)
}

// compute derives mastery for every page. It returns the API projection and
// the persistence rows in one pass.
func (s *Service) compute(
	ctx context.Context,
	scope interfaces.MemoryScope,
	kbID string,
	pages []types.WikiPage,
	affinity []types.MemoryDocAffinity,
	topics []types.MemoryTopicStat,
	events []types.LearningNodeEvent,
) ([]types.LearningNode, []types.LearningNodeState) {
	now := time.Now()

	// doc signal: doc affinity (hits ≥ threshold, enforced by the query) maps
	// through the source index; decay uses LastUsedAt as the event time.
	mappings, err := s.repo.ListPageSourceIndex(ctx, scope.TenantID, kbID)
	if err != nil {
		logger.Warnf(ctx, "learning: source index read failed, doc signal skipped: %v", err)
	}
	docByPage := map[string]float64{}
	weightByDoc := map[string]float64{}
	for _, m := range mappings {
		weightByDoc[m.KnowledgeID+m.Slug] = m.RefWeight
	}
	for _, a := range affinity {
		for _, m := range mappings {
			if m.KnowledgeID != a.KnowledgeID {
				continue
			}
			docByPage[m.Slug] += float64(a.Hits) * m.RefWeight * decay(now.Sub(a.LastUsedAt))
		}
	}

	// topic signal: recurring interests lexically matched against the page
	// title and aliases. Normalization is lowercase + whitespace squeeze; the
	// memory subsystem already canonicalizes its side.
	topicTerms := make([]string, 0, len(topics))
	for _, t := range topics {
		if t.Hits <= 0 {
			continue
		}
		terms := append([]string{t.Topic}, t.Aliases...)
		for _, term := range terms {
			term = normalizeTerm(term)
			if term != "" {
				topicTerms = append(topicTerms, term)
			}
		}
	}

	// view signal: sum of decayed view events per page.
	viewByPage := map[string]float64{}
	for _, e := range events {
		d := decay(now.Sub(e.OccurredAt))
		switch e.SignalType {
		case types.LearningSignalView:
			viewByPage[e.Slug] += e.Weight * d
		case types.LearningSignalDoc:
			docByPage[e.Slug] += e.Weight * d
		}
	}

	raws := make([]float64, len(pages))
	nodes := make([]types.LearningNode, len(pages))
	for i, p := range pages {
		doc := docByPage[p.Slug]
		view := viewByPage[p.Slug]
		topic := topicSignal(p, topicTerms)
		raws[i] = weightDoc*doc + weightView*view + weightTopic*topic
		nodes[i] = types.LearningNode{
			Slug:     p.Slug,
			Title:    p.Title,
			PageType: p.PageType,
			InLinks:  len(p.InLinks),
			OutLinks: len(p.OutLinks),
			Signals: map[string]float64{
				types.LearningSignalDoc:   doc,
				types.LearningSignalView:  view,
				types.LearningSignalTopic: topic,
			},
		}
	}

	// τ adapts to the KB: P75 of the positive raw scores. With almost no
	// signal yet, the default keeps early mastery values readable instead of
	// pinning everyone at ~0 or ~1.
	tau := adaptiveTau(raws)

	states := make([]types.LearningNodeState, len(pages))
	for i, p := range pages {
		mastery := 1 - math.Exp(-raws[i]/tau)
		state := stateOf(mastery, 0)
		nodes[i].Mastery = round3(mastery)
		nodes[i].State = state
		states[i] = types.LearningNodeState{
			ID:              uuid.New().String(),
			TenantID:        scope.TenantID,
			SubjectID:       scope.SubjectID,
			KnowledgeBaseID: kbID,
			Slug:            p.Slug,
			RawScore:        raws[i],
			Mastery:         nodes[i].Mastery,
			State:           state,
			SignalDoc:       nodes[i].Signals[types.LearningSignalDoc],
			SignalTopic:     nodes[i].Signals[types.LearningSignalTopic],
			SignalView:      nodes[i].Signals[types.LearningSignalView],
			UpdatedAt:       now,
		}
	}

	// frontier needs lit-neighbor counts, which require the lit set first.
	lit := map[string]bool{}
	for _, n := range nodes {
		if n.State == types.LearningStateLit {
			lit[n.Slug] = true
		}
	}
	bySlug := map[string]int{}
	for i := range nodes {
		bySlug[nodes[i].Slug] = i
	}
	for i := range nodes {
		p := pages[i]
		litIn, litOut := 0, 0
		for _, in := range p.InLinks {
			if lit[in] {
				litIn++
			}
		}
		for _, out := range p.OutLinks {
			if lit[out] {
				litOut++
			}
		}
		nodes[i].LitIn = litIn
		nodes[i].LitOut = litOut
		if nodes[i].State == types.LearningStateDim && litIn+litOut > 0 {
			nodes[i].State = types.LearningStateFrontier
			states[i].State = types.LearningStateFrontier
		}
		_ = bySlug
	}
	return nodes, states
}

// stateOf bands mastery; frontier is added later when neighbors are known.
func stateOf(mastery float64, litNeighbors int) string {
	switch {
	case mastery >= types.LearningMasteryLit:
		return types.LearningStateLit
	case mastery >= types.LearningMasteryDim:
		return types.LearningStateDim
	case litNeighbors > 0:
		return types.LearningStateFrontier
	default:
		return types.LearningStateDark
	}
}

// decay is exponential with the module half-life.
func decay(elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 1
	}
	return math.Exp2(-elapsed.Hours() / halfLife.Hours())
}

// adaptiveTau picks the saturation constant. τ = P75 of positive raw scores
// means the busiest quarter of the KB approaches or passes the lit band while
// the rest spreads out below it — a per-KB curve no fixed constant can give.
func adaptiveTau(raws []float64) float64 {
	positive := make([]float64, 0, len(raws))
	for _, r := range raws {
		if r > 0 {
			positive = append(positive, r)
		}
	}
	if len(positive) == 0 {
		return 1
	}
	sort.Float64s(positive)
	p75 := positive[(len(positive)*3)/4]
	if p75 < 0.25 {
		p75 = 0.25
	}
	return p75
}

// topicSignal counts how many of the person's recurring topics match the page
// title or aliases. Each match contributes one unit, capped so a page with a
// generic title cannot accumulate unbounded topic credit.
func topicSignal(p types.WikiPage, topicTerms []string) float64 {
	if len(topicTerms) == 0 {
		return 0
	}
	haystacks := []string{normalizeTerm(p.Title)}
	for _, a := range p.Aliases {
		haystacks = append(haystacks, normalizeTerm(a))
	}
	matches := 0
	seen := map[string]bool{}
	for _, term := range topicTerms {
		if seen[term] {
			continue
		}
		for _, hay := range haystacks {
			if hay != "" && strings.Contains(hay, term) {
				matches++
				seen[term] = true
				break
			}
		}
		if matches >= 3 {
			break
		}
	}
	return float64(matches)
}

func normalizeTerm(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// recommend picks frontier nodes (untouched but adjacent to lit pages) and
// ranks them. Every recommendation carries a reason built from its lit
// neighbors: guidance you cannot explain is guidance nobody follows.
func recommend(nodes []types.LearningNode, limit int) []types.LearningRecommendation {
	type cand struct {
		node types.LearningNode
		score float64
	}
	cands := make([]cand, 0)
	for _, n := range nodes {
		if n.State != types.LearningStateFrontier {
			continue
		}
		neighbors := n.InLinks + n.OutLinks
		litRatio := 0.0
		if neighbors > 0 {
			litRatio = float64(n.LitIn+n.LitOut) / float64(neighbors)
		}
		importance := 0.0
		if total := n.InLinks + n.OutLinks; total > 0 {
			importance = math.Min(1, float64(n.InLinks)/10)
		}
		score := 0.6*litRatio + 0.4*importance
		cands = append(cands, cand{node: n, score: score})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].node.Slug < cands[j].node.Slug
	})
	recs := make([]types.LearningRecommendation, 0, limit)
	for _, c := range cands {
		if len(recs) >= limit {
			break
		}
		recs = append(recs, types.LearningRecommendation{
			Slug:     c.node.Slug,
			Title:    c.node.Title,
			Mastery:  c.node.Mastery,
			Score:    round3(c.score),
			LitCount: c.node.LitIn + c.node.LitOut,
			Reason:   "与已掌握内容相邻，建议下一步了解",
		})
	}
	return recs
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
