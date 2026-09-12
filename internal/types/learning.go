package types

import (
	"time"
)

// Learning signal types name the observable behavior a mastery score is built
// from. The values are stored in learning_node_events.signal_type.
const (
	// LearningSignalDoc records that an answer of this person drew on a
	// document that cites the node's wiki page. It is the same event that
	// feeds memory_doc_affinity, folded into the learning log so mastery can
	// decay per event instead of per counter.
	LearningSignalDoc = "doc"
	// LearningSignalView records that this person opened the wiki page.
	LearningSignalView = "view"
	// LearningSignalTopic marks that one of the person's recurring interest
	// topics lexically matches the node. Derived at recompute time; kept as a
	// constant so the states table stays self-describing.
	LearningSignalTopic = "topic"
)

// Learning node states. frontier is the "next best thing to learn" band: the
// node is essentially untouched but sits next to lit neighbors.
const (
	LearningStateLit      = "lit"
	LearningStateDim      = "dim"
	LearningStateFrontier = "frontier"
	LearningStateDark     = "dark"
)

// Mastery bands. A node lights up at 0.6, counts as "in progress" from 0.2.
const (
	LearningMasteryLit = 0.6
	LearningMasteryDim = 0.2
)

// LearningNodeEvent is one observed exposure between a person and a wiki page.
type LearningNodeEvent struct {
	ID              string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	TenantID        uint64    `json:"tenant_id" gorm:"not null;index:idx_learning_events_scope,priority:1"`
	SubjectID       string    `json:"subject_id" gorm:"type:varchar(512);not null;index:idx_learning_events_scope,priority:2"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index:idx_learning_events_scope,priority:3"`
	Slug            string    `json:"slug" gorm:"type:varchar(255);not null;index:idx_learning_events_scope,priority:4"`
	SignalType      string    `json:"signal_type" gorm:"type:varchar(16);not null"`
	Weight          float64   `json:"weight" gorm:"not null;default:1"`
	OccurredAt      time.Time `json:"occurred_at"`
	CreatedAt       time.Time `json:"created_at"`
}

func (LearningNodeEvent) TableName() string { return "learning_node_events" }

// LearningNodeState is the derived mastery snapshot for one (person, page).
type LearningNodeState struct {
	ID              string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	TenantID        uint64    `json:"tenant_id" gorm:"not null;uniqueIndex:uq_learning_state,priority:1"`
	SubjectID       string    `json:"subject_id" gorm:"type:varchar(512);not null;uniqueIndex:uq_learning_state,priority:2"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_learning_state,priority:3"`
	Slug            string    `json:"slug" gorm:"type:varchar(255);not null;uniqueIndex:uq_learning_state,priority:4"`
	RawScore        float64   `json:"raw_score" gorm:"not null;default:0"`
	Mastery         float64   `json:"mastery" gorm:"not null;default:0"`
	State           string    `json:"state" gorm:"type:varchar(16);not null;default:'dark'"`
	SignalDoc       float64   `json:"signal_doc" gorm:"not null;default:0"`
	SignalTopic     float64   `json:"signal_topic" gorm:"not null;default:0"`
	SignalView      float64   `json:"signal_view" gorm:"not null;default:0"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (LearningNodeState) TableName() string { return "learning_node_states" }

// LearningPageSourceIndex maps a source document to the wiki pages citing it,
// so document-level familiarity can be attributed to pages. ref_weight splits
// one document's signal across every page that cites it.
type LearningPageSourceIndex struct {
	ID              string  `json:"id" gorm:"primaryKey;type:varchar(36)"`
	TenantID        uint64  `json:"tenant_id" gorm:"not null;index:idx_learning_page_source_kb,priority:1"`
	KnowledgeBaseID string  `json:"knowledge_base_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_learning_page_source,priority:1"`
	KnowledgeID     string  `json:"knowledge_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_learning_page_source,priority:2"`
	Slug            string  `json:"slug" gorm:"type:varchar(255);not null;uniqueIndex:uq_learning_page_source,priority:3"`
	RefWeight       float64 `json:"ref_weight" gorm:"not null;default:1"`
}

func (LearningPageSourceIndex) TableName() string { return "learning_page_source_index" }

// LearningNode is the API projection of one node's mastery, joined with the
// page title so the frontend can render it without a second round trip.
type LearningNode struct {
	Slug      string  `json:"slug"`
	Title     string  `json:"title"`
	PageType  string  `json:"page_type"`
	Mastery   float64 `json:"mastery"`
	State     string  `json:"state"`
	InLinks   int     `json:"in_links"`
	OutLinks  int     `json:"out_links"`
	LitIn     int     `json:"lit_in"`
	LitOut    int     `json:"lit_out"`
	Signals   map[string]float64 `json:"signals"`
}

// LearningRecommendation is one guided next step with a human-readable reason.
type LearningRecommendation struct {
	Slug     string  `json:"slug"`
	Title    string  `json:"title"`
	Mastery  float64 `json:"mastery"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason"`
	LitCount int     `json:"lit_neighbors"`
}

// LearningProfile is the full per-person view of one knowledge base.
type LearningProfile struct {
	KnowledgeBaseID string                 `json:"knowledge_base_id"`
	Nodes           []LearningNode         `json:"nodes"`
	Recommendations []LearningRecommendation `json:"recommendations"`
	ComputedAt      time.Time              `json:"computed_at"`
}
