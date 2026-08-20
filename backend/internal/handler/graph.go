package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
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

// ProjectAccessCheck 项目权限检查中间件（admin 放行，非 admin 检查是否负责该项目）
func (h *GraphHandler) ProjectAccessCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := middleware.GetUser(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}
		// admin 直接放行
		if user.Role == model.RoleAdmin {
			c.Next()
			return
		}
		projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
			c.Abort()
			return
		}
		projectIDs := GetResponsibleProjectIDs(user.GitlabUsername)
		found := false
		for _, pid := range projectIDs {
			if pid == uint(projectID) {
				found = true
				break
			}
		}
		if !found {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权限访问该项目"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// RegisterRoutes 注册路由：common 组放只读路由，adminOnly 组放写入/管理路由
func (h *GraphHandler) RegisterRoutes(common, adminOnly *gin.RouterGroup) {
	// 只读路由 —— 所有登录用户可访问（含项目负责人）
	readGraph := common.Group("/projects/:id/graph")
	readGraph.Use(h.ProjectAccessCheck())
	{
		readGraph.GET("/status", h.HandleGraphStatus)
		readGraph.GET("/overview", h.HandleGraphOverview)
		readGraph.GET("/visualization", h.HandleGraphVisualization)
		readGraph.GET("/files", h.HandleGraphFiles)
		readGraph.GET("/files/content", h.HandleFileContent)
		readGraph.GET("/metrics", h.HandleGraphMetrics)
		readGraph.GET("/dependencies", h.HandleGraphDependencies)
		readGraph.GET("/security", h.HandleGraphSecurity)
		readGraph.GET("/build-history", h.HandleBuildHistory)
		readGraph.GET("/node-detail", h.HandleNodeDetail)
		readGraph.GET("/nodes/:node_id/detail", h.HandleNodeDetail)
	}

	// 写入/管理路由 —— 仅管理员
	writeGraph := adminOnly.Group("/projects/:id/graph")
	{
		writeGraph.POST("/build", h.HandleBuildGraph)
		writeGraph.POST("/refresh", h.HandleRefreshGraph)
		writeGraph.PATCH("/config", h.HandleUpdateGraphConfig)
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
		task, scanErr = h.scanService.RefreshScan(projectID, req.Branch, graphscan.WithTriggerType("manual"))
	} else {
		task, scanErr = h.scanService.TriggerScan(projectID, req.Branch, graphscan.WithTriggerType("manual"))
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

// HandleGraphVisualization 获取图可视化数据（分层加载）
func (h *GraphHandler) HandleGraphVisualization(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	detailLevel := c.DefaultQuery("detail_level", "low")
	focusFile := c.Query("focus_file")
	focusPackage := c.Query("focus_package")
	maxNodes, _ := strconv.Atoi(c.DefaultQuery("max_nodes", "500"))

	data, err := h.scanService.GetGraphVisualization(projectID, detailLevel, focusFile, focusPackage, maxNodes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, data)
}

// HandleNodeDetail 获取节点详情（支持路径参数和 query 参数）
func (h *GraphHandler) HandleNodeDetail(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	nodeID := c.Param("node_id")
	if nodeID == "" {
		nodeID = c.Query("node_id")
	}

	detail, err := h.scanService.GetNodeDetail(projectID, nodeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, detail)
}

// HandleGraphFiles 获取文件列表及文件级指标
func (h *GraphHandler) HandleGraphFiles(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	files, err := h.scanService.GetGraphFiles(projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"files": files})
}

// HandleFileContent 读取指定文件内容（用于代码片段展示）
func (h *GraphHandler) HandleFileContent(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	filePath := c.Query("file_path")
	if filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_path query parameter required"})
		return
	}

	content, err := h.scanService.GetFileContent(projectID, filePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"file_path": filePath,
		"content":   content,
	})
}

// HandleGraphMetrics 获取度量仪表盘数据
func (h *GraphHandler) HandleGraphMetrics(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	metrics, err := h.scanService.GetGraphMetrics(projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, metrics)
}

// HandleGraphDependencies 获取依赖网络数据
func (h *GraphHandler) HandleGraphDependencies(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	deps, err := h.scanService.GetGraphDependencies(projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, deps)
}

// HandleGraphSecurity 获取安全分析数据
func (h *GraphHandler) HandleGraphSecurity(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	security, err := h.scanService.GetGraphSecurity(projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, security)
}

// HandleBuildHistory 获取代码地图构建历史
func (h *GraphHandler) HandleBuildHistory(c *gin.Context) {
	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid project_id"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	h.db.Model(&model.GraphScanTask{}).Where("project_id = ?", projectID).Count(&total)

	var tasks []model.GraphScanTask
	offset := (page - 1) * pageSize
	if err := h.db.Where("project_id = ?", projectID).
		Order("created_at DESC").
		Limit(pageSize).Offset(offset).
		Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 转换响应格式
	items := make([]map[string]interface{}, 0, len(tasks))
	for _, t := range tasks {
		item := map[string]interface{}{
			"id":           t.ID,
			"trigger_type": t.TriggerType,
			"branch":       t.Branch,
			"status":       t.Status,
			"node_count":   t.NodeCount,
			"rel_count":    t.RelationCount,
			"file_count":   t.FileCount,
			"duration_ms":  t.DurationMs,
			"error_msg":    t.ErrorMessage,
			"created_at":   t.CreatedAt,
		}
		if t.TriggerType == "mr_merge" {
			item["mr_iid"] = t.MRIID
			item["mr_title"] = t.MRTitle
		}
		if t.CompletedAt != nil {
			item["completed_at"] = t.CompletedAt
		}
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
