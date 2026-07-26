package engine

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ComputeFingerprint 计算 Issue 的语义指纹
func ComputeFingerprint(issue *model.ReviewIssue) string {
	snippet := normalizeCode(issue.CodeSnippet)
	if runeCount(snippet) > 150 {
		rs := []rune(snippet)
		snippet = string(rs[:150])
	}
	sig := sha256Hex(snippet)
	raw := fmt.Sprintf("%s|%s|%s", issue.File, issue.RuleCode, sig)
	return sha256Hex(raw)
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

// BuildIssueHistoryMap 构建同一 MR 下所有历史 Issue 的指纹→最新记录映射（含已软删除）
func BuildIssueHistoryMap(tx *gorm.DB, mrID int, excludeTaskID uint) map[string]*model.ReviewIssue {
	if mrID <= 0 {
		return map[string]*model.ReviewIssue{}
	}
	var issues []model.ReviewIssue
	// 注意：这里不能用 tx（当前事务内的 soft delete 会过滤掉），需用 model.DB.Unscoped()
	if err := model.DB.Unscoped().
		Where("mr_id = ? AND task_id != ? AND fingerprint != ''", mrID, excludeTaskID).
		Order("created_at DESC").
		Find(&issues).Error; err != nil {
		zap.L().Warn("load issue history failed", zap.Int("mr_id", mrID), zap.Error(err))
		return map[string]*model.ReviewIssue{}
	}

	out := make(map[string]*model.ReviewIssue, len(issues))
	for i := range issues {
		iss := &issues[i]
		if _, exists := out[iss.Fingerprint]; !exists {
			out[iss.Fingerprint] = iss
		}
	}
	return out
}

// ApplyFingerprintAndInheritance 对新 Issue 应用指纹计算和状态继承
func ApplyFingerprintAndInheritance(issue *model.ReviewIssue, historyMap map[string]*model.ReviewIssue, ownerID uint) {
	issue.Fingerprint = ComputeFingerprint(issue)

	if ownerID > 0 {
		issue.OwnerID = &ownerID
		issue.CurrentOwnerID = &ownerID
	}

	if historyMap == nil {
		now := time.Now()
		issue.OriginalCreatedAt = &now
		return
	}

	if hist, ok := historyMap[issue.Fingerprint]; ok {
		issue.InheritedFromIssueID = &hist.ID
		switch hist.Status {
		case model.IssueStatusRejected, model.IssueStatusDismissed, model.IssueStatusAutoFiltered:
			issue.Status = model.IssueStatusAutoFiltered
			issue.OriginalCreatedAt = hist.OriginalCreatedAt
			if issue.OriginalCreatedAt == nil || issue.OriginalCreatedAt.IsZero() {
				issue.OriginalCreatedAt = &hist.CreatedAt
			}
		case model.IssueStatusAccepted:
			issue.Status = model.IssueStatusPending
			issue.OriginalCreatedAt = hist.OriginalCreatedAt
			if issue.OriginalCreatedAt == nil || issue.OriginalCreatedAt.IsZero() {
				issue.OriginalCreatedAt = &hist.CreatedAt
			}
		default:
			issue.Status = model.IssueStatusPending
			issue.OriginalCreatedAt = &hist.CreatedAt
		}
	} else {
		now := time.Now()
		issue.OriginalCreatedAt = &now
	}
}
