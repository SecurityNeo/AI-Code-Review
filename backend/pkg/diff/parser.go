package diff

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// hunkHeaderRe 匹配：@@ -45,7 +45,9 @@ func ProcessOrder(...)
	hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

	// fileHeaderRe 匹配：diff --git a/src/foo.go b/src/foo.go
	fileHeaderRe = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)
)

// Parser diff 解析器
type Parser struct {
	warnings []string
}

// NewParser 创建解析器
func NewParser() *Parser {
	return &Parser{}
}

// Parse 解析完整的 unified diff 文本
func (p *Parser) Parse(rawDiff string) ([]*ParsedDiffFile, error) {
	if len(strings.TrimSpace(rawDiff)) == 0 {
		return nil, nil
	}

	// 规范化换行符
	rawDiff = strings.ReplaceAll(rawDiff, "\r\n", "\n")
	if !strings.HasPrefix(rawDiff, "diff --git ") {
		rawDiff = "\n" + rawDiff
	}

	// 按 "\ndiff --git " 分割为多个文件块
	parts := strings.Split(rawDiff, "\ndiff --git ")

	var files []*ParsedDiffFile
	for i, part := range parts {
		if len(strings.TrimSpace(part)) == 0 {
			continue
		}
		if i > 0 {
			part = "diff --git " + part
		}
		file, err := p.parseFile(part)
		if err != nil {
			return nil, fmt.Errorf("parse file: %w", err)
		}
		if file != nil {
			files = append(files, file)
		}
	}
	return files, nil
}

// parseFile 解析单个文件的 diff（单遍扫描）
func (p *Parser) parseFile(raw string) (*ParsedDiffFile, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return nil, nil
	}

	file := &ParsedDiffFile{RawDiff: raw}

	// 阶段一：解析文件头，定位到第一个 hunk 的位置
	var hunkStartIdx int
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			m := fileHeaderRe.FindStringSubmatch(line)
			if m != nil {
				file.OldPath = m[1]
				file.NewPath = m[2]
			}
		case strings.HasPrefix(line, "old mode "):
			file.OldFileMode = strings.TrimPrefix(line, "old mode ")
		case strings.HasPrefix(line, "new mode "):
			file.NewFileMode = strings.TrimPrefix(line, "new mode ")
		case strings.HasPrefix(line, "deleted file mode "):
			file.IsDeleted = true
			file.OldFileMode = strings.TrimPrefix(line, "deleted file mode ")
		case strings.HasPrefix(line, "new file mode "):
			file.IsNewFile = true
			file.NewFileMode = strings.TrimPrefix(line, "new file mode ")
		case strings.HasPrefix(line, "rename from "):
			file.IsRenamed = true
			file.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			file.NewPath = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "similarity index "):
			// no-op
		case strings.HasPrefix(line, "--- "):
			pathPart := strings.TrimPrefix(line, "--- ")
			if pathPart == "/dev/null" {
				file.OldPath = "/dev/null"
			} else if strings.HasPrefix(pathPart, "a/") {
				file.OldPath = strings.TrimPrefix(pathPart, "a/")
			}
		case strings.HasPrefix(line, "+++ "):
			pathPart := strings.TrimPrefix(line, "+++ ")
			if pathPart == "/dev/null" {
				file.NewPath = "/dev/null"
			} else if strings.HasPrefix(pathPart, "b/") {
				file.NewPath = strings.TrimPrefix(pathPart, "b/")
			}
		case strings.HasPrefix(line, "@@ "):
			// 文件头结束，从此处开始解析 hunk
			hunkStartIdx = i
			goto parseHunks
		}
	}

parseHunks:
	// 阶段二：解析所有 hunk（单遍扫描）
	for i := hunkStartIdx; i < len(lines); i++ {
		line := lines[i]
		currentLineNo := i + 1

		switch {
		case strings.HasPrefix(line, "@@ "):
			// 遇到了一个新的 hunk header
			hunk, consumedCount, err := p.readHunk(lines[i:], currentLineNo)
			if err != nil {
				return nil, fmt.Errorf("parse hunk at line %d: %w", currentLineNo, err)
			}
			file.Hunks = append(file.Hunks, *hunk)
			file.Lines = append(file.Lines, hunk.Lines...)
			i += consumedCount - 1 // 跳过已被 hunk 消费的行（-1 因为 for 循环会执行 i++）

		case strings.HasPrefix(line, "diff --git "):
			// 遇到下一个文件的开始（理论上不会发生，因为外层已按文件分割）
			goto done

		case strings.HasPrefix(line, "\\ "):
			// "\ No newline at end of file" 标记行
			file.HasNoNewLine = true
			file.Lines = append(file.Lines, DiffLine{
				DiffOffset: currentLineNo,
				Raw:        line,
				Type:       LineTypeUnknown,
			})

		default:
			// 其他非 hunk 行，忽略（如空行分割等）
		}
	}

done:
	// 统计 additions / deletions
	for _, line := range file.Lines {
		switch line.Type {
		case LineTypeAddition:
			file.Additions++
		case LineTypeDeletion:
			file.Deletions++
		}
	}

	return file, nil
}

// readHunk 从一个 hunk header 开始读取完整的 hunk，返回 (hunk, 消费行数, error)
func (p *Parser) readHunk(lines []string, headerLineNo int) (*Hunk, int, error) {
	if len(lines) == 0 {
		return nil, 0, fmt.Errorf("empty hunk")
	}

	headerLine := lines[0]
	m := hunkHeaderRe.FindStringSubmatch(headerLine)
	if m == nil {
		return nil, 0, fmt.Errorf("invalid hunk header: %s", headerLine)
	}

	oldStart := mustAtoi(m[1])
	oldLines := 1
	if m[2] != "" {
		oldLines = mustAtoi(m[2])
	}
	newStart := mustAtoi(m[3])
	newLines := 1
	if m[4] != "" {
		newLines = mustAtoi(m[4])
	}
	context := strings.TrimSpace(m[5])

	hunk := &Hunk{
		OldStart: oldStart,
		OldCount: oldLines,
		NewStart: newStart,
		NewCount: newLines,
		Context:  context,
		HeaderAt: headerLineNo,
	}

	// hunk header 行本身
	headerDiffLine := DiffLine{
		DiffOffset: headerLineNo,
		Raw:        headerLine,
		Type:       LineTypeHunkHeader,
		OldLineNo:  0,
		NewLineNo:  0,
	}
	hunk.Lines = append(hunk.Lines, headerDiffLine)

	oldLine := oldStart
	newLine := newStart
	consumedCount := 1 // header 行已消费

	for i := 1; i < len(lines); i++ {
		line := lines[i]

		// 检查是否是下一个 hunk 或下一个文件
		if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "diff --git ") {
			break
		}

		// "\ No newline at end of file" → 标记行，不属于 hunk 内容
		// 让外层循环处理（设置 file.HasNoNewLine）
		if strings.HasPrefix(line, "\\ ") {
			break
		}

		currentLineNo := headerLineNo + i
		consumedCount++

			// 获取第一个可见字符用于判断行类型
		trimmedLeft := strings.TrimLeft(line, "\t")
		if len(trimmedLeft) == 0 {
			// 纯空白行（如全 tab 或全空格），当作 context
			dl := DiffLine{
				DiffOffset: currentLineNo,
				Raw:        line,
				Type:       LineTypeContext,
				OldLineNo:  oldLine,
				NewLineNo:  newLine,
			}
			oldLine++
			newLine++
			hunk.Lines = append(hunk.Lines, dl)
			continue
		}

		firstChar := trimmedLeft[0]
		switch firstChar {
		case '+':
			dl := DiffLine{
				DiffOffset: currentLineNo,
				Raw:        line,
				Type:       LineTypeAddition,
				OldLineNo:  0, // 新增行在旧文件中不存在
				NewLineNo:  newLine,
			}
			newLine++
			hunk.Lines = append(hunk.Lines, dl)

		case '-':
			dl := DiffLine{
				DiffOffset: currentLineNo,
				Raw:        line,
				Type:       LineTypeDeletion,
				OldLineNo:  oldLine,
				NewLineNo:  0, // 删除行在新文件中不存在
			}
			oldLine++
			hunk.Lines = append(hunk.Lines, dl)

		case ' ':
			dl := DiffLine{
				DiffOffset: currentLineNo,
				Raw:        line,
				Type:       LineTypeContext,
				OldLineNo:  oldLine,
				NewLineNo:  newLine,
			}
			oldLine++
			newLine++
			hunk.Lines = append(hunk.Lines, dl)

		default:
			// 遇到非 diff 内容行，视为 hunk 结束
			goto hunkEnd
		}
	}

hunkEnd:
	p.validateHunk(hunk)
	return hunk, consumedCount, nil
}

// validateHunk 校验 hunk 行数是否与 header 声明一致
func (p *Parser) validateHunk(hunk *Hunk) {
	actualOld := 0
	actualNew := 0
	for _, l := range hunk.Lines {
		if l.IsOldLine() {
			actualOld++
		}
		if l.IsNewLine() {
			actualNew++
		}
	}
	if actualOld != hunk.OldCount || actualNew != hunk.NewCount {
		p.warnings = append(p.warnings, fmt.Sprintf(
			"hunk count mismatch at %d: old expect=%d actual=%d, new expect=%d actual=%d",
			hunk.HeaderAt, hunk.OldCount, actualOld, hunk.NewCount, actualNew,
		))
	}
}

// Warnings 返回解析过程中的警告
func (p *Parser) Warnings() []string {
	return p.warnings
}

func mustAtoi(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
