package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// ========== SecurityAuditExecutor ==========

type SecurityAuditExecutor struct {
	llmService LLMService
	modelID    uint
}

func NewSecurityAuditExecutor(llm LLMService, modelID uint) *SecurityAuditExecutor {
	return &SecurityAuditExecutor{llmService: llm, modelID: modelID}
}

func (e *SecurityAuditExecutor) Code() string { return "security_audit" }

func (e *SecurityAuditExecutor) Execute(ctx StageContext) error {
	// 检查任务是否已被取消
	select {
	case <-ctx.Done():
		return fmt.Errorf("任务已取消")
	default:
	}

	task := ctx.Task()
	if task == nil {
		return fmt.Errorf("task is nil")
	}

	// 读取智能体配置
	var cfg *model.ReviewAgentConfig
	if v, ok := ctx.GetInput("_agent_config").(model.ReviewAgentConfig); ok {
		cfg = &v
	}

	// 读取本阶段可配置参数
	llmMaxTokens := 2000
	llmTimeoutSec := 300
	functionBodyMaxLen := 1500
	triggerSnippetMaxLen := 300
	confidenceThreshold := 0.5
	if cfg != nil {
		llmMaxTokens = cfg.GetStageParam("security_audit", "llm_max_tokens", llmMaxTokens)
		llmTimeoutSec = cfg.GetStageParam("security_audit", "llm_timeout_sec", llmTimeoutSec)
		functionBodyMaxLen = cfg.GetStageParam("security_audit", "function_body_max_len", functionBodyMaxLen)
		triggerSnippetMaxLen = cfg.GetStageParam("security_audit", "trigger_snippet_max_len", triggerSnippetMaxLen)
		confidenceThreshold = cfg.GetStageParamFloat("security_audit", "verify_confidence_threshold", confidenceThreshold)
	}

	fileContents, _ := ctx.GetOutput("file_contents").(map[string]string)

	// 1. 加载启用规则（按语言筛选）
	files, _ := ctx.GetInput("diff_files").([]map[string]interface{})
	lang := detectLanguageFromFiles(files)
	rules := e.loadEnabledRules(lang)

	// 2. 获取 AST 上下文和 diff 文件
	astCtx := getASTContextFromStage(ctx)
	diffFiles, _ := ctx.GetInput("diff_files").([]map[string]interface{})

	var findings []model.SecurityAuditFinding

	// 3. AST 模式匹配（Mode=ast 的规则）
	astRules := filterAuditRules(rules, "ast")
	if astCtx != nil && len(astRules) > 0 {
		findings = append(findings, e.matchAST(astCtx, astRules, task.ID, triggerSnippetMaxLen)...)
	}

	// 4. 正则模式匹配（Mode=regex 的规则，兜底）
	regexRules := filterAuditRules(rules, "regex")
	if len(regexRules) > 0 && len(diffFiles) > 0 {
		findings = append(findings, e.matchRegex(diffFiles, regexRules, task.ID, fileContents, triggerSnippetMaxLen)...)
	}

	// 5. 去重：同一位置同一规则只保留一条
	findings = dedupAuditFindings(findings)

	// 6. LLM 增强验证（Phase 3）
	verifiedFindings := findings
	verifiedCount := 0
	var verificator *AgentVerificator
	if e.llmService != nil && len(findings) > 0 {
		modelID := e.modelID
		if modelID == 0 && task.UsedModelID > 0 {
			modelID = task.UsedModelID
		}
		// modelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）
		verificator = NewAgentVerificator(e.llmService, VerificatorConfig{
			ModelID:              modelID,
			MaxTokens:            llmMaxTokens,
			VerifyTimeout:        time.Duration(llmTimeoutSec) * time.Second,
			FunctionBodyMaxLen:   functionBodyMaxLen,
			TriggerSnippetMaxLen: triggerSnippetMaxLen,
			ContextLinesBefore:   6,
			ContextLinesAfter:    4,
			ConfidenceThreshold:  confidenceThreshold,
		})

		// 从 code_understanding_structured 提取相关污点路径
		taintFlows := extractRelevantTaintFlows(ctx, findings)
		fileContentsWithTaint := make(map[string]string, len(fileContents)+len(taintFlows))
		for k, v := range fileContents {
			fileContentsWithTaint[k] = v
		}
		for k, v := range taintFlows {
			fileContentsWithTaint[k+"_taint"] = v
		}

		vres, err := verificator.VerifySecurityAudit(context.Background(), &task.ID, modelID, findings, fileContentsWithTaint)
		if err == nil && len(vres) > 0 {
			var updated []model.SecurityAuditFinding
			for _, vr := range vres {
				// LLM 判定为真实安全问题且置信度达到阈值时才保留
				if vr.Result.IsLeak && vr.Result.Confidence >= confidenceThreshold {
					updated = append(updated, vr.Original)
					verifiedCount++
				}
			}
			verifiedFindings = updated
			ctx.SetOutput("security_audit_llm_verified", true)
			ctx.SetOutput("security_audit_verified_count", verifiedCount)
		} else if err != nil {
			zap.L().Warn("security_audit LLM verification failed, falling back to rule engine results", zap.Error(err))
		}
	}

	// 7. 保存记录
	if len(verifiedFindings) > 0 {
		if err := model.DB.CreateInBatches(verifiedFindings, 100).Error; err != nil {
			zap.L().Warn("save security audit findings failed", zap.Error(err))
		}
	}

	// 8. 产出注入
	ctx.SetOutput("security_audit_findings", verifiedFindings)
	ctx.SetOutput("security_audit_count", len(verifiedFindings))

	securityAuditMarkdown := ""
	if len(verifiedFindings) > 0 {
		securityAuditMarkdown = buildSecurityAuditMarkdown(verifiedFindings)
		ctx.SetOutput("security_audit_markdown", securityAuditMarkdown)
	}

	llmPrompt := ""
	llmRawOutput := ""
	modelName := ""
	inputTokens := 0
	outputTokens := 0
	if verificator != nil {
		llmPrompt = verificator.LastPrompt()
		llmRawOutput = verificator.LastRawContent()
		modelName = verificator.LastModelName()
		inputTokens = verificator.LastInputTokens()
		outputTokens = verificator.LastOutputTokens()
	}

	// 9. 保存 Pipeline 快照
	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"rule_count": len(rules),
		"file_count": len(diffFiles),
		"ast_mode":   len(astRules) > 0,
		"regex_mode": len(regexRules) > 0,
	})
	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"finding_count":        len(verifiedFindings),
		"original_count":       len(findings),
		"verified_count":       verifiedCount,
		"findings":             verifiedFindings,
		"llm_prompt":           llmPrompt,
		"llm_prompt_available": llmPrompt != "",
		"llm_raw_output":       llmRawOutput,
		"model_name":           modelName,
		"input_tokens":         inputTokens,
		"output_tokens":        outputTokens,
		"prompt_injection":     securityAuditMarkdown,
	})

	return nil
}

func (e *SecurityAuditExecutor) loadEnabledRules(lang string) []model.SecurityAuditRule {
	var rules []model.SecurityAuditRule
	// 加载通用规则 + 当前语言特定规则
	query := model.DB.Where("enabled = ?", true)
	if lang != "" {
		query = query.Where("language = ? OR language = ?", "", lang)
	} else {
		query = query.Where("language = ?", "")
	}
	if err := query.Order("severity DESC, code ASC").Find(&rules).Error; err != nil {
		zap.L().Warn("load security audit rules failed", zap.Error(err))
		return nil
	}
	return rules
}

func filterAuditRules(rules []model.SecurityAuditRule, mode string) []model.SecurityAuditRule {
	var filtered []model.SecurityAuditRule
	for _, r := range rules {
		if r.Mode == mode {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// matchRegex 正则模式匹配（规则正则已预编译缓存）
func (e *SecurityAuditExecutor) matchRegex(files []map[string]interface{}, rules []model.SecurityAuditRule, taskID uint, fileContents map[string]string, triggerSnippetMaxLen int) []model.SecurityAuditFinding {
	var findings []model.SecurityAuditFinding

	// 预编译所有正则
	reCache := make(map[string]*regexp.Regexp, len(rules))
	for _, rule := range rules {
		if rule.RegexPattern == "" {
			continue
		}
		if re, err := regexp.Compile(rule.RegexPattern); err == nil {
			reCache[rule.Code] = re
		} else {
			zap.L().Warn("invalid security audit pattern", zap.String("code", rule.Code), zap.Error(err))
		}
	}

	for _, f := range files {
		path, _ := f["path"].(string)
		diff, _ := f["diff"].(string)
		if path == "" || diff == "" {
			continue
		}

		changedLines := extractChangedLines(diff)

		for lineNum, line := range changedLines {
			for _, rule := range rules {
				re, ok := reCache[rule.Code]
				if !ok {
					continue
				}
				if loc := re.FindStringIndex(line); loc != nil {
					fullStmt := strings.TrimSpace(line)
					if content, ok := fileContents[path]; ok && triggerSnippetMaxLen > 0 {
						extracted := extractStatementByLine(content, lineNum, triggerSnippetMaxLen)
						if extracted != "" {
							fullStmt = extracted
						}
					}
					findings = append(findings, model.SecurityAuditFinding{
						TaskID:         taskID,
						RuleCode:       rule.Code,
						FilePath:       path,
						LineNumber:     lineNum + 1,
						Snippet:        strings.TrimSpace(line),
						TriggerSnippet: fullStmt,
						Severity:       rule.Severity,
						Message:        rule.Description,
						Suggestion:     rule.Suggestion,
					})
				}
			}
		}
	}

	return findings
}

// matchAST AST 模式匹配（基于已有 ASTContext 结构做简化匹配）
func (e *SecurityAuditExecutor) matchAST(astCtx *ASTContext, rules []model.SecurityAuditRule, taskID uint, triggerSnippetMaxLen int) []model.SecurityAuditFinding {
	var findings []model.SecurityAuditFinding

	for _, fn := range astCtx.Functions {
		for _, rule := range rules {
			// 简化的 AST 匹配：基于函数签名和 ASTPattern 关键词
			if e.matchASTPattern(fn, rule.ASTPattern) {
				// 提取触发规则的精确语句
				triggerStmt := extractTriggerStatementFromBody(fn.Body, rule.ASTPattern, triggerSnippetMaxLen)

				// 构建函数签名字符串作为 snippet
				sig := fn.Name + "("
				for i, p := range fn.Params {
					if i > 0 {
						sig += ", "
					}
					sig += p
				}
				sig += ")"
				findings = append(findings, model.SecurityAuditFinding{
					TaskID:         taskID,
					RuleCode:       rule.Code,
					FilePath:       fn.FilePath,
					LineNumber:     fn.LineStart,
					Snippet:        sig,
					TriggerSnippet: triggerStmt,
					Severity:       rule.Severity,
					Message:        rule.Description,
					Suggestion:     rule.Suggestion,
				})
			}
		}
	}

	return findings
}

func (e *SecurityAuditExecutor) matchASTPattern(fn FunctionSignature, pattern string) bool {
	if pattern == "" {
		return false
	}

	// 简化实现：ASTPattern 是一个 DSL 字符串，支持以下模式：
	// - "unsafe" → 检查函数体是否包含 unsafe 关键字
	// - "Query" → 检查函数名或调用是否包含 Query
	// - "eval" → 检查函数体是否包含 eval
	// - "Sprintf" → 检查函数体是否包含 Sprintf
	// - "reflect" → 检查函数体或导入是否包含 reflect

	lowerPattern := strings.ToLower(pattern)
	lowerBody := strings.ToLower(fn.Body)
	lowerName := strings.ToLower(fn.Name)

	// 通用关键词匹配
	switch lowerPattern {
	case "unsafe":
		return strings.Contains(lowerBody, "unsafe")
	case "eval":
		return strings.Contains(lowerBody, "eval(")
	case "query":
		return strings.Contains(lowerBody, "query") || strings.Contains(lowerBody, "exec")
	case "sprintf":
		return strings.Contains(lowerBody, "sprintf")
	case "reflect":
		return strings.Contains(lowerBody, "reflect") || strings.Contains(lowerName, "reflect")
	case "deserialize", "unmarshal", "decode":
		return strings.Contains(lowerBody, "unmarshal") || strings.Contains(lowerBody, "decode") || strings.Contains(lowerBody, "deserialize")
	case "file操作", "os.open", "os.create":
		return strings.Contains(lowerBody, "os.open") || strings.Contains(lowerBody, "os.create") || strings.Contains(lowerBody, "ioutil")
	default:
		// 通用子串匹配
		return strings.Contains(lowerBody, lowerPattern) || strings.Contains(lowerName, lowerPattern)
	}
}

func dedupAuditFindings(findings []model.SecurityAuditFinding) []model.SecurityAuditFinding {
	seen := make(map[string]bool)
	var deduped []model.SecurityAuditFinding
	for _, f := range findings {
		key := fmt.Sprintf("%s:%s:%d", f.RuleCode, f.FilePath, f.LineNumber)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, f)
		}
	}
	return deduped
}

func buildSecurityAuditMarkdown(findings []model.SecurityAuditFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 安全审计报告\n\n发现 %d 处安全敏感操作：\n\n", len(findings)))
	sb.WriteString("| 文件 | 行号 | 规则 | 级别 | 建议 |\n")
	sb.WriteString("|:---|:---:|:---|:---:|:---|\n")

	for _, f := range findings {
		suggestion := f.Suggestion
		if suggestion == "" {
			suggestion = "—"
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %d | %s | %s | %s |\n", f.FilePath, f.LineNumber, f.RuleCode, f.Severity, suggestion))
	}

	sb.WriteString("\n请在评审中特别关注上述安全问题，确认是否存在实际风险。\n")
	return sb.String()
}

// extractTriggerStatementFromBody 在函数体文本中查找包含 trigger 关键词的完整语句。
// 返回该语句及其前后 1 行上下文，最长不超过 maxLen 字符。
func extractTriggerStatementFromBody(body, trigger string, maxLen int) string {
	if body == "" || trigger == "" {
		return ""
	}
	if maxLen <= 0 {
		maxLen = 300
	}
	lowerTrigger := strings.ToLower(trigger)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), lowerTrigger) {
			// 提取该语句（当前行 + 前后 1 行）
			start := i - 1
			if start < 0 {
				start = 0
			}
			end := i + 2
			if end > len(lines) {
				end = len(lines)
			}
			var sb strings.Builder
			for j := start; j < end; j++ {
				sb.WriteString(lines[j])
				sb.WriteString("\n")
			}
			result := sb.String()
			if utf8.RuneCountInString(result) > maxLen {
				result = truncateRunes(result, maxLen)
			}
			return result
		}
	}
	return ""
}

// extractStatementByLine 从完整文件内容中提取指定行号的完整语句。
// 向前扫描到语句开始，向后扫描到语句结束（Go 语句以 ; 或换行结束，块语句以 {} 包裹）。
func extractStatementByLine(content string, lineNum int, maxLen int) string {
	if content == "" || lineNum < 0 {
		return ""
	}
	if maxLen <= 0 {
		maxLen = 300
	}
	lines := strings.Split(content, "\n")
	if lineNum >= len(lines) {
		return ""
	}

	// 向前扫描到语句开始：跳过空行和注释行
	start := lineNum
	for start > 0 {
		prev := strings.TrimSpace(lines[start-1])
		if prev == "" || strings.HasPrefix(prev, "//") {
			start--
			continue
		}
		// 如果前一行以运算符或逗号结尾，说明当前行是语句的延续
		if strings.HasSuffix(prev, ",") || strings.HasSuffix(prev, "+") || strings.HasSuffix(prev, "-") ||
			strings.HasSuffix(prev, "*") || strings.HasSuffix(prev, "/") || strings.HasSuffix(prev, "=") ||
			strings.HasSuffix(prev, "&") || strings.HasSuffix(prev, "|") || strings.HasSuffix(prev, "&&") ||
			strings.HasSuffix(prev, "||") || strings.HasSuffix(prev, "<<") || strings.HasSuffix(prev, ">>") ||
			strings.HasSuffix(prev, ":=") || strings.HasSuffix(prev, "+=") || strings.HasSuffix(prev, "-=") ||
			strings.HasSuffix(prev, "*=") || strings.HasSuffix(prev, "/=") || strings.HasSuffix(prev, "%=") ||
			strings.HasSuffix(prev, "^=") || strings.HasSuffix(prev, "&=") || strings.HasSuffix(prev, "|=") {
			start--
			continue
		}
		break
	}

	// 向后扫描到语句结束
	end := lineNum
	for end < len(lines)-1 {
		next := strings.TrimSpace(lines[end+1])
		if next == "" || strings.HasPrefix(next, "//") {
			end++
			continue
		}
		// 如果当前行以运算符结尾，说明下一行是延续
		curr := strings.TrimSpace(lines[end])
		// 扩展续行运算符判断：包含 Go 中可能出现在行尾的所有二元/复合运算符
		if strings.HasSuffix(curr, ",") || strings.HasSuffix(curr, "+") || strings.HasSuffix(curr, "-") ||
			strings.HasSuffix(curr, "*") || strings.HasSuffix(curr, "/") || strings.HasSuffix(curr, "=") ||
			strings.HasSuffix(curr, "&") || strings.HasSuffix(curr, "|") || strings.HasSuffix(curr, "&&") ||
			strings.HasSuffix(curr, "||") || strings.HasSuffix(curr, "<<") || strings.HasSuffix(curr, ">>") ||
			strings.HasSuffix(curr, ":=") || strings.HasSuffix(curr, "+=") || strings.HasSuffix(curr, "-=") ||
			strings.HasSuffix(curr, "*=") || strings.HasSuffix(curr, "/=") || strings.HasSuffix(curr, "%=") ||
			strings.HasSuffix(curr, "^=") || strings.HasSuffix(curr, "&=") || strings.HasSuffix(curr, "|=") {
			end++
			continue
		}
		break
	}

	var sb strings.Builder
	for i := start; i <= end && i < len(lines); i++ {
		sb.WriteString(lines[i])
		sb.WriteString("\n")
	}
	result := sb.String()
	if utf8.RuneCountInString(result) > maxLen {
		result = truncateRunes(result, maxLen)
	}
	return result
}

// 向前扫描时同样扩展运算符判断（内部辅助函数）
// 内联在 extractStatementByLine 中使用，避免重复。

// extractRelevantTaintFlows 从 code_understanding_structured 中提取与当前审计文件相关的污点路径。
func extractRelevantTaintFlows(ctx StageContext, findings []model.SecurityAuditFinding) map[string]string {
	result := make(map[string]string)
	codeReport, ok := ctx.GetOutput("code_understanding_structured").(*CodeUnderstandingReport)
	if !ok || codeReport == nil || len(codeReport.TaintFlows) == 0 {
		return result
	}

	// 收集审计涉及的文件集合
	auditFiles := make(map[string]bool)
	for _, f := range findings {
		auditFiles[f.FilePath] = true
	}

	// 筛选与审计文件相关的污点流
	for _, tf := range codeReport.TaintFlows {
		if auditFiles[tf.SourceFile] || auditFiles[tf.SinkFile] {
			key := tf.SinkFile
			entry := fmt.Sprintf("【数据流上下文】\n- Source: %s (%s)\n", tf.SourceFunc, tf.SourceFile)
			entry += fmt.Sprintf("  → SINK: %s (%s) [风险: %s]\n", tf.SinkFunc, tf.SinkFile, tf.RiskLevel)
			if existing, ok := result[key]; ok {
				result[key] = existing + "\n" + entry
			} else {
				result[key] = entry
			}
		}
	}
	return result
}
