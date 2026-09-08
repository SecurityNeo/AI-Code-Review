package handler

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/pkg/encrypt"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type TaskHandler struct{}

func NewTaskHandler() *TaskHandler {
	return &TaskHandler{}
}

func (h *TaskHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	projectID, _ := strconv.Atoi(c.Query("project_id"))
	status := c.Query("status")
	timeFilter := c.Query("time_filter")
	author := c.Query("author")
	mrIID := c.Query("mr_iid")
	hasPendingIssues := c.Query("has_pending_issues") == "true"
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	// 多租户改造：校验 project_id 是否属于当前用户可见范围
	if projectID > 0 {
		if !CanAccessProject(scope, uint(projectID)) {
			c.JSON(403, gin.H{"error": "无权访问该项目"})
			return
		}
	}

	var startTime, endTime time.Time
	now := time.Now()
	switch timeFilter {
	case "today":
		startTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		endTime = startTime.AddDate(0, 0, 1)
	case "7d":
		startTime = now.AddDate(0, 0, -7)
		endTime = now
	case "30d":
		startTime = now.AddDate(0, 0, -30)
		endTime = now
	}

	user, _ := middleware.GetUser(c)
	tasks, total, err := service.NewTaskService().List(scope, user, uint(projectID), status, startTime, endTime, author, mrIID, hasPendingIssues, page, pageSize)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：二次过滤，确保只返回用户组织可见的任务
	if !scope.IsSuperAdmin {
		var filtered []model.Task
		for _, t := range tasks {
			if CanAccessTask(scope, t.ID) {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}

	// 清理敏感字段：不透传密码、API Key、项目Token
	for i := range tasks {
		tasks[i].Project.AccessToken = ""
		if tasks[i].Pool.ID > 0 {
			tasks[i].Pool.OpencodePassword = ""
			tasks[i].Pool.OpencodeAPIKey = ""
		}
		if tasks[i].UsedModel.ID > 0 {
			tasks[i].UsedModel.APIKey = ""
		}
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(tasks))
	for _, t := range tasks {
		orgIDs = append(orgIDs, t.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range tasks {
		tasks[i].OrgName = orgNameMap[tasks[i].OrgID]
	}

	c.JSON(200, gin.H{"data": tasks, "total": total})
}

func (h *TaskHandler) Get(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	task, err := service.NewTaskService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	// 多租户改造：通过 CanAccessTask 校验组织归属
	if !CanAccessTask(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权查看此任务"})
		return
	}

	user, _ := middleware.GetUser(c)
	// 权限检查：admin、任务提交者、项目负责人均可查看（旧逻辑保留）
	if !service.NewTaskService().CanViewTask(scope, user, *task) {
		c.JSON(403, gin.H{"error": "无权查看此任务"})
		return
	}

	// 清理敏感字段：不透传密码、API Key、项目Token
	task.Project.AccessToken = ""
	if task.Pool.ID > 0 {
		task.Pool.OpencodePassword = ""
		task.Pool.OpencodeAPIKey = ""
	}
	if task.UsedModel.ID > 0 {
		task.UsedModel.APIKey = ""
	}

	c.JSON(200, gin.H{"data": task})
}

func (h *TaskHandler) Create(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：校验用户是否有权限访问project_id对应的项目
	if pid, ok := data["project_id"].(float64); ok && pid > 0 {
		if !CanAccessProject(scope, uint(pid)) {
			c.JSON(403, gin.H{"error": "无权访问该项目"})
			return
		}
	}

	// 多租户改造：注入当前 org_id（非 super_admin 禁止客户端传入）
	if _, ok := data["org_id"]; !ok || !scope.IsSuperAdmin {
		data["org_id"] = float64(middleware.GetCurrentOrgID(c))
	}

	task, err := service.NewTaskService().Create(data)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	go service.NewTaskService().Execute(task.ID)

	c.JSON(200, gin.H{"message": "task created", "data": task})
}

func (h *TaskHandler) Execute(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	// 多租户改造：校验任务权限
	if !CanAccessTask(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权操作此任务"})
		return
	}
	if err := service.NewTaskService().Execute(uint(id)); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "task started"})
}

const maxUserReviewCommentLen = 5000

func (h *TaskHandler) Retry(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	// 多租户改造：校验任务权限
	if !CanAccessTask(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权操作此任务"})
		return
	}

	var req struct {
		UserReviewComment  string `json:"user_review_comment"`
		SelectedCommentIDs []uint `json:"selected_comment_ids"`
	}
	_ = c.ShouldBindJSON(&req) // 可选字段，不强制要求
	if len(req.UserReviewComment) > maxUserReviewCommentLen {
		c.JSON(400, gin.H{"error": "补充复核意见过长，最多5000字符"})
		return
	}
	operatorID, _ := c.Get("user_id")
	if operatorID == nil {
		operatorID = uint(0)
	}
	if err := service.NewTaskService().Retry(scope, uint(id), req.UserReviewComment, req.SelectedCommentIDs, operatorID.(uint), c.ClientIP()); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "task retried"})
}

// ListReviewComments 获取任务人工复核意见列表
func (h *TaskHandler) ListReviewComments(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	// 多租户改造：校验任务权限
	if !CanAccessTask(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权访问此任务"})
		return
	}
	comments, err := service.NewTaskService().ListReviewComments(uint(id))
	if err != nil {
		zap.L().Error("list review comments failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"data": comments})
}

func (h *TaskHandler) Stop(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	taskID := uint(id)
	// 多租户改造：校验任务权限
	if !CanAccessTask(scope, taskID) {
		c.JSON(403, gin.H{"error": "无权操作此任务"})
		return
	}
	user, _ := middleware.GetUser(c)
	if err := service.NewTaskService().Abort(scope, taskID); err != nil {
		model.RecordOpLog("停止任务", fmt.Sprintf("任务ID:%d", taskID), taskID, user.ID, "failed", err.Error(), c.ClientIP())
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	model.RecordOpLog("停止任务", fmt.Sprintf("任务ID:%d", taskID), taskID, user.ID, "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "task stopped"})
}

func (h *TaskHandler) Logs(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if !CanAccessTask(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权访问此任务"})
		return
	}
	task, err := service.NewTaskService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	c.JSON(200, gin.H{"logs": task.AIResponse})
}

func (h *TaskHandler) Messages(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if !CanAccessTask(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权访问此任务"})
		return
	}
	messages, err := service.NewTaskService().GetTaskMessages(uint(id))
	if err != nil {
		zap.L().Error("get task messages failed", zap.Uint("task_id", uint(id)), zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"data": messages})
}

func (h *TaskHandler) SendMessage(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if !CanAccessTask(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权访问此任务"})
		return
	}

	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "content is required"})
		return
	}

	if err := service.NewTaskService().SendTaskMessage(uint(id), req.Content); err != nil {
		zap.L().Error("send task message failed", zap.Uint("task_id", uint(id)), zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "message sent asynchronously"})
}

// SubscribeEvents SSE 流式订阅 OpenCode 全局事件（仅过滤当前 session）
func (h *TaskHandler) SubscribeEvents(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	taskID, _ := strconv.Atoi(c.Param("id"))

	// 获取任务信息
	var task model.Task
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := db.First(&task, taskID).Error; err != nil {
		c.JSON(404, gin.H{"error": "task not found"})
		return
	}

	if task.OpencodeSessionID == "" {
		c.JSON(400, gin.H{"error": "task has no associated session"})
		return
	}

	// 获取任务关联的资源池
	var pool model.ResourcePool
	poolDB := model.DB
	if !scope.IsSuperAdmin {
		poolDB = poolDB.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := poolDB.First(&pool, task.PoolID).Error; err != nil {
		c.JSON(500, gin.H{"error": "pool not found"})
		return
	}

	// 解密密码
	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	// 创建 OpenCode 客户端
	// 注意：SSE 需要无超时连接，因此使用专门的无超时客户端
	var client *service.OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = service.NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = service.NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}

	zap.L().Info("subscribing opencode events", zap.Uint("task_id", uint(taskID)), zap.String("session_id", task.OpencodeSessionID), zap.String("endpoint", pool.OpencodeEndpoint))

	// 设置为 SSE（添加防止代理缓冲的header）
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // 禁用Nginx缓冲
	c.Writer.Header().Set("Keep-Alive", "timeout=3600")
	c.Writer.Flush()

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	eventChan, err := client.SubscribeGlobalEvents(ctx, task.OpencodeSessionID)
	if err != nil {
		zap.L().Error("subscribe events failed", zap.Error(err))
		c.SSEvent("error", err.Error())
		c.Writer.Flush()
		return
	}

	zap.L().Info("sse connected", zap.Uint("task_id", uint(taskID)), zap.String("session_id", task.OpencodeSessionID))

	// 先发一个 connected 事件，确认前沿连接可用
	c.SSEvent("connected", "ok")
	c.Writer.Flush()

	for event := range eventChan {
		c.SSEvent(event.Event, event.Data)
		c.Writer.Flush()
	}

	zap.L().Info("sse disconnected", zap.Uint("task_id", uint(taskID)))
}

func (h *TaskHandler) DeleteSession(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if err := service.NewTaskService().DeleteTaskSession(uint(id)); err != nil {
		zap.L().Error("delete task session failed", zap.Uint("task_id", uint(id)), zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "session deleted"})
}

func (h *TaskHandler) Callback(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(403, gin.H{"error": "未认证"})
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	taskID, _ := strconv.Atoi(c.Query("task_id"))
	status := req["status"].(string)
	response, _ := req["response"].(string)

	var task model.Task
	if err := model.DB.Scopes(model.OrgScope(scope)).First(&task, taskID).Error; err != nil {
		c.JSON(403, gin.H{"error": "task not found"})
		return
	}

	var taskStatus string
	switch status {
	case "completed":
		taskStatus = "success"
	case "failed":
		taskStatus = "failed"
	default:
		taskStatus = status
	}

	if err := service.NewTaskService().UpdateStatus(scope, uint(taskID), service.StringToTaskStatus(taskStatus), response); err != nil {
		zap.L().Error("callback update failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	zap.L().Info("task callback", zap.Any("body", req))
	c.JSON(200, gin.H{"message": "callback received"})
}

// GetDiff 获取任务关联的指定文件的 diff 内容（实时从 GitLab 拉取）
// 参数: file 文件路径，不传则返回所有文件的 diff
func (h *TaskHandler) GetDiff(c *gin.Context) {
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

	id, _ := strconv.Atoi(c.Param("id"))
	task, err := service.NewTaskService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	// 权限检查：admin、任务提交者、项目负责人均可查看
	if !service.NewTaskService().CanViewTask(scope, user, *task) {
		c.JSON(403, gin.H{"error": "无权查看此任务"})
		return
	}

	if task.MRMergeID == 0 || task.ProjectID == 0 {
		c.JSON(400, gin.H{"error": "任务无关联 MR"})
		return
	}

	// 拉取 diff（复用 service 逻辑）
	diffFiles, err := service.NewTaskService().GetTaskDiffFiles(*task)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	filePath := c.Query("file")
	if filePath != "" {
		// 返回指定文件
		for _, df := range diffFiles {
			if np, ok := df["new_path"].(string); ok && np == filePath {
				c.JSON(200, gin.H{"code": 0, "data": df})
				return
			}
		}
		c.JSON(404, gin.H{"error": "file not found in diff"})
		return
	}

	// 返回全部
	c.JSON(200, gin.H{"code": 0, "data": diffFiles})
}

// ListTaskReviewRules 获取任务实际使用的评审规则（含截断记录）
func (h *TaskHandler) ListTaskReviewRules(c *gin.Context) {
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

	id, _ := strconv.Atoi(c.Param("id"))
	task, err := service.NewTaskService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	// 权限检查：admin、任务提交者、项目负责人均可查看
	if !service.NewTaskService().CanViewTask(scope, user, *task) {
		c.JSON(403, gin.H{"error": "无权查看此任务"})
		return
	}

	selected, truncated, total, err := service.NewTaskService().ListTaskReviewRules(task.ID)
	if err != nil {
		zap.L().Error("load task review rules failed", zap.Uint("task_id", task.ID), zap.Error(err))
		c.JSON(500, gin.H{"error": "加载规则列表失败"})
		return
	}

	// 获取模版配置中的 max_rules_per_review
	maxRules := 5
	var template model.ProjectTemplate
	if task.Project.TemplateID > 0 {
		if err := model.DB.First(&template, task.Project.TemplateID).Error; err == nil && template.MaxRulesPerReview > 0 {
			maxRules = template.MaxRulesPerReview
		}
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"total":     total,
			"selected":  selected,
			"truncated": truncated,
			"max_rules": maxRules, // 返回实际配置的 max_rules_per_review
		},
	})
}
