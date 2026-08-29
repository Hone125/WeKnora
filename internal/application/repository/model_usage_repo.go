package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// modelUsageRepository 实现模型调用账本的持久化访问。
type modelUsageRepository struct {
	db *gorm.DB
}

// NewModelUsageRepository 创建模型调用账本仓储。
func NewModelUsageRepository(db *gorm.DB) interfaces.ModelUsageRepository {
	return &modelUsageRepository{db: db}
}

// Record 追加一条模型调用记录。
func (r *modelUsageRepository) Record(ctx context.Context, record *types.ModelUsageRecord) error {
	if record.ID == "" {
		record.ID = uuid.New().String()
	}
	return r.db.WithContext(ctx).Create(record).Error
}

// Aggregate 按模型聚合某租户在时间区间内的调用账本。
// 返回每模型的调用次数、token 量与缓存命中 token 量；费用由上层按定价计算。
func (r *modelUsageRepository) Aggregate(
	ctx context.Context,
	tenantID uint64,
	modelIDs []string,
	start, end time.Time,
) ([]*types.ModelUsageAggregate, error) {
	query := r.db.WithContext(ctx).
		Model(&types.ModelUsageRecord{}).
		Select(
			"model_id, model_type, "+
				"COUNT(*) AS call_count, "+
				"COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, "+
				"COALESCE(SUM(completion_tokens), 0) AS completion_tokens, "+
				"COALESCE(SUM(total_tokens), 0) AS total_tokens, "+
				"COALESCE(SUM(cached_tokens), 0) AS cached_tokens",
		).
		Where("tenant_id = ?", tenantID)

	if len(modelIDs) > 0 {
		query = query.Where("model_id IN ?", modelIDs)
	}
	if !start.IsZero() {
		query = query.Where("created_at >= ?", start)
	}
	if !end.IsZero() {
		query = query.Where("created_at <= ?", end)
	}

	query = query.Group("model_id, model_type").Order("call_count DESC")

	var rows []*types.ModelUsageAggregate
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	// 计算缓存命中率（cached/prompt，0~1）。
	for _, r := range rows {
		if r.PromptTokens > 0 {
			r.CacheHitRate = float64(r.CachedTokens) / float64(r.PromptTokens)
		}
	}
	return rows, nil
}
