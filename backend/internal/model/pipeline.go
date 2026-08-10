package model

import "time"

// ReviewPipelineStage 评审链路阶段定义（预置数据，由 migration 初始化）
type ReviewPipelineStage struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Code        string    `gorm:"uniqueIndex;size:32" json:"code"`
	Name        string    `gorm:"size:64" json:"name"`
	Description string    `gorm:"size:256" json:"description"`
	Icon        string    `gorm:"size:32" json:"icon"`            // FontAwesome class
	IsGroup     bool      `gorm:"default:false" json:"is_group"`  // 是否分组阶段（含子阶段）
	IsAsync     bool      `gorm:"default:false" json:"is_async"`  // 是否支持并行
	TimeoutSec  int       `gorm:"default:300" json:"timeout_sec"`
	SortOrder   int       `json:"sort_order"`
	CreatedAt   time.Time `json:"created_at"`
}

// TaskPipelineExecution 任务 Pipeline 阶段执行实例
type TaskPipelineExecution struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
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
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`

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
