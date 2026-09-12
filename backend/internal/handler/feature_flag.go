package handler

import (
	"net/http"
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// FeatureFlagHandler 动态 Feature Flag 管理（仅 super_admin 可用）
type FeatureFlagHandler struct{}

func NewFeatureFlagHandler() *FeatureFlagHandler {
	return &FeatureFlagHandler{}
}

// GetLayer 获取当前多租户 Feature Flag 层数
func (h *FeatureFlagHandler) GetLayer(c *gin.Context) {
	layer := middleware.GlobalFeatureFlag.Layer()
	enabled := middleware.GlobalFeatureFlag.IsReadIsolationEnabled()
	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"layer":                        layer,
			"read_isolation_enabled":       enabled,
			"write_intercept_enabled":      middleware.GlobalFeatureFlag.IsWriteInterceptEnabled(),
			"org_mgmt_enabled":             middleware.GlobalFeatureFlag.IsOrgMgmtEnabled(),
			"org_ui_switcher_enabled":      middleware.GlobalFeatureFlag.IsOrgUISwitcherEnabled(),
			"full_multitenancy_enabled":    middleware.GlobalFeatureFlag.IsFullMultitenancyEnabled(),
			"layer_0_kill_switch":          !enabled, // Layer 0 = 全局关闭
		},
	})
}

// SetLayer 动态设置 Feature Flag 层数（仅 super_admin）
// 请求体: {"layer": 0-5}
// Layer 说明：
//   0 = 全局关闭（单组织模式，所有多租户逻辑 fallback）
//   1 = 读隔离（查询 WHERE org_id = ?）
//   2 = 写拦截（拒绝 org_id=0 的写入）
//   3 = 组织管理 API（创建/切换组织）
//   4 = 组织切换 UI（前端组织选择器）
//   5 = 完全启用（多租户全功能）
func (h *FeatureFlagHandler) SetLayer(c *gin.Context) {
	// 仅 super_admin 可修改
	scope := middleware.GetAuthScope(c)
	if scope == nil || !scope.IsSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅超级管理员可操作 Feature Flag"})
		return
	}

	var req struct {
		Layer int `json:"layer" binding:"required,min=0,max=5"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "layer 必须是 0-5 的整数"})
		return
	}

	oldLayer := middleware.GlobalFeatureFlag.Layer()
	middleware.GlobalFeatureFlag.SetLayer(req.Layer)

	zap.L().Warn("Feature Flag layer changed",
		zap.Int("old_layer", oldLayer),
		zap.Int("new_layer", req.Layer),
		zap.Uint("operator_user_id", scope.UserID),
	)

	c.JSON(http.StatusOK, gin.H{
		"message":    "Feature Flag 层数已更新",
		"old_layer":  oldLayer,
		"new_layer":  req.Layer,
	})
}

// GetLayerFromQuery 兼容 GET 方式获取层数（无需认证，用于健康检查）
func (h *FeatureFlagHandler) GetLayerFromQuery(c *gin.Context) {
	layerStr := c.Query("layer")
	if layerStr == "" {
		// 返回当前层数
		c.JSON(http.StatusOK, gin.H{"layer": middleware.GlobalFeatureFlag.Layer()})
		return
	}

	layer, err := strconv.Atoi(layerStr)
	if err != nil || layer < 0 || layer > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "layer 必须是 0-5 的整数"})
		return
	}

	// 检查指定层是否启用
	c.JSON(http.StatusOK, gin.H{
		"layer":   layer,
		"enabled": middleware.GlobalFeatureFlag.IsEnabled(layer),
	})
}
