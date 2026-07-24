package handler

import (
	"context"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ReviewRuleHandler struct {
	embedSvc *service.EmbeddingService
	store    vectorstore.Store
}

func NewReviewRuleHandler(embedSvc *service.EmbeddingService, store vectorstore.Store) *ReviewRuleHandler {
	return &ReviewRuleHandler{embedSvc: embedSvc, store: store}
}

// List 获取规则库列表
func (h *ReviewRuleHandler) List(c *gin.Context) {
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

	db := model.DB.Model(&model.ReviewRule{})

	if category != "" {
		db = db.Where("category = ?", category)
	}
	if language != "" {
		db = db.Where("language = ?", language)
	}
	if isEnabled != "" {
		db = db.Where("is_enabled = ?", isEnabled == "true")
	}
	if keyword != "" {
		db = db.Where("name LIKE ? OR code LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		zap.L().Error("count review rules failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	var rules []model.ReviewRule
	if err := db.Order("sort_order ASC, id ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rules).Error; err != nil {
		zap.L().Error("list review rules failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 补充近 7 天命中次数（单次 GROUP BY 注入，避免 N+1）。
	// 使用默认 scope，过滤掉软删除的 issues：
	// persistor.go 在任务重试时会 soft delete 旧 issues 再 insert 新的，
	// 默认 scope 保证"近 7 天命中"反映当前可见的 issues 数量。
	hitMap := map[string]int64{}
	if len(rules) > 0 {
		sevenDaysAgo := time.Now().AddDate(0, 0, -7)
		type hitRow struct {
			RuleCode string `gorm:"column:rule_code"`
			Cnt      int64  `gorm:"column:cnt"`
		}
		var rows []hitRow
		if err := model.DB.Table("review_issues").
			Select("rule_code, COUNT(*) AS cnt").
			Where("rule_code != '' AND created_at >= ?", sevenDaysAgo).
			Group("rule_code").
			Scan(&rows).Error; err != nil {
			zap.L().Warn("load hit_count_7d failed", zap.Error(err))
		}
		for _, r := range rows {
			hitMap[r.RuleCode] = r.Cnt
		}
	}

	type ruleWithHit struct {
		model.ReviewRule
		HitCount7d int64 `json:"hit_count_7d"`
	}
	out := make([]ruleWithHit, len(rules))
	for i, r := range rules {
		out[i] = ruleWithHit{ReviewRule: r, HitCount7d: hitMap[r.Code]}
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
	var rules []model.ReviewRule
	if err := model.DB.Where("is_enabled = ?", true).Order("sort_order ASC").Find(&rules).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	tree := make(map[string]map[string][]model.ReviewRule)
	for _, rule := range rules {
		if _, ok := tree[rule.Language]; !ok {
			tree[rule.Language] = make(map[string][]model.ReviewRule)
		}
		tree[rule.Language][rule.Category] = append(tree[rule.Language][rule.Category], rule)
	}

	c.JSON(200, gin.H{"code": 0, "data": tree})
}

// BatchEnable 批量更新规则启用状态（仅内置规则的 is_enabled）
func (h *ReviewRuleHandler) BatchEnable(c *gin.Context) {
	var req struct {
		RuleIDs   []uint `json:"rule_ids"`
		IsEnabled bool   `json:"is_enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	if err := model.DB.Model(&model.ReviewRule{}).
		Where("id IN ?", req.RuleIDs).
		Update("is_enabled", req.IsEnabled).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "updated"})
}

// Create 创建自定义规则
func (h *ReviewRuleHandler) Create(c *gin.Context) {
	var rule model.ReviewRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	rule.IsBuiltIn = false // 用户创建的规则标记为非内置

	// 零值穿透写入：用 UpdateColumn 直接操作数据库，绕过 GORM 零值跳过机制
	if err := model.DB.Create(&rule).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 兜底：无论数据库列默认值如何，强制覆盖为 false
	model.DB.Model(&rule).UpdateColumn("is_built_in", false)
	rule.IsBuiltIn = false

	// 为所有已有项目自动插入默认配置（默认禁用），确保项目列表统计和规则配置页能正确显示
	autoCreateProjectReviewConfigs(rule.ID)

	// 异步生成规则 embedding 向量
	h.embedRule(rule)

	c.JSON(200, gin.H{"code": 0, "message": "created", "data": rule})
}

// Update 编辑自定义规则（仅非内置规则可编辑）
func (h *ReviewRuleHandler) Update(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var rule model.ReviewRule
	if err := model.DB.First(&rule, id).Error; err != nil {
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
	delete(req, "is_built_in")

	if err := model.DB.Model(&rule).Updates(req).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 重新加载更新后的规则并异步刷新 embedding
	model.DB.First(&rule, rule.ID)
	h.embedRule(rule)

	c.JSON(200, gin.H{"message": "updated"})
}

// Delete 删除自定义规则
func (h *ReviewRuleHandler) Delete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var rule model.ReviewRule
	if err := model.DB.First(&rule, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	if rule.IsBuiltIn {
		c.JSON(400, gin.H{"error": "内置规则不可删除"})
		return
	}

	if err := model.DB.Delete(&rule).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "deleted"})
}

// autoCreateProjectReviewConfigs 为所有已有项目自动插入新规则的默认配置（默认禁用）
func autoCreateProjectReviewConfigs(ruleID uint) {
	var projects []model.Project
	if err := model.DB.Find(&projects).Error; err != nil {
		zap.L().Warn("auto create project review configs: find projects failed", zap.Error(err))
		return
	}

	for _, p := range projects {
		// 检查是否已存在
		var count int64
		model.DB.Model(&model.ProjectReviewConfig{}).
			Where("project_id = ? AND rule_id = ?", p.ID, ruleID).
			Count(&count)
		if count > 0 {
			continue
		}

		cfg := model.ProjectReviewConfig{
			ProjectID: p.ID,
			RuleID:    ruleID,
			IsEnabled: false, // 默认禁用，用户需要手动在项目中启用
			Severity:  "",
		}
		if err := model.DB.Create(&cfg).Error; err != nil {
			zap.L().Warn("auto create project review config failed",
				zap.Uint("project_id", p.ID),
				zap.Uint("rule_id", ruleID),
				zap.Error(err))
		}
	}
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
