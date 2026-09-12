package handler

import (
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ReviewCategoryHandler struct{}

func NewReviewCategoryHandler() *ReviewCategoryHandler {
	return &ReviewCategoryHandler{}
}

// List 获取所有评审维度（内置+自定义）
func (h *ReviewCategoryHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(403, gin.H{"error": "未认证"})
		return
	}
	if _, ok := currentUserOrAbort(c); !ok { return }
	var cats []model.ReviewCategory
	db := model.DB.Order("sort_order ASC, id ASC")
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ? OR is_built_in = ?", scope.VisibleOrgIDs, true)
	}
	if err := db.Find(&cats).Error; err != nil {
		zap.L().Error("list review categories failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(cats))
	for _, cat := range cats {
		orgIDs = append(orgIDs, cat.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range cats {
		cats[i].OrgName = orgNameMap[cats[i].OrgID]
	}

	c.JSON(200, gin.H{"data": cats})
}

// Create 创建自定义维度（内置维度不可通过此接口创建）
func (h *ReviewCategoryHandler) Create(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(403, gin.H{"error": "未认证"})
		return
	}
	_, ok := currentUserOrAbort(c)
	if !ok { return }
	if !scope.HasOrgRole("super_admin", "org_admin") {
		c.JSON(403, gin.H{"error": "admin required"})
		return
	}
	var cat model.ReviewCategory
	if err := c.ShouldBindJSON(&cat); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if cat.Code == "" || cat.Name == "" {
		c.JSON(400, gin.H{"error": "code and name are required"})
		return
	}
	cat.IsBuiltIn = false
	if cat.OrgID == 0 || !scope.IsSuperAdmin {
		cat.OrgID = scope.CurrentOrgID
	}
	if err := model.DB.Create(&cat).Error; err != nil {
		zap.L().Error("create review category failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "created", "data": cat})
}

// Update 更新维度（仅自定义维度可更新）
func (h *ReviewCategoryHandler) Update(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(403, gin.H{"error": "未认证"})
		return
	}
	_, ok := currentUserOrAbort(c)
	if !ok { return }
	if !scope.HasOrgRole("super_admin", "org_admin") {
		c.JSON(403, gin.H{"error": "admin required"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var cat model.ReviewCategory
	if err := model.DB.Scopes(model.OrgScope(scope)).First(&cat, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if cat.IsBuiltIn {
		c.JSON(403, gin.H{"error": "built-in category cannot be modified"})
		return
	}
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	updates := make(map[string]interface{})
	if v, ok := req["code"]; ok {
		updates["code"] = v.(string)
	}
	if v, ok := req["name"]; ok {
		updates["name"] = v.(string)
	}
	if v, ok := req["sort_order"]; ok {
		updates["sort_order"] = int(v.(float64))
	}
	if len(updates) == 0 {
		c.JSON(400, gin.H{"error": "no fields to update"})
		return
	}
	if err := model.DB.Model(&cat).Updates(updates).Error; err != nil {
		zap.L().Error("update review category failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "updated", "data": cat})
}

// Delete 删除维度（仅自定义维度可删除）
func (h *ReviewCategoryHandler) Delete(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(403, gin.H{"error": "未认证"})
		return
	}
	_, ok := currentUserOrAbort(c)
	if !ok { return }
	if !scope.HasOrgRole("super_admin", "org_admin") {
		c.JSON(403, gin.H{"error": "admin required"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var cat model.ReviewCategory
	if err := model.DB.Scopes(model.OrgScope(scope)).First(&cat, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if cat.IsBuiltIn {
		c.JSON(403, gin.H{"error": "built-in category cannot be deleted"})
		return
	}
	// 检查是否有规则正在使用此维度
	var count int64
	model.DB.Scopes(model.OrgScope(scope)).Model(&model.ReviewRule{}).Where("category = ?", cat.Code).Count(&count)
	if count > 0 {
		c.JSON(400, gin.H{"error": "cannot delete category in use by review rules"})
		return
	}
	if err := model.DB.Scopes(model.OrgScope(scope)).Delete(&cat).Error; err != nil {
		zap.L().Error("delete review category failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "deleted"})
}
