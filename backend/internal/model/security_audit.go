package model

import "time"

// SecurityAuditRule 安全审计规则库
type SecurityAuditRule struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Code         string    `gorm:"size:64;uniqueIndex;not null" json:"code"` // 规则编码，如 SA_SQL_INJECTION
	Name         string    `gorm:"size:128;not null" json:"name"`            // 规则名称
	Category     string    `gorm:"size:32;not null" json:"category"`         // 分类: sql_injection / unsafe_eval / deserialization / reflection / hardcoded_secret / path_traversal
	Severity     string    `gorm:"size:16;not null" json:"severity"`         // critical / high / medium / low
	Language     string    `gorm:"size:32" json:"language"`                  // 语言（空=通用，Go/Java/Python/JS）
	Mode         string    `gorm:"size:16;not null" json:"mode"`             // ast / regex
	ASTPattern   string    `gorm:"type:text" json:"ast_pattern"`             // AST DSL（Mode=ast 时使用）
	RegexPattern string    `gorm:"type:text" json:"regex_pattern"`           // 正则表达式（Mode=regex 时使用）
	Description  string    `gorm:"type:text" json:"description"`             // 规则描述
	Suggestion   string    `gorm:"type:text" json:"suggestion"`              // 修复建议
	IsBuiltin    bool      `gorm:"default:true;not null" json:"is_builtin"`  // 是否系统内置
	Enabled      bool      `gorm:"default:true;not null" json:"enabled"`     // 是否启用
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (SecurityAuditRule) TableName() string {
	return "security_audit_rules"
}

// SecurityAuditFinding 安全审计发现记录
type SecurityAuditFinding struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	TaskID         uint      `gorm:"index;not null" json:"task_id"`
	RuleCode       string    `gorm:"size:64;not null" json:"rule_code"`
	FilePath       string    `gorm:"size:512;not null" json:"file_path"`
	LineNumber     int       `json:"line_number"`
	ColumnStart    int       `json:"column_start"`
	ColumnEnd      int       `json:"column_end"`
	Snippet        string    `gorm:"type:text" json:"snippet"` // 代码片段（函数签名或单行摘要）
	TriggerSnippet string    `json:"trigger_snippet" gorm:"-"` // 触发规则的具体语句块（仅内存，不入库）
	Severity       string    `gorm:"size:16" json:"severity"`
	Message        string    `gorm:"type:text" json:"message"`    // 发现问题描述
	Suggestion     string    `gorm:"type:text" json:"suggestion"` // 修复建议（快照）
	CreatedAt      time.Time `json:"created_at"`
}

func (SecurityAuditFinding) TableName() string {
	return "security_audit_findings"
}
