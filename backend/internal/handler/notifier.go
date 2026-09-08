package handler

import (
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type NotifierHandler struct{}

func NewNotifierHandler() *NotifierHandler {
	return &NotifierHandler{}
}

func (h *NotifierHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	notifiers, err := service.NewNotifierService().List(scope)
	if err != nil {
		zap.L().Error("list notifiers failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	type NotifierResponse struct {
		ID             uint   `json:"id"`
		Name           string `json:"name"`
		WebhookUrl     string `json:"webhook_url"`
		HasTemplate    bool   `json:"has_template"`
		ProjectID      *uint  `json:"project_id"`
		ProjectName    string `json:"project_name"`
		OrgID          uint   `json:"org_id"`
		OrgName        string `json:"org_name"`
		Enabled        bool   `json:"enabled"`
		LastTestStatus string `json:"last_test_status"`
		CreatedAt      string `json:"created_at"`
	}

	// 批量查询项目名称，避免 N+1
	projectIDs := make(map[uint]struct{})
	for _, n := range notifiers {
		if n.ProjectID != nil && *n.ProjectID > 0 {
			projectIDs[*n.ProjectID] = struct{}{}
		}
	}
	projectMap := make(map[uint]string)
	if len(projectIDs) > 0 {
		ids := make([]uint, 0, len(projectIDs))
		for id := range projectIDs {
			ids = append(ids, id)
		}
		var projects []model.Project
		if err := model.DB.Where("id IN ?", ids).Find(&projects).Error; err == nil {
			for _, p := range projects {
				projectMap[p.ID] = p.Name
			}
		}
	}

	// 批量查询组织名称
	orgIDs := make([]uint, 0, len(notifiers))
	for _, n := range notifiers {
		orgIDs = append(orgIDs, n.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)

	result := make([]NotifierResponse, 0, len(notifiers))
	for _, n := range notifiers {
		projectName := "全局"
		if n.ProjectID != nil && *n.ProjectID > 0 {
			if name, ok := projectMap[*n.ProjectID]; ok && name != "" {
				projectName = name
			} else {
				projectName = "未知项目"
			}
		}
		result = append(result, NotifierResponse{
			ID:             n.ID,
			Name:           n.Name,
			WebhookUrl:     n.WebhookUrl,
			HasTemplate:    n.MessageTemplate != "",
			ProjectID:      n.ProjectID,
			ProjectName:    projectName,
			OrgID:          n.OrgID,
			OrgName:        orgNameMap[n.OrgID],
			Enabled:        n.Enabled,
			LastTestStatus: n.LastTestStatus,
			CreatedAt:      n.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(200, gin.H{"data": result})
}

func (h *NotifierHandler) Get(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	notifier, err := service.NewNotifierService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	c.JSON(200, gin.H{
		"data": map[string]interface{}{
			"id":               notifier.ID,
			"name":             notifier.Name,
			"webhook_url":      notifier.WebhookUrl,
			"has_template":     notifier.MessageTemplate != "",
			"message_template": notifier.MessageTemplate,
			"project_id":       notifier.ProjectID,
			"enabled":          notifier.Enabled,
		},
	})
}

func (h *NotifierHandler) Create(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	required := []string{"name", "webhook_url"}
	for _, key := range required {
		if _, ok := data[key]; !ok {
			c.JSON(400, gin.H{"error": "missing field: " + key})
			return
		}
	}

	
	// 多租户改造：注入当前 org_id（非 super_admin 禁止客户端传入）
	if _, ok := data["org_id"]; !ok || !scope.IsSuperAdmin {
		data["org_id"] = middleware.GetCurrentOrgID(c)
	}
	notifier, err := service.NewNotifierService().Create(scope, data)
	if err != nil {
		zap.L().Error("create notifier failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("通知配置创建", notifier.Name, notifier.ID, userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "created", "data": map[string]interface{}{"id": notifier.ID}})
}

func (h *NotifierHandler) Update(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 如果 webhook_url 为空，不更新
	if url, ok := data["webhook_url"].(string); ok && url == "" {
		delete(data, "webhook_url")
	}

	// 1. 非 super_admin 禁止修改 org_id
	if !scope.IsSuperAdmin {
		delete(data, "org_id")
	}

	// 2. super_admin 可以修改 org_id：用 UpdateColumn 绕过 GORM hook
	if scope.IsSuperAdmin {
		if orgID, ok := extractOrgIDFromMap(data); ok {
			zap.L().Info("notifier update: changing org_id", zap.Int("id", id), zap.Uint("new_org_id", orgID))
			if err := model.DB.Model(&model.WeComNotifier{}).Where("id = ?", id).UpdateColumn("org_id", orgID).Error; err != nil {
				zap.L().Error("notifier update: org_id update failed", zap.Int("id", id), zap.Uint("org_id", orgID), zap.Error(err))
				c.JSON(500, gin.H{"error": "组织归属更新失败: " + err.Error()})
				return
			}
			delete(data, "org_id")
		}
	}

	err := service.NewNotifierService().Update(scope, uint(id), data)
	if err != nil {
		zap.L().Error("update notifier failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("通知配置更新", strconv.Itoa(id), uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "updated"})
}

// UpdateTemplate 更新消息模板
func (h *NotifierHandler) UpdateTemplate(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	template, ok := data["message_template"].(string)
	if !ok {
		c.JSON(400, gin.H{"error": "missing field: message_template"})
		return
	}

	err := service.NewNotifierService().UpdateTemplate(scope, uint(id), template)
	if err != nil {
		zap.L().Error("update template failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("通知模板更新", strconv.Itoa(id), uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "updated"})
}

func (h *NotifierHandler) Delete(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))

	notifier, _ := service.NewNotifierService().Get(scope, uint(id))
	err := service.NewNotifierService().Delete(scope, uint(id))
	if err != nil {
		zap.L().Error("delete notifier failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("通知配置删除", notifier.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "deleted"})
}

func (h *NotifierHandler) Test(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))

	success, msg, err := service.NewNotifierService().Test(scope, uint(id))
	if err != nil {
		zap.L().Error("test notifier failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	if success {
		c.JSON(200, gin.H{"message": "测试消息发送成功", "status": "success"})
	} else {
		c.JSON(400, gin.H{"error": "发送失败: " + msg})
	}
}

func (h *NotifierHandler) Toggle(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	enabled := true
	if v, ok := data["enabled"].(bool); ok {
		enabled = v
	}

	err := service.NewNotifierService().Toggle(scope, uint(id), enabled)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "updated", "enabled": enabled})
}
