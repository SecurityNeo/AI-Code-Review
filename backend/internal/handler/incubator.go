package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

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

// GetClusterJob returns a cluster job status with parsed summary.
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

	// Parse result_summary for richer frontend display
	var summary map[string]any
	if job.ResultSummary != "" && job.ResultSummary != "{}" {
		_ = json.Unmarshal([]byte(job.ResultSummary), &summary)
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": map[string]any{
			"id":             job.ID,
			"job_type":       job.JobType,
			"status":         job.Status,
			"params":         job.Params,
			"result_summary": summary,
			"error_msg":      job.ErrorMsg,
			"created_at":     job.CreatedAt,
			"started_at":     job.StartedAt,
			"completed_at":   job.CompletedAt,
		},
	})
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
	severity := c.Query("severity")

	list, total, err := h.svc.ListCandidates(status, keyword, severity, page, pageSize)
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

// GetSimilarGraph returns the similarity graph for a candidate rule.
// GET /api/v1/incubator/candidates/:id/similar-graph
func (h *IncubatorHandler) GetSimilarGraph(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	data, err := h.svc.GetSimilarGraph(uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "候选规则不存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": data})
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
// Supports passing model_id to test a newly selected model before saving configuration.
func (h *IncubatorHandler) ValidateEmbedding(c *gin.Context) {
	var req struct {
		ModelID uint `json:"model_id"`
	}
	c.ShouldBindJSON(&req)

	data, err := h.svc.ValidateEmbedding(req.ModelID)
	if err != nil {
		c.JSON(200, gin.H{"code": 0, "data": map[string]any{"available": false, "error": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": data})
}

// TriggerRefine queues a refine job for a candidate.
// POST /api/v1/incubator/candidates/:id/refine
func (h *IncubatorHandler) TriggerRefine(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	job, err := service.QueueJob("refine", map[string]any{"incubation_id": uint(id)})
	if err != nil {
		zap.L().Error("queue refine job failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": map[string]any{"job_id": job.ID, "status": "queued", "message": "Prompt提炼任务已加入队列"}})
}

// TriggerSimilarCheck queues a similar-rule check job.
// POST /api/v1/incubator/candidates/:id/similar-check
func (h *IncubatorHandler) TriggerSimilarCheck(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	job, err := service.QueueJob("similar_check", map[string]any{"incubation_id": uint(id)})
	if err != nil {
		zap.L().Error("queue similar_check job failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": map[string]any{"job_id": job.ID, "status": "queued", "message": "相似规则检测任务已加入队列"}})
}

// TriggerSandboxTest queues a sandbox test job.
// POST /api/v1/incubator/candidates/:id/test
func (h *IncubatorHandler) TriggerSandboxTest(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	job, err := service.QueueJob("sandbox_test", map[string]any{"incubation_id": uint(id)})
	if err != nil {
		zap.L().Error("queue sandbox_test job failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": map[string]any{"job_id": job.ID, "status": "queued", "message": "模拟测试任务已加入队列"}})
}

// Pipeline returns the full incubation pipeline visualization data.
// GET /api/v1/incubator/pipeline
func (h *IncubatorHandler) Pipeline(c *gin.Context) {
	jobIDStr := c.Query("job_id")
	var jobID *uint
	if jobIDStr != "" {
		id, err := strconv.ParseUint(jobIDStr, 10, 32)
		if err == nil && id > 0 {
			uid := uint(id)
			jobID = &uid
		}
	}
	status, err := h.svc.GetPipelineStatus(jobID)
	if err != nil {
		zap.L().Error("get pipeline status failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": status})
}

// SubscribePipelineEvents SSE endpoint for real-time pipeline status updates.
// GET /api/v1/incubator/pipeline/events?job_id=xxx
func (h *IncubatorHandler) SubscribePipelineEvents(c *gin.Context) {
	jobIDStr := c.Query("job_id")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil || jobID <= 0 {
		c.JSON(400, gin.H{"error": "无效的 job_id"})
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	client := h.svc.SSEHub.Subscribe(jobID, c.Writer)
	defer h.svc.SSEHub.Unsubscribe(jobID, client)

	c.SSEvent("connected", jobIDStr)
	c.Writer.Flush()

	// 连接建立后立即推送一次当前 Job 的完整步骤状态，解决前端错过早期广播的问题
	var job model.RuleIncubationJob
	if err := model.DB.First(&job, uint(jobID)).Error; err == nil {
		var summary map[string]any
		_ = json.Unmarshal([]byte(job.ResultSummary), &summary)
		if stepsMap, ok := summary["pipeline_steps"].(map[string]any); ok {
			for stepID, v := range stepsMap {
				if m, ok := v.(map[string]any); ok {
					if st, ok := m["status"].(string); ok && st != "idle" {
						b, _ := json.Marshal(map[string]any{
							"job_id":          uint(jobID),
							"step_id":         stepID,
							"status":          st,
							"pipeline_status": job.Status,
							"updated_at":      time.Now().Format(time.RFC3339),
						})
						c.SSEvent("pipeline_update", string(b))
						c.Writer.Flush()
					}
				}
			}
		}
	}

	for {
		select {
		case msg, ok := <-client.Chan:
			if !ok {
				return
			}
			c.SSEvent("pipeline_update", msg)
			c.Writer.Flush()
		case <-client.Done:
			return
		case <-c.Request.Context().Done():
			return
		}
	}
}

// CandidateTrace returns the full bloodline trace of a candidate rule.
// GET /api/v1/incubator/candidates/:id/trace
func (h *IncubatorHandler) CandidateTrace(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	trace, err := h.svc.GetCandidateTrace(uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "candidate not found"})
			return
		}
		zap.L().Error("get candidate trace failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": trace})
}

// IssueTrace returns the full lifecycle trace of a review issue.
// GET /api/v1/incubator/issues/:id/trace
func (h *IncubatorHandler) IssueTrace(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	trace, err := h.svc.GetIssueTrace(uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "issue not found"})
			return
		}
		zap.L().Error("get issue trace failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": trace})
}

// RetroMatch queues a retroactive matching job for a published rule.
// POST /api/v1/incubator/retro-match
func (h *IncubatorHandler) RetroMatch(c *gin.Context) {
	var req struct {
		RuleID       uint    `json:"rule_id" binding:"required"`
		LookbackDays int     `json:"lookback_days"`
		Threshold    float64 `json:"threshold"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	job, err := service.QueueJob("retro_match", map[string]any{
		"rule_id":       req.RuleID,
		"lookback_days": req.LookbackDays,
		"threshold":     req.Threshold,
	})
	if err != nil {
		zap.L().Error("queue retro_match job failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": map[string]any{"job_id": job.ID, "status": "queued", "message": "回溯匹配任务已加入队列"}})
}

// RuleHealth returns the health overview of published rules.
// GET /api/v1/incubator/health/rules
func (h *IncubatorHandler) RuleHealth(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "12"))
	if pageSize < 1 {
		pageSize = 12
	}
	if pageSize > 100 {
		pageSize = 100
	}
	alertOnly := c.Query("alert_only") == "true"

	var rules []model.ReviewRule
	if err := model.DB.Where("is_enabled = ?", true).Find(&rules).Error; err != nil {
		zap.L().Error("load rules for health failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	sevenDaysAgo := time.Now().AddDate(0, 0, -7)
	var result []gin.H
	for _, rule := range rules {
		var hitCount int64
		model.DB.Model(&model.ReviewIssue{}).Where("rule_id = ? AND created_at >= ?", rule.ID, sevenDaysAgo).Count(&hitCount)

		var missedCount int64
		model.DB.Model(&model.ReviewIssue{}).
			Where("rule_id IS NULL AND created_at >= ? AND category = ?", sevenDaysAgo, rule.Category).
			Count(&missedCount)

		var totalIssues, rejectedIssues int64
		model.DB.Model(&model.ReviewIssue{}).Where("rule_id = ?", rule.ID).Count(&totalIssues)
		model.DB.Model(&model.ReviewIssue{}).Where("rule_id = ? AND status = ?", rule.ID, "rejected").Count(&rejectedIssues)

		rejectRate := float64(0)
		if totalIssues > 0 {
			rejectRate = float64(rejectedIssues) / float64(totalIssues)
		}

		alertLevel := "healthy"
		if rejectRate > 0.15 || missedCount > 20 {
			alertLevel = "warning"
		}
		if rejectRate > 0.25 || missedCount > 50 {
			alertLevel = "critical"
		}

		var suggestion string
		switch alertLevel {
		case "critical":
			suggestion = "该规则命中率高但拒绝率也很高，或同类未命中问题严重。建议立即审查Prompt，收紧检查条件，防止误报。"
		case "warning":
			suggestion = "该规则健康度下降，存在较多同类未命中问题。建议审查Prompt，补充边界情况说明。"
		default:
			suggestion = ""
		}

		result = append(result, gin.H{
			"rule_id":         rule.ID,
			"rule_name":       rule.Name,
			"rule_code":       rule.Code,
			"hit_count_7d":    hitCount,
			"missed_count_7d": missedCount,
			"reject_rate":     rejectRate,
			"alert_level":     alertLevel,
			"suggestion":      suggestion,
		})
	}

	// Sort: critical > warning > healthy
	order := map[string]int{"critical": 0, "warning": 1, "healthy": 2}
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			ai := result[i]["alert_level"].(string)
			aj := result[j]["alert_level"].(string)
			if order[ai] > order[aj] {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	// Filter if alert_only
	if alertOnly {
		var filtered []gin.H
		for _, r := range result {
			if r["alert_level"].(string) != "healthy" {
				filtered = append(filtered, r)
			}
		}
		result = filtered
	}

	total := len(result)
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	paged := result[start:end]

	c.JSON(200, gin.H{"code": 0, "data": gin.H{
		"list":      paged,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}})
}

// RunPipeline triggers a full automated pipeline run (cluster → refine → similar → test).
// POST /api/v1/incubator/pipeline/run
func (h *IncubatorHandler) RunPipeline(c *gin.Context) {
	var req struct {
		TimeRangeDays int `json:"time_range_days"`
		MinGroupSize  int `json:"min_group_size"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	job, err := h.svc.RunPipeline(req.TimeRangeDays, req.MinGroupSize)
	if err != nil {
		if running, ok := err.(*service.ErrPipelineRunning); ok {
			c.JSON(409, gin.H{
				"error":  running.Error(),
				"job_id": running.JobID,
			})
			return
		}
		zap.L().Error("run pipeline failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"code": 0,
		"data": map[string]any{
			"job_id": job.ID,
			"status": job.Status,
			"message": "流水线已启动",
		},
	})
}
