package model

import (
	"go.uber.org/zap"
)

// initSecretScanRules 初始化密钥扫描内置规则库
func initSecretScanRules() {
	var count int64
	DB.Model(&SecretScanRule{}).Where("is_builtin = ?", true).Count(&count)
	if count > 0 {
		return
	}

	builtins := []SecretScanRule{
		{Code: "SECRET-001", Name: "AWS Access Key ID", Category: "aws", Severity: "critical", Pattern: `(?i)AKIA[0-9A-Z]{16}`, Keywords: "AKIA", Description: "AWS Access Key ID 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-002", Name: "AWS Secret Access Key", Category: "aws", Severity: "critical", Pattern: `(?i)aws(.{0,20})?(secret)?.{0,20}['""][A-Za-z0-9/+=]{40}['""]`, Keywords: "aws,secret", Description: "AWS Secret Access Key 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-003", Name: "GitHub Personal Token", Category: "github", Severity: "critical", Pattern: `ghp_[A-Za-z0-9_]{36}`, Keywords: "ghp_", Description: "GitHub Personal Access Token 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-004", Name: "GitHub OAuth Token", Category: "github", Severity: "critical", Pattern: `gho_[A-Za-z0-9_]{36}`, Keywords: "gho_", Description: "GitHub OAuth Token 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-005", Name: "GitHub App Token", Category: "github", Severity: "critical", Pattern: `ghs_[A-Za-z0-9_]{36}`, Keywords: "ghs_", Description: "GitHub App Installation Token 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-006", Name: "GitLab Personal Token", Category: "gitlab", Severity: "critical", Pattern: `glpat-[A-Za-z0-9_-]{20}`, Keywords: "glpat", Description: "GitLab Personal Access Token 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-007", Name: "Slack Token", Category: "slack", Severity: "high", Pattern: `xox[baprs]-[A-Za-z0-9-]+`, Keywords: "xox", Description: "Slack Bot/User Token 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-008", Name: "JWT Token", Category: "generic", Severity: "high", Pattern: `eyJ[A-Za-z0-9_-]*\.eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*`, Keywords: "eyJ", Description: "JSON Web Token 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-009", Name: "通用密码赋值", Category: "generic", Severity: "high", Pattern: `(?i)(password|passwd|pwd)\s*[=:]\s*['""][^'""\s]{4,}['""]`, Keywords: "password,passwd,pwd", Description: "代码中硬编码密码", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-010", Name: "API Key 通用", Category: "generic", Severity: "high", Pattern: `(?i)(api[_-]?key|apikey)\s*[=:]\s*['""][A-Za-z0-9_-]{8,}['""]`, Keywords: "apikey,api_key", Description: "API Key 硬编码", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-011", Name: "Authorization Bearer", Category: "generic", Severity: "high", Pattern: `(?i)Authorization:\s*Bearer\s+[A-Za-z0-9_\-\.]+`, Keywords: "Authorization,Bearer", Description: "HTTP Authorization Header 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-012", Name: "私钥文件", Category: "private_key", Severity: "critical", Pattern: `-----BEGIN\s+(RSA|EC|DSA|OPENSSH)?\s*PRIVATE\s+KEY-----`, Keywords: "PRIVATE KEY", Description: "私钥文件内容泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-013", Name: "数据库连接串", Category: "database", Severity: "high", Pattern: `(?i)(postgres|mysql|mongodb|redis)://[^:]+:[^@]+@`, Keywords: "postgres://,mysql://", Description: "数据库连接凭证泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-014", Name: "阿里云 AccessKey", Category: "aliyun", Severity: "critical", Pattern: `(?i)LTAI[A-Za-z0-9]{12,20}`, Keywords: "LTAI", Description: "阿里云 AccessKey ID 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-015", Name: "腾讯云 SecretId", Category: "tencent", Severity: "critical", Pattern: `(?i)AKID[A-Za-z0-9]{32}`, Keywords: "AKID", Description: "腾讯云 SecretId 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-016", Name: "华为云 AK", Category: "huawei", Severity: "critical", Pattern: `(?i)Y3[A-Za-z0-9]{20,40}`, Keywords: "Y3", Description: "华为云 Access Key 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-017", Name: "DingTalk Token", Category: "dingtalk", Severity: "high", Pattern: `(?i)ding[a-f0-9]{32}`, Keywords: "ding", Description: "钉钉 Access Token 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-018", Name: "WeCom Webhook Key", Category: "wecom", Severity: "high", Pattern: `(?i)key=[a-f0-9]{32,64}`, Keywords: "key=", Description: "企业微信 Webhook Key 泄露", IsBuiltin: true, Enabled: true},
		{Code: "SECRET-019", Name: "硬编码 IP + 端口", Category: "generic", Severity: "medium", Pattern: `\b(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b`, Keywords: "", Description: "硬编码 IP 地址（可能是内网服务地址）", IsBuiltin: true, Enabled: false},
		{Code: "SECRET-020", Name: "十六进制密钥", Category: "generic", Severity: "medium", Pattern: `['""][a-f0-9]{32,64}['""]`, Keywords: "", Description: "十六进制编码的密钥/Token（需上下文确认）", IsBuiltin: true, Enabled: false},
	}

	if err := DB.CreateInBatches(builtins, 20).Error; err != nil {
		zap.L().Warn("init secret scan rules failed", zap.Error(err))
	} else {
		zap.L().Info("init secret scan rules", zap.Int("count", len(builtins)))
	}
}
