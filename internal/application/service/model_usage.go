package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// modelUsageService 实现模型成本可观测（M2）的应用层逻辑：
// 读取账本聚合结果，关联每个模型的定价，产出模型页成本视图所需的数据。
type modelUsageService struct {
	usageRepo interfaces.ModelUsageRepository
	modelRepo interfaces.ModelRepository
}

// NewModelUsageService 创建模型成本可观测服务。
func NewModelUsageService(
	usageRepo interfaces.ModelUsageRepository,
	modelRepo interfaces.ModelRepository,
) interfaces.ModelUsageService {
	return &modelUsageService{usageRepo: usageRepo, modelRepo: modelRepo}
}

// GetOverview 返回当前租户在 [start, end] 时间区间内按模型聚合的
// 调用量、缓存命中率与费用。费用按模型当前定价即时计算，不固化在账本里。
func (s *modelUsageService) GetOverview(
	ctx context.Context,
	start, end time.Time,
) ([]*types.ModelUsageAggregate, error) {
	tenantID := types.MustTenantIDFromContext(ctx)

	// 拉取该租户全部模型，建立 modelID -> model 映射，用于补全模型名与定价。
	models, err := s.modelRepo.List(ctx, tenantID, "", "")
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"tenant_id": tenantID})
		return nil, err
	}
	byID := make(map[string]*types.Model, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}

	rows, err := s.usageRepo.Aggregate(ctx, tenantID, nil, start, end)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"tenant_id": tenantID})
		return nil, err
	}

	for _, row := range rows {
		if m, ok := byID[row.ModelID]; ok {
			row.ModelName = m.Name
			if m.Parameters.Pricing != nil {
				row.ComputeCost(m.Parameters.Pricing)
			}
		}
	}

	logger.Infof(ctx, "模型成本概览：tenant=%d, 模型数=%d", tenantID, len(rows))
	return rows, nil
}
