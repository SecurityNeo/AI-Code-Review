package handler

import (
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type DashboardHandler struct {
	startTime time.Time
}

func NewDashboardHandler() *DashboardHandler {
	return &DashboardHandler{
		startTime: time.Now(),
	}
}

func (h *DashboardHandler) GetStats(c *gin.Context) {
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var totalProjects, todayTasks, runningTasks, failedTasks24h, activePools int64

	dbProjects := model.DB.Model(&model.Project{})
	if !scope.IsSuperAdmin {
		dbProjects = dbProjects.Scopes(model.OrgScope(scope))
	}
	dbProjects.Count(&totalProjects)

	today := time.Now().Truncate(24 * time.Hour)
	queryToday := model.DB.Model(&model.Task{}).Where("created_at >= ?", today)
	if !scope.IsSuperAdmin {
		queryToday = queryToday.Scopes(model.OrgScope(scope))
	}
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		queryToday = queryToday.Where("mr_author = ?", user.GitlabUsername)
	}
	queryToday.Count(&todayTasks)

	queryRunning := model.DB.Model(&model.Task{}).Where("status = ?", model.TaskRunning)
	if !scope.IsSuperAdmin {
		queryRunning = queryRunning.Scopes(model.OrgScope(scope))
	}
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		queryRunning = queryRunning.Where("mr_author = ?", user.GitlabUsername)
	}
	queryRunning.Count(&runningTasks)

	yesterday := today.Add(-24 * time.Hour)
	queryFailed := model.DB.Model(&model.Task{}).Where("status = ? AND updated_at >= ?", model.TaskFailed, yesterday)
	if !scope.IsSuperAdmin {
		queryFailed = queryFailed.Scopes(model.OrgScope(scope))
	}
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		queryFailed = queryFailed.Where("mr_author = ?", user.GitlabUsername)
	}
	queryFailed.Count(&failedTasks24h)

	dbPools := model.DB.Model(&model.ResourcePool{}).Where("status = ?", "active")
	if !scope.IsSuperAdmin {
		dbPools = dbPools.Scopes(model.OrgScope(scope))
	}
	dbPools.Count(&activePools)

	c.JSON(200, gin.H{
		"total_projects":   totalProjects,
		"today_tasks":      todayTasks,
		"running_tasks":    runningTasks,
		"failed_tasks_24h": failedTasks24h,
		"active_pools":     activePools,
	})
}

func (h *DashboardHandler) GetTrends(c *gin.Context) {
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	labels := make([]string, 7)
	success := make([]int, 7)
	failed := make([]int, 7)

	now := time.Now()
	for i := 6; i >= 0; i-- {
		date := now.AddDate(0, 0, -i)
		labels[6-i] = date.Format("01-02")

		startOfDay := date.Truncate(24 * time.Hour)
		endOfDay := startOfDay.Add(24 * time.Hour)

		var successCount, failedCount int64
		qSuccess := model.DB.Model(&model.Task{}).
			Where("status = ? AND created_at >= ? AND created_at < ?", model.TaskSuccess, startOfDay, endOfDay)
		if !scope.IsSuperAdmin {
			qSuccess = qSuccess.Scopes(model.OrgScope(scope))
		}
		if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
			qSuccess = qSuccess.Where("mr_author = ?", user.GitlabUsername)
		}
		qSuccess.Count(&successCount)

		qFailed := model.DB.Model(&model.Task{}).
			Where("status = ? AND created_at >= ? AND created_at < ?", model.TaskFailed, startOfDay, endOfDay)
		if !scope.IsSuperAdmin {
			qFailed = qFailed.Scopes(model.OrgScope(scope))
		}
		if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
			qFailed = qFailed.Where("mr_author = ?", user.GitlabUsername)
		}
		qFailed.Count(&failedCount)

		success[6-i] = int(successCount)
		failed[6-i] = int(failedCount)
	}

	c.JSON(200, gin.H{
		"labels":  labels,
		"success": success,
		"failed":  failed,
	})
}

func (h *DashboardHandler) GetRecentProjects(c *gin.Context) {
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var projects []model.Project

	// user 时：先查自己最近有任务的项目ID，再用这些ID查项目详情
	if !scope.HasOrgRole("super_admin", "org_admin") {
		var projectIDs []uint
		dbTask := model.DB.Model(&model.Task{}).
			Select("DISTINCT project_id").
			Where("mr_author = ?", user.GitlabUsername).
			Order("created_at DESC").
			Limit(5)
		if !scope.IsSuperAdmin {
			dbTask = dbTask.Scopes(model.OrgScope(scope))
		}
		dbTask.Pluck("project_id", &projectIDs)

		if len(projectIDs) == 0 {
			c.JSON(200, gin.H{"data": []any{}})
			return
		}

		db := model.DB.Where("id IN ?", projectIDs)
		if !scope.IsSuperAdmin {
			db = db.Scopes(model.OrgScope(scope))
		}
		db.Preload("Tasks", func(db *gorm.DB) *gorm.DB {
			return db.Where("mr_author = ?", user.GitlabUsername).Order("created_at DESC").Limit(5)
		}).
			Find(&projects)
	} else {
		db := model.DB
		if !scope.IsSuperAdmin {
			db = db.Scopes(model.OrgScope(scope))
		}
		err := db.Preload("Tasks", func(db *gorm.DB) *gorm.DB {
			return db.Order("created_at DESC").Limit(5)
		}).Order("updated_at DESC").Limit(5).Find(&projects).Error

		if err != nil {
			zap.L().Error("get recent projects failed", zap.Error(err))
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}

	type ProjectTask struct {
		ID     uint   `json:"id"`
		Name   string `json:"name"`
		Path   string `json:"path"`
		Status string `json:"status"`
	}

	type RecentProject struct {
		ID          uint          `json:"id"`
		Name        string        `json:"name"`
		ProjectPath string        `json:"project_path"`
		Tasks       []ProjectTask `json:"tasks"`
	}

	result := make([]RecentProject, 0, len(projects))
	for _, p := range projects {
		tasks := make([]ProjectTask, 0, len(p.Tasks))
		for _, t := range p.Tasks {
			tasks = append(tasks, ProjectTask{
				ID:     t.ID,
				Name:   p.Name,
				Path:   p.ProjectPath,
				Status: string(t.Status),
			})
		}
		result = append(result, RecentProject{
			ID:          p.ID,
			Name:        p.Name,
			ProjectPath: p.ProjectPath,
			Tasks:       tasks,
		})
	}

	c.JSON(200, gin.H{"data": result})
}

func (h *DashboardHandler) GetRecentFailures(c *gin.Context) {
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

	var tasks []model.Task

	query := model.DB.Preload("Project").Scopes(model.OrgScope(scope)).
		Where("status = ?", model.TaskFailed).
		Order("updated_at DESC").
		Limit(10)
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		query = query.Where("mr_author = ?", user.GitlabUsername)
	}
	err := query.Find(&tasks).Error

	if err != nil {
		zap.L().Error("get recent failures failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	type FailureTask struct {
		ID          uint   `json:"id"`
		ProjectName string `json:"project_name"`
		MRMergeID   int    `json:"mr_iid"`
		ErrorMsg    string `json:"error_msg"`
		UpdatedAt   string `json:"updated_at"`
	}

	result := make([]FailureTask, 0, len(tasks))
	for _, t := range tasks {
		projectName := t.Project.Name
		if projectName == "" {
			projectName = "未知项目"
		}
		errorMsg := t.ErrorMsg
		if errorMsg == "" {
			errorMsg = "任务执行失败"
		}
		if len(errorMsg) > 50 {
			errorMsg = errorMsg[:50] + "..."
		}

		result = append(result, FailureTask{
			ID:          t.ID,
			ProjectName: projectName,
			MRMergeID:   t.MRMergeID,
			ErrorMsg:    errorMsg,
			UpdatedAt:   t.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(200, gin.H{"data": result})
}

func (h *DashboardHandler) GetTaskDistribution(c *gin.Context) {
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

	type ProjectTaskCount struct {
		ProjectName string
		Count       int64
	}

	var counts []ProjectTaskCount
	query := model.DB.Model(&model.Task{}).Scopes(model.OrgScope(scope)).
		Select("projects.name as project_name, COUNT(tasks.id) as count").
		Joins("LEFT JOIN projects ON tasks.project_id = projects.id").
		Group("tasks.project_id").
		Order("count DESC").
		Limit(6)
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		query = query.Where("mr_author = ?", user.GitlabUsername)
	}
	query.Scan(&counts)

	labels := make([]string, len(counts))
	data := make([]int, len(counts))
	colors := []string{"#3b82f6", "#f59e0b", "#8b5cf6", "#10b981", "#ec4899", "#6b7280"}

	for i, c := range counts {
		labels[i] = c.ProjectName
		if labels[i] == "" {
			labels[i] = "未知项目"
		}
		data[i] = int(c.Count)
	}

	// 如果不足6个，添加"其他"
	var otherCount int64
	otherQuery := model.DB.Model(&model.Task{}).Scopes(model.OrgScope(scope))
	if scope.CurrentOrgRole == "developer" && user.GitlabUsername != "" {
		otherQuery = otherQuery.Where("mr_author = ?", user.GitlabUsername)
	}
	otherQuery.Count(&otherCount)

	sum := int64(0)
	for _, d := range data {
		sum += int64(d)
	}
	if otherCount > sum {
		labels = append(labels, "其他")
		data = append(data, int(otherCount-sum))
		colors = append(colors, "#9ca3af")
	}

	c.JSON(200, gin.H{
		"labels": labels,
		"data":   data,
		"colors": colors[:len(labels)],
	})
}
