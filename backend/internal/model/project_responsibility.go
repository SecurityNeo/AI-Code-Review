package model

import "time"

// ProjectResponsibility 项目职责分配
// 融合原 ProjectSteward，以人为核心，项目维度为属性
type ProjectResponsibility struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	MemberID       uint      `gorm:"not null;index:idx_member_project;uniqueIndex:uk_member_project_scope,priority:1" json:"member_id"`
	ProjectID      uint      `gorm:"not null;index:idx_project_scope;uniqueIndex:uk_member_project_scope,priority:2" json:"project_id"`
	ScopeType      string    `gorm:"size:32;not null;default:'default';uniqueIndex:uk_member_project_scope,priority:3" json:"scope_type"`  // default / rule_category / language
	ScopeValue     string    `gorm:"size:64;default:'';uniqueIndex:uk_member_project_scope,priority:4" json:"scope_value"`               // "" / "security" / "golang"
	Priority       int       `gorm:"default:0" json:"priority"`                                                                          // 同项目同维度下优先级，越小越优先
	NotifyChannels string    `gorm:"type:json" json:"notify_channels"`                                                                   // ["wecom","email"]
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// 关联（GORM Preload）
	Member  TeamMember `gorm:"foreignKey:MemberID;references:ID" json:"member,omitempty"`
	Project Project    `gorm:"foreignKey:ProjectID;references:ID" json:"project,omitempty"`
}
