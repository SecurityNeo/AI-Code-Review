package pipeline

import (
	"unicode/utf8"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
)

// charsPerTokenApprox 字符/Token 估算比
type TokenEstimator struct {
	charsPerToken int
}

func NewTokenEstimator() *TokenEstimator {
	return &TokenEstimator{charsPerToken: 4}
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
}

// ToBreakdown 将 BatchContext 转换为 Token 构成明细（tokens）
func (ctx BatchContext) ToBreakdown(estimator *TokenEstimator) map[string]interface{} {
	systemTokens := ctx.SystemPromptChars / estimator.charsPerToken
	commitsTokens := ctx.CommitsChars / estimator.charsPerToken
	mrTitleTokens := ctx.MRTitleChars / estimator.charsPerToken
	batchHeaderTokens := ctx.BatchHeaderChars / estimator.charsPerToken
	outputFormatTokens := ctx.OutputFormatChars / estimator.charsPerToken
	treeTokens := ctx.TreeSitterChars / estimator.charsPerToken
	rulesTokens := ctx.RulesChars / estimator.charsPerToken
	customTokens := ctx.CustomInstChars / estimator.charsPerToken
	overhead := systemTokens + commitsTokens + mrTitleTokens + batchHeaderTokens + outputFormatTokens + treeTokens + rulesTokens + customTokens
	safety := overhead / 10
	totalOverhead := overhead + safety
	return map[string]interface{}{
		"system_instruction": systemTokens,
		"commits_info":       commitsTokens,
		"mr_title":           mrTitleTokens,
		"batch_header":       batchHeaderTokens,
		"output_format":      outputFormatTokens,
		"ast_context":        treeTokens,
		"rules_context":      rulesTokens,
		"custom_instruction": customTokens,
		"subtotal":           overhead,
		"safety_margin_10":   safety,
		"total_overhead":     totalOverhead,
	}
}

// EstimateOverheadTokens 估算固定开销 Token 数
func (e *TokenEstimator) EstimateOverheadTokens(ctx BatchContext) int {
	overhead := ctx.SystemPromptChars + ctx.CommitsChars + ctx.MRTitleChars +
		ctx.BatchHeaderChars + ctx.OutputFormatChars + ctx.TreeSitterChars + ctx.RulesChars + ctx.CustomInstChars
	// 10% 安全余量
	overhead += overhead / 10
	return overhead / e.charsPerToken
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
	MaxTokensPerBatch int           `json:"max_tokens_per_batch"`
	BatchCount        int           `json:"batch_count"`
	Strategy          string        `json:"strategy"` // "single" | "multi"
	Batches           []BatchDetail `json:"batches"`
	OverheadTokens    int           `json:"overhead_tokens"`
	AvailableTokens   int           `json:"available_tokens"`
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
		fileTokens := len(diff) / 4
		if fileTokens == 0 {
			fileTokens = 1
		}

		// 单文件截断处理：创建副本避免修改原始 map
		var fileObj interface{} = file
		if fileTokens > availableTokens {
			maxChars := availableTokens * 4
			if maxChars > 100 {
				maxChars -= 100 // 给截断标记留空间
			}
			if utf8.RuneCountInString(diff) > maxChars {
				fileCopy := make(map[string]interface{})
				for k, v := range file {
					fileCopy[k] = v
				}
				diff = string([]rune(diff)[:maxChars])
				diff += "\n\n[该文件 diff 较大，当前批次为控制 token 用量仅展示部分内容。完整变更请在代码库查看]"
				fileCopy["diff"] = diff
				fileCopy["truncated"] = true
				fileObj = fileCopy
				fileTokens = availableTokens
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
func BuildBatchContext(ctx StageContext) BatchContext {
	template := ""
	if v, ok := ctx.GetInput("project_template").(string); ok {
		template = v
	}
	// commits 和 MRTitle 在多批场景只在最后一批注入，不计入每批固定开销
	// （单批场景由 executeSingleBatchStructured 自行处理）
	if v, ok := ctx.GetInput("commits_text").(string); ok {
		_ = v
	}
	if t := ctx.Task(); t != nil {
		_ = t.MRTitle
	}
	// AST 文本和跨文件调用链在 batch_collection prompt 中不计入固定开销
	// （按批次过滤后的小片段已包含在 diff 可用额度中）
	_ = ""
	if v, ok := ctx.GetInput("ast_context").(string); ok {
		_ = v
	}
	_ = ""
	if v, ok := ctx.GetInput("cross_file_call_chain").(string); ok {
		_ = v
	}

	// 计算规则部分的字符数（从 prompt_context 中读取）
	rulesChars := 0
	customInstChars := 0
	if pc, ok := ctx.GetInput("prompt_context").(*engine.PromptContext); ok && pc != nil {
		// 估算规则文本长度（code + name + severity + prompt 的大致长度）
		for _, rule := range pc.Rules {
			rulesChars += len(rule.Code) + len(rule.Name) + len(rule.Severity) + len(rule.Prompt) + 50 // 格式开销
		}
		// 维度权重文本
		for code, dim := range pc.DimensionWeights {
			rulesChars += len(code) + len(dim.Label) + 30
		}
		// 扣分规则文本
		rulesChars += 300
		// 总分计算规则
		rulesChars += 400
		// 项目自定义说明
		customInstChars = len(pc.CustomInstruction)
	}

	return BatchContext{
		SystemPromptChars: len(template) + 500, // template + 固定指令
		// 【修复】batch_collection prompt 中不包含完整 AST 和跨文件调用链文本
		// 这些文本只在 context_extract 阶段内部使用，或通过 filterCrossFileContextForBatch
		// 按批次过滤后的小片段注入 prompt。使用完整文本估算会严重高估 overhead，
		// 导致 availableTokens 为负值，所有文件被无意义截断。
		TreeSitterChars:   0,
		CommitsChars:      0, // 多批场景只在最后一批注入
		MRTitleChars:      0, // 同上
		BatchHeaderChars:  100,
		OutputFormatChars: 1200, // 结构化输出 Schema 较大
		RulesChars:        rulesChars,
		CustomInstChars:   customInstChars,
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

// TruncateString 截断字符串用于预览（DB/对象存储展示用，不用于 LLM prompt）
func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + " ... [展示截断，完整内容请在代码库查看]"
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
