package handler

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// IncubatorHandler handles rule incubator APIs.
type IncubatorHandler struct {
	svc *service.IncubatorService
}

// NewIncubatorHandler creates a new incubator handler.
func NewIncubatorHandler(store vectorstore.Store) *IncubatorHandler {
	embedSvc := service.NewEmbeddingService(store)
	return &IncubatorHandler{
		svc: service.NewIncubatorService(embedSvc, store),
	}
}

// Status returns incubator status and embedding availability.
// GET /api/v1/incubator/status
func (h *IncubatorHandler) Status(c *gin.Context) {
	data, err := h.svc.Status()
	if err != nil {
		zap.L().Error("incubator status failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": data})
}

// ListIssues returns unmatched review issues.
// GET /api/v1/incubator/issues
func (h *IncubatorHandler) ListIssues(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize > 100 {
		pageSize = 100
	}

	filter := service.IssueFilter{
		Page:      page,
		PageSize:  pageSize,
		StartDate: c.Query("start_date"),
		EndDate:   c.Query("end_date"),
		Language:  c.Query("language"),
		Category:  c.Query("category"),
		Severity:  c.Query("severity"),
		Status:    c.Query("status"),
		Keyword:   c.Query("keyword"),
	}
	if pidStr := c.Query("project_id"); pidStr != "" {
		pid, _ := strconv.ParseUint(pidStr, 10, 32)
		filter.ProjectID = uint(pid)
	}

	issues, total, err := h.svc.ListUnmatchedIssues(filter)
	if err != nil {
		zap.L().Error("list unmatched issues failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":      0,
		"data":      issues,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// Cluster triggers keyword clustering on unmatched issues.
// POST /api/v1/incubator/cluster
func (h *IncubatorHandler) Cluster(c *gin.Context) {
	var req struct {
		TimeRangeDays int      `json:"time_range_days"`
		Languages     []string `json:"languages"`
		MinGroupSize  int      `json:"min_group_size"`
		Force         bool     `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	job, err := h.svc.ClusterIssues(req.TimeRangeDays, req.Languages, req.MinGroupSize)
	if err != nil {
		zap.L().Error("cluster issues failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": map[string]any{
			"job_id": job.ID,
			"status": job.Status,
		},
	})
}

// GetClusterJob returns a cluster job status.
// GET /api/v1/incubator/cluster/jobs/:id
func (h *IncubatorHandler) GetClusterJob(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	var job model.RuleIncubationJob
	if err := model.DB.First(&job, uint(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "job not found"})
			return
		}
		zap.L().Error("get cluster job failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": job})
}

// CreateCandidate creates a candidate rule from selected issues.
// POST /api/v1/incubator/candidates
func (h *IncubatorHandler) CreateCandidate(c *gin.Context) {
	var req service.CreateCandidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	cand, err := h.svc.CreateCandidate(req, userID.(uint))
	if err != nil {
		zap.L().Error("create candidate failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": map[string]any{
			"id":      cand.ID,
			"status":  cand.Status,
			"message": "候选规则已创建",
		},
	})
}

// ListCandidates lists incubation candidates.
// GET /api/v1/incubator/candidates
func (h *IncubatorHandler) ListCandidates(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	status := c.Query("status")
	keyword := c.Query("keyword")

	list, total, err := h.svc.ListCandidates(status, keyword, page, pageSize)
	if err != nil {
		zap.L().Error("list candidates failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":      0,
		"data":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetCandidate returns a candidate with source issues.
// GET /api/v1/incubator/candidates/:id
func (h *IncubatorHandler) GetCandidate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	cand, issues, err := h.svc.GetCandidate(uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "候选规则不存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": map[string]any{
			"candidate": cand,
			"issues":    issues,
		},
	})
}

// UpdateCandidate edits a candidate.
// PUT /api/v1/incubator/candidates/:id
func (h *IncubatorHandler) UpdateCandidate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	var req map[string]any
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误"})
		return
	}

	if err := h.svc.UpdateCandidate(uint(id), req); err != nil {
		zap.L().Error("update candidate failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "已更新"})
}

// DeleteCandidate marks a candidate as rejected.
// DELETE /api/v1/incubator/candidates/:id
func (h *IncubatorHandler) DeleteCandidate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	if err := h.svc.DeleteCandidate(uint(id)); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "已忽略"})
}

// PublishCandidate publishes a candidate as a real rule.
// POST /api/v1/incubator/candidates/:id/publish
func (h *IncubatorHandler) PublishCandidate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	var req service.PublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误"})
		return
	}

	userID, _ := c.Get("user_id")
	ruleID, err := h.svc.PublishCandidate(uint(id), req, userID.(uint))
	if err != nil {
		zap.L().Error("publish candidate failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	model.RecordOpLog("规则发布", fmt.Sprintf("从孵化台发布规则ID: %d", ruleID), ruleID, userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{
		"code": 0,
		"data": map[string]any{
			"published_rule_id": ruleID,
		},
	})
}

// GetConfig returns incubator configuration.
// GET /api/v1/incubator/config
func (h *IncubatorHandler) GetConfig(c *gin.Context) {
	cfg := h.svc.GetConfig()
	c.JSON(200, gin.H{"code": 0, "data": cfg})
}

// SaveConfig updates incubator configuration.
// PUT /api/v1/incubator/config
func (h *IncubatorHandler) SaveConfig(c *gin.Context) {
	var req map[string]any
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误"})
		return
	}
	if err := h.svc.SaveConfig(req); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "已保存"})
}

// ValidateEmbedding tests embedding connectivity.
// POST /api/v1/incubator/config/validate-embedding
func (h *IncubatorHandler) ValidateEmbedding(c *gin.Context) {
	data, err := h.svc.ValidateEmbedding()
	if err != nil {
		c.JSON(200, gin.H{"code": 0, "data": map[string]any{"available": false, "error": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": data})
}
