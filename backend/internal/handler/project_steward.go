package handler

import (
	"net/http"
	"strconv"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
)

// ProjectStewardHandler 项目环节负责人 Handler
type ProjectStewardHandler struct{}

func NewProjectStewardHandler() *ProjectStewardHandler {
	return &ProjectStewardHandler{}
}

// ListByProject 列出某项目的环节负责人
func (h *ProjectStewardHandler) ListByProject(c *gin.Context) {
	projectID, _ := strconv.ParseUint(c.Param("project_id"), 10, 64)
	var list []model.ProjectSteward
	if err := model.DB.Preload("User").Where("project_id = ?", projectID).Order("role_type, priority").Find(&list).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

// Create 创建环节负责人
func (h *ProjectStewardHandler) Create(c *gin.Context) {
	var req struct {
		ProjectID uint   `json:"project_id" binding:"required"`
		UserID    uint   `json:"user_id" binding:"required"`
		RoleType  string `json:"role_type" binding:"required"`
		Priority  int    `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	steward := model.ProjectSteward{
		ProjectID: req.ProjectID,
		UserID:    req.UserID,
		RoleType:  req.RoleType,
		Priority:  req.Priority,
	}
	if err := model.DB.Create(&steward).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": steward})
}

// Update 更新
func (h *ProjectStewardHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		RoleType string `json:"role_type"`
		Priority int    `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Model(&model.ProjectSteward{}).Where("id = ?", id).Updates(map[string]interface{}{
		"role_type": req.RoleType,
		"priority":  req.Priority,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// Delete 删除
func (h *ProjectStewardHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := model.DB.Delete(&model.ProjectSteward{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}
