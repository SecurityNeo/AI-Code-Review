package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/mcp"
	"github.com/ai-optimizer/backend/internal/mcp/mappers"
	"github.com/ai-optimizer/backend/internal/model"
)

// ---------------------- 任务相关 Tools ----------------------

func handleListTasks(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	limit := 5
	offset := 0
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
		if limit > 20 {
			limit = 20
		}
		if limit < 1 {
			limit = 1
		}
	}
	if v, ok := args["offset"].(float64); ok {
		offset = int(v)
	}

	query := model.DB.Model(&model.Task{}).Order("created_at DESC")

	// 数据隔离
	mine := true
	all := false
	if v, ok := args["mine"].(bool); ok {
		mine = v
	}
	if v, ok := args["all"].(bool); ok {
		all = v
	}

	if authCtx.IsAdmin && all {
		// Admin + all=true: 查询全部
	} else {
		// 默认按当前绑定用户过滤
		if authCtx.User != nil && authCtx.User.GitlabUsername != "" {
			if mine {
				query = query.Where("mr_author = ?", authCtx.User.GitlabUsername)
			}
		}
	}

	// 过滤条件
	if status, ok := args["status"].(string); ok && status != "" {
		query = query.Where("status = ?", status)
	}
	if projID, ok := args["project_id"].(float64); ok && projID > 0 {
		query = query.Where("project_id = ?", uint(projID))
	}
	if author, ok := args["author"].(string); ok && author != "" {
		if authCtx.IsAdmin {
			query = query.Where("mr_author = ?", author)
		}
	}

	var total int64
	query.Count(&total)

	var tasks []model.Task
	query.Limit(limit).Offset(offset).Find(&tasks)

	items := make([]map[string]interface{}, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, mappers.MapTaskSummary(t))
	}

	// Token 预算控制
	items = mappers.TruncateList(items, 1800)

	queryScope := "mine"
	if authCtx.IsAdmin && all {
		queryScope = "all"
	}

	return map[string]interface{}{
		"items":      items,
		"total":      total,
		"query_scope": queryScope,
		"next_offset": offset + len(items),
	}, nil
}

func handleGetTask(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID := uint(args["task_id"].(float64))
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	// 权限检查
	if !authCtx.IsAdmin && task.MRAuthor != authCtx.User.GitlabUsername {
		return nil, fmt.Errorf("permission denied: not your task")
	}

	return mappers.MapTaskDetail(task), nil
}

func handleGetTaskPipeline(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID := uint(args["task_id"].(float64))
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if !authCtx.IsAdmin && task.MRAuthor != authCtx.User.GitlabUsername {
		return nil, fmt.Errorf("permission denied: not your task")
	}

	var stages []model.TaskPipelineExecution
	model.DB.Where("task_id = ?", taskID).Order("id ASC").Find(&stages)

	stageList := make([]map[string]interface{}, 0)
	for _, s := range stages {
		stageList = append(stageList, mappers.MapPipeline(s))
	}

	return map[string]interface{}{
		"task_id":        taskID,
		"overall_status": string(task.Status),
		"stages":         stageList,
	}, nil
}

func handleGetMRReview(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID := uint(args["task_id"].(float64))
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if !authCtx.IsAdmin && task.MRAuthor != authCtx.User.GitlabUsername {
		return nil, fmt.Errorf("permission denied: not your task")
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
		if limit > 20 {
			limit = 20
		}
	}

	query := model.DB.Model(&model.ReviewIssue{}).
		Where("task_id = ? AND deleted_at IS NULL", taskID)

	if severity, ok := args["severity"].(string); ok && severity != "" {
		query = query.Where("severity = ?", severity)
	}
	if category, ok := args["category"].(string); ok && category != "" {
		query = query.Where("category = ?", category)
	}

	var total int64
	query.Count(&total)

	var issues []model.ReviewIssue
	query.Order("severity DESC, created_at DESC").Limit(limit).Find(&issues)

	items := make([]map[string]interface{}, 0, len(issues))
	for _, issue := range issues {
		items = append(items, map[string]interface{}{
			"issue_id":    issue.ID,
			"category":    issue.Category,
			"severity":    issue.Severity,
			"file_path":   issue.File,
			"line_number": issue.LineStart,
			"message":     issue.Message,
			"suggestion":  issue.Suggestion,
		})
	}

	return map[string]interface{}{
		"task_id":     taskID,
		"mr_title":    task.MRTitle,
		"total_issues": total,
		"items":       items,
	}, nil
}

func handleRetryTask(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID := uint(args["task_id"].(float64))
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	// 权限：作者或 Admin
	if !authCtx.IsAdmin && task.MRAuthor != authCtx.User.GitlabUsername {
		return nil, fmt.Errorf("permission denied: not your task")
	}

	if task.Status != model.TaskFailed && task.Status != model.TaskStopped {
		return nil, fmt.Errorf("task status is %s, only failed or stopped tasks can be retried", task.Status)
	}

	// 创建重试任务（简化：创建新任务复制原任务信息）
	newTask := model.Task{
		ProjectID:    task.ProjectID,
		MRMergeID:    task.MRMergeID,
		MRTitle:      task.MRTitle,
		MRAuthor:     task.MRAuthor,
		SourceBranch: task.SourceBranch,
		TargetBranch: task.TargetBranch,
		Status:       model.TaskPending,
	}
	if err := model.DB.Create(&newTask).Error; err != nil {
		return nil, fmt.Errorf("create retry task failed: %w", err)
	}

	return map[string]interface{}{
		"success":     true,
		"task_id":     taskID,
		"new_task_id": newTask.ID,
		"message":     fmt.Sprintf("已创建重试任务，新任务ID: %d", newTask.ID),
	}, nil
}

func handleStopTask(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID := uint(args["task_id"].(float64))
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if !authCtx.IsAdmin && task.MRAuthor != authCtx.User.GitlabUsername {
		return nil, fmt.Errorf("permission denied: not your task")
	}

	if task.Status != model.TaskRunning && task.Status != model.TaskPending {
		return nil, fmt.Errorf("task status is %s, cannot stop", task.Status)
	}

	// 更新状态为 stopped
	model.DB.Model(&task).Update("status", model.TaskStopped)

	return map[string]interface{}{
		"success": true,
		"task_id": taskID,
		"message": "已发送停止信号，任务将在当前 stage 完成后终止",
	}, nil
}

// handleGetIssueDetail 查询单个 Issue 的详细信息
func handleGetIssueDetail(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	issueID := uint(args["issue_id"].(float64))

	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, fmt.Errorf("issue not found: %d", issueID)
	}

	// 加载关联的 Task 和 Project 做权限校验
	var task model.Task
	if err := model.DB.First(&task, issue.TaskID).Error; err != nil {
		return nil, fmt.Errorf("task not found for issue")
	}

	var project model.Project
	if err := model.DB.First(&project, task.ProjectID).Error; err != nil {
		return nil, fmt.Errorf("project not found for issue")
	}

	// 权限控制：admin、issue owner、task author、项目责任人可查看
	if !authCtx.IsAdmin {
		allowed := false

		// 1. Issue owner（MR 提交者）
		if issue.OwnerID != nil && *issue.OwnerID == authCtx.UserID {
			allowed = true
		}

		// 2. Task MR Author
		if !allowed && task.MRAuthor != "" && task.MRAuthor == authCtx.User.GitlabUsername {
			allowed = true
		}

		// 3. 当前责任人（current_owner_id）
		if !allowed && issue.CurrentOwnerID != nil && *issue.CurrentOwnerID == authCtx.UserID {
			allowed = true
		}

		// 4. 项目负责人（通过 project_responsibilities 判定）
		if !allowed {
			var count int64
			model.DB.Model(&model.ProjectResponsibility{}).
				Where("project_id = ? AND user_id = ?", task.ProjectID, authCtx.UserID).
				Count(&count)
			if count > 0 {
				allowed = true
			}
		}

		if !allowed {
			return nil, fmt.Errorf("permission denied: not authorized to view this issue")
		}
	}

	// 构建 owner / resolver 显示名
	ownerName := ""
	if issue.OwnerID != nil && *issue.OwnerID > 0 {
		var owner model.User
		if model.DB.Select("username, display_name").First(&owner, *issue.OwnerID).Error == nil {
			ownerName = owner.DisplayName
			if ownerName == "" {
				ownerName = owner.Username
			}
		}
	}

	currentOwnerName := ""
	if issue.CurrentOwnerID != nil && *issue.CurrentOwnerID > 0 {
		var co model.User
		if model.DB.Select("username, display_name").First(&co, *issue.CurrentOwnerID).Error == nil {
			currentOwnerName = co.DisplayName
			if currentOwnerName == "" {
				currentOwnerName = co.Username
			}
		}
	}

	resolvedByName := ""
	if issue.ResolvedBy > 0 {
		var ru model.User
		if model.DB.Select("username, display_name").First(&ru, issue.ResolvedBy).Error == nil {
			resolvedByName = ru.DisplayName
			if resolvedByName == "" {
				resolvedByName = ru.Username
			}
		}
	}

	// 关联 Rule 信息（如有）
	var ruleName, ruleDescription string
	if issue.RuleID != nil && *issue.RuleID > 0 {
		var rule struct {
			Code        string
			Name        string
			Description string
		}
		if model.DB.Table("review_rules").Select("code, name, description").Where("id = ?", *issue.RuleID).Scan(&rule).Error == nil {
			ruleName = rule.Name
			if ruleName == "" {
				ruleName = rule.Code
			}
			ruleDescription = rule.Description
		}
	}

	return map[string]interface{}{
		"id":                 issue.ID,
		"task_id":            issue.TaskID,
		"mr_id":              issue.MRID,
		"project_id":         task.ProjectID,
		"project_name":       project.Name,
		"mr_title":           task.MRTitle,
		"rule_id":            func() uint { if issue.RuleID != nil { return *issue.RuleID }; return 0 }(),
		"rule_name":          ruleName,
		"rule_description":   ruleDescription,
		"rule_code":          issue.RuleCode,
		"category":           issue.Category,
		"severity":           issue.Severity,
		"status":             issue.Status,
		"file_path":          issue.File,
		"line_start":         issue.LineStart,
		"line_end":           issue.LineEnd,
		"code_snippet":       issue.CodeSnippet,
		"message":            issue.Message,
		"suggestion":         issue.Suggestion,
		"deduct_score":       issue.DeductScore,
		"owner_id":           issue.OwnerID,
		"owner_name":         ownerName,
		"current_owner_id":   issue.CurrentOwnerID,
		"current_owner_name": currentOwnerName,
		"escalation_level":   issue.EscalationLevel,
		"inherited_from_id":  issue.InheritedFromIssueID,
		"resolved_by_id":     issue.ResolvedBy,
		"resolved_by_name":   resolvedByName,
		"resolved_at":        formatTimePtr(issue.ResolvedAt),
		"reject_reason":      issue.RejectReason,
		"gitlab_discussion_id": issue.GitlabDiscussionID,
		"fingerprint":        issue.Fingerprint,
		"original_created_at": formatTimePtr(issue.OriginalCreatedAt),
		"created_at":         issue.CreatedAt.Format("2006-01-02 15:04"),
		"updated_at":         issue.UpdatedAt.Format("2006-01-02 15:04"),
	}, nil
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}
