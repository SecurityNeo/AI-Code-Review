package model

import (
	"time"

	"github.com/ai-optimizer/backend/pkg/encrypt"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Organization 组织/租户表（树形结构，支持层级）
type Organization struct {
	ID                    uint           `gorm:"primaryKey" json:"id"`
	ParentID              uint           `gorm:"column:parent_id;default:0;index" json:"parent_id"`
	Code                  string         `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name                  string         `gorm:"size:255;not null" json:"name"`
	Type                  string         `gorm:"size:20;default:'enterprise'" json:"type"` // enterprise/saas_client/internal
	Status                string         `gorm:"size:20;default:'active'" json:"status"`   // active/suspended/archived
	MaxProjects           int            `gorm:"default:100" json:"max_projects"`
	MaxUsers              int            `gorm:"default:50" json:"max_users"`
	MaxDepartments        int            `gorm:"default:10" json:"max_departments"`
	MaxTeams              int            `gorm:"default:30" json:"max_teams"`
	MaxLLMTokensPerDay    int64          `gorm:"default:0" json:"max_llm_tokens_per_day"`
	MaxParallelTasks      int            `gorm:"default:20" json:"max_parallel_tasks"`
	DefaultPoolID         *uint          `json:"default_pool_id"`
	DefaultModelID        *uint          `json:"default_model_id"`
	DefaultTemplateID     *uint          `json:"default_template_id"`
	DefaultGitLabInstanceID *uint        `json:"default_gitlab_instance_id"`
	BillingPlan           string         `gorm:"size:32;default:'internal'" json:"billing_plan"`
	BillingExpireAt       *time.Time     `json:"billing_expire_at"`
	BillingCycleAnchor    *time.Time     `json:"billing_cycle_anchor"`
	Timezone              string         `gorm:"size:64;default:'Asia/Shanghai'" json:"timezone"`
	Locale                string         `gorm:"size:10;default:'zh-CN'" json:"locale"`
	DataRegion            string         `gorm:"size:32;default:'cn'" json:"data_region"`
	LogRetentionDays      int            `gorm:"default:365" json:"log_retention_days"`
	DataRetentionDays     int            `gorm:"default:90" json:"data_retention_days"`
	Version               int            `gorm:"default:1" json:"version"` // 乐观锁版本号
	CreatedBy             uint           `gorm:"default:0" json:"created_by"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	DeletedAt             gorm.DeletedAt `gorm:"index" json:"-"`
}

// OrgTreeNode 组织树形节点（API 返回用）
type OrgTreeNode struct {
	Organization
	MemberCount int64         `json:"member_count"`
	LLMUsed     int64         `json:"llm_used"`
	TokenUsed   int64         `json:"token_used"`
	Level       int           `json:"level"`
	Children    []OrgTreeNode `json:"children,omitempty"`
}

// BuildOrgTree 将扁平组织列表构建为树
func BuildOrgTree(orgs []Organization, counts map[uint]int64, level int) []OrgTreeNode {
	// parent_id -> children
	childrenMap := make(map[uint][]Organization)
	for _, o := range orgs {
		childrenMap[o.ParentID] = append(childrenMap[o.ParentID], o)
	}
	var build func(parentID uint, depth int) []OrgTreeNode
	build = func(parentID uint, depth int) []OrgTreeNode {
		var result []OrgTreeNode
		for _, o := range childrenMap[parentID] {
			node := OrgTreeNode{
				Organization: o,
				MemberCount:  counts[o.ID],
				Level:        depth,
			}
			node.Children = build(o.ID, depth+1)
			result = append(result, node)
		}
		return result
	}
	return build(0, level)
}

// Department 部门/事业部表
type Department struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	OrgID       uint           `gorm:"column:org_id;not null;index:idx_org_code,priority:1" json:"org_id"`
	ParentID    uint           `gorm:"column:parent_id;default:0" json:"parent_id"`
	Code        string         `gorm:"size:64;not null;index:idx_org_code,priority:2" json:"code"`
	Name        string         `gorm:"size:255;not null" json:"name"`
	Description string         `gorm:"size:512" json:"description"`
	ManagerID   *uint          `json:"manager_id"`
	SortOrder   int            `gorm:"default:0" json:"sort_order"`
	Status      string         `gorm:"size:20;default:'active'" json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// Team 团队/小组表
type Team struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	OrgID       uint           `gorm:"column:org_id;not null;index:idx_org_code,priority:1" json:"org_id"`
	DeptID      uint           `gorm:"column:dept_id;not null;index:idx_dept" json:"dept_id"`
	Code        string         `gorm:"size:64;not null;index:idx_org_code,priority:2" json:"code"`
	Name        string         `gorm:"size:255;not null" json:"name"`
	Description string         `gorm:"size:512" json:"description"`
	LeaderID    *uint          `json:"leader_id"`
	Status      string         `gorm:"size:20;default:'active'" json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// GitLabInstance GitLab 多实例配置表
// api_token_enc 为加密密文，必须通过 SetAPIToken/GetAPIToken 访问
type GitLabInstance struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	OrgID               uint       `gorm:"column:org_id;default:1;index:idx_org_name,priority:1" json:"org_id"`
	Name                string     `gorm:"size:100;not null;index:idx_org_name,priority:2" json:"name"`
	BaseURL             string     `gorm:"column:base_url;size:512;not null" json:"base_url"`
	APITokenEnc         string     `gorm:"column:api_token_enc;size:1024;not null" json:"-"`
	WebhookSecret       string     `gorm:"column:webhook_secret;size:255" json:"-"`
	IsDefault           bool       `gorm:"column:is_default;default:false" json:"is_default"`
	Status              string     `gorm:"size:20;default:'active'" json:"status"`
	// OAuth 配置（多实例独立配置）
	OAuthEnabled        bool       `gorm:"column:oauth_enabled;default:false" json:"oauth_enabled"`
	OAuthClientID       string     `gorm:"column:oauth_client_id;size:255" json:"oauth_client_id"`
	OAuthClientSecret   string     `gorm:"column:oauth_client_secret;size:255" json:"-"`
	OAuthRedirectURI    string     `gorm:"column:oauth_redirect_uri;size:512" json:"oauth_redirect_uri"`
	OAuthAutoCreateUser bool       `gorm:"column:oauth_auto_create_user;default:true" json:"oauth_auto_create_user"`
	OAuthSkipVerify     bool       `gorm:"column:oauth_skip_verify;default:false" json:"oauth_skip_verify"`
	// 元数据
	LastSyncAt          *time.Time `json:"last_sync_at"`
	SyncError           string     `gorm:"column:sync_error;size:512" json:"sync_error"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// SetAPIToken 加密并设置 GitLab Admin Token（入库前必须调用）
func (inst *GitLabInstance) SetAPIToken(plain string) error {
	enc, err := encrypt.Encrypt(plain)
	if err != nil {
		return err
	}
	inst.APITokenEnc = enc
	return nil
}

// GetAPIToken 解密获取 GitLab Admin Token
func (inst *GitLabInstance) GetAPIToken() (string, error) {
	return encrypt.Decrypt(inst.APITokenEnc)
}

// GitLabUserBinding 用户-GitLab 多实例绑定表
type GitLabUserBinding struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UserID           uint      `gorm:"column:user_id;not null;index:idx_user" json:"user_id"`
	GitLabInstanceID uint      `gorm:"column:gitlab_instance_id;not null;index:idx_instance_gitlab_user,priority:1" json:"gitlab_instance_id"`
	GitLabUserID     uint      `gorm:"column:gitlab_user_id;not null;index:idx_instance_gitlab_user,priority:2" json:"gitlab_user_id"`
	GitLabUsername   string    `gorm:"column:gitlab_username;size:100" json:"gitlab_username"`
	GitLabEmail      string    `gorm:"column:gitlab_email;size:255" json:"gitlab_email"`
	GitLabAvatarURL  string    `gorm:"column:gitlab_avatar_url;size:512" json:"gitlab_avatar_url"`
	IsPrimary        bool      `gorm:"column:is_primary;default:false" json:"is_primary"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// BatchOrgNames 根据组织 ID 列表批量查询组织名称，返回 id→name 映射
func BatchOrgNames(orgIDs []uint) map[uint]string {
	result := make(map[uint]string)
	if len(orgIDs) == 0 {
		return result
	}
	// 去重
	seen := make(map[uint]struct{}, len(orgIDs))
	unique := make([]uint, 0, len(orgIDs))
	for _, id := range orgIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return result
	}
	var orgs []Organization
	if err := DB.Select("id, name").Where("id IN ?", unique).Find(&orgs).Error; err != nil {
		zap.L().Warn("BatchOrgNames query failed", zap.Error(err))
		return result
	}
	for _, o := range orgs {
		result[o.ID] = o.Name
	}
	return result
}
