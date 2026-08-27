package diff

// LineType 表示 diff 中一行的类型
type LineType int

const (
	LineTypeUnknown LineType = iota
	LineTypeContext           // 未变更行 (前缀 ' ')
	LineTypeAddition          // 新增行 (前缀 '+')
	LineTypeDeletion          // 删除行 (前缀 '-')
	LineTypeHunkHeader        // @@ 行号头
	LineTypeFileHeader        // diff --git / --- / +++ 等
)

// DiffLine diff 中的单行结构化数据
type DiffLine struct {
	DiffOffset int      // 在 diff 文本中的行号（从 1 开始）
	Raw        string   // 原始行文本（含 +/-/ 前缀）
	Type       LineType // 行类型
	OldLineNo  int      // 在旧文件中的行号，0 表示不存在（如新增行）
	NewLineNo  int      // 在新文件中的行号，0 表示不存在（如删除行）
}

// IsOldLine 此行是否存在于旧版本中
func (dl *DiffLine) IsOldLine() bool {
	return dl.Type == LineTypeContext || dl.Type == LineTypeDeletion
}

// IsNewLine 此行是否存在于新版本中
func (dl *DiffLine) IsNewLine() bool {
	return dl.Type == LineTypeContext || dl.Type == LineTypeAddition
}

// Hunk diff 中的一个变更块（由 @@ header 标识）
type Hunk struct {
	OldStart int    // 旧文件中此 hunk 的起始行号
	OldCount int    // 旧文件中此 hunk 涉及行数
	NewStart int    // 新文件中此 hunk 的起始行号
	NewCount int    // 新文件中此 hunk 涉及行数
	Context  string // @@ 行尾的可选上下文（如函数名）
	Lines    []DiffLine // 此 hunk 中的所有行（含 header 行本身）
	HeaderAt int    // @@ header 行在整体 diff 中的行号
}

// NewFileStart 返回此 hunk 在新文件中的起始行号
func (h *Hunk) NewFileStart() int {
	return h.NewStart
}

// NewFileEnd 返回此 hunk 在新文件中的结束行号（含）
func (h *Hunk) NewFileEnd() int {
	last := h.NewStart
	for _, l := range h.Lines {
		if l.IsNewLine() {
			last = l.NewLineNo
		}
	}
	return last
}

// ParsedDiffFile 一个文件的完整结构化 diff
type ParsedDiffFile struct {
	OldPath      string // 原文件路径
	NewPath      string // 新文件路径（可能与 OldPath 相同）
	IsNewFile    bool   // 是否新增文件
	IsDeleted    bool   // 是否删除文件
	IsRenamed    bool   // 是否重命名
	OldFileMode  string // 旧文件模式（如 100644）
	NewFileMode  string // 新文件模式
	Hunks        []Hunk // 所有变更块
	Lines        []DiffLine // 扁平化的所有行（含 header）
	Additions    int    // 新增行数
	Deletions    int    // 删除行数
	HasNoNewLine bool   // diff 末尾是否有 "\ No newline at end of file"
	RawDiff      string // 原始 diff 文本（保留，用于重建）
}

// FindHunkByNewLine 根据新文件行号查找所属的 Hunk
func (pdf *ParsedDiffFile) FindHunkByNewLine(newLine int) *Hunk {
	for i := range pdf.Hunks {
		start := pdf.Hunks[i].NewStart
		end := pdf.Hunks[i].NewFileEnd()
		if newLine >= start && newLine <= end {
			return &pdf.Hunks[i]
		}
	}
	return nil
}

// BuildLineMap 构建行号映射表（供 Prompt 注入和序列化用）
func (pdf *ParsedDiffFile) BuildLineMap() []DiffLine {
	return pdf.Lines
}

// DiffLineInfo 用于序列化到数据库和注入 Prompt 的精简行号信息
// 使用短字段名控制 JSON 体积；不存 content（内容在 raw_diff 中已有）
type DiffLineInfo struct {
	N int    `json:"n"` // 新文件行号（new_line）
	O int    `json:"o"` // 旧文件行号（old_line，新增行为 0）
	T string `json:"t"` // 行类型: "c"=context, "+"=addition, "-"=deletion
}
