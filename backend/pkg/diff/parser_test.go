package diff

import (
	"strings"
	"testing"
)

func TestNewParser(t *testing.T) {
	p := NewParser()
	if p == nil {
		t.Fatal("NewParser() returned nil")
	}
}

func TestParse_Empty(t *testing.T) {
	p := NewParser()
	files, err := p.Parse("")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

func TestParse_SimpleAddition(t *testing.T) {
	raw := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package main
 
 func main() {
+	fmt.Println("hello")
 }
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0]
	if f.NewPath != "foo.go" {
		t.Errorf("NewPath = %q, want %q", f.NewPath, "foo.go")
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(f.Hunks))
	}

	h := f.Hunks[0]
	if h.NewStart != 1 {
		t.Errorf("NewStart = %d, want 1", h.NewStart)
	}
	if h.NewCount != 4 {
		t.Errorf("NewCount = %d, want 4", h.NewCount)
	}

	// 找到新增行
	var addedLine *DiffLine
	for i := range f.Lines {
		if f.Lines[i].Type == LineTypeAddition {
			addedLine = &f.Lines[i]
			break
		}
	}
	if addedLine == nil {
		t.Fatal("no addition line found")
	}
	// 新增行前有 package main（context）、空行（context）、func main() {（context）
	// 因此 +fmt.Println("hello") 在新文件中的行号为 4
	if addedLine.NewLineNo != 4 {
		t.Errorf("added line NewLineNo = %d, want 4", addedLine.NewLineNo)
	}
	if addedLine.OldLineNo != 0 {
		t.Errorf("added line OldLineNo = %d, want 0", addedLine.OldLineNo)
	}
}

func TestParse_NewFile(t *testing.T) {
	raw := `diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,3 @@
+package main
+
+func New() {}
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0]
	if !f.IsNewFile {
		t.Error("expected IsNewFile = true")
	}
	if f.OldPath != "/dev/null" {
		t.Errorf("OldPath = %q, want /dev/null", f.OldPath)
	}

	// 所有新文件的行，old_line 都应大于 0（git diff 新文件从 -0,0 开始）
	// 实际上新增文件 diff 中旧行数为 0，但 context 行的 old_line 可能从 1 开始
	// 这里我们只验证新增行的 OldLineNo
	for _, l := range f.Lines {
		if l.Type == LineTypeAddition && l.OldLineNo != 0 {
			t.Errorf("new file addition line has OldLineNo=%d, want 0", l.OldLineNo)
		}
	}
}

func TestParse_DeletedFile(t *testing.T) {
	raw := `diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1,5 +0,0 @@
-package main
-
-func Old() {}
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0]
	if !f.IsDeleted {
		t.Error("expected IsDeleted = true")
	}

	// 删除文件的行，NewLineNo 应为 0
	for _, l := range f.Lines {
		if l.Type == LineTypeDeletion && l.NewLineNo != 0 {
			t.Errorf("deleted file deletion line has NewLineNo=%d, want 0", l.NewLineNo)
		}
	}
}

func TestParse_RenamedFile(t *testing.T) {
	raw := `diff --git a/old.go b/new.go
similarity index 98%
rename from old.go
rename to new.go
--- a/old.go
+++ b/new.go
@@ -10,5 +10,6 @@ func Foo() {
      line1
      line2
      line3
+	added
 }
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0]
	if !f.IsRenamed {
		t.Error("expected IsRenamed = true")
	}
	if f.OldPath != "old.go" {
		t.Errorf("OldPath = %q, want old.go", f.OldPath)
	}
	if f.NewPath != "new.go" {
		t.Errorf("NewPath = %q, want new.go", f.NewPath)
	}
}

func TestParse_MultiHunk(t *testing.T) {
	raw := `diff --git a/multi.go b/multi.go
--- a/multi.go
+++ b/multi.go
@@ -1,3 +1,4 @@
 package main

 func Foo() {
+	var x int
 }
@@ -20,5 +21,6 @@ func Bar() {
 	line1
 	line2
 	line3
+	line4
 }
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0]
	if len(f.Hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(f.Hunks))
	}

	// 第一个 hunk 在新文件中的行号范围应为 1-4
	h1 := f.Hunks[0]
	if h1.NewStart != 1 {
		t.Errorf("h1.NewStart = %d, want 1", h1.NewStart)
	}

	// 第二个 hunk 在新文件中的行号
	h2 := f.Hunks[1]
	if h2.NewStart != 21 {
		t.Errorf("h2.NewStart = %d, want 21", h2.NewStart)
	}
}

func TestParse_BinaryFile(t *testing.T) {
	raw := `diff --git a/logo.png b/logo.png
Binary files differ
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0]
	if len(f.Hunks) != 0 {
		t.Errorf("expected 0 hunks for binary file, got %d", len(f.Hunks))
	}
}

func TestParse_WithContextLines(t *testing.T) {
	// 验证 context 行号是否正确递増
	raw := `diff --git a/calc.go b/calc.go
--- a/calc.go
+++ b/calc.go
@@ -10,7 +10,8 @@ func Add(a, b int) int {
 	if a < 0 {
 		return 0
 	}
+	// validate b
 	if b < 0 {
 		return 0
 	}
 }
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	f := files[0]
	if len(f.Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(f.Hunks))
	}

	// context 行的 old/new 行号应对应
	var contextLines int
	for _, l := range f.Lines {
		if l.Type == LineTypeContext {
			contextLines++
			if l.OldLineNo == 0 || l.NewLineNo == 0 {
				t.Errorf("context line should have both old and new line numbers, got old=%d new=%d",
					l.OldLineNo, l.NewLineNo)
			}
		}
	}
	if contextLines == 0 {
		t.Error("expected some context lines")
	}
}

func TestParse_CarriageReturn(t *testing.T) {
	// 测试 \r\n 换行符处理
	raw := "diff --git a/a.go b/a.go\r\n--- a/a.go\r\n+++ b/a.go\r\n@@ -1,2 +1,2 @@\r\n line1\r\n-line2\r\n+line2fixed\r\n"
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	// 确认 \r 已被规范化移除
	for _, l := range files[0].Lines {
		if strings.Contains(l.Raw, "\r") {
			t.Errorf("line still contains \\r: %q", l.Raw)
		}
	}
}

func TestParse_FindHunkByNewLine(t *testing.T) {
	raw := `diff --git a/multi.go b/multi.go
--- a/multi.go
+++ b/multi.go
@@ -1,3 +1,4 @@
 package main
 
 func Foo() {
+	var x int
 }
@@ -20,5 +21,6 @@ func Bar() {
 	line1
 }
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	f := files[0]

	// line 2 应在第一个 hunk 中
	h := f.FindHunkByNewLine(2)
	if h == nil {
		t.Fatal("FindHunkByNewLine(2) returned nil")
	}
	if h.NewStart != 1 {
		t.Errorf("found hunk NewStart = %d, want 1", h.NewStart)
	}

	// line 22 应在第二个 hunk 中（第二个 hunk NewStart=21，覆盖 line1 的 context 行）
	h = f.FindHunkByNewLine(22)
	if h == nil {
		t.Fatal("FindHunkByNewLine(22) returned nil")
	}
	if h.NewStart != 21 {
		t.Errorf("found hunk NewStart = %d, want 21", h.NewStart)
	}

	// line 100 不应找到
	h = f.FindHunkByNewLine(100)
	if h != nil {
		t.Error("FindHunkByNewLine(100) should return nil")
	}
}

func TestParse_NoNewLineAtEnd(t *testing.T) {
	raw := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
 line1
-line2
+line2fixed
\ No newline at end of file
`
	p := NewParser()
	files, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	f := files[0]
	if !f.HasNoNewLine {
		t.Error("expected HasNoNewLine = true")
	}
}

func TestParse_ParserWarnings(t *testing.T) {
	// 测试一个可能导致行数不匹配的 diff（手动构造的不规范 diff）
	// 这里只测试 Warnings() 调用不 panic
	p := NewParser()
	_ = p.Warnings() // 不应 panic
}
