package model

import "time"

// ProjectResponsibility 项目职责分配
// user_id 为主访问键，member_id 已退役仅保留兼容
type ProjectResponsibility struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	OrgID          uint      `gorm:"column:org_id;not null;default:1;index:idx_org_id" json:"org_id"`
	MemberID       *uint     `gorm:"index:idx_member_project" json:"member_id,omitempty"` // 退役字段，nullable
	// ⚠️ uniqueIndex 不在 GORM tag 中定义，而是在 migration 脚本 Step 10 创建，
	//    避免 AutoMigrate 为现有数据（user_id 默认值 0）创建索引时因重复而失败。
	UserID         uint      `gorm:"not null;index:idx_user_project" json:"user_id"`
	ProjectID      uint      `gorm:"not null;index:idx_project_scope" json:"project_id"`
	ScopeType      string    `gorm:"size:32;not null;default:'default'" json:"scope_type"` // default / rule_category / language
	ScopeValue     string    `gorm:"size:64;default:''" json:"scope_value"`               // "" / "security" / "golang"
	Priority       int       `gorm:"default:0" json:"priority"`                          // 同项目同维度下优先级，越小越优先
	NotifyChannels string    `gorm:"type:json" json:"notify_channels"`                   // ["wecom","email"]
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// 关联（GORM Preload）
	Member  *TeamMember `gorm:"foreignKey:MemberID;references:ID" json:"member,omitempty"`
	User    User         `gorm:"foreignKey:UserID;references:ID" json:"user,omitempty"`
	Project Project      `gorm:"foreignKey:ProjectID;references:ID" json:"project,omitempty"`
}
