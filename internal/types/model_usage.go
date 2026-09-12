package types

import "time"

// TableName matches both PostgreSQL and SQLite usage-ledger migrations.
func (ModelUsageRecord) TableName() string { return "model_usages" }

// ModelUsageRecord 记录一次模型调用（chat/embedding/rerank/vlm）的结构化账本行。
// 它是「模型成本可观测」的原子单元：只存 token 量与缓存命中信息，
// 费用由读取端按模型定价（ModelPricing）在查询时计算，避免价格变动导致历史金额失真。
type ModelUsageRecord struct {
	ID          string `json:"id"          gorm:"primaryKey;type:varchar(64)"`
	TenantID    uint64 `json:"tenant_id"   gorm:"index:idx_model_usages_query"`
	ModelID     string `json:"model_id"    gorm:"type:varchar(64);index:idx_model_usages_query"`
	ModelType   string `json:"model_type"  gorm:"type:varchar(32)"` // chat / embedding / rerank / vlm
	Purpose     string `json:"purpose"     gorm:"type:varchar(64)"` // knowledge_qa / document_summary / ...
	SessionID   string `json:"session_id"  gorm:"type:varchar(64);default:''"`
	MessageID   string `json:"message_id"  gorm:"type:varchar(64);default:''"`
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(64);default:''"`

	PromptTokens     int64 `json:"prompt_tokens"`     // 输入 token（含缓存命中部分）
	CompletionTokens int64 `json:"completion_tokens"` // 输出 token
	TotalTokens      int64 `json:"total_tokens"`      // 总 token

	CachedTokens     int64  `json:"cached_tokens"`                               // 缓存命中输入 token（兼容别名）
	CacheReadTokens  int64  `json:"cache_read_tokens"`                           // 读缓存命中的 token
	CacheWriteTokens int64  `json:"cache_write_tokens"`                          // 写缓存 token
	CacheMissTokens  int64  `json:"cache_miss_tokens"`                           // 缓存未命中 token
	CacheStatus      string `json:"cache_status"        gorm:"type:varchar(32)"` // hit / miss / unreported

	DurationMs int64     `json:"duration_ms"` // 调用耗时（毫秒）
	CreatedAt  time.Time `json:"created_at"`
}

// ModelUsageAggregate 是按「模型」聚合后的调用量/命中率/费用汇总，
// 供模型页成本视图直接展示。
type ModelUsageAggregate struct {
	ModelID          string  `json:"model_id"`
	ModelName        string  `json:"model_name"`
	ModelType        string  `json:"model_type"`
	CallCount        int64   `json:"call_count"`        // 调用次数
	PromptTokens     int64   `json:"prompt_tokens"`     // 累计输入 token
	CompletionTokens int64   `json:"completion_tokens"` // 累计输出 token
	TotalTokens      int64   `json:"total_tokens"`      // 累计总 token
	CachedTokens     int64   `json:"cached_tokens"`     // 累计缓存命中 token
	CacheHitRate     float64 `json:"cache_hit_rate"`    // 缓存命中率 = cached/prompt（0~1）
	Cost             float64 `json:"cost"`              // 按当前定价计算的费用
	Currency         string  `json:"currency"`          // 货币单位
}

// ComputeCost 按给定定价计算该聚合的费用（单位：货币）。
// 费用口径：
//
//	(prompt - cached)/1M × input + cached/1M × cached(空则回落 input) + completion/1M × output
//
// pricing 为 nil 时返回 0。
func (a *ModelUsageAggregate) ComputeCost(pricing *ModelPricing) {
	a.Cost = 0
	a.Currency = ""
	if pricing == nil {
		return
	}
	a.Currency = pricing.Currency
	inputRate := pricing.InputPerMillion
	cachedRate := pricing.CachedInputPerMillion
	if cachedRate == 0 {
		cachedRate = inputRate
	}
	uncachedPrompt := a.PromptTokens - a.CachedTokens
	if uncachedPrompt < 0 {
		uncachedPrompt = 0
	}
	a.Cost = float64(uncachedPrompt)/1e6*inputRate +
		float64(a.CachedTokens)/1e6*cachedRate +
		float64(a.CompletionTokens)/1e6*pricing.OutputPerMillion
}
