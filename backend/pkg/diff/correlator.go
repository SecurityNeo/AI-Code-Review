package diff

import (
	"fmt"
	"strings"
)

// MatchType 匹配类型
type MatchType int

const (
	MatchTypeExact       MatchType = iota // 完全匹配
	MatchTypeFuzzy                         // 模糊匹配
	MatchTypeNotAvailable                  // 无 snippet，未做校正
)

// CorrectionResult 校正结果
type CorrectionResult struct {
	FilePath      string  // 文件路径
	OldStart      int     // 原始起始行号（模型返回）
	OldEnd        int     // 原始结束行号
	NewStart      int     // 校正后起始行号
	NewEnd        int     // 校正后结束行号
	MatchType     MatchType // 匹配类型
	Confidence    float64 // 置信度 0.0-1.0
	OriginalSnippet string // 原始 snippet
	MatchedSnippet  string // 匹配到的 snippet
	Message       string  // 人类可读说明
}

// LineCorrelator 行号校正器
type LineCorrelator struct {
	// filePath -> ParsedDiffFile mapping
	fileMap map[string]*ParsedDiffFile
}

// NewLineCorrelator 创建校正器
func NewLineCorrelator(fileMap map[string]*ParsedDiffFile) *LineCorrelator {
	return &LineCorrelator{fileMap: fileMap}
}

// CorrectIssue 对单个 Issue 进行校正
func (lc *LineCorrelator) CorrectIssue(filePath string, startLine, endLine int, snippet string) CorrectionResult {
	result := CorrectionResult{
		FilePath:        filePath,
		OldStart:        startLine,
		OldEnd:          endLine,
		NewStart:        startLine,
		NewEnd:          endLine,
		OriginalSnippet: snippet,
		MatchType:       MatchTypeNotAvailable,
		Confidence:      0.0,
	}

	file, ok := lc.fileMap[filePath]
	if !ok {
		result.Message = fmt.Sprintf("文件 %s 不在 diff 中，直接信任模型行号", filePath)
		return result
	}

	cleanSnippet := strings.TrimSpace(snippet)
	if cleanSnippet == "" {
		result.Message = "模型未返回有效 code snippet（空或纯空白），直接信任模型行号"
		result.MatchType = MatchTypeNotAvailable
		result.Confidence = 0.0
		return result
	}

	// 首先尝试 exact match：在 file.Lines 中精确查找 snippet
	exactLine := lc.findExact(file, snippet)
	if exactLine != nil {
		result.NewStart = getEffectiveLineNo(exactLine)
		result.NewEnd = getEffectiveLineNo(exactLine)
		result.MatchType = MatchTypeExact
		result.Confidence = 1.0
		result.MatchedSnippet = snippet
		result.Message = fmt.Sprintf("精确匹配到新文件第%d行", result.NewStart)
		return result
	}

	// 次选：使用 fuzzy 匹配（编辑距离）
	fuzzyLine := lc.findFuzzy(file, snippet)
	if fuzzyLine != nil {
		cleanSnippet := strings.TrimSpace(snippet)
		cleanMatched := stripDiffPrefix(fuzzyLine.Raw, fuzzyLine.Type)
		dist := levenshteinDistance(cleanSnippet, cleanMatched)
		maxLen := max(len(cleanSnippet), len(cleanMatched))
		var confidence float64
		if maxLen == 0 {
			confidence = 0 // 两边都是空字符串，认为无意义匹配
		} else {
			confidence = 1.0 - float64(dist)/float64(maxLen)
		}
		result.NewStart = getEffectiveLineNo(fuzzyLine)
		result.NewEnd = getEffectiveLineNo(fuzzyLine)
		result.MatchType = MatchTypeFuzzy
		result.Confidence = confidence
		result.MatchedSnippet = fuzzyLine.Raw
		result.Message = fmt.Sprintf("模糊匹配到第%d行，置信度%.2f", fuzzyLine.DiffOffset, confidence)
		return result
	}

	// fallback：不校正
	result.Message = fmt.Sprintf("未找到匹配，直接信任模型行号（搜索范围：%d 行）", len(file.Lines))
	return result
}

// getEffectiveLineNo 返回 diff 行在目标文件中的有效行号
// deletion 行返回旧文件行号，其他返回新文件行号
func getEffectiveLineNo(line *DiffLine) int {
	if line.Type == LineTypeDeletion {
		return line.OldLineNo
	}
	return line.NewLineNo
}

// stripDiffPrefix 根据行类型安全移除 diff 前缀符号
func stripDiffPrefix(raw string, t LineType) string {
	cleanLine := strings.TrimSpace(raw)
	switch t {
	case LineTypeAddition:
		return strings.TrimPrefix(cleanLine, "+")
	case LineTypeDeletion:
		return strings.TrimPrefix(cleanLine, "-")
	case LineTypeContext:
		if strings.HasPrefix(cleanLine, " ") {
			return cleanLine[1:]
		}
	}
	return cleanLine
}

// findExact 在文件中精确查找代码片段
func (lc *LineCorrelator) findExact(file *ParsedDiffFile, snippet string) *DiffLine {
	cleanSnippet := strings.TrimSpace(snippet)
	for i := range file.Lines {
		line := &file.Lines[i]
		if line.Type == LineTypeHunkHeader {
			continue
		}
		cleanLine := stripDiffPrefix(line.Raw, line.Type)
		if cleanLine == cleanSnippet {
			return line
		}
	}
	return nil
}

// findFuzzy 使用最小编辑距离查找最相似的行
func (lc *LineCorrelator) findFuzzy(file *ParsedDiffFile, snippet string) *DiffLine {
	cleanSnippet := strings.TrimSpace(snippet)
	bestLine := -1
	bestDist := int(^uint(0) >> 1) // MaxInt

	for i := range file.Lines {
		line := &file.Lines[i]
		if line.Type == LineTypeHunkHeader {
			continue
		}
		cleanLine := stripDiffPrefix(line.Raw, line.Type)
		dist := levenshteinDistance(cleanSnippet, cleanLine)
		if dist < bestDist {
			bestDist = dist
			bestLine = i
		}
	}

	if bestLine < 0 {
		return nil
	}
	return &file.Lines[bestLine]
}

// maxSnippetLen 模糊匹配时截断的最大长度，防止超长单行导致 O(n²) 性能问题
const maxSnippetLen = 200

// levenshteinDistance 计算两个字符串的编辑距离（输入会被截断到 maxSnippetLen）
func levenshteinDistance(a, b string) int {
	if len(a) > maxSnippetLen {
		a = a[:maxSnippetLen]
	}
	if len(b) > maxSnippetLen {
		b = b[:maxSnippetLen]
	}
	la := len(a)
	lb := len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// 使用一维 DP 优化空间
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)

	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = min(min(
				prev[j]+1,      // deletion
				curr[j-1]+1),   // insertion
				prev[j-1]+cost) // substitution
		}
		prev, curr = curr, prev
	}

	return prev[lb]
}

// BatchCorrect 批量校正多个 Issue
func (lc *LineCorrelator) BatchCorrect(issues []CorrectionInput) []CorrectionResult {
	results := make([]CorrectionResult, len(issues))
	for i, issue := range issues {
		results[i] = lc.CorrectIssue(issue.FilePath, issue.LineStart, issue.LineEnd, issue.CodeSnippet)
	}
	return results
}

// CorrectionInput 校正输入
type CorrectionInput struct {
	FilePath    string
	LineStart   int
	LineEnd     int
	CodeSnippet string
}

// FilterHighConfidence 过滤出高置信度的校正结果（可用于自动批量修复）
func FilterHighConfidence(results []CorrectionResult, threshold float64) []CorrectionResult {
	var filtered []CorrectionResult
	for _, r := range results {
		if r.Confidence >= threshold {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// StoreOriginalResult 存储原始模型输出用于审计（预留接口）
func StoreOriginalResult(result CorrectionResult) map[string]interface{} {
	return map[string]interface{}{
		"file_path":        result.FilePath,
		"old_start":        result.OldStart,
		"old_end":          result.OldEnd,
		"new_start":        result.NewStart,
		"new_end":          result.NewEnd,
		"match_type":       result.MatchType,
		"confidence":       result.Confidence,
		"original_snippet": result.OriginalSnippet,
		"matched_snippet":  result.MatchedSnippet,
		"message":          result.Message,
	}
}
