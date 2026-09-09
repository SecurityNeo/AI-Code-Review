package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
)

// ReviewAgentConfigHandler 智能体全局配置 Handler
type ReviewAgentConfigHandler struct {
	svc *service.ReviewAgentConfigService
}

func NewReviewAgentConfigHandler() *ReviewAgentConfigHandler {
	return &ReviewAgentConfigHandler{svc: service.NewReviewAgentConfigService()}
}

// Get 获取当前智能体全局配置
// GET /api/v1/review-agent-config
// 任何人可读（前端 Pipeline 展示需要）
func (h *ReviewAgentConfigHandler) Get(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	cfg := h.svc.GetOrDefault(scope, middleware.GetCurrentOrgID(c))

	// 将 JSON 字符串字段解析为前端友好的类型
	var stageConfigs map[string]interface{}
	if cfg.StageConfigs != "" && cfg.StageConfigs != "null" {
		_ = json.Unmarshal([]byte(cfg.StageConfigs), &stageConfigs)
	}

	// 运行依赖校验，让前端一进入配置页就能看到哪些卡片不满足依赖
	_, violations := cfg.ValidateEnabledStages()

	// 查询更新者名称（display_name > username）
	updatedByName := ""
	if cfg.UpdatedBy > 0 {
		var u model.User
		if err := model.DB.Where("id = ?", cfg.UpdatedBy).First(&u).Error; err == nil {
			updatedByName = u.DisplayName
			if updatedByName == "" {
				updatedByName = u.Username
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"data": map[string]interface{}{
		"id":                cfg.ID,
		"enabled_stages":    cfg.EnabledStageCodes(),
		"stage_configs":     stageConfigs,
		"trigger_events":    cfg.TriggerEventCodes(),
		"show_agent_status": cfg.ShowAgentStatus,
		"created_at":        cfg.CreatedAt,
		"updated_at":        cfg.UpdatedAt,
		"updated_by":        cfg.UpdatedBy,
		"updated_by_name":   updatedByName,
		"violations":        violations,
	}})
}

// Save 保存智能体全局配置
// PUT /api/v1/review-agent-config
// 仅 admin 可写
func (h *ReviewAgentConfigHandler) Save(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	var req struct {
		EnabledStages   []string               `json:"enabled_stages" binding:"required"`
		StageConfigs    map[string]interface{} `json:"stage_configs"`
		TriggerEvents   []string               `json:"trigger_events"`
		ShowAgentStatus bool                   `json:"show_agent_status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var userID uint
	if uid, ok := c.Get("user_id"); ok {
		if id, ok := uid.(uint); ok {
			userID = id
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "无法获取用户信息"})
			return
		}
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	if err := h.svc.Save(scope, middleware.GetCurrentOrgID(c), req.EnabledStages, req.StageConfigs, req.TriggerEvents, req.ShowAgentStatus, userID); err != nil {
		// 依赖校验失败时返回 422 + 结构化 violations，方便前端精确定位标红卡片
		var depErr *service.DependencyViolationError
		if errors.As(err, &depErr) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":      "dependency_violation",
				"violations": depErr.Violations,
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// GetFileFilterDefaults 获取文件过滤默认规则
// GET /api/v1/review-agent-config/file-filter-defaults
// 任意登录用户可读（默认规则是只读系统配置）
func (h *ReviewAgentConfigHandler) GetFileFilterDefaults(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok { return }
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"languages": engine.DefaultExcludePatternsByLanguage,
			"common":    engine.CommonExcludePatterns,
		},
	})
}
