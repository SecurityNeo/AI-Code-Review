package handler

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type GitLabAuthHandler struct {
	oauthSvc *service.GitLabOAuthService
}

func NewGitLabAuthHandler() *GitLabAuthHandler {
	return &GitLabAuthHandler{
		oauthSvc: service.NewGitLabOAuthService(),
	}
}

// ListInstances GET /api/v1/auth/gitlab/instances
func (h *GitLabAuthHandler) ListInstances(c *gin.Context) {
	var instances []model.GitLabInstance
	if err := model.DB.Where("status = ?", "active").Find(&instances).Error; err != nil {
		zap.L().Error("list gitlab instances failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "获取 GitLab 实例列表失败"})
		return
	}

	type publicInstance struct {
		ID        uint   `json:"id"`
		Name      string `json:"name"`
		BaseURL   string `json:"base_url"`
		IsDefault bool   `json:"is_default"`
	}

	result := make([]publicInstance, 0, len(instances))
	for _, inst := range instances {
		result = append(result, publicInstance{
			ID:        inst.ID,
			Name:      inst.Name,
			BaseURL:   inst.BaseURL,
			IsDefault: inst.IsDefault,
		})
	}

	c.JSON(200, gin.H{"data": result})
}

// Redirect GET /api/v1/auth/gitlab
func (h *GitLabAuthHandler) Redirect(c *gin.Context) {
	instanceID, err := resolveInstanceID(c)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	state := service.GenerateSelfContainedState(instanceID)
	authURL, err := h.oauthSvc.BuildAuthURL(state, instanceID)
	if err != nil {
		c.JSON(400, gin.H{"error": "GitLab OAuth 未启用或配置不完整: " + err.Error()})
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// Callback GET /api/v1/auth/gitlab/callback
func (h *GitLabAuthHandler) Callback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")

	if code == "" || state == "" {
		c.JSON(400, gin.H{"error": "缺少 code 或 state"})
		return
	}

	instanceID, ok := service.ParseSelfContainedState(state)
	if !ok {
		c.JSON(403, gin.H{"error": "state 无效或已过期"})
		return
	}

	accessToken, err := h.oauthSvc.ExchangeCode(code, instanceID)
	if err != nil {
		zap.L().Error("gitlab oauth exchange code failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "GitLab 认证失败: " + err.Error()})
		return
	}

	userInfo, err := h.oauthSvc.GetUserInfo(accessToken, instanceID)
	if err != nil {
		zap.L().Error("gitlab get user info failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "获取用户信息失败: " + err.Error()})
		return
	}

	user, _, err := h.oauthSvc.FindOrCreateUser(userInfo, instanceID)
	if err != nil {
		// 区分"未启用"和"用户不存在"两种错误
		errMsg := err.Error()
		status := 403
		if errMsg == "gitlab oauth not enabled" {
			errMsg = "GitLab OAuth 未启用"
		}
		c.JSON(status, gin.H{"error": errMsg})
		return
	}

	token, err := middleware.GenerateToken(user.ID, user.Username)
	if err != nil {
		zap.L().Error("generate token failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "登录失败，请重试"})
		return
	}

	zap.L().Info("gitlab login success",
		zap.String("username", user.Username),
		zap.String("gitlab_username", user.GitlabUsername),
		zap.Uint("gitlab_instance_id", instanceID),
	)

	// 前端 auth.js 已支持从 URL 参数 token 读取并写入 localStorage，
	// 同时保留 SetCookie 供后端 API 中间件使用
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("auth_token", token, 7*24*3600, "/", "", true, true)
	c.Redirect(http.StatusFound, "/?token="+token)
}

// resolveInstanceID 从请求参数解析实例ID，未指定时查找默认实例，fallback 到 1
func resolveInstanceID(c *gin.Context) (uint, error) {
	idStr := c.Query("instance_id")
	if idStr != "" {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("无效的 instance_id")
		}
		return uint(id), nil
	}

	// 未指定时查找默认实例
	var inst model.GitLabInstance
	if err := model.DB.Where("is_default = ? AND status = ?", true, "active").First(&inst).Error; err == nil {
		return inst.ID, nil
	}

	// fallback 到兼容的旧实例
	return 1, nil
}

// ========== GitLab 实例管理（管理员接口） ==========

// ListInstancesAdmin GET /api/v1/gitlab-instances
func (h *GitLabAuthHandler) ListInstancesAdmin(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	db := model.DB.Model(&model.GitLabInstance{}).Order("id DESC")
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}

	var instances []model.GitLabInstance
	if err := db.Find(&instances).Error; err != nil {
		zap.L().Error("list gitlab instances admin failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "查询失败"})
		return
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(instances))
	for _, inst := range instances {
		orgIDs = append(orgIDs, inst.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)

	// 清理敏感字段后返回
	result := make([]gin.H, 0, len(instances))
	for _, inst := range instances {
		result = append(result, gin.H{
			"id":                      inst.ID,
			"org_id":                  inst.OrgID,
			"org_name":                orgNameMap[inst.OrgID],
			"name":                    inst.Name,
			"base_url":                inst.BaseURL,
			"is_default":              inst.IsDefault,
			"status":                  inst.Status,
			"oauth_enabled":           inst.OAuthEnabled,
			"oauth_client_id":         inst.OAuthClientID,
			"oauth_redirect_uri":      inst.OAuthRedirectURI,
			"oauth_auto_create_user":  inst.OAuthAutoCreateUser,
			"oauth_skip_verify":       inst.OAuthSkipVerify,
			"last_sync_at":            inst.LastSyncAt,
			"sync_error":              inst.SyncError,
			"created_at":              inst.CreatedAt,
			"updated_at":              inst.UpdatedAt,
		})
	}
	c.JSON(200, gin.H{"data": result})
}

// CreateInstance POST /api/v1/gitlab-instances
func (h *GitLabAuthHandler) CreateInstance(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var req struct {
		Name                string `json:"name" binding:"required"`
		BaseURL             string `json:"base_url" binding:"required"`
		APIToken            string `json:"api_token"`
		WebhookSecret       string `json:"webhook_secret"`
		OrgID               uint   `json:"org_id"`
		IsDefault           bool   `json:"is_default"`
		OAuthEnabled        bool   `json:"oauth_enabled"`
		OAuthClientID       string `json:"oauth_client_id"`
		OAuthClientSecret   string `json:"oauth_client_secret"`
		OAuthRedirectURI    string `json:"oauth_redirect_uri"`
		OAuthAutoCreateUser bool   `json:"oauth_auto_create_user"`
		OAuthSkipVerify     bool   `json:"oauth_skip_verify"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	orgID := req.OrgID
	if orgID == 0 || !scope.IsSuperAdmin {
		orgID = middleware.GetCurrentOrgID(c)
	}

	inst := model.GitLabInstance{
		OrgID:               orgID,
		Name:                req.Name,
		BaseURL:             req.BaseURL,
		WebhookSecret:       req.WebhookSecret,
		IsDefault:           req.IsDefault,
		Status:              "active",
		OAuthEnabled:        req.OAuthEnabled,
		OAuthClientID:       req.OAuthClientID,
		OAuthClientSecret:   req.OAuthClientSecret,
		OAuthRedirectURI:    req.OAuthRedirectURI,
		OAuthAutoCreateUser: req.OAuthAutoCreateUser,
		OAuthSkipVerify:     req.OAuthSkipVerify,
	}
	if req.APIToken != "" {
		if err := inst.SetAPIToken(req.APIToken); err != nil {
			zap.L().Error("set api token failed", zap.Error(err))
		}
	}
	// 如果设为默认，取消其他实例的默认状态
	if inst.IsDefault {
		model.DB.Model(&model.GitLabInstance{}).Where("org_id = ?", orgID).Update("is_default", false)
	}
	if err := model.DB.Create(&inst).Error; err != nil {
		zap.L().Error("create gitlab instance failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "创建失败: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"data": inst.ID, "message": "创建成功"})
}

// UpdateInstance PUT /api/v1/gitlab-instances/:id
func (h *GitLabAuthHandler) UpdateInstance(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var inst model.GitLabInstance
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := db.First(&inst, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "实例不存在"})
		return
	}

	var req struct {
		Name                string `json:"name"`
		BaseURL             string `json:"base_url"`
		APIToken            string `json:"api_token"`
		WebhookSecret       string `json:"webhook_secret"`
		OrgID               uint   `json:"org_id"`
		IsDefault           *bool  `json:"is_default"`
		Status              string `json:"status"`
		OAuthEnabled        *bool  `json:"oauth_enabled"`
		OAuthClientID       string `json:"oauth_client_id"`
		OAuthClientSecret   string `json:"oauth_client_secret"`
		OAuthRedirectURI    string `json:"oauth_redirect_uri"`
		OAuthAutoCreateUser *bool  `json:"oauth_auto_create_user"`
		OAuthSkipVerify     *bool  `json:"oauth_skip_verify"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" { updates["name"] = req.Name }
	if req.BaseURL != "" { updates["base_url"] = req.BaseURL }
	if req.WebhookSecret != "" { updates["webhook_secret"] = req.WebhookSecret }
	if req.Status != "" { updates["status"] = req.Status }
	if req.OAuthClientID != "" { updates["oauth_client_id"] = req.OAuthClientID }
	if req.OAuthClientSecret != "" { updates["oauth_client_secret"] = req.OAuthClientSecret }
	if req.OAuthRedirectURI != "" { updates["oauth_redirect_uri"] = req.OAuthRedirectURI }
	if req.OAuthEnabled != nil { updates["oauth_enabled"] = *req.OAuthEnabled }
	if req.OAuthAutoCreateUser != nil { updates["oauth_auto_create_user"] = *req.OAuthAutoCreateUser }
	if req.OAuthSkipVerify != nil { updates["oauth_skip_verify"] = *req.OAuthSkipVerify }

	// super_admin 可修改 org_id
	if req.OrgID > 0 && scope.IsSuperAdmin {
		updates["org_id"] = req.OrgID
	}

	if req.APIToken != "" {
		if err := inst.SetAPIToken(req.APIToken); err != nil {
			zap.L().Error("set api token failed", zap.Error(err))
		} else {
			updates["api_token_enc"] = inst.APITokenEnc
		}
	}

	if req.IsDefault != nil && *req.IsDefault {
		updates["is_default"] = true
		orgID := inst.OrgID
		if req.OrgID > 0 && scope.IsSuperAdmin {
			orgID = req.OrgID
		}
		model.DB.Model(&model.GitLabInstance{}).Where("org_id = ? AND id != ?", orgID, id).Update("is_default", false)
	} else if req.IsDefault != nil && !*req.IsDefault {
		updates["is_default"] = false
	}

	if err := model.DB.Model(&inst).Updates(updates).Error; err != nil {
		zap.L().Error("update gitlab instance failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "更新失败"})
		return
	}
	c.JSON(200, gin.H{"message": "更新成功"})
}

// DeleteInstance DELETE /api/v1/gitlab-instances/:id
func (h *GitLabAuthHandler) DeleteInstance(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var inst model.GitLabInstance
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := db.First(&inst, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "实例不存在"})
		return
	}

	// 检查是否有项目依赖此实例
	var count int64
	model.DB.Model(&model.Project{}).Where("gitlab_instance_id = ?", id).Count(&count)
	if count > 0 {
		c.JSON(400, gin.H{"error": fmt.Sprintf("该实例被 %d 个项目引用，无法删除", count)})
		return
	}

	// 级联删除 GitLab 用户绑定记录
	if err := model.DB.Where("gitlab_instance_id = ?", id).Delete(&model.GitLabUserBinding{}).Error; err != nil {
		zap.L().Warn("delete gitlab user bindings failed", zap.Uint("instance_id", uint(id)), zap.Error(err))
	}

	if err := model.DB.Delete(&inst).Error; err != nil {
		zap.L().Error("delete gitlab instance failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(200, gin.H{"message": "删除成功"})
}

// TestInstance POST /api/v1/gitlab-instances/:id/test
func (h *GitLabAuthHandler) TestInstance(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var inst model.GitLabInstance
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := db.First(&inst, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "实例不存在"})
		return
	}

	// 简单验证 base_url 可达（/api/v4/version）
	// 内部网络场景下跳过 TLS 证书校验，确保测试连接可用性
	client := &http.Client{Timeout: 10 * time.Second}
	client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	resp, err := client.Get(inst.BaseURL + "/api/v4/version")
	if err != nil {
		c.JSON(200, gin.H{"success": false, "error": "连接失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 || resp.StatusCode == 401 {
		c.JSON(200, gin.H{"success": true, "message": "连接正常"})
	} else {
		c.JSON(200, gin.H{"success": false, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)})
	}
}
