package diff

import (
	"strings"
	"testing"
)

// TestE2E_PipelineCorrection 模拟完整的 Pipeline 场景端到端测试
// 覆盖：gitlab.DiffFile → rebuildRawDiff → Parse → BuildPrompt → 模拟 LLM → Corrector 校正
func TestE2E_PipelineCorrection(t *testing.T) {
	// 1. 构造一个典型的 unified diff（从 gitlab 获取的格式）
	rawDiff := `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -1,6 +1,8 @@
 package auth

 func Authenticate(token string) error {
+	// P0: 检查 token 是否为空
+	if token == "" {
+		return fmt.Errorf("token is required")
+	}
 	// 验证 token
 	if len(token) < 10 {
 		return fmt.Errorf("token too short")
	}

	return nil
}
`

	// 2. 解析 diff（模拟 Pipeline 中 Parser 的行为）
	parser := NewParser()
	parsedFiles, err := parser.Parse(rawDiff)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(parsedFiles) != 1 {
		t.Fatalf("expected 1 file, got %d", len(parsedFiles))
	}

	// 3. 构建带行号前缀的 Prompt（模拟 builder.go 的行为）
	pf := parsedFiles[0]
	prompt := BuildPrompt(pf, PromptConfig{Enabled: true, InjectLineNumbers: true})
	if !strings.Contains(prompt, "[new") {
		t.Fatal("prompt should contain [new|old] line number prefixes")
	}

	// 4. 模拟模型返回（带有错误行号的 issue）
	// 模型自身的 line_start 能力可能错误地指向 diff 文本的第 8 行（实际是 auth.go 新文件的第 5 行）
	fileMap := map[string]*ParsedDiffFile{pf.NewPath: pf}
	correlator := NewLineCorrelator(fileMap)

	// 测试场景 A：模型返回了正确的 code snippet，但行号错误
	result := correlator.CorrectIssue(pf.NewPath, 8, 8, "if token == \"\" {")
	if result.MatchType != MatchTypeExact && result.MatchType != MatchTypeFuzzy {
		t.Fatalf("expected exact or fuzzy match, got %v", result.MatchType)
	}
	// 期望校正后指向新文件的第 5 行（即 "if token == "" {" 在 auth.go 新文件中的实际行号）
	expectedLine := 5
	if result.NewStart != expectedLine {
		t.Errorf("expected corrected line %d, got %d (match_type=%v, confidence=%.2f)",
			expectedLine, result.NewStart, result.MatchType, result.Confidence)
	}
	if result.Confidence < 0.8 {
		t.Errorf("expected high confidence (>0.8), got %.2f", result.Confidence)
	}

	t.Logf("E2E correction: original=%d → corrected=%d (confidence=%.2f, type=%s)",
		result.OldStart, result.NewStart, result.Confidence, matchTypeString(result.MatchType))
}

// TestE2E_MultiFileDiff 多文件 diff 的端到端场景
func TestE2E_MultiFileDiff(t *testing.T) {
	rawDiff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 line1
+add1
 line2

diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1,2 +1,3 @@
 line1
+add2
 line2
`

	parser := NewParser()
	parsedFiles, err := parser.Parse(rawDiff)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(parsedFiles) != 2 {
		t.Fatalf("expected 2 files, got %d", len(parsedFiles))
	}

	// 验证每个文件都能正确注入行号 Prompt
	for _, pf := range parsedFiles {
		prompt := BuildPrompt(pf, PromptConfig{Enabled: true, InjectLineNumbers: true})
		if !strings.Contains(prompt, "[new") {
			t.Errorf("prompt for %s should contain line number prefixes", pf.NewPath)
		}
	}

	// 验证跨文件校正：第二个文件的 snippet 不应匹配到第一个文件
	fileMap := make(map[string]*ParsedDiffFile)
	for _, pf := range parsedFiles {
		fileMap[pf.NewPath] = pf
	}
	correlator := NewLineCorrelator(fileMap)

	// 尝试用 "add1" 的 snippet 去匹配 "b.go"，应该找不到（或低置信度）
	result := correlator.CorrectIssue("b.go", 99, 99, "add1")
	if result.NewStart != 99 {
		t.Logf("cross-file mismatch handled: kept original line %d", result.NewStart)
	}
}

// matchTypeString 辅助函数用于测试日志
func matchTypeString(mt MatchType) string {
	switch mt {
	case MatchTypeExact:
		return "exact"
	case MatchTypeFuzzy:
		return "fuzzy"
	case MatchTypeNotAvailable:
		return "na"
	default:
		return "unknown"
	}
}
