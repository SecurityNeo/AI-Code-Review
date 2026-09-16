package handler

import (
	"strconv"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ApplyOrgScopeFilter 应用组织维度的过滤条件（含后代组织）。
//
// 语义：
//   - scope 为空：返回空结果集
//   - 非 super_admin：按 scope.VisibleOrgIDs 过滤（org_admin 的 VisibleOrgIDs 已含后代组织）
//   - super_admin：
//       显式传了 org_id → 按该组织子树过滤
//       未传 org_id → 不过滤（可见全部组织）
func ApplyOrgScopeFilter(c *gin.Context, db *gorm.DB, scope *model.UserAuthScope) *gorm.DB {
	if scope == nil {
		return db.Where("1 = 0")
	}
	if !scope.IsSuperAdmin {
		if len(scope.VisibleOrgIDs) == 0 {
			return db.Where("1 = 0")
		}
		return db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if orgIDStr := c.Query("org_id"); orgIDStr != "" {
		if id, err := strconv.ParseUint(orgIDStr, 10, 64); err == nil && id > 0 {
			ids, err := model.CollectOrgSubtreeIDs(uint(id))
			if err != nil || len(ids) == 0 {
				return db.Where("1 = 0")
			}
			return db.Where("org_id IN ?", ids)
		}
	}
	return db
}

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
