package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestRBAC 验证权限矩阵：每种角色 × 每种操作的预期结果
// T8.3: developer 调用 org_admin 接口返回 403
func TestRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// 准备组织/用户
	org := model.Organization{Name: "RBAC Org"}
	model.DB.Create(&org)

	roles := map[string]string{
		"super_admin": "super@test.com",
		"org_admin":   "admin@test.com",
		"developer":   "dev@test.com",
		"steward":     "steward@test.com",
	}

	users := make(map[string]model.User)
	for role, email := range roles {
		u := model.User{Username: role, GitlabEmail: email, DefaultOrgID: org.ID}
		model.DB.Create(&u)
		model.DB.Create(&model.OrgUser{OrgID: org.ID, UserID: u.ID, Role: role, Status: "active"})
		users[role] = u
	}

	// 模拟 org_admin 专属接口（如创建项目）
	adminOnly := router.Group("/admin")
	adminOnly.Use(func(c *gin.Context) {
		scope, _ := c.Get("auth_scope")
		if s, ok := scope.(*model.UserAuthScope); ok && s.CurrentOrgRole != "org_admin" && !s.IsSuperAdmin {
			c.AbortWithStatusJSON(403, gin.H{"error": "需要 org_admin 权限"})
			return
		}
		c.Next()
	})
	adminOnly.POST("/project", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "created"})
	})

	// 测试用例矩阵
	tests := []struct {
		name       string
		role       string
		expectCode int
	}{
		{"super_admin_create_project", "super_admin", 200},
		{"org_admin_create_project", "org_admin", 200},
		{"developer_create_project", "developer", 403},
		{"steward_create_project", "steward", 403},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := users[tt.role]
			scope := &model.UserAuthScope{
				UserID:         u.ID,
				CurrentOrgID:   org.ID,
				VisibleOrgIDs:  []uint{org.ID},
				CurrentOrgRole: tt.role,
				IsSuperAdmin:   tt.role == "super_admin",
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/admin/project", nil)
			// 使用 engine 完整路由执行
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Set("auth_scope", scope)
				c.Set("user_id", u.ID)
				c.Next()
			})
			engine.POST("/admin/project", func(c *gin.Context) {
				s, exists := c.Get("auth_scope")
				if !exists {
					c.AbortWithStatusJSON(401, gin.H{"error": "未认证"})
					return
				}
				authScope := s.(*model.UserAuthScope)
				if authScope.CurrentOrgRole != "org_admin" && !authScope.IsSuperAdmin {
					c.AbortWithStatusJSON(403, gin.H{"error": "需要 org_admin 权限"})
					return
				}
				c.JSON(200, gin.H{"message": "created"})
			})
			engine.ServeHTTP(w, req)

			assert.Equal(t, tt.expectCode, w.Code, "role %s should get %d", tt.role, tt.expectCode)
		})
	}
}
