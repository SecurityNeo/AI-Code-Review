package handler

import (
	"encoding/json"
	"net/http"

	"github.com/ai-optimizer/backend/internal/engine"
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
	cfg := h.svc.GetOrDefault()

	// 将 JSON 字符串字段解析为前端友好的类型
	var stageConfigs map[string]interface{}
	if cfg.StageConfigs != "" && cfg.StageConfigs != "null" {
		_ = json.Unmarshal([]byte(cfg.StageConfigs), &stageConfigs)
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
	}})
}

// Save 保存智能体全局配置
// PUT /api/v1/review-agent-config
// 仅 admin 可写
func (h *ReviewAgentConfigHandler) Save(c *gin.Context) {
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

	if err := h.svc.Save(req.EnabledStages, req.StageConfigs, req.TriggerEvents, req.ShowAgentStatus, userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// GetFileFilterDefaults 获取文件过滤默认规则
// GET /api/v1/review-agent-config/file-filter-defaults
// 任意登录用户可读（默认规则是只读系统配置）
func (h *ReviewAgentConfigHandler) GetFileFilterDefaults(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"languages": engine.DefaultExcludePatternsByLanguage,
			"common":    engine.CommonExcludePatterns,
		},
	})
}
