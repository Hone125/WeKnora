package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ModelUsageRepository 定义模型调用账本的持久化操作。
type ModelUsageRepository interface {
	// Record 追加一条模型调用记录（异步旁路，不阻塞调用主链）。
	Record(ctx context.Context, record *types.ModelUsageRecord) error
	// Aggregate 按模型聚合某租户在 [start, end] 时间区间内的调用账本，
	// 返回按模型分组的调用量/缓存命中/费用汇总（不计算费用，仅 token 量）。
	Aggregate(
		ctx context.Context,
		tenantID uint64,
		modelIDs []string,
		start, end time.Time,
	) ([]*types.ModelUsageAggregate, error)
}

// ModelUsageService 定义模型成本可观测（M2）的应用层能力：
// 把账本聚合结果与模型定价关联，产出可直接展示的费用视图。
type ModelUsageService interface {
	// GetOverview 返回当前租户在 [start, end] 时间区间内按模型聚合的
	// 调用量、缓存命中率与费用。start/end 为零值时表示不限边界。
	GetOverview(ctx context.Context, start, end time.Time) ([]*types.ModelUsageAggregate, error)
}
