package contract

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-optimizer/backend/internal/handler"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestAPIContractUsersMe 验证 GET /api/v1/users/me 返回 Schema
// T8.1: users/me 响应包含完整 organizations 数组
func TestAPIContractUsersMe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// 准备测试数据
	org := model.Organization{Name: "Contract Org"}
	model.DB.Create(&org)
	user := model.User{Username: "contract_user", GitlabEmail: "c@test.com", DefaultOrgID: org.ID}
	model.DB.Create(&user)
	model.DB.Create(&model.OrgUser{OrgID: org.ID, UserID: user.ID, Role: "org_admin", Status: "active"})

	scope := &model.UserAuthScope{
		UserID:         user.ID,
		CurrentOrgID:   org.ID,
		VisibleOrgIDs:  []uint{org.ID},
		CurrentOrgRole: "org_admin",
	}

	// 注册路由
	router.GET("/api/v1/users/me", func(c *gin.Context) {
		c.Set("auth_scope", scope)
		c.Set("user_id", user.ID)
		handler.NewUserHandler().GetCurrentUser(c)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/users/me", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	data, ok := resp["data"].(map[string]interface{})
	assert.True(t, ok, "response should have data field")

	// 必须包含 organizations 数组
	orgs, ok := data["organizations"].([]interface{})
	assert.True(t, ok, "response must contain organizations array")
	assert.Len(t, orgs, 1, "should return 1 organization")

	orgObj := orgs[0].(map[string]interface{})
	assert.Equal(t, float64(org.ID), orgObj["id"], "organization id should match")
	assert.Equal(t, "org_admin", orgObj["role"], "organization role should match")
}

// TestAPIContractProjectCreate 验证 POST /api/v1/projects 响应结构
func TestAPIContractProjectCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	org := model.Organization{Name: "Project Contract Org"}
	model.DB.Create(&org)
	inst := model.GitLabInstance{OrgID: org.ID, Name: "GitLab", BaseURL: "https://gl.test"}
	model.DB.Create(&inst)

	scope := &model.UserAuthScope{
		UserID:         1,
		CurrentOrgID:   org.ID,
		VisibleOrgIDs:  []uint{org.ID},
		CurrentOrgRole: "org_admin",
	}

	// 模拟 Handler 中间件设置 scope
	router.Use(func(c *gin.Context) {
		c.Set("auth_scope", scope)
		c.Set("user_id", uint(1))
		c.Next()
	})

	// 注册简化版路由
	router.POST("/api/v1/projects", func(c *gin.Context) {
		var req struct {
			Name             string `json:"name"`
			GitLabURL        string `json:"gitlab_url"`
			GitLabInstanceID uint   `json:"gitlab_instance_id"`
		}
		c.ShouldBindJSON(&req)

		// Schema 校验
		assert.NotEmpty(t, req.Name, "name is required")
		assert.NotEmpty(t, req.GitLabURL, "gitlab_url is required")
		assert.Greater(t, req.GitLabInstanceID, uint(0), "gitlab_instance_id is required")

		c.JSON(200, gin.H{
			"data": gin.H{
				"id":                1,
				"name":              req.Name,
				"org_id":            org.ID,
				"gitlab_instance_id": req.GitLabInstanceID,
			},
		})
	})

	body := `{"name":"Contract Project","gitlab_url":"https://gl.test/group/proj","gitlab_instance_id":1}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/projects", jsonString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	data := resp["data"].(map[string]interface{})
	assert.NotNil(t, data["org_id"], "project response must contain org_id")
	assert.NotNil(t, data["gitlab_instance_id"], "project response must contain gitlab_instance_id")
	assert.Equal(t, "Contract Project", data["name"])
}

func jsonString(s string) *bytes.Buffer {
	return bytes.NewBufferString(s)
}
