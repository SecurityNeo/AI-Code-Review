package model

import "time"

// SecretScanRule 密钥扫描规则库
type SecretScanRule struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Code        string    `gorm:"size:64;uniqueIndex;not null" json:"code"` // 规则编码，如 SECRET-001
	Name        string    `gorm:"size:128;not null" json:"name"`            // 规则名称
	Category    string    `gorm:"size:32;not null" json:"category"`         // 分类: aws / github / generic / database / private_key
	Severity    string    `gorm:"size:16;not null" json:"severity"`         // critical / high / medium
	Pattern     string    `gorm:"type:text;not null" json:"pattern"`        // 正则表达式（RE2 语法）
	Keywords    string    `gorm:"type:text" json:"keywords"`                // 关键词触发（逗号分隔，预过滤用）
	Description string    `gorm:"type:text" json:"description"`             // 规则描述
	IsBuiltin   bool      `gorm:"default:true;not null" json:"is_builtin"`  // 是否系统内置
	Enabled     bool      `gorm:"default:true;not null" json:"enabled"`     // 是否启用
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (SecretScanRule) TableName() string {
	return "secret_scan_rules"
}

// SecretScanFinding 密钥扫描发现记录
type SecretScanFinding struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	TaskID      uint      `gorm:"index;not null" json:"task_id"`
	RuleCode    string    `gorm:"size:64;not null" json:"rule_code"`
	FilePath    string    `gorm:"size:512;not null" json:"file_path"`
	LineNumber  int       `json:"line_number"`
	MatchText   string    `gorm:"type:text" json:"match_text"` // 匹配到的原始文本（脱敏存储）
	Severity    string    `gorm:"size:16" json:"severity"`
	Description string    `gorm:"type:text" json:"description"`      // 规则描述（快照）
	IsConfirmed bool      `gorm:"default:false" json:"is_confirmed"` // 管理员是否已确认（白名单功能预留）
	CreatedAt   time.Time `json:"created_at"`
}

func (SecretScanFinding) TableName() string {
	return "secret_scan_findings"
}
