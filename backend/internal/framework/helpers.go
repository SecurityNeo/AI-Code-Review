package framework

import "strings"

// extractStringLiteral 从字符串字面量表达式中提取裸字符串值
// 支持双引号转义字符串、原始字符串（反引号）、以及裸标识符
func extractStringLiteral(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}

	// 双引号字符串（处理 Go/Python/Java 等常见转义序列）
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		unquoted := s[1 : len(s)-1]
		unquoted = strings.ReplaceAll(unquoted, `\\`, `\`)
		unquoted = strings.ReplaceAll(unquoted, `\"`, `"`)
		unquoted = strings.ReplaceAll(unquoted, `\n`, "\n")
		unquoted = strings.ReplaceAll(unquoted, `\t`, "\t")
		unquoted = strings.ReplaceAll(unquoted, `\r`, "\r")
		return unquoted
	}

	// 单引号字符串（Python）
	if strings.HasPrefix(s, `'`) && strings.HasSuffix(s, `'`) {
		return s[1 : len(s)-1]
	}

	// 原始字符串（Go 反引号）
	if strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") {
		return s[1 : len(s)-1]
	}

	// 已经是裸字符串
	return s
}
