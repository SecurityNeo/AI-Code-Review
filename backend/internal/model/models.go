package model

import (
	"time"

	"gorm.io/gorm"
)

// --- Project ---

type Project struct {
	ID                 uint            `gorm:"primaryKey" json:"id"`
	Name               string          `gorm:"size:255;not null" json:"name"`
	ProjectPath        string          `gorm:"size:255;uniqueIndex" json:"project_path"`
	GitLabProjectID    int             `gorm:"column:gitlab_project_id" json:"gitlab_project_id"`
	TemplateID         uint            `gorm:"index" json:"template_id"`
	PoolID             uint            `gorm:"index" json:"pool_id"`
	DefaultModelID     *uint           `gorm:"index" json:"default_model_id"` // NULL = 未指定，不触发review任务;
	AIEnabled          bool            `gorm:"default:false" json:"ai_enabled"`
	Source             string          `gorm:"size:20;default:'manual'" json:"source"`
	Language           string          `gorm:"size:32;default:'golang'" json:"language"` // 项目主要编程语言
	AccessToken        string          `gorm:"size:500" json:"access_token"`
	LastSyncAt         *time.Time      `json:"last_sync_at"`
	SyncStatus         string          `gorm:"size:20;default:'success'" json:"sync_status"`
	SyncError          string          `gorm:"size:512" json:"sync_error"`
	GraphBaseBranch    string          `gorm:"size:255;not null;default:'main'" json:"graph_base_branch"`
	GraphBuiltAt       *time.Time      `json:"graph_built_at"`
	GraphNodeCount     int             `gorm:"not null;default:0" json:"graph_node_count"`
	GraphRelCount      int             `gorm:"not null;default:0" json:"graph_rel_count"`
	GraphEndpointCount int             `gorm:"not null;default:0" json:"graph_endpoint_count"`
	GraphFrameworks    string          `gorm:"size:512" json:"graph_frameworks"`                         // JSON数组存储
	GraphScanStatus    string          `gorm:"size:32;not null;default:'none'" json:"graph_scan_status"` // none | pending | running | completed | failed
	GraphLastBuildAt   *time.Time      `json:"graph_last_build_at"`
	GraphScanError     string          `gorm:"size:512" json:"graph_scan_error"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	DeletedAt          gorm.DeletedAt  `gorm:"index" json:"deleted_at"`
	Template           ProjectTemplate `gorm:"foreignKey:TemplateID;references:ID" json:"template,omitempty"`
	Pool               ResourcePool    `gorm:"foreignKey:PoolID;references:ID" json:"pool,omitempty"`
	Model              LLMModel        `gorm:"foreignKey:DefaultModelID;references:ID" json:"model,omitempty"`
	Tasks              []Task          `gorm:"foreignKey:ProjectID" json:"tasks,omitempty"`

	// 关联统计（非表字段，仅 API 展示使用）
	EnabledRuleCount  int `gorm:"-" json:"enabled_rule_count,omitempty"`
	TotalRuleCount    int `gorm:"-" json:"total_rule_count,omitempty"`
	PendingIssueCount int `gorm:"default:0" json:"pending_issue_count"` // 项目中待处理 Issue 的快照计数
}

type TaskStatus string

const (
	TaskPending TaskStatus = "pending"
	TaskRunning TaskStatus = "running"
	TaskSuccess TaskStatus = "success"
	TaskFailed  TaskStatus = "failed"
	TaskTimeout TaskStatus = "timeout"
	TaskStopped TaskStatus = "stopped"
)

type Task struct {
	ID                  uint                `gorm:"primaryKey" json:"id"`
	ProjectID           uint                `gorm:"index;not null" json:"project_id"`
	MRMergeID           int                 `json:"mr_iid"`
	MRAuthor            string              `gorm:"size:100" json:"author"`
	MRAuthorDisplayName string              `gorm:"size:100" json:"author_display_name"`
	MRTitle             string              `gorm:"size:512" json:"mr_title"`
	MRURL               string              `gorm:"size:512" json:"mr_url"`
	NoteID              int                 `json:"note_id"`
	TriggerType         string              `gorm:"size:20;default:'webhook'" json:"trigger_type"`
	TriggerSource       string              `gorm:"size:30;default:'manual'" json:"trigger_source"` // manual | score_threshold
	TaskType            string              `gorm:"size:20;default:'chat'" json:"task_type"`        // chat 或 bugfix
	Status              TaskStatus          `gorm:"size:20;index;default:'pending'" json:"status"`
	SourceBranch        string              `gorm:"size:100" json:"source_branch"`
	TargetBranch        string              `gorm:"size:100" json:"target_branch"`
	PoolID              uint                `json:"pool_id"`
	UsedModelID         uint                `gorm:"column:model_id" json:"model_id"` // 实际使用的LLM模型ID（review任务）
	GitlabTokenID       uint                `json:"gitlab_token_id"`
	StartedAt           *time.Time          `json:"started_at"`
	CompletedAt         *time.Time          `json:"completed_at"`
	DurationSec         int                 `gorm:"default:0" json:"duration_sec"`
	ErrorMsg            string              `gorm:"type:longtext" json:"error_msg"`
	OpencodeSessionID   string              `gorm:"size:128" json:"opencode_session_id"`
	DiffSummary         string              `gorm:"type:text" json:"diff_summary"`
	AIPrompt            string              `gorm:"type:longtext;column:ai_prompt" json:"ai_prompt"`
	AIResponse          string              `gorm:"type:longtext" json:"ai_response"`
	AIResponseJSON      string              `gorm:"type:json;column:ai_response_json" json:"ai_response_json"`   // 结构化评审原始 JSON
	DimensionScores     string              `gorm:"type:json;column:dimension_scores" json:"dimension_scores"`   // 各维度评分
	DiffFilesJSON       string              `gorm:"type:json;column:diff_files_json" json:"diff_files_json"`     // diff 文件列表 JSON（用于任务详情展示）
	DiffFilterStats     string              `gorm:"type:json;column:diff_filter_stats" json:"diff_filter_stats"` // diff 文件过滤统计 JSON
	IssueCount          int                 `gorm:"default:0;column:issue_count" json:"issue_count"`             // issue 总数
	RetryCount          int                 `gorm:"default:0" json:"retry_count"`
	ScoreValue          int                 `gorm:"default:0" json:"score_value"`                      // 评分值（后置校验后，可参考分）
	RawAIScore          int                 `gorm:"default:0;column:raw_ai_score" json:"raw_ai_score"` // LLM 原始评分（未校验）
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	ReviewComments      []TaskReviewComment `gorm:"foreignKey:TaskID" json:"review_comments,omitempty"`
	Project             Project             `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
	Pool                ResourcePool        `gorm:"foreignKey:PoolID" json:"pool,omitempty"`
	UsedModel           LLMModel            `gorm:"foreignKey:UsedModelID;references:ID" json:"used_model,omitempty"`
	PendingIssueCount   int                 `gorm:"default:0;column:pending_issue_count" json:"pending_issue_count"` // 待处理Issue数量（列表页由子查询填充）
	ExecutionCount      int                 `gorm:"default:0;column:execution_count" json:"execution_count"`         // 同一任务第几次运行（首次创建为0，首次保存后变为1）
}

// BeforeCreate GORM hook: 确保 JSON 字段有合法默认值
func (t *Task) BeforeCreate(tx *gorm.DB) error {
	if t.DiffFilesJSON == "" {
		t.DiffFilesJSON = "[]"
	}
	if t.DiffFilterStats == "" {
		t.DiffFilterStats = "{}"
	}
	if t.DimensionScores == "" {
		t.DimensionScores = "{}"
	}
	if t.AIResponseJSON == "" {
		t.AIResponseJSON = "{}"
	}
	return nil
}

// BeforeUpdate GORM hook: 防止空字符串写入 JSON 列导致 MySQL 报错
func (t *Task) BeforeUpdate(tx *gorm.DB) error {
	if t.DiffFilesJSON == "" {
		t.DiffFilesJSON = "[]"
	}
	if t.DiffFilterStats == "" {
		t.DiffFilterStats = "{}"
	}
	if t.DimensionScores == "" {
		t.DimensionScores = "{}"
	}
	if t.AIResponseJSON == "" {
		t.AIResponseJSON = "{}"
	}
	return nil
}

type ReviewCategory struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Code      string    `gorm:"uniqueIndex;size:32" json:"code"`
	Name      string    `gorm:"size:100" json:"name"`
	IsBuiltIn bool      `gorm:"default:false" json:"is_built_in"`
	SortOrder int       `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// --- ReviewRule --- 评审规则库

type ReviewRule struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Code        string    `gorm:"uniqueIndex;size:64" json:"code"`
	Name        string    `gorm:"size:100" json:"name"`
	Category    string    `gorm:"size:32" json:"category"`                  // security/performance/readability/maintainability/test_coverage
	Severity    string    `gorm:"size:16" json:"severity"`                  // critical/high/medium/low/info
	Language    string    `gorm:"size:32;default:'common'" json:"language"` // golang/python/frontend/java/common
	Description string    `gorm:"type:text" json:"description"`
	Prompt      string    `gorm:"type:text" json:"prompt"`
	SortOrder   int       `gorm:"default:0" json:"sort_order"`
	IsBuiltIn   bool      `gorm:"default:false" json:"is_built_in"` // 自定义规则默认false,内置规则由初始化脚本显式设为true
	IsEnabled   bool      `gorm:"default:true" json:"is_enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// --- ProjectReviewConfig --- 项目评审规则配置

type ProjectReviewConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ProjectID uint      `gorm:"index" json:"project_id"`
	RuleID    uint      `gorm:"index" json:"rule_id"`
	IsEnabled bool      `gorm:"default:true" json:"is_enabled"`
	Severity  string    `gorm:"size:16" json:"severity"` // NULL=使用规则默认
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// --- TaskReviewRule --- 任务实际使用的评审规则（含截断记录）

type TaskReviewRule struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	TaskID      uint      `gorm:"index;not null" json:"task_id"`
	RuleID      *uint     `gorm:"index" json:"rule_id"` // 指向 review_rules.id，NULL=规则已删除
	RuleCode    string    `gorm:"size:64;not null" json:"rule_code"`
	Name        string    `gorm:"size:100" json:"name"`
	Category    string    `gorm:"size:32" json:"category"`
	Severity    string    `gorm:"size:16" json:"severity"`
	SortOrder   int       `json:"sort_order"`                                      // 排序后位置
	WasSelected bool      `json:"was_selected"`                                    // true=传入Prompt，false=被截断
	IssueCount  int       `gorm:"default:0;column:issue_count" json:"issue_count"` // 本次命中Issue数
	CreatedAt   time.Time `json:"created_at"`
}

// --- ReviewIssue --- 结构化评审结果明细

type ReviewIssue struct {
	ID                   uint           `gorm:"primaryKey" json:"id"`
	TaskID               uint           `gorm:"index" json:"task_id"`
	MRID                 int            `gorm:"column:mr_id;index:idx_mr_fingerprint" json:"mr_id"` // 关联 MR IID
	RuleID               *uint          `gorm:"index" json:"rule_id"`                               // NULL=AI自主发现
	RuleCode             string         `gorm:"size:64;index:idx_rule_code_created,priority:1" json:"rule_code"`
	Category             string         `gorm:"size:32" json:"category"`
	Severity             string         `gorm:"size:16" json:"severity"`
	DeductScore          int            `gorm:"default:0;column:deduct_score" json:"deduct_score"` // 该 Issue 扣多少分
	File                 string         `gorm:"size:255" json:"file"`
	LineStart            int            `gorm:"default:0" json:"line_start"`
	LineEnd              int            `gorm:"default:0" json:"line_end"`
	CodeSnippet          string         `gorm:"type:text" json:"code_snippet"`
	Message              string         `gorm:"type:text" json:"message"`
	Suggestion           string         `gorm:"type:text" json:"suggestion"`
	Status               string         `gorm:"size:20;default:'pending'" json:"status"`             // pending / pending_inherited / resolved / false_positive / ignored / auto_filtered / auto_archived
	ResolvedBy           uint           `gorm:"index;default:0" json:"resolved_by"`                  // 操作人ID
	ResolvedAt           *time.Time     `json:"resolved_at"`                                         // 操作时间
	RejectReason         string         `gorm:"type:text;column:reject_reason" json:"reject_reason"` // 拒绝/不采纳原因
	GitlabDiscussionID   string         `gorm:"size:64;column:gitlab_discussion_id" json:"gitlab_discussion_id"`
	IsResolved           bool           `gorm:"default:false;column:is_resolved" json:"is_resolved"`
	Fingerprint          string         `gorm:"size:64;index:idx_mr_fingerprint" json:"fingerprint"` // 语义指纹
	InheritedFromIssueID *uint          `json:"inherited_from_issue_id"`                             // 继承自历史 Issue
	OwnerID              *uint          `gorm:"index" json:"owner_id"`                               // MR 提交者用户ID（外键至 users）
	CurrentOwnerID       *uint          `gorm:"index" json:"current_owner_id"`                       // 当前责任人（升级后可能变更）
	OriginalCreatedAt    *time.Time     `json:"original_created_at"`                                 // 首次发现时间（用于升级计时）
	EscalationLevel      int            `gorm:"default:0" json:"escalation_level"`                   // 当前升级层级
	CreatedAt            time.Time      `gorm:"index:idx_rule_code_created,priority:2" json:"created_at"`
	UpdatedAt            time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"-"`
}

// --- TaskReviewComment 任务人工复核意见 ---
type TaskReviewComment struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	TaskID     uint      `gorm:"index;not null" json:"task_id"`
	Content    string    `gorm:"type:text;not null" json:"content"`
	RetryRound int       `gorm:"default:1" json:"retry_round"`
	OperatorID uint      `gorm:"index" json:"operator_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type ProjectTemplate struct {
	ID                    uint      `gorm:"primaryKey" json:"id"`
	Name                  string    `gorm:"size:100;uniqueIndex;not null" json:"name"`
	Description           string    `gorm:"size:512" json:"description"`
	Prompt                string    `gorm:"type:text;not null" json:"prompt"`                                  // 旧版自定义 prompt（废弃兼容）
	CustomInstruction     string    `gorm:"type:text;column:custom_instruction" json:"custom_instruction"`     // 项目特殊说明
	DimensionWeights      string    `gorm:"type:json;column:dimension_weights" json:"dimension_weights"`       // 维度权重 JSON
	DeductScoreConfig     string    `gorm:"type:json;column:deduct_score_config" json:"deduct_score_config"`   // 扣分规则配置 JSON
	MaxRulesPerReview     int       `gorm:"default:5;column:max_rules_per_review" json:"max_rules_per_review"` // 每次评审最多规则数
	IsDefault             bool      `gorm:"default:false" json:"is_default"`
	GitLabCommentTemplate string    `gorm:"type:text;column:gitlab_comment_template" json:"gitlab_comment_template"` // GitLab 评论模板
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// --- ResourcePool ---

type PoolStatus string

const (
	PoolActive   PoolStatus = "active"
	PoolInactive PoolStatus = "inactive"
	PoolError    PoolStatus = "error"
)

type ResourcePool struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Name             string     `gorm:"size:100;uniqueIndex;not null" json:"name"`
	OpencodeEndpoint string     `gorm:"size:512;not null" json:"opencode_endpoint"`
	OpencodeUsername string     `gorm:"size:100" json:"opencode_username"`
	OpencodePassword string     `gorm:"size:512" json:"opencode_password"`
	OpencodeAPIKey   string     `gorm:"size:512" json:"opencode_api_key"`
	OpencodeVersion  string     `gorm:"size:50" json:"opencode_version"`
	MaxParallel      int        `gorm:"default:5" json:"max_parallel"`
	CheckIntervalSec int        `gorm:"default:5" json:"check_interval_sec"`
	Status           PoolStatus `gorm:"size:20;default:'active'" json:"status"`
	IsDefault        bool       `gorm:"default:false" json:"is_default"`
	LastCheckAt      *time.Time `json:"last_check_at"`
	StatusChangedAt  *time.Time `json:"status_changed_at"`
	LastAlertAt      *time.Time `json:"last_alert_at"`
	CheckError       string     `gorm:"type:text" json:"check_error"`
	ActiveJobs       int        `gorm:"default:0" json:"active_jobs"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// --- LLMModel ---

type ModelType string

const (
	ModelTypeLLM       ModelType = "llm"
	ModelTypeEmbedding ModelType = "embedding"
)

type LLMModel struct {
	ID                    uint       `gorm:"primaryKey" json:"id"`
	Provider              string     `gorm:"size:50;not null" json:"provider"`
	ModelID               string     `gorm:"size:100;not null" json:"model_id"`
	BaseURL               string     `gorm:"size:512;not null" json:"base_url"`
	APIKey                string     `gorm:"size:512;not null" json:"api_key"`
	MaxTokens             int        `gorm:"default:4096" json:"max_tokens"`
	TimeoutSec            int        `gorm:"default:120" json:"timeout_sec"`
	Temperature           float64    `gorm:"default:0.1" json:"temperature"`
	IsDefault             bool       `gorm:"default:false" json:"is_default"`
	IsPrimary             bool       `gorm:"default:false" json:"is_primary"`                                           // 是否为主模型（全局唯一）
	BackupOrder           int        `gorm:"default:0" json:"backup_order"`                                             // 备用顺序：0=非备用，1=第一备用，2=第二备用...
	InputPricePerMTokens  float64    `gorm:"default:0;column:input_price_per_mtokens" json:"input_price_per_mtokens"`   // 输入价格 USD / 百万 token；0=未知/不计入成本
	OutputPricePerMTokens float64    `gorm:"default:0;column:output_price_per_mtokens" json:"output_price_per_mtokens"` // 输出价格 USD / 百万 token
	CachedPricePerMTokens float64    `gorm:"default:0;column:cached_price_per_mtokens" json:"cached_price_per_mtokens"` // 缓存命中价格 USD / 百万 token
	CheckIntervalSec      int        `gorm:"default:5" json:"check_interval_sec"`
	ModelType             string     `gorm:"size:20;not null;default:'llm';index:idx_type_status" json:"model_type"`
	Status                string     `gorm:"size:20;default:'active'" json:"status"`
	CheckError            string     `gorm:"type:text" json:"check_error"`
	LastCheckAt           *time.Time `json:"last_check_at"`
	StatusChangedAt       *time.Time `json:"status_changed_at"`
	LastAlertAt           *time.Time `json:"last_alert_at"`
	LastTestAt            *time.Time `json:"last_test_at"`
	LastTestStatus        string     `gorm:"size:20;default:''" json:"last_test_status"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// --- WeComNotifier ---

type WeComNotifier struct {
	ID              uint   `gorm:"primaryKey"`
	Name            string `gorm:"size:100;not null"`
	WebhookUrl      string `gorm:"size:512;not null"` // Webhook URL（完整URL，明文存储）
	MessageTemplate string `gorm:"type:text"`         // 消息模版
	ProjectID       *uint  `gorm:"index"`
	Enabled         bool   `gorm:"default:false"`
	LastTestAt      *time.Time
	LastTestStatus  string `gorm:"size:20"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// --- OperationLog ---

type OperationLog struct {
	ID         uint   `gorm:"primaryKey"`
	OpType     string `gorm:"size:50;index;not null"`
	OpObject   string `gorm:"size:100"`
	OpObjectID uint
	OpUserID   uint      // 操作人ID
	OpResult   string    `gorm:"size:20"`
	ErrorMsg   string    `gorm:"size:512"`
	RequestIP  string    `gorm:"size:45"`
	CreatedAt  time.Time `gorm:"index"`
}

// --- MergeRequestReviewLog ---

type MergeRequestReviewLog struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	ProjectName       string     `gorm:"size:255;index" json:"project_name"`
	Author            string     `gorm:"size:100;index" json:"author"`
	AuthorDisplayName string     `gorm:"column:author_display_name;size:100" json:"author_display_name"`
	SourceBranch      string     `gorm:"column:source_branch;size:100" json:"source_branch"`
	TargetBranch      string     `gorm:"column:target_branch;size:100" json:"target_branch"`
	MRID              int        `gorm:"column:mr_id;index" json:"mr_id"`
	Score             float64    `gorm:"default:0" json:"score"`
	ScoreHistory      string     `gorm:"type:text" json:"score_history"`
	ReviewCount       int        `gorm:"default:0" json:"review_count"`
	Additions         int        `json:"additions"`
	Deletions         int        `json:"deletions"`
	URL               string     `gorm:"size:512;index" json:"url"`
	LastCommitID      string     `gorm:"column:last_commit_id;size:64" json:"last_commit_id"`
	MRTitle           string     `gorm:"column:mr_title;size:512" json:"mr_title"`
	MRState           string     `gorm:"column:mr_state;size:20" json:"mr_state"`
	IsDraft           bool       `gorm:"column:is_draft;default:false" json:"is_draft"`
	Commits           string     `gorm:"type:text" json:"commits"`
	MRCreatedAt       *time.Time `gorm:"column:mr_created_at" json:"mr_created_at"`
	SyncedAt          time.Time  `json:"synced_at"`
}

// --- SystemConfig ---

type SystemConfig struct {
	ID                      uint   `gorm:"primaryKey" json:"id"`
	GitlabToken             string `gorm:"size:255" json:"gitlab_token"`
	TaskTimeoutMin          int    `gorm:"default:120" json:"task_timeout_min"`
	SyncIntervalSec         int    `gorm:"default:60" json:"sync_interval_sec"`
	MRSyncIntervalSec       int    `gorm:"default:60" json:"mr_sync_interval_sec"`
	MaxParallelTask         int    `gorm:"default:20" json:"max_parallel_task"`
	LogRetentionDay         int    `gorm:"default:90" json:"log_retention_day"`
	AILogTemplate           string `gorm:"type:text" json:"ai_log_template"`
	ScoreThreshold          int    `gorm:"default:60" json:"score_threshold"`
	DeepReviewEnabled       bool   `gorm:"default:false" json:"deep_review_enabled"`
	ReviewTemplate          string `gorm:"type:text" json:"review_template"`
	DiffTruncationThreshold int    `gorm:"default:5000" json:"diff_truncation_threshold"`
	AlertDurationSec        int    `gorm:"default:300" json:"alert_duration_sec"`
	AlertCooldownSec        int    `gorm:"default:3600" json:"alert_cooldown_sec"`
	AlertNotifierID         uint   `gorm:"default:0" json:"alert_notifier_id"`
	AlertMentionUserIDs     string `gorm:"size:512" json:"alert_mention_user_ids"`

	// JSON 结构化输出重试配置
	JSONRetryMaxAttempts       int     `gorm:"default:3;column:json_retry_max_attempts" json:"json_retry_max_attempts"`
	JSONRetryInitialDelaySec   int     `gorm:"default:2;column:json_retry_initial_delay_sec" json:"json_retry_initial_delay_sec"`
	JSONRetryBackoffMultiplier float64 `gorm:"default:2.0;column:json_retry_backoff_multiplier" json:"json_retry_backoff_multiplier"`
	JSONRetryMaxDelaySec       int     `gorm:"default:30;column:json_retry_max_delay_sec" json:"json_retry_max_delay_sec"`
	JSONRetryFallbackStrategy  string  `gorm:"size:20;default:'regex';column:json_retry_fallback_strategy" json:"json_retry_fallback_strategy"`

	// LLM HTTP 调用重试配置（针对 502/503/504 与网络层瞬时错误）
	LLMRetryMaxAttempts          int     `gorm:"default:3;column:llm_retry_max_attempts" json:"llm_retry_max_attempts"`
	LLMRetryInitialDelayMs       int     `gorm:"default:1000;column:llm_retry_initial_delay_ms" json:"llm_retry_initial_delay_ms"`
	LLMRetryBackoffMultiplier    float64 `gorm:"default:2.0;column:llm_retry_backoff_multiplier" json:"llm_retry_backoff_multiplier"`
	LLMRetryMaxDelayMs           int     `gorm:"default:30000;column:llm_retry_max_delay_ms" json:"llm_retry_max_delay_ms"`
	DefaultDimensionWeights      string  `gorm:"type:json;column:default_dimension_weights" json:"default_dimension_weights"`
	DefaultGitLabCommentTemplate string  `gorm:"type:text;column:default_gitlab_comment_template" json:"default_gitlab_comment_template"`

	// Pipeline 与分批评审配置
	PipelineEnabled bool `gorm:"default:false;column:pipeline_enabled" json:"pipeline_enabled"`

	// 跨文件调用链分析配置
	CallChainDepth       int  `gorm:"default:1;column:call_chain_depth" json:"call_chain_depth"`                // 调用链分析深度（默认1层）
	CodeWorkspaceEnabled bool `gorm:"default:true;column:code_workspace_enabled" json:"code_workspace_enabled"` // 是否启用持久化代码工作区

	// GitLab OAuth 配置（从环境变量迁移到数据库动态配置）
	GitlabOAuthEnabled        bool   `gorm:"default:false;column:gitlab_oauth_enabled" json:"gitlab_oauth_enabled"`
	GitlabBaseURL             string `gorm:"size:512;column:gitlab_base_url" json:"gitlab_base_url"`
	GitlabOAuthClientID       string `gorm:"size:255;column:gitlab_oauth_client_id" json:"gitlab_oauth_client_id"`
	GitlabOAuthClientSecret   string `gorm:"size:255;column:gitlab_oauth_client_secret" json:"gitlab_oauth_client_secret"`
	GitlabOAuthRedirectURI    string `gorm:"size:512;column:gitlab_oauth_redirect_uri" json:"gitlab_oauth_redirect_uri"`
	GitlabOAuthAutoCreateUser bool   `gorm:"default:true;column:gitlab_oauth_auto_create_user" json:"gitlab_oauth_auto_create_user"`
	GitlabOAuthSkipVerify     bool   `gorm:"default:false;column:gitlab_oauth_skip_verify" json:"gitlab_oauth_skip_verify"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// --- Role Constants ---

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// --- User ---
type User struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Username       string    `gorm:"size:50;uniqueIndex;not null" json:"username"`
	DisplayName    string    `gorm:"size:100" json:"display_name"`
	Password       string    `gorm:"size:255" json:"-"`                         // GitLab 用户无本地密码
	Role           string    `gorm:"size:20;default:'user'" json:"role"`        // admin / user
	LoginType      string    `gorm:"size:20;default:'local'" json:"login_type"` // local / gitlab
	GitlabUserID   *uint64   `gorm:"index" json:"gitlab_user_id"`
	GitlabUsername string    `gorm:"size:100" json:"gitlab_username"`
	GitlabEmail    string    `gorm:"size:255" json:"gitlab_email"`
	IMPlatform     string    `gorm:"size:32" json:"im_platform"`
	IMUserID       string    `gorm:"size:128" json:"im_user_id"`
	Enabled        bool      `gorm:"default:true" json:"enabled"`
	AvatarURL      string    `gorm:"size:512" json:"avatar_url"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// --- Token ---
type Token struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Username  string    `gorm:"size:50" json:"username"`
	Token     string    `gorm:"size:255;index" json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// --- SMTPConfig ---

type SMTPConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Host      string    `gorm:"size:255;not null" json:"host"`
	Port      int       `gorm:"not null;default:587" json:"port"`
	Username  string    `gorm:"size:255" json:"username"`
	Password  string    `gorm:"size:512" json:"password"`
	FromEmail string    `gorm:"size:255;not null" json:"from_email"`
	FromName  string    `gorm:"size:100" json:"from_name"`
	UseTLS    bool      `gorm:"default:true" json:"use_tls"`
	IsDefault bool      `gorm:"default:true" json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// --- ReportConfig ---

type ReportConfig struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	ReportType      string    `gorm:"size:20;not null" json:"report_type"` // weekly / monthly
	Enabled         bool      `gorm:"default:false" json:"enabled"`        // 保留兼容
	GenerateEnabled bool      `gorm:"default:false;column:generate_enabled" json:"generate_enabled"`
	SendEnabled     bool      `gorm:"default:false;column:send_enabled" json:"send_enabled"`
	SendGroups      string    `gorm:"size:512" json:"send_groups"` // JSON 数组，空则发送给所有分组
	CronExpr        string    `gorm:"size:100" json:"cron_expr"`
	DataPeriodDays  int       `gorm:"default:7" json:"data_period_days"`
	SendHour        int       `gorm:"default:9" json:"send_hour"`
	SendMinute      int       `gorm:"default:0" json:"send_minute"`
	SendDayOfWeek   int       `gorm:"default:1" json:"send_day_of_week"`  // 0=Sunday, 1=Monday
	SendDayOfMonth  int       `gorm:"default:1" json:"send_day_of_month"` // 1-31
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// --- ReportRecipient ---

type ReportRecipient struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:100" json:"name"`
	Email     string    `gorm:"size:255;not null" json:"email"`
	GroupName string    `gorm:"size:100;default:'默认分组'" json:"group_name"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// --- ReportLog ---

type ReportLog struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ReportType  string    `gorm:"size:20" json:"report_type"`  // weekly / monthly
	TriggerType string    `gorm:"size:20" json:"trigger_type"` // auto / manual / preview
	Status      string    `gorm:"size:20" json:"status"`       // sent_success / sent_failed / generated_success / generated_failed
	Subject     string    `gorm:"size:255" json:"subject"`
	Recipients  string    `gorm:"type:text" json:"recipients"` // JSON 数组
	HtmlContent string    `gorm:"type:longtext" json:"-"`      // 存储完整 HTML（长文本）
	ErrorMsg    string    `gorm:"type:text" json:"error_msg"`
	SentAt      time.Time `json:"sent_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// --- LLMCallLog 每次 LLM 调用的记录（Token 用量监控）---

// CallType 调用类型枚举
const (
	CallTypeScore      = "score"       // AI 评分（ChatCompletion / Structured）
	CallTypeDeepReview = "deep_review" // 深度审查（OpenCode 接入后启用）
)

type LLMCallLog struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	TaskID           *uint     `gorm:"index:idx_task_call_type,priority:1" json:"task_id,omitempty"`
	ModelID          *uint     `gorm:"index:idx_model_created,priority:2" json:"model_id,omitempty"`
	Provider         string    `gorm:"size:32;not null;default:'';index" json:"provider"`
	ModelName        string    `gorm:"size:128;not null;default:'';index" json:"model_name"`
	CallType         string    `gorm:"size:32;not null;default:'';index:idx_call_type_created,priority:2" json:"call_type"`
	Caller           string    `gorm:"size:64;not null;default:'';index" json:"caller"`
	PromptTokens     int       `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int       `gorm:"not null;default:0" json:"completion_tokens"`
	CachedTokens     int       `gorm:"not null;default:0" json:"cached_tokens"`
	TotalTokens      int       `gorm:"not null;default:0" json:"total_tokens"`
	DurationMs       int       `gorm:"not null;default:0" json:"duration_ms"`
	Status           string    `gorm:"size:16;not null;default:'success';index:idx_status_created,priority:1" json:"status"`
	ErrorMsg         string    `gorm:"type:text" json:"error_msg,omitempty"`
	CostCents        int64     `gorm:"not null;default:0" json:"cost_cents"`
	CreatedAt        time.Time `gorm:"index:idx_call_type_created,priority:1;index:idx_model_created,priority:1;index:idx_status_created,priority:2;not null" json:"created_at"`
}

// --- IncubatorConfig --- Rule Incubator 全局配置（单例表）

type IncubatorConfig struct {
	ID                            uint      `gorm:"primaryKey" json:"id"`
	EmbeddingModelID              *uint     `gorm:"index" json:"embedding_model_id"`
	ClusterMinGroupSize           int       `gorm:"default:3" json:"cluster_min_group_size"`
	ClusterMaxGroupsPerRun        int       `gorm:"default:20" json:"cluster_max_groups_per_run"`
	ClusterTimeWindowDays         int       `gorm:"default:30" json:"cluster_time_window_days"`
	RetroMatchEnabled             bool      `gorm:"default:true" json:"retro_match_enabled"`
	RetroMatchConfidenceThreshold float64   `gorm:"default:0.6" json:"retro_match_confidence_threshold"`
	SimilarityPassThreshold       float64   `gorm:"default:0.5" json:"similarity_pass_threshold"`
	RetroMatchMaxDaysLookback     int       `gorm:"default:30" json:"retro_match_max_days_lookback"`
	HealthCheckEnabled            bool      `gorm:"default:true" json:"health_check_enabled"`
	HealthCheckIntervalDays       int       `gorm:"default:7" json:"health_check_interval_days"`
	VectorStoreType               string    `gorm:"size:20;default:'mysql'" json:"vector_store_type"`
	VectorStoreConfig             string    `gorm:"type:json" json:"vector_store_config"`
	CreatedAt                     time.Time `json:"created_at"`
	UpdatedAt                     time.Time `json:"updated_at"`
}

type RuleIncubation struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Status           string     `gorm:"size:20;default:'draft';index" json:"status"`
	Name             string     `gorm:"size:100" json:"name"`
	Code             string     `gorm:"size:128;uniqueIndex" json:"code"`
	Category         string     `gorm:"size:32" json:"category"`
	PipelineJobID    *uint      `gorm:"index" json:"pipeline_job_id"`
	Severity         string     `gorm:"size:16" json:"severity"`
	Language         string     `gorm:"size:32" json:"language"`
	Description      string     `gorm:"type:text" json:"description"`
	Prompt           string     `gorm:"type:text" json:"prompt"`
	SourceIssueIDs   string     `gorm:"type:json;column:source_issue_ids" json:"source_issue_ids"`
	SourceCount      int        `gorm:"default:0" json:"source_count"`
	SourceProjects   string     `gorm:"type:json;column:source_projects" json:"source_projects"`
	ConfidenceScore  float64    `gorm:"default:0" json:"confidence_score"`
	UserAcceptRate   float64    `gorm:"default:0;column:user_accept_rate" json:"user_accept_rate"`
	MergedIntoRuleID *uint      `gorm:"index" json:"merged_into_rule_id"`
	SimilarRules     string     `gorm:"type:json" json:"similar_rules"`
	SimilarPassed    bool       `gorm:"default:false" json:"similar_passed"`
	TestResults      string     `gorm:"type:json" json:"test_results"`
	PublishedRuleID  *uint      `gorm:"index" json:"published_rule_id"`
	CreatedBy        uint       `gorm:"index" json:"created_by"`
	ResolvedBy       uint       `json:"resolved_by"`
	ResolvedAt       *time.Time `json:"resolved_at"`
	ExpiredAt        *time.Time `json:"expired_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type RuleIncubationJob struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	JobType       string     `gorm:"size:32;index" json:"job_type"`
	Status        string     `gorm:"size:20;index" json:"status"`
	TimeRangeFrom *time.Time `json:"time_range_from"`
	TimeRangeTo   *time.Time `json:"time_range_to"`
	Params        string     `gorm:"type:json" json:"params"`
	ResultSummary string     `gorm:"type:json" json:"result_summary"`
	ErrorMsg      string     `gorm:"type:text" json:"error_msg"`
	CreatedBy     uint       `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at"`
}

type ReviewIssueRuleMatch struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	IssueID    uint       `gorm:"uniqueIndex:idx_issue_rule,priority:1" json:"issue_id"`
	RuleID     uint       `gorm:"uniqueIndex:idx_issue_rule,priority:2" json:"rule_id"`
	MatchType  string     `gorm:"size:20;default:'retroactive'" json:"match_type"`
	MatchedAt  time.Time  `json:"matched_at"`
	Confidence float64    `json:"confidence"`
	IsDisabled bool       `gorm:"default:false" json:"is_disabled"`
	DisabledBy uint       `json:"disabled_by"`
	DisabledAt *time.Time `json:"disabled_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type ReviewIssueVector struct {
	ID        uint   `gorm:"primaryKey"`
	IssueID   uint   `gorm:"uniqueIndex;not null"`
	ModelID   uint   `gorm:"index"`
	Vector    string `gorm:"type:json;not null"`
	Dimension int    `gorm:"default:1536"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ReviewRuleVector struct {
	ID        uint   `gorm:"primaryKey"`
	RuleID    uint   `gorm:"uniqueIndex;not null"`
	ModelID   uint   `gorm:"index"`
	Vector    string `gorm:"type:json;not null"`
	Dimension int    `gorm:"default:1536"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RuleIncubationVector struct {
	ID           uint   `gorm:"primaryKey"`
	IncubationID uint   `gorm:"uniqueIndex;not null"`
	ModelID      uint   `gorm:"index"`
	Vector       string `gorm:"type:json;not null"`
	Dimension    int    `gorm:"default:1536"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
