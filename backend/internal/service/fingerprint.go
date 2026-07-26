package service

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"

	"github.com/ai-optimizer/backend/internal/model"
)

// FingerprintService Issue 语义指纹服务
type FingerprintService struct{}

func NewFingerprintService() *FingerprintService {
	return &FingerprintService{}
}

// Compute 计算 Issue 的语义指纹
func (s *FingerprintService) Compute(issue *model.ReviewIssue) string {
	snippet := normalizeCode(issue.CodeSnippet)
	if runeCount(snippet) > 150 {
		rs := []rune(snippet)
		snippet = string(rs[:150])
	}
	sig := sha256Hex(snippet)

	raw := fmt.Sprintf("%s|%s|%s", issue.File, issue.RuleCode, sig)
	return sha256Hex(raw)
}

// MatchBest 在同一 MR 下查找最佳历史匹配（含已软删除记录）
func (s *FingerprintService) MatchBest(mrID uint, fingerprint string) *model.ReviewIssue {
	if fingerprint == "" || mrID == 0 {
		return nil
	}
	var issues []model.ReviewIssue
	// 查询该 MR 下所有历史 Issue（含已软删除），按创建时间倒序
	model.DB.Unscoped().Where("mr_iid = ? AND fingerprint = ?", mrID, fingerprint).
		Order("created_at DESC").Limit(5).Find(&issues)
	// 优先返回有明确处理状态的历史记录
	for _, iss := range issues {
		if iss.Status == model.IssueStatusRejected || iss.Status == model.IssueStatusDismissed ||
			iss.Status == model.IssueStatusAccepted || iss.Status == model.IssueStatusAutoFiltered {
			return &iss
		}
	}
	// 如果没有已处理记录，返回第一条（最新的）
	if len(issues) > 0 {
		return &issues[0]
	}
	return nil
}

// IsLikelySame 模糊匹配：判定两个代码片段是否大概率是同一问题（变量重命名等）
func (s *FingerprintService) IsLikelySame(a, b string) bool {
	similarity := levenshteinNormalized(normalizeCode(a), normalizeCode(b))
	return similarity > 0.82
}

// normalizeCode 代码片段标准化：去除首尾空白、统一换行符、去除行尾注释
func normalizeCode(snippet string) string {
	lines := strings.Split(snippet, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		// 去除行尾注释（Go/Java/JS 风格）
		if idx := strings.Index(line, "// "); idx >= 0 {
			line = strings.TrimRightFunc(line[:idx], unicode.IsSpace)
		}
		if line != "" {
			out = append(out, line)
		}
	}
	s := strings.Join(out, "\n")
	// 统一为 LF 换行
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func sha256Hex(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func runeCount(s string) int {
	return len([]rune(s))
}

// levenshteinNormalized 归一化 Levenshtein 距离（0~1），值越大越相似
func levenshteinNormalized(a, b string) float64 {
	if a == "" && b == "" {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}
	dist := levenshtein([]rune(a), []rune(b))
	maxLen := len([]rune(a))
	if len([]rune(b)) > maxLen {
		maxLen = len([]rune(b))
	}
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(dist)/float64(maxLen)
}

func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// 动态规划：编辑距离
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func minInt(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
