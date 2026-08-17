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

// AgentExecutionStatus 智能体执行状态（用于报告注入）
type AgentExecutionStatus struct {
	Enabled     []string // 名称列表，如 "触发过滤器","代码克隆"
	Skipped     []string // 名称列表
	ImpactNotes []string // 影响说明，如 "本次评审缺少 AST 上下文..."
}

// ImpactFinding 影响分析单条结果
type ImpactFinding struct {
	Type          string   `json:"type"` // breaking_change / schema_change / config_change / migration
	FilePath      string   `json:"file_path"`
	SymbolName    string   `json:"symbol_name"`    // 变更的符号名（函数/结构体/配置项）
	ChangeDesc    string   `json:"change_desc"`    // 变更描述
	AffectedFiles []string `json:"affected_files"` // 受影响的下游文件（基于调用链）
	Severity      string   `json:"severity"`       // critical / high / medium
	Suggestion    string   `json:"suggestion"`     // 建议操作
}

// LicenseFinding 许可证检查单条结果
type LicenseFinding struct {
	PackageName    string `json:"package_name"`
	CurrentVersion string `json:"current_version"`
	License        string `json:"license"`         // 检测到的许可证
	Compatibility  string `json:"compatibility"`   // compatible / incompatible / attention
	ProjectLicense string `json:"project_license"` // 项目当前许可证
	Reason         string `json:"reason"`          // 结论原因
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
	ShowAgentStatus       bool                       // 是否在报告中展示智能体启用状态
	AgentStatus           *AgentExecutionStatus      // 智能体执行状态

	// 扩展智能体产出（Phase C）
	SecretScanFindings    []model.SecretScanFinding    `json:"secret_scan_findings,omitempty"`
	SecurityAuditFindings []model.SecurityAuditFinding `json:"security_audit_findings,omitempty"`
	ImpactFindings        []ImpactFinding              `json:"impact_findings,omitempty"`
	LicenseFindings       []LicenseFinding             `json:"license_findings,omitempty"`
	TestSuggestions       []TestSuggestionItem         `json:"test_suggestions,omitempty"`

	// 代码理解器产出（AST + 知识图谱 + 数据流分析）
	CodeUnderstandingReport string `json:"code_understanding_report,omitempty"` // Prompt注入文本

	// 【新增】各智能体预渲染的 Markdown（直接拼接进 Prompt，避免重复渲染）
	AgentMarkdowns map[string]string `json:"-"` // key: secret_scan/security_audit/test_suggestion/impact_analysis/dependency_scan/code_understanding

	// 【新增】review_arbitration 阶段使用的聚合数据
	BatchReviewResults []*llm.BatchReviewResult `json:"batch_review_results,omitempty"` // batch_review 输出结果
	DimensionCodes     []string                 `json:"dimension_codes,omitempty"`      // 维度代码列表（用于 GetReviewJSONSchema）
}

// TestSuggestionItem 测试建议条目（PromptContext 使用）
type TestSuggestionItem struct {
	FunctionName string             `json:"function_name"`
	FilePath     string             `json:"file_path"`
	LineNumber   int                `json:"line_number"`
	Scenarios    []llm.TestScenario `json:"scenarios"`
}

// AgentStatusSection 生成报告尾部的智能体配置段落
func (p *PromptContext) AgentStatusSection() string {
	if !p.ShowAgentStatus || p.AgentStatus == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\n\n")
	sb.WriteString("### 本次评审配置\n\n")

	if len(p.AgentStatus.Enabled) > 0 {
		sb.WriteString("**已启用的智能体**：\n")
		for _, s := range p.AgentStatus.Enabled {
			sb.WriteString(fmt.Sprintf("- [启用] %s\n", s))
		}
		sb.WriteString("\n")
	}

	if len(p.AgentStatus.Skipped) > 0 {
		sb.WriteString("**跳过的智能体**：\n")
		for _, s := range p.AgentStatus.Skipped {
			sb.WriteString(fmt.Sprintf("- [跳过] %s — 管理员配置关闭\n", s))
		}
		sb.WriteString("\n")
	}

	if len(p.AgentStatus.ImpactNotes) > 0 {
		sb.WriteString("**影响说明**：\n")
		for _, note := range p.AgentStatus.ImpactNotes {
			sb.WriteString(fmt.Sprintf("- %s\n", note))
		}
	}

	return sb.String()
}

// ==================== 【新增】PromptContext 辅助方法（review_arbitration 使用）====================

// DimensionConfig 评审维度配置（用于 review_arbitration Prompt 注入）
type DimensionConfig struct {
	Code   string `json:"code"`
	Weight int    `json:"weight"`
}

// GetDimensionConfig 将 DimensionWeights 转为 review_arbitration 使用的维度配置
func (p *PromptContext) GetDimensionConfig() map[string]DimensionConfig {
	result := make(map[string]DimensionConfig, len(p.DimensionWeights))
	for code, dw := range p.DimensionWeights {
		result[code] = DimensionConfig{
			Code:   code,
			Weight: dw.Weight,
		}
	}
	return result
}

// GetDeductScoreConfig 返回扣分规则配置
func (p *PromptContext) GetDeductScoreConfig() DeductScoreConfig {
	return p.DeductScoreConfig
}

// GetDimensionCodes 返回维度代码列表（用于 GetReviewJSONSchema 参数）
func (p *PromptContext) GetDimensionCodes() []string {
	if len(p.DimensionCodes) > 0 {
		return p.DimensionCodes
	}
	// 从 DimensionWeights 推导
	codes := make([]string, 0, len(p.DimensionWeights))
	for k := range p.DimensionWeights {
		codes = append(codes, k)
	}
	return codes
}

// AgentFindingsJSON 将 6 类 Agent Finding 统一序列化为 JSON（用于结构化 Prompt 注入）
func (p *PromptContext) AgentFindingsJSON() ([]byte, error) {
	data := map[string]interface{}{
		"secret_scan":       p.SecretScanFindings,
		"security_audit":    p.SecurityAuditFindings,
		"test_suggestion":   p.TestSuggestions,
		"impact_analysis":   p.ImpactFindings,
		"dependency_scan":   p.DependencyVulns,
		"code_understanding": p.CodeUnderstandingReport,
	}
	return json.Marshal(data)
}

// SecretScanJsonBytes 序列化 SecretScanFindings（懒加载，按需调用）
func (p *PromptContext) SecretScanJsonBytes() ([]byte, error) {
	if len(p.SecretScanFindings) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(p.SecretScanFindings)
}

// DependencyVuln 依赖漏洞信息（用于注入 Prompt）
type DependencyVuln struct {
	PackageName    string `json:"package_name"`
	CurrentVersion string `json:"current_version"`
	VulnID         string `json:"vuln_id"`
	Aliases        string `json:"aliases"` // CVE-XXXX-XXXX, GHSA-XXXX (逗号分隔)
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
//  1. 按 PackageName:CurrentVersion 分组，每组只展示一次包名+版本
//  2. 包的整体严重级别取该包下所有漏洞的最高级别
//  3. 不展示 VulnID（GHSA-xxx），仅展示 Aliases（CVE-ID）
//  4. 组内漏洞按严重级别降序排列
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

// BuildDependencyVulnsSection 导出版本：按包汇总渲染依赖漏洞扫描结果（Prompt 注入用）
func BuildDependencyVulnsSection(vulns []DependencyVuln) string {
	return buildDependencyVulnsSection(vulns)
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

	// 4. 各智能体预渲染 Markdown（直接拼接，避免重复渲染）
	// 优先级：AgentMarkdowns > 重新渲染（兼容旧路径）
	hasAgentContent := false
	for _, key := range []string{"dependency_scan", "secret_scan", "security_audit", "impact_analysis", "test_suggestion", "code_understanding"} {
		if md, ok := ctx.AgentMarkdowns[key]; ok && md != "" {
			sb.WriteString(md)
			sb.WriteString("\n")
			hasAgentContent = true
		}
	}

	// 兼容旧路径：如果 AgentMarkdowns 未设置，回退到原始 render 逻辑
	if !hasAgentContent {
		if len(ctx.DependencyVulns) > 0 {
			sb.WriteString(buildDependencyVulnsSection(ctx.DependencyVulns))
		}
		if len(ctx.SecretScanFindings) > 0 {
			sb.WriteString(buildSecretScanSection(ctx.SecretScanFindings))
		}
		if len(ctx.SecurityAuditFindings) > 0 {
			sb.WriteString(buildSecurityAuditSection(ctx.SecurityAuditFindings))
		}
		if len(ctx.LicenseFindings) > 0 {
			sb.WriteString(buildLicenseSection(ctx.LicenseFindings))
		}
		if len(ctx.ImpactFindings) > 0 {
			sb.WriteString(buildImpactSection(ctx.ImpactFindings))
		}
		if len(ctx.TestSuggestions) > 0 {
			sb.WriteString(buildTestSuggestionSection(ctx.TestSuggestions))
		}
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
		SchemaVersion string                   `json:"schema_version"`
		TotalScore    int                      `json:"total_score"`
		Dimensions    map[string]llm.Dimension `json:"dimensions"`
		Summary       string                   `json:"summary"`
		Issues        []struct {
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

	// 代码理解器报告（AST + 知识图谱 + 数据流分析）
	if ctx.CodeUnderstandingReport != "" {
		sb.WriteString("【代码理解报告】\n")
		sb.WriteString(ctx.CodeUnderstandingReport)
		sb.WriteString("\n\n")
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
// BuildBatchCollectionPrompt 构建分批评审收集 Prompt（分批场景用）
// 输出：含规则 + diff，但 JSON Schema 只要求 issues[] 和 recommendations[]，不计算总分
// batchFiles 必须使用 SmartSplitIntoBatches 截断后的文件 map，确保单文件不超过可用 token 配额
// isLastBatch 控制是否在最后一批注入通用信息（Agent findings、commit、MR标题、依赖漏洞），避免每批重复
func BuildBatchCollectionPrompt(ctx *PromptContext, batchIndex, totalBatches int, batchFiles []map[string]interface{}, isLastBatch bool) string {
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

	if isLastBatch {
		// 各智能体预渲染 Markdown（仅最后一批注入，避免 Token 浪费）
		hasAgentContent := false
		for _, key := range []string{"dependency_scan", "secret_scan", "security_audit", "impact_analysis", "test_suggestion", "code_understanding"} {
			if md, ok := ctx.AgentMarkdowns[key]; ok && md != "" {
				sb.WriteString(md)
				sb.WriteString("\n")
				hasAgentContent = true
			}
		}

		// 兼容旧路径
		if !hasAgentContent {
			if len(ctx.DependencyVulns) > 0 {
				sb.WriteString(buildDependencyVulnsSection(ctx.DependencyVulns))
			}
			if len(ctx.SecretScanFindings) > 0 {
				sb.WriteString(buildSecretScanSection(ctx.SecretScanFindings))
			}
			if len(ctx.SecurityAuditFindings) > 0 {
				sb.WriteString(buildSecurityAuditSection(ctx.SecurityAuditFindings))
			}
			if len(ctx.TestSuggestions) > 0 {
				sb.WriteString(buildTestSuggestionSection(ctx.TestSuggestions))
			}
		}

		// 当存在 Agent 发现时，注入禁止重复指令
		agentFindingCount := len(ctx.SecretScanFindings) + len(ctx.SecurityAuditFindings) + len(ctx.ImpactFindings)
		if agentFindingCount > 0 {
			sb.WriteString(buildNoRepeatInstruction())
		}

		// Commit 信息（仅最后一批）
		if ctx.CommitsText != "" {
			sb.WriteString(fmt.Sprintf("\ncommits：\n%s\n", ctx.CommitsText))
		}

		// MR 标题（仅最后一批）
		if ctx.MRTitle != "" {
			sb.WriteString(fmt.Sprintf("\nMR名称：%s", ctx.MRTitle))
		}
	}

	// 待评审代码（使用截断后的 diff）
	sb.WriteString(fmt.Sprintf("\n## 待评审内容（第 %d/%d 批）\n\n", batchIndex, totalBatches))

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

// BuildArbitrationPrompt 【新增】构建 review_arbitration 专用的结构化 Prompt（JSON 注入模式）
// 与 BuildScoreArbitrationPrompt 的区别：
// 1. Agent findings 以结构化 JSON 注入（非 markdown 文本退化）
// 2. 使用 GetReviewArbitrationJSONSchema（扩展了 security_findings / testing_notes / impact_notes / dedup_log / overall_suggestion）
// 3. 返回 (string, *llm.ResponseFormat, error) 三元组
func BuildArbitrationPrompt(ctx *PromptContext) (string, *llm.ResponseFormat, error) {
	var sb strings.Builder

	// 1. System Prompt（角色定义 + 核心职责 + 输入说明 + 去重/评分规则）
	sb.WriteString("你是一名资深代码评审架构师，负责整合多智能体检出结果与 AI 增量评审发现，输出最终的代码评审报告。\n\n")
	sb.WriteString("## 核心职责\n")
	sb.WriteString("1. 去重仲裁：合并 Agent 规则引擎发现与 LLM 增量评审发现中的重复项\n")
	sb.WriteString("2. 统一格式化：将所有发现转换为标准化的 AIReviewIssue 格式\n")
	sb.WriteString("3. 综合评分：基于全量问题（Agent + LLM）计算各维度得分\n")
	sb.WriteString("4. 独立分类：将测试建议和影响分析放入独立数组，不混入 Issues[]\n")
	sb.WriteString("5. 来源标记：在每条 issue 中标注来源（agent / llm / merged）\n\n")

	sb.WriteString("## 输入数据说明\n")
	sb.WriteString("- Agent 发现：位置精确、severity 可信、rule_code 可追踪\n")
	sb.WriteString("- AI 评审发现：情境理解深、描述丰富、可能重复\n\n")

	sb.WriteString("## 去重规则\n")
	sb.WriteString("1. 位置重叠：同一文件 + 行号差值 ≤ 3 行 → 视为同一位置\n")
	sb.WriteString("2. 语义相似：消息文本相似度 > 70% 或 category 相同 + 位置重叠 → 视为同一问题\n")
	sb.WriteString("3. 去重优先级：保留 Agent 的 severity + rule_code，优先采用 LLM 的 description + suggestion\n")
	sb.WriteString("4. 独立保留：Agent 发现但 LLM 未发现 → 保留（source=agent）；LLM 发现但 Agent 未发现 → 保留（source=llm）\n\n")

	// 2. 扣分规则（来自项目 AI 评审模板）
	sb.WriteString(buildScoreRulesText(ctx.DeductScoreConfig))
	sb.WriteString("\n")

	// 3. 评分规则
	sb.WriteString("## 评分规则\n")
	sb.WriteString("1. 安全维度：Issues[] 中 category = security 的问题 + Agent SecurityFindings 共同计入\n")
	sb.WriteString("2. 代码质量维度：Issues[] 中 category = code_quality 的问题计入\n")
	sb.WriteString("3. 性能维度：Issues[] 中 category = performance 的问题计入\n")
	sb.WriteString("4. 每个 Issue 的 deduct_score = DeductScoreConfig[severity]（来自项目 AI 评审模板）\n")
	sb.WriteString("5. 维度得分 = max(0, 100 - Σ(该维度所有问题扣分))\n")
	sb.WriteString("6. 总分 = Σ(维度得分 × 维度权重) / 100\n\n")

	// 4. 独立输出规则
	sb.WriteString("## 独立输出规则（强制执行）\n")
	sb.WriteString("- TestSuggestion → testing_notes[]（不得放入 Issues[]）\n")
	sb.WriteString("- ImpactAnalysis → impact_notes[]（不得放入 Issues[]）\n")
	sb.WriteString("- SecurityFinding → security_findings[]（仅展示用，已在 Issues[] 中体现扣分）\n\n")

	// 5. 各智能体预渲染 Markdown（直接拼接）
	hasMarkdown := false
	for _, key := range []string{"dependency_scan", "secret_scan", "security_audit", "impact_analysis", "test_suggestion", "code_understanding"} {
		if md, ok := ctx.AgentMarkdowns[key]; ok && md != "" {
			sb.WriteString(md)
			sb.WriteString("\n")
			hasMarkdown = true
		}
	}

	// 6. 构建结构化 User Prompt（JSON 格式数据，仅含 batch_results，避免与 markdown 重复）
	batchJSON, err := json.Marshal(ctx.BatchReviewResults)
	if err != nil {
		return "", nil, fmt.Errorf("Batch results 序列化失败: %w", err)
	}

	dimConfig := ctx.GetDimensionConfig()
	dimJSON, _ := json.Marshal(dimConfig)
	dscJSON, _ := json.Marshal(ctx.GetDeductScoreConfig())

	// 如 markdown 未注入，fallback 到精简 agent JSON（向后兼容）
	var agentJSON []byte
	if !hasMarkdown {
		agentJSON, err = ctx.AgentFindingsJSON()
		if err != nil {
			return "", nil, fmt.Errorf("Agent findings 序列化失败: %w", err)
		}
	}

	type arbitrationInputData struct {
		AgentFindings     json.RawMessage `json:"agent_findings,omitempty"`
		BatchResults      json.RawMessage `json:"batch_results"`
		DimensionConfig   json.RawMessage `json:"dimension_config"`
		DeductScoreConfig json.RawMessage `json:"deduct_score_config"`
	}
	userData := arbitrationInputData{
		AgentFindings:     agentJSON,
		BatchResults:      batchJSON,
		DimensionConfig:   dimJSON,
		DeductScoreConfig: dscJSON,
	}
	userJSON, err := json.MarshalIndent(userData, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("输入数据序列化失败: %w", err)
	}

	sb.WriteString("## 输入数据\n\n```json\n")
	sb.Write(userJSON)
	sb.WriteString("\n```\n")

	// 6. ResponseFormat（使用 review_arbitration 扩展 Schema）
	dimensions := ctx.GetDimensionCodes()
	schema := llm.GetReviewArbitrationJSONSchema(dimensions)
	responseFormat := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "review_arbitration_result",
			Strict: true,
			Schema: schema,
		},
	}

	return sb.String(), responseFormat, nil
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

// buildSecretScanSection 密钥泄露扫描 Prompt 段落
func buildSecretScanSection(findings []model.SecretScanFinding) string {
	var sb strings.Builder
	sb.WriteString("## 密钥泄露扫描结果\n\n")
	sb.WriteString(fmt.Sprintf("发现 %d 处潜在密钥泄露：\n\n", len(findings)))
	sb.WriteString("| 文件 | 行号 | 类型 | 严重级别 |\n")
	sb.WriteString("|:---|:---:|:---|:---:|\n")
	for _, f := range findings {
		sb.WriteString(fmt.Sprintf("| `%s` | %d | %s | %s |\n", f.FilePath, f.LineNumber, f.RuleCode, f.Severity))
	}
	sb.WriteString("\n请重点检查上述位置，确认是否为真实密钥泄露。如果是误报（如测试数据、示例代码），请在回复中说明。\n\n")
	return sb.String()
}

// buildSecurityAuditSection 敏感操作审计 Prompt 段落
func buildSecurityAuditSection(findings []model.SecurityAuditFinding) string {
	var sb strings.Builder
	sb.WriteString("## 敏感操作审计结果\n\n")
	sb.WriteString(fmt.Sprintf("发现 %d 处安全敏感操作：\n\n", len(findings)))
	sb.WriteString("| 文件 | 行号 | 规则 | 级别 | 建议 |\n")
	sb.WriteString("|:---|:---:|:---|:---:|:---|\n")
	for _, f := range findings {
		suggestion := f.Suggestion
		if suggestion == "" {
			suggestion = "—"
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %d | %s | %s | %s |\n", f.FilePath, f.LineNumber, f.RuleCode, f.Severity, suggestion))
	}
	sb.WriteString("\n请在评审中特别关注上述安全问题，确认是否存在实际风险。\n\n")
	return sb.String()
}

// buildLicenseSection License 合规检查 Prompt 段落
func buildLicenseSection(findings []LicenseFinding) string {
	var sb strings.Builder
	sb.WriteString("## License 合规检查\n\n")
	sb.WriteString(fmt.Sprintf("发现 %d 处许可证兼容性问题：\n\n", len(findings)))
	sb.WriteString("| 依赖 | 版本 | 许可证 | 兼容性 | 说明 |\n")
	sb.WriteString("|:---|:---|:---|:---:|:---|\n")
	for _, f := range findings {
		compat := f.Compatibility
		if compat == "incompatible" {
			compat = "❌"
		} else if compat == "attention" {
			compat = "⚠️"
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n", f.PackageName, f.CurrentVersion, f.License, compat, f.Reason))
	}
	sb.WriteString("\n请确认是否需要替换不兼容的依赖，或评估许可证污染风险。\n\n")
	return sb.String()
}

// buildImpactSection 变更影响分析 Prompt 段落
func buildImpactSection(findings []ImpactFinding) string {
	var sb strings.Builder
	var breaking []ImpactFinding
	var others []ImpactFinding
	for _, f := range findings {
		if f.Type == "breaking_change" {
			breaking = append(breaking, f)
		} else {
			others = append(others, f)
		}
	}

	sb.WriteString("## 变更影响分析\n\n")
	if len(breaking) > 0 {
		sb.WriteString(fmt.Sprintf("⚠️ 发现 %d 处潜在 Breaking Change：\n\n", len(breaking)))
		for _, f := range breaking {
			sb.WriteString(fmt.Sprintf("**`%s`** 在 `%s` 发生变更\n", f.SymbolName, f.FilePath))
			sb.WriteString(fmt.Sprintf("- 影响范围：%s\n", strings.Join(f.AffectedFiles, ", ")))
			sb.WriteString(fmt.Sprintf("- 建议：%s\n\n", f.Suggestion))
		}
	}
	if len(others) > 0 {
		sb.WriteString("### 其他变更提示\n\n")
		for _, f := range others {
			sb.WriteString(fmt.Sprintf("- **%s** `%s` — %s\n", f.Type, f.FilePath, f.ChangeDesc))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// buildTestSuggestionSection 测试建议 Prompt 段落
func buildTestSuggestionSection(suggestions []TestSuggestionItem) string {
	var sb strings.Builder
	sb.WriteString("## 测试建议\n\n")
	sb.WriteString("以下函数建议补充测试场景：\n\n")
	sb.WriteString("| 函数 | 文件 | 建议测试场景 |\n")
	sb.WriteString("|:---|:---|:---|\n")
	for _, s := range suggestions {
		var sceneNames []string
		for _, sc := range s.Scenarios {
			sceneNames = append(sceneNames, sc.Name)
		}
		scenes := strings.Join(sceneNames, " · ")
		sb.WriteString(fmt.Sprintf("| `%s` | `%s:%d` | %s |\n", s.FunctionName, s.FilePath, s.LineNumber, scenes))
	}
	sb.WriteString("\n> 注：以上为基于代码分支结构的自动分析建议，实际测试场景需结合业务逻辑判断。\n\n")
	return sb.String()
}

// buildNoRepeatInstruction 构建禁止重复报告 Agent 发现的指令
func buildNoRepeatInstruction() string {
	return "\n## 【禁止重复】Agent 已确认的问题\n\n" +
		"以上列出的密钥泄露扫描和安全审计结果是已由规则引擎高置信度确认的问题。\n\n" +
		"⚠️ 你在本次批次评审中 **不要** 在 `issues[]` 中重复报告这些问题。\n\n" +
		"正确的做法：\n" +
		"1. 如果某条 Agent 发现在你的批次代码中确实存在，但你认为可以补充更多上下文 → 在 `recommendations[]` 中提出建议\n" +
		"2. 如果 Agent 发现的位置在本批次代码中不存在 → 完全忽略，不输出任何相关内容\n" +
		"3. 如果你发现了 Agent 未提及的**全新问题** → 正常放入 `issues[]`\n\n" +
		"错误示例（不要做）：\n" +
		"```json\n" +
		"{ \"file\": \"config.go\", \"line_start\": 42, \"message\": \"发现 API_KEY 泄露\" }\n" +
		"```\n" +
		"→ 这条如果已在 Agent 扫描结果中列出，你重复报告会增加去重负担。\n\n"
}
