package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// evaluationRepository 实现评测任务的持久化访问。
type evaluationRepository struct {
	db *gorm.DB
}

// NewEvaluationRepository 创建评测任务仓储。
func NewEvaluationRepository(db *gorm.DB) interfaces.EvaluationRepository {
	return &evaluationRepository{db: db}
}

// Create 新建一条评测任务记录。
func (r *evaluationRepository) Create(ctx context.Context, record *types.EvaluationTaskRecord) error {
	return r.db.WithContext(ctx).Create(record).Error
}

// Update 全量更新一条评测任务记录（包含零值字段，如 status=0）。
func (r *evaluationRepository) Update(ctx context.Context, record *types.EvaluationTaskRecord) error {
	return r.db.WithContext(ctx).
		Model(&types.EvaluationTaskRecord{}).
		Where("id = ? AND tenant_id = ?", record.ID, record.TenantID).
		Select("*").
		Updates(record).Error
}

// GetByID 按任务 ID 查询评测任务记录（限定租户）。
func (r *evaluationRepository) GetByID(
	ctx context.Context, tenantID uint64, taskID string,
) (*types.EvaluationTaskRecord, error) {
	var record types.EvaluationTaskRecord
	if err := r.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", taskID, tenantID).
		First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &record, nil
}

// List 列出某租户下的所有评测任务，按创建时间倒序。
func (r *evaluationRepository) List(
	ctx context.Context, tenantID uint64,
) ([]*types.EvaluationTaskRecord, error) {
	var records []*types.EvaluationTaskRecord
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}
