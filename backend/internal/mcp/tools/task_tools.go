package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/mcp"
	"github.com/ai-optimizer/backend/internal/mcp/mappers"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
)

// ---------------------- 公共辅助函数 ----------------------

// requireFloat64 安全地从 args 提取 float64 参数
func requireFloat64(args map[string]interface{}, key string) (float64, bool) {
	v, ok := args[key].(float64)
	return v, ok
}

// requireString 安全地从 args 提取 string 参数
func requireString(args map[string]interface{}, key string) (string, bool) {
	v, ok := args[key].(string)
	return v, ok
}

// requireUint 安全地从 args 提取 uint 类型的 task_id / issue_id
func requireUint(args map[string]interface{}, key string) (uint, error) {
	v, ok := args[key].(float64)
	if !ok {
		return 0, fmt.Errorf("missing or invalid parameter: %s", key)
	}
	if v < 0 {
		return 0, fmt.Errorf("invalid parameter %s: must be non-negative", key)
	}
	return uint(v), nil
}

// checkTaskPermission 统一检查任务访问权限（作者或 Admin）
// 返回 nil 表示通过，否则返回权限错误
func checkTaskPermission(authCtx *mcp.AuthContext, task model.Task) error {
	if authCtx.IsAdmin {
		return nil
	}
	if authCtx.User != nil && task.MRAuthor != "" && task.MRAuthor == authCtx.User.GitlabUsername {
		return nil
	}
	return fmt.Errorf("permission denied: not your task")
}

// checkIssuePermission 统一检查 Issue 查看权限
func checkIssuePermission(authCtx *mcp.AuthContext, issue model.ReviewIssue, task model.Task) error {
	if authCtx.IsAdmin {
		return nil
	}
	// 1. Issue owner
	if issue.OwnerID != nil && *issue.OwnerID == authCtx.UserID {
		return nil
	}
	// 2. Task MR Author
	if authCtx.User != nil && task.MRAuthor != "" && task.MRAuthor == authCtx.User.GitlabUsername {
		return nil
	}
	// 3. 当前责任人
	if issue.CurrentOwnerID != nil && *issue.CurrentOwnerID == authCtx.UserID {
		return nil
	}
	// 4. 项目责任人
	var count int64
	if err := model.DB.Model(&model.ProjectResponsibility{}).
		Where("project_id = ? AND user_id = ?", task.ProjectID, authCtx.UserID).
		Count(&count).Error; err != nil {
		return fmt.Errorf("permission check failed: %w", err)
	}
	if count > 0 {
		return nil
	}
	return fmt.Errorf("permission denied: not authorized to view this issue")
}

// ---------------------- 任务相关 Tools ----------------------

func handleListTasks(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	limit := 5
	offset := 0
	if v, ok := requireFloat64(args, "limit"); ok {
		limit = int(v)
		if limit > 20 {
			limit = 20
		}
		if limit < 1 {
			limit = 1
		}
	}
	if v, ok := requireFloat64(args, "offset"); ok {
		offset = int(v)
	}

	query := model.DB.Model(&model.Task{}).Order("created_at DESC")

	// 数据隔离
	all := false
	if v, ok := args["all"].(bool); ok {
		all = v
	}

	if authCtx.IsAdmin && all {
		// Admin + all=true: 查询全部
	} else {
		// 默认按当前绑定用户过滤（非 admin 或 mine=true 时）
		if authCtx.User != nil && authCtx.User.GitlabUsername != "" {
			query = query.Where("mr_author = ?", authCtx.User.GitlabUsername)
		} else if !authCtx.IsAdmin {
			// 非 admin 且未绑定用户 → 返回空结果，避免越权
			return map[string]interface{}{
				"items":       []map[string]interface{}{},
				"total":       0,
				"query_scope": "mine",
				"next_offset": 0,
			}, nil
		}
	}

	// 过滤条件
	if status, ok := requireString(args, "status"); ok && status != "" {
		query = query.Where("status = ?", status)
	}
	if projID, ok := requireFloat64(args, "project_id"); ok && projID > 0 {
		query = query.Where("project_id = ?", uint(projID))
	}
	if author, ok := requireString(args, "author"); ok && author != "" {
		if authCtx.IsAdmin {
			query = query.Where("mr_author = ?", author)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count tasks failed: %w", err)
	}

	var tasks []model.Task
	if err := query.Limit(limit).Offset(offset).Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("query tasks failed: %w", err)
	}

	items := make([]map[string]interface{}, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, mappers.MapTaskSummary(t))
	}

	// Token 预算控制
	items = mappers.TruncateList(items, 1800)

	queryScope := "mine"
	if authCtx.IsAdmin {
		queryScope = "admin"
		if all {
			queryScope = "all"
		}
	}

	return map[string]interface{}{
		"items":       items,
		"total":       total,
		"query_scope": queryScope,
		"next_offset": offset + len(items),
	}, nil
}

func handleGetTask(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID, err := requireUint(args, "task_id")
	if err != nil {
		return nil, err
	}
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if err := checkTaskPermission(authCtx, task); err != nil {
		return nil, err
	}

	return mappers.MapTaskDetail(task), nil
}

func handleGetTaskPipeline(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID, err := requireUint(args, "task_id")
	if err != nil {
		return nil, err
	}
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if err := checkTaskPermission(authCtx, task); err != nil {
		return nil, err
	}

	var stages []model.TaskPipelineExecution
	if err := model.DB.Where("task_id = ?", taskID).Order("id ASC").Find(&stages).Error; err != nil {
		return nil, fmt.Errorf("query pipeline stages failed: %w", err)
	}

	stageList := make([]map[string]interface{}, 0, len(stages))
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
	taskID, err := requireUint(args, "task_id")
	if err != nil {
		return nil, err
	}
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if err := checkTaskPermission(authCtx, task); err != nil {
		return nil, err
	}

	limit := 10
	if v, ok := requireFloat64(args, "limit"); ok {
		limit = int(v)
		if limit > 20 {
			limit = 20
		}
	}

	query := model.DB.Model(&model.ReviewIssue{}).
		Where("task_id = ? AND deleted_at IS NULL", taskID)

	if severity, ok := requireString(args, "severity"); ok && severity != "" {
		query = query.Where("severity = ?", severity)
	}
	if category, ok := requireString(args, "category"); ok && category != "" {
		query = query.Where("category = ?", category)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count issues failed: %w", err)
	}

	var issues []model.ReviewIssue
	if err := query.Order("severity DESC, created_at DESC").Limit(limit).Find(&issues).Error; err != nil {
		return nil, fmt.Errorf("query issues failed: %w", err)
	}

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
			"status":      issue.Status,
		})
	}

	return map[string]interface{}{
		"task_id":      taskID,
		"mr_title":     task.MRTitle,
		"total_issues": total,
		"items":        items,
	}, nil
}

func handleRetryTask(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID, err := requireUint(args, "task_id")
	if err != nil {
		return nil, err
	}
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if err := checkTaskPermission(authCtx, task); err != nil {
		return nil, err
	}

	// 与平台前端和后端 Service 保持一致：failed / stopped / timeout / pending / success 均可重试
	allowed := map[model.TaskStatus]bool{
		model.TaskFailed:  true,
		model.TaskStopped: true,
		model.TaskTimeout: true,
		model.TaskPending: true,
		model.TaskSuccess: true,
	}
	if !allowed[task.Status] {
		return nil, fmt.Errorf("task status is %s, only failed / stopped / timeout / pending / success tasks can be retried", task.Status)
	}

	// 解析可选的补充复核意见
	var userReviewComment string
	if v, ok := requireString(args, "user_review_comment"); ok {
		if len(v) > 5000 {
			return nil, fmt.Errorf("user_review_comment exceeds 5000 characters")
		}
		userReviewComment = v
	}

	// 解析可选的历史复核意见 ID 列表
	var selectedCommentIDs []uint
	if arr, ok := args["selected_comment_ids"]; ok && arr != nil {
		switch ids := arr.(type) {
		case []interface{}:
			for _, item := range ids {
				if f, ok := item.(float64); ok {
					selectedCommentIDs = append(selectedCommentIDs, uint(f))
				}
			}
		case []float64:
			for _, f := range ids {
				selectedCommentIDs = append(selectedCommentIDs, uint(f))
			}
		}
	}

	// 调用后端 Service 进行重试（复用现有逻辑，包含意见注入和 Pipeline 清理）
	err = service.NewTaskService().Retry(
		taskID,
		userReviewComment,
		selectedCommentIDs,
		authCtx.UserID,
		"", // clientIP 在 MCP 场景下为空
	)
	if err != nil {
		return nil, fmt.Errorf("retry task failed: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"task_id": taskID,
		"message": "已触发重试",
	}, nil
}

func handleStopTask(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	taskID, err := requireUint(args, "task_id")
	if err != nil {
		return nil, err
	}
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %d", taskID)
	}

	if err := checkTaskPermission(authCtx, task); err != nil {
		return nil, err
	}

	if task.Status != model.TaskRunning && task.Status != model.TaskPending {
		return nil, fmt.Errorf("task status is %s, cannot stop", task.Status)
	}

	// 复用 service 层逻辑，与页面 API 保持一致的停止行为
	if err := service.NewTaskService().Abort(taskID); err != nil {
		return nil, fmt.Errorf("stop task failed: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"task_id": taskID,
		"message": "任务已终止，Pipeline 执行已中断",
	}, nil
}

// handleGetIssueDetail 查询单个 Issue 的详细信息
func handleGetIssueDetail(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	issueID, err := requireUint(args, "issue_id")
	if err != nil {
		return nil, err
	}

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

	// 权限控制
	if err := checkIssuePermission(authCtx, issue, task); err != nil {
		return nil, err
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

	// 提取 rule_id 为局部变量【P2-2 修复】
	var ruleID uint
	if issue.RuleID != nil {
		ruleID = *issue.RuleID
	}

	return map[string]interface{}{
		"id":                   issue.ID,
		"task_id":              issue.TaskID,
		"mr_id":                issue.MRID,
		"project_id":           task.ProjectID,
		"project_name":         project.Name,
		"mr_title":             task.MRTitle,
		"rule_id":              ruleID,
		"rule_name":            ruleName,
		"rule_description":     ruleDescription,
		"rule_code":            issue.RuleCode,
		"category":             issue.Category,
		"severity":             issue.Severity,
		"status":               issue.Status,
		"file_path":            issue.File,
		"line_start":           issue.LineStart,
		"line_end":             issue.LineEnd,
		"code_snippet":         issue.CodeSnippet,
		"message":              issue.Message,
		"suggestion":           issue.Suggestion,
		"deduct_score":         issue.DeductScore,
		"owner_id":             issue.OwnerID,
		"owner_name":           ownerName,
		"current_owner_id":     issue.CurrentOwnerID,
		"current_owner_name":   currentOwnerName,
		"escalation_level":     issue.EscalationLevel,
		"inherited_from_id":    issue.InheritedFromIssueID,
		"resolved_by_id":       issue.ResolvedBy,
		"resolved_by_name":     resolvedByName,
		"resolved_at":          formatTimePtr(issue.ResolvedAt),
		"reject_reason":        issue.RejectReason,
		"gitlab_discussion_id": issue.GitlabDiscussionID,
		"fingerprint":          issue.Fingerprint,
		"original_created_at":  formatTimePtr(issue.OriginalCreatedAt),
		"created_at":           issue.CreatedAt.Format("2006-01-02 15:04"),
		"updated_at":           issue.UpdatedAt.Format("2006-01-02 15:04"),
	}, nil
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}
