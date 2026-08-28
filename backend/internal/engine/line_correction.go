package engine

import (
	"github.com/ai-optimizer/backend/config"
	"github.com/ai-optimizer/backend/pkg/diff"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

// ApplyLineCorrections 对 LLM 返回的 issues 进行行号校正
// 如果配置未启用、灰度不命中、或 rawDiff 为空，直接返回原始 issues
func ApplyLineCorrections(issues []llm.AIReviewIssue, rawDiff string, cfg *config.DiffLineMapConfig, projectID uint) []llm.AIReviewIssue {
	if cfg == nil || !cfg.Enabled || !cfg.UseCorrelator {
		return issues
	}
	if !cfg.IsGrayEnabled(projectID) {
		zap.L().Debug("ApplyLineCorrections: gray not enabled for project",
			zap.Uint("project_id", projectID),
			zap.Int("gray_percent", cfg.GrayPercent))
		return issues
	}
	if rawDiff == "" {
		zap.L().Warn("ApplyLineCorrections: rawDiff is empty, skipping correction")
		return issues
	}

	parser := diff.NewParser()
	parsedFiles, err := parser.Parse(rawDiff)
	if err != nil {
		zap.L().Warn("ApplyLineCorrections: parse rawDiff failed",
			zap.Error(err),
			zap.Int("issue_count", len(issues)),
		)
		return issues
	}
	if len(parsedFiles) == 0 {
		zap.L().Warn("ApplyLineCorrections: no files parsed from rawDiff")
		return issues
	}

	// 构建 filePath -> ParsedDiffFile 映射
	fileMap := make(map[string]*diff.ParsedDiffFile, len(parsedFiles))
	for _, pf := range parsedFiles {
		fileMap[pf.NewPath] = pf
	}

	// 【P1 修复】使用带阈值的校正器，避免低置信度 fuzzy 匹配导致错误校正
	correlator := diff.NewLineCorrelatorWithThreshold(fileMap, cfg.MinFuzzyConfidence)

	// 【P2】指标收集
	var corrected, unchanged, exactMatch, fuzzyMatch int
	var matchedConfidenceSum float64

	for i := range issues {
		result := correlator.CorrectIssue(
			issues[i].File,
			issues[i].LineStart,
			issues[i].LineEnd,
			issues[i].CodeSnippet,
		)

		switch result.MatchType {
		case diff.MatchTypeExact:
			exactMatch++
			matchedConfidenceSum += result.Confidence
		case diff.MatchTypeFuzzy:
			fuzzyMatch++
			matchedConfidenceSum += result.Confidence
		}

		if result.NewStart != result.OldStart || result.NewEnd != result.OldEnd {
			corrected++
			zap.L().Info("ApplyLineCorrections: line number corrected",
				zap.String("file", issues[i].File),
				zap.Int("old_start", result.OldStart),
				zap.Int("new_start", result.NewStart),
				zap.Float64("confidence", result.Confidence),
				zap.String("match_type", matchTypeString(result.MatchType)),
			)
			issues[i].LineStart = result.NewStart
			issues[i].LineEnd = result.NewEnd
		} else {
			unchanged++
		}
	}

	// 【P2】输出结构化指标（可被 ELK / Loki 聚合分析）
	// 【R4 修复】avgConfidence 只统计 Exact+Fuzzy，排除 NotAvailable 的 0 值
	matchedCount := exactMatch + fuzzyMatch
	avgConf := 0.0
	if matchedCount > 0 {
		avgConf = matchedConfidenceSum / float64(matchedCount)
	}
	zap.L().Info("ApplyLineCorrections: metrics",
		zap.Int("total_issues", len(issues)),
		zap.Int("corrected", corrected),
		zap.Int("unchanged", unchanged),
		zap.Int("exact_match", exactMatch),
		zap.Int("fuzzy_match", fuzzyMatch),
		zap.Int("matched_count", matchedCount),
		zap.Float64("avg_confidence", avgConf),
	)

	return issues
}

func matchTypeString(mt diff.MatchType) string {
	switch mt {
	case diff.MatchTypeExact:
		return "exact"
	case diff.MatchTypeFuzzy:
		return "fuzzy"
	case diff.MatchTypeNotAvailable:
		return "not_available"
	default:
		return "unknown"
	}
}
