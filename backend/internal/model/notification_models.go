package model

import "time"

// Notification 站内信/通知中心
type Notification struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	UserID        uint      `gorm:"index:idx_user_unread, priority:1;not null" json:"user_id"`
	Type          string    `gorm:"size:32;not null" json:"type"` // task_completed / issue_escalation / deadline_digest / daily_digest / auto_archived / batch_alert
	Title         string    `gorm:"size:200;not null" json:"title"`
	Content       string    `gorm:"type:text;not null" json:"content"`
	Link          string    `gorm:"size:512" json:"link"`
	RelatedTaskID *uint     `gorm:"index" json:"related_task_id"`
	IsRead        bool      `gorm:"default:false;index:idx_user_unread, priority:2" json:"is_read"`
	CreatedAt     time.Time `gorm:"index:idx_user_unread, priority:3" json:"created_at"`
	ReadAt        *time.Time `json:"read_at"`
}

// NotificationDeliveryLog IM 投递日志
type NotificationDeliveryLog struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	NotifierID     *uint     `json:"notifier_id"`
	TaskID         *uint     `gorm:"index" json:"task_id"`
	Channel        string    `gorm:"size:32;not null;default:'wecom'" json:"channel"` // wecom / dingtalk / lark
	RecipientType  string    `gorm:"size:32" json:"recipient_type"`                     // mr_author / steward / admin
	MessagePreview string    `gorm:"size:500" json:"message_preview"`
	Status         string    `gorm:"size:20;not null;index" json:"status"`              // success / failed
	ErrorMsg       string    `gorm:"size:512" json:"error_msg"`
	CreatedAt      time.Time `json:"created_at"`
}

// ProjectSteward 项目环节负责人（按规则类型/语言维度）
type ProjectSteward struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ProjectID uint      `gorm:"not null;index:idx_project_role;uniqueIndex:uk_project_role_user,priority:1" json:"project_id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uk_project_role_user,priority:2" json:"user_id"`
	RoleType  string    `gorm:"size:32;not null;default:'default';index:idx_project_role;uniqueIndex:uk_project_role_user,priority:3" json:"role_type"` // default / security / performance / golang / java / ...
	Priority  int       `gorm:"default:0" json:"priority"` // 同类型下优先级，越小越优先
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Holiday 节假日配置
type Holiday struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	Date     string    `gorm:"size:10;not null;uniqueIndex" json:"date"` // YYYY-MM-DD
	Name     string    `gorm:"size:100" json:"name"`
	IsWorkday bool     `gorm:"default:false" json:"is_workday"`          // 如果为 true，表示调休上班的周末
}

const (
	IssueStatusPending          = "pending"
	IssueStatusPendingInherited = "pending_inherited" // 历史 resolved 问题复现
	IssueStatusResolved         = "resolved"         // 用户确认已修复（原 accepted）
	IssueStatusFalsePositive    = "false_positive"   // 用户判定为误报（原 rejected）
	IssueStatusIgnored          = "ignored"          // 用户主动忽略（原 dismissed）
	IssueStatusAutoFiltered     = "auto_filtered"    // 系统自动过滤（历史误报/忽略继承）
	IssueStatusAutoArchived     = "auto_archived"    // 超期自动归档
)

const (
	NotificationTypeTaskCompleted      = "task_completed"
	NotificationTypeIssueEscalation    = "issue_escalation"
	NotificationTypeDeadlineDigest     = "deadline_digest"
	NotificationTypeDailyDigest        = "daily_digest"
	NotificationTypeWeeklyDigest       = "weekly_digest"
	NotificationTypeAutoArchived       = "auto_archived"
	NotificationTypeBatchAlert         = "batch_alert"
)
