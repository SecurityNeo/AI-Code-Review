package diff

import (
	"strings"
	"testing"
)

// makeHunk 辅助函数：从行列表构造 hunk（含 header）
func makeHunk(lines []string) Hunk {
	var dl []DiffLine
	hunkStart := 1
	for i, raw := range lines {
		t := LineTypeContext
		oldNo, newNo := 0, 0
		switch {
		case strings.HasPrefix(raw, "@@ "):
			t = LineTypeHunkHeader
		case strings.HasPrefix(raw, "+"):
			t = LineTypeAddition
			newNo = i + 100 // dummy
		case strings.HasPrefix(raw, "-"):
			t = LineTypeDeletion
			oldNo = i + 100
		case len(raw) == 0 || raw[0] == ' ' || raw[0] == '\t':
			t = LineTypeContext
			oldNo = i + 100
			newNo = i + 100
		}
		dl = append(dl, DiffLine{
			DiffOffset: i + 1,
			Raw:        raw,
			Type:       t,
			OldLineNo:  oldNo,
			NewLineNo:  newNo,
		})
		if t == LineTypeHunkHeader {
			hunkStart = i + 1
		}
	}
	return Hunk{Lines: dl, HeaderAt: hunkStart}
}

func TestSubtreeTruncator_KeepChanges(t *testing.T) {
	lines := []string{
		"@@ -1,10 +1,11 @@",
		" context1",
		" context2",
		" context3",
		" context4",
		"-deleted",
		"+added1",
		" context5",
		" context6",
		" context7",
		" context8",
	}
	h := makeHunk(lines)
	tr := NewSubtreeTruncator(SubtreeConfig{})
	result := tr.Truncate(h)

	if len(result) == 0 {
		t.Fatal("truncate returned empty")
	}
	// 应保留 header + 变更前后各 3 行 context
	hasHeader := result[0].Type == LineTypeHunkHeader
	if !hasHeader {
		t.Error("first line should be header")
	}

	// 检查变更行确实被保留
	foundAdded := false
	for _, l := range result {
		if l.Type == LineTypeAddition {
			foundAdded = true
		}
	}
	if !foundAdded {
		t.Error("added line should be kept")
	}
}

func TestSubtreeTruncator_NoChanges(t *testing.T) {
	lines := []string{
		"@@ -1,5 +1,5 @@",
		" ctx1",
		" ctx2",
		" ctx3",
		" ctx4",
		" ctx5",
	}
	h := makeHunk(lines)
	tr := NewSubtreeTruncator(SubtreeConfig{})
	result := tr.Truncate(h)

	if len(result) == 0 {
		t.Fatal("truncate returned empty")
	}
	// 无变更时保留 header + 首尾 2 行
	if result[0].Type != LineTypeHunkHeader {
		t.Error("first line should be header")
	}
}

func TestMiddleTruncator_Truncate(t *testing.T) {
	lines := []string{
		"@@ -1,20 +1,20 @@",
		" c1", " c2", " c3", " c4", " c5",
		"-del",
		"+add",
		" c6", " c7", " c8", " c9", " c10",
		" c11", " c12", " c13", " c14", " c15",
		" c16", " c17", " c18", " c19", " c20",
	}
	h := makeHunk(lines)
	tr := NewMiddleTruncator(MiddleConfig{
		MaxLinesPerHunk:    10,
		ContextLinesAround: 3,
	})
	result := tr.Truncate(h)

	// hunk 共 21 行，截断后应不超过 MaxLinesPerHunk(~10) + header
	if len(result) > 15 {
		t.Errorf("truncated hunk too large: %d lines", len(result))
	}
	// 应保留变更行
	foundChange := false
	for _, l := range result {
		if l.Type == LineTypeAddition || l.Type == LineTypeDeletion {
			foundChange = true
		}
	}
	if !foundChange {
		t.Error("change lines should be kept")
	}
}

func TestBuildTruncatedDiff(t *testing.T) {
	raw := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -1,5 +1,5 @@
 line1
 line2
-line3
+line3new
 line4
 line5
`
	parser := NewParser()
	files, _ := parser.Parse(raw)
	if len(files) != 1 {
		t.Fatal("expected 1 file")
	}

	tr := NewSubtreeTruncator(SubtreeConfig{})
	diff := BuildTruncatedDiff(files[0], tr)
	if diff == "" {
		t.Fatal("BuildTruncatedDiff returned empty")
	}
	// 应包含重建的 hunk header
	if !strings.Contains(diff, "@@") {
		t.Error("truncated diff should contain hunk header")
	}
	// 应包含 context 行
	if !strings.Contains(diff, "line1") {
		t.Error("truncated diff should contain context lines")
	}
	// 应包含变更行
	if !strings.Contains(diff, "line3new") {
		t.Error("truncated diff should contain added line")
	}
}

func TestRebuildHunkHeader(t *testing.T) {
	lines := []DiffLine{
		{DiffOffset: 1, Raw: "@@ -10,5 +10,5 @@ func Foo() {", Type: LineTypeHunkHeader},
		{DiffOffset: 2, Raw: " ctx1", Type: LineTypeContext, OldLineNo: 10, NewLineNo: 10},
		{DiffOffset: 3, Raw: "-del", Type: LineTypeDeletion, OldLineNo: 11},
		{DiffOffset: 4, Raw: "+add", Type: LineTypeAddition, NewLineNo: 11},
		{DiffOffset: 5, Raw: " ctx2", Type: LineTypeContext, OldLineNo: 12, NewLineNo: 12},
	}
	newHunk := rebuildHunkHeader(lines)
	if newHunk.OldCount != 3 { // ctx1 + del + ctx2
		t.Errorf("OldCount = %d, want 3", newHunk.OldCount)
	}
	if newHunk.NewCount != 3 { // ctx1 + add + ctx2
		t.Errorf("NewCount = %d, want 3", newHunk.NewCount)
	}
	if !strings.HasPrefix(newHunk.Lines[0].Raw, "@@ -10,3 +10,3 @@") {
		t.Errorf("header = %q, want @@ -10,3 +10,3 @@", newHunk.Lines[0].Raw)
	}
}

func TestRebuildHunkHeader_Empty(t *testing.T) {
	newHunk := rebuildHunkHeader(nil)
	if len(newHunk.Lines) != 0 {
		t.Error("empty input should return empty hunk")
	}
}

func TestRebuildHunkHeader_NoHeader(t *testing.T) {
	lines := []DiffLine{
		{DiffOffset: 1, Raw: " ctx1", Type: LineTypeContext},
		{DiffOffset: 2, Raw: "-del", Type: LineTypeDeletion},
	}
	newHunk := rebuildHunkHeader(lines)
	if len(newHunk.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(newHunk.Lines))
	}
	if newHunk.Lines[0].Raw != " ctx1" {
		t.Error("first line should remain unchanged when no header present")
	}
}

func TestTruncationType(t *testing.T) {
	st := NewSubtreeTruncator(SubtreeConfig{})
	if st.Type() != TruncationTypeSubtree {
		t.Error("SubtreeTruncator type mismatch")
	}
	mt := NewMiddleTruncator(MiddleConfig{})
	if mt.Type() != TruncationTypeMiddle {
		t.Error("MiddleTruncator type mismatch")
	}

	nt := NewNoChangeTruncator()
	if nt.Type() != TruncationTypeNoChange {
		t.Error("NoChangeTruncator type mismatch")
	}

	// NoChangeTruncator 应返回原始 hunk 的完整副本
	lines := []string{
		"@@ -1,3 +1,4 @@",
		" line1",
		"+add",
		" line2",
	}
	h := makeHunk(lines)
	result := nt.Truncate(h)
	if len(result) != len(h.Lines) {
		t.Errorf("NoChangeTruncator should return all %d lines, got %d", len(h.Lines), len(result))
	}
	// 验证返回的是副本（修改 result 不应影响原始 hunk）
	result[0].Raw = "modified"
	if h.Lines[0].Raw == "modified" {
		t.Error("NoChangeTruncator should return a copy, not original slice")
	}
}
