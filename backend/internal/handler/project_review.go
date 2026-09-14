package handler

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ProjectReviewHandler struct{}

func NewProjectReviewHandler() *ProjectReviewHandler {
	return &ProjectReviewHandler{}
}

// ListRules 获取项目评审规则列表（包含所有规则，新增规则自动 fallback 到全局状态）
func (h *ProjectReviewHandler) ListRules(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	projectID, _ := strconv.Atoi(c.Param("id"))

	// 1. 加载所有规则（全局规则库）
	var allRules []model.ReviewRule
	if err := model.DB.Scopes(model.OrgScope(scope)).Order("sort_order ASC, id ASC").Find(&allRules).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 2. 加载该项目的配置（可能部分规则没有配置记录）
	var configs []model.ProjectReviewConfig
	model.DB.Where("project_id = ?", projectID).Find(&configs)
	configMap := make(map[uint]model.ProjectReviewConfig)
	for _, cfg := range configs {
		configMap[cfg.RuleID] = cfg
	}

	// 3. 组装结果：每条规则都返回，无项目配置时 fallback 到全局状态
	var result []gin.H
	for _, rule := range allRules {
		cfg, hasConfig := configMap[rule.ID]
		isEnabled := rule.IsEnabled   // fallback：全局启用状态
		projSeverity := ""            // fallback：使用规则默认级别
		if hasConfig {
			isEnabled = cfg.IsEnabled // 项目有配置，以项目配置为准
			projSeverity = cfg.Severity
		}
		result = append(result, gin.H{
			"rule_id":          rule.ID,
			"code":             rule.Code,
			"name":             rule.Name,
			"category":         rule.Category,
			"severity":         rule.Severity,
			"language":         rule.Language,
			"description":      rule.Description,
			"is_enabled":       isEnabled,
			"default_severity": rule.Severity,
			"project_severity": projSeverity,
			"has_config":       hasConfig,
		})
	}

	c.JSON(200, gin.H{"code": 0, "data": result})
}

// UpdateRules 批量更新项目规则配置
func (h *ProjectReviewHandler) UpdateRules(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	projectID, _ := strconv.Atoi(c.Param("id"))

	var req struct {
		Rules map[string]struct {
			IsEnabled bool   `json:"is_enabled"`
			Severity  string `json:"severity"`
		} `json:"rules"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	changed := 0
	for ruleIDStr, r := range req.Rules {
		ruleID, _ := strconv.ParseUint(ruleIDStr, 10, 64)

		// 原子 upsert：存在则更新，不存在则创建
		var cfg model.ProjectReviewConfig
		err := model.DB.Where("project_id = ? AND rule_id = ?", projectID, uint(ruleID)).
			Attrs(model.ProjectReviewConfig{
				ProjectID: uint(projectID),
				RuleID:    uint(ruleID),
				IsEnabled: r.IsEnabled,
				Severity:  r.Severity,
			}).
			FirstOrCreate(&cfg).Error
		if err != nil {
			zap.L().Warn("upsert project review config failed",
				zap.Int("project_id", projectID),
				zap.Uint64("rule_id", ruleID),
				zap.Error(err))
			continue
		}

		// 无条件更新，确保值一定被写入（覆盖 GORM 零值跳过问题）
		if err := model.DB.Model(&cfg).UpdateColumns(map[string]interface{}{
			"is_enabled": r.IsEnabled,
			"severity":   r.Severity,
		}).Error; err != nil {
			zap.L().Warn("force update project review config failed",
				zap.Int("project_id", projectID),
				zap.Uint64("rule_id", ruleID),
				zap.Error(err))
			continue
		}
		changed++
	}

	c.JSON(200, gin.H{"message": "updated", "changed": changed})
}

// ResetRules 重置为默认规则配置
func (h *ProjectReviewHandler) ResetRules(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	projectID, _ := strconv.Atoi(c.Param("id"))

	// 先查项目确认存在并获取 org_id（scope过滤）
	var project model.Project
	pdb := model.DB
	if !scope.IsSuperAdmin {
		pdb = pdb.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := pdb.First(&project, projectID).Error; err != nil {
		c.JSON(404, gin.H{"error": "project not found"})
		return
	}

	// 删除现有配置（按组织过滤）
	db := model.DB.Where("org_id = ? AND project_id = ?", project.OrgID, projectID)
	db.Delete(&model.ProjectReviewConfig{})

	// 重新生成
	var rules []model.ReviewRule
	rdb := model.DB.Where("is_enabled = ? AND (language = 'common' OR language = ?)", true, project.Language)
	if !scope.IsSuperAdmin {
		rdb = rdb.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	rdb.Find(&rules)

	for _, rule := range rules {
		cfg := model.ProjectReviewConfig{
			OrgID:     project.OrgID,
			ProjectID: uint(projectID),
			RuleID:    rule.ID,
			IsEnabled: true,
			Severity:  "",
		}
		if err := model.DB.Create(&cfg).Error; err != nil {
			zap.L().Warn("reset project review config failed", zap.Error(err))
		}
	}

	c.JSON(200, gin.H{"message": "reset"})
}

// QueryStructuredReview 查询 Task 结构化评审结果
func (h *ProjectReviewHandler) QueryStructuredReview(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	taskID, _ := strconv.Atoi(c.Param("id"))
	if !CanAccessTask(scope, uint(taskID)) {
		c.JSON(403, gin.H{"error": "无权访问此任务"})
		return
	}

	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		c.JSON(404, gin.H{"error": "task not found"})
		return
	}

	if task.AIResponseJSON == "" {
		c.JSON(200, gin.H{"code": 0, "data": nil, "message": "无结构化评审数据"})
		return
	}

	// 加载 issues
	var issues []model.ReviewIssue
	model.DB.Where("task_id = ?", taskID).Order("severity DESC, id ASC").Find(&issues)

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"total_score":      task.ScoreValue,
			"raw_ai_score":     task.RawAIScore,
			"dimension_scores": task.DimensionScores,
			"issue_count":      task.IssueCount,
			"ai_response_json": task.AIResponseJSON,
			"issues":           issues,
			"markdown_comment": task.AIResponse,
		},
	})
}

// BatchResolveIssues 批量处理 Issue 状态（已处理/误报/忽略/恢复待处理）
func (h *ProjectReviewHandler) BatchResolveIssues(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var req struct {
		Issues []struct {
			ID           uint   `json:"id"`
			Status       string `json:"status" binding:"required"` // resolved / false_positive / ignored / pending
			RejectReason string `json:"reject_reason"`
		} `json:"issues" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	userID, exist := c.Get("user_id")
	if !exist {
		c.JSON(401, gin.H{"error": "未认证"})
		return
	}
	operatorID := userID.(uint)
	now := time.Now()

	statusLabels := map[string]string{
		"resolved":       "已处理",
		"false_positive": "误报",
		"ignored":        "忽略",
		"pending":        "恢复待处理",
	}

	for _, item := range req.Issues {
		if item.Status != "resolved" && item.Status != "false_positive" && item.Status != "ignored" && item.Status != "pending" {
			c.JSON(400, gin.H{"error": fmt.Sprintf("invalid status for issue %d", item.ID)})
			return
		}

		// 查询 Issue 所属任务ID，并校验跨表权限（GORM Updates 不支持 Joins，改用子查询）
		var issue model.ReviewIssue
		if err := model.DB.First(&issue, item.ID).Error; err != nil {
			zap.L().Warn("issue not found", zap.Uint("issue_id", item.ID), zap.Error(err))
			continue
		}
		taskID := issue.TaskID

		// 子查询校验：该 issue 的 task 所属 org 是否在可见范围内
		var count int64
		if err := model.DB.Table("tasks").
			Where("id = ? AND org_id IN ?", taskID, scope.VisibleOrgIDs).
			Count(&count).Error; err != nil {
			zap.L().Warn("verify issue permission failed", zap.Uint("issue_id", item.ID), zap.Error(err))
			continue
		}
		if count == 0 {
			zap.L().Warn("issue not in visible orgs", zap.Uint("issue_id", item.ID), zap.Uint("task_id", taskID))
			continue
		}

		updates := map[string]interface{}{
			"status":       item.Status,
			"resolved_by":  operatorID,
			"resolved_at":  now,
			"is_resolved":  item.Status == "resolved" || item.Status == "false_positive" || item.Status == "ignored",
		}
		// 误报/忽略时记录原因
		if item.Status == "false_positive" || item.Status == "ignored" {
			updates["reject_reason"] = item.RejectReason
		}

		if err := model.DB.Model(&model.ReviewIssue{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
			zap.L().Warn("update review issue status failed",
				zap.Uint("issue_id", item.ID),
				zap.Error(err))
			model.RecordOpLog("Issue处理", fmt.Sprintf("任务ID：%d，Issue ID：%d 状态变更为 %s", taskID, item.ID, statusLabels[item.Status]), item.ID, operatorID, "failed", err.Error(), c.ClientIP())
			continue
		}

		// 记录操作日志
		opDetail := fmt.Sprintf("任务ID：%d，Issue ID：%d 状态变更为 %s", taskID, item.ID, statusLabels[item.Status])
		model.RecordOpLog("Issue处理", opDetail, item.ID, operatorID, "success", "", c.ClientIP())
	}

	c.JSON(200, gin.H{"message": "updated", "count": len(req.Issues)})
}
