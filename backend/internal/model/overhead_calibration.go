package model

import "time"

// OverheadCalibration Token 开销校准记录
// 用于记录每次 LLM 调用时的估算开销 vs 实际开销，
// 通过历史数据校准后续任务的开销估算，使预算计算更加精准。
type OverheadCalibration struct {
	ID                uint64    `gorm:"primaryKey" json:"id"`
	TaskID            uint      `gorm:"index" json:"task_id"`
	ExecutionID       uint      `gorm:"index" json:"execution_id"`
	StageCode         string    `gorm:"size:50" json:"stage_code"`
	RuleCount         int       `json:"rule_count"`
	EstimatedOverhead int       `json:"estimated_overhead"`
	ActualOverhead    int       `json:"actual_overhead"`
	TotalInputTokens  int       `json:"total_input_tokens"`
	DiffTokens        int       `json:"diff_tokens"`
	CreatedAt         time.Time `json:"created_at"`
}

// TableName 返回表名
func (OverheadCalibration) TableName() string {
	return "overhead_calibrations"
}
