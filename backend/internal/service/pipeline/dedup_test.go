package pipeline

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/pkg/llm"
)

func TestNewMergeEngine(t *testing.T) {
	cfg := engine.DeductScoreConfig{Critical: 10, High: 5, Medium: 3, Low: 1, Info: 0}
	me := NewMergeEngine(cfg)
	if me.lineThreshold != 3 {
		t.Errorf("expected lineThreshold=3, got %d", me.lineThreshold)
	}
	if me.similarityThreshold != 0.70 {
		t.Errorf("expected similarityThreshold=0.70, got %f", me.similarityThreshold)
	}
}

func TestMergeEngineExecuteNoOverlap(t *testing.T) {
	cfg := engine.DeductScoreConfig{Critical: 10, High: 5, Medium: 3, Low: 1, Info: 0}
	me := NewMergeEngine(cfg)

	agentFindings := []UnifiedFinding{
		{RuleCode: "SECRET-001", Severity: "high", Category: "security", File: "a.go", LineStart: 10, Message: "found secret"},
	}
	llmIssues := []UnifiedFinding{
		{RuleCode: "", Severity: "medium", Category: "code_quality", File: "b.go", LineStart: 20, Message: "bad naming"},
	}

	issues, logs := me.Execute(agentFindings, llmIssues)
	if len(issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(issues))
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 dedup logs, got %d", len(logs))
	}
	if issues[0].Source != "agent" {
		t.Errorf("expected first issue source=agent, got %s", issues[0].Source)
	}
	if issues[1].Source != "llm" {
		t.Errorf("expected second issue source=llm, got %s", issues[1].Source)
	}
}

func TestMergeEngineExecuteWithOverlap(t *testing.T) {
	cfg := engine.DeductScoreConfig{Critical: 10, High: 5, Medium: 3, Low: 1, Info: 0}
	me := NewMergeEngine(cfg)

	agentFindings := []UnifiedFinding{
		{RuleCode: "SECRET-001", Severity: "high", Category: "security", File: "a.go", LineStart: 10, Message: "found secret key"},
	}
	llmIssues := []UnifiedFinding{
		{RuleCode: "", Severity: "medium", Category: "security", File: "a.go", LineStart: 11, Message: "found secret key in code"},
	}

	issues, logs := me.Execute(agentFindings, llmIssues)
	if len(issues) != 1 {
		t.Errorf("expected 1 merged issue, got %d", len(issues))
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 dedup log, got %d", len(logs))
	}
	if issues[0].Source != "merged" {
		t.Errorf("expected merged source, got %s", issues[0].Source)
	}
	if logs[0].Action != "merged" {
		t.Errorf("expected merged action, got %s", logs[0].Action)
	}
}

func TestCalculateJaccardSimilarity(t *testing.T) {
	f1 := UnifiedFinding{Category: "security", Message: "sql injection vulnerability found"}
	f2 := UnifiedFinding{Category: "security", Message: "sql injection detected in query"}
	if !isSemanticallySimilar(f1, f2, 0.5) {
		t.Error("expected similarity for related messages")
	}

	f3 := UnifiedFinding{Message: "completely unrelated topic here"}
	if isSemanticallySimilar(f1, f3, 0.7) {
		t.Error("expected no similarity for unrelated messages")
	}
}

func TestCalculateDimensionsLocally(t *testing.T) {
	cfg := engine.DeductScoreConfig{Critical: 10, High: 5, Medium: 3, Low: 1, Info: 0}
	issues := []llm.AIReviewIssue{
		{Category: "security", Severity: "high", DeductScore: 5},
		{Category: "security", Severity: "medium", DeductScore: 3},
		{Category: "code_quality", Severity: "low", DeductScore: 1},
	}
	dimWeights := map[string]engine.DimensionWeight{
		"security":     {Weight: 40},
		"code_quality": {Weight: 30},
		"performance":  {Weight: 30},
	}
	dims := calculateDimensionsLocally(issues, dimWeights, cfg)
	if dims["security"].Score != 92 {
		t.Errorf("expected security score=92, got %d", dims["security"].Score)
	}
	if dims["code_quality"].Score != 99 {
		t.Errorf("expected code_quality score=99, got %d", dims["code_quality"].Score)
	}
	if dims["performance"].Score != 100 {
		t.Errorf("expected performance score=100, got %d", dims["performance"].Score)
	}
}

func TestCalculateTotalScore(t *testing.T) {
	dims := map[string]llm.Dimension{
		"security":     {Score: 80, Weight: 40},
		"code_quality": {Score: 90, Weight: 30},
		"performance":  {Score: 100, Weight: 30},
	}
	score := calculateTotalScore(dims)
	expected := int((80*40 + 90*30 + 100*30) / 100)
	if score != expected {
		t.Errorf("expected total score=%d, got %d", expected, score)
	}
}

// ==================== 边界测试 ====================

func TestMergeEngineExecuteWithZeroLine(t *testing.T) {
	// LLM issue 无行号 → 仅做语义相似度判定（不同文件不同行也应保留）
	cfg := engine.DeductScoreConfig{Critical: 10, High: 5, Medium: 3, Low: 1, Info: 0}
	me := NewMergeEngine(cfg)

	agentFindings := []UnifiedFinding{
		{ID: "a1", RuleCode: "RULE-001", Severity: "high", Category: "security", File: "a.go", LineStart: 10, Message: "sql injection"},
	}
	llmIssues := []UnifiedFinding{
		{ID: "l1", RuleCode: "", Severity: "high", Category: "security", File: "a.go", LineStart: 0, Message: "sql injection vulnerability"},
	}

	issues, logs := me.Execute(agentFindings, llmIssues)
	// LineStart=0 与 LineStart=10 差值 <= 3 不成立，应为独立 issue
	if len(issues) != 2 {
		t.Errorf("expected 2 issues (no line overlap), got %d", len(issues))
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 dedup logs, got %d", len(logs))
	}
}

func TestMergeEngineExecuteMultipleAgentSameLocation(t *testing.T) {
	// 同一位置多条 Agent 发现 → 应独立保留（通过 ID 区分）
	cfg := engine.DeductScoreConfig{Critical: 10, High: 5, Medium: 3, Low: 1, Info: 0}
	me := NewMergeEngine(cfg)

	agentFindings := []UnifiedFinding{
		{ID: "a1", RuleCode: "SECRET-001", Severity: "high", Category: "security", File: "config.go", LineStart: 42, Message: "api key leak"},
		{ID: "a2", RuleCode: "SECRET-002", Severity: "medium", Category: "security", File: "config.go", LineStart: 42, Message: "password in url"},
	}
	llmIssues := []UnifiedFinding{
		{ID: "l1", RuleCode: "", Severity: "high", Category: "security", File: "config.go", LineStart: 42, Message: "found credentials in config"},
	}

	issues, logs := me.Execute(agentFindings, llmIssues)

	// l1 先匹配 a1（第一个位置重叠的 Agent），category 相同 → 合并

	// l1 不会再次匹配 a2（findOverlappingAgent 找到第一个就返回）

	// 结果：merged(a1,l1) + a2(original) = 2 issues, 1 log

	if len(issues) != 2 {

		t.Errorf("expected 2 issues (a1 merged with l1, a2 kept), got %d", len(issues))

	}

	if len(logs) != 1 {

		t.Errorf("expected 1 dedup log, got %d", len(logs))

	}

	// 验证 a2 未被错误更新（通过 ID 映射精确更新）

	if issues[1].Source != "agent" || issues[1].RuleCode != "SECRET-002" {

		t.Errorf("expected a2 kept as agent, got source=%s rule=%s", issues[1].Source, issues[1].RuleCode)

	}

}

func TestCalculateTotalScoreWithZeroWeight(t *testing.T) {
	// 权重为 0 的维度不参与总分计算
	dims := map[string]llm.Dimension{
		"security":     {Score: 50, Weight: 50},
		"code_quality": {Score: 100, Weight: 0},
		"performance":  {Score: 100, Weight: 50},
	}
	score := calculateTotalScore(dims)
	expected := int((50*50 + 100*50) / 100) // 75
	if score != expected {
		t.Errorf("expected total score=%d (zero-weight excluded), got %d", expected, score)
	}
}

func TestCalculateDimensionsLocallyOverflow(t *testing.T) {
	// 大量问题导致 deduction 超过 100，score 应封顶为 0
	cfg := engine.DeductScoreConfig{Critical: 10, High: 5, Medium: 3, Low: 1, Info: 0}
	issues := []llm.AIReviewIssue{
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
		{Category: "security", Severity: "critical", DeductScore: 10},
	}
	dimWeights := map[string]engine.DimensionWeight{
		"security": {Weight: 100},
	}
	dims := calculateDimensionsLocally(issues, dimWeights, cfg)
	if dims["security"].Score != 0 {
		t.Errorf("expected security score=0 (overflow capped), got %d", dims["security"].Score)
	}
}
