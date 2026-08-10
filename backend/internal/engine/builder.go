package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/gitlab"
	"github.com/ai-optimizer/backend/pkg/llm"
)

// DimensionWeight 维度权重配置（JSON 解析用）
type DimensionWeight struct {
	Weight int    `json:"weight"`
	Label  string `json:"label"`
}

// DeductScoreConfig 扣分规则配置
type DeductScoreConfig struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

// DefaultDeductScoreConfig 返回默认扣分配置
func DefaultDeductScoreConfig() DeductScoreConfig {
	return DeductScoreConfig{
		Critical: 10,
		High:     5,
		Medium:   3,
		Low:      1,
		Info:     0,
	}
}

// DeductScoreFor 按 severity 返回对应扣分值
func (cfg DeductScoreConfig) DeductScoreFor(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return cfg.Critical
	case "high":
		return cfg.High
	case "medium":
		return cfg.Medium
	case "low":
		return cfg.Low
	case "info":
		return cfg.Info
	default:
		return 0
	}
}

// PromptContext Prompt 组装所需的上下文
type PromptContext struct {
	Files                 []gitlab.DiffFile // diff 文件列表
	CommitsText           string
	MRTitle               string
	CustomInstruction     string                     // 项目自定义说明
	DimensionWeights      map[string]DimensionWeight // 维度权重
	DeductScoreConfig     DeductScoreConfig          // 扣分规则配置
	Rules                 []model.ReviewRule         // 启用的规则列表（已按项目配置合并后的规则）
	MaxRules              int                        // 最多输出规则数
	ASTContext            string                     // AST 上下文文本
	CrossFileContext      string                     // 跨文件调用链上下文
	GitLabCommentTemplate string                     // GitLab 评论模板
	DependencyVulns       []DependencyVuln           // 依赖漏洞扫描结果
}

// DependencyVuln 依赖漏洞信息（用于注入 Prompt）
type DependencyVuln struct {
	PackageName    string `json:"package_name"`
	CurrentVersion string `json:"current_version"`
	VulnID         string `json:"vuln_id"`
	Aliases        string `json:"aliases"`         // CVE-XXXX-XXXX, GHSA-XXXX (逗号分隔)
	Severity       string `json:"severity"`
	Summary        string `json:"summary"`
	FixedVersion   string `json:"fixed_version"`
}

// pkgVulnGroup 按包名+版本汇总的漏洞组
type pkgVulnGroup struct {
	PackageName    string
	CurrentVersion string
	Severity       string           // 取该包下最高严重级别
	Vulns          []DependencyVuln // 该包下的所有漏洞
}

// severityRank 返回严重级别排序权重（越高越严重）
func severityRank(sev string) int {
	switch strings.ToLower(sev) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// buildDependencyVulnsSection 按包汇总渲染依赖漏洞扫描结果（Prompt 注入用）
// 规则：
//   1. 按 PackageName:CurrentVersion 分组，每组只展示一次包名+版本
//   2. 包的整体严重级别取该包下所有漏洞的最高级别
//   3. 不展示 VulnID（GHSA-xxx），仅展示 Aliases（CVE-ID）
//   4. 组内漏洞按严重级别降序排列
func buildDependencyVulnsSection(vulns []DependencyVuln) string {
	if len(vulns) == 0 {
		return ""
	}

	// 按 PackageName:CurrentVersion 分组，并计算每组最高严重级别
	groupMap := make(map[string]*pkgVulnGroup)
	for _, v := range vulns {
		key := v.PackageName + ":" + v.CurrentVersion
		if g, ok := groupMap[key]; ok {
			g.Vulns = append(g.Vulns, v)
			if severityRank(v.Severity) > severityRank(g.Severity) {
				g.Severity = v.Severity
			}
		} else {
			groupMap[key] = &pkgVulnGroup{
				PackageName:    v.PackageName,
				CurrentVersion: v.CurrentVersion,
				Severity:       v.Severity,
				Vulns:          []DependencyVuln{v},
			}
		}
	}

	// 转为切片并按严重级别降序（包之间）
	var groups []*pkgVulnGroup
	for _, g := range groupMap {
		// 组内漏洞按严重级别降序排列
		sort.Slice(g.Vulns, func(i, j int) bool {
			return severityRank(g.Vulns[i].Severity) > severityRank(g.Vulns[j].Severity)
		})
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return severityRank(groups[i].Severity) > severityRank(groups[j].Severity)
	})

	var sb strings.Builder
	sb.WriteString("### 依赖漏洞扫描结果\n")
	sb.WriteString("本次变更涉及以下依赖存在已知漏洞，请在评审时特别关注其影响范围和修复建议：\n\n")

	for _, g := range groups {
		sevLabel := severityDisplay(g.Severity)
		sb.WriteString(fmt.Sprintf("- **[%s]** %s:%s\n", sevLabel, g.PackageName, g.CurrentVersion))
		sb.WriteString("  - 漏洞列表\n")
		for _, v := range g.Vulns {
			cveID := v.Aliases
			if cveID == "" {
				cveID = v.VulnID // fallback：若没有 CVE-ID 则回退到原 ID
			}
			sb.WriteString(fmt.Sprintf("    - %s\n", cveID))
			if v.Summary != "" {
				sb.WriteString(fmt.Sprintf("      漏洞摘要：%s\n", v.Summary))
			}
			if v.FixedVersion != "" {
				sb.WriteString(fmt.Sprintf("      修复建议：升级至%s\n", v.FixedVersion))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// BuildReviewPrompt 组装 AI 评审 Prompt
func BuildReviewPrompt(ctx *PromptContext) string {
	var sb strings.Builder

	// 1. System 角色及输出约束
	sb.WriteString("你是一名资深代码审查专家。请对以下代码变更进行严格审查。\n\n")
	sb.WriteString("## 【重要】返回格式要求\n")
	sb.WriteString("你的响应必须严格符合以下 JSON Schema，不要包含任何 Markdown 代码块标记（如 ```json）或额外解释文字。如果不涉及的评分维度，可以不包含在 dimensions 内。所有字段必须填写，issues 数组为空时填写 []，recommendations 为空时填写 []：\n")

	// 动态生成 SchemaExample，确保示例和实际维度完全一致
	schemaExample := buildSchemaExample(ctx.DimensionWeights)
	sb.WriteString(schemaExample)
	sb.WriteString("\n\n")

	sb.WriteString("## 【总分计算规则 - 必须严格遵守】\n")
	sb.WriteString("1. 每个 Issue 按严重程度固定扣分（deduct_score）：\n")
	dsc := ctx.DeductScoreConfig
	sb.WriteString(fmt.Sprintf("   - critical（严重）: 扣 %d 分\n", dsc.Critical))
	sb.WriteString(fmt.Sprintf("   - high（高危）: 扣 %d 分\n", dsc.High))
	sb.WriteString(fmt.Sprintf("   - medium（中危）: 扣 %d 分\n", dsc.Medium))
	sb.WriteString(fmt.Sprintf("   - low（低危）: 扣 %d 分\n", dsc.Low))
	sb.WriteString(fmt.Sprintf("   - info（提示）: 扣 %d 分\n\n", dsc.Info))
	sb.WriteString("2. 按维度汇总扣分：\n")
	sb.WriteString("   将 issues 按 category 分组，每组内所有 deduct_score 相加。\n\n")
	sb.WriteString("3. 计算各维度得分：\n")
	sb.WriteString("   维度得分 = max(0, 100 - 该维度扣分总和)\n\n")
	sb.WriteString("4. 计算总分（加权平均）：\n")
	sb.WriteString("   total_score = \u03A3(维度得分 \u00D7 维度权重) / 100\n")
	sb.WriteString("   结果四舍五入到整数（0-100）。\n\n")
	sb.WriteString("5. 权重为 0 的维度不参与总分计算，但仍返回 score 供参考。\n\n")
	sb.WriteString("【重要】total_score 必须与上述公式计算结果一致，不能随意填写。\n\n")

	// 2. 项目自定义说明
	if ctx.CustomInstruction != "" {
		sb.WriteString("## 【项目特殊要求】\n")
		sb.WriteString(ctx.CustomInstruction)
		sb.WriteString("\n\n")
	}

	// 3. 维度 + 权重 + 规则 柔和在一起
	sb.WriteString("## 【评分维度、权重及评审规则】\n\n")
	sb.WriteString("请严格按照以下维度和权重进行评分，所有维度得分必须在 0-100 之间，涉及的维度权重之和应为 100：\n\n")

	selected, _ := SelectTopRules(ctx.Rules, ctx.MaxRules)
	grouped := groupRulesByCategory(selected)

	// 维度排序按权重降序，权重相同按名称
	order := sortDimensions(ctx.DimensionWeights)
	for _, cat := range order {
		group, ok := grouped[cat]
		if !ok || len(group) == 0 {
			continue
		}
		weightVal := 0
		if dim, ok2 := ctx.DimensionWeights[cat]; ok2 {
			weightVal = dim.Weight
		}
		sb.WriteString(fmt.Sprintf("### %s（权重 %d%%）：%s\n\n", categoryDisplay(cat), weightVal, cat))
		for _, rule := range group {
			severityLabel := severityDisplay(rule.Severity)
			sb.WriteString(fmt.Sprintf("#### `%s` | %s | **%s**\n\n", rule.Code, rule.Name, severityLabel))

			promptText := strings.TrimSpace(rule.Prompt)
			if promptText != "" {
				hasStructure := strings.HasPrefix(promptText, "#") ||
					strings.HasPrefix(promptText, "-") ||
					strings.HasPrefix(promptText, "*") ||
					listItemRe.MatchString(promptText)
				if !hasStructure {
					sb.WriteString("**检查要点：**\n")
				}
				sb.WriteString(promptText)
				sb.WriteString("\n\n")
			}
		}
	}
	sb.WriteString("对于未在规则列表中的其他问题，也可以一并指出，此时 `rule_code` 填空字符串。\n\n")

	// 4. 依赖漏洞扫描结果
	if len(ctx.DependencyVulns) > 0 {
		sb.WriteString(buildDependencyVulnsSection(ctx.DependencyVulns))
	}

	// 5. 待评审代码
	sb.WriteString("【待评审的代码变更】\n")
	for i, file := range ctx.Files {
		if file.Diff == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("### 文件 %d：%s\n", i+1, file.NewPath))
		sb.WriteString("```diff\n")
		sb.WriteString(file.Diff)
		sb.WriteString("\n```\n\n")
	}

	// 5. Commit 信息
	if ctx.CommitsText != "" {
		sb.WriteString(fmt.Sprintf("【Commit 历史】\n%s\n\n", ctx.CommitsText))
	}

	// 6. MR 标题
	sb.WriteString(fmt.Sprintf("【MR 标题】%s\n", ctx.MRTitle))

	return sb.String()
}

// buildSchemaExample 根据实际维度权重动态生成 JSON Schema 示例
func buildSchemaExample(dimWeights map[string]DimensionWeight) string {
	// 从 dimWeights 提取 dimensions 对象
	dims := make(map[string]llm.Dimension)
	for code, dim := range dimWeights {
		dims[code] = llm.Dimension{Score: 85, Weight: dim.Weight}
	}

	ex := struct {
		SchemaVersion   string                   `json:"schema_version"`
		TotalScore      int                      `json:"total_score"`
		Dimensions      map[string]llm.Dimension `json:"dimensions"`
		Summary         string                   `json:"summary"`
		Issues          []struct {
			RuleCode    string `json:"rule_code"`
			Severity    string `json:"severity"`
			Category    string `json:"category"`
			File        string `json:"file"`
			LineStart   int    `json:"line_start"`
			LineEnd     int    `json:"line_end"`
			CodeSnippet string `json:"code_snippet"`
			Message     string `json:"message"`
			Suggestion  string `json:"suggestion"`
		} `json:"issues"`
		Recommendations []string `json:"recommendations"`
	}{
		SchemaVersion: "1.0",
		TotalScore:    85,
		Dimensions:    dims,
		Summary:       "本次MR整体质量良好，但存在若干需要关注的问题。",
		Issues: []struct {
			RuleCode    string `json:"rule_code"`
			Severity    string `json:"severity"`
			Category    string `json:"category"`
			File        string `json:"file"`
			LineStart   int    `json:"line_start"`
			LineEnd     int    `json:"line_end"`
			CodeSnippet string `json:"code_snippet"`
			Message     string `json:"message"`
			Suggestion  string `json:"suggestion"`
		}{
			{
				RuleCode:    "security-hardcoded-secret",
				Severity:    "high",
				Category:    "security",
				File:        "pkg/service/auth.go",
				LineStart:   45,
				LineEnd:     52,
				CodeSnippet: "const API_KEY = \"sk-1234567890abcdef\"",
				Message:     "此处使用了硬编码密钥",
				Suggestion:  "建议改从环境变量读取，如 os.Getenv(\"API_KEY\")",
			},
		},
		Recommendations: []string{"建议增加单元测试覆盖", "考虑将密码学操作抽取为独立包"},
	}

	b, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return SchemaExampleFallback
	}
	return string(b)
}

// sortDimensions 按权重降序排序维度 code，权重相同按名称字母序
func sortDimensions(dimWeights map[string]DimensionWeight) []string {
	type pair struct {
		code   string
		weight int
	}
	pairs := make([]pair, 0, len(dimWeights))
	for code, dim := range dimWeights {
		pairs = append(pairs, pair{code: code, weight: dim.Weight})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].weight != pairs[j].weight {
			return pairs[i].weight > pairs[j].weight
		}
		return pairs[i].code < pairs[j].code
	})
	result := make([]string, len(pairs))
	for i, p := range pairs {
		result[i] = p.code
	}
	return result
}

// listItemRe 匹配有序列表项开头（如 1. 2.）
var listItemRe = regexp.MustCompile(`^\d+\.\s`)

// SelectRulesResult 规则截断结果
type SelectRulesResult struct {
	Selected   []model.ReviewRule // 实际传入Prompt的规则
	Truncated  []model.ReviewRule // 被截断未传入的规则
	TotalCount int                // 总计规则数
}

// SelectTopRules 按严重级别排序并截断规则列表
// 严重级别枚举: critical > high > medium > low > info
func SelectTopRules(rules []model.ReviewRule, max int) (selected []model.ReviewRule, truncated []model.ReviewRule) {
	severityOrder := map[string]int{"critical": 5, "high": 4, "medium": 3, "low": 2, "info": 1}

	sorted := make([]model.ReviewRule, len(rules))
	copy(sorted, rules)

	sort.Slice(sorted, func(i, j int) bool {
		return severityOrder[sorted[i].Severity] > severityOrder[sorted[j].Severity]
	})

	if len(sorted) > max {
		return sorted[:max], sorted[max:]
	}
	return sorted, nil
}

// groupRulesByCategory 按维度类别分组规则
func groupRulesByCategory(rules []model.ReviewRule) map[string][]model.ReviewRule {
	grouped := make(map[string][]model.ReviewRule)
	for _, rule := range rules {
		cat := rule.Category
		if cat == "" {
			cat = "common"
		}
		grouped[cat] = append(grouped[cat], rule)
	}
	return grouped
}

// categoryDisplay 维度名称中文映射（查看 ReviewCategory 表获取最终源）
// 内置 5 个维度硬编码中文；自定义维度直接返回原名
func categoryDisplay(category string) string {
	m := map[string]string{
		"security":        "安全性",
		"performance":     "性能",
		"readability":     "可读性",
		"maintainability": "可维护性",
		"test_coverage":   "测试覆盖",
	}
	if v, ok := m[category]; ok {
		return v
	}
	// 自定义维度：直接返回原名
	return category
}

// severityDisplay 严重级别中文显示
func severityDisplay(severity string) string {
	m := map[string]string{
		"critical": "严重",
		"high":     "高危",
		"medium":   "中危",
		"low":      "低危",
		"info":     "提示",
	}
	if v, ok := m[severity]; ok {
		return v
	}
	return severity
}

// SchemaExampleFallback 兜底示例（无法 marshal 时使用）
const SchemaExampleFallback = `{
  "schema_version": "1.0",
  "total_score": 85,
  "dimensions": {
    "security": {"score": 95, "weight": 30}
  },
  "summary": "本次MR整体质量良好。",
  "issues": [
    {
      "rule_code": "security-hardcoded-secret",
      "severity": "high",
      "category": "security",
      "file": "pkg/service/auth.go",
      "line_start": 45,
      "line_end": 52,
      "code_snippet": "const API_KEY = \"sk-1234567890abcdef\"",
      "message": "此处使用了硬编码密钥",
      "suggestion": "建议改从环境变量读取"
    }
  ],
  "recommendations": ["建议增加单元测试覆盖"]
}`

// ParseDeductScoreConfig 从 JSON 字符串解析扣分规则配置
func ParseDeductScoreConfig(jsonStr string) (DeductScoreConfig, error) {
	cfg := DefaultDeductScoreConfig()
	if jsonStr == "" || jsonStr == "{}" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ==================== Pipeline 结构化评审 Prompt 构建器 ====================

// BuildFullStructuredPrompt 构建完整的结构化评审 Prompt（单批场景用）
// 输出：含 System 角色 + JSON Schema + 规则 + diff 的完整 Prompt，以及对应的 ResponseFormat
func BuildFullStructuredPrompt(ctx *PromptContext) (string, *llm.ResponseFormat) {
	var sb strings.Builder

	sb.WriteString("你是一名资深代码审查专家。请对以下代码变更进行严格审查。\n\n")
	sb.WriteString("## 【重要】返回格式要求\n")
	sb.WriteString("你的响应必须严格符合以下 JSON Schema，不要包含任何 Markdown 代码块标记（如 ```json）或额外解释文字。如果不涉及的评分维度，可以不包含在 dimensions 内。所有字段必须填写，issues 数组为空时填写 []，recommendations 为空时填写 []：\n")
	schemaExample := buildSchemaExample(ctx.DimensionWeights)
	sb.WriteString(schemaExample)
	sb.WriteString("\n\n")

	sb.WriteString(buildScoreRulesText(ctx.DeductScoreConfig))
	sb.WriteString("\n")

	// 项目自定义说明
	if ctx.CustomInstruction != "" {
		sb.WriteString("## 【项目特殊要求】\n")
		sb.WriteString(ctx.CustomInstruction)
		sb.WriteString("\n\n")
	}

	// 维度 + 权重 + 规则
	sb.WriteString(buildRulesSection(ctx.Rules, ctx.DimensionWeights))

	// AST 上下文
	if ctx.ASTContext != "" {
		sb.WriteString(ctx.ASTContext)
		sb.WriteString("\n")
	}

	// 跨文件调用链
	if ctx.CrossFileContext != "" {
		sb.WriteString(ctx.CrossFileContext)
		sb.WriteString("\n")
	}

	// 待评审代码
	sb.WriteString("【待评审的代码变更】\n")
	for i, file := range ctx.Files {
		if file.Diff == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("### 文件 %d：%s\n", i+1, file.NewPath))
		sb.WriteString("```diff\n")
		sb.WriteString(file.Diff)
		sb.WriteString("\n```\n\n")
	}

	// Commit 信息
	if ctx.CommitsText != "" {
		sb.WriteString(fmt.Sprintf("【Commit 历史】\n%s\n\n", ctx.CommitsText))
	}

	// MR 标题
	sb.WriteString(fmt.Sprintf("【MR 标题】%s\n", ctx.MRTitle))

	// 构建 JSON Schema ResponseFormat
	dimNames := make([]string, 0, len(ctx.DimensionWeights))
	for code := range ctx.DimensionWeights {
		dimNames = append(dimNames, code)
	}
	schema := llm.GetReviewJSONSchema(dimNames)
	responseFormat := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "code_review_result",
			Strict: true,
			Schema: schema,
		},
	}

	return sb.String(), responseFormat
}

// BuildBatchCollectionPrompt 构建分批评审收集 Prompt（分批场景用）
// 输出：含规则 + diff，但 JSON Schema 只要求 issues[] 和 recommendations[]，不计算总分
// batchFiles 必须使用 SmartSplitIntoBatches 截断后的文件 map，确保单文件不超过可用 token 配额
func BuildBatchCollectionPrompt(ctx *PromptContext, batchIndex, totalBatches int, batchFiles []map[string]interface{}) string {
	var sb strings.Builder

	sb.WriteString("你是一名资深代码审查专家。请对以下代码变更进行审查。\n\n")
	sb.WriteString("## 【重要】返回格式要求\n")
	sb.WriteString("你的响应必须严格符合以下 JSON Schema，不要包含任何 Markdown 代码块标记或额外解释文字：\n")
	batchSchema := llm.GetBatchCollectionJSONSchema()
	schemaBytes, _ := json.MarshalIndent(batchSchema, "", "  ")
	sb.WriteString(string(schemaBytes))
	sb.WriteString("\n\n")

	sb.WriteString("【重要】本次为分批评审（第 ")
	sb.WriteString(fmt.Sprintf("%d/%d", batchIndex, totalBatches))
	sb.WriteString(" 批），你只需：\n")
	sb.WriteString("1. 检查以下代码变更中是否存在违反评审规则的问题\n")
	sb.WriteString("2. 按上述格式输出 issues[] 和 recommendations[]\n")
	sb.WriteString("3. 不需要计算各维度得分和总分\n")
	sb.WriteString("4. 后续会有汇总阶段统一裁决\n\n")

	// 扣分规则（供参考）
	sb.WriteString(buildScoreRulesText(ctx.DeductScoreConfig))
	sb.WriteString("\n")

	// 项目自定义说明
	if ctx.CustomInstruction != "" {
		sb.WriteString("## 【项目特殊要求】\n")
		sb.WriteString(ctx.CustomInstruction)
		sb.WriteString("\n\n")
	}

	// 维度 + 权重 + 规则
	sb.WriteString(buildRulesSection(ctx.Rules, ctx.DimensionWeights))

	// 依赖漏洞扫描结果
	if len(ctx.DependencyVulns) > 0 {
		sb.WriteString(buildDependencyVulnsSection(ctx.DependencyVulns))
	}

	// AST 上下文
	if ctx.ASTContext != "" {
		sb.WriteString(ctx.ASTContext)
		sb.WriteString("\n")
	}

	// 跨文件调用链
	if ctx.CrossFileContext != "" {
		sb.WriteString(ctx.CrossFileContext)
		sb.WriteString("\n")
	}

	// 待评审代码（使用截断后的 diff）
	sb.WriteString(fmt.Sprintf("## 待评审内容（第 %d/%d 批）\n\n", batchIndex, totalBatches))
	for i, f := range batchFiles {
		path, _ := f["path"].(string)
		diff, _ := f["diff"].(string)
		if diff == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%d、文件：%s\n", i+1, path))
		sb.WriteString("```diff\n")
		sb.WriteString(diff)
		sb.WriteString("\n```\n\n")
	}

	// Commit 信息
	if ctx.CommitsText != "" {
		sb.WriteString(fmt.Sprintf("\ncommits：\n%s\n", ctx.CommitsText))
	}

	// MR 标题
	sb.WriteString(fmt.Sprintf("\nMR名称：%s", ctx.MRTitle))

	return sb.String()
}

// BuildScoreArbitrationPrompt 构建汇总裁决 Prompt（汇总场景用）
// 输出：基于全量 issues 计算总分和维度得分的 Prompt
func BuildScoreArbitrationPrompt(ctx *PromptContext, batchResults []*llm.BatchReviewResult) (string, *llm.ResponseFormat) {
	var sb strings.Builder

	sb.WriteString("你是一名资深代码审查专家。请基于以下各批次发现的问题，生成最终的综合评审报告。\n\n")
	sb.WriteString("## 【重要】返回格式要求\n")
	sb.WriteString("你的响应必须严格符合以下 JSON Schema，不要包含任何 Markdown 代码块标记或额外解释文字：\n")
	schemaExample := buildSchemaExample(ctx.DimensionWeights)
	sb.WriteString(schemaExample)
	sb.WriteString("\n\n")

	sb.WriteString(buildScoreRulesText(ctx.DeductScoreConfig))
	sb.WriteString("\n")

	// 项目自定义说明
	if ctx.CustomInstruction != "" {
		sb.WriteString("## 【项目特殊要求】\n")
		sb.WriteString(ctx.CustomInstruction)
		sb.WriteString("\n\n")
	}

	// 只列出维度名称和权重
	sb.WriteString("## 【评分维度及权重】\n\n")
	order := sortDimensions(ctx.DimensionWeights)
	for _, cat := range order {
		weightVal := 0
		if dim, ok := ctx.DimensionWeights[cat]; ok {
			weightVal = dim.Weight
		}
		label := categoryDisplay(cat)
		sb.WriteString(fmt.Sprintf("- %s（权重 %d%%）：%s\n", label, weightVal, cat))
	}
	sb.WriteString("\n")

	// 各批次发现的问题汇总
	sb.WriteString("## 各批次发现的问题汇总\n\n")
	for i, br := range batchResults {
		sb.WriteString(fmt.Sprintf("### 批次 %d", i+1))
		if br.BatchNotes != "" {
			sb.WriteString(fmt.Sprintf("（%s）", br.BatchNotes))
		}
		sb.WriteString(fmt.Sprintf("（共 %d 个问题）\n", len(br.Issues)))
		for j, issue := range br.Issues {
			sb.WriteString(fmt.Sprintf("- Issue %d [%s] %s | `%s` | 文件: %s (行 %d-%d) | %s\n",
				j+1, issue.Severity, issue.RuleCode, issue.Category, issue.File, issue.LineStart, issue.LineEnd, issue.Message))
			if issue.Suggestion != "" {
				sb.WriteString(fmt.Sprintf("  建议: %s\n", issue.Suggestion))
			}
		}
		if len(br.Recommendations) > 0 {
			sb.WriteString("改进建议:\n")
			for _, rec := range br.Recommendations {
				sb.WriteString(fmt.Sprintf("- %s\n", rec))
			}
		}
		sb.WriteString("\n")
	}

	// Commit 信息
	if ctx.CommitsText != "" {
		sb.WriteString(fmt.Sprintf("\n【Commit 历史】\n%s\n", ctx.CommitsText))
	}

	// MR 标题
	sb.WriteString(fmt.Sprintf("\n【MR 标题】%s\n", ctx.MRTitle))

	sb.WriteString("\n---\n\n")
	sb.WriteString("请基于以上信息，完成以下任务：\n")
	sb.WriteString("1. **去重合并**：同一位置的同类问题只保留最严重的一条\n")
	sb.WriteString("2. **计算各维度扣分**：按 category 分组，汇总 deduct_score\n")
	sb.WriteString("3. **计算维度得分**：max(0, 100 - 该维度扣分总和)\n")
	sb.WriteString("4. **计算总分**：total_score = Σ(维度得分 × 维度权重) / 100，四舍五入到整数\n")
	sb.WriteString("5. **权重为 0 的维度**：仍需返回 score=100（无问题时），参与展示\n")
	sb.WriteString("6. **生成 summary**：100字以内的总体评价\n")
	sb.WriteString("7. **输出完整 JSON**：包含 total_score、dimensions（所有维度）、issues（合并后）、recommendations\n")
	sb.WriteString("【重要】total_score 必须与公式计算结果一致，不能随意填写。\n")

	// 构建 JSON Schema ResponseFormat
	dimNames := make([]string, 0, len(ctx.DimensionWeights))
	for code := range ctx.DimensionWeights {
		dimNames = append(dimNames, code)
	}
	schema := llm.GetReviewJSONSchema(dimNames)
	responseFormat := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "code_review_result",
			Strict: true,
			Schema: schema,
		},
	}

	return sb.String(), responseFormat
}

// ==================== 辅助函数 ====================

// buildScoreRulesText 构建总分计算规则文本
func buildScoreRulesText(dsc DeductScoreConfig) string {
	var sb strings.Builder
	sb.WriteString("## 【总分计算规则 - 必须严格遵守】\n")
	sb.WriteString("1. 每个 Issue 按严重程度固定扣分（deduct_score）：\n")
	sb.WriteString(fmt.Sprintf("   - critical（严重）: 扣 %d 分\n", dsc.Critical))
	sb.WriteString(fmt.Sprintf("   - high（高危）: 扣 %d 分\n", dsc.High))
	sb.WriteString(fmt.Sprintf("   - medium（中危）: 扣 %d 分\n", dsc.Medium))
	sb.WriteString(fmt.Sprintf("   - low（低危）: 扣 %d 分\n", dsc.Low))
	sb.WriteString(fmt.Sprintf("   - info（提示）: 扣 %d 分\n\n", dsc.Info))
	sb.WriteString("2. 按维度汇总扣分：将 issues 按 category 分组，每组内所有 deduct_score 相加。\n\n")
	sb.WriteString("3. 计算各维度得分：维度得分 = max(0, 100 - 该维度扣分总和)\n\n")
	sb.WriteString("4. 计算总分（加权平均）：total_score = Σ(维度得分 × 维度权重) / 100\n")
	sb.WriteString("   结果四舍五入到整数（0-100）。\n\n")
	sb.WriteString("5. 权重为 0 的维度不参与总分计算，但仍返回 score 供参考。\n\n")
	sb.WriteString("【重要】total_score 必须与上述公式计算结果一致，不能随意填写。\n")
	return sb.String()
}

// buildRulesSection 构建规则章节（维度 + 权重 + 规则列表）
func buildRulesSection(rules []model.ReviewRule, dimWeights map[string]DimensionWeight) string {
	var sb strings.Builder
	sb.WriteString("## 【评分维度、权重及评审规则】\n\n")
	sb.WriteString("请严格按照以下维度和权重进行评分，所有维度得分必须在 0-100 之间，涉及的维度权重之和应为 100：\n\n")

	selected, _ := SelectTopRules(rules, len(rules))
	grouped := groupRulesByCategory(selected)

	order := sortDimensions(dimWeights)
	for _, cat := range order {
		group, ok := grouped[cat]
		if !ok || len(group) == 0 {
			continue
		}
		weightVal := 0
		if dim, ok2 := dimWeights[cat]; ok2 {
			weightVal = dim.Weight
		}
		sb.WriteString(fmt.Sprintf("### %s（权重 %d%%）：%s\n\n", categoryDisplay(cat), weightVal, cat))
		for _, rule := range group {
			severityLabel := severityDisplay(rule.Severity)
			sb.WriteString(fmt.Sprintf("#### `%s` | %s | **%s**\n\n", rule.Code, rule.Name, severityLabel))

			promptText := strings.TrimSpace(rule.Prompt)
			if promptText != "" {
				hasStructure := strings.HasPrefix(promptText, "#") ||
					strings.HasPrefix(promptText, "-") ||
					strings.HasPrefix(promptText, "*") ||
					listItemRe.MatchString(promptText)
				if !hasStructure {
					sb.WriteString("**检查要点：**\n")
				}
				sb.WriteString(promptText)
				sb.WriteString("\n\n")
			}
		}
	}
	sb.WriteString("对于未在规则列表中的其他问题，也可以一并指出，此时 `rule_code` 填空字符串。\n\n")
	return sb.String()
}
