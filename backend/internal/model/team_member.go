package model

import "time"

// TeamMember 团队成员（人员管理主表）
// 融合原 MemberMapping + User 的人员维度信息，一人可负责多个项目
type TeamMember struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Username       string    `gorm:"size:100;not null" json:"username"`                          // 系统用户名
	DisplayName    string    `gorm:"size:100" json:"display_name"`                               // 展示名
	GitlabUsername string    `gorm:"size:100;uniqueIndex:uk_gitlab_username" json:"gitlab_username"` // GitLab 用户名（MR 提交者匹配键）
	IMPlatform     string    `gorm:"size:20;default:'wecom'" json:"im_platform"`                 // IM 平台
	IMUserID       string    `gorm:"size:100" json:"im_user_id"`                                 // IM 用户 ID（用于 @mention）
	Email          string    `gorm:"size:255" json:"email"`                                      // 邮箱
	Role           string    `gorm:"size:50;default:'member'" json:"role"`                       // 全局角色（admin/member），独立管理
	Enabled        bool      `gorm:"default:true" json:"enabled"`                                // 是否启用
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// 关联
	Responsibilities []ProjectResponsibility `gorm:"foreignKey:MemberID" json:"responsibilities,omitempty"`
}
