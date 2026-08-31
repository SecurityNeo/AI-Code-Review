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

	// 批量查询 Task 和 Project，避免 N+1
	taskMap, projectMap := buildTaskProjectMaps(issues)

	items := make([]map[string]interface{}, 0, len(issues))
	for i, issue := range issues {
		projectName, mrTitle := "", ""
		if task, ok := taskMap[issue.TaskID]; ok {
			mrTitle = task.MRTitle
			if proj, ok2 := projectMap[task.ProjectID]; ok2 {
				projectName = proj.Name
			}
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
			"days_pending": daysSincePtr(issue.OriginalCreatedAt),
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
		if !found {
			return nil, fmt.Errorf("invalid status filter: %s, allowed: resolved|false_positive|ignored|auto_archived", statusFilter)
		}
		query = query.Where("status = ?", statusFilter)
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
	issueID, err := parseIssueID(args)
	if err != nil {
		return nil, err
	}
	userID := authCtx.UserID

	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, fmt.Errorf("issue not found: %d", issueID)
	}

	if forbid, ok := checkIssueActionable(&issue, userID); !ok {
		forbid["issue_id"] = issueID
		return forbid, nil
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
	issueID, err := parseIssueID(args)
	if err != nil {
		return nil, err
	}
	userID := authCtx.UserID

	reason, ok := requireString(args, "reason")
	if !ok || reason == "" {
		return nil, fmt.Errorf("参数 'reason' 必填，请说明误报原因")
	}

	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, fmt.Errorf("issue not found: %d", issueID)
	}

	if forbid, ok := checkIssueActionable(&issue, userID); !ok {
		forbid["issue_id"] = issueID
		return forbid, nil
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
	issueID, err := parseIssueID(args)
	if err != nil {
		return nil, err
	}
	userID := authCtx.UserID

	reason, ok := requireString(args, "reason")
	if !ok || reason == "" {
		return nil, fmt.Errorf("参数 'reason' 必填，请说明忽略原因")
	}

	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, fmt.Errorf("issue not found: %d", issueID)
	}

	if forbid, ok := checkIssueActionable(&issue, userID); !ok {
		forbid["issue_id"] = issueID
		return forbid, nil
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

// checkIssueActionable 检查 Issue 是否允许当前用户执行 resolve/reject/ignore
func checkIssueActionable(issue *model.ReviewIssue, userID uint) (map[string]interface{}, bool) {
	if issue.OwnerID == nil || *issue.OwnerID != userID {
		return map[string]interface{}{
			"success":    false,
			"error_code": "permission_denied",
			"message":    "当前用户不是该 Issue 的创建者，无法处理。仅 Issue 创建者可执行 resolve/reject/ignore 操作。",
		}, false
	}
	if issue.Status != model.IssueStatusPending && issue.Status != model.IssueStatusPendingInherited {
		return map[string]interface{}{
			"success":    false,
			"error_code": "invalid_status",
			"message":    fmt.Sprintf("Issue 当前状态为 %s，只有 pending/pending_inherited 状态可被处理", issue.Status),
		}, false
	}
	return nil, true
}

// parseIssueID 安全解析 arguments 中的 issue_id
func parseIssueID(args map[string]interface{}) (uint, error) {
	raw, ok := args["issue_id"]
	if !ok {
		return 0, fmt.Errorf("issue_id is required")
	}
	v, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("issue_id must be a number, got %T", raw)
	}
	return uint(v), nil
}

// buildTaskProjectMaps 根据 Issue 列表批量查询 Task 和 Project，返回映射表
func buildTaskProjectMaps(issues []model.ReviewIssue) (map[uint]model.Task, map[uint]model.Project) {
	taskIDs := make([]uint, 0, len(issues))
	for _, issue := range issues {
		taskIDs = append(taskIDs, issue.TaskID)
	}

	taskMap := make(map[uint]model.Task)
	projectMap := make(map[uint]model.Project)

	if len(taskIDs) == 0 {
		return taskMap, projectMap
	}

	var tasks []model.Task
	model.DB.Select("id", "project_id", "mr_title").
		Where("id IN ?", taskIDs).
		Find(&tasks)

	projectIDs := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
		projectIDs = append(projectIDs, task.ProjectID)
	}

	if len(projectIDs) > 0 {
		var projects []model.Project
		model.DB.Select("id", "name").
			Where("id IN ?", projectIDs).
			Find(&projects)
		for _, proj := range projects {
			projectMap[proj.ID] = proj
		}
	}

	return taskMap, projectMap
}

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

func daysSincePtr(t *time.Time) int {
	if t == nil || t.IsZero() {
		return 0
	}
	d := int(time.Since(*t).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}
