package diff

import (
	"fmt"
	"strings"
)

// PromptConfig Prompt 构建配置
type PromptConfig struct {
	// 是否启用行号注入
	Enabled bool
	// 是否在 hunk 中注入行号前缀
	InjectLineNumbers bool
	// 被截断 diff 的上下文标记前缀
	TruncationMarkerPrefix string
}

// BuildPrompt 根据解析后的 diff 文件构建注入行号的 Prompt 文本
func BuildPrompt(file *ParsedDiffFile, cfg PromptConfig) string {
	if !cfg.Enabled {
		return file.RawDiff
	}

	var parts []string

	// 文件头（不含 diff --git 等元信息，只保留变更路径）
	parts = append(parts, fmt.Sprintf("--- %s", file.OldPath))
	parts = append(parts, fmt.Sprintf("+++ %s", file.NewPath))

	for _, hunk := range file.Hunks {
		// hunk header 保持原样
		parts = append(parts, hunk.Lines[0].Raw)

		if !cfg.InjectLineNumbers {
			// 不注入行号，直接追加原始行
			for i := 1; i < len(hunk.Lines); i++ {
				parts = append(parts, hunk.Lines[i].Raw)
			}
			continue
		}

		// 注入行号前缀
		for i := 1; i < len(hunk.Lines); i++ {
			line := hunk.Lines[i]
			prefix := buildLinePrefix(line)
			parts = append(parts, prefix+line.Raw)
		}
	}

	return strings.Join(parts, "\n")
}

// BuildPromptWithTruncation 对截断后的 hunk 构建 Prompt
func BuildPromptWithTruncation(file *ParsedDiffFile, truncator HunkTruncator, cfg PromptConfig) string {
	if !cfg.Enabled {
		return file.RawDiff
	}

	var parts []string

	parts = append(parts, fmt.Sprintf("--- %s", file.OldPath))
	parts = append(parts, fmt.Sprintf("+++ %s", file.NewPath))

	for _, hunk := range file.Hunks {
		truncated := truncator.Truncate(hunk)
		if len(truncated) == 0 {
			continue
		}

		// hunk header
		parts = append(parts, truncated[0].Raw)

		if truncator.Type() != TruncationTypeNoChange && cfg.TruncationMarkerPrefix != "" {
			// 若存在截断标记，追加标记行
			// （此能力由调用方在外层按需注入）
		}

		if !cfg.InjectLineNumbers {
			for i := 1; i < len(truncated); i++ {
				parts = append(parts, truncated[i].Raw)
			}
			continue
		}

		for i := 1; i < len(truncated); i++ {
			line := truncated[i]
			prefix := buildLinePrefix(line)
			parts = append(parts, prefix+line.Raw)
		}
	}

	return strings.Join(parts, "\n")
}

// buildLinePrefix 构造单行前缀 [newXXX|oldYYY]
func buildLinePrefix(line DiffLine) string {
	switch line.Type {
	case LineTypeContext:
		return fmt.Sprintf("[new%d|old%d] ", line.NewLineNo, line.OldLineNo)
	case LineTypeAddition:
		return fmt.Sprintf("[new%d|old-] ", line.NewLineNo)
	case LineTypeDeletion:
		return fmt.Sprintf("[new-|old%d] ", line.OldLineNo)
	default:
		return ""
	}
}

// BuildPromptLegacy 兼容旧版：不注入行号、不做截断，直接返回原始 diff
func BuildPromptLegacy(file *ParsedDiffFile) string {
	return file.RawDiff
}

// BuildDiffLineInfoSlice 将 diff 行列表转换为 DiffLineInfo 切片（用于数据库存储）
func BuildDiffLineInfoSlice(lines []DiffLine) []DiffLineInfo {
	var result []DiffLineInfo
	for _, line := range lines {
		t, ok := typeToChar(line.Type)
		if !ok {
			continue
		}
		result = append(result, DiffLineInfo{
			N: line.NewLineNo,
			O: line.OldLineNo,
			T: t,
		})
	}
	return result
}

// typeToChar 将 LineType 转换为单字符标识
func typeToChar(t LineType) (string, bool) {
	switch t {
	case LineTypeContext:
		return "c", true
	case LineTypeAddition:
		return "+", true
	case LineTypeDeletion:
		return "-", true
	default:
		return "", false
	}
}

// ExtractSnippet 从 Prompt 文本中反向提取代码片段（用于 Correlator 输入格式化）
func ExtractSnippet(promptLines []string, startLine, endLine int) string {
	var snippet []string
	for i := startLine - 1; i < endLine && i < len(promptLines); i++ {
		snippet = append(snippet, promptLines[i])
	}
	return strings.Join(snippet, "\n")
}
