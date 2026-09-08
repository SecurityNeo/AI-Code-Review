package middleware

import (
	"strconv"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
)

// GetCurrentOrgID 从 Header / Cookie / Query 获取 org_id
// 多租户改造：增加权限校验，防止用户设置不属于自己的 org_id
func GetCurrentOrgID(c *gin.Context) uint {
	orgIDStr := c.GetHeader("X-Org-Id")
	if orgIDStr == "" {
		orgIDStr = c.Query("org_id")
	}
	if orgIDStr == "" {
		orgIDStr, _ = c.Cookie("org_id")
	}

	var requestedOrgID uint
	if orgIDStr != "" {
		id, err := strconv.ParseUint(orgIDStr, 10, 64)
		if err != nil {
			return 0
		}
		requestedOrgID = uint(id)
	}

	scope := GetAuthScope(c)
	if scope == nil {
		return 0
	}

	// 如果用户请求了特定的 org_id，校验是否有权限访问
	if requestedOrgID > 0 {
		if scope.IsSuperAdmin {
			return requestedOrgID
		}
		for _, orgID := range scope.VisibleOrgIDs {
			if orgID == requestedOrgID {
				return requestedOrgID
			}
		}
		// 请求的 org_id 不在用户可见范围内，拒绝并返回默认 org
		return scope.CurrentOrgID
	}

	// 未指定 org_id，返回当前用户的默认 org
	return scope.CurrentOrgID
}

// GetAuthScope 从 gin context 获取 UserAuthScope
func GetAuthScope(c *gin.Context) *model.UserAuthScope {
	v, exists := c.Get("auth_scope")
	if !exists {
		return nil
	}
	scope, ok := v.(*model.UserAuthScope)
	if !ok {
		return nil
	}
	return scope
}

// GetCurrentOrgRole 从 gin context 获取当前组织角色
func GetCurrentOrgRole(c *gin.Context) string {
	scope := GetAuthScope(c)
	if scope == nil {
		return ""
	}
	return scope.CurrentOrgRole
}

// IsOrgAdmin 判断当前用户是否为组织管理员
func IsOrgAdmin(c *gin.Context) bool {
	scope := GetAuthScope(c)
	if scope == nil {
		return false
	}
	if scope.IsSuperAdmin {
		return true
	}
	return scope.CurrentOrgRole == "org_admin"
}

// parseTargetOrgID 安全解析目标组织ID（绝不用 ShouldBindJSON）
func parseTargetOrgID(c *gin.Context) uint {
	orgIDStr := c.GetHeader("X-Target-Org-Id")
	if orgIDStr == "" {
		orgIDStr = c.Query("target_org_id")
	}
	if orgIDStr == "" {
		orgIDStr, _ = c.Cookie("target_org_id")
	}
	if orgIDStr == "" {
		return 0
	}
	id, err := strconv.ParseUint(orgIDStr, 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}
