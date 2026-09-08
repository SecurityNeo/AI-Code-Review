package handler

import (
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// extractOrgIDFromMap 从 map[string]interface{} 中安全提取 org_id（支持 float64/int/string）
func extractOrgIDFromMap(data map[string]interface{}) (uint, bool) {
	v, ok := data["org_id"]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return uint(val), true
	case int:
		return uint(val), true
	case int64:
		return uint(val), true
	case uint:
		return val, true
	case string:
		if id, err := strconv.Atoi(val); err == nil {
			return uint(id), true
		}
	}
	return 0, false
}

// ResolveOrgID 统一解析创建/更新请求中的 org_id。
// 原则：
//   - super_admin 可以显式指定任意 org_id（0 表示沿用 scope.CurrentOrgID）
//   - org_admin / developer 强制绑定到当前组织，忽略客户端传入值
// 适用场景：所有在 orgAdmin 路由组下的 Create / Update handler。
func ResolveOrgID(c *gin.Context, requested uint) uint {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		return requested
	}
	if scope.IsSuperAdmin {
		if requested > 0 {
			return requested
		}
		return scope.CurrentOrgID
	}
	return scope.CurrentOrgID
}

// RequireOrgID 强制绑定到当前组织（无视客户端 org_id），返回 gin.H 错误时停止处理。
// 适用于没有独立 scope 但需要统一注入 org_id 的 handler。
func RequireOrgID(c *gin.Context) (uint, bool) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return 0, false
	}
	return scope.CurrentOrgID, true
}
