package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/mcp"
	"github.com/ai-optimizer/backend/internal/model"
)

// ---------------------- Workbench Tool ----------------------

func handleGetWorkbench(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	view := "developer"
	if v, ok := args["view"].(string); ok && v != "" {
		view = v
	}

	if view == "admin" {
		if !authCtx.IsAdmin {
			return nil, fmt.Errorf("permission denied: admin view requires admin role")
		}
		return getAdminWorkbench(authCtx)
	}
	// 绑定 admin 用户的 Key 没有具体的 developer 业务数据（无 owner_id 关联的 Issue），
	// 默认切到 admin view 以避免返回全空结果误导智能体
	if authCtx.IsAdmin {
		return getAdminWorkbench(authCtx)
	}
	return getDeveloperWorkbench(authCtx)
}

func getDeveloperWorkbench(authCtx *mcp.AuthContext) (interface{}, error) {
	userID := authCtx.User.ID
	gitlabUsername := authCtx.User.GitlabUsername

	baseline := getNotificationBaseline()
	
	// mine: owner_id = 当前用户
	var mineTotal, mineCritical, mineHigh, mineMedium, mineLow int64
	qMineBase := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND owner_id = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID)
	if !baseline.IsZero() {
		qMineBase = qMineBase.Where("original_created_at >= ?", baseline)
	}
	qMineBase.Count(&mineTotal)
	
	qMineCritical := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND owner_id = ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, "critical")
	if !baseline.IsZero() {
		qMineCritical = qMineCritical.Where("original_created_at >= ?", baseline)
	}
	qMineCritical.Count(&mineCritical)

	qMineHigh := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND owner_id = ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, "high")
	if !baseline.IsZero() {
		qMineHigh = qMineHigh.Where("original_created_at >= ?", baseline)
	}
	qMineHigh.Count(&mineHigh)

	qMineMedium := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND owner_id = ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, "medium")
	if !baseline.IsZero() {
		qMineMedium = qMineMedium.Where("original_created_at >= ?", baseline)
	}
	qMineMedium.Count(&mineMedium)

	qMineLow := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND owner_id = ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, "low")
	if !baseline.IsZero() {
		qMineLow = qMineLow.Where("original_created_at >= ?", baseline)
	}
	qMineLow.Count(&mineLow)

	// escalated_to_me: current_owner_id = 当前用户 且 owner_id != 当前用户
	var escTotal, escCritical, escHigh, escMedium, escLow int64
	qEscBase := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND current_owner_id = ? AND owner_id != ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, userID)
	if !baseline.IsZero() {
		qEscBase = qEscBase.Where("original_created_at >= ?", baseline)
	}
	qEscBase.Count(&escTotal)

	qEscCritical := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND current_owner_id = ? AND owner_id != ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, userID, "critical")
	if !baseline.IsZero() {
		qEscCritical = qEscCritical.Where("original_created_at >= ?", baseline)
	}
	qEscCritical.Count(&escCritical)

	qEscHigh := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND current_owner_id = ? AND owner_id != ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, userID, "high")
	if !baseline.IsZero() {
		qEscHigh = qEscHigh.Where("original_created_at >= ?", baseline)
	}
	qEscHigh.Count(&escHigh)

	qEscMedium := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND current_owner_id = ? AND owner_id != ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, userID, "medium")
	if !baseline.IsZero() {
		qEscMedium = qEscMedium.Where("original_created_at >= ?", baseline)
	}
	qEscMedium.Count(&escMedium)

	qEscLow := model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND current_owner_id = ? AND owner_id != ? AND severity = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, userID, "low")
	if !baseline.IsZero() {
		qEscLow = qEscLow.Where("original_created_at >= ?", baseline)
	}
	qEscLow.Count(&escLow)

	// 本周统计
	since := time.Now().AddDate(0, 0, -7)
	var weekReceived, weekResolved int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND created_at >= ?", userID, userID, since).
		Count(&weekReceived)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND resolved_at >= ? AND status IN (?)",
			userID, userID, since, []string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).
		Count(&weekResolved)

	// 最早未处理
	var earliestPending model.ReviewIssue
	model.DB.Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?)",
		[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, userID).
		Order("original_created_at ASC").
		First(&earliestPending)

	earliestText := "-"
	if earliestPending.ID > 0 {
		hours := int(time.Since(*earliestPending.OriginalCreatedAt).Hours())
		if hours < 24 {
			earliestText = fmt.Sprintf("%d 小时", hours)
		} else {
			earliestText = fmt.Sprintf("%d 天", hours/24)
		}
	}

	// Team Pending（仅 Project Owner）
	var teamPending []map[string]interface{}
	if authCtx.IsAdmin || isProjectOwner(userID) {
		// 获取当前用户负责的项目下的成员待处理统计
		var responsibilities []model.ProjectResponsibility
		model.DB.Where("user_id = ?", userID).Find(&responsibilities)
		projectIDs := make([]uint, 0, len(responsibilities))
		for _, r := range responsibilities {
			projectIDs = append(projectIDs, r.ProjectID)
		}
		if len(projectIDs) > 0 {
			var responsibleUsers []model.User
			model.DB.Where("id IN (?)",
				model.DB.Table("project_responsibilities").
					Select("DISTINCT user_id").
					Where("project_id IN ?", projectIDs)).
				Find(&responsibleUsers)

			for _, u := range responsibleUsers {
				var pendingCount, overdueCount int64
				model.DB.Model(&model.ReviewIssue{}).
					Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?)",
						[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, u.ID, u.ID).
					Count(&pendingCount)
				// overdue > 120 work hours (simplified to 5 days)
				overdueSince := time.Now().AddDate(0, 0, -5)
				model.DB.Model(&model.ReviewIssue{}).
					Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?) AND original_created_at < ?",
						[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, u.ID, u.ID, overdueSince).
					Count(&overdueCount)
				teamPending = append(teamPending, map[string]interface{}{
					"member_name":   u.DisplayName,
					"pending_count": pendingCount,
					"overdue_count": overdueCount,
				})
			}
		}
	}

	// 近4周趋势
	trendWeeks := make([]map[string]interface{}, 0, 4)
	for i := 0; i < 4; i++ {
		weekStart := time.Now().AddDate(0, 0, -7*(i+1))
		weekEnd := time.Now().AddDate(0, 0, -7*i)
		var wReceived, wResolved int64
		model.DB.Model(&model.ReviewIssue{}).
			Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND created_at >= ? AND created_at < ?",
				userID, userID, weekStart, weekEnd).
			Count(&wReceived)
		model.DB.Model(&model.ReviewIssue{}).
			Where("deleted_at IS NULL AND (owner_id = ? OR current_owner_id = ?) AND resolved_at >= ? AND resolved_at < ? AND status IN (?)",
				userID, userID, weekStart, weekEnd, []string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).
			Count(&wResolved)
		label := "本周"
		if i == 1 {
			label = "上周"
		} else if i == 2 {
			label = "两周前"
		} else if i == 3 {
			label = "三周前"
		}
		trendWeeks = append([]map[string]interface{}{
			{"week": label, "received": wReceived, "resolved": wResolved},
		}, trendWeeks...)
	}

	// 最近任务
	var recentTasks []map[string]interface{}
	if gitlabUsername != "" {
		var tasks []model.Task
		model.DB.Where("mr_author = ?", gitlabUsername).Order("created_at DESC").Limit(2).Find(&tasks)
		for _, t := range tasks {
			projectName := ""
			var p model.Project
			model.DB.Select("name").First(&p, t.ProjectID)
			projectName = p.Name
			recentTasks = append(recentTasks, map[string]interface{}{
				"task_id":      t.ID,
				"mr_title":     t.MRTitle,
				"project_name": projectName,
				"status":       string(t.Status),
				"created_at":   t.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
	}

	// Top 5 Issues
	var topIssues []map[string]interface{}
	var topIssueList []model.ReviewIssue
	model.DB.Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?)",
		[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, userID, userID).
		Order("severity DESC, original_created_at ASC").
		Limit(5).Find(&topIssueList)
	for _, issue := range topIssueList {
		projectName := ""
		mrTitle := ""
		var task model.Task
		if model.DB.Select("project_id, mr_title").First(&task, issue.TaskID).Error == nil {
			var p model.Project
			model.DB.Select("name").First(&p, task.ProjectID)
			projectName = p.Name
			mrTitle = task.MRTitle
		}
		topIssues = append(topIssues, map[string]interface{}{
			"id":           issue.ID,
			"rule_name":    issue.RuleCode,
			"severity":     issue.Severity,
			"file":         issue.File,
			"line":         issue.LineStart,
			"project_name": projectName,
			"mr_title":     mrTitle,
			"status":       issue.Status,
			"created_at":   issue.OriginalCreatedAt.Format("2006-01-02"),
		})
	}

	return map[string]interface{}{
		"view": "developer",
		"pending_summary": map[string]interface{}{
			"mine": map[string]interface{}{
				"total":    mineTotal,
				"critical": mineCritical,
				"high":     mineHigh,
				"medium":   mineMedium,
				"low":      mineLow,
			},
			"escalated_to_me": map[string]interface{}{
				"total":    escTotal,
				"critical": escCritical,
				"high":     escHigh,
				"medium":   escMedium,
				"low":      escLow,
			},
		},
		"team_pending":    teamPending,
		"week_stats":      map[string]interface{}{"received": weekReceived, "resolved": weekResolved},
		"earliest_pending": earliestText,
		"trend_4weeks":    trendWeeks,
		"recent_tasks":    recentTasks,
		"top_issues":      topIssues,
	}, nil
}

func getAdminWorkbench(authCtx *mcp.AuthContext) (interface{}, error) {
	baseline := getNotificationBaseline()

	var todayNew, totalPending, totalPendingBefore, overdue, overdueBefore int64
	todayStart := time.Now().Truncate(24 * time.Hour)
	model.DB.Model(&model.ReviewIssue{}).Where("org_id = ? AND deleted_at IS NULL AND created_at >= ?", authCtx.OrgID, todayStart).Count(&todayNew)
	q := model.DB.Model(&model.ReviewIssue{}).Where("org_id = ? AND deleted_at IS NULL AND status IN (?)", authCtx.OrgID, []string{model.IssueStatusPending, model.IssueStatusPendingInherited})
	q.Count(&totalPending)
	if !baseline.IsZero() {
		q.Where("original_created_at < ?", baseline).Count(&totalPendingBefore)
	}

	overdueSince := time.Now().AddDate(0, 0, -5)
	qo := model.DB.Model(&model.ReviewIssue{}).
		Where("org_id = ? AND deleted_at IS NULL AND status IN (?) AND original_created_at < ?",
			authCtx.OrgID, []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, overdueSince)
	qo.Count(&overdue)
	if !baseline.IsZero() {
		qo.Where("original_created_at < ?", baseline).Count(&overdueBefore)
	}

	var todayArchived, totalArchived, todayEscalated int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("org_id = ? AND deleted_at IS NULL AND status = ? AND updated_at >= ?", authCtx.OrgID, model.IssueStatusAutoArchived, todayStart).Count(&todayArchived)
	model.DB.Model(&model.ReviewIssue{}).
		Where("org_id = ? AND deleted_at IS NULL AND status = ?", authCtx.OrgID, model.IssueStatusAutoArchived).Count(&totalArchived)
	model.DB.Model(&model.ReviewIssue{}).
		Where("org_id = ? AND deleted_at IS NULL AND escalation_level > 0 AND status != ? AND updated_at >= ?", authCtx.OrgID, model.IssueStatusAutoArchived, todayStart).Count(&todayEscalated)

	// 7天闭环率
	sevenDaysAgo := time.Now().AddDate(0, 0, -7)
	var weekCreated, weekClosed int64
	model.DB.Model(&model.ReviewIssue{}).Where("org_id = ? AND deleted_at IS NULL AND created_at >= ?", authCtx.OrgID, sevenDaysAgo).Count(&weekCreated)
	model.DB.Model(&model.ReviewIssue{}).
		Where("org_id = ? AND deleted_at IS NULL AND resolved_at >= ? AND status IN (?)", authCtx.OrgID, sevenDaysAgo,
			[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).Count(&weekClosed)
	closeRate := float64(0)
	if weekCreated > 0 {
		closeRate = float64(weekClosed) / float64(weekCreated) * 100
	}

	// 中位闭环天数
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	type ResolveDuration struct{ DurationSec float64 }
	var durations []ResolveDuration
	model.DB.Raw(`SELECT TIMESTAMPDIFF(SECOND, review_issues.original_created_at, review_issues.resolved_at) AS duration_sec FROM review_issues INNER JOIN tasks ON tasks.id = review_issues.task_id WHERE review_issues.deleted_at IS NULL AND review_issues.status IN (?) AND review_issues.resolved_at IS NOT NULL AND review_issues.resolved_at >= ? AND tasks.org_id = ?`,
		[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}, thirtyDaysAgo, authCtx.OrgID).Scan(&durations)
	medianDays := float64(0)
	if len(durations) > 0 {
		vals := make([]float64, len(durations))
		for i, d := range durations {
			vals[i] = d.DurationSec / 86400.0
		}
		medianDays = medianFloat64(vals)
	}

	// IM 投递统计
	var imTotal, imSuccess int64
	model.DB.Model(&model.NotificationDeliveryLog{}).Where("created_at >= ?", todayStart).Count(&imTotal)
	model.DB.Model(&model.NotificationDeliveryLog{}).Where("status = ? AND created_at >= ?", "success", todayStart).Count(&imSuccess)

	// Issue 状态分布
	var distPending, distPendingInherited, distResolved, distFalsePositive, distIgnored, distAutoFiltered int64
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusPending).Count(&distPending)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusPendingInherited).Count(&distPendingInherited)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusResolved).Count(&distResolved)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusFalsePositive).Count(&distFalsePositive)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusIgnored).Count(&distIgnored)
	model.DB.Model(&model.ReviewIssue{}).Where("deleted_at IS NULL AND status = ?", model.IssueStatusAutoFiltered).Count(&distAutoFiltered)

	// 积压告警
	type alertItem struct {
		ProjectID    uint   `json:"project_id"`
		ProjectName  string `json:"project_name"`
		PendingCount int64  `json:"pending_count"`
		OverdueCount int64  `json:"overdue_count"`
	}
	var alerts []alertItem
	model.DB.Raw(`SELECT projects.id AS project_id, projects.name AS project_name, COUNT(review_issues.id) AS pending_count, COUNT(CASE WHEN review_issues.original_created_at < ? THEN 1 END) AS overdue_count FROM projects INNER JOIN tasks ON tasks.project_id = projects.id INNER JOIN review_issues ON review_issues.task_id = tasks.id AND review_issues.deleted_at IS NULL AND review_issues.status IN (?) WHERE projects.org_id = ? GROUP BY projects.id, projects.name HAVING pending_count > 0 ORDER BY pending_count DESC LIMIT 10`,
		overdueSince, []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, authCtx.OrgID).Scan(&alerts)

	// Top 积压项目
	type pendingProj struct {
		Name         string `json:"name"`
		PendingCount int64  `json:"pending_count"`
	}
	var topPendingProjects []pendingProj
	model.DB.Raw(`SELECT projects.name AS name, COUNT(review_issues.id) AS pending_count FROM projects INNER JOIN tasks ON tasks.project_id = projects.id INNER JOIN review_issues ON review_issues.task_id = tasks.id AND review_issues.deleted_at IS NULL AND review_issues.status IN (?) WHERE projects.org_id = ? GROUP BY projects.id, projects.name ORDER BY pending_count DESC LIMIT 5`,
		[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, authCtx.OrgID).Scan(&topPendingProjects)

	return map[string]interface{}{
		"view": "admin",
		"kpi": map[string]interface{}{
			"today_new":           todayNew,
			"total_pending":       totalPending,
			"overdue":             overdue,
			"today_archived":      todayArchived,
			"total_archived":      totalArchived,
			"today_escalated":     todayEscalated,
			"week_close_rate":     fmt.Sprintf("%.1f%%", closeRate),
			"median_resolve_days": fmt.Sprintf("%.1f", medianDays),
		},
		"im_delivery": map[string]interface{}{
			"total":        imTotal,
			"success":      imSuccess,
			"failed":       imTotal - imSuccess,
			"success_rate": fmt.Sprintf("%.1f%%", float64(imSuccess)/float64(imTotal)*100),
		},
		"issue_status_distribution": map[string]interface{}{
			"pending":           distPending,
			"pending_inherited": distPendingInherited,
			"resolved":          distResolved,
			"false_positive":    distFalsePositive,
			"ignored":           distIgnored,
			"auto_filtered":     distAutoFiltered,
		},
		"alerts":              alerts,
		"top_pending_projects": topPendingProjects,
	}, nil
}

// ---------------------- Dashboard / Stats Tools ----------------------

func handleGetDashboardStats(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	timeRange := "week"
	if v, ok := args["time_range"].(string); ok && v != "" {
		timeRange = v
	}

	var startTime time.Time
	switch timeRange {
	case "today":
		startTime = time.Now().Truncate(24 * time.Hour)
	case "month":
		startTime = time.Now().AddDate(0, 0, -30)
	default:
		startTime = time.Now().AddDate(0, 0, -7)
	}

	query := model.DB.Model(&model.Task{}).Where("created_at >= ?", startTime)

	// 数据隔离
	mine := true
	all := false
	if v, ok := args["mine"].(bool); ok {
		mine = v
	}
	if v, ok := args["all"].(bool); ok {
		all = v
	}

	queryScope := "mine"
	if authCtx.IsAdmin && all {
		queryScope = "all"
	} else if authCtx.User != nil && authCtx.User.GitlabUsername != "" && mine {
		query = query.Where("mr_author = ?", authCtx.User.GitlabUsername)
	}

	var totalTasks int64
	query.Count(&totalTasks)

	var successCount, failedCount, pendingCount, runningCount int64
	query.Where("status = ?", model.TaskSuccess).Count(&successCount)
	query.Where("status = ?", model.TaskFailed).Count(&failedCount)
	query.Where("status = ?", model.TaskPending).Count(&pendingCount)
	query.Where("status = ?", model.TaskRunning).Count(&runningCount)

	successRate := float64(0)
	if totalTasks > 0 {
		successRate = float64(successCount) / float64(totalTasks)
	}

	// Top 项目
	type topProject struct {
		ProjectName string `json:"project_name"`
		Total       int64  `json:"total"`
		Success     int64  `json:"success"`
	}
	var topProjects []topProject
	model.DB.Raw(`SELECT projects.name as project_name, COUNT(tasks.id) as total, COUNT(CASE WHEN tasks.status = ? THEN 1 END) as success FROM tasks JOIN projects ON tasks.project_id = projects.id WHERE tasks.org_id = ? AND tasks.created_at >= ? GROUP BY projects.id, projects.name ORDER BY total DESC LIMIT 5`,
		model.TaskSuccess, authCtx.OrgID, startTime).Scan(&topProjects)

	return map[string]interface{}{
		"time_range":  timeRange,
		"query_scope": queryScope,
		"summary": map[string]interface{}{
			"total_tasks":    totalTasks,
			"success_rate":   successRate,
			"failure_rate":   1 - successRate,
			"pending_count":  pendingCount,
			"running_count":  runningCount,
		},
		"top_projects": topProjects,
	}, nil
}

func handleGetTokenUsage(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	// Token 用量统计（从 llm_call_logs 表汇总）
	type usageDay struct {
		Date         string  `json:"date"`
		InputTokens  int64   `json:"input_tokens"`
		OutputTokens int64   `json:"output_tokens"`
		Cost         float64 `json:"cost"`
	}

	var days []usageDay
	model.DB.Raw(`SELECT DATE(ll.created_at) as date, SUM(ll.prompt_tokens) as input_tokens, SUM(ll.completion_tokens) as output_tokens, SUM(ll.cost_cents)/100.0 as cost FROM llm_call_logs ll LEFT JOIN tasks t ON t.id = ll.task_id WHERE (t.org_id = ? OR ll.task_id IS NULL) AND ll.created_at >= ? GROUP BY DATE(ll.created_at) ORDER BY date DESC LIMIT 30`,
		authCtx.OrgID, time.Now().AddDate(0, 0, -30)).Scan(&days)

	var totalInput, totalOutput int64
	var totalCost float64
	for _, d := range days {
		totalInput += d.InputTokens
		totalOutput += d.OutputTokens
		totalCost += d.Cost
	}

	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_input_tokens":  totalInput,
			"total_output_tokens": totalOutput,
			"total_cost_cny":      totalCost,
		},
		"by_day": days,
	}, nil
}

// ---------------------- Projects Tool ----------------------

func handleListProjects(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	limit := 10
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
		if limit > 20 {
			limit = 20
		}
	}

	query := model.DB.Model(&model.Project{}).Where("deleted_at IS NULL")

	// 数据隔离：非 admin 只能看到自己有任务的 project
	mine := true
	all := false
	if v, ok := args["mine"].(bool); ok {
		mine = v
	}
	if v, ok := args["all"].(bool); ok {
		all = v
	}

	if !authCtx.IsAdmin || (mine && !all) {
		if authCtx.User != nil && authCtx.User.GitlabUsername != "" {
			// 查找用户有任务的项目
			var projectIDs []uint
			model.DB.Model(&model.Task{}).
				Select("DISTINCT project_id").
				Where("mr_author = ?", authCtx.User.GitlabUsername).
				Pluck("project_id", &projectIDs)
			if len(projectIDs) > 0 {
				query = query.Where("id IN ?", projectIDs)
			} else {
				return map[string]interface{}{
					"items":       []interface{}{},
					"total":       0,
					"query_scope": "mine",
				}, nil
			}
		}
	}

	var total int64
	query.Count(&total)

	var projects []model.Project
	query.Limit(limit).Find(&projects)

	items := make([]map[string]interface{}, 0, len(projects))
	for _, p := range projects {
		items = append(items, map[string]interface{}{
			"id":       p.ID,
			"name":     p.Name,
			"language": p.Language,
			"path":     p.ProjectPath,
		})
	}

	queryScope := "all"
	if !authCtx.IsAdmin || mine {
		queryScope = "mine"
	}

	return map[string]interface{}{
		"items":       items,
		"total":       total,
		"query_scope": queryScope,
	}, nil
}

// ---------------------- Notifications Tools ----------------------

func handleListNotifications(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	status := "unread"
	if s, ok := args["status"].(string); ok && s != "" {
		status = s
	}

	limit := 5
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
		if limit > 20 {
			limit = 20
		}
	}

	query := model.DB.Model(&model.Notification{}).Where("user_id = ?", authCtx.UserID)
	if status == "unread" {
		query = query.Where("is_read = ?", false)
	}
	query = query.Order("created_at DESC")

	var total, unreadCount int64
	query.Count(&total)
	model.DB.Model(&model.Notification{}).Where("user_id = ? AND is_read = ?", authCtx.UserID, false).Count(&unreadCount)

	var notifications []model.Notification
	query.Limit(limit).Find(&notifications)

	items := make([]map[string]interface{}, 0, len(notifications))
	for _, n := range notifications {
		items = append(items, map[string]interface{}{
			"id":         n.ID,
			"type":       n.Type,
			"title":      n.Title,
			"message":    n.Content,
			"task_id":    n.RelatedTaskID,
			"read":       n.IsRead,
			"created_at": n.CreatedAt.Format("2006-01-02 15:04"),
		})
	}

	return map[string]interface{}{
		"items":        items,
		"unread_count": unreadCount,
		"total":        total,
	}, nil
}

func handleMarkAllRead(ctx context.Context, authCtx *mcp.AuthContext, args map[string]interface{}) (interface{}, error) {
	result := model.DB.Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", authCtx.UserID, false).
		Update("is_read", true)

	return map[string]interface{}{
		"success":       true,
		"marked_count":  result.RowsAffected,
	}, nil
}

// ---------------------- Helpers ----------------------

func getNotificationBaseline() time.Time {
	var setting model.NotificationGlobalSetting
	if err := model.DB.First(&setting).Error; err == nil && setting.NotificationBaselineAt != nil {
		return *setting.NotificationBaselineAt
	}
	return time.Time{}
}

func isProjectOwner(userID uint) bool {
	var count int64
	model.DB.Model(&model.ProjectResponsibility{}).Where("user_id = ?", userID).Count(&count)
	return count > 0
}

func medianFloat64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
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
