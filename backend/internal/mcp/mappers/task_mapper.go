package mappers

import (
	"encoding/json"

	"github.com/ai-optimizer/backend/internal/model"
)

// estimateTokens 估算 JSON 文本的 Token 数（简化版）
func estimateTokens(data interface{}) int {
	b, _ := json.Marshal(data)
	return len([]rune(string(b))) / 2
}

// TruncateList 截断列表以适应 Token 预算
func TruncateList(items []map[string]interface{}, budget int) []map[string]interface{} {
	result := make([]map[string]interface{}, 0)
	currentTokens := 0

	for _, item := range items {
		itemJSON, _ := json.Marshal(item)
		itemTokens := len([]rune(string(itemJSON))) / 2
		if currentTokens+itemTokens > budget {
			break
		}
		result = append(result, item)
		currentTokens += itemTokens
	}
	return result
}

// --- Task Mappers ---

func MapTaskSummary(task model.Task) map[string]interface{} {
	projectName := ""
	if task.ProjectID > 0 {
		var p model.Project
		model.DB.Select("name").First(&p, task.ProjectID)
		projectName = p.Name
	}
	return map[string]interface{}{
		"id":            task.ID,
		"status":        string(task.Status),
		"mr_title":      task.MRTitle,
		"author":        task.MRAuthor,
		"project_name":  projectName,
		"source_branch": task.SourceBranch,
		"target_branch": task.TargetBranch,
		"created_at":    task.CreatedAt.Format("2006-01-02 15:04"),
		"duration_sec":  task.DurationSec,
	}
}

func MapTaskDetail(task model.Task) map[string]interface{} {
	projectName := ""
	projectLanguage := ""
	if task.ProjectID > 0 {
		var p model.Project
		model.DB.Select("name, language").First(&p, task.ProjectID)
		projectName = p.Name
		projectLanguage = p.Language
	}

	// diff 摘要（使用 []map[string]interface{} 解析，因为 DiffFilesJSON 存储的是小写字段名）
	filesChanged := 0
	additions := 0
	deletions := 0
	var fileList []string
	var diffFiles []map[string]interface{}
	_ = json.Unmarshal([]byte(task.DiffFilesJSON), &diffFiles)
	filesChanged = len(diffFiles)
	for _, df := range diffFiles {
		if add, ok := df["additions"].(float64); ok {
			additions += int(add)
		}
		if del, ok := df["deletions"].(float64); ok {
			deletions += int(del)
		}
		if newPath, ok := df["new_path"].(string); ok && newPath != "" {
			fileList = append(fileList, newPath)
		} else if oldPath, ok := df["old_path"].(string); ok && oldPath != "" {
			fileList = append(fileList, oldPath)
		}
	}

	// issue 摘要
	var totalIssues, high, medium, low int64
	model.DB.Model(&model.ReviewIssue{}).Where("task_id = ? AND deleted_at IS NULL", task.ID).Count(&totalIssues)
	model.DB.Model(&model.ReviewIssue{}).Where("task_id = ? AND deleted_at IS NULL AND severity = ?", task.ID, "high").Count(&high)
	model.DB.Model(&model.ReviewIssue{}).Where("task_id = ? AND deleted_at IS NULL AND severity = ?", task.ID, "medium").Count(&medium)
	model.DB.Model(&model.ReviewIssue{}).Where("task_id = ? AND deleted_at IS NULL AND severity = ?", task.ID, "low").Count(&low)

	var categories []string
	model.DB.Model(&model.ReviewIssue{}).
		Select("DISTINCT category").
		Where("task_id = ? AND deleted_at IS NULL AND category != ''", task.ID).
		Pluck("category", &categories)

	return map[string]interface{}{
		"id":               task.ID,
		"status":           string(task.Status),
		"mr_title":         task.MRTitle,
		"mr_iid":           task.MRMergeID,
		"author":           task.MRAuthor,
		"project_name":     projectName,
		"project_language": projectLanguage,
		"source_branch":    task.SourceBranch,
		"target_branch":    task.TargetBranch,
		"created_at":       task.CreatedAt.Format("2006-01-02 15:04"),
		"duration_sec":     task.DurationSec,
		"retry_count":      task.RetryCount,
		"diff_summary": map[string]interface{}{
			"files_changed": filesChanged,
			"additions":     additions,
			"deletions":     deletions,
			"file_list":     fileList,
		},
		"issue_summary": map[string]interface{}{
			"total_issues": totalIssues,
			"high":         high,
			"medium":       medium,
			"low":          low,
			"categories":   categories,
		},
	}
}

func MapPipeline(stage model.TaskPipelineExecution) map[string]interface{} {
	// 获取子节点
	var children []map[string]interface{}
	if len(stage.Children) > 0 {
		for _, child := range stage.Children {
			children = append(children, map[string]interface{}{
				"stage_code": child.StageCode,
				"status":     child.Status,
			})
		}
	}

	result := map[string]interface{}{
		"stage_code":  stage.StageCode,
		"status":      stage.Status,
		"duration_ms": stage.DurationMs,
	}
	if stage.DegradedReason != "" {
		result["llm_fallback_reason"] = stage.DegradedReason
	}
	if len(children) > 0 {
		result["children"] = children
	}
	return result
}
