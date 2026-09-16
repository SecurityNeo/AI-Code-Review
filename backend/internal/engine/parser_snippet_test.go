package engine

import (
	"strings"
	"testing"
)

// 分批评审/Agentic 解析入口应清洗 code_snippet 中的 [new|old] 注解
func TestParseBatchReviewResult_StripsLineAnnotations(t *testing.T) {
	content := `{"batch_notes":"n","issues":[{"rule_code":"r","severity":"high","category":"security","file":"a.go","line_start":1,"line_end":1,"code_snippet":"[new1|old1]  foo(); <<< 问题所在","message":"m","suggestion":"s"}],"recommendations":[]}`

	res, err := ParseBatchReviewResult(content, DefaultDeductScoreConfig())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(res.Issues))
	}
	got := res.Issues[0].CodeSnippet
	if strings.Contains(got, "[new") {
		t.Fatalf("annotation not stripped: %q", got)
	}
	if !strings.Contains(got, "foo();") || !strings.Contains(got, "问题所在") {
		t.Fatalf("unexpected snippet: %q", got)
	}
}

// 单批/裁决解析入口（postParse）应清洗 code_snippet
func TestParseReviewResult_StripsLineAnnotations(t *testing.T) {
	content := `{"schema_version":"1","total_score":100,"dimensions":{},"summary":"s","issues":[{"rule_code":"r","severity":"low","category":"security","file":"a.go","line_start":2,"line_end":2,"code_snippet":"[new2|old2] bar();","message":"m","suggestion":"s"}],"recommendations":[]}`

	res, err := ParseReviewResult(content, nil, nil, DefaultDeductScoreConfig())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(res.Issues))
	}
	if got := res.Issues[0].CodeSnippet; strings.Contains(got, "[new") {
		t.Fatalf("annotation not stripped: %q", got)
	}
}
