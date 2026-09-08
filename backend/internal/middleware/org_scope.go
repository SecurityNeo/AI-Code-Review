package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// OrgScopeMiddleware 多租户组织范围中间件
// 在 Auth 之后执行，根据 Feature Flag 层数执行不同的 org_id 处理逻辑
func OrgScopeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Layer 0: 单组织模式，跳过所有多租户校验
		if !GlobalFeatureFlag.IsReadIsolationEnabled() {
			c.Next()
			return
		}

		// Layer 1+: 读隔离已启用
		// 1. 获取用户认证范围
		scope := GetAuthScope(c)
		if scope == nil {
			// 未认证路径（白名单）已在 Auth 中间件放行，此处不应到达
			c.Next()
			return
		}

		// 2. 如果用户无任何组织且非 super_admin，支持 Feature Flag 开关控制是否拦截
		if len(scope.VisibleOrgIDs) == 0 && !scope.IsSuperAdmin {
			if GlobalFeatureFlag.IsOrgMgmtEnabled() {
				// Layer 3+: 组织管理功能已启用，用户必须隶属某个组织
				c.JSON(http.StatusForbidden, gin.H{"error": "用户未分配组织，请联系管理员"})
				c.Abort()
				return
			}
			// Layer 1-2: 兼容模式，允许无组织用户访问（回退到根组织）
			c.Next()
			return
		}

		// 3. Layer 2+: 写拦截 - 检查写操作是否携带了有效的 org_id
		if GlobalFeatureFlag.IsWriteInterceptEnabled() {
			if isWriteOperation(c.Request.Method) {
				orgID := GetCurrentOrgID(c)
				if orgID == 0 {
					c.JSON(http.StatusBadRequest, gin.H{"error": "缺少组织 ID (X-Org-Id)"})
					c.Abort()
					return
				}
			}
		}

		c.Next()
	}
}

// isWriteOperation 判断 HTTP 方法是否为写操作
func isWriteOperation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
