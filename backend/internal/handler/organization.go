package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// OrganizationHandler 组织管理（仅 super_admin）
type OrganizationHandler struct{}

// NewOrganizationHandler creates handler
func NewOrganizationHandler() *OrganizationHandler {
	return &OrganizationHandler{}
}

// ListOrganizations GET /api/v1/organizations — 返回树形结构
func (h *OrganizationHandler) ListOrganizations(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || scope.CurrentOrgRole != "super_admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅超级管理员可管理组织"})
		return
	}

	var orgs []model.Organization
	if err := model.DB.Order("id ASC").Find(&orgs).Error; err != nil {
		zap.L().Error("list organizations failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询组织列表失败"})
		return
	}

	// 统计每个组织的成员数
	counts := make(map[uint]int64)
	for _, o := range orgs {
		var cnt int64
		if err := model.DB.Model(&model.OrgUser{}).Where("org_id = ?", o.ID).Count(&cnt).Error; err != nil {
			zap.L().Warn("count org members failed", zap.Uint("org_id", o.ID), zap.Error(err))
		}
		counts[o.ID] = cnt
	}

	tree := model.BuildOrgTree(orgs, counts, 0)
	c.JSON(http.StatusOK, gin.H{"data": tree})
}

// CreateOrganization POST /api/v1/organizations
func (h *OrganizationHandler) CreateOrganization(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || scope.CurrentOrgRole != "super_admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅超级管理员可创建组织"})
		return
	}

	var req struct {
		Name       string `json:"name" binding:"required"`
		Slug       string `json:"slug"`
		ParentID   uint   `json:"parent_id"`
		Status     string `json:"status"`
		LLMQuota   int    `json:"llm_quota"`
		TokenQuota int    `json:"token_quota"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	if req.Status == "" {
		req.Status = "active"
	}

	code := req.Slug
	if code == "" {
		// 从 name 自动生成 code（小写、替换空格为连字符）
		code = generateOrgCode(req.Name)
	}

	// 检查 code 是否已存在，若存在则追加随机后缀
	var existing int64
	model.DB.Model(&model.Organization{}).Where("code = ?", code).Count(&existing)
	if existing > 0 {
		code = code + "-" + strconv.FormatInt(time.Now().Unix(), 10)
	}

	org := model.Organization{
		Name:               req.Name,
		Code:               code,
		ParentID:           req.ParentID,
		Status:             req.Status,
		MaxLLMTokensPerDay: int64(req.LLMQuota),
	}
	if err := model.DB.Create(&org).Error; err != nil {
		zap.L().Error("create organization failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建组织失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": org})
}

// UpdateOrganization PUT /api/v1/organizations/:id
func (h *OrganizationHandler) UpdateOrganization(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || scope.CurrentOrgRole != "super_admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅超级管理员可编辑组织"})
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Name       string `json:"name"`
		Slug       string `json:"slug"`
		ParentID   *uint  `json:"parent_id"`
		Status     string `json:"status"`
		LLMQuota   int    `json:"llm_quota"`
		TokenQuota int    `json:"token_quota"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Slug != "" {
		updates["code"] = req.Slug
	}
	if req.ParentID != nil {
		updates["parent_id"] = *req.ParentID
	}
	if req.Status != "" {
		updates["status"] = req.Status
	}
	updates["max_llm_tokens_per_day"] = req.LLMQuota

	result := model.DB.Model(&model.Organization{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		zap.L().Error("update organization failed", zap.Error(result.Error))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新组织失败"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "组织不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
}

// ArchiveOrganization POST /api/v1/organizations/:id/archive
func (h *OrganizationHandler) ArchiveOrganization(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || scope.CurrentOrgRole != "super_admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅超级管理员可操作"})
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	result := model.DB.Model(&model.Organization{}).Where("id = ?", id).Update("status", "archived")
	if result.Error != nil {
		zap.L().Error("archive organization failed", zap.Error(result.Error))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "归档组织失败"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "组织不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "归档成功"})
}

// ListOrganizationMembers GET /api/v1/organizations/:id/members
func (h *OrganizationHandler) ListOrganizationMembers(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || !scope.IsSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权限"})
		return
	}

	orgID, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	var members []struct {
		ID          uint   `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
		OrgRole     string `json:"org_role"`
		IsDefault   bool   `json:"is_default"`
		Status      string `json:"status"`
	}

	err := model.DB.Raw(`
		SELECT u.id, u.username, u.display_name, u.role,
			ou.role as org_role, ou.is_default, ou.status
		FROM users u
		JOIN org_users ou ON u.id = ou.user_id
		WHERE ou.org_id = ?
		ORDER BY ou.role DESC, u.id ASC
	`, orgID).Scan(&members).Error
	if err != nil {
		zap.L().Error("list org members failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询成员列表失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": members})
}

// AddOrganizationMember POST /api/v1/organizations/:id/members
func (h *OrganizationHandler) AddOrganizationMember(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || !scope.IsSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权限"})
		return
	}

	orgID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		UserID uint   `json:"user_id" binding:"required"`
		Role   string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Role == "" {
		req.Role = "developer"
	}

	var exists int64
	if err := model.DB.Model(&model.OrgUser{}).Where("org_id = ? AND user_id = ?", orgID, req.UserID).Count(&exists).Error; err != nil {
		zap.L().Error("check org member exists failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if exists > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该用户已在组织中"})
		return
	}

	ou := model.OrgUser{
		OrgID:     uint(orgID),
		UserID:    req.UserID,
		Role:      req.Role,
		Status:    "active",
		IsDefault: false,
	}
	if err := model.DB.Create(&ou).Error; err != nil {
		zap.L().Error("add org member failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "添加成员失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "添加成功"})
}

// RemoveOrganizationMember DELETE /api/v1/organizations/:id/members/:userId
func (h *OrganizationHandler) RemoveOrganizationMember(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || !scope.IsSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权限"})
		return
	}

	orgID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	userID, _ := strconv.ParseUint(c.Param("userId"), 10, 64)

	if err := model.DB.Where("org_id = ? AND user_id = ?", orgID, userID).Delete(&model.OrgUser{}).Error; err != nil {
		zap.L().Error("remove org member failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "移除成员失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "移除成功"})
}

// UpdateMemberRole PUT /api/v1/organizations/:id/members/:userId/role
func (h *OrganizationHandler) UpdateMemberRole(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil || !scope.IsSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权限"})
		return
	}

	orgID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	userID, _ := strconv.ParseUint(c.Param("userId"), 10, 64)
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if err := model.DB.Model(&model.OrgUser{}).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		Update("role", req.Role).Error; err != nil {
		zap.L().Error("update member role failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新角色失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
}

// generateOrgCode 从组织名称生成 URL-safe 的 code
func generateOrgCode(name string) string {
	if name == "" {
		return "org-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	// 移除特殊字符，仅保留字母、数字、空格和连字符
	code := strings.ToLower(name)
	code = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, code)
	// 替换空格为连字符
	code = strings.ReplaceAll(code, " ", "-")
	// 去除连续连字符
	for strings.Contains(code, "--") {
		code = strings.ReplaceAll(code, "--", "-")
	}
	// 限制长度
	if len(code) > 50 {
		code = code[:50]
	}
	code = strings.Trim(code, "-")
	if code == "" {
		return "org-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	return code
}
