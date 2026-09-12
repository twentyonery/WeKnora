package learning

import (
	"math"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestDecayMonotonic(t *testing.T) {
	// Older events must contribute strictly less; a future event clamps to 1.
	cases := []struct {
		age  time.Duration
		want float64
	}{
		{-time.Hour, 1},
		{0, 1},
		{halfLife, 0.5},
		{2 * halfLife, 0.25},
		{4 * halfLife, 0.0625},
	}
	for _, c := range cases {
		got := decay(c.age)
		if math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("decay(%v) = %v, want %v", c.age, got, c.want)
		}
	}
}

func TestAdaptiveTau(t *testing.T) {
	// Empty signal keeps the default so early profiles are not pinned at 0.
	if got := adaptiveTau([]float64{0, 0, 0}); got != 1 {
		t.Fatalf("adaptiveTau(all zero) = %v, want 1", got)
	}
	// P75 of positive scores drives the constant; busy quarter saturates.
	raws := []float64{0, 1, 1, 1, 4}
	got := adaptiveTau(raws)
	if got < 1 || got > 4 {
		t.Fatalf("adaptiveTau = %v, expected P75 of positives (between 1 and 4)", got)
	}
	// Floor prevents a tiny burst from saturating the whole KB.
	if got := adaptiveTau([]float64{0.01}); got != 0.25 {
		t.Fatalf("adaptiveTau floor = %v, want 0.25", got)
	}
}

func TestMasteryBoundedAndMonotonic(t *testing.T) {
	tau := 2.0
	prev := -1.0
	for raw := 0.0; raw <= 20; raw += 0.5 {
		m := 1 - math.Exp(-raw/tau)
		if m < 0 || m > 1 {
			t.Fatalf("mastery %v out of [0,1] at raw %v", m, raw)
		}
		if m < prev {
			t.Fatalf("mastery decreased at raw %v", raw)
		}
		prev = m
	}
	// raw = τ lands near 0.63 per the design formula.
	if m := 1 - math.Exp(-tau/tau); math.Abs(m-0.632) > 0.001 {
		t.Fatalf("mastery(raw=tau) = %v, want ~0.632", m)
	}
}

func TestStateBands(t *testing.T) {
	if stateOf(0.7, 0) != types.LearningStateLit {
		t.Fatal("0.7 must be lit")
	}
	if stateOf(0.4, 0) != types.LearningStateDim {
		t.Fatal("0.4 must be dim")
	}
	if stateOf(0.05, 0) != types.LearningStateDark {
		t.Fatal("0.05 without neighbors must be dark")
	}
	if stateOf(0.05, 2) != types.LearningStateFrontier {
		t.Fatal("0.05 with lit neighbors must be frontier")
	}
}

func TestTopicSignal(t *testing.T) {
	page := types.WikiPage{
		Title:   "Retrieval-Augmented Generation",
		Aliases: types.StringArray{"RAG", "rag 检索增强"},
	}
	terms := []string{"retrieval-augmented generation", "rag", "vector database"}
	got := topicSignal(page, terms)
	if got != 2 {
		t.Fatalf("topicSignal = %v, want 2 (title contains RAG term, alias exact, vector db unrelated)", got)
	}
	// Cap: at most 3 matches regardless of how many topics hit.
	many := []string{"retrieval", "augmented", "generation", "rag", "rag 检索增强"}
	if got := topicSignal(page, many); got > 3 {
		t.Fatalf("topicSignal = %v, must cap at 3", got)
	}
}

func TestRecommendPrefersFrontierAndExplains(t *testing.T) {
	nodes := []types.LearningNode{
		{Slug: "a/lit", State: types.LearningStateLit},
		{Slug: "b/dark", State: types.LearningStateDark},
		{Slug: "c/frontier", State: types.LearningStateFrontier, InLinks: 3, OutLinks: 1, LitIn: 2, LitOut: 1},
		{Slug: "d/frontier", State: types.LearningStateFrontier, InLinks: 2, OutLinks: 0, LitIn: 1, LitOut: 0},
	}
	recs := recommend(nodes, 5)
	if len(recs) != 2 {
		t.Fatalf("recommend returned %d recs, want 2 (frontier only)", len(recs))
	}
	if recs[0].Slug != "c/frontier" {
		t.Fatalf("top rec = %s, want c/frontier (higher lit ratio)", recs[0].Slug)
	}
	if recs[0].Reason == "" {
		t.Fatal("every recommendation must carry an explanation")
	}
	// Limit is respected.
	if recs := recommend(nodes, 1); len(recs) != 1 {
		t.Fatalf("limit not respected: %d", len(recs))
	}
}

func TestRound3(t *testing.T) {
	if got := round3(0.123456); got != 0.123 {
		t.Fatalf("round3 = %v", got)
	}
}
