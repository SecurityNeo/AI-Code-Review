package handler

import (
	"context"
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ReviewRuleHandler struct {
	embedSvc *service.EmbeddingService
	store    vectorstore.Store
	svc      *service.ReviewRuleService
}

func NewReviewRuleHandler(embedSvc *service.EmbeddingService, store vectorstore.Store) *ReviewRuleHandler {
	return &ReviewRuleHandler{embedSvc: embedSvc, store: store, svc: service.NewReviewRuleService()}
}

// List 获取规则库列表
func (h *ReviewRuleHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	category := c.Query("category")
	language := c.Query("language")
	isEnabled := c.Query("is_enabled")
	keyword := c.Query("keyword")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "15"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 1000 {
		pageSize = 15
	}

	rules, total, hitMap, err := h.svc.List(scope, category, language, isEnabled, keyword, page, pageSize)
	if err != nil {
		zap.L().Error("list review rules failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	type ruleWithHit struct {
		model.ReviewRule
		HitCount7d int64 `json:"hit_count_7d"`
	}
	out := make([]ruleWithHit, len(rules))
	for i, r := range rules {
		out[i] = ruleWithHit{ReviewRule: r, HitCount7d: hitMap[r.Code]}
	}

	// 批量填充组织名称（ReviewRule 嵌入后字段提升）
	orgIDs := make([]uint, 0, len(rules))
	for _, r := range rules {
		orgIDs = append(orgIDs, r.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range out {
		out[i].OrgName = orgNameMap[out[i].OrgID]
	}

	c.JSON(200, gin.H{
		"code":      0,
		"data":      out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// Tree 按语言、维度分组返回规则树
func (h *ReviewRuleHandler) Tree(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	tree, err := h.svc.Tree(scope)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"code": 0, "data": tree})
}

// BatchEnable 批量更新规则启用状态（仅内置规则的 is_enabled）
func (h *ReviewRuleHandler) BatchEnable(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var req struct {
		RuleIDs   []uint `json:"rule_ids"`
		IsEnabled bool   `json:"is_enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.BatchEnable(scope, req.RuleIDs, req.IsEnabled); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "updated"})
}

// Create 创建自定义规则
func (h *ReviewRuleHandler) Create(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var rule model.ReviewRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：注入当前 org_id（super_admin 可以传入）
	if rule.OrgID == 0 || !scope.IsSuperAdmin {
		rule.OrgID = middleware.GetCurrentOrgID(c)
	}
	rule.IsBuiltIn = false // 用户创建的规则标记为非内置

	// 零值穿透写入：用 UpdateColumn 直接操作数据库，绕过 GORM 零值跳过机制
	if err := h.svc.Create(scope, &rule); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 异步生成规则 embedding 向量
	h.embedRule(rule)

	c.JSON(200, gin.H{"code": 0, "message": "created", "data": rule})
}

// Update 编辑自定义规则（仅非内置规则可编辑）
func (h *ReviewRuleHandler) Update(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))

	rule, err := h.svc.Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	if rule.IsBuiltIn {
		c.JSON(400, gin.H{"error": "内置规则不可编辑"})
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 删除不能修改的字段
	delete(req, "id")
	if !scope.IsSuperAdmin {
		delete(req, "org_id") // 多租户改造：禁止修改 org_id
	}
	delete(req, "is_built_in")
	// super_admin 可以修改 org_id：用 UpdateColumn 绕过 GORM hook
	if scope.IsSuperAdmin {
		if orgID, ok := extractOrgIDFromMap(req); ok {
			zap.L().Info("review rule update: changing org_id", zap.Int("id", id), zap.Uint("new_org_id", orgID))
			if err := model.DB.Model(&model.ReviewRule{}).Where("id = ?", id).UpdateColumn("org_id", orgID).Error; err != nil {
				zap.L().Error("review rule update: org_id update failed", zap.Int("id", id), zap.Uint("org_id", orgID), zap.Error(err))
				c.JSON(500, gin.H{"error": "组织归属更新失败: " + err.Error()})
				return
			}
			zap.L().Info("review rule update: org_id updated successfully", zap.Int("id", id), zap.Uint("new_org_id", orgID))
			delete(req, "org_id")
		}
	}

	if err := h.svc.Update(scope, uint(id), req); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 重新加载更新后的规则并异步刷新 embedding
	updatedRule, _ := h.svc.Get(scope, rule.ID)
	if updatedRule != nil {
		h.embedRule(*updatedRule)
	}

	c.JSON(200, gin.H{"message": "updated"})
}

// Delete 删除自定义规则
func (h *ReviewRuleHandler) Delete(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))

	rule, err := h.svc.Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	if rule.IsBuiltIn {
		c.JSON(400, gin.H{"error": "内置规则不可删除"})
		return
	}

	if err := h.svc.Delete(scope, uint(id)); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "deleted"})
}

// embedRule asynchronously generates embedding for a review rule.
// It reads the model ID from incubator config; if unavailable, it silently skips.
func (h *ReviewRuleHandler) embedRule(rule model.ReviewRule) {
	if h.embedSvc == nil || !h.embedSvc.IsAvailable() || h.store == nil {
		return
	}
	var cfg model.IncubatorConfig
	var modelID uint
	if err := model.DB.First(&cfg, 1).Error; err == nil && cfg.EmbeddingModelID != nil && *cfg.EmbeddingModelID > 0 {
		modelID = *cfg.EmbeddingModelID
	} else {
		if err == nil {
			zap.L().Warn("embedRule: no embedding model configured", zap.Uint("rule_id", rule.ID))
		}
		return
	}
	go func(r model.ReviewRule, mid uint) {
		key := vectorstore.Key{
			EntityType: "rule",
			EntityID:   r.ID,
			ModelID:    mid,
		}
		text := r.Name + " " + r.Description + " " + r.Prompt
		ctx := context.Background()
		if err := h.embedSvc.EmbedAndStore(ctx, key, text); err != nil {
			zap.L().Warn("embedRule failed",
				zap.Uint("rule_id", r.ID),
				zap.String("code", r.Code),
				zap.Error(err))
		} else {
			zap.L().Info("embedRule succeeded",
				zap.Uint("rule_id", r.ID),
				zap.String("code", r.Code))
		}
	}(rule, modelID)
}
