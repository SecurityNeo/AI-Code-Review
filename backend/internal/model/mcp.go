package model

import (
	"time"

	"gorm.io/gorm"
)

// MCPAPIKey MCP 接入密钥
// 从共享模式升级为绑定模式（Phase 1 双轨运行）：
//   - 新 Key 必须绑定 UserID（user_id > 0），调用时直接用绑定用户身份，无需再传 X-IM-* Header
//   - 旧 Key user_id = 0 继续兼容共享模式，调用时仍需通过 X-IM-Provider + X-IM-User-ID 传入身份
type MCPAPIKey struct {
	ID           uint           `gorm:"primaryKey" json:"id"`
	Name         string         `gorm:"size:100;not null" json:"name"`
	Prefix       string         `gorm:"size:20;not null" json:"prefix"`        // 密钥前缀（如 sk-cg-wecom）
	KeyHash      string         `gorm:"size:255;not null" json:"-"`            // 密钥哈希（bcrypt），不返回给前端
	Scopes       string         `gorm:"size:512;not null" json:"scopes"`       // JSON 数组，如 ["tasks:read","tasks:write"]
	IPWhitelist  string         `gorm:"size:512" json:"ip_whitelist"`          // 逗号分隔的 IP 列表，空表示不限制
	RateLimit    int            `gorm:"not null;default:100" json:"rate_limit"` // 请求/分钟
	Status       string         `gorm:"size:20;not null;default:active" json:"status"` // active / disabled / deprecated
	UserID       uint           `gorm:"index;default:0" json:"user_id"`        // 绑定用户（0 表示旧共享模式）
	LastUsedAt   *time.Time     `json:"last_used_at"`
	ExpiresAt    *time.Time     `json:"expires_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"deleted_at"`
}

// MCPCallLog MCP 调用审计日志
type MCPCallLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	APIKeyID     uint      `gorm:"index" json:"api_key_id"`
	APIKeyName   string    `gorm:"size:100" json:"api_key_name"`
	IMUserID     string    `gorm:"size:100;index" json:"im_user_id"`
	IMProvider   string    `gorm:"size:20" json:"im_provider"`
	TeamMemberID uint      `gorm:"index" json:"team_member_id"`
	UserID       uint      `gorm:"index" json:"user_id"`
	Method       string    `gorm:"size:50" json:"method"`      // tools/call, tools/list, initialize, etc.
	ToolName     string    `gorm:"size:100" json:"tool_name"`  // 调用的 Tool 名称
	Params       string    `gorm:"type:text" json:"params"`    // 请求参数 JSON
	Status       string    `gorm:"size:20;index" json:"status"` // success / error
	StatusCode   int       `json:"status_code"`
	ErrorMsg     string    `gorm:"type:text" json:"error_msg"`
	DurationMs   int64     `json:"duration_ms"`
	ResponseSize int       `json:"response_size"`
	ClientIP     string    `gorm:"size:45" json:"client_ip"`
	CreatedAt    time.Time `json:"created_at"`
}

// MCPTool MCP 能力中心展示用的工具元数据
type MCPTool struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ToolName       string    `gorm:"size:64;uniqueIndex;not null" json:"tool_name"`
	DisplayName    string    `gorm:"size:64;not null" json:"display_name"`
	Description    string    `gorm:"type:text;not null" json:"description"`
	Category       string    `gorm:"size:16;not null" json:"category"`
	Icon           string    `gorm:"size:32" json:"icon"`
	Color          string    `gorm:"size:16" json:"color"`
	Tags           string    `gorm:"type:text" json:"tags"`
	Params         string    `gorm:"type:text" json:"params"`
	Phrases        string    `gorm:"type:text" json:"phrases"`
	Requires       string    `gorm:"type:text" json:"requires"`
	ResponseExample string   `gorm:"type:text" json:"response_example"`
	Dangerous      bool      `gorm:"default:false" json:"dangerous"`
	SortOrder      int       `gorm:"default:0" json:"sort_order"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
