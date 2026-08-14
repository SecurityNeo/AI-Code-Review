package engine

import (
	"path/filepath"
	"strings"
)

// FileMatcher 文件路径匹配器
// 支持 glob 语法：* 匹配任意字符（单层），** 匹配任意层级目录
type FileMatcher struct {
	excludePatterns []string
	includePatterns []string
}

// NewFileMatcher 创建匹配器
// lang: 项目语言（从 task.Project.Language 获取）
func NewFileMatcher(cfg FileFilterConfig, lang string) *FileMatcher {
	m := &FileMatcher{}

	// 1. 收集所有排除规则
	var excludePatterns []string

	if cfg.EnableDefaultFilters {
		excludePatterns = append(excludePatterns, CommonExcludePatterns...)
		if patterns, ok := DefaultExcludePatternsByLanguage[strings.ToLower(lang)]; ok {
			excludePatterns = append(excludePatterns, patterns...)
		}
	}

	// 自定义排除规则
	excludePatterns = append(excludePatterns, cfg.ExcludePatterns...)

	// 2. 收集包含规则（白名单）
	m.excludePatterns = excludePatterns
	m.includePatterns = append([]string{}, cfg.IncludePatterns...)

	return m
}

// ShouldSkip 判断是否应该跳过该文件
// 规则：先检查白名单（include），再检查黑名单（exclude）
func (m *FileMatcher) ShouldSkip(path string) bool {
	// 1. 白名单检查：如果匹配 include，则不跳过
	for _, p := range m.includePatterns {
		if matchGlob(path, p) {
			return false
		}
	}

	// 2. 黑名单检查：如果匹配 exclude，则跳过
	for _, p := range m.excludePatterns {
		if matchGlob(path, p) {
			return true
		}
	}

	return false
}

// matchGlob 路径与 glob 模式匹配
// 支持标准 glob 特性：*、?、[]、/**/
func matchGlob(path, pattern string) bool {
	// 1. 精确匹配
	if path == pattern {
		return true
	}

	// 2. 处理 ** 递归匹配（如 vendor/**）
	if strings.Contains(pattern, "**") {
		return matchDoubleStar(path, pattern)
	}

	// 3. 使用标准库 filepath.Match 处理单层 glob
	matched, err := filepath.Match(pattern, path)
	if err == nil && matched {
		return true
	}

	// 4. 处理目录前缀匹配（如 vendor/ 匹配 vendor/xxx/yyy.go）
	if strings.HasSuffix(pattern, "/") {
		if strings.HasPrefix(path, pattern) {
			return true
		}
	}

	return false
}

// matchDoubleStar 处理包含 ** 的递归匹配
// 支持模式如: vendor/**, **/test/**, a/**/b.go
func matchDoubleStar(path, pattern string) bool {
	// 处理前缀/** 模式（如 vendor/**）
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if prefix == "" {
			return true // /** 匹配所有
		}
		if path == prefix {
			return true
		}
		if strings.HasPrefix(path, prefix+"/") {
			return true
		}
		return false
	}

	// 处理 **/suffix 模式（如 **/vendor/）
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		if strings.HasSuffix(path, suffix) {
			return true
		}
		// 也检查路径中是否包含该后缀
		if strings.Contains(path, "/"+suffix) {
			return true
		}
		return false
	}

	// 复杂模式如 a/**/b.go：分段匹配
	parts := strings.Split(pattern, "**")
	if len(parts) != 2 {
		// 多个 ** 或没有 **，退回到简单处理
		matched, _ := filepath.Match(pattern, path)
		return matched
	}

	prefix := parts[0]
	suffix := parts[1]

	// 检查前缀匹配
	if prefix != "" && !strings.HasPrefix(path, prefix) {
		return false
	}

	// 检查后缀匹配
	if suffix != "" && !strings.HasSuffix(path, suffix) {
		return false
	}

	return true
}
