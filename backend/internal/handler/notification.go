package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
)

// NotificationHandler 站内信/通知中心 Handler
type NotificationHandler struct {
}

func NewNotificationHandler() *NotificationHandler {
	return &NotificationHandler{}
}

// List 站内信列表
func (h *NotificationHandler) List(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	notifType := c.Query("type")
	isReadStr := c.Query("is_read")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	var isRead *bool
	if isReadStr == "true" {
		t := true
		isRead = &t
	} else if isReadStr == "false" {
		t := false
		isRead = &t
	}

	svc := service.NewNotificationService()
	list, total, err := svc.List(user.ID, notifType, isRead, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// UnreadCount 未读数
func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	svc := service.NewNotificationService()
	count := svc.UnreadCount(user.ID)
	c.JSON(http.StatusOK, gin.H{"data": count})
}

// MarkRead 单条已读
func (h *NotificationHandler) MarkRead(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	svc := service.NewNotificationService()
	if err := svc.MarkRead(user.ID, uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// MarkAllRead 全部已读
func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	svc := service.NewNotificationService()
	if err := svc.MarkAllRead(user.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// TestRule 测试通知规则模板
func (h *NotificationHandler) TestRule(c *gin.Context) {
	var body struct {
		Template     string `json:"template"`
		TemplateType string `json:"template_type"` // "im_escalation" 或空
	}
	_ = c.ShouldBindJSON(&body)
	template := body.Template

	if template == "" {
		// 回退到从数据库读取
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		if id > 0 {
			var rule model.NotificationRule
			if err := model.DB.First(&rule, id).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			template = rule.Template
		}
	}

	if template == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template is required"})
		return
	}

	// 构造测试上下文（使用最近一条任务）
	var task model.Task
	model.DB.Order("id DESC").First(&task)
	model.DB.First(&task.Project, task.ProjectID)

	var rendered string
	if body.TemplateType == "im_escalation" {
		// Issue 升级 IM 模板：使用 RenderIMTemplate + 模拟数据
		ctx := service.IMTemplateContext{
			Project:     task.Project,
			Task:        task,
			Developer:   task.MRAuthor,
			AtRecipient: "<@wxid_demo_steward>",
			IssueCount:  3,
			IssueList: []service.IMIssueItem{
				{ID: 72, Message: "存在信息泄露风险"},
				{ID: 73, Message: "未处理 nil 指针"},
				{ID: 74, Message: "SQL注入风险"},
			},
			IssueID:      72,
			IssueMessage: "存在信息泄露风险",
		}
		rendered = service.RenderIMTemplate(template, ctx)
	} else {
		// 通用模板
		stats := service.CalcIssueStats(task.ID, task.MRMergeID)
		ctx := service.TemplateContext{
			Task:          task,
			Stats:         stats,
			Project:       task.Project,
			Developer:     task.MRAuthor,
			DeadlineHours: 120,
		}
		rendered = service.RenderMessage(template, ctx)
	}

	c.JSON(http.StatusOK, gin.H{"data": rendered})
}

// GetRule 获取单条通知规则
func (h *NotificationHandler) GetRule(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var rule model.NotificationRule
	if err := model.DB.First(&rule, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rule})
}

// ProjectOwnerDashboard 项目负责人工作台（项目治理视图）
// 管理员可查看所有项目，普通用户仅查看自己负责的项目
func (h *NotificationHandler) ProjectOwnerDashboard(c *gin.Context) {
	user := c.MustGet("user").(model.User)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var projectIDs []uint
	debugInfo := gin.H{
		"current_user_id":         user.ID,
		"current_gitlab_username": user.GitlabUsername,
		"role":                    user.Role,
	}

	if user.Role == model.RoleAdmin {
		// 管理员：返回所有项目
		var allProjects []model.Project
		model.DB.Select("id").Find(&allProjects)
		projectIDs = make([]uint, 0, len(allProjects))
		for _, p := range allProjects {
			projectIDs = append(projectIDs, p.ID)
		}
		debugInfo["mode"] = "admin_all_projects"
	} else {
		// 普通用户：只返回自己负责的项目
		var responsibilities []model.ProjectResponsibility
		if user.GitlabUsername != "" {
			model.DB.Joins("INNER JOIN team_members ON team_members.id = project_responsibilities.member_id").
				Where("team_members.gitlab_username = ?", user.GitlabUsername).
				Find(&responsibilities)
		}
		projectIDs = make([]uint, 0, len(responsibilities))
		for _, r := range responsibilities {
			projectIDs = append(projectIDs, r.ProjectID)
		}
		debugInfo["mode"] = "user_steward_projects"
		debugInfo["responsibility_records"] = len(responsibilities)
	}
	debugInfo["project_ids"] = projectIDs

	if len(projectIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"data":  gin.H{"projects": []interface{}{}, "total": 0, "page": page, "page_size": pageSize},
			"debug": debugInfo,
		})
		return
	}

	type projInfo struct {
		ID              uint     `json:"id"`
		Name            string   `json:"name"`
		ProjectPath     string   `json:"project_path"`
		GitlabProjectID int      `json:"gitlab_project_id"`
		Language        string   `json:"language"`
		StewardCount    int64    `json:"steward_count"`
		Stewards        []string `json:"stewards"`
		PendingCount    int64    `json:"pending_count"`
		CriticalCount   int64    `json:"critical_count"`
		HighCount       int64    `json:"high_count"`
		OverdueCount    int64    `json:"overdue_count"`
		WeekCloseRate   float64  `json:"week_close_rate"`
		AvgResolveDays  float64  `json:"avg_resolve_days"`
	}

	// 查询项目基础信息（用原生模型查询，避免 GORM Scan 兼容性问题）
	var rawProjects []model.Project
	if err := model.DB.Where("id IN ?", projectIDs).Find(&rawProjects).Error; err != nil {
		debugInfo["project_query_error"] = err.Error()
		c.JSON(http.StatusOK, gin.H{
			"data":  gin.H{"projects": []interface{}{}, "total": 0, "page": page, "page_size": pageSize},
			"debug": debugInfo,
		})
		return
	}

	total := len(rawProjects)
	start := (page - 1) * pageSize
	if start >= total {
		c.JSON(http.StatusOK, gin.H{
			"data":  gin.H{"projects": []interface{}{}, "total": total, "page": page, "page_size": pageSize},
			"debug": debugInfo,
		})
		return
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageProjects := rawProjects[start:end]

	projects := make([]projInfo, 0, len(pageProjects))
	for _, rp := range pageProjects {
		projects = append(projects, projInfo{
			ID:              rp.ID,
			Name:            rp.Name,
			ProjectPath:     rp.ProjectPath,
			GitlabProjectID: rp.GitLabProjectID,
			Language:        rp.Language,
		})
	}

	overdueSince := time.Now().AddDate(0, 0, -5)
	sevenDaysAgo := time.Now().AddDate(0, 0, -7)

	for i := range projects {
		p := &projects[i]
		model.DB.Model(&model.ProjectResponsibility{}).Where("project_id = ?", p.ID).Count(&p.StewardCount)

		// 查询负责人名称列表
		var stewards []struct{ Username string }
		model.DB.Raw(`
			SELECT COALESCE(tm.display_name, tm.username) AS username 
			FROM project_responsibilities pr
			JOIN team_members tm ON tm.id = pr.member_id
			WHERE pr.project_id = ?
			ORDER BY tm.display_name
		`, p.ID).Scan(&stewards)
		p.Stewards = make([]string, 0, len(stewards))
		for _, s := range stewards {
			if s.Username != "" {
				p.Stewards = append(p.Stewards, s.Username)
			}
		}

		model.DB.Model(&model.ReviewIssue{}).
			Where("review_issues.deleted_at IS NULL AND review_issues.status IN (?)", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}).
			Joins("INNER JOIN tasks ON tasks.id = review_issues.task_id").
			Where("tasks.project_id = ?", p.ID).
			Count(&p.PendingCount)
		model.DB.Model(&model.ReviewIssue{}).
			Where("review_issues.deleted_at IS NULL AND review_issues.status IN (?) AND review_issues.severity = ?", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, "critical").
			Joins("INNER JOIN tasks ON tasks.id = review_issues.task_id").
			Where("tasks.project_id = ?", p.ID).
			Count(&p.CriticalCount)
		model.DB.Model(&model.ReviewIssue{}).
			Where("review_issues.deleted_at IS NULL AND review_issues.status IN (?) AND review_issues.severity = ?", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, "high").
			Joins("INNER JOIN tasks ON tasks.id = review_issues.task_id").
			Where("tasks.project_id = ?", p.ID).
			Count(&p.HighCount)
		model.DB.Model(&model.ReviewIssue{}).
			Where("review_issues.deleted_at IS NULL AND review_issues.status IN (?) AND review_issues.original_created_at < ?", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, overdueSince).
			Joins("INNER JOIN tasks ON tasks.id = review_issues.task_id").
			Where("tasks.project_id = ?", p.ID).
			Count(&p.OverdueCount)

		var weekCreated, weekClosed int64
		model.DB.Model(&model.ReviewIssue{}).
			Joins("INNER JOIN tasks ON tasks.id = review_issues.task_id").
			Where("tasks.project_id = ? AND review_issues.deleted_at IS NULL AND review_issues.created_at >= ?", p.ID, sevenDaysAgo).
			Count(&weekCreated)
		model.DB.Model(&model.ReviewIssue{}).
			Where("review_issues.deleted_at IS NULL AND review_issues.resolved_at >= ? AND review_issues.status IN (?)", sevenDaysAgo,
				[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).
			Joins("INNER JOIN tasks ON tasks.id = review_issues.task_id").
			Where("tasks.project_id = ?", p.ID).
			Count(&weekClosed)
		if weekCreated > 0 {
			p.WeekCloseRate = float64(weekClosed) / float64(weekCreated) * 100
		}

		var durData []struct{ DurationSec float64 }
		model.DB.Raw(`SELECT TIMESTAMPDIFF(SECOND, review_issues.original_created_at, review_issues.resolved_at) AS duration_sec
			FROM review_issues INNER JOIN tasks ON tasks.id = review_issues.task_id
			WHERE review_issues.deleted_at IS NULL AND review_issues.status IN (?) AND review_issues.resolved_at IS NOT NULL
				AND tasks.project_id = ?`,
			[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}, p.ID).Scan(&durData)
		if len(durData) > 0 {
			var totalDur float64
			for _, d := range durData {
				totalDur += d.DurationSec / 86400.0
			}
			p.AvgResolveDays = totalDur / float64(len(durData))
		}
	}

	debugInfo["projects_returned"] = len(projects)
	c.JSON(http.StatusOK, gin.H{
		"data":  gin.H{"projects": projects, "total": total, "page": page, "page_size": pageSize},
		"debug": debugInfo,
	})
}

// --- Dashboard / 工作台 ---

// DeveloperDashboard 开发者工作台数据
func (h *NotificationHandler) DeveloperDashboard(c *gin.Context) {
	user := c.MustGet("user").(model.User)

	// 待处理统计（包含 pending 和 pending_inherited）
	var pendingCount, criticalCount, highCount int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?)",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, user.ID, user.ID).
		Count(&pendingCount)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND severity = ? AND (owner_id = ? OR current_owner_id = ?)",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, "critical", user.ID, user.ID).
		Count(&criticalCount)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND severity = ? AND (owner_id = ? OR current_owner_id = ?)",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, "high", user.ID, user.ID).
		Count(&highCount)

	// 本周统计（简化：近7天）
	var weekReceived, weekResolved int64
	since := time.Now().AddDate(0, 0, -7)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND created_at >= ?",
			user.ID, user.ID, since).
		Count(&weekReceived)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND resolved_at >= ? AND status IN (?)",
			user.ID, user.ID, since, []string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).
		Count(&weekResolved)

	// 分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	// 待处理列表（增强：携带项目信息、任务MR信息）
	var totalIssues int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("review_issues.deleted_at IS NULL AND review_issues.status IN (?) AND (review_issues.owner_id = ? OR review_issues.current_owner_id = ?)",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, user.ID, user.ID).
		Count(&totalIssues)

	var rawItems []struct {
		model.ReviewIssue
		ProjectName string `json:"project_name"`
		MRTitle     string `json:"mr_title"`
	}
	model.DB.Model(&model.ReviewIssue{}).
		Select("review_issues.*, projects.name as project_name, tasks.mr_title as mr_title").
		Joins("LEFT JOIN tasks ON tasks.id = review_issues.task_id").
		Joins("LEFT JOIN projects ON projects.id = tasks.project_id").
		Where("review_issues.deleted_at IS NULL AND review_issues.status IN (?) AND (review_issues.owner_id = ? OR review_issues.current_owner_id = ?)",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, user.ID, user.ID).
		Order("review_issues.escalation_level ASC, review_issues.severity DESC, review_issues.original_created_at ASC").
		Limit(pageSize).Offset(offset).Scan(&rawItems)

	// 转换为前端需要的格式（避免 embedded struct 字段名冲突）
	type issueItem struct {
		ID                   uint       `json:"id"`
		TaskID               uint       `json:"task_id"`
		MRID                 int        `json:"mr_id"`
		RuleID               *uint      `json:"rule_id"`
		RuleCode             string     `json:"rule_code"`
		Category             string     `json:"category"`
		Severity             string     `json:"severity"`
		DeductScore          int        `json:"deduct_score"`
		File                 string     `json:"file"`
		LineStart            int        `json:"line_start"`
		LineEnd              int        `json:"line_end"`
		CodeSnippet          string     `json:"code_snippet"`
		Message              string     `json:"message"`
		Suggestion           string     `json:"suggestion"`
		Status               string     `json:"status"`
		ResolvedBy           uint       `json:"resolved_by"`
		ResolvedAt           *time.Time `json:"resolved_at"`
		RejectReason         string     `json:"reject_reason"`
		GitlabDiscussionID   string     `json:"gitlab_discussion_id"`
		IsResolved           bool       `json:"is_resolved"`
		Fingerprint          string     `json:"fingerprint"`
		InheritedFromIssueID *uint      `json:"inherited_from_issue_id"`
		OwnerID              *uint      `json:"owner_id"`
		CurrentOwnerID       *uint      `json:"current_owner_id"`
		OriginalCreatedAt    *time.Time `json:"original_created_at"`
		EscalationLevel      int        `json:"escalation_level"`
		CreatedAt            time.Time  `json:"created_at"`
		ProjectName          string     `json:"project_name"`
		MRTitle              string     `json:"mr_title"`
	}

	issues := make([]issueItem, len(rawItems))
	for i, raw := range rawItems {
		issues[i] = issueItem{
			ID:                   raw.ID,
			TaskID:               raw.TaskID,
			MRID:                 raw.MRID,
			RuleID:               raw.RuleID,
			RuleCode:             raw.RuleCode,
			Category:             raw.Category,
			Severity:             raw.Severity,
			DeductScore:          raw.DeductScore,
			File:                 raw.File,
			LineStart:            raw.LineStart,
			LineEnd:              raw.LineEnd,
			CodeSnippet:          raw.CodeSnippet,
			Message:              raw.Message,
			Suggestion:           raw.Suggestion,
			Status:               raw.Status,
			ResolvedBy:           raw.ResolvedBy,
			ResolvedAt:           raw.ResolvedAt,
			RejectReason:         raw.RejectReason,
			GitlabDiscussionID:   raw.GitlabDiscussionID,
			IsResolved:           raw.IsResolved,
			Fingerprint:          raw.Fingerprint,
			InheritedFromIssueID: raw.InheritedFromIssueID,
			OwnerID:              raw.OwnerID,
			CurrentOwnerID:       raw.CurrentOwnerID,
			OriginalCreatedAt:    raw.OriginalCreatedAt,
			EscalationLevel:      raw.EscalationLevel,
			CreatedAt:            raw.CreatedAt,
			ProjectName:          raw.ProjectName,
			MRTitle:              raw.MRTitle,
		}
	}

	// 待处理 Issue severity 分组统计 + 最早未处理距今
	var mediumCount, lowCount int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND severity = ? AND (owner_id = ? OR current_owner_id = ?)",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, "medium", user.ID, user.ID).
		Count(&mediumCount)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND severity = ? AND (owner_id = ? OR current_owner_id = ?)",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, "low", user.ID, user.ID).
		Count(&lowCount)

	// 最早未处理距今
	var earliestPending model.ReviewIssue
	err := model.DB.Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?)",
		[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, user.ID, user.ID).
		Order("original_created_at ASC").First(&earliestPending).Error

	var earliestPendingHours interface{} = nil
	if err == nil {
		t := earliestPending.OriginalCreatedAt
		if t == nil || t.IsZero() {
			// 旧数据可能没有 original_created_at，回退到 created_at
			t = &earliestPending.CreatedAt
		}
		if t != nil && !t.IsZero() {
			earliestPendingHours = t
		}
	}

	// 平均闭环天数（近30天已闭环的）
	var avgCloseDays float64
	var closeData []struct{ DurationSec float64 }
	model.DB.Raw(`SELECT TIMESTAMPDIFF(SECOND, original_created_at, resolved_at) AS duration_sec
		FROM review_issues
		WHERE deleted_at IS NULL AND status IN (?) AND resolved_at IS NOT NULL
			AND resolved_at >= ? AND (owner_id = ? OR current_owner_id = ?)`,
		[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored},
		time.Now().AddDate(0, 0, -30), user.ID, user.ID).Scan(&closeData)
	if len(closeData) > 0 {
		var total float64
		for _, d := range closeData {
			total += d.DurationSec / 86400.0
		}
		avgCloseDays = total / float64(len(closeData))
	}

	// 团队排名（基于闭环率）
	type userRank struct {
		UserID       uint  `json:"user_id"`
		TotalCreated int64 `json:"total_created"`
		TotalClosed  int64 `json:"total_closed"`
	}
	var ranks []userRank
	model.DB.Raw(`SELECT 
			owner_id AS user_id,
			COUNT(*) AS total_created,
			COUNT(CASE WHEN status IN (?) THEN 1 END) AS total_closed
		FROM review_issues
		WHERE deleted_at IS NULL AND created_at >= ?
		GROUP BY owner_id
		HAVING total_created > 0`,
		[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored},
		time.Now().AddDate(0, 0, -30)).Scan(&ranks)
	teamRank := 0
	position := 0
	totalRankers := len(ranks)
	for i, r := range ranks {
		if r.TotalCreated > 0 {
			closeRate := float64(r.TotalClosed) / float64(r.TotalCreated)
			if r.UserID == user.ID {
				position = i + 1
				teamRank = int(closeRate * 100)
			}
		}
	}

	// 近4周趋势
	type trendItem struct {
		Week         string `json:"week"`
		Received     int64  `json:"received"`
		Closed       int64  `json:"closed"`
		StillPending int64  `json:"still_pending"`
	}
	var trends []trendItem
	for i := 3; i >= 0; i-- {
		weekStart := time.Now().AddDate(0, 0, -7*(i+1)).Truncate(24 * time.Hour)
		weekEnd := weekStart.AddDate(0, 0, 7)
		var wRecv, wClose, wPending int64
		model.DB.Model(&model.ReviewIssue{}).
			Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND created_at >= ? AND created_at < ?", user.ID, user.ID, weekStart, weekEnd).
			Count(&wRecv)
		model.DB.Model(&model.ReviewIssue{}).
			Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND resolved_at >= ? AND resolved_at < ? AND status IN (?)", user.ID, user.ID, weekStart, weekEnd,
				[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).
			Count(&wClose)
		model.DB.Model(&model.ReviewIssue{}).
			Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?) AND created_at <= ?", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, user.ID, user.ID, weekEnd).
			Count(&wPending)
		trends = append(trends, trendItem{Week: weekStart.Format("01/02") + "-" + weekEnd.Format("01/02"), Received: wRecv, Closed: wClose, StillPending: wPending})
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"pending_count":          pendingCount,
		"critical_count":         criticalCount,
		"high_count":             highCount,
		"medium_count":           mediumCount,
		"low_count":              lowCount,
		"week_received":          weekReceived,
		"week_resolved":          weekResolved,
		"average_close_days":     fmt.Sprintf("%.1f", avgCloseDays),
		"earliest_pending_hours": earliestPendingHours,
		"team_rank_percent":      teamRank,
		"team_rank_position":     position,
		"team_rank_total":        totalRankers,
		"trends":                 trends,
		"issues":                 issues,
		"total":                  totalIssues,
	}})
}

// AdminDashboard 管理员全局大盘
func (h *NotificationHandler) AdminDashboard(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	if user.Role != model.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var todayNew, totalPending, overdue int64
	todayStart := time.Now().Truncate(24 * time.Hour)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND created_at >= ?", todayStart).Count(&todayNew)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?)", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}).Count(&totalPending)

	// overdue: pending > 120 work hours（简化用 5 天）
	overdueSince := time.Now().AddDate(0, 0, -5)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND original_created_at < ?", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, overdueSince).Count(&overdue)

	// 归档统计
	var todayArchived, totalArchived, todayEscalated int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status = ? AND updated_at >= ?", model.IssueStatusAutoArchived, todayStart).Count(&todayArchived)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status = ?", model.IssueStatusAutoArchived).Count(&totalArchived)
	// 今日升级：escalation_level 今日发生变化（排除已归档）
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND escalation_level > 0 AND status != ? AND updated_at >= ?", model.IssueStatusAutoArchived, todayStart).Count(&todayEscalated)

	// 7 天闭环率
	sevenDaysAgo := time.Now().AddDate(0, 0, -7)
	var weekCreated, weekClosed int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND created_at >= ?", sevenDaysAgo).Count(&weekCreated)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND resolved_at >= ? AND status IN (?)", sevenDaysAgo,
			[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).Count(&weekClosed)
	closeRate := float64(0)
	if weekCreated > 0 {
		closeRate = float64(weekClosed) / float64(weekCreated) * 100
	}

	// 闭环速度中位数（仅取最近 30 天已闭环的）
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	type ResolveDuration struct {
		DurationSec float64
	}
	var durations []ResolveDuration
	model.DB.Raw(`
		SELECT TIMESTAMPDIFF(SECOND, original_created_at, resolved_at) AS duration_sec
		FROM review_issues
		WHERE deleted_at IS NULL AND status IN (?) AND resolved_at IS NOT NULL
			AND resolved_at >= ?
	`, []string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}, thirtyDaysAgo).Scan(&durations)
	medianDays := float64(0)
	if len(durations) > 0 {
		vals := make([]float64, len(durations))
		for i, d := range durations {
			vals[i] = d.DurationSec / 86400.0
		}
		medianDays = medianFloat64(vals)
	}

	// IM 投递统计（今日）
	var imTotal, imSuccess int64
	model.DB.Model(&model.NotificationDeliveryLog{}).
		Where("created_at >= ?", todayStart).Count(&imTotal)
	model.DB.Model(&model.NotificationDeliveryLog{}).
		Where("status = ? AND created_at >= ?", "success", todayStart).Count(&imSuccess)

	// 积压项目告警
	type alertItem struct {
		ProjectID    uint   `json:"project_id"`
		ProjectName  string `json:"project_name"`
		PendingCount int64  `json:"pending_count"`
		OverdueCount int64  `json:"overdue_count"`
	}
	var alerts []alertItem
	model.DB.Raw(`
		SELECT 
			projects.id AS project_id,
			projects.name AS project_name,
			COUNT(review_issues.id) AS pending_count,
			COUNT(CASE WHEN review_issues.original_created_at < ? THEN 1 END) AS overdue_count
		FROM projects
		INNER JOIN tasks ON tasks.project_id = projects.id
		INNER JOIN review_issues ON review_issues.task_id = tasks.id
			AND review_issues.deleted_at IS NULL
			AND review_issues.status IN (?)
		GROUP BY projects.id, projects.name
		HAVING pending_count > 0
		ORDER BY pending_count DESC
		LIMIT 10
	`, overdueSince, []string{model.IssueStatusPending, model.IssueStatusPendingInherited}).Scan(&alerts)

	// Issue 状态分布

	var distPending, distPendingInherited, distResolved, distFalsePositive, distIgnored, distAutoFiltered int64
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusPending).Count(&distPending)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusPendingInherited).Count(&distPendingInherited)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusResolved).Count(&distResolved)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusFalsePositive).Count(&distFalsePositive)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusIgnored).Count(&distIgnored)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusAutoFiltered).Count(&distAutoFiltered)

	// 待处理最多的项目 Top 10
	type pendingProj struct {
		Name         string `json:"name"`
		PendingCount int64  `json:"pending_count"`
	}
	var topPendingProjects []pendingProj
	model.DB.Raw(`
		SELECT projects.name AS name, COUNT(review_issues.id) AS pending_count
		FROM projects
		INNER JOIN tasks ON tasks.project_id = projects.id
		INNER JOIN review_issues ON review_issues.task_id = tasks.id
			AND review_issues.deleted_at IS NULL
			AND review_issues.status IN (?)
		GROUP BY projects.id, projects.name
		ORDER BY pending_count DESC
		LIMIT 10
	`, []string{model.IssueStatusPending, model.IssueStatusPendingInherited}).Scan(&topPendingProjects)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"today_new":           todayNew,
		"total_pending":       totalPending,
		"overdue":             overdue,
		"today_archived":      todayArchived,
		"total_archived":      totalArchived,
		"today_escalated":     todayEscalated,
		"im_total":            imTotal,
		"im_success":          imSuccess,
		"week_close_rate":     fmt.Sprintf("%.1f%%", closeRate),
		"median_resolve_days": fmt.Sprintf("%.1f", medianDays),
		"alerts":              alerts,
		"status_distribution": gin.H{
			"pending":           distPending,
			"pending_inherited": distPendingInherited,
			"resolved":          distResolved,
			"false_positive":    distFalsePositive,
			"ignored":           distIgnored,
			"auto_filtered":     distAutoFiltered,
		},
		"top_pending_projects": topPendingProjects,
	}})
}

func medianFloat64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	// 简单冒泡排序（数据量小，够用）
	for i := 0; i < len(vals); i++ {
		for j := i + 1; j < len(vals); j++ {
			if vals[i] > vals[j] {
				vals[i], vals[j] = vals[j], vals[i]
			}
		}
	}
	mid := len(vals) / 2
	if len(vals)%2 == 1 {
		return vals[mid]
	}
	return (vals[mid-1] + vals[mid]) / 2
}

// DeleteOldRead 删除 30 天前已读通知
func (h *NotificationHandler) DeleteOldRead(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	cutoff := time.Now().AddDate(0, 0, -30)
	result := model.DB.Where("user_id = ? AND is_read = ? AND created_at < ?", user.ID, true, cutoff).Delete(&model.Notification{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok", "deleted_count": result.RowsAffected})
}

// --- 通知规则模板管理（admin only） ---

// ListRules 通知规则列表
func (h *NotificationHandler) ListRules(c *gin.Context) {
	var rules []model.NotificationRule
	model.DB.Order("id DESC").Find(&rules)
	c.JSON(http.StatusOK, gin.H{"data": rules})
}

// CreateRule 创建通知规则
func (h *NotificationHandler) CreateRule(c *gin.Context) {
	var rule model.NotificationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// issue.escalation 规则唯一性校验
	if rule.Trigger == "issue.escalation" {
		var existing model.NotificationRule
		if err := model.DB.Where("`trigger` = ? AND enabled = ?", "issue.escalation", true).First(&existing).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "已存在启用的 Issue 升级规则，请先禁用或删除现有规则"})
			return
		}
	}
	// MySQL JSON 列不接受空字符串，统一替换为有效 JSON
	if rule.Condition == "" {
		rule.Condition = "{}"
	}
	if rule.Actions == "" {
		rule.Actions = "{}"
	}
	if rule.EscalationConfig == "" {
		rule.EscalationConfig = "{}"
	}
	if err := model.DB.Create(&rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rule})
}

// UpdateRule 更新通知规则
func (h *NotificationHandler) UpdateRule(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var rule model.NotificationRule
	if err := model.DB.First(&rule, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// issue.escalation 规则唯一性校验（排除自身）
	trigger, _ := updates["trigger"].(string)
	enabled, enabledOK := updates["enabled"].(bool)
	if (trigger == "issue.escalation") || (trigger == "" && rule.Trigger == "issue.escalation") {
		// 如果更新后仍为 issue.escalation 且 enabled=true（或未传 enabled 但原规则已启用）
		willBeEnabled := enabled
		if !enabledOK {
			willBeEnabled = rule.Enabled
		}
		if willBeEnabled {
			var existing model.NotificationRule
			if err := model.DB.Where("`trigger` = ? AND enabled = ? AND id != ?", "issue.escalation", true, rule.ID).First(&existing).Error; err == nil {
				c.JSON(http.StatusConflict, gin.H{"error": "已存在启用的 Issue 升级规则，请先禁用或删除现有规则"})
				return
			}
		}
	}
	// 同理：空字符串 JSON 字段替换为 {}
	for _, key := range []string{"condition", "actions", "escalation_config"} {
		if v, ok := updates[key]; ok {
			if s, ok := v.(string); ok && s == "" {
				updates[key] = "{}"
			}
		}
	}
	if err := model.DB.Model(&rule).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rule})
}

// DeleteRule 删除通知规则
func (h *NotificationHandler) DeleteRule(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := model.DB.Delete(&model.NotificationRule{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ========== Holiday CRUD ==========

func (h *NotificationHandler) ListHolidays(c *gin.Context) {
	yearStr := c.Query("year")
	holidayType := c.Query("type")
	db := model.DB.Model(&model.Holiday{})
	if yearStr != "" {
		db = db.Where("year = ? OR year = 0", yearStr)
	}
	if holidayType != "" {
		db = db.Where("type = ?", holidayType)
	}
	var holidays []model.Holiday
	db.Order("date DESC").Find(&holidays)
	c.JSON(http.StatusOK, gin.H{"data": holidays})
}

func (h *NotificationHandler) CreateHoliday(c *gin.Context) {
	var req struct {
		Date        string `json:"date" binding:"required"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		IsRecurring bool   `json:"is_recurring"`
		IsWorkday   bool   `json:"is_workday"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 严格校验日期格式
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid date format, expected YYYY-MM-DD"})
		return
	}
	year, _ := strconv.Atoi(req.Date[:4])
	if req.Type == "" {
		req.Type = model.HolidayTypeNational
	}
	// 补班类型自动设置 is_workday=true
	if req.Type == model.HolidayTypeMakeup {
		req.IsWorkday = true
	}
	if req.IsRecurring {
		year = 0
	}

	holiday := model.Holiday{
		Date:        req.Date,
		Name:        req.Name,
		Type:        req.Type,
		IsRecurring: req.IsRecurring,
		Year:        year,
		IsWorkday:   req.IsWorkday,
	}
	if err := model.DB.Create(&holiday).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": holiday})
}

func (h *NotificationHandler) UpdateHoliday(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var holiday model.Holiday
	if err := model.DB.First(&holiday, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var req struct {
		Date        *string `json:"date"`
		Name        *string `json:"name"`
		Type        *string `json:"type"`
		IsRecurring *bool   `json:"is_recurring"`
		IsWorkday   *bool   `json:"is_workday"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updates := make(map[string]interface{})
	if req.Date != nil {
		if _, err := time.Parse("2006-01-02", *req.Date); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid date format, expected YYYY-MM-DD"})
			return
		}
		updates["date"] = *req.Date
		if y, err := strconv.Atoi((*req.Date)[:4]); err == nil {
			updates["year"] = y
		}
	}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Type != nil {
		updates["type"] = *req.Type
		if *req.Type == model.HolidayTypeMakeup {
			updates["is_workday"] = true
		}
	}
	if req.IsRecurring != nil {
		updates["is_recurring"] = *req.IsRecurring
		if *req.IsRecurring {
			updates["year"] = 0
		}
	}
	if req.IsWorkday != nil {
		updates["is_workday"] = *req.IsWorkday
	}
	if err := model.DB.Model(&holiday).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": holiday})
}

func (h *NotificationHandler) DeleteHoliday(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := model.DB.Delete(&model.Holiday{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func (h *NotificationHandler) BatchDeleteHolidays(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Delete(&model.Holiday{}, req.IDs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok", "count": len(req.IDs)})
}

// ImportHolidays 批量导入（JSON 数组）
func (h *NotificationHandler) ImportHolidays(c *gin.Context) {
	var req []struct {
		Date        string `json:"date" binding:"required"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		IsRecurring bool   `json:"is_recurring"`
		IsWorkday   bool   `json:"is_workday"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var toCreate []model.Holiday
	for _, item := range req {
		if item.Type == "" {
			item.Type = model.HolidayTypeNational
		}
		if item.Type == model.HolidayTypeMakeup {
			item.IsWorkday = true
		}
		year := 0
		if len(item.Date) >= 4 {
			if y, err := strconv.Atoi(item.Date[:4]); err == nil {
				year = y
			}
		}
		if item.IsRecurring {
			year = 0
		}
		toCreate = append(toCreate, model.Holiday{
			Date:        item.Date,
			Name:        item.Name,
			Type:        item.Type,
			IsRecurring: item.IsRecurring,
			Year:        year,
			IsWorkday:   item.IsWorkday,
		})
	}
	var count int64
	if err := model.DB.CreateInBatches(toCreate, 100).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	count = int64(len(toCreate))
	c.JSON(http.StatusOK, gin.H{"message": "ok", "count": count})
}

// SyncHolidaysFromAPI 从 holiday-cn 同步中国法定节假日与调休数据
func (h *NotificationHandler) SyncHolidaysFromAPI(c *gin.Context) {
	year := c.Query("year")
	if year == "" {
		year = strconv.Itoa(time.Now().Year())
	}
	apiURL := fmt.Sprintf("https://cdn.jsdelivr.net/gh/NateScarlet/holiday-cn@master/%s.json", year)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch holiday API: " + err.Error()})
		return
	}
	if resp == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Holiday API returned nil response"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Holiday API returned %d", resp.StatusCode)})
		return
	}

	type apiDay struct {
		Name     string `json:"name"`
		Date     string `json:"date"`
		IsOffDay bool   `json:"isOffDay"`
	}
	type apiResp struct {
		Year int      `json:"year"`
		Days []apiDay `json:"days"`
	}
	var data apiResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode API response: " + err.Error()})
		return
	}

	yearInt, _ := strconv.Atoi(year)
	var toCreate []model.Holiday
	for _, item := range data.Days {
		var existing model.Holiday
		if err := model.DB.Where("date = ? AND type IN ?", item.Date, []string{model.HolidayTypeNational, model.HolidayTypeMakeup}).First(&existing).Error; err == nil {
			continue // 已存在则跳过
		}
		// isOffDay=true: 放假 (national); isOffDay=false: 调休补班 (makeup)
		holidayType := model.HolidayTypeNational
		isWorkday := false
		if !item.IsOffDay {
			holidayType = model.HolidayTypeMakeup
			isWorkday = true
		}
		toCreate = append(toCreate, model.Holiday{
			Date:        item.Date,
			Name:        item.Name,
			Type:        holidayType,
			IsRecurring: false,
			Year:        yearInt,
			IsWorkday:   isWorkday,
		})
	}
	count := len(toCreate)
	if count > 0 {
		if err := model.DB.CreateInBatches(toCreate, 100).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save holidays: " + err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok", "count": count, "year": year})
}
