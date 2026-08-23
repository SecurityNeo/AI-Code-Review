package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ai-optimizer/backend/internal/mcp"
	"github.com/ai-optimizer/backend/internal/model"
)

// ---------------------- MR 查询 Tools ----------------------

func handleListMRs(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	limit := 5
	offset := 0
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
		if limit > 20 {
			limit = 20
		}
	}
	if v, ok := args["offset"].(float64); ok {
		offset = int(v)
	}

	// 数据隔离
	mine := true
	all := false
	if v, ok := args["mine"].(bool); ok {
		mine = v
	}
	if v, ok := args["all"].(bool); ok {
		all = v
	}

	// 获取当前用户关联的 GitLab 用户名
	gitlabUsername := ""
	if authCtx.User != nil {
		gitlabUsername = authCtx.User.GitlabUsername
	}

	// 对于 MR 查询，我们需要从 GitLab 获取实际 MR 列表
	// 这里用 Task 表近似代替（因为只有已触发评审的 MR 才有记录）
	query := model.DB.Model(&model.Task{}).Order("created_at DESC")

	if authCtx.IsAdmin && all {
		// 查询全部
	} else {
		if mine && gitlabUsername != "" {
			query = query.Where("mr_author = ?", gitlabUsername)
		}
	}

	state := "opened"
	if s, ok := args["state"].(string); ok && s != "" {
		state = s
	}
	if state != "all" {
		// Task 没有 state 字段，用 status 近似映射
		if state == "opened" {
			query = query.Where("status IN ?", []model.TaskStatus{model.TaskPending, model.TaskRunning, model.TaskFailed, model.TaskSuccess})
		}
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
		projectName := ""
		var p model.Project
		model.DB.Select("name").First(&p, t.ProjectID)
		projectName = p.Name

		items = append(items, map[string]interface{}{
			"mr_iid":         t.MRMergeID,
			"title":          t.MRTitle,
			"author":         t.MRAuthor,
			"project_name":   projectName,
			"source_branch":  t.SourceBranch,
			"target_branch":  t.TargetBranch,
			"state":          "opened",
			"created_at":     t.CreatedAt.Format("2006-01-02"),
			"updated_at":     t.UpdatedAt.Format("2006-01-02"),
			"has_task":       true,
			"latest_task_id": t.ID,
			"latest_task_status": string(t.Status),
		})
	}

	queryScope := "mine"
	if authCtx.IsAdmin && all {
		queryScope = "all"
	}

	return map[string]interface{}{
		"items":       items,
		"total":       total,
		"query_scope": queryScope,
		"next_offset": offset + len(items),
	}, nil
}

func handleGetMRDetail(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	projectID := uint(args["project_id"].(float64))
	mrIID := int(args["mr_iid"].(float64))

	var task model.Task
	if err := model.DB.Where("project_id = ? AND mr_merge_id = ?", projectID, mrIID).Order("created_at DESC").First(&task).Error; err != nil {
		return nil, fmt.Errorf("MR not found: project=%d, mr_iid=%d", projectID, mrIID)
	}

	if !authCtx.IsAdmin && task.MRAuthor != authCtx.User.GitlabUsername {
		return nil, fmt.Errorf("permission denied")
	}

	// diff 摘要（使用 []map[string]interface{} 解析，因为 DiffFilesJSON 存储的是小写字段名）
	var diffFiles []map[string]interface{}
	_ = json.Unmarshal([]byte(task.DiffFilesJSON), &diffFiles)
	fileList := make([]string, 0, len(diffFiles))
	additions, deletions := 0, 0
	for _, df := range diffFiles {
		if newPath, ok := df["new_path"].(string); ok && newPath != "" {
			fileList = append(fileList, newPath)
		} else if oldPath, ok := df["old_path"].(string); ok && oldPath != "" {
			fileList = append(fileList, oldPath)
		}
		if add, ok := df["additions"].(float64); ok {
			additions += int(add)
		}
		if del, ok := df["deletions"].(float64); ok {
			deletions += int(del)
		}
	}

	// 关联的 CodeGuard Tasks
	var cgTasks []map[string]interface{}
	model.DB.Model(&model.Task{}).Where("project_id = ? AND mr_merge_id = ?", projectID, mrIID).Order("created_at DESC").Find(&[]model.Task{}).Scan(&cgTasks)

	taskHistory := make([]map[string]interface{}, 0)
	var relatedTasks []model.Task
	model.DB.Where("project_id = ? AND mr_merge_id = ?", projectID, mrIID).Order("created_at DESC").Limit(5).Find(&relatedTasks)
	for _, rt := range relatedTasks {
		var issueCount int64
		model.DB.Model(&model.ReviewIssue{}).Where("task_id = ? AND deleted_at IS NULL", rt.ID).Count(&issueCount)
		taskHistory = append(taskHistory, map[string]interface{}{
			"task_id":      rt.ID,
			"status":       string(rt.Status),
			"issue_count":  issueCount,
			"created_at":   rt.CreatedAt.Format("2006-01-02 15:04"),
			"duration_sec": rt.DurationSec,
		})
	}

	projectName := ""
	var p model.Project
	model.DB.Select("name").First(&p, projectID)
	projectName = p.Name

	return map[string]interface{}{
		"mr_iid":        mrIID,
		"title":         task.MRTitle,
		"description":   task.MRTitle,
		"author":        task.MRAuthor,
		"project_name":  projectName,
		"source_branch": task.SourceBranch,
		"target_branch": task.TargetBranch,
		"state":         "opened",
		"created_at":    task.CreatedAt.Format("2006-01-02"),
		"updated_at":    task.UpdatedAt.Format("2006-01-02"),
		"changed_files": map[string]interface{}{
			"count":      len(diffFiles),
			"additions":  additions,
			"deletions":  deletions,
			"file_list":  fileList,
		},
		"codeguard_tasks": taskHistory,
	}, nil
}
