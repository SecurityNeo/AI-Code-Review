package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

// RetryConfig JSON 重试配置
type RetryConfig struct {
	MaxAttempts       int
	InitialDelay      time.Duration
	BackoffMultiplier float64
	MaxDelay          time.Duration
	FallbackStrategy  string // "regex" | "markdown" | "fail"
}

// ParseReviewResult 解析 LLM 响应，含重试和 fallback
func ParseReviewResult(
	content string,
	callLLM func() (string, error),
	cfg *RetryConfig,
	deductCfg DeductScoreConfig,
) (*llm.AIReviewResult, error) {
	// Step 1: 尝试直接解析
	result, err := tryParseJSON(content, deductCfg)
	if err == nil {
		return result, nil
	}
	zap.L().Warn("initial JSON parse failed", zap.Error(err))

	// Step 2: Sanitize 后重试解析
	cleaned := sanitizeJSON(content)
	result, err = tryParseJSON(cleaned, deductCfg)
	if err == nil {
		return result, nil
	}
	zap.L().Warn("sanitized JSON parse failed", zap.Error(err))

	// Step 3: 重试调用 LLM
	if cfg != nil && callLLM != nil {
		delay := cfg.InitialDelay
		for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
			zap.L().Info("retrying LLM call", zap.Int("attempt", attempt), zap.Duration("delay", delay))
			time.Sleep(delay)

			newContent, callErr := callLLM()
			if callErr != nil {
				zap.L().Warn("LLM retry call failed", zap.Int("attempt", attempt), zap.Error(callErr))
				delay = nextDelay(delay, cfg)
				continue
			}

			result, err = tryParseJSON(newContent, deductCfg)
			if err == nil {
				return result, nil
			}

			// sanitize 后再试
			result, err = tryParseJSON(sanitizeJSON(newContent), deductCfg)
			if err == nil {
				return result, nil
			}

			delay = nextDelay(delay, cfg)
		}
	}

	// Step 4: Fallback 策略
	if cfg == nil {
		cfg = &RetryConfig{FallbackStrategy: "fail"}
	}

	zap.L().Warn("all parse attempts failed, using fallback",
		zap.String("strategy", cfg.FallbackStrategy))

	switch cfg.FallbackStrategy {
	case "regex":
		return fallbackRegex(content), nil
	case "markdown":
		return fallbackMarkdown(content), nil
	case "fail":
		return nil, fmt.Errorf("JSON parse failed after all retries: %w", err)
	default:
		return fallbackRegex(content), nil
	}
}

// tryParseJSON 尝试解析 JSON（含结构修复兜底）
func tryParseJSON(content string, deductCfg DeductScoreConfig) (*llm.AIReviewResult, error) {
	var result llm.AIReviewResult
	// 第一次：标准解析
	if err := json.Unmarshal([]byte(content), &result); err == nil {
		return postParse(&result, deductCfg)
	}
	// 第二次：结构修复后解析（处理 dimensions 中混入标量值如 total_score）
	var raw map[string]interface{}
	if json.Unmarshal([]byte(content), &raw) == nil {
		if fixDimensionsNesting(raw) {
			fixedBytes, marshalErr := json.Marshal(raw)
			if marshalErr == nil {
				if err := json.Unmarshal(fixedBytes, &result); err == nil {
					zap.L().Info("fixed malformed JSON: removed scalar from dimensions, parse success",
						zap.Int("total_score", result.TotalScore))
					return postParse(&result, deductCfg)
				}
			}
		}
	}
	return nil, fmt.Errorf("json parse failed after all attempts")
}

// fixDimensionsNesting 修复 LLM 返回的 JSON 中 dimensions 对象内混入标量值的问题
// 例如：dimensions 内部包含 "total_score": 48 这样的键值对
func fixDimensionsNesting(raw map[string]interface{}) bool {
	dims, ok := raw["dimensions"].(map[string]interface{})
	if !ok {
		return false
	}
	fixed := false
	for k, v := range dims {
		if _, isObj := v.(map[string]interface{}); isObj {
			continue
		}
		// v 不是对象（map），说明是标量，将其移到顶层
		if _, exists := raw[k]; !exists {
			raw[k] = v
		}
		delete(dims, k)
		fixed = true
	}
	return fixed
}

// postParse 解析成功后的统一后处理
func postParse(result *llm.AIReviewResult, deductCfg DeductScoreConfig) (*llm.AIReviewResult, error) {
	// 基础校验
	if result.TotalScore < 0 || result.TotalScore > 100 {
		return nil, fmt.Errorf("invalid total_score: %d", result.TotalScore)
	}
	// 如果 issues 为 nil，初始化为空切片
	if result.Issues == nil {
		result.Issues = []llm.AIReviewIssue{}
	}
	if result.Recommendations == nil {
		result.Recommendations = []string{}
	}
	// 补充 deduct_score，未返回时按模版配置计算
	for i := range result.Issues {
		if result.Issues[i].DeductScore <= 0 {
			result.Issues[i].DeductScore = deductCfg.DeductScoreFor(result.Issues[i].Severity)
		}
	}
	// 非阻塞校验：权重之和
	if err := ValidateResult(result); err != nil {
		zap.L().Warn("review result validation warning", zap.Error(err))
	}

	// 后置校验：从 Issue 扣分重新计算总分
	result.OriginalTotalScore = result.TotalScore
	calculated := RecalculateTotalScore(result)
	if abs(result.TotalScore-calculated) > 5 {
		zap.L().Warn("LLM总分与计算值差异超过阈值，启用后置校验修正",
			zap.Int("llm_score", result.TotalScore),
			zap.Int("calculated_score", calculated),
			zap.Int("diff", abs(result.TotalScore-calculated)))
		result.TotalScore = calculated
	}

	// 【Phase 2】清洗 suggestion 字段中的模型自证性废话
	for i := range result.Issues {
		result.Issues[i].Suggestion = cleanSuggestion(result.Issues[i].Suggestion)
	}

	return result, nil
}

// cleanSuggestion 清洗 suggestion 字段中的模型自证性废话
// Phase 2 兜底策略：即使 model 在 suggestion 中填入评分计算、自证性文字，
// 也能在解析后被识别并清理。
func cleanSuggestion(suggestion string) string {
	if suggestion == "" {
		return ""
	}

	// 模型自证性关键词
	selfProofKeywords := []string{
		// 评分计算相关
		"deduct_score", "维度得分", "总分合计", "加权贡献",
		"max(0,100-", "/100", "经公式",
		// 自证/合规声明
		"与输入完全一致", "未做修改", "未混入", "未捏造", "不适用",
		"已标注", "已遵守", "未产生", "触发零次", "仲裁完成",
		"空问题处理", "独立数组", "顶层字段", "与 dimensions 同级",
		// 去重/来源说明
		"去重仲裁", "dedup_", "Agent 无发现",
	}

	// 策略1：匹配 ≥3 个关键词 → 判为模型废话，返回占位提示
	matchCount := 0
	for _, kw := range selfProofKeywords {
		if strings.Contains(suggestion, kw) {
			matchCount++
		}
	}
	if matchCount >= 3 {
		zap.L().Info("cleanSuggestion: 拦截模型自证废话",
			zap.Int("match_count", matchCount),
			zap.Int("original_len", len(suggestion)))
		// 尝试截取第一个实际句子作为建议
		firstSentence := extractFirstMeaningfulSentence(suggestion)
		if firstSentence != "" {
			return firstSentence + "（建议内容过长，已自动截断模型冗余信息）"
		}
		return "（建议内容包含模型内部推理信息，已自动过滤，请人工查看代码并给出修复建议）"
	}

	// 策略2：单句中出现 "total_score" 或 "max(0," → 截取前面的实际建议
	if strings.Contains(suggestion, "total_score") || strings.Contains(suggestion, "max(0,") {
		idx := strings.Index(suggestion, "total_score")
		if idx == -1 {
			idx = strings.Index(suggestion, "max(0,")
		}
		if idx > 20 {
			truncated := strings.TrimSpace(suggestion[:idx])
			truncated = strings.TrimRight(truncated, "，；,.;")
			if len(truncated) > 10 {
				return truncated
			}
		}
	}

	return suggestion
}

// extractFirstMeaningfulSentence 从 suggestion 中提取第一个有意义的句子
// 用于 model 废话命中时尽量保留前半部分的真实建议
func extractFirstMeaningfulSentence(s string) string {
	// 按句号、问号、感叹号分割，取第一个长度 > 10 且不含自证关键词的句子
	for _, sep := range []string{"。", "？", "！", ". ", "? ", "! "} {
		if idx := strings.Index(s, sep); idx > 10 {
			candidate := strings.TrimSpace(s[:idx])
			if !strings.Contains(candidate, "max(0,") &&
				!strings.Contains(candidate, "deduct_score") &&
				!strings.Contains(candidate, "维度得分") &&
				len(candidate) > 10 {
				return candidate
			}
		}
	}
	return ""
}

// ValidateResult 校验评审结果数据质量（非阻塞，仅记录警告）
func ValidateResult(result *llm.AIReviewResult) error {
	if len(result.Dimensions) == 0 {
		return fmt.Errorf("dimensions empty")
	}
	totalWeight := 0
	for _, d := range result.Dimensions {
		totalWeight += d.Weight
	}
	// 仅当存在 5 个标准维度时强制权重和为 100，自定义维度允许局部和
	if len(result.Dimensions) >= 5 && totalWeight != 100 {
		return fmt.Errorf("dimension weights sum not 100: got %d", totalWeight)
	}
	return nil
}

// sanitizeJSON 清洗可能的 JSON 包装
func sanitizeJSON(raw string) string {
	// 1. 去掉 BOM
	raw = strings.TrimPrefix(raw, "\ufeff")

	// 2. 去掉 markdown 代码块标记
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	// 3. 找第一个 { 和最后一个 }
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		raw = raw[start : end+1]
	}

	return strings.TrimSpace(raw)
}

// nextDelay 计算下一次延迟
func nextDelay(current time.Duration, cfg *RetryConfig) time.Duration {
	d := time.Duration(float64(current) * cfg.BackoffMultiplier)
	if d > cfg.MaxDelay {
		return cfg.MaxDelay
	}
	return d
}

// fallbackRegex 正则提取关键信息（兼容旧模式）
func fallbackRegex(content string) *llm.AIReviewResult {
	score := extractScoreFromText(content)
	return &llm.AIReviewResult{
		SchemaVersion:   "1.0",
		TotalScore:      score,
		Dimensions:      map[string]llm.Dimension{},
		Summary:         "AI评审返回非结构化数据，仅提取到总分。",
		Issues:          []llm.AIReviewIssue{},
		Recommendations: []string{},
	}
}

// fallbackMarkdown 保留原始文本但不提取分数
func fallbackMarkdown(content string) *llm.AIReviewResult {
	return &llm.AIReviewResult{
		SchemaVersion:   "1.0",
		TotalScore:      0,
		Dimensions:      map[string]llm.Dimension{},
		Summary:         "AI评审返回原始文本（结构化解析失败）。",
		Issues:          []llm.AIReviewIssue{},
		Recommendations: []string{},
	}
}

// extractScoreFromText 从文本中提取分数
var scoreRegex = regexp.MustCompile(`(?i)(?:总分|综合评分|score|AI评分|total_score)[:：\s"]*(\d+)`)

func extractScoreFromText(text string) int {
	matches := scoreRegex.FindStringSubmatch(text)
	if len(matches) > 1 {
		var score int
		fmt.Sscanf(matches[1], "%d", &score)
		return score
	}
	return 0
}

// ParseBatchReviewResult 解析分批评审收集模式的简化输出
// 不校验 total_score/dimensions，只提取 issues 和 recommendations
func ParseBatchReviewResult(content string, deductCfg DeductScoreConfig) (*llm.BatchReviewResult, error) {
	cleaned := sanitizeJSON(content)
	var result llm.BatchReviewResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		// 尝试从原始内容解析
		if err2 := json.Unmarshal([]byte(content), &result); err2 != nil {
			return nil, fmt.Errorf("batch review result parse failed: %w", err)
		}
	}
	if result.Issues == nil {
		result.Issues = []llm.AIReviewIssue{}
	}
	if result.Recommendations == nil {
		result.Recommendations = []string{}
	}
	// 补充 deduct_score
	for i := range result.Issues {
		if result.Issues[i].DeductScore <= 0 {
			result.Issues[i].DeductScore = deductCfg.DeductScoreFor(result.Issues[i].Severity)
		}
	}
	return &result, nil
}

// NormalizeDimensionWeights 根据已启用的规则补齐缺失的维度权重
// 如果规则分类对应的维度在 weights 中不存在，自动以 weight=0 补齐，并记录日志
func NormalizeDimensionWeights(weights map[string]DimensionWeight, rules []model.ReviewRule) map[string]DimensionWeight {
	if len(weights) == 0 {
		weights = DefaultDimensionWeights()
	}

	result := make(map[string]DimensionWeight, len(weights))
	for k, v := range weights {
		result[k] = v
	}

	// 从规则中收集所有分类维度
	dimSet := make(map[string]bool)
	for _, r := range rules {
		cat := r.Category
		if cat == "" || cat == "common" {
			continue
		}
		dimSet[cat] = true
	}

	// 补齐缺失维度
	warnings := []string{}
	for dim := range dimSet {
		if _, ok := result[dim]; !ok {
			result[dim] = DimensionWeight{Weight: 0, Label: categoryDisplay(dim)}
			warnings = append(warnings, dim)
		}
	}

	if len(warnings) > 0 {
		zap.L().Warn("模板维度权重缺少已启用规则的分类维度，已自动补齐 weight=0",
			zap.Strings("missing_dimensions", warnings))
	}

	return result
}

// BuildDimensionWeights 从规则列表和给定权重构建最终的维度权重 map
// 用于 runStructuredAIReview 中自动补齐
func BuildDimensionWeights(jsonStr string, rules []model.ReviewRule) map[string]DimensionWeight {
	weights, _ := ParseDimensionWeights(jsonStr)
	return NormalizeDimensionWeights(weights, rules)
}

// ParseDimensionWeights 解析模板维度权重 JSON
// 兼容两种格式：
//   1. 简单格式（前端当前使用）：{"security":30, "code_quality":25}
//   2. 对象格式（历史/内部）：{"security":{"weight":30,"label":"安全性"}}
func ParseDimensionWeights(jsonStr string) (map[string]DimensionWeight, error) {
	if jsonStr == "" || jsonStr == "{}" {
		return DefaultDimensionWeights(), nil
	}

	// 先尝试解析对象格式 {"key":{"weight":30,"label":"安全性"}}
	var objWeights map[string]DimensionWeight
	if err := json.Unmarshal([]byte(jsonStr), &objWeights); err == nil {
		for _, v := range objWeights {
			if v.Weight > 0 || v.Label != "" {
				return objWeights, nil
			}
		}
	}

	// 降级解析简单格式 {"key":30}
	var simpleWeights map[string]int
	if err := json.Unmarshal([]byte(jsonStr), &simpleWeights); err != nil {
		return DefaultDimensionWeights(), err
	}

	weights := make(map[string]DimensionWeight, len(simpleWeights))
	for k, v := range simpleWeights {
		weights[k] = DimensionWeight{
			Weight: v,
			Label:  categoryDisplay(k),
		}
	}
	return weights, nil
}

// DefaultDimensionWeights 返回默认维度权重
func DefaultDimensionWeights() map[string]DimensionWeight {
	return map[string]DimensionWeight{
		"security":        {Weight: 30, Label: "安全性"},
		"code_quality":    {Weight: 25, Label: "代码质量"},
		"readability":     {Weight: 20, Label: "可读性"},
		"maintainability": {Weight: 15, Label: "可维护性"},
		"test_coverage":   {Weight: 10, Label: "测试覆盖"},
	}
}

// defaultDeductScore 按 severity 返回默认扣分
func defaultDeductScore(severity string) int {
	// 等级越严重扣分越多，建议保持与 prompt instruction 一致
	switch severity {
	case "critical":
		return 10
	case "high":
		return 5
	case "medium":
		return 3
	case "low":
		return 1
	default:
		return 0
	}
}

// RecalculateTotalScore 从 Issue 扣分重新计算总分（加权平均）
// 公式: total_score = round( Σ(max(0, 100 - 维度扣分总和) × 维度权重) / Σ(维度权重) )
func RecalculateTotalScore(result *llm.AIReviewResult) int {
	// 按维度汇总扣分
	dimDeductions := make(map[string]int)
	for _, issue := range result.Issues {
		dimDeductions[issue.Category] += issue.DeductScore
	}

	// 加权计算
	var totalScore, totalWeight int
	for code, dim := range result.Dimensions {
		if dim.Weight <= 0 {
			continue
		}
		deducted := dimDeductions[code]
		score := max(0, 100-deducted)
		totalScore += score * dim.Weight
		totalWeight += dim.Weight
	}

	if totalWeight == 0 {
		return 100
	}
	return int(math.Round(float64(totalScore) / float64(totalWeight)))
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
