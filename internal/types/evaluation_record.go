package types

import "time"

// EvaluationTaskRecord 是评测任务的持久化模型，对应数据库表 evaluation_tasks。
//
// 与内存结构 EvaluationDetail 的对应关系：
//   - Task.*        → 展开成平铺字段（id/tenant_id/dataset_id/status/...）
//   - Params         → Params（JSONB，配置快照，保证"可复现"）
//   - Metric         → Metric（JSONB，检索 + 生成两类指标）
//   - Cost           → prompt/completion/total_tokens + latency_ms（成本 + 耗时两类结果）
//
// 成本/耗时字段拆成独立列（而非塞进 JSON），是为了后续 M2 的模型调用账本、
// 模型页按时间/模型聚合统计时可以直接 SQL 求和，不必解析 JSON。
type EvaluationTaskRecord struct {
	ID        string     `gorm:"column:id;primaryKey" json:"id"`
	TenantID  uint64     `gorm:"column:tenant_id;index" json:"tenant_id"`
	DatasetID string     `gorm:"column:dataset_id" json:"dataset_id"`
	Status    int        `gorm:"column:status" json:"status"` // 见 EvaluationStatue 枚举
	ErrMsg    string     `gorm:"column:err_msg" json:"err_msg,omitempty"`
	Total     int        `gorm:"column:total" json:"total"`
	Finished  int        `gorm:"column:finished" json:"finished"`
	StartTime time.Time  `gorm:"column:start_time" json:"start_time"`
	EndTime   *time.Time `gorm:"column:end_time" json:"end_time,omitempty"`

	Params JSON `gorm:"column:params;type:jsonb" json:"params"`
	Metric JSON `gorm:"column:metric;type:jsonb" json:"metric,omitempty"`

	PromptTokens     int64 `gorm:"column:prompt_tokens" json:"prompt_tokens"`
	CompletionTokens int64 `gorm:"column:completion_tokens" json:"completion_tokens"`
	TotalTokens      int64 `gorm:"column:total_tokens" json:"total_tokens"`
	LatencyMs        int64 `gorm:"column:latency_ms" json:"latency_ms"`

	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定持久化表名。
func (EvaluationTaskRecord) TableName() string { return "evaluation_tasks" }
