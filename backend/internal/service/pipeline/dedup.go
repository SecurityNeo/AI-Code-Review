package pipeline

import (
	"fmt"
	"math"
	"strings"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
)

// ==================== 统一 Finding 视图（内部使用）====================

// UnifiedFinding Agent 和 LLM 发现的统一视图
type UnifiedFinding struct {
	ID          string
	Source      string // "agent" / "llm"
	AgentSource string // "secret_scan" / "security_audit" / ""
	RuleCode    string
	Severity    string
	Category    string
	File        string
	LineStart   int
	LineEnd     int
	Message     string
	Suggestion  string
	CodeSnippet string
	AgentMeta   map[string]interface{}
}

// AgentFindingCollection 聚合所有 Agent 发现
type AgentFindingCollection struct {
	SecretScan      []model.SecretScanFinding
	SecurityAudit   []model.SecurityAuditFinding
	TestSuggestions []engine.TestSuggestionItem
	ImpactFindings  []engine.ImpactFinding
}

// ==================== MergeEngine ====================

// MergeEngine 去重合并引擎
type MergeEngine struct {
	lineThreshold       int
	similarityThreshold float64
	cfg                 engine.DeductScoreConfig
}

// NewMergeEngine 创建 MergeEngine 实例
func NewMergeEngine(cfg engine.DeductScoreConfig) *MergeEngine {
	return &MergeEngine{
		lineThreshold:       3,
		similarityThreshold: 0.70,
		cfg:                 cfg,
	}
}

// MergeResult 合并结果
type MergeResult struct {
	Issue    llm.AIReviewIssue
	DedupLog llm.DedupEntry
	IsMerged bool
}

// Execute 执行去重合并
func (e *MergeEngine) Execute(
	agentFindings []UnifiedFinding,
	llmIssues []UnifiedFinding,
) ([]llm.AIReviewIssue, []llm.DedupEntry) {
	var finalIssues []llm.AIReviewIssue
	var dedupLogs []llm.DedupEntry

	// 步骤 1：Agent findings 作为基准加入，并建立 ID → index 映射
	agentIndexMap := make(map[string]int, len(agentFindings)) // UnifiedFinding.ID → finalIssues index
	for _, af := range agentFindings {
		agentIndexMap[af.ID] = len(finalIssues)
		issue := e.agentToAIReviewIssue(af)
		finalIssues = append(finalIssues, issue)
	}

	// 步骤 2：处理 LLM issues，逐一检查是否与 Agent 重复
	for _, li := range llmIssues {
		overlappingAgent := e.findOverlappingAgent(li, agentFindings)

		if overlappingAgent == nil {
			// 无重叠 → 直接保留
			finalIssues = append(finalIssues, e.llmToAIReviewIssue(li))
			continue
		}

		// 有重叠 → 语义相似度判定
		if isSemanticallySimilar(li, *overlappingAgent, e.similarityThreshold) {
			// 判定为同一问题 → 合并
			merged := e.mergeIssue(*overlappingAgent, li)

			// 通过索引映射精确更新对应的 Agent issue
			if idx, ok := agentIndexMap[overlappingAgent.ID]; ok {
				finalIssues[idx] = merged.Issue
			}

			dedupLogs = append(dedupLogs, merged.DedupLog)
		} else {
			// 位置重叠但语义不同 → 保留两条
			finalIssues = append(finalIssues, e.llmToAIReviewIssue(li))
			dedupLogs = append(dedupLogs, llm.DedupEntry{
				Action:    "kept_llm",
				AgentFile: overlappingAgent.File,
				AgentLine: overlappingAgent.LineStart,
				AgentMsg:  overlappingAgent.Message,
				LLMFile:   li.File,
				LLMLine:   li.LineStart,
				LLMMsg:    li.Message,
				Reason:    fmt.Sprintf("位置重叠但语义不相似，判定为不同问题"),
			})
		}
	}

	return finalIssues, dedupLogs
}

func (e *MergeEngine) findOverlappingAgent(
	llmIssue UnifiedFinding,
	agentFindings []UnifiedFinding,
) *UnifiedFinding {
	for _, af := range agentFindings {
		if af.File != llmIssue.File {
			continue
		}
		// 行号重叠判定
		if abs(af.LineStart-llmIssue.LineStart) <= e.lineThreshold {
			return &af
		}
		// LLM 的范围覆盖了 Agent 的行号
		if llmIssue.LineStart <= af.LineStart && llmIssue.LineEnd >= af.LineStart {
			return &af
		}
	}
	return nil
}

func (e *MergeEngine) mergeIssue(agent, llmFinding UnifiedFinding) MergeResult {
	// Message：取更长/更详细的
	message := agent.Message
	if len(llmFinding.Message) > len(agent.Message) {
		message = llmFinding.Message
	}
	if agent.RuleCode != "" && !strings.HasPrefix(message, "["+agent.RuleCode+"]") {
		message = "[" + agent.RuleCode + "] " + message
	}

	// Suggestion：合并或取更详细的
	suggestion := mergeSuggestions(agent.Suggestion, llmFinding.Suggestion)

	// CodeSnippet：优先 Agent，否则 LLM
	codeSnippet := agent.CodeSnippet
	if codeSnippet == "" {
		codeSnippet = llmFinding.CodeSnippet
	}

	// DeductScore：从配置获取
	deductScore := e.cfg.DeductScoreFor(agent.Severity)

	issue := llm.AIReviewIssue{
		RuleCode:    agent.RuleCode,
		Severity:    agent.Severity,
		DeductScore: deductScore,
		Category:    agent.Category,
		File:        agent.File,
		LineStart:   agent.LineStart,
		LineEnd:     agent.LineEnd,
		CodeSnippet: codeSnippet,
		Message:     message,
		Suggestion:  suggestion,
		Source:      "merged",
		AgentMeta:   agent.AgentMeta,
	}

	dedupLog := llm.DedupEntry{
		Action:    "merged",
		AgentFile: agent.File,
		AgentLine: agent.LineStart,
		AgentMsg:  agent.Message,
		LLMFile:   llmFinding.File,
		LLMLine:   llmFinding.LineStart,
		LLMMsg:    llmFinding.Message,
		Reason: fmt.Sprintf("位置重叠(%s:%d vs %s:%d)且语义相似，保留 Agent severity=%s rule=%s，合并 LLM suggestion",
			agent.File, agent.LineStart, llmFinding.File, llmFinding.LineStart, agent.Severity, agent.RuleCode),
	}

	return MergeResult{Issue: issue, DedupLog: dedupLog, IsMerged: true}
}

func (e *MergeEngine) agentToAIReviewIssue(af UnifiedFinding) llm.AIReviewIssue {
	return llm.AIReviewIssue{
		RuleCode:    af.RuleCode,
		Severity:    af.Severity,
		DeductScore: e.cfg.DeductScoreFor(af.Severity),
		Category:    af.Category,
		File:        af.File,
		LineStart:   af.LineStart,
		LineEnd:     af.LineEnd,
		CodeSnippet: af.CodeSnippet,
		Message:     af.Message,
		Suggestion:  af.Suggestion,
		Source:      "agent",
		AgentMeta:   af.AgentMeta,
	}
}

func (e *MergeEngine) llmToAIReviewIssue(li UnifiedFinding) llm.AIReviewIssue {
	return llm.AIReviewIssue{
		RuleCode:    li.RuleCode,
		Severity:    li.Severity,
		DeductScore: e.cfg.DeductScoreFor(li.Severity),
		Category:    li.Category,
		File:        li.File,
		LineStart:   li.LineStart,
		LineEnd:     li.LineEnd,
		CodeSnippet: li.CodeSnippet,
		Message:     li.Message,
		Suggestion:  li.Suggestion,
		Source:      "llm",
	}
}

// ==================== 语义相似度算法 ====================

func isSemanticallySimilar(f1, f2 UnifiedFinding, threshold float64) bool {
	// 策略 1：category 相同 → 直接判定相似（快速路径）
	if f1.Category == f2.Category && f1.Category != "" {
		return true
	}

	// 策略 2：Jaccard 相似度
	sim := calculateJaccardSimilarity(f1.Message, f2.Message)
	if sim >= threshold {
		return true
	}

	return false
}

func calculateJaccardSimilarity(msg1, msg2 string) float64 {
	words1 := tokenize(msg1)
	words2 := tokenize(msg2)

	stopwords := map[string]bool{"的": true, "了": true, "在": true, "is": true, "the": true, "a": true, "to": true, "of": true}
	set1 := make(map[string]bool)
	set2 := make(map[string]bool)

	for _, w := range words1 {
		if !stopwords[w] && len(w) > 1 {
			set1[w] = true
		}
	}
	for _, w := range words2 {
		if !stopwords[w] && len(w) > 1 {
			set2[w] = true
		}
	}

	intersection := 0
	for w := range set1 {
		if set2[w] {
			intersection++
		}
	}
	union := len(set1) + len(set2) - intersection

	if union == 0 {
		return 0.0
	}
	return float64(intersection) / float64(union)
}

func tokenize(text string) []string {
	// 简单分词：按非字母数字中文分割
	var words []string
	var current strings.Builder

	for _, r := range text {
		if isWordChar(r) {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				words = append(words, strings.ToLower(current.String()))
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		words = append(words, strings.ToLower(current.String()))
	}
	return words
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		(r >= '\u4e00' && r <= '\u9fff') // CJK
}

func mergeSuggestions(agentSugg, llmSugg string) string {
	if agentSugg != "" && llmSugg != "" {
		return "【自动扫描建议】\n" + agentSugg + "\n\n【AI 评审建议】\n" + llmSugg
	}
	if llmSugg != "" {
		return llmSugg
	}
	return agentSugg
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ==================== 辅助函数 ====================

func countDedupAction(logs []llm.DedupEntry, action string) int {
	count := 0
	for _, l := range logs {
		if l.Action == action {
			count++
		}
	}
	return count
}

func countTotalIssues(batchResults []*llm.BatchReviewResult) int {
	count := 0
	for _, br := range batchResults {
		count += len(br.Issues)
	}
	return count
}

// convertAgentFindings 将各 Agent 发现（密钥扫描 + 安全审计）转换为 UnifiedFinding。
// 注意：TestSuggestions 和 ImpactFindings 不经过此函数，它们作为独立数据流直接输出到
// TestingNotes[] 和 ImpactNotes[]，不参与 Issues[] 的去重合并。
func convertAgentFindings(
	secretScan []model.SecretScanFinding,
	securityAudit []model.SecurityAuditFinding,
) []UnifiedFinding {
	var unifieds []UnifiedFinding

	for _, f := range secretScan {
		unifieds = append(unifieds, UnifiedFinding{
			ID:          fmt.Sprintf("agent-secret-%s-%d", f.FilePath, f.LineNumber),
			Source:      "agent",
			AgentSource: "secret_scan",
			RuleCode:    f.RuleCode,
			Severity:    f.Severity,
			Category:    "security",
			File:        f.FilePath,
			LineStart:   f.LineNumber,
			LineEnd:     f.LineNumber,
			Message:     f.Description,
			AgentMeta: map[string]interface{}{
				"rule_code":  f.RuleCode,
				"match_text": f.MatchText,
			},
		})
	}

	for _, f := range securityAudit {
		unifieds = append(unifieds, UnifiedFinding{
			ID:          fmt.Sprintf("agent-audit-%s-%d", f.FilePath, f.LineNumber),
			Source:      "agent",
			AgentSource: "security_audit",
			RuleCode:    f.RuleCode,
			Severity:    f.Severity,
			Category:    "security",
			File:        f.FilePath,
			LineStart:   f.LineNumber,
			LineEnd:     f.LineNumber,
			Message:     f.Message,
			Suggestion:  f.Suggestion,
		})
	}

	return unifieds
}

// convertLLMIssues 将 BatchReviewResult 中的 issues 转换为 UnifiedFinding
func convertLLMIssues(batchResults []*llm.BatchReviewResult) []UnifiedFinding {
	var unifieds []UnifiedFinding
	for _, br := range batchResults {
		for _, issue := range br.Issues {
			unifieds = append(unifieds, UnifiedFinding{
				ID:          fmt.Sprintf("llm-%s-%d", issue.File, issue.LineStart),
				Source:      "llm",
				RuleCode:    issue.RuleCode,
				Severity:    issue.Severity,
				Category:    issue.Category,
				File:        issue.File,
				LineStart:   issue.LineStart,
				LineEnd:     issue.LineEnd,
				Message:     issue.Message,
				Suggestion:  issue.Suggestion,
				CodeSnippet: issue.CodeSnippet,
			})
		}
	}
	return unifieds
}

// calculateDimensionsLocally 本地计算维度得分（不调用 LLM）
func calculateDimensionsLocally(
	issues []llm.AIReviewIssue,
	dimWeights map[string]engine.DimensionWeight,
	cfg engine.DeductScoreConfig,
) map[string]llm.Dimension {
	dimensions := make(map[string]llm.Dimension)

	// 初始化所有维度
	for code, dw := range dimWeights {
		dimensions[code] = llm.Dimension{
			Score:  100,
			Weight: dw.Weight,
		}
	}

	// 按维度汇总扣分
	deductions := make(map[string]int)
	for _, issue := range issues {
		if issue.DeductScore > 0 {
			deductions[issue.Category] += issue.DeductScore
		}
	}

	// 计算得分（上限保护：deduction 不超过 100，防止 100-deduction 负溢出）
	for code, deduction := range deductions {
		if dim, ok := dimensions[code]; ok {
			dim.Score = int(math.Max(0, float64(100-math.Min(float64(deduction), 100))))
			dimensions[code] = dim
		}
	}

	return dimensions
}

// calculateTotalScore 计算总分
func calculateTotalScore(dimensions map[string]llm.Dimension) int {
	var totalScore, totalWeight int
	for _, dim := range dimensions {
		if dim.Weight > 0 {
			totalScore += dim.Score * dim.Weight
			totalWeight += dim.Weight
		}
	}
	if totalWeight == 0 {
		return 100
	}
	return int(math.Round(float64(totalScore) / float64(totalWeight)))
}
