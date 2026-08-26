package pipeline

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

// ReviewArbitrationExecutor 评审裁决阶段执行器（SortOrder=55）
type ReviewArbitrationExecutor struct {
	llmService LLMService
	modelID    uint
}

// NewReviewArbitrationExecutor 创建 ReviewArbitrationExecutor
func NewReviewArbitrationExecutor(llm LLMService, modelID uint) *ReviewArbitrationExecutor {
	return &ReviewArbitrationExecutor{
		llmService: llm,
		modelID:    modelID,
	}
}

func (e *ReviewArbitrationExecutor) Code() string { return "review_arbitration" }

// Execute 执行评审裁决阶段
func (e *ReviewArbitrationExecutor) Execute(ctx StageContext) error {
	// 检查任务是否已被取消
	select {
	case <-ctx.Done():
		return fmt.Errorf("任务已取消")
	default:
	}

	// 1. 读取 Agent Findings
	agentData := AgentFindingCollection{}
	if findings, ok := ctx.GetOutput("secret_scan_findings").([]model.SecretScanFinding); ok {
		agentData.SecretScan = findings
	}
	if findings, ok := ctx.GetOutput("security_audit_findings").([]model.SecurityAuditFinding); ok {
		agentData.SecurityAudit = findings
	}
	if suggestions, ok := ctx.GetOutput("test_suggestions").([]engine.TestSuggestionItem); ok {
		agentData.TestSuggestions = suggestions
	}
	if findings, ok := ctx.GetOutput("impact_analysis_findings").([]engine.ImpactFinding); ok {
		agentData.ImpactFindings = findings
	}

	// 1.1 读取依赖漏洞扫描结果（dependency_scan 阶段产出）
	var depVulns []engine.DependencyVuln
	if v, ok := ctx.GetOutput("dependency_vulns").([]engine.DependencyVuln); ok && len(v) > 0 {
		depVulns = v
	}

	// 2. 读取 Batch Review Results
	batchResults, ok := ctx.GetOutput("batch_review_results").([]*llm.BatchReviewResult)
	if !ok || len(batchResults) == 0 {
		zap.L().Warn("review_arbitration: 未找到 batch_review_results，仅输出 Agent findings")
		batchResults = []*llm.BatchReviewResult{}
	}

	// 3. 获取 DeductScoreConfig 和 DimensionWeights
	// 优先从 Pipeline inputs 的 prompt_context 获取（由上游阶段传递）
	deductCfg := engine.DefaultDeductScoreConfig()
	dimWeights := make(map[string]engine.DimensionWeight)
	if pc, ok := ctx.GetInput("prompt_context").(*engine.PromptContext); ok && pc != nil {
		if len(pc.DimensionWeights) > 0 {
			dimWeights = pc.DimensionWeights
		}
		// 只有当配置非零值时才覆盖，保留默认值作为 fallback
		if pc.DeductScoreConfig.Critical > 0 || pc.DeductScoreConfig.High > 0 {
			deductCfg = pc.DeductScoreConfig
		}
	}

	// 组装 PromptContext（用于 Builder 构建结构化 Prompt）
	promptCtx := &engine.PromptContext{
		SecretScanFindings:    agentData.SecretScan,
		SecurityAuditFindings: agentData.SecurityAudit,
		TestSuggestions:       agentData.TestSuggestions,
		ImpactFindings:        agentData.ImpactFindings,
		DependencyVulns:       depVulns,
		BatchReviewResults:    batchResults,
		DimensionWeights:      dimWeights,
		DeductScoreConfig:     deductCfg,
	}

	// 6. 构建结构化 Prompt
	userPrompt, responseFormat, err := engine.BuildArbitrationPrompt(promptCtx)
	if err != nil {
		return fmt.Errorf("构建裁决 Prompt 失败: %w", err)
	}

	// 6. 保存输入快照（prompt 截断长度支持环境变量配置，默认 256KB）
	maxPromptSnapLen := 262144 // 256KB
	if v := os.Getenv("REVIEW_ARBITRATION_MAX_PROMPT_SNAP_LEN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxPromptSnapLen = n
		}
	}
	promptSnap := userPrompt
	if len(promptSnap) > maxPromptSnapLen {
		promptSnap = promptSnap[:maxPromptSnapLen] + "\n\n[... Prompt truncated, total length: " + fmt.Sprintf("%d chars]", len(userPrompt))
	}
	inputSnapshot := map[string]interface{}{
		"prompt": promptSnap,
		"agent_findings": map[string]int{
			"secret_scan":        len(agentData.SecretScan),
			"security_audit":     len(agentData.SecurityAudit),
			"test_suggestion":    len(agentData.TestSuggestions),
			"impact_analysis":    len(agentData.ImpactFindings),
			"dependency_scan":    len(depVulns),
			"code_understanding": 0,
		},
		"batch_results_count":  len(batchResults),
		"total_raw_llm_issues": countTotalIssues(batchResults),
	}
	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, inputSnapshot)

	// 7. 执行裁决（三层降级）
	result, rawLLMOutput, fallbackReason, err := e.arbitrate(ctx, promptCtx, userPrompt, responseFormat, deductCfg)
	if err != nil {
		return fmt.Errorf("评审裁决失败: %w", err)
	}

	// 8. 保存输出快照
	outputSnapshot := map[string]interface{}{
		"final_issues_count":      len(result.Issues),
		"security_findings_count": len(result.SecurityFindings),
		"testing_notes_count":     len(result.TestingNotes),
		"impact_notes_count":      len(result.ImpactNotes),
		"impact_notes":            result.ImpactNotes,
		"total_score":             result.TotalScore,
		"dimensions":              result.Dimensions,
		"dedup_log":               result.DedupLog,
		"dedup_merged_count":      countDedupAction(result.DedupLog, "merged"),
		"dedup_kept_agent_count":  countDedupAction(result.DedupLog, "kept_agent"),
		"dedup_kept_llm_count":    countDedupAction(result.DedupLog, "kept_llm"),
		"overall_suggestion":      result.OverallSuggestion,
		"raw_llm_output":          rawLLMOutput,
		"model_name":              getStrFromCtxOutput(ctx, "model_name"),
		"input_tokens":            getIntFromCtxOutput(ctx, "input_tokens"),
		"output_tokens":           getIntFromCtxOutput(ctx, "output_tokens"),
		"llm_fallback_reason":     fallbackReason,
	}
	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, outputSnapshot)

	// 9. 存储最终结果到 PipelineContext
	ctx.SetOutput("ai_review_result", result)
	ctx.SetOutput("score", result.TotalScore)

	return nil
}

// arbitrate 三层降级策略
func (e *ReviewArbitrationExecutor) arbitrate(
	ctx StageContext,
	promptCtx *engine.PromptContext,
	userPrompt string,
	responseFormat *llm.ResponseFormat,
	deductCfg engine.DeductScoreConfig,
) (result *llm.AIReviewResult, rawLLMOutput string, fallbackReason string, err error) {
	// Layer 3 最终兜底（recover panic）
	defer func() {
		if r := recover(); r != nil {
			zap.L().Error("review_arbitration panic", zap.Any("panic", r), zap.ByteString("stack", debug.Stack()))
			result = e.fallbackMerge(promptCtx, deductCfg)
			rawLLMOutput = ""
			fallbackReason = "兜底合并模式（裁决引擎 panic）"
			err = fmt.Errorf("裁决引擎 panic，已降级: %v", r)
		}
	}()

	// Layer 1: 尝试完整 LLM 汇总裁决（结构化 Prompt + JSON Schema 严格约束）
	result, rawLLMOutput, err = e.callLLMWithStructuredPrompt(ctx, promptCtx, userPrompt, responseFormat, deductCfg)
	if err == nil && isValidResult(result) {
		return result, rawLLMOutput, "", nil
	}

	fallbackReason = "本地去重模式（LLM汇总裁决失败/超时）"
	zap.L().Warn("review_arbitration: LLM 汇总失败/超时，降级到本地去重模式", zap.Error(err))

	// Layer 2: 本地去重 + 评分（不调用 LLM）
	result = e.localDeduplicationAndMerge(promptCtx, deductCfg)
	return result, "", fallbackReason, nil
}

// callLLMWithStructuredPrompt 调用 LLM 进行结构化裁决
// 返回 (解析后的结果, 原始 JSON 内容, error)
func (e *ReviewArbitrationExecutor) callLLMWithStructuredPrompt(
	ctx StageContext,
	promptCtx *engine.PromptContext,
	userPrompt string,
	responseFormat *llm.ResponseFormat,
	deductCfg engine.DeductScoreConfig,
) (*llm.AIReviewResult, string, error) {
	task := ctx.Task()

	// 如果 modelID 为 0，使用任务配置中的模型；若仍为 0 则走全局主备链路
	modelID := e.modelID
	if modelID == 0 && task.UsedModelID > 0 {
		modelID = task.UsedModelID
	}

	// 使用本阶段独立的 LLM 调用超时配置（优先读取 stage_configs，默认60秒）
	llmTimeoutSec := 60
	if cfg, ok := ctx.GetInput("_agent_config").(model.ReviewAgentConfig); ok {
		llmTimeoutSec = cfg.GetStageParam("review_arbitration", "timeout", llmTimeoutSec)
	}
	llmCtx := context.Context(ctx)
	if llmTimeoutSec > 0 {
		var cancel context.CancelFunc
		llmCtx, cancel = context.WithTimeout(llmCtx, time.Duration(llmTimeoutSec)*time.Second)
		defer cancel()
	}

	result, err := e.llmService.ChatCompletionStructured(
		llmCtx,
		&task.ID,
		modelID,
		"review_arbitration",
		"",
		userPrompt,
		responseFormat,
	)
	if err != nil {
		return nil, "", fmt.Errorf("LLM 裁决调用失败: %w", err)
	}

	// Refusal 检测
	if result.Response != nil && len(result.Response.Choices) > 0 && result.Response.Choices[0].Message.Refusal != "" {
		return nil, result.Content, fmt.Errorf("模型拒绝回答: %s", result.Response.Choices[0].Message.Refusal)
	}

	// 解析 + 后置校验（复用现有 ParseReviewResult）
	// 第二个参数传 nil 表示解析失败时不自动重试 LLM，因为 review_arbitration 有独立的
	// 三层降级（Layer 1 LLM → Layer 2 本地 → Layer 3 panic 兜底），避免无限重试循环
	parsedResult, err := engine.ParseReviewResult(result.Content, nil, getRetryConfig(), deductCfg)
	if err != nil {
		return nil, result.Content, fmt.Errorf("LLM 裁决结果解析失败: %w", err)
	}

	// 保存实际使用的模型 ID 到 Pipeline Context，供 PostProcess 和任务列表使用
	ctx.SetOutput("model_id", result.ModelID)
	// 同时保存模型名称和 token 用量，供 snapshot 展示
	ctx.SetOutput("model_name", result.ModelName)
	ctx.SetOutput("input_tokens", result.InputTokens)
	ctx.SetOutput("output_tokens", result.OutputTokens)

	// 【修复】将 token 同步到 selfExec，供 Pipeline 列表页展示
	if exec := ctx.GetSelfExec(); exec != nil {
		exec.InputTokens = result.InputTokens
		exec.OutputTokens = result.OutputTokens
		exec.ModelName = result.ModelName
		if exec.LLMModelID == nil {
			exec.LLMModelID = &result.ModelID
		}
	}

	// 【关键修复】LLM 偶发篡改维度权重（如全部置 0），导致总分异常为 0。
	// 使用 promptCtx 中的原始权重覆盖 LLM 输出，并重新计算维度得分与总分。
	if parsedResult != nil && len(promptCtx.DimensionWeights) > 0 {
		parsedResult.Dimensions, parsedResult.TotalScore = recalcDimensionsWithOriginalWeights(
			parsedResult.Issues, promptCtx.DimensionWeights, deductCfg,
		)
	}

	return parsedResult, result.Content, nil
}

// recalcDimensionsWithOriginalWeights 使用原始维度权重重新计算得分（防御 LLM 篡改）
func recalcDimensionsWithOriginalWeights(
	issues []llm.AIReviewIssue,
	dimWeights map[string]engine.DimensionWeight,
	cfg engine.DeductScoreConfig,
) (map[string]llm.Dimension, int) {
	dims := calculateDimensionsLocally(issues, dimWeights, cfg)
	score := calculateTotalScore(dims)

	// 记录覆盖行为，便于审计
	zap.L().Info("review_arbitration: dimensions recalculated with original weights",
		zap.Int("issue_count", len(issues)),
		zap.Int("total_score", score),
	)

	return dims, score
}

// localDeduplicationAndMerge 本地去重模式（不调用 LLM）
func (e *ReviewArbitrationExecutor) localDeduplicationAndMerge(
	promptCtx *engine.PromptContext,
	deductCfg engine.DeductScoreConfig,
) *llm.AIReviewResult {
	result := &llm.AIReviewResult{}

	// 1. 转换 Agent findings（仅 SecretScan + SecurityAudit）和 LLM issues 为 UnifiedFinding
	agentFindings := convertAgentFindings(
		promptCtx.SecretScanFindings,
		promptCtx.SecurityAuditFindings,
	)
	llmIssues := convertLLMIssues(promptCtx.BatchReviewResults)

	// 2. 执行去重合并
	mergeEngine := NewMergeEngine(deductCfg)
	mergedIssues, dedupLogs := mergeEngine.Execute(agentFindings, llmIssues)

	// 3. 构建独立数组（SecurityFinding 的 CodeSnippet 截断/脱敏，避免泄露密钥）
	var securityFindings []llm.SecurityFinding
	for _, f := range promptCtx.SecretScanFindings {
		// 截断 MatchText 防止泄露长密钥，最大保留 60 字符
		snippet := f.MatchText
		if len(snippet) > 60 {
			snippet = snippet[:30] + "..." + snippet[len(snippet)-20:]
		}
		securityFindings = append(securityFindings, llm.SecurityFinding{
			Source:      "secret_scan",
			Category:    "credential_leak",
			Severity:    f.Severity,
			Confidence:  0.95,
			File:        f.FilePath,
			LineStart:   f.LineNumber,
			LineEnd:     f.LineNumber,
			Title:       f.Description,
			Description: f.Description,
			CodeSnippet: snippet,
			AgentMeta: map[string]interface{}{
				"rule_code": f.RuleCode,
			},
		})
	}
	for _, f := range promptCtx.SecurityAuditFindings {
		securityFindings = append(securityFindings, llm.SecurityFinding{
			Source:      "security_audit",
			Category:    "security_audit",
			Severity:    f.Severity,
			Confidence:  1.0,
			File:        f.FilePath,
			LineStart:   f.LineNumber,
			LineEnd:     f.LineNumber,
			Title:       f.Message,
			Description: f.Message,
			Suggestion:  f.Suggestion,
		})
	}

	var testingNotes []llm.TestingNote
	for _, item := range promptCtx.TestSuggestions {
		testingNotes = append(testingNotes, llm.TestingNote{
			FunctionName: item.FunctionName,
			FilePath:     item.FilePath,
			LineNumber:   item.LineNumber,
			Scenarios:    item.Scenarios,
		})
	}

	var impactNotes []llm.ImpactNote
	for _, f := range promptCtx.ImpactFindings {
		// 截断过长的 suggestion，避免污染最终报告
		suggestion := f.Suggestion
		if len(suggestion) > 500 {
			suggestion = suggestion[:500] + "..."
		}
		// 截断过长的 migration_steps，避免前端展示爆炸
		migrationSteps := f.MigrationSteps
		if len(migrationSteps) > 500 {
			migrationSteps = migrationSteps[:500] + "..."
		}
		// 将启发式 Type 映射为前端期望的 Compatibility 枚举值
		// enrichment 阶段若成功执行，f.Type 已被覆盖为 LLM 返回值（breaking/behavioral/backward_compatible）
		compatibility := f.Type
		switch f.Type {
		case "breaking_change", "migration":
			compatibility = "breaking"
		case "config_change", "schema_change":
			compatibility = "behavioral"
		}
		impactNotes = append(impactNotes, llm.ImpactNote{
			Type:           f.Type,
			FilePath:       f.FilePath,
			SymbolName:     f.SymbolName,
			Description:    f.ChangeDesc,
			Severity:       f.Severity,
			Suggestion:     suggestion,
			AffectedFiles:  f.AffectedFiles,
			Compatibility:  compatibility,
			MigrationSteps: migrationSteps,
		})
	}

	// 4. 本地评分
	dimensions := calculateDimensionsLocally(mergedIssues, promptCtx.DimensionWeights, deductCfg)
	totalScore := calculateTotalScore(dimensions)

	// 5. 组装结果（确保所有 JSON Schema required 字段非 nil，避免 omitempty 导致 Strict Mode 拒绝）
	result.Issues = mergedIssues
	if result.Issues == nil {
		result.Issues = []llm.AIReviewIssue{}
	}
	result.SecurityFindings = securityFindings
	if result.SecurityFindings == nil {
		result.SecurityFindings = []llm.SecurityFinding{}
	}
	result.TestingNotes = testingNotes
	if result.TestingNotes == nil {
		result.TestingNotes = []llm.TestingNote{}
	}
	result.ImpactNotes = impactNotes
	if result.ImpactNotes == nil {
		result.ImpactNotes = []llm.ImpactNote{}
	}
	result.DedupLog = dedupLogs
	if result.DedupLog == nil {
		result.DedupLog = []llm.DedupEntry{}
	}
	result.Dimensions = dimensions
	result.TotalScore = totalScore
	result.SchemaVersion = "2.0"
	result.Summary = fmt.Sprintf("评审完成，共发现 %d 个问题（含 %d 个安全发现）",
		len(mergedIssues), len(securityFindings))
	result.Recommendations = []string{}

	return result
}

// fallbackMerge panic 降级：直接拼接，不处理重复
func (e *ReviewArbitrationExecutor) fallbackMerge(
	promptCtx *engine.PromptContext,
	deductCfg engine.DeductScoreConfig,
) *llm.AIReviewResult {
	if promptCtx == nil {
		zap.L().Error("fallbackMerge called with nil promptCtx")
		return &llm.AIReviewResult{
			SchemaVersion:    "2.0",
			Summary:          "评审引擎异常，promptCtx 为 nil",
			Issues:           []llm.AIReviewIssue{},
			Recommendations:  []string{},
			SecurityFindings: []llm.SecurityFinding{},
			TestingNotes:     []llm.TestingNote{},
			ImpactNotes:      []llm.ImpactNote{},
			DedupLog:         []llm.DedupEntry{},
		}
	}

	result := &llm.AIReviewResult{
		SchemaVersion:     "2.0",
		Summary:           "评审引擎异常，返回基础合并结果（可能存在重复）",
		Issues:            []llm.AIReviewIssue{},
		Recommendations:   []string{},
		SecurityFindings:  []llm.SecurityFinding{},
		TestingNotes:      []llm.TestingNote{},
		ImpactNotes:       []llm.ImpactNote{},
		DedupLog:          []llm.DedupEntry{},
		OverallSuggestion: "",
	}

	// Agent findings → AIReviewIssue
	for _, f := range promptCtx.SecretScanFindings {
		result.Issues = append(result.Issues, llm.AIReviewIssue{
			RuleCode:    f.RuleCode,
			Severity:    f.Severity,
			DeductScore: deductCfg.DeductScoreFor(f.Severity),
			Category:    "security",
			File:        f.FilePath,
			LineStart:   f.LineNumber,
			LineEnd:     f.LineNumber,
			Message:     f.Description,
			Source:      "agent",
		})
	}
	for _, f := range promptCtx.SecurityAuditFindings {
		result.Issues = append(result.Issues, llm.AIReviewIssue{
			RuleCode:    f.RuleCode,
			Severity:    f.Severity,
			DeductScore: deductCfg.DeductScoreFor(f.Severity),
			Category:    "security",
			File:        f.FilePath,
			LineStart:   f.LineNumber,
			LineEnd:     f.LineNumber,
			Message:     f.Message,
			Source:      "agent",
		})
	}

	// LLM issues → 直接追加
	for _, br := range promptCtx.BatchReviewResults {
		for _, issue := range br.Issues {
			result.Issues = append(result.Issues, issue)
		}
	}

	// 简单评分
	result.Dimensions = calculateDimensionsLocally(result.Issues, promptCtx.DimensionWeights, deductCfg)
	result.TotalScore = calculateTotalScore(result.Dimensions)

	return result
}

// isValidResult 校验结果是否有效（含维度权重和保护）
func isValidResult(result *llm.AIReviewResult) bool {
	if result == nil || result.TotalScore < 0 || result.TotalScore > 100 {
		return false
	}
	// 校验维度权重和是否为 100（防止 LLM 篡改权重导致评分失真）
	if len(result.Dimensions) >= 5 {
		totalWeight := 0
		for _, d := range result.Dimensions {
			totalWeight += d.Weight
		}
		if totalWeight != 100 {
			zap.L().Warn("review_arbitration: LLM output dimension weights invalid, trigger fallback",
				zap.Int("total_weight", totalWeight))
			return false
		}
	}
	return true
}

// getStrFromCtxOutput 安全地从 StageContext Output 读取字符串
func getStrFromCtxOutput(ctx StageContext, key string) string {
	if v, ok := ctx.GetOutput(key).(string); ok {
		return v
	}
	return ""
}

// getIntFromCtxOutput 安全地从 StageContext Output 读取整数
func getIntFromCtxOutput(ctx StageContext, key string) int {
	switch v := ctx.GetOutput(key).(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
