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

	// 使用 MergeRequestReviewLog 表（与 Web 页面 mr-stats.html 一致）
	// 该表包含从 GitLab 同步的真实 MR 状态 mr_state
	query := model.DB.Model(&model.MergeRequestReviewLog{}).Order("synced_at DESC")

	if authCtx.IsAdmin && all {
		// Admin + all=true: 查询全部
	} else {
		if mine && gitlabUsername != "" {
			query = query.Where("author = ?", gitlabUsername)
		}
	}

	state := "opened"
	if s, ok := args["state"].(string); ok && s != "" {
		state = s
	}
	if state != "all" {
		query = query.Where("mr_state = ?", state)
	}

	if projName, ok := args["project_name"].(string); ok && projName != "" {
		query = query.Where("project_name = ?", projName)
	}
	if author, ok := args["author"].(string); ok && author != "" {
		if authCtx.IsAdmin {
			query = query.Where("author = ?", author)
		}
	}

	var total int64
	query.Count(&total)

	var logs []model.MergeRequestReviewLog
	query.Limit(limit).Offset(offset).Find(&logs)

	items := make([]map[string]interface{}, 0, len(logs))
	for _, l := range logs {
		createdAt := "-"
		if l.MRCreatedAt != nil {
			createdAt = l.MRCreatedAt.Format("2006-01-02")
		}

		items = append(items, map[string]interface{}{
			"mr_iid":           l.MRID,
			"title":            l.MRTitle,
			"author":           l.Author,
			"author_name":      l.AuthorDisplayName,
			"project_name":     l.ProjectName,
			"source_branch":    l.SourceBranch,
			"target_branch":    l.TargetBranch,
			"state":            l.MRState,
			"is_draft":         l.IsDraft,
			"score":            l.Score,
			"review_count":     l.ReviewCount,
			"additions":        l.Additions,
			"deletions":        l.Deletions,
			"url":              l.URL,
			"created_at":       createdAt,
			"synced_at":        l.SyncedAt.Format("2006-01-02"),
			"last_commit_id":   l.LastCommitID,
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
	projectIDRaw, err := requireUint(args, "project_id")
	if err != nil {
		return nil, err
	}
	mrIIDRaw, err := requireUint(args, "mr_iid")
	if err != nil {
		return nil, err
	}
	projectID := projectIDRaw
	mrIID := int(mrIIDRaw)

	var task model.Task
	if err := model.DB.Where("project_id = ? AND mr_merge_id = ?", projectID, mrIID).Order("created_at DESC").First(&task).Error; err != nil {
		return nil, fmt.Errorf("MR not found: project=%d, mr_iid=%d", projectID, mrIID)
	}

	if !authCtx.IsAdmin && task.MRAuthor != authCtx.User.GitlabUsername {
		return map[string]interface{}{
			"success":    false,
			"error_code": "permission_denied",
			"message":    "无权查看此 MR，仅 MR 作者或 Admin 可访问",
		}, nil
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
