package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

// AgentVerificator 对 Agent 发现（SecretScan/SecurityAudit）进行 LLM 二次验证
// 注意：AgentVerificator 不是并发安全的，每次调用应新建实例或在外部串行使用。
type AgentVerificator struct {
	llmService       LLMService
	modelID          uint
	maxTokens        int
	verifyTimeout    time.Duration
	lastPrompt       string // 最近一次构建的 Prompt（用于详情展示）
	lastRawContent   string // 最近一次 LLM 原始 JSON 输出（用于详情展示）
	lastModelName    string // 最近一次 LLM 模型名称
	lastInputTokens  int    // 最近一次 LLM 输入 token
	lastOutputTokens int    // 最近一次 LLM 输出 token
}

// NewAgentVerificator 创建 AgentVerificator
// modelID=0 时使用 task.UsedModelID
type VerificatorConfig struct {
	ModelID       uint
	MaxTokens     int
	VerifyTimeout time.Duration
}

func NewAgentVerificator(svc LLMService, cfg VerificatorConfig) *AgentVerificator {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 2000
	}
	if cfg.VerifyTimeout <= 0 {
		cfg.VerifyTimeout = 300 * time.Second
	}
	return &AgentVerificator{
		llmService:    svc,
		modelID:       cfg.ModelID,
		maxTokens:     cfg.MaxTokens,
		verifyTimeout: cfg.VerifyTimeout,
	}
}

// LastPrompt 返回最近一次构建的 Prompt（用于详情展示）
func (v *AgentVerificator) LastPrompt() string {
	return v.lastPrompt
}

// LastRawContent 返回最近一次 LLM 原始 JSON 输出（用于详情展示）
func (v *AgentVerificator) LastRawContent() string {
	return v.lastRawContent
}

// LastModelName 返回最近一次 LLM 模型名称
func (v *AgentVerificator) LastModelName() string {
	return v.lastModelName
}

// LastInputTokens 返回最近一次 LLM 输入 token
func (v *AgentVerificator) LastInputTokens() int {
	return v.lastInputTokens
}

// LastOutputTokens 返回最近一次 LLM 输出 token
func (v *AgentVerificator) LastOutputTokens() int {
	return v.lastOutputTokens
}

// VerificationResult 单条验证结果
type VerificationResult struct {
	IsLeak             bool    `json:"is_leak"`
	LeakType           string  `json:"leak_type"`
	Severity           string  `json:"severity"`
	Confidence         float64 `json:"confidence"`
	Reason             string  `json:"reason"`
	CorrectedLineStart int     `json:"corrected_line_start,omitempty"`
	CorrectedLineEnd   int     `json:"corrected_line_end,omitempty"`
}

// VerifiedSecretFinding 验证后的密钥扫描发现
type VerifiedSecretFinding struct {
	Original   model.SecretScanFinding
	Result     VerificationResult
	MaskedText string
}

// VerifySecretScan 验证 SecretScan 候选发现
// 高置信度(>=0.9)强制进入 SecurityFinding，低置信度(<0.5)过滤
func (v *AgentVerificator) VerifySecretScan(
	ctx context.Context,
	taskID *uint,
	actualModelID uint,
	findings []model.SecretScanFinding,
	fileContents map[string]string,
) ([]VerifiedSecretFinding, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	// actualModelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）

	// 构建 Prompt：候选列表 + 代码上下文（±5 行）
	prompt := v.buildSecretVerifyPrompt(findings, fileContents)

	schema := getSecretVerifyJSONSchema()
	rf := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "secret_scan_verification",
			Strict: true,
			Schema: schema,
		},
	}

	result, err := v.callLLM(ctx, taskID, actualModelID, "secret_scan_verify", prompt, rf)
	if err != nil {
		zap.L().Warn("AgentVerificator LLM 调用失败，降级为规则引擎结果", zap.Error(err))
		return v.fallbackVerify(findings), nil
	}
	v.lastRawContent = result.Content
	v.lastModelName = result.ModelName
	v.lastInputTokens = result.InputTokens
	v.lastOutputTokens = result.OutputTokens

	// 解析验证结果
	var verdicts []VerificationResult
	if err := json.Unmarshal([]byte(result.Content), &verdicts); err != nil {
		zap.L().Warn("AgentVerificator JSON 解析失败，降级", zap.Error(err), zap.String("raw", result.Content))
		return v.fallbackVerify(findings), nil
	}

	// 对齐 verdicts 与 findings（假设顺序一致）
	var verified []VerifiedSecretFinding
	for i, f := range findings {
		var vr VerificationResult
		if i < len(verdicts) {
			vr = verdicts[i]
		} else {
			vr = VerificationResult{IsLeak: true, Confidence: 0.95, Severity: f.Severity}
		}
		mask := f.MatchText
		if len(mask) > 60 {
			mask = mask[:25] + "..." + mask[len(mask)-20:]
		}
		verified = append(verified, VerifiedSecretFinding{
			Original:   f,
			Result:     vr,
			MaskedText: mask,
		})
	}
	return verified, nil
}

func (v *AgentVerificator) fallbackVerify(findings []model.SecretScanFinding) []VerifiedSecretFinding {
	var res []VerifiedSecretFinding
	for _, f := range findings {
		mask := f.MatchText
		if len(mask) > 60 {
			mask = mask[:25] + "..." + mask[len(mask)-20:]
		}
		res = append(res, VerifiedSecretFinding{
			Original:   f,
			Result:     VerificationResult{IsLeak: true, LeakType: f.RuleCode, Severity: f.Severity, Confidence: 0.95, Reason: "规则引擎命中（LLM 验证未启用或失败）"},
			MaskedText: mask,
		})
	}
	return res
}

// buildSecretVerifyPrompt 构建 SecretScan 验证 Prompt
func (v *AgentVerificator) buildSecretVerifyPrompt(findings []model.SecretScanFinding, fileContents map[string]string) string {
	var sb strings.Builder
	sb.WriteString("你是一名安全专家。以下是通过正则引擎从代码 diff 中提取的密钥泄露候选位置。\n")
	sb.WriteString("请结合上下文判断每个候选是否真实泄露，给出最终判定。\n\n")

	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("候选 %d:\n", i+1))
		sb.WriteString(fmt.Sprintf("  文件: %s (第 %d 行)\n", f.FilePath, f.LineNumber))
		sb.WriteString(fmt.Sprintf("  规则: %s\n", f.RuleCode))
		mask := f.MatchText
		if len(mask) > 60 {
			mask = mask[:25] + "..." + mask[len(mask)-20:]
		}
		sb.WriteString(fmt.Sprintf("  匹配文本(脱敏): %s\n", mask))

		// 注入 ±5 行上下文
		if content, ok := fileContents[f.FilePath]; ok {
			lines := strings.Split(content, "\n")
			start := f.LineNumber - 6
			if start < 0 {
				start = 0
			}
			end := f.LineNumber + 4
			if end > len(lines) {
				end = len(lines)
			}
			sb.WriteString("  上下文:\n")
			for j := start; j < end && j < len(lines); j++ {
				prefix := "    "
				if j == f.LineNumber-1 {
					prefix = "  >>>"
				}
				sb.WriteString(fmt.Sprintf("%s %d: %s\n", prefix, j+1, strings.TrimRight(lines[j], "\r")))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("请对以上每个候选，按以下 JSON 数组格式输出判定结果：\n")
	sb.WriteString(`[
  {"is_leak": true, "leak_type": "AWS AK", "severity": "critical", "confidence": 0.97, "reason": "第15行硬编码了 AK/SK 对，且文件路径为 config/prod.go 生产环境配置"},
  ...
]`)
	return sb.String()
}

// VerifiedSecurityAuditFinding 验证后的安全审计发现
type VerifiedSecurityAuditFinding struct {
	Original model.SecurityAuditFinding
	Result   VerificationResult
}

// VerifySecurityAudit 验证 SecurityAudit 候选发现
func (v *AgentVerificator) VerifySecurityAudit(
	ctx context.Context,
	taskID *uint,
	actualModelID uint,
	findings []model.SecurityAuditFinding,
	fileContents map[string]string,
) ([]VerifiedSecurityAuditFinding, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	// actualModelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）

	prompt := v.buildSecurityAuditVerifyPrompt(findings, fileContents)
	schema := getSecurityAuditVerifyJSONSchema()
	rf := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "security_audit_verification",
			Strict: true,
			Schema: schema,
		},
	}

	result, err := v.callLLM(ctx, taskID, actualModelID, "security_audit_verify", prompt, rf)
	if err != nil {
		zap.L().Warn("AgentVerificator LLM 调用失败，降级为规则引擎结果", zap.Error(err))
		return v.fallbackVerifySecurityAudit(findings), nil
	}
	v.lastRawContent = result.Content
	v.lastModelName = result.ModelName
	v.lastInputTokens = result.InputTokens
	v.lastOutputTokens = result.OutputTokens

	var verdicts []VerificationResult
	if err := json.Unmarshal([]byte(result.Content), &verdicts); err != nil {
		zap.L().Warn("AgentVerificator JSON 解析失败，降级", zap.Error(err), zap.String("raw", result.Content))
		return v.fallbackVerifySecurityAudit(findings), nil
	}

	var verified []VerifiedSecurityAuditFinding
	for i, f := range findings {
		var vr VerificationResult
		if i < len(verdicts) {
			vr = verdicts[i]
		} else {
			vr = VerificationResult{IsLeak: true, Confidence: 0.95, Severity: f.Severity}
		}
		verified = append(verified, VerifiedSecurityAuditFinding{
			Original: f,
			Result:   vr,
		})
	}
	return verified, nil
}

func (v *AgentVerificator) fallbackVerifySecurityAudit(findings []model.SecurityAuditFinding) []VerifiedSecurityAuditFinding {
	var res []VerifiedSecurityAuditFinding
	for _, f := range findings {
		res = append(res, VerifiedSecurityAuditFinding{
			Original: f,
			Result:   VerificationResult{IsLeak: true, Severity: f.Severity, Confidence: 0.95, Reason: "规则引擎命中（LLM 验证未启用或失败）"},
		})
	}
	return res
}

// buildSecurityAuditVerifyPrompt 构建 SecurityAudit 验证 Prompt
func (v *AgentVerificator) buildSecurityAuditVerifyPrompt(findings []model.SecurityAuditFinding, fileContents map[string]string) string {
	var sb strings.Builder
	sb.WriteString("你是一名应用安全专家。以下是通过静态分析发现的潜在安全问题。\n")
	sb.WriteString("请结合代码语义判断是否存在真实风险，并给出深入的技术分析。\n\n")

	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("发现 %d:\n", i+1))
		sb.WriteString(fmt.Sprintf("  文件: %s (第 %d 行)\n", f.FilePath, f.LineNumber))
		sb.WriteString(fmt.Sprintf("  规则: %s\n", f.RuleCode))
		sb.WriteString(fmt.Sprintf("  描述: %s\n", f.Message))
		if content, ok := fileContents[f.FilePath]; ok {
			lines := strings.Split(content, "\n")
			start := f.LineNumber - 6
			if start < 0 {
				start = 0
			}
			end := f.LineNumber + 4
			if end > len(lines) {
				end = len(lines)
			}
			sb.WriteString("  上下文:\n")
			for j := start; j < end && j < len(lines); j++ {
				prefix := "    "
				if j == f.LineNumber-1 {
					prefix = "  >>>"
				}
				sb.WriteString(fmt.Sprintf("%s %d: %s\n", prefix, j+1, strings.TrimRight(lines[j], "\r")))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("请对以上每个发现，按 JSON 数组格式输出判定结果。\n")
	sb.WriteString(`示例: [{"is_leak": true, "leak_type": "sql_injection", "severity": "high", "confidence": 0.92, "reason": "用户输入直接拼接到 SQL 查询"}, ...]`)
	v.lastPrompt = sb.String()
	return sb.String()
}

// AgentEnricher 基于 Agent 发现生成 LLM 丰富内容（TestSuggestion + ImpactAnalysis）
// 注意：AgentEnricher 不是并发安全的，每次调用应新建实例或在外部串行使用。
type AgentEnricher struct {
	llmService       LLMService
	modelID          uint
	maxTokens        int
	enrichTimeout    time.Duration
	lastPrompt       string // 最近一次构建的 Prompt（用于详情展示）
	lastRawContent   string // 最近一次 LLM 原始 JSON 输出（用于详情展示）
	lastModelName    string // 最近一次 LLM 模型名称
	lastInputTokens  int    // 最近一次 LLM 输入 token
	lastOutputTokens int    // 最近一次 LLM 输出 token
}

// NewAgentEnricher 创建 AgentEnricher
type EnricherConfig struct {
	ModelID       uint
	MaxTokens     int
	EnrichTimeout time.Duration
}

func NewAgentEnricher(svc LLMService, cfg EnricherConfig) *AgentEnricher {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 3000
	}
	if cfg.EnrichTimeout <= 0 {
		cfg.EnrichTimeout = 300 * time.Second
	}
	return &AgentEnricher{
		llmService:    svc,
		modelID:       cfg.ModelID,
		maxTokens:     cfg.MaxTokens,
		enrichTimeout: cfg.EnrichTimeout,
	}
}

// LastPrompt 返回最近一次构建的 Prompt（用于详情展示）
func (e *AgentEnricher) LastPrompt() string {
	return e.lastPrompt
}

// LastRawContent 返回最近一次 LLM 原始 JSON 输出（用于详情展示）
func (e *AgentEnricher) LastRawContent() string {
	return e.lastRawContent
}

// LastModelName 返回最近一次 LLM 模型名称
func (e *AgentEnricher) LastModelName() string {
	return e.lastModelName
}

// LastInputTokens 返回最近一次 LLM 输入 token
func (e *AgentEnricher) LastInputTokens() int {
	return e.lastInputTokens
}

// LastOutputTokens 返回最近一次 LLM 输出 token
func (e *AgentEnricher) LastOutputTokens() int {
	return e.lastOutputTokens
}

// EnrichedTestScenario 丰富的测试场景
type EnrichedTestScenario struct {
	Name      string `json:"name"`
	Input     string `json:"input"`
	Expected  string `json:"expected"`
	Reasoning string `json:"reasoning"`
	Priority  string `json:"priority"`
}

// EnrichedImpactNote 丰富的影响分析
type EnrichedImpactNote struct {
	IsBreaking      bool   `json:"is_breaking"`
	BreakingType    string `json:"breaking_type"`
	Compatibility   string `json:"compatibility"`
	AffectedScope   string `json:"affected_scope"`
	MigrationNeeded bool   `json:"migration_needed"`
	MigrationSteps  string `json:"migration_steps"`
}

// EnrichTestSuggestions 为 TestSuggestion 生成结构化测试场景
func (e *AgentEnricher) EnrichTestSuggestions(
	ctx context.Context,
	taskID *uint,
	actualModelID uint,
	items []engine.TestSuggestionItem,
	fileContents map[string]string,
) (map[string][]EnrichedTestScenario, error) {
	if len(items) == 0 {
		return nil, nil
	}
	// actualModelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）

	prompt := e.buildTestEnrichPrompt(items, fileContents)
	schema := getTestEnrichJSONSchema()
	rf := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "test_suggestion_enrichment",
			Strict: true,
			Schema: schema,
		},
	}

	result, err := e.callLLM(ctx, taskID, actualModelID, "test_suggestion_enrich", prompt, rf)
	if err != nil {
		zap.L().Warn("AgentEnricher LLM 调用失败", zap.Error(err))
		return nil, nil
	}
	e.lastRawContent = result.Content
	e.lastModelName = result.ModelName
	e.lastInputTokens = result.InputTokens
	e.lastOutputTokens = result.OutputTokens

	var enriched map[string][]EnrichedTestScenario
	if err := json.Unmarshal([]byte(result.Content), &enriched); err != nil {
		zap.L().Warn("AgentEnricher JSON 解析失败", zap.Error(err), zap.String("raw", result.Content))
		return nil, nil
	}
	return enriched, nil
}

// EnrichImpactAnalysis 为 ImpactAnalysis 做语义判断
func (e *AgentEnricher) EnrichImpactAnalysis(
	ctx context.Context,
	taskID *uint,
	actualModelID uint,
	findings []engine.ImpactFinding,
	callChains map[string][]string,
) (map[string]EnrichedImpactNote, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	// actualModelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）

	prompt := e.buildImpactEnrichPrompt(findings, callChains)
	schema := getImpactEnrichJSONSchema()
	rf := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "impact_analysis_enrichment",
			Strict: true,
			Schema: schema,
		},
	}

	result, err := e.callLLM(ctx, taskID, actualModelID, "impact_analysis_enrich", prompt, rf)
	if err != nil {
		zap.L().Warn("AgentEnricher LLM 调用失败", zap.Error(err))
		return nil, nil
	}
	e.lastRawContent = result.Content
	e.lastModelName = result.ModelName
	e.lastInputTokens = result.InputTokens
	e.lastOutputTokens = result.OutputTokens

	var enriched map[string]EnrichedImpactNote
	if err := json.Unmarshal([]byte(result.Content), &enriched); err != nil {
		zap.L().Warn("AgentEnricher JSON 解析失败", zap.Error(err), zap.String("raw", result.Content))
		return nil, nil
	}
	return enriched, nil
}

// buildTestEnrichPrompt 构建测试建议丰富 Prompt
func (e *AgentEnricher) buildTestEnrichPrompt(items []engine.TestSuggestionItem, fileContents map[string]string) string {
	var sb strings.Builder
	sb.WriteString("你是一名资深测试架构师。以下是代码变更中涉及的函数及其 AST 摘要。\n")
	sb.WriteString("请基于业务语义和代码逻辑，给出具体、可执行的测试场景建议。\n\n")

	for i, item := range items {
		sb.WriteString(fmt.Sprintf("函数 %d: %s @ %s:%d\n", i+1, item.FunctionName, item.FilePath, item.LineNumber))
		if content, ok := fileContents[item.FilePath]; ok {
			lines := strings.Split(content, "\n")
			start := item.LineNumber - 3
			if start < 0 {
				start = 0
			}
			end := item.LineNumber + 10
			if end > len(lines) {
				end = len(lines)
			}
			for j := start; j < end && j < len(lines); j++ {
				sb.WriteString(fmt.Sprintf("  %d: %s\n", j+1, strings.TrimRight(lines[j], "\r")))
			}
		}
		if len(item.Scenarios) > 0 {
			sb.WriteString("  已有启发式场景:\n")
			for _, sc := range item.Scenarios {
				sb.WriteString(fmt.Sprintf("    - %s (priority: %s)\n", sc.Name, sc.Priority))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("请对每个函数输出结构化测试场景，以函数名为 key 的 JSON 对象格式返回。\n")
	sb.WriteString(`格式示例: {"FuncA": [{"name":"...","input":"...","expected":"...","reasoning":"...","priority":"must_have"}]}`)
	e.lastPrompt = sb.String()
	return sb.String()
}

// buildImpactEnrichPrompt 构建影响分析丰富 Prompt
func (e *AgentEnricher) buildImpactEnrichPrompt(findings []engine.ImpactFinding, callChains map[string][]string) string {
	var sb strings.Builder
	sb.WriteString("你是一名系统架构师。以下是导出函数/结构体变更的 diff 片段及跨文件调用链。\n")
	sb.WriteString("请判断此变更是否破坏 API 兼容性。\n\n")

	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("变更 %d: %s @ %s\n", i+1, f.SymbolName, f.FilePath))
		sb.WriteString(fmt.Sprintf("  类型: %s\n", f.Type))
		sb.WriteString(fmt.Sprintf("  描述: %s\n", f.ChangeDesc))
		if callers, ok := callChains[f.SymbolName]; ok && len(callers) > 0 {
			sb.WriteString(fmt.Sprintf("  下游调用者: %s\n", strings.Join(callers, ", ")))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("请以符号名为 key 的 JSON 对象格式返回判定结果。\n")
	sb.WriteString(`格式示例: {"GetUser": {"is_breaking":true, "breaking_type":"signature_change", "compatibility":"breaking", ...}}`)
	e.lastPrompt = sb.String()
	return sb.String()
}

// callLLM 统一 LLM 调用封装，带超时控制
func (v *AgentVerificator) callLLM(
	ctx context.Context,
	taskID *uint,
	modelID uint,
	caller string,
	prompt string,
	responseFormat *llm.ResponseFormat,
) (*StructuredChatResult, error) {
	c, cancel := context.WithTimeout(ctx, v.verifyTimeout)
	defer cancel()

	done := make(chan struct {
		r   *StructuredChatResult
		err error
	}, 1)

	go func() {
		r, err := v.llmService.ChatCompletionStructured(taskID, modelID, caller, "", prompt, responseFormat)
		done <- struct {
			r   *StructuredChatResult
			err error
		}{r, err}
	}()

	select {
	case res := <-done:
		return res.r, res.err
	case <-c.Done():
		if c.Err() == context.Canceled {
			return nil, fmt.Errorf("AgentVerificator LLM 调用被取消")
		}
		return nil, fmt.Errorf("AgentVerificator LLM 调用超时 (%v)", v.verifyTimeout)
	}
}

func (e *AgentEnricher) callLLM(
	ctx context.Context,
	taskID *uint,
	modelID uint,
	caller string,
	prompt string,
	responseFormat *llm.ResponseFormat,
) (*StructuredChatResult, error) {
	c, cancel := context.WithTimeout(ctx, e.enrichTimeout)
	defer cancel()

	done := make(chan struct {
		r   *StructuredChatResult
		err error
	}, 1)

	go func() {
		r, err := e.llmService.ChatCompletionStructured(taskID, modelID, caller, "", prompt, responseFormat)
		done <- struct {
			r   *StructuredChatResult
			err error
		}{r, err}
	}()

	select {
	case res := <-done:
		return res.r, res.err
	case <-c.Done():
		if c.Err() == context.Canceled {
			return nil, fmt.Errorf("AgentEnricher LLM 调用被取消")
		}
		return nil, fmt.Errorf("AgentEnricher LLM 调用超时 (%v)", e.enrichTimeout)
	}
}

// getSecretVerifyJSONSchema 返回 SecretScan 验证 JSON Schema
func getSecretVerifyJSONSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"is_leak":    map[string]interface{}{"type": "boolean"},
				"leak_type":  map[string]interface{}{"type": "string"},
				"severity":   map[string]interface{}{"type": "string", "enum": []string{"critical", "high", "medium", "low", "info"}},
				"confidence": map[string]interface{}{"type": "number"},
				"reason":     map[string]interface{}{"type": "string"},
			},
			"required": []string{"is_leak", "leak_type", "severity", "confidence", "reason"},
		},
	}
}

// getSecurityAuditVerifyJSONSchema 返回 SecurityAudit 验证 JSON Schema
func getSecurityAuditVerifyJSONSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"is_leak":    map[string]interface{}{"type": "boolean"},
				"leak_type":  map[string]interface{}{"type": "string"},
				"severity":   map[string]interface{}{"type": "string", "enum": []string{"critical", "high", "medium", "low", "info"}},
				"confidence": map[string]interface{}{"type": "number"},
				"reason":     map[string]interface{}{"type": "string"},
			},
			"required": []string{"is_leak", "leak_type", "severity", "confidence", "reason"},
		},
	}
}

// getTestEnrichJSONSchema 返回测试建议丰富 JSON Schema
func getTestEnrichJSONSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"additionalProperties": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":      map[string]interface{}{"type": "string"},
					"input":     map[string]interface{}{"type": "string"},
					"expected":  map[string]interface{}{"type": "string"},
					"reasoning": map[string]interface{}{"type": "string"},
					"priority":  map[string]interface{}{"type": "string", "enum": []string{"must_have", "should_have", "nice_to_have"}},
				},
				"required": []string{"name", "input", "expected", "reasoning", "priority"},
			},
		},
	}
}

// getImpactEnrichJSONSchema 返回影响分析丰富 JSON Schema
func getImpactEnrichJSONSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"additionalProperties": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"is_breaking":      map[string]interface{}{"type": "boolean"},
				"breaking_type":    map[string]interface{}{"type": "string", "enum": []string{"signature_change", "behavior_change", "removal", "schema_change"}},
				"compatibility":    map[string]interface{}{"type": "string", "enum": []string{"breaking", "behavioral", "backward_compatible"}},
				"affected_scope":   map[string]interface{}{"type": "string"},
				"migration_needed": map[string]interface{}{"type": "boolean"},
				"migration_steps":  map[string]interface{}{"type": "string"},
			},
			"required": []string{"is_breaking", "breaking_type", "compatibility", "affected_scope", "migration_needed"},
		},
	}
}
