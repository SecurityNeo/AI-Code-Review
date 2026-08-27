package diff

import (
	"fmt"
	"strings"
)

// TruncationType 截断策略类型
type TruncationType int

const (
	TruncationTypeNoChange TruncationType = iota // 保留完整 diff，不做截断
	TruncationTypeSubtree                        // 保留变更子树（标准截断）
	TruncationTypeMiddle                         // 大 hunk 中间保留（超长 diff）
)

// SubtreeConfig SubtreeTruncator 配置
type SubtreeConfig struct {
	TotalMaxTokens      int // diff 总体最大 token 数（触发截断的阈值）
	SkipContextIfLarge  bool
}

// MiddleConfig MiddleTruncator 配置
type MiddleConfig struct {
	MaxLinesPerHunk     int // 每个 hunk 最多保留行数
	ContextLinesAround  int // 变更行前后保留的 context 行数
	Sep                 string
}

// HunkTruncator 接口
type HunkTruncator interface {
	// Truncate 对单个 hunk 执行截断，返回截断后的行列表
	Truncate(hunk Hunk) []DiffLine
	// Type 返回截断类型
	Type() TruncationType
}

// SubtreeTruncator 保留变更子树 + 上下 context
type SubtreeTruncator struct {
	config SubtreeConfig
}

// NewSubtreeTruncator 创建子树截断器
func NewSubtreeTruncator(cfg SubtreeConfig) *SubtreeTruncator {
	return &SubtreeTruncator{config: cfg}
}

// Type implements HunkTruncator
func (s *SubtreeTruncator) Type() TruncationType { return TruncationTypeSubtree }

// Truncate 保留变更行及其直接上下文，省略远离变更的 context
func (s *SubtreeTruncator) Truncate(hunk Hunk) []DiffLine {
	// 找到 hunk 中所有变更行（addition/deletion）的索引
	// 粗略估计变更行不超过总行数的 1/3，避免频繁扩容
	changeIndices := make([]int, 0, len(hunk.Lines)/3)
	for i, line := range hunk.Lines {
		if line.Type == LineTypeAddition || line.Type == LineTypeDeletion {
			changeIndices = append(changeIndices, i)
		}
	}

	if len(changeIndices) == 0 {
		// 没有变更行，保留 hunk header + 首尾少量 context
		return s.keepHeaderAndEnds(hunk, 2)
	}

	// 默认保留每处变更前后各 3 行 context（可根据 token 动态调整）
	contextWindow := 3
	selected := make(map[int]bool)

	// header 行始终保留
	selected[0] = true

	for _, idx := range changeIndices {
		// 变更行本身
		selected[idx] = true
		// 前后 context
		for j := idx - contextWindow; j <= idx+contextWindow; j++ {
			if j >= 0 && j < len(hunk.Lines) {
				selected[j] = true
			}
		}
	}

	// 按顺序收集选中的行
	var result []DiffLine
	for i := range hunk.Lines {
		if selected[i] {
			result = append(result, hunk.Lines[i])
		}
	}
	return result
}

func (s *SubtreeTruncator) keepHeaderAndEnds(hunk Hunk, n int) []DiffLine {
	if len(hunk.Lines) <= n*2+1 {
		return append([]DiffLine(nil), hunk.Lines...)
	}
	var result []DiffLine
	result = append(result, hunk.Lines[0]) // header
	result = append(result, hunk.Lines[1:min(1+n, len(hunk.Lines))]...)
	endStart := max(1, len(hunk.Lines)-n)
	result = append(result, hunk.Lines[endStart:]...)
	return result
}

// NoChangeTruncator 不做截断的截断器（恒等变换）
type NoChangeTruncator struct{}

// NewNoChangeTruncator 创建恒等截断器
func NewNoChangeTruncator() *NoChangeTruncator { return &NoChangeTruncator{} }

// Type implements HunkTruncator
func (n *NoChangeTruncator) Type() TruncationType { return TruncationTypeNoChange }

// Truncate 直接返回原始 hunk 的所有行（不做任何截断）
func (n *NoChangeTruncator) Truncate(hunk Hunk) []DiffLine {
	result := make([]DiffLine, len(hunk.Lines))
	copy(result, hunk.Lines)
	return result
}

// MiddleTruncator 大 hunk 截断器：保留中间变更，省略首尾大量 context
type MiddleTruncator struct {
	config MiddleConfig
}

// NewMiddleTruncator 创建中间截断器
func NewMiddleTruncator(cfg MiddleConfig) *MiddleTruncator {
	return &MiddleTruncator{config: cfg}
}

// Type implements HunkTruncator
func (m *MiddleTruncator) Type() TruncationType { return TruncationTypeMiddle }

// Truncate 保留 hunk 中间最多 MaxLinesPerHunk 行，省略多余的首尾 context
func (m *MiddleTruncator) Truncate(hunk Hunk) []DiffLine {
	if len(hunk.Lines) <= m.config.MaxLinesPerHunk+m.config.ContextLinesAround*2+1 {
		return append([]DiffLine(nil), hunk.Lines...)
	}

	// 找到所有变更行的中心位置
	sumIdx := 0
	count := 0
	for i, line := range hunk.Lines {
		if line.Type == LineTypeAddition || line.Type == LineTypeDeletion {
			sumIdx += i
			count++
		}
	}
	if count == 0 {
		return append([]DiffLine(nil), hunk.Lines...)
	}

	center := sumIdx / count
	halfLimit := m.config.MaxLinesPerHunk / 2
	start := max(1, center-halfLimit) // 1 保留 header
	end := min(len(hunk.Lines), start+m.config.MaxLinesPerHunk)

	var result []DiffLine
	result = append(result, hunk.Lines[0]) // header
	result = append(result, hunk.Lines[start:end]...)
	return result
}

// BuildTruncatedDiff 将截断后的 diff 文件重建为字符串
func BuildTruncatedDiff(file *ParsedDiffFile, truncator HunkTruncator) string {
	var parts []string
	for _, hunk := range file.Hunks {
		truncated := truncator.Truncate(hunk)
		if len(truncated) == 0 {
			continue
		}
		// 重建 hunk header（因为行数变了，需要重新计算 count）
		newHunk := rebuildHunkHeader(truncated)
		for _, line := range newHunk.Lines {
			parts = append(parts, line.Raw)
		}
	}
	return strings.Join(parts, "\n")
}

// rebuildHunkHeader 根据截断后的行列表重建正确的 @@ header
func rebuildHunkHeader(lines []DiffLine) Hunk {
	if len(lines) == 0 {
		return Hunk{}
	}

	// 如果第一行不是 header，返回原样
	if lines[0].Type != LineTypeHunkHeader {
		return Hunk{Lines: lines}
	}

	// 重新计算新的 old/new count
	oldCount := 0
	newCount := 0
	for _, l := range lines[1:] { // 跳过 header 行
		switch l.Type {
		case LineTypeContext:
			oldCount++
			newCount++
		case LineTypeAddition:
			newCount++
		case LineTypeDeletion:
			oldCount++
		}
	}

	// 解析旧的 header 获取 start 位置和上下文
	header := lines[0].Raw
	m := hunkHeaderRe.FindStringSubmatch(header)
	if m == nil {
		// header 格式异常，返回原样
		return Hunk{Lines: lines}
	}
	oldStart := mustAtoi(m[1])
	newStart := mustAtoi(m[3])
	ctx := strings.TrimSpace(m[5])

	newHeader := fmt.Sprintf("@@ -%d,%d +%d,%d @@ %s", oldStart, oldCount, newStart, newCount, ctx)

	newLines := make([]DiffLine, len(lines))
	copy(newLines, lines)
	newLines[0] = DiffLine{
		DiffOffset: lines[0].DiffOffset,
		Raw:        newHeader,
		Type:       LineTypeHunkHeader,
	}

	return Hunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
		Context:  ctx,
		Lines:    newLines,
		HeaderAt: lines[0].DiffOffset,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
