package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// LearningRepository persists learning-navigator data. Personal reads and
// writes take a MemoryScope so tenant/subject isolation is enforced in one
// place, mirroring the memory repository.
type LearningRepository interface {
	ListWikiPages(ctx context.Context, tenantID uint64, kbID string) ([]types.WikiPage, error)
	ListDocAffinity(ctx context.Context, scope MemoryScope, kbID string) ([]types.MemoryDocAffinity, error)
	ListTopicStats(ctx context.Context, scope MemoryScope) ([]types.MemoryTopicStat, error)
	ListEvents(ctx context.Context, scope MemoryScope, kbID string) ([]types.LearningNodeEvent, error)
	AppendEvents(ctx context.Context, scope MemoryScope, events []types.LearningNodeEvent) error
	ListPageSourceIndex(ctx context.Context, tenantID uint64, kbID string) ([]types.LearningPageSourceIndex, error)
	ReplacePageSourceIndex(ctx context.Context, tenantID uint64, kbID string, rows []types.LearningPageSourceIndex) error
	UpsertStates(ctx context.Context, scope MemoryScope, states []types.LearningNodeState) error
	DeleteProfile(ctx context.Context, scope MemoryScope, kbID string) error
}

// LearningService computes and serves the per-person knowledge-network mastery
// profile, plus the guided next-step recommendations derived from it.
type LearningService interface {
	// Profile recomputes mastery for every published page in the KB and
	// returns the full profile with recommendations attached.
	Profile(ctx context.Context, kbID string) (*types.LearningProfile, error)
	// Recommend returns the top frontier nodes to learn next.
	Recommend(ctx context.Context, kbID string, limit int) ([]types.LearningRecommendation, error)
	// RecordPageView appends a view event for one page.
	RecordPageView(ctx context.Context, kbID, slug string) error
	// RecordAnswerExposure appends doc-signal events for the documents an
	// answer cited, mapped onto the pages that cite those documents.
	RecordAnswerExposure(ctx context.Context, refs []types.MemoryDocAffinity) error
	// DeleteProfile removes every learning row for the caller, optionally
	// scoped to one KB (empty kbID = all KBs in the workspace).
	DeleteProfile(ctx context.Context, kbID string) error
}
