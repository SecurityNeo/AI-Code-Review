package model

import (
	"encoding/json"
	"time"
)

// ReviewPipelineStage 评审链路阶段定义（预置数据，由 migration 初始化）
type ReviewPipelineStage struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Code           string    `gorm:"uniqueIndex;size:32" json:"code"`
	Name           string    `gorm:"size:64" json:"name"`
	Description    string    `gorm:"size:256" json:"description"`
	Icon           string    `gorm:"size:32" json:"icon"`           // FontAwesome class
	IsGroup        bool      `gorm:"default:false" json:"is_group"` // 是否分组阶段（含子阶段）
	IsAsync        bool      `gorm:"default:false" json:"is_async"` // 是否支持并行
	TimeoutSec     int       `gorm:"default:300" json:"timeout_sec"`
	SortOrder      int       `json:"sort_order"`
	IsMandatory    bool      `gorm:"default:false" json:"is_mandatory"`   // 是否不可禁用
	AllowedToSkip  bool      `gorm:"default:true" json:"allowed_to_skip"` // 允许在依赖失败时跳过
	RequiredStages string    `gorm:"type:text" json:"required_stages"`    // JSON []string，依赖的阶段编码
	CreatedAt      time.Time `json:"created_at"`
}

// TaskPipelineExecution 任务 Pipeline 阶段执行实例
type TaskPipelineExecution struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	OrgID           uint       `gorm:"column:org_id;not null;default:1;index:idx_org_id" json:"org_id"`
	TaskID          uint       `gorm:"index" json:"task_id"`
	StageCode       string     `gorm:"size:32;index" json:"stage_code"`
	Status          string     `gorm:"size:16;default:'pending'" json:"status"` // pending / running / success / failed / timeout / skipped
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	DurationMs      int        `json:"duration_ms"`
	InputSnapshot   string     `gorm:"type:text" json:"input_snapshot"`
	OutputSnapshot  string     `gorm:"type:text" json:"output_snapshot"`
	ErrorMessage    string     `gorm:"type:text" json:"error_message"`
	Degraded        bool       `gorm:"default:false" json:"degraded"`
	DegradedReason  string     `gorm:"size:256" json:"degraded_reason"`
    InputTokens     int        `json:"input_tokens"`
    OutputTokens    int        `json:"output_tokens"`
    ModelName       string     `gorm:"size:64" json:"model_name"`       // 实际使用模型名称
    LLMModelID      *uint      `gorm:"index" json:"llm_model_id"`
	BatchTotal      int        `json:"batch_total"`
	BatchCompleted  int        `json:"batch_completed"`
	BatchIndex      int        `json:"batch_index"` // 子批次序号，0 表示非批次
	ParentID        *uint      `gorm:"index" json:"parent_id"`
	SortOrder       int        `json:"sort_order"`

	// Token 预算相关字段（批次评审专用）
	EstimatedOverhead int    `json:"estimated_overhead"`      // 估算系统开销token
	ActualOverhead    int    `json:"actual_overhead"`         // 实际系统开销token
	LLMInputBudget    int    `json:"llm_input_budget"`        // 用户配置的LLM输入预算
	EffectiveBudget   int    `json:"effective_budget"`        // 自动扩大后的实际预算
	BudgetWarning     string `gorm:"size:255" json:"budget_warning"` // 预算警告信息

	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	// 关联
	Task     *Task                    `gorm:"foreignKey:TaskID" json:"task,omitempty"`
	Model    *LLMModel                `gorm:"foreignKey:LLMModelID;references:ID" json:"model,omitempty"`
	Parent   *TaskPipelineExecution   `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
	Children []TaskPipelineExecution  `gorm:"foreignKey:ParentID" json:"children,omitempty"`
}

// PipelineStageStatus 阶段状态常量
const (
	PipelineStagePending  = "pending"
	PipelineStageRunning  = "running"
	PipelineStageSuccess  = "success"
	PipelineStageFailed   = "failed"
	PipelineStageTimeout  = "timeout"
	PipelineStageSkipped  = "skipped"
)

// GetRequiredStages 解析 RequiredStages JSON 为字符串数组
func (s *ReviewPipelineStage) GetRequiredStages() []string {
	if s.RequiredStages == "" {
		return nil
	}
	var stages []string
	_ = json.Unmarshal([]byte(s.RequiredStages), &stages)
	return stages
}

// SetRequiredStages 将字符串数组序列化为 RequiredStages JSON
func (s *ReviewPipelineStage) SetRequiredStages(stages []string) {
	if len(stages) == 0 {
		s.RequiredStages = ""
		return
	}
	b, _ := json.Marshal(stages)
	s.RequiredStages = string(b)
}
