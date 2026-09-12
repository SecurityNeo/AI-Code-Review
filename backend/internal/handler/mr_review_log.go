package handler

import (
	"strconv"
	"strings"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/gitlab"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type MRStats struct {
	TotalAdditions   int64   `json:"total_additions"`
	TotalDeletions   int64   `json:"total_deletions"`
	TotalReviewCount int64   `json:"total_review_count"`
	AvgScore         float64 `json:"avg_score"`
	MergedCount      int64   `json:"merged_count"`
	OpenedCount      int64   `json:"opened_count"`
	ClosedCount      int64   `json:"closed_count"`
}

type MRReviewLogHandler struct{}

func NewMRReviewLogHandler() *MRReviewLogHandler {
	return &MRReviewLogHandler{}
}

func (h *MRReviewLogHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize > 100 {
		pageSize = 100
	}

	projectName := c.Query("project_name")
	author := c.Query("author")
	mrState := c.Query("mr_state")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	var logs []model.MergeRequestReviewLog
	var total int64

	db := model.DB.Model(&model.MergeRequestReviewLog{})
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	} else {
		// super_admin 如果显式传了 org_id query，按指定组织过滤
		if orgIDStr := c.Query("org_id"); orgIDStr != "" {
			if orgID, err := strconv.Atoi(orgIDStr); err == nil && orgID > 0 {
				db = db.Where("org_id = ?", uint(orgID))
			}
		}
	}

	// 按用户角色过滤：developer 只能看自己的；废弃 users.role
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		db = db.Where("author = ?", user.GitlabUsername)
	}

	if projectName != "" {
		db = db.Where("project_name = ?", projectName)
	}
	if author != "" {
		db = db.Where("author = ?", author)
	}
	if mrState != "" {
		db = db.Where("mr_state = ?", mrState)
	}
	if startDate != "" {
		db = db.Where("COALESCE(mr_created_at, synced_at) >= ?", startDate)
	}
	if endDate != "" {
		db = db.Where("COALESCE(mr_created_at, synced_at) <= ?", endDate+" 23:59:59")
	}

	if err := db.Count(&total).Error; err != nil {
		zap.L().Error("count mr review logs failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	if err := db.Order("COALESCE(mr_created_at, synced_at) DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&logs).Error; err != nil {
		zap.L().Error("list mr review logs failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(logs))
	for _, l := range logs {
		orgIDs = append(orgIDs, l.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range logs {
		logs[i].OrgName = orgNameMap[logs[i].OrgID]
	}

	// 聚合统计（基于同样的筛选条件，使用新 Session 避免影响主查询）
	var stats MRStats
	type aggResult struct {
		SumAdditions   int64
		SumDeletions   int64
		SumReviewCount int64
		SumScore       float64
		ScoreCount     int64
	}
	var agg aggResult
	if err := db.Session(&gorm.Session{}).Offset(-1).Limit(-1).Select(
		"COALESCE(SUM(additions),0) as sum_additions, " +
			"COALESCE(SUM(deletions),0) as sum_deletions, " +
			"COALESCE(SUM(review_count),0) as sum_review_count, " +
			"COALESCE(SUM(score),0) as sum_score, " +
			"COUNT(CASE WHEN score > 0 THEN 1 END) as score_count").
		Scan(&agg).Error; err != nil {
		zap.L().Error("aggregate mr review logs failed", zap.Error(err))
	} else {
		stats.TotalAdditions = agg.SumAdditions
		stats.TotalDeletions = agg.SumDeletions
		stats.TotalReviewCount = agg.SumReviewCount
		if agg.ScoreCount > 0 {
			stats.AvgScore = agg.SumScore / float64(agg.ScoreCount)
		}
	}

	// 状态分组统计（merged / opened / closed）
	// 与主列表共享 project / author / mr_state / 日期 + FilterByUser 筛选条件
	var stateResults []struct {
		MRState string
		Count   int64
	}
	stateDB := model.DB.Model(&model.MergeRequestReviewLog{}).
		Select("mr_state, COUNT(*) as count").
		Where("mr_state IN ?", []string{"merged", "opened", "closed"}).
		Scopes(model.OrgScope(scope))
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		stateDB = stateDB.Where("author = ?", user.GitlabUsername)
	}
	if projectName != "" {
		stateDB = stateDB.Where("project_name = ?", projectName)
	}
	if author != "" {
		stateDB = stateDB.Where("author = ?", author)
	}
	if mrState != "" {
		stateDB = stateDB.Where("mr_state = ?", mrState)
	}
	if startDate != "" {
		stateDB = stateDB.Where("COALESCE(mr_created_at, synced_at) >= ?", startDate)
	}
	if endDate != "" {
		stateDB = stateDB.Where("COALESCE(mr_created_at, synced_at) <= ?", endDate+" 23:59:59")
	}
	if err := stateDB.Group("mr_state").Scan(&stateResults).Error; err != nil {
		zap.L().Error("aggregate mr state counts failed", zap.Error(err))
	} else {
		for _, r := range stateResults {
			switch r.MRState {
			case "merged":
				stats.MergedCount = r.Count
			case "opened":
				stats.OpenedCount = r.Count
			case "closed":
				stats.ClosedCount = r.Count
			}
		}
	}

	c.JSON(200, gin.H{
		"data":      logs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"stats":     stats,
	})
}

// MarkAsDraft 将 MR 标记为 Draft
func (h *MRReviewLogHandler) MarkAsDraft(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))

	var log model.MergeRequestReviewLog
	if err := model.DB.First(&log, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "mr not found"})
		return
	}

	// 查找项目配置获取 GitLab Project ID 和 token
	var project model.Project
	if err := model.DB.Scopes(model.OrgScope(scope)).Where("name = ?", log.ProjectName).First(&project).Error; err != nil {
		c.JSON(500, gin.H{"error": "project not found"})
		return
	}

	if project.GitLabProjectID == 0 {
		c.JSON(400, gin.H{"error": "gitlab project id not configured"})
		return
	}

	token := project.AccessToken
	if token == "" {
		var cfg model.SystemConfig
		if err := model.DB.First(&cfg).Error; err == nil {
			token = cfg.GitlabToken
		}
	}

	// 1. 获取当前 MR 详情（title）
	_, mrIID, err := gitlab.ParseMRFidFromURL(log.URL)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid mr url"})
		return
	}

	host := gitlab.ExtractGitLabHost(log.URL)
	if host == "" {
		c.JSON(400, gin.H{"error": "cannot extract gitlab host"})
		return
	}

	client := gitlab.NewClient(host, token)
	mr, err := client.GetMergeRequest(project.GitLabProjectID, mrIID)
	if err != nil {
		zap.L().Error("get mr details failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "get mr from gitlab failed: " + err.Error()})
		return
	}

	newTitle := mr.Title
	if !strings.HasPrefix(newTitle, "[Draft]:") && !strings.HasPrefix(newTitle, "Draft:") {
		newTitle = "[Draft]: " + newTitle
	}

	// 2. 调用 GitLab API 更新 MR title
	if err := client.UpdateMergeRequestTitle(project.GitLabProjectID, mrIID, newTitle); err != nil {
		zap.L().Error("update mr title failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "update mr title failed: " + err.Error()})
		return
	}

	// 3. 更新本地记录
	model.DB.Model(&log).Updates(map[string]interface{}{
		"is_draft": true,
		"mr_title": newTitle,
	})

	c.JSON(200, gin.H{"message": "marked as draft", "title": newTitle})
}

// MarkAsReady 将 MR 从 Draft 标记为 ready（移除 [Draft]: 前缀）
func (h *MRReviewLogHandler) MarkAsReady(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))

	var log model.MergeRequestReviewLog
	if err := model.DB.First(&log, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "mr not found"})
		return
	}

	var project model.Project
	if err := model.DB.Scopes(model.OrgScope(scope)).Where("name = ?", log.ProjectName).First(&project).Error; err != nil {
		c.JSON(500, gin.H{"error": "project not found"})
		return
	}
	if project.GitLabProjectID == 0 {
		c.JSON(400, gin.H{"error": "gitlab project id not configured"})
		return
	}

	token := project.AccessToken
	if token == "" {
		var cfg model.SystemConfig
		if err := model.DB.First(&cfg).Error; err == nil {
			token = cfg.GitlabToken
		}
	}

	_, mrIID, err := gitlab.ParseMRFidFromURL(log.URL)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid mr url"})
		return
	}

	host := gitlab.ExtractGitLabHost(log.URL)
	if host == "" {
		c.JSON(400, gin.H{"error": "cannot extract gitlab host"})
		return
	}

	client := gitlab.NewClient(host, token)
	mr, err := client.GetMergeRequest(project.GitLabProjectID, mrIID)
	if err != nil {
		zap.L().Error("get mr details failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "get mr from gitlab failed: " + err.Error()})
		return
	}

	newTitle := mr.Title
	// 移除可能的 Draft 前缀
	if strings.HasPrefix(newTitle, "[Draft]:") {
		newTitle = strings.TrimSpace(strings.TrimPrefix(newTitle, "[Draft]:"))
	} else if strings.HasPrefix(newTitle, "Draft:") {
		newTitle = strings.TrimSpace(strings.TrimPrefix(newTitle, "Draft:"))
	}

	if err := client.UpdateMergeRequestTitle(project.GitLabProjectID, mrIID, newTitle); err != nil {
		zap.L().Error("update mr title failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "update mr title failed: " + err.Error()})
		return
	}

	model.DB.Model(&log).Updates(map[string]interface{}{
		"is_draft": false,
		"mr_title": newTitle,
	})

	c.JSON(200, gin.H{"message": "marked as ready", "title": newTitle})
}

func (h *MRReviewLogHandler) Projects(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	db := model.DB.Model(&model.MergeRequestReviewLog{}).Scopes(model.OrgScope(scope))

	var orgID uint
	if orgIDStr := c.Query("org_id"); orgIDStr != "" {
		if id, err := strconv.ParseUint(orgIDStr, 10, 64); err == nil {
			orgID = uint(id)
		}
	}
	// 非 super_admin 未显式传参时 fallback 到当前默认组织
	// super_admin 未传参时不限制（返回所有组织）
	if orgID == 0 && !scope.IsSuperAdmin {
		orgID = middleware.GetCurrentOrgID(c)
	}
	if orgID > 0 {
		db = db.Where("org_id = ?", orgID)
	}

	var projects []string
	db.Distinct("project_name").Pluck("project_name", &projects)
	c.JSON(200, gin.H{"data": projects})
}

func (h *MRReviewLogHandler) Authors(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	db := model.DB.Model(&model.MergeRequestReviewLog{}).Scopes(model.OrgScope(scope))

	var orgID uint
	if orgIDStr := c.Query("org_id"); orgIDStr != "" {
		if id, err := strconv.ParseUint(orgIDStr, 10, 64); err == nil {
			orgID = uint(id)
		}
	}
	// 非 super_admin 未显式传参时 fallback 到当前默认组织
	// super_admin 未传参时不限制（返回所有组织）
	if orgID == 0 && !scope.IsSuperAdmin {
		orgID = middleware.GetCurrentOrgID(c)
	}
	if orgID > 0 {
		db = db.Where("org_id = ?", orgID)
	}

	var authors []string
	db.Distinct("author").Where("author != ?", "").Pluck("author", &authors)
	c.JSON(200, gin.H{"data": authors})
}
