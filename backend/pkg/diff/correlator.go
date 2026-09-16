package diff

import (
	"fmt"
	"strings"
)

// MatchType 匹配类型
type MatchType int

const (
	MatchTypeExact        MatchType = iota // 完全匹配
	MatchTypeFuzzy                         // 模糊匹配
	MatchTypeNotAvailable                  // 无 snippet，未做校正
)

// CorrectionResult 校正结果
type CorrectionResult struct {
	FilePath        string     // 文件路径
	OldStart        int        // 原始起始行号（模型返回）
	OldEnd          int        // 原始结束行号
	NewStart        int        // 校正后起始行号
	NewEnd          int        // 校正后结束行号
	MatchType       MatchType  // 匹配类型
	MarkerKind      MarkerKind // snippet 标记类型（problem/region/none）
	Confidence      float64    // 置信度 0.0-1.0
	OriginalSnippet string     // 原始 snippet
	MatchedSnippet  string     // 匹配到的 snippet
	Message         string     // 人类可读说明
}

// LineCorrelator 行号校正器
type LineCorrelator struct {
	// filePath -> ParsedDiffFile mapping
	fileMap            map[string]*ParsedDiffFile
	minFuzzyConfidence float64 // fuzzy 匹配最小置信度阈值，低于此值视为不匹配
}

// NewLineCorrelator 创建校正器
func NewLineCorrelator(fileMap map[string]*ParsedDiffFile) *LineCorrelator {
	return NewLineCorrelatorWithThreshold(fileMap, 0.3)
}

// NewLineCorrelatorWithThreshold 创建带自定义阈值的校正器
func NewLineCorrelatorWithThreshold(fileMap map[string]*ParsedDiffFile, threshold float64) *LineCorrelator {
	if threshold <= 0 {
		threshold = 0.3 // 安全默认值
	}
	return &LineCorrelator{fileMap: fileMap, minFuzzyConfidence: threshold}
}

// CorrectIssue 对单个 Issue 进行校正。
// 策略（按优先级）：
//  1. 将去掉标记后的 snippet 作为"连续代码块"在文件新行序列中查找，
//     并选取离模型 line_start 最近的匹配块（解决同文件重复行歧义）；
//     若模型 line_start 落在匹配块内，则保留（避免漂移），否则以块起始行为准。
//  2. 块匹配失败时，退化为对"问题行/区域首行"做单行精确/模糊匹配。
//  3. 均失败则信任模型行号。
//
// 区域标记（<<< 问题区域开始/结束）决定多行区间；仅单行标记时收敛为单行。
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

	if strings.TrimSpace(snippet) == "" {
		result.Message = "模型未返回有效 code snippet（空或纯空白），直接信任模型行号"
		return result
	}

	// 解析问题定位标记
	mk := ParseSnippetMarkers(snippet)
	result.MarkerKind = mk.Kind

	codeLines := mk.CodeLines
	if len(codeLines) == 0 {
		result.Message = "snippet 无有效代码行（仅标记/空白），直接信任模型行号"
		return result
	}

	// 1) 连续代码块匹配（就近消歧）
	if blockStart, blockEnd, ok := lc.findBlockNearest(file, codeLines, startLine); ok {
		newStart := blockStart
		if startLine >= blockStart && startLine <= blockEnd {
			newStart = startLine // 模型行号已在匹配块内 → 信任模型，避免无谓漂移
		}
		result.NewStart = newStart
		switch mk.Kind {
		case MarkerKindRegion:
			// 区域末端以「问题区域结束」标记所在行为准，避免 snippet 末尾的上下文行把范围撑大
			newEnd := blockEnd
			if mk.EndIdx >= 0 && blockStart+mk.EndIdx <= blockEnd {
				newEnd = blockStart + mk.EndIdx
			}
			if newEnd > newStart {
				result.NewEnd = newEnd
			} else {
				result.NewEnd = newStart
			}
		case MarkerKindProblem:
			// 单行问题标记 → 收敛为单行
			result.NewEnd = newStart
		default:
			// 无标记：保留模型给出的区间长度，随起始行平移（避免把多行问题误折叠为单行）
			span := endLine - startLine
			if span < 0 {
				span = 0
			}
			result.NewEnd = newStart + span
			if result.NewEnd < result.NewStart {
				result.NewEnd = result.NewStart
			}
		}
		result.MatchType = MatchTypeExact
		result.Confidence = 1.0
		result.MatchedSnippet = strings.Join(codeLines, "\n")
		result.Message = fmt.Sprintf("代码块匹配到新文件第%d-%d行", result.NewStart, result.NewEnd)
		return result
	}

	// 2) 退化：单行匹配"问题行/区域首行"
	target := mk.StartText
	if target == "" {
		target = FirstCodeLine(snippet)
	}
	line, matchType, confidence, matchedRaw := lc.matchLine(file, target)
	if line == nil {
		result.Message = fmt.Sprintf("未找到匹配代码，直接信任模型行号（搜索范围：%d 行）", len(file.Lines))
		return result
	}
	result.NewStart = getEffectiveLineNo(line)
	result.NewEnd = result.NewStart
	result.MatchType = matchType
	result.Confidence = confidence
	result.MatchedSnippet = matchedRaw

	// 多行区域：单独定位末行，得到合法区间 [NewStart, NewEnd]
	if mk.Kind == MarkerKindRegion && mk.EndText != "" {
		if el, _, _, _ := lc.matchLine(file, mk.EndText); el != nil {
			result.NewEnd = getEffectiveLineNo(el)
		}
		if result.NewEnd < result.NewStart {
			result.NewStart, result.NewEnd = result.NewEnd, result.NewStart
		}
	}
	// 无标记：保留原始区间长度（模型已给出多行范围但未打标记的情况）
	if mk.Kind == MarkerKindNone {
		if span := endLine - startLine; span > 0 {
			result.NewEnd = result.NewStart + span
		}
	}

	switch matchType {
	case MatchTypeExact:
		result.Message = fmt.Sprintf("精确匹配到新文件第%d行", result.NewStart)
	default:
		result.Message = fmt.Sprintf("模糊匹配到新文件第%d行，置信度%.2f", result.NewStart, confidence)
	}
	return result
}

// findBlockNearest 在新行序列中查找与 snippetLines 连续匹配的代码块，
// 返回块的首/末新文件行号。存在多个匹配时，选取起始行号最接近 anchor 的块。
// 要求匹配块在「新文件行号」上连续（防止跨 hunk 间隙拼出伪连续块）。
func (lc *LineCorrelator) findBlockNearest(file *ParsedDiffFile, snippetLines []string, anchor int) (int, int, bool) {
	// 候选行：存在于新文件中的行（context + addition），保持文件顺序
	cand := make([]*DiffLine, 0, len(file.Lines))
	for i := range file.Lines {
		if file.Lines[i].IsNewLine() {
			cand = append(cand, &file.Lines[i])
		}
	}
	n := len(snippetLines)
	if n == 0 || len(cand) < n {
		return 0, 0, false
	}

	bestIdx, bestDist := -1, 0
	for i := 0; i+n <= len(cand); i++ {
		ok := true
		for j := 0; j < n; j++ {
			// 新文件行号必须连续（间隔 >1 说明跨越了未变更区间/hunk 边界）
			if cand[i+j].NewLineNo != cand[i].NewLineNo+j {
				ok = false
				break
			}
			if strings.TrimSpace(stripDiffPrefix(cand[i+j].Raw, cand[i+j].Type)) != snippetLines[j] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		dist := 0
		if anchor > 0 {
			dist = absInt(anchor - cand[i].NewLineNo)
		}
		if bestIdx == -1 || dist < bestDist {
			bestIdx, bestDist = i, dist
		}
	}
	if bestIdx == -1 {
		return 0, 0, false
	}
	return cand[bestIdx].NewLineNo, cand[bestIdx+n-1].NewLineNo, true
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// matchLine 在文件中查找与单行文本 text 最匹配的行：
// 先精确匹配，失败后按编辑距离模糊匹配（低于阈值视为不匹配）。
func (lc *LineCorrelator) matchLine(file *ParsedDiffFile, text string) (*DiffLine, MatchType, float64, string) {
	if line := lc.findExact(file, text); line != nil {
		return line, MatchTypeExact, 1.0, line.Raw
	}
	line := lc.findFuzzy(file, text)
	if line == nil {
		return nil, MatchTypeNotAvailable, 0, ""
	}
	conf := fuzzyConfidence(strings.TrimSpace(text), stripDiffPrefix(line.Raw, line.Type))
	if conf < lc.minFuzzyConfidence {
		return nil, MatchTypeNotAvailable, conf, ""
	}
	return line, MatchTypeFuzzy, conf, line.Raw
}

// fuzzyConfidence 计算两段文本的相似度（1 - 编辑距离 / 最大长度）
func fuzzyConfidence(a, b string) float64 {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	maxLen := max(len([]rune(a)), len([]rune(b)))
	if maxLen == 0 {
		return 0
	}
	return 1.0 - float64(levenshteinDistance(a, b))/float64(maxLen)
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

// maxSnippetLen 模糊匹配时截断的最大 rune 数，防止超长单行导致 O(n²) 性能问题
const maxSnippetLen = 200

// levenshteinDistance 计算两个字符串的编辑距离（输入会按 rune 截断到 maxSnippetLen）
// 【R2 修复】对 rune 而非字节进行截断和计算，避免 CJK 字符被截断在字节中间产生乱码
func levenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) > maxSnippetLen {
		ra = ra[:maxSnippetLen]
	}
	if len(rb) > maxSnippetLen {
		rb = rb[:maxSnippetLen]
	}
	la := len(ra)
	lb := len(rb)
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
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			curr[j] = min(min(
				prev[j]+1,    // deletion
				curr[j-1]+1), // insertion
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
		"marker_kind":      result.MarkerKind.String(),
		"match_type":       result.MatchType,
		"confidence":       result.Confidence,
		"original_snippet": result.OriginalSnippet,
		"matched_snippet":  result.MatchedSnippet,
		"message":          result.Message,
	}
}
