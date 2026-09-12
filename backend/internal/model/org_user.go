package model

import (
	"time"

	"gorm.io/gorm"
)

// OrgUser 用户-组织-角色关联表（一个用户可属于多个组织）
type OrgUser struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	OrgID     uint           `gorm:"column:org_id;not null;index:idx_org_user,priority:1" json:"org_id"`
	UserID    uint           `gorm:"column:user_id;not null;index:idx_org_user,priority:2;index:idx_user_default" json:"user_id"`
	DeptID    uint           `gorm:"column:dept_id;default:0;index:idx_dept" json:"dept_id"`
	TeamID    uint           `gorm:"column:team_id;default:0;index:idx_team" json:"team_id"`
	Role      string         `gorm:"size:32;default:'developer'" json:"role"`
	IsDefault bool           `gorm:"column:is_default;default:false" json:"is_default"`
	Status    string         `gorm:"size:20;default:'active'" json:"status"` // active/inactive
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// OrgUserInvite 组织用户邀请表
// token 字段不序列化到 JSON
type OrgUserInvite struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	OrgID      uint       `gorm:"column:org_id;not null;index:idx_org_email,priority:1" json:"org_id"`
	Email      string     `gorm:"size:255;not null;index:idx_org_email,priority:2" json:"email"`
	Role       string     `gorm:"size:32;default:'developer'" json:"role"`
	InvitedBy  uint       `gorm:"column:invited_by;not null" json:"invited_by"`
	Token      string     `gorm:"size:255;uniqueIndex;not null" json:"-"` // 不暴露token
	ExpiresAt  time.Time  `gorm:"not null" json:"expires_at"`
	Status     string     `gorm:"size:20;default:'pending'" json:"status"` // pending/accepted/expired/revoked
	AcceptedAt *time.Time `json:"accepted_at"`
	AcceptedBy *uint      `json:"accepted_by"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TeamProject 团队-项目关联表（多对多）
type TeamProject struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	TeamID    uint      `gorm:"column:team_id;not null;uniqueIndex:idx_team_project,priority:1" json:"team_id"`
	ProjectID uint      `gorm:"column:project_id;not null;uniqueIndex:idx_team_project,priority:2;index:idx_project" json:"project_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ResourceQuota 组织资源配额表
type ResourceQuota struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	OrgID        uint      `gorm:"column:org_id;not null;uniqueIndex:idx_org_resource,priority:1" json:"org_id"`
	ResourceType string    `gorm:"column:resource_type;size:32;not null;uniqueIndex:idx_org_resource,priority:2" json:"resource_type"` // llm_tokens/parallel_tasks/projects/users
	LimitValue   int64     `gorm:"column:limit_value;default:0" json:"limit_value"`                                     // 0=无限制
	UsedValue    int64     `gorm:"column:used_value;default:0" json:"used_value"`
	PeriodType   string    `gorm:"column:period_type;size:20;default:'monthly'" json:"period_type"`                     // daily/weekly/monthly
	ResetAt      *time.Time `json:"reset_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// IsExpired 检查邀请是否过期
func (i *OrgUserInvite) IsExpired() bool {
	return time.Now().After(i.ExpiresAt)
}

// CanAccept 检查邀请是否可以被接受
func (i *OrgUserInvite) CanAccept() bool {
	return i.Status == "pending" && !i.IsExpired()
}

// ResetDailyResourceQuotas 重置所有组织的日周期配额（每日 00:00 执行）
func ResetDailyResourceQuotas(db *gorm.DB) error {
	return db.Model(&ResourceQuota{}).
		Where("period_type = ?", "daily").
		Updates(map[string]interface{}{
			"used_value": 0,
			"reset_at":   time.Now(),
		}).Error
}
