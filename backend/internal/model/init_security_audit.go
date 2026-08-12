package model

import (
	"go.uber.org/zap"
)

// initSecurityAuditRules 初始化安全审计内置规则库
func initSecurityAuditRules() {
	var count int64
	DB.Model(&SecurityAuditRule{}).Where("is_builtin = ?", true).Count(&count)
	if count > 0 {
		return
	}

	builtins := []SecurityAuditRule{
		// 通用规则（正则模式）
		{Code: "SA_SQL_INJECTION", Name: "SQL 拼接注入", Category: "sql_injection", Severity: "high", Language: "", Mode: "regex", RegexPattern: `(?i)(Query|Exec)\s*\(.*[+%].*\)`, Description: "SQL 语句通过字符串拼接构造，存在 SQL 注入风险", Suggestion: "使用参数化查询或预编译语句", IsBuiltin: true, Enabled: true},
		{Code: "SA_UNSAFE_EVAL", Name: "不安全 eval", Category: "unsafe_eval", Severity: "critical", Language: "", Mode: "regex", RegexPattern: `(?i)\beval\s*\(`, Description: "eval() 调用可能导致任意代码执行", Suggestion: "避免使用 eval，改用安全的解析器", IsBuiltin: true, Enabled: true},
		{Code: "SA_HARDCODED_SECRET", Name: "硬编码密钥", Category: "hardcoded_secret", Severity: "high", Language: "", Mode: "regex", RegexPattern: `(?i)(password|secret|token)\s*=\s*['""][^'""]{8,}['""]`, Description: "代码中硬编码敏感凭证", Suggestion: "使用环境变量或密钥管理服务", IsBuiltin: true, Enabled: true},
		{Code: "SA_PATH_TRAVERSAL", Name: "路径遍历", Category: "path_traversal", Severity: "medium", Language: "", Mode: "regex", RegexPattern: `\.\./\.\.`, Description: "目录遍历路径，可能导致未授权文件访问", Suggestion: "对路径进行规范化处理和白名单校验", IsBuiltin: true, Enabled: true},
		// Go 特定规则（AST 模式）
		{Code: "SA_GO_UNSAFE", Name: "unsafe 包使用", Category: "unsafe_eval", Severity: "high", Language: "golang", Mode: "ast", ASTPattern: "unsafe", Description: "使用 unsafe 包进行类型转换或指针操作", Suggestion: "尽量避免使用 unsafe，如必须使用请添加充分注释说明原因", IsBuiltin: true, Enabled: true},
		{Code: "SA_GO_SQL_FMT", Name: "SQL 格式化注入", Category: "sql_injection", Severity: "high", Language: "golang", Mode: "ast", ASTPattern: "Sprintf", Description: "使用 fmt.Sprintf 拼接 SQL 语句", Suggestion: "使用数据库驱动的参数化查询", IsBuiltin: true, Enabled: true},
		{Code: "SA_GO_REFLECT", Name: "反射调用", Category: "reflection", Severity: "medium", Language: "golang", Mode: "ast", ASTPattern: "reflect", Description: "使用 reflect 包进行运行时反射", Suggestion: "反射降低性能且绕过类型检查，尽量使用接口和类型断言", IsBuiltin: true, Enabled: true},
		{Code: "SA_GO_DESERIALIZE", Name: "不安全的反序列化", Category: "deserialization", Severity: "high", Language: "golang", Mode: "ast", ASTPattern: "unmarshal", Description: "对不可信输入进行反序列化操作", Suggestion: "反序列化前验证输入数据格式和大小", IsBuiltin: true, Enabled: true},
		// Java/Python/JS 兜底规则（正则模式）
		{Code: "SA_JAVA_SERIALIZE", Name: "Java 反序列化", Category: "deserialization", Severity: "critical", Language: "java", Mode: "regex", RegexPattern: `(?i)ObjectInputStream|readObject`, Description: "Java 对象反序列化存在漏洞风险", Suggestion: "使用 JSON 等安全格式替代 Java 原生序列化", IsBuiltin: true, Enabled: true},
		{Code: "SA_PYTHON_EXEC", Name: "Python 动态执行", Category: "unsafe_eval", Severity: "critical", Language: "python", Mode: "regex", RegexPattern: `(?i)\bexec\s*\(`, Description: "exec() 调用可能导致任意代码执行", Suggestion: "避免执行动态代码，使用安全解析方案", IsBuiltin: true, Enabled: true},
		{Code: "SA_JS_INNER_HTML", Name: "JS innerHTML 注入", Category: "unsafe_eval", Severity: "high", Language: "javascript", Mode: "regex", RegexPattern: `(?i)\.innerHTML\s*=\s*[^;]+`, Description: "直接设置 innerHTML 可能导致 XSS", Suggestion: "使用 textContent 或安全的 DOM 操作", IsBuiltin: true, Enabled: true},
		{Code: "SA_GENERIC_REFLECT", Name: "通用反射", Category: "reflection", Severity: "medium", Language: "", Mode: "regex", RegexPattern: `(?i)\breflect\.`, Description: "反射调用可能存在安全风险", Suggestion: "避免不必要的反射，使用类型安全的替代方案", IsBuiltin: true, Enabled: true},
	}

	if err := DB.CreateInBatches(builtins, 20).Error; err != nil {
		zap.L().Warn("init security audit rules failed", zap.Error(err))
	} else {
		zap.L().Info("init security audit rules", zap.Int("count", len(builtins)))
	}
}
