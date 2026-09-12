package handler

import (
	"github.com/ai-optimizer/backend/internal/model"
)

// CanAccessProject 检查用户是否有权访问指定项目
// 适用于在 service 层或 handler 层做二次权限校验
func CanAccessProject(scope *model.UserAuthScope, projectID uint) bool {
	if scope == nil || projectID == 0 {
		return false
	}
	if scope.IsSuperAdmin {
		return true
	}

	var project model.Project
	if err := model.DB.Where("id = ?", projectID).Select("org_id").First(&project).Error; err != nil {
		return false
	}

	for _, orgID := range scope.VisibleOrgIDs {
		if orgID == project.OrgID {
			return true
		}
	}
	return false
}

// CanAccessTask 检查用户是否有权访问指定任务
// 通过 task → project → org 进行归属链校验
func CanAccessTask(scope *model.UserAuthScope, taskID uint) bool {
	if scope == nil || taskID == 0 {
		return false
	}
	if scope.IsSuperAdmin {
		return true
	}

	var task model.Task
	if err := model.DB.Where("id = ?", taskID).Select("project_id").First(&task).Error; err != nil {
		return false
	}

	return CanAccessProject(scope, task.ProjectID)
}
