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

// Holiday 节假日配置
type Holiday struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Date        string    `gorm:"size:10;not null;uniqueIndex:idx_date_type" json:"date"` // YYYY-MM-DD
	Name        string    `gorm:"size:100" json:"name"`
	Type        string    `gorm:"size:20;default:'national';uniqueIndex:idx_date_type" json:"type"` // national / company / makeup
	IsRecurring bool      `gorm:"default:false" json:"is_recurring"`                                // 是否每年重复（如国庆、元旦）
	Year        int       `gorm:"default:0" json:"year"`                                            // 所属年份，0 表示每年重复
	IsWorkday   bool      `gorm:"default:false" json:"is_workday"`                                  // true=调休上班（补班）
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const (
	HolidayTypeNational = "national"
	HolidayTypeCompany  = "company"
	HolidayTypeMakeup   = "makeup"
)

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
	NotificationTypeTaskCompleted        = "task_completed"
	NotificationTypeTaskCompletedNoIssue = "task_completed_no_issue"
	NotificationTypeIssueEscalation      = "issue_escalation"
	NotificationTypeDeadlineDigest       = "deadline_digest"
	NotificationTypeDailyDigest          = "daily_digest"
	NotificationTypeWeeklyDigest         = "weekly_digest"
	NotificationTypeAutoArchived         = "auto_archived"
	NotificationTypeBatchAlert           = "batch_alert"
)

// NotificationRule 通知规则模板（后台管理）
type NotificationRule struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	Name               string    `gorm:"size:100;not null" json:"name"`
	Trigger            string    `gorm:"size:50;not null" json:"trigger"` // task.completed / issue.escalation / daily.digest 等
	Provider           string    `gorm:"size:32;default:'wecom'" json:"provider"`          // 供应商：wecom / dingtalk / lark
	NotifierID         *uint     `gorm:"index" json:"notifier_id"`                       // 关联的通知机器人/配置ID
	Condition          string    `gorm:"type:json" json:"condition"`                     // JSON 条件
	Actions            string    `gorm:"type:json" json:"actions"`                       // JSON 动作列表
	Template           string    `gorm:"type:text" json:"template"`                      // 消息模板（Markdown）
	DelayMinutes       int       `gorm:"default:0" json:"delay_minutes"`
	QuietHoursStart    string    `gorm:"size:5;default:'22:00'" json:"quiet_hours_start"`  // 如 "22:00"
	QuietHoursEnd      string    `gorm:"size:5;default:'09:00'" json:"quiet_hours_end"`    // 如 "09:00"
	DedupWindowMinutes int       `gorm:"default:0" json:"dedup_window_minutes"`            // 0=不合并
	EscalationConfig   string    `gorm:"type:json" json:"escalation_config"`               // 升级策略 JSON（阶段/阈值小时/收件人）
	Enabled            bool      `gorm:"default:true" json:"enabled"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// NotificationGlobalSetting 通知规则全局生效起点（单行表，id=1）
type NotificationGlobalSetting struct {
	ID                     uint       `gorm:"primaryKey" json:"id"`
	NotificationBaselineAt *time.Time `json:"notification_baseline_at"`                          // 生效起点：此时间之前的 Issue 不参与通知规则
	BaselineSetBy          uint       `json:"baseline_set_by"`                                    // 最后一次设置者 user_id
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}
