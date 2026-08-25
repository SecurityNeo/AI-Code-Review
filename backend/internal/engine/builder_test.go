package engine

import (
	"strings"
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
)

func TestBuildDimensionDefinitionsSection(t *testing.T) {
	dimWeights := map[string]DimensionWeight{
		"security":        {Weight: 30, Label: "安全性"},
		"code_quality":    {Weight: 25, Label: "代码质量"},
		"readability":     {Weight: 20, Label: "可读性"},
		"maintainability": {Weight: 15, Label: "可维护性"},
		"test_coverage":   {Weight: 10, Label: "测试覆盖"},
	}

	result := buildDimensionDefinitionsSection(dimWeights)

	// 验证包含标题
	if !strings.Contains(result, "## 【问题分类维度】") {
		t.Errorf("expected section header, got:\n%s", result)
	}

	// 验证包含所有维度 code
	for code := range dimWeights {
		if !strings.Contains(result, code) {
			t.Errorf("expected dimension code '%s' in output, got:\n%s", code, result)
		}
	}

	// 验证包含所有维度 label
	labels := []string{"安全性", "代码质量", "可读性", "可维护性", "测试覆盖"}
	for _, label := range labels {
		if !strings.Contains(result, label) {
			t.Errorf("expected dimension label '%s' in output, got:\n%s", label, result)
		}
	}

	// 验证不包含权重值（如 "30%" 或 "权重"）
	if strings.Contains(result, "权重") {
		t.Errorf("output should not contain '权重', got:\n%s", result)
	}

	// 验证包含 category 约束说明
	if !strings.Contains(result, "`category` 字段") {
		t.Errorf("expected category field constraint, got:\n%s", result)
	}
}

func TestBuildDimensionDefinitionsSectionEmpty(t *testing.T) {
	result := buildDimensionDefinitionsSection(map[string]DimensionWeight{})

	// 空 map 时仍然输出标题和约束说明
	if !strings.Contains(result, "## 【问题分类维度】") {
		t.Errorf("expected section header for empty dimensions, got:\n%s", result)
	}

	// 不应该输出任何具体的维度条目（用 - ** 标记判断）
	if strings.Contains(result, "- **") {
		t.Errorf("empty dimensions should not output dimension entries, got:\n%s", result)
	}
}

func TestBuildDimensionDefinitionsSectionNoWeightLeak(t *testing.T) {
	dimWeights := map[string]DimensionWeight{
		"security": {Weight: 30, Label: "安全性"},
	}

	result := buildDimensionDefinitionsSection(dimWeights)

	// 严格验证不泄漏权重值（30 或 30% 都不应出现）
	if strings.Contains(result, "30") {
		t.Errorf("output should not contain weight value '30', got:\n%s", result)
	}
	if strings.Contains(result, "100") {
		t.Errorf("output should not contain '100' (score related), got:\n%s", result)
	}
	if strings.Contains(result, "评分") {
		t.Errorf("output should not contain '评分', got:\n%s", result)
	}
}

func TestBuildRulesSectionLite(t *testing.T) {
	dimWeights := map[string]DimensionWeight{
		"security":     {Weight: 30, Label: "安全性"},
		"code_quality": {Weight: 25, Label: "代码质量"},
	}

	rules := []model.ReviewRule{
		{Code: "SEC-001", Name: "SQL 注入检查", Severity: "critical", Category: "security", Prompt: "检查 SQL 拼接语句"},
		{Code: "CQ-001", Name: "命名规范检查", Severity: "low", Category: "code_quality", Prompt: "检查变量命名是否符合驼峰命名法"},
	}

	result := buildRulesSectionLite(rules, dimWeights)

	// 验证包含标题（无权重信息）
	if !strings.Contains(result, "## 【评审规则列表】") {
		t.Errorf("expected section header, got:\n%s", result)
	}

	// 验证包含规则
	if !strings.Contains(result, "SQL 注入检查") {
		t.Errorf("expected rule name in output, got:\n%s", result)
	}

	// 验证不含"权重"关键词
	if strings.Contains(result, "权重") {
		t.Errorf("buildRulesSectionLite should not contain '权重', got:\n%s", result)
	}

	// 验证不含"评分"关键词
	if strings.Contains(result, "评分") {
		t.Errorf("buildRulesSectionLite should not contain '评分', got:\n%s", result)
	}

	// 验证包含维度 code（如 `security`）
	if !strings.Contains(result, "`security`") {
		t.Errorf("expected dimension code in output, got:\n%s", result)
	}

	// 验证包含约束说明
	if !strings.Contains(result, "`category` 必须严格使用") {
		t.Errorf("expected category constraint in output, got:\n%s", result)
	}
}

func TestBuildRulesSectionLiteNoRules(t *testing.T) {
	dimWeights := map[string]DimensionWeight{
		"security": {Weight: 30, Label: "安全性"},
	}

	result := buildRulesSectionLite([]model.ReviewRule{}, dimWeights)

	// 空规则列表时仍然输出标题
	if !strings.Contains(result, "## 【评审规则列表】") {
		t.Errorf("expected section header for empty rules, got:\n%s", result)
	}

	// 不应该输出任何具体规则
	if strings.Contains(result, "SEC-") {
		t.Errorf("empty rules should not output rule codes, got:\n%s", result)
	}
}
