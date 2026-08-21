package pipeline

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// ========== SecretScanExecutor ==========

type SecretScanExecutor struct {
	llmService LLMService
	modelID    uint
}

func NewSecretScanExecutor(llm LLMService, modelID uint) *SecretScanExecutor {
	return &SecretScanExecutor{llmService: llm, modelID: modelID}
}

func (e *SecretScanExecutor) Code() string { return "secret_scan" }

func (e *SecretScanExecutor) Execute(ctx StageContext) error {
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

	// 读取本阶段可配置参数（均有默认值）
	llmMaxTokens := 2000
	llmTimeoutSec := int(getStageLLMEnhanceTimeout("secret_scan").Seconds())
	matchTextMaxLen := 80
	functionBodyMaxLen := 800
	confidenceThreshold := 0.5
	if cfg != nil {
		llmMaxTokens = cfg.GetStageParam("secret_scan", "llm_max_tokens", llmMaxTokens)
		llmTimeoutSec = cfg.GetStageParam("secret_scan", "llm_enhance_timeout", llmTimeoutSec)
		matchTextMaxLen = cfg.GetStageParam("secret_scan", "match_text_max_len", matchTextMaxLen)
		functionBodyMaxLen = cfg.GetStageParam("secret_scan", "function_body_max_len", functionBodyMaxLen)
		confidenceThreshold = cfg.GetStageParamFloat("secret_scan", "verify_confidence_threshold", confidenceThreshold)
	}

	// 1. 加载启用规则
	rules := e.loadEnabledRules()

	// 2. 获取 diff 文件
	diffFiles, ok := ctx.GetInput("diff_files").([]map[string]interface{})
	if !ok || len(diffFiles) == 0 {
		return nil
	}

	var findings []model.SecretScanFinding
	detector := newSecretDetector(rules)

	// 3. 逐文件扫描（只扫描 diff 变更行）
	for _, f := range diffFiles {
		path, _ := f["path"].(string)
		diff, _ := f["diff"].(string)
		if path == "" || diff == "" {
			continue
		}

		changedLines := extractChangedLines(diff)
		fileFindings := detector.Scan(changedLines, path, task.ID)
		findings = append(findings, fileFindings...)
	}

	// 4. LLM 增强验证（Phase 3）
	verifiedFindings := findings
	verifiedCount := 0
	var verificator *AgentVerificator
	llmFallbackReason := ""
	if e.llmService != nil && len(findings) > 0 {
		fileContents, _ := ctx.GetOutput("file_contents").(map[string]string)
		modelID := e.modelID
		if modelID == 0 && task.UsedModelID > 0 {
			modelID = task.UsedModelID
		}
		// modelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）
		verificator = NewAgentVerificator(e.llmService, VerificatorConfig{
			ModelID:             modelID,
			MaxTokens:           llmMaxTokens,
			VerifyTimeout:       time.Duration(llmTimeoutSec) * time.Second,
			FunctionBodyMaxLen:  functionBodyMaxLen,
			MatchTextMaxLen:     matchTextMaxLen,
			ContextLinesBefore:  6,
			ContextLinesAfter:   4,
			ConfidenceThreshold: confidenceThreshold,
		})
		vres, err := verificator.VerifySecretScan(ctx, &task.ID, modelID, findings, fileContents)
		if err == nil && len(vres) > 0 {
			var updated []model.SecretScanFinding
			for _, vr := range vres {
				// LLM 判定为真实泄露且置信度达到阈值时才保留
				if vr.Result.IsLeak && vr.Result.Confidence >= confidenceThreshold {
					updated = append(updated, vr.Original)
					verifiedCount++
				}
			}
			verifiedFindings = updated
			ctx.SetOutput("secret_scan_llm_verified", true)
			ctx.SetOutput("secret_scan_verified_count", verifiedCount)
		} else if err != nil {
			zap.L().Warn("secret_scan LLM verification failed, falling back to rule engine results", zap.Error(err))
			llmFallbackReason = "规则引擎结果（LLM验证超时/失败）"
		}
	}

	// 5. 保存发现记录
	if len(verifiedFindings) > 0 {
		if err := model.DB.CreateInBatches(verifiedFindings, 100).Error; err != nil {
			zap.L().Warn("save secret scan findings failed", zap.Error(err))
		}
	}

	// 6. 产出注入 StageContext
	ctx.SetOutput("secret_scan_findings", verifiedFindings)
	ctx.SetOutput("secret_scan_count", len(verifiedFindings))

	secretScanMarkdown := ""
	if len(verifiedFindings) > 0 {
		secretScanMarkdown = buildSecretScanMarkdown(verifiedFindings)
		ctx.SetOutput("secret_scan_markdown", secretScanMarkdown)
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

		// 【修复】将 token 同步到 selfExec，供 MarkSuccess 写入 task_pipeline_executions
		if exec := ctx.GetSelfExec(); exec != nil {
			if inputTokens > 0 {
				exec.InputTokens = inputTokens
			}
			if outputTokens > 0 {
				exec.OutputTokens = outputTokens
			}
			if modelName != "" {
				exec.ModelName = modelName
			}
		}
	}

	// 7. 保存 Pipeline 快照
	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"rule_count": len(rules),
		"file_count": len(diffFiles),
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
		"prompt_injection":     secretScanMarkdown,
		"llm_fallback_reason":  llmFallbackReason,
	})

	return nil
}

func (e *SecretScanExecutor) loadEnabledRules() []model.SecretScanRule {
	var rules []model.SecretScanRule
	if err := model.DB.Where("enabled = ?", true).Order("severity DESC, code ASC").Find(&rules).Error; err != nil {
		zap.L().Warn("load secret scan rules failed", zap.Error(err))
		return nil
	}
	return rules
}

// extractChangedLines 从 diff 中提取新增/修改行（以 + 开头，且不是文件头）
func extractChangedLines(diff string) []string {
	var lines []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			lines = append(lines, line[1:]) // 去掉 + 前缀
		}
	}
	return lines
}

// SecretDetector 密钥检测器
type SecretDetector struct {
	rules   []model.SecretScanRule
	regexps map[string]*regexp.Regexp // code -> 预编译正则
}

func newSecretDetector(rules []model.SecretScanRule) *SecretDetector {
	d := &SecretDetector{
		rules:   rules,
		regexps: make(map[string]*regexp.Regexp, len(rules)),
	}
	for _, r := range rules {
		if re, err := regexp.Compile(r.Pattern); err == nil {
			d.regexps[r.Code] = re
		} else {
			zap.L().Warn("invalid secret scan pattern", zap.String("code", r.Code), zap.Error(err))
		}
	}
	return d
}

func (d *SecretDetector) Scan(lines []string, filePath string, taskID uint) []model.SecretScanFinding {
	var findings []model.SecretScanFinding

	for lineNum, line := range lines {
		for _, rule := range d.rules {
			// 关键词预过滤
			if rule.Keywords != "" && !hasKeyword(line, rule.Keywords) {
				continue
			}

			re, ok := d.regexps[rule.Code]
			if !ok {
				continue
			}

			if matches := re.FindAllStringIndex(line, -1); len(matches) > 0 {
				for _, m := range matches {
					matchText := line[m[0]:m[1]]
					maskedText := maskSecret(matchText)

					findings = append(findings, model.SecretScanFinding{
						TaskID:       taskID,
						RuleCode:     rule.Code,
						FilePath:     filePath,
						LineNumber:   lineNum + 1,
						MatchText:    maskedText,
						RawMatchText: matchText, // 【新增】保留原始值供 LLM 验证使用
						Severity:     rule.Severity,
						Description:  rule.Description,
					})
				}
			}
		}
	}

	return findings
}

func hasKeyword(line, keywords string) bool {
	lowerLine := strings.ToLower(line)
	for _, kw := range strings.Split(keywords, ",") {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if strings.Contains(lowerLine, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

func buildSecretScanMarkdown(findings []model.SecretScanFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 密钥扫描报告\n\n发现 %d 处潜在密钥泄露：\n\n", len(findings)))
	sb.WriteString("| 文件 | 行号 | 类型 | 严重级别 |\n")
	sb.WriteString("|:---|:---:|:---|:---:|\n")

	for _, f := range findings {
		sb.WriteString(fmt.Sprintf("| `%s` | %d | %s | %s |\n", f.FilePath, f.LineNumber, f.RuleCode, f.Severity))
	}

	sb.WriteString("\n请重点检查上述位置，确认是否为真实密钥泄露。如果是误报（如测试数据、示例代码），请在回复中说明。\n")
	return sb.String()
}
