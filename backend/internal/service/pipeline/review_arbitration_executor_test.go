package pipeline

import (
	"strings"
	"testing"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
)

// mkBatchReviewResults 构造 n 条同文件（src/a.go）的批次评审 issue
func mkBatchReviewResults(n int) []*llm.BatchReviewResult {
	issues := make([]llm.AIReviewIssue, 0, n)
	for i := 0; i < n; i++ {
		issues = append(issues, llm.AIReviewIssue{
			RuleCode:  "test-rule",
			Severity:  "high",
			Category:  "security",
			File:      "src/a.go",
			LineStart: i + 1,
			LineEnd:   i + 1,
			Message:   "批次评审问题",
		})
	}
	return []*llm.BatchReviewResult{{Issues: issues}}
}

// Rule 5：输入有批次评审问题，但裁决输出 issues 为空 → 判定吞没并拒绝（触发本地去重降级）
func TestVerifyLLMOutput_BatchIssuesDropped(t *testing.T) {
	promptCtx := &engine.PromptContext{BatchReviewResults: mkBatchReviewResults(5)}
	result := &llm.AIReviewResult{Issues: []llm.AIReviewIssue{}}

	err := verifyLLMOutput(result, promptCtx)
	if err == nil {
		t.Fatal("输入含 5 条批次问题而输出 issues 为空，应被判为吞没，但校验通过了")
	}
	if !strings.Contains(err.Error(), "吞没") {
		t.Fatalf("错误信息应包含“吞没”，got: %v", err)
	}
}

// 仅影响分析、无任何批次问题/Agent 安全发现时，issues 为空是合法的
func TestVerifyLLMOutput_OnlyImpactNotesPasses(t *testing.T) {
	promptCtx := &engine.PromptContext{
		ImpactFindings: []engine.ImpactFinding{{FilePath: "config.json", Type: "config_change"}},
	}
	result := &llm.AIReviewResult{Issues: []llm.AIReviewIssue{}}

	if err := verifyLLMOutput(result, promptCtx); err != nil {
		t.Fatalf("仅有影响分析且 issues 为空应通过，got: %v", err)
	}
}

// Rule 1：输入为空却输出 issues → 判捏造
func TestVerifyLLMOutput_Rule1_FabricatedWhenInputEmpty(t *testing.T) {
	promptCtx := &engine.PromptContext{}
	result := &llm.AIReviewResult{Issues: []llm.AIReviewIssue{{
		Severity: "high", Category: "security", Message: "凭空生成",
	}}}

	err := verifyLLMOutput(result, promptCtx)
	if err == nil || !strings.Contains(err.Error(), "捏造") {
		t.Fatalf("空输入却输出 issue 应判捏造，got: %v", err)
	}
}

// Rule 5b：输入有 Agent 安全发现，但 issues 与 security_findings 均空 → 判吞没
func TestVerifyLLMOutput_AgentSecurityDropped(t *testing.T) {
	promptCtx := &engine.PromptContext{
		SecretScanFindings: []model.SecretScanFinding{{
			FilePath: "src/a.go", LineNumber: 1, Severity: "high", RuleCode: "secret",
		}},
	}
	result := &llm.AIReviewResult{}

	err := verifyLLMOutput(result, promptCtx)
	if err == nil || !strings.Contains(err.Error(), "吞没") {
		t.Fatalf("Agent 安全发现既不在 issues 也不在 security_findings，应判吞没，got: %v", err)
	}
}

// Rule 5b：Agent 安全发现已在 security_findings 承载 → 通过
func TestVerifyLLMOutput_AgentSecurityInSecurityFindingsPasses(t *testing.T) {
	promptCtx := &engine.PromptContext{
		SecretScanFindings: []model.SecretScanFinding{{
			FilePath: "src/a.go", LineNumber: 1, Severity: "high", RuleCode: "secret",
		}},
	}
	result := &llm.AIReviewResult{
		SecurityFindings: []llm.SecurityFinding{{Source: "secret_scan", File: "src/a.go", Severity: "high"}},
	}

	if err := verifyLLMOutput(result, promptCtx); err != nil {
		t.Fatalf("Agent 发现已在 security_findings 承载应通过，got: %v", err)
	}
}

// 正常保留：输入批次问题非空且输出问题非空 → 通过
func TestVerifyLLMOutput_NormalPasses(t *testing.T) {
	promptCtx := &engine.PromptContext{BatchReviewResults: mkBatchReviewResults(2)}
	result := &llm.AIReviewResult{Issues: []llm.AIReviewIssue{{
		Severity: "high", Category: "security", File: "src/a.go", LineStart: 1, LineEnd: 1, Message: "保留",
	}}}

	if err := verifyLLMOutput(result, promptCtx); err != nil {
		t.Fatalf("输入与输出均非空应通过，got: %v", err)
	}
}

// ========== recoverMissingFiles ==========

func TestRecoverMissingFiles(t *testing.T) {
	batch := []*llm.BatchReviewResult{{Issues: []llm.AIReviewIssue{
		{
			File: "a.go", RuleCode: "ra", Message: "ma",
			CodeSnippet: "func handler() {\n\tif err != nil {\n\t\treturn nil\n\t}\n\treturn x\n}",
		},
		{File: "b.go", RuleCode: "r2", Message: "m2", CodeSnippet: ""},
	}}}

	issues := []llm.AIReviewIssue{
		// 片段可匹配（rule/message 故意不同，强制走片段匹配）
		{File: "", RuleCode: "other", Message: "other",
			CodeSnippet: "func handler() {\n\tif err != nil {\n\t\treturn nil\n\t}\n\treturn x\n}"},
		// rule_code + message 匹配
		{File: "", RuleCode: "r2", Message: "m2", CodeSnippet: ""},
		// 非文件类问题（提交规范）→ 保持为空
		{File: "", RuleCode: "commit-msg", Message: "提交信息不符合规范", CodeSnippet: ""},
	}

	n := recoverMissingFiles(issues, batch)
	if n != 2 {
		t.Fatalf("recovered = %d, want 2", n)
	}
	if issues[0].File != "a.go" {
		t.Errorf("issue[0].File = %q, want a.go", issues[0].File)
	}
	if issues[1].File != "b.go" {
		t.Errorf("issue[1].File = %q, want b.go", issues[1].File)
	}
	if issues[2].File != "" {
		t.Errorf("non-file issue should stay empty, got %q", issues[2].File)
	}
}
