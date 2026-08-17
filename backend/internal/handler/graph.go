package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service/graphscan"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GraphHandler 知识图谱API处理器
type GraphHandler struct {
	db          *gorm.DB
	scanService *graphscan.ScanService
}

// NewGraphHandler 创建图谱处理器
func NewGraphHandler(db *gorm.DB, workspace string) *GraphHandler {
	return &GraphHandler{
		db:          db,
		scanService: graphscan.NewScanService(db, workspace),
	}
}

// RegisterRoutes 注册路由
func (h *GraphHandler) RegisterRoutes(r *gin.RouterGroup) {
	graph := r.Group("/projects/:id/graph")
	{
		graph.POST("/build", h.HandleBuildGraph)
		graph.POST("/refresh", h.HandleRefreshGraph)
		graph.GET("/status", h.HandleGraphStatus)
		graph.GET("/overview", h.HandleGraphOverview)
		graph.PATCH("/config", h.HandleUpdateGraphConfig)
	}
}

// HandleBuildGraph 触发全量扫描
func (h *GraphHandler) HandleBuildGraph(c *gin.Context) {
	h.startScan(c, false)
}

// HandleRefreshGraph 重新全量扫描
func (h *GraphHandler) HandleRefreshGraph(c *gin.Context) {
	h.startScan(c, true)
}

func (h *GraphHandler) startScan(c *gin.Context, isRefresh bool) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	var req struct {
		Branch string `json:"branch"`
	}
	// 允许空 body，此时从项目配置读取默认分支
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Branch == "" {
		var project model.Project
		if err := h.db.First(&project, projectID).Error; err == nil && project.GraphBaseBranch != "" {
			req.Branch = project.GraphBaseBranch
		} else {
			req.Branch = "main"
		}
	}

	var task *model.GraphScanTask
	var scanErr error
	if isRefresh {
		task, scanErr = h.scanService.RefreshScan(projectID, req.Branch)
	} else {
		task, scanErr = h.scanService.TriggerScan(projectID, req.Branch)
	}
	if scanErr != nil {
		c.JSON(http.StatusConflict, gin.H{"error": scanErr.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":    task.ID,
		"status":     task.Status,
		"branch":     req.Branch,
		"created_at": task.CreatedAt,
	})
}

// HandleGraphStatus 获取扫描状态
func (h *GraphHandler) HandleGraphStatus(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	task, err := h.scanService.GetScanStatus(projectID)
	if err != nil {
		// 没有扫描任务，从项目表读取
		var project model.Project
		if err := h.db.First(&project, projectID).Error; err == nil {
			frameworks := []string{}
			if project.GraphFrameworks != "" {
				json.Unmarshal([]byte(project.GraphFrameworks), &frameworks)
			}
			c.JSON(http.StatusOK, gin.H{
				"status":         project.GraphScanStatus,
				"node_count":     project.GraphNodeCount,
				"relation_count": project.GraphRelCount,
				"endpoint_count": project.GraphEndpointCount,
				"frameworks":     frameworks,
				"built_at":       project.GraphBuiltAt,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "none"})
		return
	}

	resp := gin.H{
		"task_id":        task.ID,
		"status":         task.Status,
		"scanned_files":  task.FileCount,
		"total_files":    task.FileCount,
		"node_count":     task.NodeCount,
		"relation_count": task.RelationCount,
		"created_at":     task.CreatedAt,
	}
	if task.CompletedAt != nil {
		resp["completed_at"] = task.CompletedAt
	}
	c.JSON(http.StatusOK, resp)
}

// HandleGraphOverview 获取图谱概览
func (h *GraphHandler) HandleGraphOverview(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	overview, err := h.scanService.GetGraphOverview(projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, overview)
}

// HandleUpdateGraphConfig 更新图谱配置
func (h *GraphHandler) HandleUpdateGraphConfig(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	var req struct {
		GraphBaseBranch string `json:"graph_base_branch,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if req.GraphBaseBranch != "" {
		updates["graph_base_branch"] = req.GraphBaseBranch
	}

	if len(updates) > 0 {
		if err := h.db.Model(&model.Project{}).Where("id = ?", projectID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}
