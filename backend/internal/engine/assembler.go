package engine

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/ai-optimizer/backend/pkg/llm"
)

// CommentTemplateContext GitLab 评论模板上下文
type CommentTemplateContext struct {
	TaskID               uint
	ProjectName          string
	MRTitle              string
	MRAuthor             string
	TotalScore           int
	Summary              string
	DimensionsTable      string // 预渲染 Markdown 表格
	Dimensions           []DimensionContext
	IssuesList           string // 预渲染 Markdown 列表
	Issues               []IssueContext
	IssueCount           int
	CriticalCount        int
	HighCount            int
	MediumCount          int
	LowCount             int
	InfoCount            int
	Recommendations      []string
	RecommendationsList  string // 预渲染
	DependencyVulns      []DependencyVulnContext
	DependencyVulnsList  string // 预渲染 Markdown 列表
	HasDependencyVulns   bool
	BR                   string // 换行
}

// DependencyVulnContext 依赖漏洞上下文
type DependencyVulnContext struct {
	PackageName    string
	CurrentVersion string
	VulnID         string
	Aliases        string                     // CVE-XXXX-XXXX, GHSA-XXXX (逗号分隔)
	Severity       string
	SeverityLabel  string
	SeverityEmoji  string
	Summary        string
	FixedVersion   string
}

// DimensionContext 维度上下文
type DimensionContext struct {
	Name        string
	Label       string
	Score       int
	Weight      int
	WeightLabel string
}

// IssueContext Issue 上下文
type IssueContext struct {
	Severity      string
	SeverityLabel string
	SeverityEmoji string
	DeductScore   int
	RuleCode      string
	RuleName      string // 可从 rule_code 映射
	Category      string
	File          string
	LineStart     int
	LineEnd       int
	CodeSnippet   string
	Message       string
	Suggestion    string
}

// DefaultGitLabCommentTemplate 默认 GitLab 评论模板
const DefaultGitLabCommentTemplate = `## 🤖 AI 代码评审报告

{{if gt .CriticalCount 0}}
⚠️ **发现 {{.CriticalCount}} 个严重问题，请立即处理！**
{{end}}

**综合评分：{{.TotalScore}}/100**

{{if .Summary}}
> 📋 **评审摘要：** {{.Summary}}
{{end}}

### 📊 维度评分
{{.DimensionsTable}}

{{if .HasDependencyVulns}}
### 🛡️ 依赖漏洞扫描
{{.DependencyVulnsList}}
{{end}}

{{if gt .IssueCount 0}}
### ⚠️ 发现的问题（共 {{.IssueCount}} 个）
{{.IssuesList}}
{{end}}

{{if .Recommendations}}
### 💡 改进建议
{{.RecommendationsList}}
{{end}}
`

// AssembleMarkdownComment 将结构化评审结果组装为 Markdown 评论（向后兼容）
func AssembleMarkdownComment(result *llm.AIReviewResult, tmplStr string) (string, error) {
	return AssembleMarkdownCommentWithVulns(result, tmplStr, nil)
}

// AssembleMarkdownCommentWithVulns 将结构化评审结果组装为 Markdown 评论（含依赖漏洞）
func AssembleMarkdownCommentWithVulns(result *llm.AIReviewResult, tmplStr string, vulns []DependencyVuln) (string, error) {
	if tmplStr == "" {
		tmplStr = DefaultGitLabCommentTemplate
	}

	ctx := buildCommentContext(result, vulns)

	tmpl, err := template.New("comment").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse comment template failed: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("execute comment template failed: %w", err)
	}

	return buf.String(), nil
}

// buildCommentContext 构建模板上下文
func buildCommentContext(result *llm.AIReviewResult, vulns []DependencyVuln) *CommentTemplateContext {
	ctx := &CommentTemplateContext{
		TotalScore:      result.TotalScore,
		Summary:         result.Summary,
		IssueCount:      len(result.Issues),
		Recommendations: result.Recommendations,
		BR:              "\n\n",
	}

	// 统计各严重级别数量
	for _, issue := range result.Issues {
		switch issue.Severity {
		case "critical":
			ctx.CriticalCount++
		case "high":
			ctx.HighCount++
		case "medium":
			ctx.MediumCount++
		case "low":
			ctx.LowCount++
		case "info":
			ctx.InfoCount++
		}
	}

	// 构建维度列表
	for name, dim := range result.Dimensions {
		ctx.Dimensions = append(ctx.Dimensions, DimensionContext{
			Name:        name,
			Label:       dimensionLabel(name),
			Score:       dim.Score,
			Weight:      dim.Weight,
			WeightLabel: fmt.Sprintf("%d%%", dim.Weight),
		})
	}

	// 预渲染维度表格
	ctx.DimensionsTable = buildDimensionsTable(ctx.Dimensions)

	// 构建 Issue 列表
	for _, issue := range result.Issues {
		ctx.Issues = append(ctx.Issues, IssueContext{
			Severity:      issue.Severity,
			SeverityLabel: severityLabel(issue.Severity),
			SeverityEmoji: severityEmoji(issue.Severity),
			DeductScore:   issue.DeductScore,
			RuleCode:      issue.RuleCode,
			Category:      issue.Category,
			File:          issue.File,
			LineStart:     issue.LineStart,
			LineEnd:       issue.LineEnd,
			CodeSnippet:   issue.CodeSnippet,
			Message:       issue.Message,
			Suggestion:    issue.Suggestion,
		})
	}

	// 预渲染 Issue 列表
	ctx.IssuesList = buildIssuesList(ctx.Issues)

	// 预渲染改进建议
	ctx.RecommendationsList = buildRecommendationsList(ctx.Recommendations)

	// 处理依赖漏洞
	if len(vulns) > 0 {
		ctx.HasDependencyVulns = true
		for _, v := range vulns {
			ctx.DependencyVulns = append(ctx.DependencyVulns, DependencyVulnContext{
				PackageName:    v.PackageName,
				CurrentVersion: v.CurrentVersion,
				VulnID:         v.VulnID,
				Aliases:        v.Aliases,
				Severity:       v.Severity,
				SeverityLabel:  severityLabel(v.Severity),
				SeverityEmoji:  severityEmoji(v.Severity),
				Summary:        v.Summary,
				FixedVersion:   v.FixedVersion,
			})
		}
		ctx.DependencyVulnsList = buildDependencyVulnsList(ctx.DependencyVulns)
	}

	return ctx
}

// buildDimensionsTable 预渲染维度评分 Markdown 表格
func buildDimensionsTable(dims []DimensionContext) string {
	if len(dims) == 0 {
		return "（无维度评分）"
	}
	var b strings.Builder
	b.WriteString("| 维度 | 得分 | 权重 |\n")
	b.WriteString("|------|------|------|\n")
	for _, d := range dims {
		b.WriteString(fmt.Sprintf("| %s | %d | %s |\n", d.Label, d.Score, d.WeightLabel))
	}
	return b.String()
}

// buildIssuesList 预渲染 Issue Markdown 列表
func buildIssuesList(issues []IssueContext) string {
	if len(issues) == 0 {
		return "（未发现明确问题）"
	}

	grouped := groupIssuesBySeverity(issues)
	var b strings.Builder

	severityOrder := []string{"critical", "high", "medium", "low", "info"}
	chineseNum := []string{"一", "二", "三", "四", "五"}

	for i, sev := range severityOrder {
		if group, ok := grouped[sev]; ok && len(group) > 0 {
			label := severitySectionLabel(sev)
			b.WriteString(fmt.Sprintf("#### %s、%s (%d)\n\n", chineseNum[i], emojiStrip(label), len(group)))
			for _, issue := range group {
				scoreLabel := ""
				if issue.DeductScore > 0 {
					scoreLabel = fmt.Sprintf("【扣%d分】", issue.DeductScore)
				}
				b.WriteString(fmt.Sprintf("**%s [%s] %s%s**\n\n", issue.SeverityEmoji, issue.RuleCode, scoreLabel, issue.Message))
				if issue.File != "" {
					b.WriteString(fmt.Sprintf("- **文件**：`%s`", issue.File))
					if issue.LineStart > 0 {
						b.WriteString(fmt.Sprintf(" (第 %d", issue.LineStart))
						if issue.LineEnd > issue.LineStart {
							b.WriteString(fmt.Sprintf("-%d", issue.LineEnd))
						}
						b.WriteString(" 行)")
					}
					b.WriteString("\n")
				}
				if issue.CodeSnippet != "" {
					b.WriteString(fmt.Sprintf("```\n%s\n```\n", issue.CodeSnippet))
				}
				if issue.Suggestion != "" {
					b.WriteString(fmt.Sprintf("- **建议**：%s\n", issue.Suggestion))
				}
				b.WriteString("\n")
			}
		}
	}

	return b.String()
}

// buildRecommendationsList 预渲染改进建议
func buildRecommendationsList(recommendations []string) string {
	if len(recommendations) == 0 {
		return ""
	}
	var b strings.Builder
	for _, rec := range recommendations {
		b.WriteString(fmt.Sprintf("- %s\n", rec))
	}
	return b.String()
}

// buildDependencyVulnsList 预渲染依赖漏洞 Markdown 列表
func buildDependencyVulnsList(vulns []DependencyVulnContext) string {
	if len(vulns) == 0 {
		return ""
	}
	var b strings.Builder
	grouped := make(map[string][]DependencyVulnContext)
	for _, v := range vulns {
		grouped[v.Severity] = append(grouped[v.Severity], v)
	}

	severityOrder := []string{"critical", "high", "medium", "low"}
	for _, sev := range severityOrder {
		if group, ok := grouped[sev]; ok && len(group) > 0 {
			b.WriteString(fmt.Sprintf("\n**%s** (%d)\n\n", severitySectionLabel(sev), len(group)))
			for _, v := range group {
				aliasPart := ""
				if v.Aliases != "" {
					aliasPart = fmt.Sprintf(" (%s)", v.Aliases)
				}
				b.WriteString(fmt.Sprintf("- %s **%s** %s → %s%s\n", v.SeverityEmoji, v.PackageName, v.CurrentVersion, v.VulnID, aliasPart))
				if v.Summary != "" {
					b.WriteString(fmt.Sprintf("  - %s\n", v.Summary))
				}
				if v.FixedVersion != "" {
					b.WriteString(fmt.Sprintf("  - 建议升级至：**%s**\n", v.FixedVersion))
				}
			}
		}
	}
	return b.String()
}

// groupIssuesBySeverity 按严重级别分组
func groupIssuesBySeverity(issues []IssueContext) map[string][]IssueContext {
	grouped := make(map[string][]IssueContext)
	for _, issue := range issues {
		grouped[issue.Severity] = append(grouped[issue.Severity], issue)
	}
	return grouped
}

// dimensionLabel 维度名称映射（GitLab 推送报告用）
// 内置 5 个维度硬编码中文；自定义维度直接返回原名
func dimensionLabel(name string) string {
	labels := map[string]string{
		"security":        "安全性",
		"performance":     "性能",
		"readability":     "可读性",
		"maintainability": "可维护性",
		"test_coverage":   "测试覆盖",
	}
	if v, ok := labels[name]; ok {
		return v
	}
	return name
}

// severityLabel 严重级别标签
func severityLabel(severity string) string {
	labels := map[string]string{
		"critical": "严重",
		"high":     "高危",
		"medium":   "中危",
		"low":      "低危",
		"info":     "提示",
	}
	if v, ok := labels[severity]; ok {
		return v
	}
	return severity
}

// severityEmoji 严重级别 Emoji
func severityEmoji(severity string) string {
	emojis := map[string]string{
		"critical": "🔴",
		"high":     "🟠",
		"medium":   "🟡",
		"low":      "🔵",
		"info":     "⚪",
	}
	if v, ok := emojis[severity]; ok {
		return v
	}
	return "⚪"
}

// severitySectionLabel 严重级别章节标题
func severitySectionLabel(severity string) string {
	labels := map[string]string{
		"critical": "🔴 严重",
		"high":     "🟠 高危",
		"medium":   "🟡 中危",
		"low":      "🔵 低危",
		"info":     "⚪ 提示",
	}
	if v, ok := labels[severity]; ok {
		return v
	}
	return severity
}

// emojiStrip 移除字符串中的 emoji（保留空格）
func emojiStrip(s string) string {
	var result []rune
	for _, r := range s {
		// 跳过常见 emoji rune 范围
		if (r >= 0x1F300 && r <= 0x1F9FF) || // 杂项符号和象形文字
			(r >= 0x2600 && r <= 0x26FF) || // 杂项符号
			(r >= 0x2700 && r <= 0x27BF) || // 装饰符号
			(r >= 0xFE00 && r <= 0xFE0F) || // 变体选择器
			(r >= 0x1F600 && r <= 0x1F64F) || // 表情符号
			(r >= 0x1F680 && r <= 0x1F6FF) || // 交通和地图符号
			(r >= 0x1F1E0 && r <= 0x1F1FF) { // 国旗
			continue
		}
		result = append(result, r)
	}
	return strings.TrimSpace(string(result))
}

// ==================== 【新增】Phase 2 扩展区域 Markdown 渲染 ====================

// AssembleFullMarkdownReport 组装完整 Markdown 报告（含 SecurityFindings / TestingNotes / ImpactNotes 独立区域）
func AssembleFullMarkdownReport(
	result *llm.AIReviewResult,
	template string,
	vulns []DependencyVuln,
) (string, error) {
	// 步骤 1：组装基础报告（评分 + Issues + Recommendations + DependencyVulns）
	report, err := AssembleMarkdownCommentWithVulns(result, template, vulns)
	if err != nil {
		// fallback 到简易报告
		report = fmt.Sprintf("## 🤖 AI 代码评审报告\n\n**综合评分：%d/100**\n\n%s",
			result.TotalScore, result.Summary)
	}

	var sb strings.Builder
	sb.WriteString(report)

	// 步骤 2：追加测试建议独立区域（TestingNotes）
	if len(result.TestingNotes) > 0 {
		sb.WriteString("\n\n---\n\n### 🧪 测试建议（")
		sb.WriteString(fmt.Sprintf("%d", len(result.TestingNotes)))
		sb.WriteString("）\n\n")
		for _, note := range result.TestingNotes {
			sb.WriteString(fmt.Sprintf("**`%s`** @ %s:%d\n\n", note.FunctionName, note.FilePath, note.LineNumber))
			sb.WriteString("| 优先级 | 场景 | 输入 | 期望 | 理由 |\n")
			sb.WriteString("|:---|:---|:---|:---|:---|\n")
			for _, s := range note.Scenarios {
				sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
					s.Priority, s.Name, s.Input, s.Expected, s.Reasoning))
			}
			sb.WriteString("\n")
		}
	}

	// 步骤 3：追加影响分析独立区域（ImpactNotes）
	if len(result.ImpactNotes) > 0 {
		sb.WriteString("\n\n---\n\n### 📊 变更影响分析（")
		sb.WriteString(fmt.Sprintf("%d", len(result.ImpactNotes)))
		sb.WriteString("）\n\n")
		for _, note := range result.ImpactNotes {
			sb.WriteString(fmt.Sprintf("**%s** `%s` @ %s\n\n", note.Type, note.SymbolName, note.FilePath))
			sb.WriteString(fmt.Sprintf("- **描述**：%s\n", note.Description))
			sb.WriteString(fmt.Sprintf("- **严重级别**：%s\n", note.Severity))
			sb.WriteString(fmt.Sprintf("- **兼容度**：%s\n", note.Compatibility))
			if len(note.AffectedFiles) > 0 {
				sb.WriteString(fmt.Sprintf("- **受影响文件**：%s\n", strings.Join(note.AffectedFiles, ", ")))
			}
			if note.MigrationSteps != "" {
				sb.WriteString(fmt.Sprintf("- **迁移步骤**：\n%s\n", note.MigrationSteps))
			}
			sb.WriteString("\n")
		}
	}

	// 步骤 4：追加总体建议
	if result.OverallSuggestion != "" {
		sb.WriteString("\n\n---\n\n### 📋 综合建议\n\n")
		sb.WriteString(result.OverallSuggestion)
		sb.WriteString("\n")
	}

	return sb.String(), nil
}
