package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/mcp"
	"github.com/ai-optimizer/backend/internal/model"
)

// ---------------------- Issue 治理 Tools ----------------------

func handleListPendingIssues(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	userID := authCtx.UserID
	baseline := getNotificationBaseline()

	limit := 20
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
		if limit > 50 {
			limit = 50
		}
		if limit < 1 {
			limit = 1
		}
	}

	query := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND owner_id = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID)

	if !baseline.IsZero() {
		query = query.Where("original_created_at >= ?", baseline)
	}

	var total int64
	query.Count(&total)

	var issues []model.ReviewIssue
	query.Order("severity DESC, original_created_at ASC").
		Limit(limit).
		Find(&issues)

	items := make([]map[string]interface{}, 0, len(issues))
	for i, issue := range issues {
		projectName, mrTitle := "", ""
		var task model.Task
		if model.DB.Select("project_id, mr_title").First(&task, issue.TaskID).Error == nil {
			var p model.Project
			model.DB.Select("name").First(&p, task.ProjectID)
			projectName = p.Name
			mrTitle = task.MRTitle
		}
		items = append(items, map[string]interface{}{
			"index":        i + 1,
			"issue_id":     issue.ID,
			"severity":     issue.Severity,
			"category":     issue.Category,
			"file":         issue.File,
			"line_start":   issue.LineStart,
			"message":      truncateString(issue.Message, 200),
			"suggestion":   truncateString(issue.Suggestion, 200),
			"project_name": projectName,
			"mr_title":     mrTitle,
			"days_pending": daysSince(issue.OriginalCreatedAt),
		})
	}

	return map[string]interface{}{
		"total":    total,
		"returned": len(items),
		"items":    items,
		"hint":     "用户可能通过 index 引用 Issue（如'第 1 个'），请维护 index -> issue_id 的映射关系",
	}, nil
}

func handleListHistoricalIssues(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	userID := authCtx.UserID

	statusFilter := ""
	if v, ok := args["status"].(string); ok && v != "" {
		statusFilter = v
	}

	limit := 20
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
		if limit > 50 {
			limit = 50
		}
		if limit < 1 {
			limit = 1
		}
	}

	validStatuses := []string{
		model.IssueStatusResolved,
		model.IssueStatusFalsePositive,
		model.IssueStatusIgnored,
		model.IssueStatusAutoArchived,
	}

	query := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND owner_id = ?", validStatuses, userID)

	if statusFilter != "" {
		found := false
		for _, s := range validStatuses {
			if s == statusFilter {
				found = true
				break
			}
		}
		if found {
			query = query.Where("status = ?", statusFilter)
		}
	}

	var total int64
	query.Count(&total)

	var issues []model.ReviewIssue
	query.Order("resolved_at DESC, updated_at DESC").
		Limit(limit).
		Find(&issues)

	items := make([]map[string]interface{}, 0, len(issues))
	for i, issue := range issues {
		items = append(items, map[string]interface{}{
			"index":         i + 1,
			"issue_id":      issue.ID,
			"severity":      issue.Severity,
			"category":      issue.Category,
			"file":          issue.File,
			"line_start":    issue.LineStart,
			"message":       truncateString(issue.Message, 200),
			"status":        issue.Status,
			"resolved_at":   formatTimePtr(issue.ResolvedAt),
			"reject_reason": issue.RejectReason,
		})
	}

	return map[string]interface{}{
		"total":    total,
		"returned": len(items),
		"items":    items,
		"filter":   statusFilter,
	}, nil
}

func handleResolveIssue(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	issueID := uint(args["issue_id"].(float64))
	userID := authCtx.UserID

	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, fmt.Errorf("issue not found: %d", issueID)
	}

	if issue.OwnerID == nil || *issue.OwnerID != userID {
		return map[string]interface{}{
			"success":    false,
			"issue_id":   issueID,
			"error_code": "permission_denied",
			"message":    "当前用户不是该 Issue 的创建者，无法处理。仅 Issue 创建者可执行 resolve/reject/ignore 操作。",
		}, nil
	}

	if issue.Status != model.IssueStatusPending && issue.Status != model.IssueStatusPendingInherited {
		return map[string]interface{}{
			"success":    false,
			"issue_id":   issueID,
			"error_code": "invalid_status",
			"message":    fmt.Sprintf("Issue 当前状态为 %s，只有 pending/pending_inherited 状态可被处理", issue.Status),
		}, nil
	}

	now := time.Now()
	comment := ""
	if v, ok := args["comment"].(string); ok {
		comment = v
	}
	updates := map[string]interface{}{
		"status":        model.IssueStatusResolved,
		"resolved_by":   userID,
		"resolved_at":   &now,
		"is_resolved":   true,
		"reject_reason": comment,
	}
	if err := model.DB.Model(&model.ReviewIssue{}).Where("id = ?", issueID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update issue failed: %w", err)
	}

	return map[string]interface{}{
		"success":    true,
		"issue_id":   issueID,
		"new_status": model.IssueStatusResolved,
		"file":       issue.File,
		"line_start": issue.LineStart,
		"message":    truncateString(issue.Message, 120),
	}, nil
}

func handleRejectIssue(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	issueID := uint(args["issue_id"].(float64))
	userID := authCtx.UserID

	reason, _ := args["reason"].(string)
	if reason == "" {
		return nil, fmt.Errorf("reason is required for rejecting an issue")
	}

	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, fmt.Errorf("issue not found: %d", issueID)
	}

	if issue.OwnerID == nil || *issue.OwnerID != userID {
		return map[string]interface{}{
			"success":    false,
			"issue_id":   issueID,
			"error_code": "permission_denied",
			"message":    "当前用户不是该 Issue 的创建者，无法处理。仅 Issue 创建者可执行 resolve/reject/ignore 操作。",
		}, nil
	}

	if issue.Status != model.IssueStatusPending && issue.Status != model.IssueStatusPendingInherited {
		return map[string]interface{}{
			"success":    false,
			"issue_id":   issueID,
			"error_code": "invalid_status",
			"message":    fmt.Sprintf("Issue 当前状态为 %s，只有 pending/pending_inherited 状态可被处理", issue.Status),
		}, nil
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":        model.IssueStatusFalsePositive,
		"resolved_by":   userID,
		"resolved_at":   &now,
		"is_resolved":   true,
		"reject_reason": reason,
	}
	if err := model.DB.Model(&model.ReviewIssue{}).Where("id = ?", issueID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update issue failed: %w", err)
	}

	return map[string]interface{}{
		"success":    true,
		"issue_id":   issueID,
		"new_status": model.IssueStatusFalsePositive,
		"file":       issue.File,
		"line_start": issue.LineStart,
		"message":    truncateString(issue.Message, 120),
		"reason":     reason,
	}, nil
}

func handleIgnoreIssue(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	issueID := uint(args["issue_id"].(float64))
	userID := authCtx.UserID

	reason, _ := args["reason"].(string)
	if reason == "" {
		return nil, fmt.Errorf("reason is required for ignoring an issue")
	}

	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, fmt.Errorf("issue not found: %d", issueID)
	}

	if issue.OwnerID == nil || *issue.OwnerID != userID {
		return map[string]interface{}{
			"success":    false,
			"issue_id":   issueID,
			"error_code": "permission_denied",
			"message":    "当前用户不是该 Issue 的创建者，无法处理。仅 Issue 创建者可执行 resolve/reject/ignore 操作。",
		}, nil
	}

	if issue.Status != model.IssueStatusPending && issue.Status != model.IssueStatusPendingInherited {
		return map[string]interface{}{
			"success":    false,
			"issue_id":   issueID,
			"error_code": "invalid_status",
			"message":    fmt.Sprintf("Issue 当前状态为 %s，只有 pending/pending_inherited 状态可被处理", issue.Status),
		}, nil
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":        model.IssueStatusIgnored,
		"resolved_by":   userID,
		"resolved_at":   &now,
		"is_resolved":   true,
		"reject_reason": reason,
	}
	if err := model.DB.Model(&model.ReviewIssue{}).Where("id = ?", issueID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update issue failed: %w", err)
	}

	return map[string]interface{}{
		"success":    true,
		"issue_id":   issueID,
		"new_status": model.IssueStatusIgnored,
		"file":       issue.File,
		"line_start": issue.LineStart,
		"message":    truncateString(issue.Message, 120),
		"reason":     reason,
	}, nil
}

// ---------------------- Helpers ----------------------

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func daysSince(t *time.Time) int {
	if t == nil || t.IsZero() {
		return 0
	}
	return int(time.Since(*t).Hours() / 24)
}
