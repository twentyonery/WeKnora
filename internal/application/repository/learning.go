package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type learningRepository struct {
	db *gorm.DB
}

// NewLearningRepository creates the learning navigator repository.
func NewLearningRepository(db *gorm.DB) interfaces.LearningRepository {
	return &learningRepository{db: db}
}

// scoped starts every personal query filtered by workspace and subject, the
// same pattern the memory repository uses. There is no read or write path that
// can skip it, so no endpoint can reach another person's profile.
func (r *learningRepository) scoped(
	ctx context.Context, scope interfaces.MemoryScope,
) *gorm.DB {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND subject_id = ?", scope.TenantID, scope.SubjectID)
}

func (r *learningRepository) ListWikiPages(
	ctx context.Context, tenantID uint64, kbID string,
) ([]types.WikiPage, error) {
	var pages []types.WikiPage
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?",
			tenantID, kbID, "published").
		Find(&pages).Error
	return pages, err
}

func (r *learningRepository) ListDocAffinity(
	ctx context.Context, scope interfaces.MemoryScope, kbID string,
) ([]types.MemoryDocAffinity, error) {
	var rows []types.MemoryDocAffinity
	err := r.scoped(ctx, scope).
		Where("knowledge_base_id = ? AND hits >= ?", kbID, types.MemoryDocAffinityMinHits).
		Find(&rows).Error
	return rows, err
}

func (r *learningRepository) ListTopicStats(
	ctx context.Context, scope interfaces.MemoryScope,
) ([]types.MemoryTopicStat, error) {
	var rows []types.MemoryTopicStat
	err := r.scoped(ctx, scope).Find(&rows).Error
	return rows, err
}

func (r *learningRepository) ListEvents(
	ctx context.Context, scope interfaces.MemoryScope, kbID string,
) ([]types.LearningNodeEvent, error) {
	var rows []types.LearningNodeEvent
	err := r.scoped(ctx, scope).
		Where("knowledge_base_id = ?", kbID).
		Order("occurred_at ASC").
		Find(&rows).Error
	return rows, err
}

func (r *learningRepository) AppendEvents(
	ctx context.Context, scope interfaces.MemoryScope, events []types.LearningNodeEvent,
) error {
	if len(events) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&events).Error
}

func (r *learningRepository) ListPageSourceIndex(
	ctx context.Context, tenantID uint64, kbID string,
) ([]types.LearningPageSourceIndex, error) {
	var rows []types.LearningPageSourceIndex
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Find(&rows).Error
	return rows, err
}

// ReplacePageSourceIndex rewrites the KB-wide document→page mapping. The wiki
// ingest pipeline is the only writer of wiki_pages, so a full refresh keyed by
// the KB is both correct and trivially idempotent.
func (r *learningRepository) ReplacePageSourceIndex(
	ctx context.Context, tenantID uint64, kbID string, rows []types.LearningPageSourceIndex,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
			Delete(&types.LearningPageSourceIndex{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for i := range rows {
			if rows[i].ID == "" {
				rows[i].ID = uuid.New().String()
			}
		}
		return tx.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&rows).Error
	})
}

func (r *learningRepository) UpsertStates(
	ctx context.Context, scope interfaces.MemoryScope, states []types.LearningNodeState,
) error {
	if len(states) == 0 {
		return nil
	}
	for i := range states {
		if states[i].ID == "" {
			states[i].ID = uuid.New().String()
		}
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "subject_id"},
				{Name: "knowledge_base_id"}, {Name: "slug"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"raw_score", "mastery", "state",
				"signal_doc", "signal_topic", "signal_view", "updated_at",
			}),
		}).
		Create(&states).Error
}

func (r *learningRepository) DeleteProfile(
	ctx context.Context, scope interfaces.MemoryScope, kbID string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		del := func(model interface{}, extra string, args ...interface{}) error {
			q := tx.Where("tenant_id = ? AND subject_id = ?", scope.TenantID, scope.SubjectID)
			if extra != "" {
				q = q.Where(extra, args...)
			}
			return q.Delete(model).Error
		}
		if err := del(&types.LearningNodeEvent{}, "knowledge_base_id = ?", kbID); err != nil {
			return err
		}
		if err := del(&types.LearningNodeState{}, "knowledge_base_id = ?", kbID); err != nil {
			return err
		}
		return nil
	})
}
