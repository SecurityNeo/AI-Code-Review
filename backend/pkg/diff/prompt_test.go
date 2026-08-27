package diff

import (
	"strings"
	"testing"
)

func TestBuildPrompt_WithLineNumbers(t *testing.T) {
	raw := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -1,3 +1,4 @@
 line1
 line2
-line3
+line3new
 line4
`
	parser := NewParser()
	files, _ := parser.Parse(raw)
	if len(files) != 1 {
		t.Fatal("expected 1 file")
	}

	cfg := PromptConfig{Enabled: true, InjectLineNumbers: true}
	prompt := BuildPrompt(files[0], cfg)

	// 应包含注入行号前缀
	if !strings.Contains(prompt, "[new1|old1]") {
		t.Error("prompt should contain context line prefix")
	}
	if !strings.Contains(prompt, "[new-|old3]") {
		t.Error("prompt should contain deletion line prefix with old line number")
	}
	if !strings.Contains(prompt, "[new3|old-]") {
		t.Error("prompt should contain addition line prefix")
	}
}

func TestBuildPrompt_Disabled(t *testing.T) {
	raw := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -1,3 +1,4 @@
 line1
 line2
-line3
+line3new
 line4
`
	parser := NewParser()
	files, _ := parser.Parse(raw)
	cfg := PromptConfig{Enabled: false}
	prompt := BuildPrompt(files[0], cfg)

	// 禁用时返回原始 diff
	if prompt != raw {
		t.Error("disabled prompt should return raw diff")
	}
}

func TestBuildPrompt_NoInjection(t *testing.T) {
	raw := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -1,3 +1,4 @@
 line1
`
	parser := NewParser()
	files, _ := parser.Parse(raw)
	cfg := PromptConfig{Enabled: true, InjectLineNumbers: false}
	prompt := BuildPrompt(files[0], cfg)

	// 不应有行号前缀
	if strings.Contains(prompt, "[new") {
		t.Error("prompt without injection should not contain line number markers")
	}
}

func TestBuildPromptWithTruncation(t *testing.T) {
	raw := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -1,3 +1,4 @@
 line1
 line2
-line3
+line3new
 line4
`
	parser := NewParser()
	files, _ := parser.Parse(raw)
	tr := NewSubtreeTruncator(SubtreeConfig{})
	cfg := PromptConfig{Enabled: true, InjectLineNumbers: true}
	prompt := BuildPromptWithTruncation(files[0], tr, cfg)

	if prompt == "" {
		t.Fatal("prompt should not be empty")
	}
	// 截断后的 diff 也应包含行号
	if !strings.Contains(prompt, "[new-|old3]") {
		t.Error("truncated prompt should still contain line number markers")
	}
}

func TestBuildDiffLineInfoSlice(t *testing.T) {
	lines := []DiffLine{
		{NewLineNo: 5, OldLineNo: 0, Type: LineTypeAddition},
		{NewLineNo: 6, OldLineNo: 6, Type: LineTypeContext},
		{NewLineNo: 0, OldLineNo: 7, Type: LineTypeDeletion},
		{Type: LineTypeHunkHeader}, // should be skipped
	}
	infos := BuildDiffLineInfoSlice(lines)
	if len(infos) != 3 {
		t.Fatalf("expected 3 infos, got %d", len(infos))
	}
	if infos[0].N != 5 || infos[0].O != 0 || infos[0].T != "+" {
		t.Errorf("addition info mismatch: %+v", infos[0])
	}
	if infos[1].N != 6 || infos[1].O != 6 || infos[1].T != "c" {
		t.Errorf("context info mismatch: %+v", infos[1])
	}
	if infos[2].N != 0 || infos[2].O != 7 || infos[2].T != "-" {
		t.Errorf("deletion info mismatch: %+v", infos[2])
	}
}

func TestExtractSnippet(t *testing.T) {
	lines := []string{
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
	}
	snippet := ExtractSnippet(lines, 2, 4)
	expected := "line2\nline3\nline4"
	if snippet != expected {
		t.Errorf("snippet = %q, want %q", snippet, expected)
	}
}

func TestExtractSnippet_OutOfRange(t *testing.T) {
	lines := []string{"line1"}
	snippet := ExtractSnippet(lines, 1, 10)
	if snippet != "line1" {
		t.Errorf("snippet = %q, want line1", snippet)
	}
}
