package pipeline

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/ai-optimizer/backend/config"
	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/diff"
)

// TokenEstimator CJK-aware Token 估算器
type TokenEstimator struct {
	charsPerToken      int     // 向后兼容
	cjkCharsPerToken   float64 // CJK 字符每 token
	asciiCharsPerToken float64 // ASCII 字符每 token
}

func NewTokenEstimator() *TokenEstimator {
	return &TokenEstimator{
		charsPerToken:      4,   // 向后兼容
		cjkCharsPerToken:   2.0,
		asciiCharsPerToken: 4.0,
	}
}

// BatchContext 批次上下文开销（字符数）
type BatchContext struct {
	SystemPromptChars int
	CommitsChars      int
	MRTitleChars      int
	BatchHeaderChars  int
	OutputFormatChars int
	TreeSitterChars   int
	RulesChars        int // 评审规则部分字符数（模式A/C较大，模式B也较大）
	CustomInstChars   int // 项目自定义说明字符数
	RuleCount         int // 参与评审的规则数量（用于校准比率查询）
}

// ToBreakdown 将 BatchContext 转换为 Token 构成明细（tokens）
func (ctx BatchContext) ToBreakdown(estimator *TokenEstimator) map[string]interface{} {
	// 使用与 EstimateOverheadTokens 相同的 CJK-aware 计算逻辑
	// 【P0 修复】基于 Task 237 实际数据校准换算率
	systemTokens := int(math.Ceil(float64(ctx.SystemPromptChars) / 3.5))
	schemaTokens := ctx.OutputFormatChars / 4
	headerTokens := ctx.BatchHeaderChars / 4
	rulesTokens := int(math.Ceil(float64(ctx.RulesChars) / 5.0))
	customTokens := int(math.Ceil(float64(ctx.CustomInstChars) / 4.0))
	commitsTokens := int(math.Ceil(float64(ctx.CommitsChars) / 4.0))
	mrTitleTokens := int(math.Ceil(float64(ctx.MRTitleChars) / 4.0))
	astTokens := ctx.TreeSitterChars / 4

	overhead := systemTokens + schemaTokens + headerTokens +
		rulesTokens + customTokens + commitsTokens +
		mrTitleTokens + astTokens
	safety := int(math.Ceil(float64(overhead) / 10.0))
	totalOverhead := overhead + safety

	// 【新增】前台明细数组
	parts := []map[string]interface{}{
		{
			"key":            "system_prompt",
			"name":           "System Prompt",
			"chars":          ctx.SystemPromptChars,
			"tokens":         systemTokens,
			"rate":           "÷3.5",
			"note":           "混合中/英/JSON",
			"is_cjk_aware":   true,
		},
		{
			"key":            "output_format",
			"name":           "输出格式 Schema",
			"chars":          ctx.OutputFormatChars,
			"tokens":         schemaTokens,
			"rate":           "÷4",
			"note":           "纯 ASCII JSON",
			"is_cjk_aware":   false,
		},
		{
			"key":            "rules_context",
			"name":           "评审规则",
			"chars":          ctx.RulesChars,
			"tokens":         rulesTokens,
			"rate":           "÷5.0",
			"note":           "Markdown/ASCII 格式符占 57%",
			"is_cjk_aware":   true,
		},
		{
			"key":            "custom_instruction",
			"name":           "项目自定义说明",
			"chars":          ctx.CustomInstChars,
			"tokens":         customTokens,
			"rate":           "÷4.0",
			"note":           "中文+英文混合",
			"is_cjk_aware":   true,
		},
		{
			"key":            "commits_info",
			"name":           "Commit 历史",
			"chars":          ctx.CommitsChars,
			"tokens":         commitsTokens,
			"rate":           "÷4.0",
			"note":           "英文 commit msg 为主",
			"is_cjk_aware":   true,
		},
		{
			"key":            "mr_title",
			"name":           "MR 标题",
			"chars":          ctx.MRTitleChars,
			"tokens":         mrTitleTokens,
			"rate":           "÷4.0",
			"note":           "中文/英文标题",
			"is_cjk_aware":   true,
		},
		{
			"key":            "ast_context",
			"name":           "AST/代码理解",
			"chars":          ctx.TreeSitterChars,
			"tokens":         astTokens,
			"rate":           "÷4",
			"note":           "代码文本",
			"is_cjk_aware":   false,
		},
		{
			"key":            "batch_header",
			"name":           "批次头信息",
			"chars":          ctx.BatchHeaderChars,
			"tokens":         headerTokens,
			"rate":           "÷4",
			"note":           "固定 ASCII",
			"is_cjk_aware":   false,
		},
	}

	return map[string]interface{}{
		// 兼容旧字段
		"system_instruction": systemTokens,
		"commits_info":       commitsTokens,
		"mr_title":           mrTitleTokens,
		"batch_header":       headerTokens,
		"output_format":      schemaTokens,
		"ast_context":        astTokens,
		"rules_context":      rulesTokens,
		"custom_instruction": customTokens,
		"subtotal":           overhead,
		"safety_margin_10":   safety,
		"total_overhead":     totalOverhead,
		// 【新增】前台明细字段
		"parts":              parts,
		"system_prompt_chars": ctx.SystemPromptChars,
		"rules_chars":         ctx.RulesChars,
		"custom_chars":        ctx.CustomInstChars,
		"commits_chars":       ctx.CommitsChars,
		"mr_title_chars":      ctx.MRTitleChars,
		"ast_chars":           ctx.TreeSitterChars,
		"schema_chars":        ctx.OutputFormatChars,
		"header_chars":        ctx.BatchHeaderChars,
	}
}

// CalculateEffectiveBudget 计算有效预算
// 当配置预算 < 系统开销*1.25 时，自动扩大预算并返回警告说明
// 返回: (effectiveBudget 实际预算, warning 警告信息)
func CalculateEffectiveBudget(configuredBudget, estimatedOverhead int) (int, string) {
	// 最小需要 = 开销 + 20% diff空间
	minRequired := int(float64(estimatedOverhead) * 1.25)

	if configuredBudget >= minRequired {
		return configuredBudget, ""
	}

	// 预算不足，自动扩大
	return minRequired, fmt.Sprintf(
		"配置预算(%d)不足以覆盖系统固定开销(%d)，为保证评审质量已自动扩大至%d token，建议将「每批token上限」调整为≥%d",
		configuredBudget, estimatedOverhead, minRequired, minRequired)
}

// countCharTypes 统计字符串中的 CJK / ASCII / Other 字符数
func countCharTypes(s string) (cjk, ascii, other int) {
	for _, r := range s {
		switch {
		case r >= '\u4e00' && r <= '\u9fff': // CJK 统一表意文字
			cjk++
		case r >= '\u3040' && r <= '\u309f': // 平假名
			cjk++
		case r >= '\u30a0' && r <= '\u30ff': // 片假名
			cjk++
		case r >= '\uac00' && r <= '\ud7af': // 韩文
			cjk++
		case r < 128:
			ascii++
		default:
			other++
		}
	}
	return
}

// Estimate 估算字符串的 Token 数（CJK-aware 混合估算）
func (e *TokenEstimator) Estimate(s string) int {
	if s == "" {
		return 0
	}
	cjk, ascii, other := countCharTypes(s)

	// CJK: 保守按 2 字符/token
	// ASCII: 按 4 字符/token
	// Other（韩文、符号）：按 3 字符/token
	tokens := int(math.Ceil(float64(cjk)/e.cjkCharsPerToken)) +
		int(math.Ceil(float64(ascii)/e.asciiCharsPerToken)) +
		int(math.Ceil(float64(other)/3.0))

	// Markdown/JSON 格式开销 +5%
	tokens = int(math.Ceil(float64(tokens) * 1.05))

	if tokens == 0 {
		return 1
	}
	return tokens
}

// EstimateOverheadTokens 估算固定开销 Token 数（CJK-aware 分段估算）
func (e *TokenEstimator) EstimateOverheadTokens(ctx BatchContext) int {
	// 各字段按内容性质使用不同换算比（保守策略，向上取整）
	// 【P0 修复】基于 Task 237 实际数据校准：
	// 规则段落虽然 CJK 比例高(40%)，但 ASCII Markdown/格式符占比更高(57.6%)，
	// 实际 bytes/token ≈ 5.0（非 2.5）。同理适用于自定义说明、MR 标题等混合文本。
	systemTokens := int(math.Ceil(float64(ctx.SystemPromptChars) / 3.5))
	schemaTokens := ctx.OutputFormatChars / 4
	headerTokens := ctx.BatchHeaderChars / 4
	rulesTokens := int(math.Ceil(float64(ctx.RulesChars) / 5.0))
	customTokens := int(math.Ceil(float64(ctx.CustomInstChars) / 4.0))
	commitsTokens := int(math.Ceil(float64(ctx.CommitsChars) / 4.0))
	mrTitleTokens := int(math.Ceil(float64(ctx.MRTitleChars) / 4.0))
	astTokens := ctx.TreeSitterChars / 4

	overhead := systemTokens + schemaTokens + headerTokens +
		rulesTokens + customTokens + commitsTokens +
		mrTitleTokens + astTokens

	// 应用校准比率
	ratio := e.calibratedRatio(ctx.RuleCount)
	if ratio > 0 {
		overhead = int(math.Ceil(float64(overhead) * ratio))
	}

	// 10% 安全余量
	overhead = int(math.Ceil(float64(overhead) * 1.1))
	return overhead
}

// calibratedRatio 从最近历史记录计算校准比率
// 返回 0 表示校准数据不足，应使用原始估算
func (e *TokenEstimator) calibratedRatio(ruleCount int) float64 {
	if model.DB == nil {
		return 0
	}
	var cals []model.OverheadCalibration
	// 【修复】只使用修复后算法（version >= 2）的历史数据进行校准，避免旧脏数据污染
	model.DB.Where("rule_count BETWEEN ? AND ? AND algorithm_version >= 2", ruleCount-5, ruleCount+5).
		Order("created_at DESC").Limit(20).Find(&cals)

	if len(cals) < 5 {
		return 0 // 校准数据不足
	}

	var totalRatio float64
	validCount := 0
	for _, c := range cals {
		if c.EstimatedOverhead > 0 && c.ActualOverhead > 0 {
			totalRatio += float64(c.ActualOverhead) / float64(c.EstimatedOverhead)
			validCount++
		}
	}
	if validCount < 5 {
		return 0
	}
	return totalRatio / float64(validCount)
}

// Batch 分批单元
type Batch struct {
	Index       int
	Files       []interface{} // 存储 DiffFile 或简化后的文件对象
	TokenEst    int
	IsTruncated bool
	FilePaths   []string
}

// BatchPlan 分批计划
type BatchPlan struct {
	TotalFiles        int           `json:"total_files"`
	TotalTokens       int           `json:"total_tokens"`
	MaxTokensPerBatch int           `json:"max_tokens_per_batch"` // 用户原始配置值
	EffectiveBudget   int           `json:"effective_budget"`     // 自动扩大后的实际预算
	EstimatedOverhead int           `json:"estimated_overhead"`   // 估算系统开销
	BatchCount        int           `json:"batch_count"`
	Strategy          string        `json:"strategy"` // "single" | "multi"
	Batches           []BatchDetail `json:"batches"`
	OverheadTokens    int           `json:"overhead_tokens"`
	AvailableTokens   int           `json:"available_tokens"`
	BudgetWarning     string        `json:"budget_warning"` // 预算不足警告
}

// BatchDetail 批次明细
type BatchDetail struct {
	Index       int      `json:"index"`
	FileCount   int      `json:"file_count"`
	TokenEst    int      `json:"token_est"`
	FilePaths   []string `json:"file_paths"`
	IsTruncated bool     `json:"is_truncated"`
}

// SmartSplitIntoBatches 智能分批：考虑完整 Prompt 上下文
func SmartSplitIntoBatches(
	files []map[string]interface{}, // 简化文件结构：{path, diff}
	maxTokensPerBatch int,
	overhead BatchContext,
) []Batch {
	estimator := NewTokenEstimator()
	availableTokens := maxTokensPerBatch - estimator.EstimateOverheadTokens(overhead)

	// 【P1 修复】若启用了 [new|old] 行号前缀注入，需预留额外 token 余量
	// 每行前缀约 20-35 字符，按 4 chars/token 折算约 5-9 tokens/行
	// 为简化，统一预留 10% 可用 token 作为余量
	if cfg := config.Load().DiffLineMap; cfg.Enabled && cfg.InjectLineNumbers {
		availableTokens = int(float64(availableTokens) * 0.9)
	}
	if availableTokens <= 0 {
		// 极端情况：上下文本身就超过限制，但仍尝试给 diff 留一半配额
		availableTokens = maxTokensPerBatch / 2
	}

	var batches []Batch
	var currentBatch Batch
	currentTokens := 0

	for _, file := range files {
		diff, _ := file["diff"].(string)
		path, _ := file["path"].(string)
		// 【修复】使用 CJK-aware 估算替代简单的 len/4
		fileTokens := estimator.Estimate(diff)
		if fileTokens == 0 {
			fileTokens = 1
		}

		// 单文件截断处理：优先使用 HunkTruncator 按 diff 结构截断
		var fileObj interface{} = file
		if fileTokens > availableTokens {
			fileCopy, truncatedDiff := truncateDiffWithHunkBoundary(file, diff, availableTokens)
			if truncatedDiff != "" {
				fileObj = fileCopy
				diff = truncatedDiff
				fileTokens = estimator.Estimate(diff)
			}
		}

		if currentTokens+fileTokens > availableTokens && len(currentBatch.Files) > 0 {
			currentBatch.TokenEst = currentTokens
			batches = append(batches, currentBatch)
			currentBatch = Batch{
				Index: len(batches) + 1,
				Files: []interface{}{fileObj},
			}
			currentTokens = fileTokens
		} else {
			currentBatch.Files = append(currentBatch.Files, fileObj)
			currentTokens += fileTokens
		}
		currentBatch.FilePaths = append(currentBatch.FilePaths, path)
	}

	if len(currentBatch.Files) > 0 {
		currentBatch.Index = len(batches) + 1
		currentBatch.TokenEst = currentTokens
		batches = append(batches, currentBatch)
	}

	return batches
}

// GroupBatchesIntoWaves 将 N 个批次分为若干 wave（波次）
// 示例：7 批，每组 3 个 → Wave1: [0,1,2], Wave2: [3,4,5], Wave3: [6]
func GroupBatchesIntoWaves(totalBatches, maxParallel int) [][]int {
	if maxParallel <= 0 {
		maxParallel = 3 // 默认值
	}

	var waves [][]int
	for i := 0; i < totalBatches; i += maxParallel {
		end := i + maxParallel
		if end > totalBatches {
			end = totalBatches
		}
		wave := make([]int, end-i)
		for j := i; j < end; j++ {
			wave[j-i] = j
		}
		waves = append(waves, wave)
	}
	return waves
}

// BuildBatchContext 从 StageContext 构建 BatchContext
// truncateDiffWithHunkBoundary 按 hunk 边界安全截断 diff
// 返回 (fileCopy, truncatedDiff)
// 如果不需要截断或截断失败，返回 nil 和 ""
func truncateDiffWithHunkBoundary(file map[string]interface{}, diffText string, maxTokens int) (map[string]interface{}, string) {
	cfg := config.Load().DiffLineMap
	if !cfg.Enabled {
		// 未启用 diff line map 时回退到字符级硬切
		return truncateDiffChars(file, diffText, maxTokens)
	}

	parser := diff.NewParser()
	parsedFiles, err := parser.Parse(diffText)
	if err != nil || len(parsedFiles) == 0 {
		// 解析失败回退到字符级硬切
		return truncateDiffChars(file, diffText, maxTokens)
	}

	// 单文件 diff 只应该解析出一个 ParsedDiffFile
	pf := parsedFiles[0]

	// 估算完整 diff 的 token 数
	estimator := NewTokenEstimator()
	fullTokens := estimator.Estimate(diffText)
	if fullTokens <= maxTokens {
		return nil, ""
	}

	// 计算截断后 hunk 的目标 token 数（留 10% 余量给截断标记）
	targetTokens := int(float64(maxTokens) * 0.9)
	// 逐步收紧 context window（3→1），直到满足 token 限制
	// 注意：window 最小为 1，因为 SubtreeTruncator 中 window<=0 会被 fallback 到 3，window=0 迭代将无效
	var truncated string
	for window := 3; window >= 1; window-- {
		tr := diff.NewSubtreeTruncator(diff.SubtreeConfig{ContextWindow: window})
		truncatedLines := make([]diff.DiffLine, 0)
		for _, hunk := range pf.Hunks {
			lines := tr.Truncate(hunk)
			truncatedLines = append(truncatedLines, lines...)
		}
		// 重建截断后的 diff
		var parts []string
		parts = append(parts, fmt.Sprintf("--- %s", pf.OldPath))
		parts = append(parts, fmt.Sprintf("+++ %s", pf.NewPath))
		for _, line := range truncatedLines {
			parts = append(parts, line.Raw)
		}
		truncated = strings.Join(parts, "\n")
		if estimator.Estimate(truncated) <= targetTokens {
			break
		}
	}

	// 截断后仍然超限，使用 MiddleTruncator
	if estimator.Estimate(truncated) > targetTokens {
		middleTr := diff.NewMiddleTruncator(diff.MiddleConfig{
			MaxLinesPerHunk:    100,
			ContextLinesAround: 3,
		})
		truncated = diff.BuildTruncatedDiff(pf, middleTr)
	}

	// 注入截断标记
	truncated += "\n\n[该文件 diff 较大，当前批次为控制 token 用量仅展示部分内容。完整变更请在代码库查看]"

	fileCopy := make(map[string]interface{})
	for k, v := range file {
		fileCopy[k] = v
	}
	fileCopy["diff"] = truncated
	fileCopy["truncated"] = true
	return fileCopy, truncated
}

// truncateDiffChars 字符级硬切（回退策略）
func truncateDiffChars(file map[string]interface{}, diffText string, maxTokens int) (map[string]interface{}, string) {
	maxChars := maxTokens * 4
	if maxChars > 100 {
		maxChars -= 100
	}
	if utf8.RuneCountInString(diffText) <= maxChars {
		return nil, ""
	}

	truncated := string([]rune(diffText)[:maxChars])
	truncated += "\n\n[该文件 diff 较大，当前批次为控制 token 用量仅展示部分内容。完整变更请在代码库查看]"

	fileCopy := make(map[string]interface{})
	for k, v := range file {
		fileCopy[k] = v
	}
	fileCopy["diff"] = truncated
	fileCopy["truncated"] = true
	return fileCopy, truncated
}

func BuildBatchContext(ctx StageContext) BatchContext {
	// ===== System Prompt 精确测量 =====
	// BatchCollection 阶段首次估算时 totalBatches 未知，使用占位符 1/1
	// 实际差异："第 1/1 批"(14字节) vs "第 1/100 批"(18字节)，偏差仅 4 字节
	systemPromptChars := engine.MeasureBatchCollectionSystemPrompt(1, 1)

	// ===== 从 StageContext 读取实际内容长度 =====
	// Commits 和 MRTitle 作为保守策略始终计入 overhead
	//（虽然只在最后一批注入，但这样可避免最后一批因突增内容而截断）
	var commitsChars, mrTitleChars int

	if v, ok := ctx.GetInput("commits_text").(string); ok && v != "" {
		commitsChars = len(v)
	}

	if t := ctx.Task(); t != nil && t.MRTitle != "" {
		mrTitleChars = len(t.MRTitle)
	}

	// ===== 规则部分：从粗糙估算改为精确测量 =====
	rulesChars := 0
	customInstChars := 0
	ruleCount := 0
	if pc, ok := ctx.GetInput("prompt_context").(*engine.PromptContext); ok && pc != nil {
		ruleCount = len(pc.Rules)
		// 【修复】直接使用实际构建函数的精确测量，替代 len(Code)+len(Name)+...+50 的粗糙估算
		// 确保预算估算值与最终注入 LLM 的 Prompt 中规则段落长度严格一致
		rulesChars = engine.MeasureRulesSectionLiteChars(pc.Rules, pc.DimensionWeights)
		customInstChars = len(pc.CustomInstruction)
	}

	return BatchContext{
		SystemPromptChars: systemPromptChars,  // 【修复】实测值，非估算
		CommitsChars:      commitsChars,       // 【修复】保守计入（只最后一批注入）
		MRTitleChars:      mrTitleChars,       // 【修复】保守计入（只最后一批注入）
		// 【注意】AST/代码理解内容按批次动态过滤，每批片段大小差异大
		// 使用完整报告长度会严重高估 overhead，故保持为 0
		TreeSitterChars:   0,
		BatchHeaderChars:  100,
		OutputFormatChars: 1200,
		RulesChars:        rulesChars,
		CustomInstChars:   customInstChars,
		RuleCount:         ruleCount,
	}
}

// ExtractFilePaths 从文件列表提取路径
func ExtractFilePaths(files []interface{}) []string {
	var paths []string
	for _, f := range files {
		if m, ok := f.(map[string]interface{}); ok {
			if p, ok := m["path"].(string); ok {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

// TruncateString 截断字符串用于预览（DB/对象存储展示用，不用于 LLM prompt）。
// 按 rune 数量安全截断，避免切在多字节 UTF-8 字符中间。
func TruncateString(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	return truncateRunes(s, maxLen) + " ... [展示截断，完整内容请在代码库查看]"
}

// ParseDiffFilesToMap 将 service 层的 diff files 转换为 map 列表（简化接口）
func ParseDiffFilesToMap(diffFiles interface{}) []map[string]interface{} {
	var result []map[string]interface{}
	if files, ok := diffFiles.([]map[string]interface{}); ok {
		return files
	}
	// 如果已经是原始类型（如 gitlab.DiffFile slice），尝试通过反射或断言处理
	// 这里提供一个通用转换，实际使用时应由调用方处理
	return result
}

// SysCfgMaxTokensPerBatch 返回当前生效的每批最大 token 数（从 ReviewAgentConfig batch_review_frame 阶段配置读取）
func SysCfgMaxTokensPerBatch() int {
	var agentCfg model.ReviewAgentConfig
	if err := model.DB.First(&agentCfg, 1).Error; err == nil {
		if sc := agentCfg.StageConfig("batch_review_frame"); sc != nil {
			if v, ok := sc["max_tokens_per_batch"].(float64); ok && v > 0 {
				return int(v)
			}
		}
	}
	return 100000
}

// SysCfgCallChainDepth 从 SystemConfig 读取调用链分析深度
func SysCfgCallChainDepth() int {
	var cfg model.SystemConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		return 1
	}
	if cfg.CallChainDepth > 0 {
		return cfg.CallChainDepth
	}
	return 1
}
