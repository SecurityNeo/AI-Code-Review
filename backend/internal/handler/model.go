package handler

import (
	"fmt"
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ModelHandler struct {
	service *service.ModelService
}

func NewModelHandler() *ModelHandler {
	return &ModelHandler{
		service: service.NewModelService(),
	}
}

// List 获取模型列表
// GET /api/v1/models?page=1&page_size=20&keyword=gpt&type=llm
// 多租户改造：叠加 OrgScope 过滤
func (h *ModelHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := c.Query("keyword")
	modelType := c.Query("type")

	var models []model.LLMModel
	var total int64
	var err error
	if modelType != "" {
		models, total, err = h.service.ListByType(scope, page, pageSize, keyword, modelType)
	} else {
		models, total, err = h.service.List(scope, page, pageSize, keyword)
	}
	if err != nil {
		zap.L().Error("list models failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：非super_admin只返回当前组织可见的模型
	if !scope.IsSuperAdmin {
		var filtered []model.LLMModel
		for _, m := range models {
			for _, orgID := range scope.VisibleOrgIDs {
				if m.OrgID == orgID || m.IsShared {
					filtered = append(filtered, m)
					break
				}
			}
		}
		models = filtered
	}

	// 清理敏感字段
	for i := range models {
		models[i].APIKey = ""
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(models))
	for _, m := range models {
		orgIDs = append(orgIDs, m.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range models {
		models[i].OrgName = orgNameMap[models[i].OrgID]
	}

	c.JSON(200, gin.H{
		"data":      models,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// Get 获取模型详情
// GET /api/v1/models/:id
// 多租户改造：校验模型是否属于当前组织或共享
func (h *ModelHandler) Get(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.Get(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		zap.L().Error("get model failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：非super_admin需校验org_id或is_shared
	if !scope.IsSuperAdmin {
		found := false
		for _, orgID := range scope.VisibleOrgIDs {
			if m.OrgID == orgID || m.IsShared {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权访问该模型"})
			return
		}
	}

	// 清理敏感字段
	m.APIKey = ""

	c.JSON(200, gin.H{"data": m})
}

// GetForUpdate 获取模型编辑数据（包含原始 API Key）
// GET /api/v1/models/:id/edit
func (h *ModelHandler) GetForUpdate(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.GetForUpdate(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		zap.L().Error("get model for update failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"data": m})
}

// Create 创建模型
// POST /api/v1/models
func (h *ModelHandler) Create(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var req service.CreateModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	// 多租户改造：注入 org_id（非 super_admin 禁止客户端传入）
	if req.OrgID == 0 || !scope.IsSuperAdmin {
		req.OrgID = middleware.GetCurrentOrgID(c)
	}

	// Normalize provider
	req.Provider = service.NormalizeProvider(req.Provider)

	// Set defaults
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}
	if req.TimeoutSec == 0 {
		req.TimeoutSec = 120
	}
	if req.Temperature == 0 {
		req.Temperature = 0.1
	}
	if req.ModelType == "" {
		req.ModelType = "llm"
	}

	llmModel, err := h.service.Create(scope, &req)
	if err != nil {
		if err == service.ErrModelExists {
			c.JSON(400, gin.H{"error": "该提供商下已存在相同模型ID"})
			return
		}
		zap.L().Error("create model failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("模型创建", fmt.Sprintf("%s-%s", llmModel.Provider, llmModel.ModelID), llmModel.ID, userID.(uint), "success", "", c.ClientIP())

	c.JSON(201, gin.H{
		"message": "模型创建成功",
		"data":    llmModel,
	})
}

// Update 更新模型
// PUT /api/v1/models/:id
func (h *ModelHandler) Update(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.Get(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：校验模型归属
	if !scope.IsSuperAdmin {
		found := false
		for _, orgID := range scope.VisibleOrgIDs {
			if m.OrgID == orgID || m.IsShared {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权访问该模型"})
			return
		}
	}

	var req service.UpdateModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	err = h.service.Update(scope, uint(id), &req)
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		if err == service.ErrModelExists {
			c.JSON(400, gin.H{"error": "该提供商下已存在相同模型ID"})
			return
		}
		if err == service.ErrBackupOrderConflict {
			c.JSON(400, gin.H{"error": "该备用顺序已被其他模型占用，请选择其他顺序"})
			return
		}
		zap.L().Error("update model failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("模型更新", fmt.Sprintf("%s-%s", m.Provider, m.ModelID), uint(id), userID.(uint), "success", "", c.ClientIP())

	c.JSON(200, gin.H{"message": "模型更新成功"})
}

// Delete 删除模型
// DELETE /api/v1/models/:id
func (h *ModelHandler) Delete(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "模型不存在"})
		return
	}

	// 多租户改造：校验模型归属
	if !scope.IsSuperAdmin {
		found := false
		for _, orgID := range scope.VisibleOrgIDs {
			if m.OrgID == orgID || m.IsShared {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权访问该模型"})
			return
		}
	}

	err = h.service.Delete(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		if err == service.ErrCannotDeleteDefault {
			c.JSON(400, gin.H{"error": "不能删除默认模型"})
			return
		}
		zap.L().Error("delete model failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("模型删除", fmt.Sprintf("%s-%s", m.Provider, m.ModelID), uint(id), userID.(uint), "success", "", c.ClientIP())

	c.JSON(200, gin.H{"message": "模型删除成功"})
}

// SetDefault 设为默认模型
// PUT /api/v1/models/:id/default
func (h *ModelHandler) SetDefault(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.Get(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	err = h.service.SetDefault(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		zap.L().Error("set default model failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("设为默认模型", fmt.Sprintf("%s-%s", m.Provider, m.ModelID), uint(id), userID.(uint), "success", "", c.ClientIP())

	c.JSON(200, gin.H{"message": "已设为默认模型"})
}

// UnsetDefault 取消默认模型
// DELETE /api/v1/models/:id/default
func (h *ModelHandler) UnsetDefault(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.Get(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	err = h.service.UnsetDefault(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		zap.L().Error("unset default model failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("取消默认模型", fmt.Sprintf("%s-%s", m.Provider, m.ModelID), uint(id), userID.(uint), "success", "", c.ClientIP())

	c.JSON(200, gin.H{"message": "已取消默认模型"})
}

// CheckAPI 测试 API 连通性
// POST /api/v1/models/:id/check
func (h *ModelHandler) CheckAPI(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	success, err := h.service.CheckConnectivity(scope, uint(id))
	if err != nil {
		zap.L().Error("model API check failed",
			zap.Uint("id", uint(id)),
			zap.Error(err))
		c.JSON(200, gin.H{
			"success": false,
			"message": "API 连接失败: " + err.Error(),
		})
		return
	}

	c.JSON(200, gin.H{
		"success": success,
		"message": "API 连接成功",
	})
}

// Disable 禁用模型
// PUT /api/v1/models/:id/disable
func (h *ModelHandler) Disable(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.Get(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	err = h.service.Disable(scope, uint(id))
	if err != nil {
		if err == service.ErrCannotDisablePrimary {
			c.JSON(400, gin.H{"error": "不能禁用主模型，请先取消主模型后再禁用"})
			return
		}
		if err == service.ErrCannotDisableDefault {
			c.JSON(400, gin.H{"error": "不能禁用默认模型，请先取消默认后再禁用"})
			return
		}
		zap.L().Error("disable model failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("模型禁用", fmt.Sprintf("%s-%s", m.Provider, m.ModelID), uint(id), userID.(uint), "success", "", c.ClientIP())

	c.JSON(200, gin.H{"message": "模型已禁用"})
}

// Enable 启用模型
// PUT /api/v1/models/:id/enable
func (h *ModelHandler) Enable(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的模型ID"})
		return
	}

	m, err := h.service.Get(scope, uint(id))
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "模型不存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	err = h.service.Enable(scope, uint(id))
	if err != nil {
		zap.L().Error("enable model failed", zap.Error(err), zap.Uint("id", uint(id)))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	model.RecordOpLog("模型启用", fmt.Sprintf("%s-%s", m.Provider, m.ModelID), uint(id), userID.(uint), "success", "", c.ClientIP())

	c.JSON(200, gin.H{"message": "模型已启用"})
}

// GetDefault 获取默认模型
// GET /api/v1/models/default
func (h *ModelHandler) GetDefault(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	model, err := h.service.GetDefault(scope)
	if err != nil {
		if err == service.ErrModelNotFound {
			c.JSON(404, gin.H{"error": "未设置默认模型"})
			return
		}
		zap.L().Error("get default model failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"data": model})
}

// CreateTest 测试请求（不保存）
// POST /api/v1/models/test
func (h *ModelHandler) CreateTest(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	var req struct {
		ModelType   string  `json:"model_type"`
		Provider    string  `json:"provider" binding:"required"`
		ModelID     string  `json:"model_id" binding:"required"`
		BaseURL     string  `json:"base_url" binding:"required"`
		APIKey      string  `json:"api_key" binding:"required"`
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"max_tokens"`
		Prompt      string  `json:"prompt"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	req.Provider = service.NormalizeProvider(req.Provider)

	timeout := 120

	var success bool
	var err error
	if req.ModelType == "embedding" {
		success, err = h.service.CheckEmbeddingConnectivityByConfig(req.BaseURL, req.APIKey, req.ModelID, timeout)
	} else {
		if req.MaxTokens == 0 {
			req.MaxTokens = 1024
		}
		if req.Temperature == 0 {
			req.Temperature = 0.1
		}
		success, err = h.service.CheckConnectivityByConfig(req.Provider, req.BaseURL, req.APIKey, req.ModelID, timeout)
	}

	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": "API 连接失败: " + err.Error(),
		})
		return
	}

	c.JSON(200, gin.H{
		"success": success,
		"message": "API 连接成功",
	})
}
