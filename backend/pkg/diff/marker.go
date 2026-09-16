package diff

import "strings"

// code_snippet 中的问题定位标记（与 schema / builder 中的说明保持一致）
const (
	MarkerProblem     = " <<< 问题所在"
	MarkerRegionStart = " <<< 问题区域开始"
	MarkerRegionEnd   = " <<< 问题区域结束"
)

// MarkerKind 标记类型
type MarkerKind int

const (
	MarkerKindNone    MarkerKind = iota // 无标记
	MarkerKindProblem                   // 单行问题：<<< 问题所在
	MarkerKindRegion                    // 多行区域：<<< 问题区域开始 / <<< 问题区域结束
)

func (k MarkerKind) String() string {
	switch k {
	case MarkerKindProblem:
		return "problem"
	case MarkerKindRegion:
		return "region"
	default:
		return "none"
	}
}

// SnippetMarker snippet 标记解析结果
type SnippetMarker struct {
	Kind MarkerKind
	// CodeLines 去掉标记与空行后的代码行（已 TrimSpace），供连续块匹配使用
	CodeLines []string
	// StartIdx / EndIdx 为标记行在 CodeLines 中的下标（0-based）；-1 表示未定位
	StartIdx  int
	EndIdx    int
	StartText string // 去掉标记后的问题行/区域首行文本（已 TrimSpace）
	EndText   string // 区域末行文本（已 TrimSpace）；单行问题为空
}

// stripAllMarkers 移除全部定位标记
func stripAllMarkers(s string) string {
	s = strings.ReplaceAll(s, MarkerRegionStart, "")
	s = strings.ReplaceAll(s, MarkerRegionEnd, "")
	s = strings.ReplaceAll(s, MarkerProblem, "")
	return s
}

// ParseSnippetMarkers 解析 code_snippet 中的问题定位标记。
// 返回标记类型，以及"去掉标记后"的问题行/区域首尾行文本，供精确匹配使用。
// 若同时出现区域与单行标记，优先按区域处理。
func ParseSnippetMarkers(snippet string) SnippetMarker {
	res := SnippetMarker{StartIdx: -1, EndIdx: -1}
	if snippet == "" {
		return res
	}

	var hasRegion, hasProblem bool

	for _, ln := range strings.Split(snippet, "\n") {
		// 先取出该行去掉标记后的代码文本，并登记到 CodeLines（空行不登记）
		text := strings.TrimSpace(stripAllMarkers(ln))
		idx := -1
		if text != "" {
			idx = len(res.CodeLines)
			res.CodeLines = append(res.CodeLines, text)
		}

		switch {
		case strings.Contains(ln, MarkerRegionStart):
			if res.StartIdx == -1 {
				res.StartIdx = idx
				res.StartText = text
			}
			hasRegion = true
		case strings.Contains(ln, MarkerRegionEnd):
			if res.EndIdx == -1 {
				res.EndIdx = idx
				res.EndText = text
			}
			hasRegion = true
		case strings.Contains(ln, MarkerProblem):
			if res.StartIdx == -1 {
				res.StartIdx = idx
				res.StartText = text
			}
			hasProblem = true
		}
	}

	switch {
	case hasRegion:
		res.Kind = MarkerKindRegion
	case hasProblem:
		res.Kind = MarkerKindProblem
	default:
		res.Kind = MarkerKindNone
	}
	return res
}

// FirstCodeLine 返回去掉标记后的第一个非空行（无标记时的兜底匹配目标）
func FirstCodeLine(snippet string) string {
	for _, ln := range strings.Split(stripAllMarkers(snippet), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}
