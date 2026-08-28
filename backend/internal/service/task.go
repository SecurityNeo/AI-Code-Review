package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ai-optimizer/backend/config"
	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"github.com/ai-optimizer/backend/pkg/diff"
	"github.com/ai-optimizer/backend/pkg/encrypt"
	"github.com/ai-optimizer/backend/pkg/gitlab"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

type TaskService struct {
	cfg             *config.Config
	TaskPipelineHub *PipelineSSEHub // Pipeline review SSE hub (key = task_id)
}

// ========== Pipeline 取消信号管理 ==========
// 用于支持手动停止任务时立即中断正在运行的 Pipeline 阶段

var (
	pipelineCancelChans = make(map[uint]chan struct{})
	pipelineCancelMu    sync.Mutex
)

// registerPipelineCancel 注册任务取消 channel（任务开始前调用）
func registerPipelineCancel(taskID uint) chan struct{} {
	ch := make(chan struct{})
	pipelineCancelMu.Lock()
	pipelineCancelChans[taskID] = ch
	pipelineCancelMu.Unlock()
	return ch
}

// unregisterPipelineCancel 注销任务取消 channel（任务结束后调用）
func unregisterPipelineCancel(taskID uint) {
	pipelineCancelMu.Lock()
	delete(pipelineCancelChans, taskID)
	pipelineCancelMu.Unlock()
}

// notifyPipelineCancel 通知指定任务的 Pipeline 取消（Abort 时调用）
func notifyPipelineCancel(taskID uint) {
	pipelineCancelMu.Lock()
	if ch, ok := pipelineCancelChans[taskID]; ok {
		close(ch)
		delete(pipelineCancelChans, taskID)
	}
	pipelineCancelMu.Unlock()
}

func NewTaskService() *TaskService {
	return &TaskService{
		cfg:             config.Load(),
		TaskPipelineHub: NewPipelineSSEHub(),
	}
}

// CanViewTask 判断用户是否有权查看某个任务详情
// admin 拥有全部权限；普通用户只能查看：自己提交的、或自己负责项目的任务
func (s *TaskService) CanViewTask(user model.User, task model.Task) bool {
	if user.Role == model.RoleAdmin {
		return true
	}
	if task.MRAuthor != "" && task.MRAuthor == user.GitlabUsername {
		return true
	}
	// 检查用户是否是任务所属项目的负责人（直接使用 user's primary key）
	if user.ID == 0 {
		return false
	}
	var count int64
	model.DB.Model(&model.ProjectResponsibility{}).
		Where("project_id = ? AND user_id = ?", task.ProjectID, user.ID).
		Count(&count)
	return count > 0
}

func (s *TaskService) List(user model.User, projectID uint, status string, startTime, endTime time.Time, author, mrIID string, hasPendingIssues bool, page, pageSize int) ([]model.Task, int64, error) {
	zap.L().Debug("TaskService.List called",
		zap.Uint("project_id", projectID),
		zap.String("status", status),
		zap.String("user", user.Username),
		zap.String("role", user.Role),
		zap.Bool("has_pending_issues", hasPendingIssues))

	var tasks []model.Task
	var total int64

	query := model.DB.Model(&model.Task{})
	// 可见性：admin 不过滤，普通用户只能查看自己提交的任务和自己负责项目的任务
	if user.Role != model.RoleAdmin {
		var conditions []string
		var args []interface{}

		// 1. 自己提交的任务
		if user.GitlabUsername != "" {
			conditions = append(conditions, "mr_author = ?")
			args = append(args, user.GitlabUsername)
		}
		if user.Username != "" {
			conditions = append(conditions, "mr_author = ?")
			args = append(args, user.Username)
		}

		// 2. 查找当前用户负责的项目（直接使用 user 的 primary key）
		var projectIDs []uint
		if user.ID > 0 {
			model.DB.Model(&model.ProjectResponsibility{}).
				Distinct("project_id").
				Where("user_id = ?", user.ID).
				Pluck("project_id", &projectIDs)
		}
			// 条件格式化保持原始格式
		// 3. 添加负责项目的条件
			if len(projectIDs) > 0 {
			conditions = append(conditions, "project_id IN ?")
			args = append(args, projectIDs)
		}

		// 5. OR 合并
		if len(conditions) > 0 {
			query = query.Where("("+strings.Join(conditions, " OR ")+")", args...)
		} else {
			query = query.Where("1 = 0") // 安全兜底：什么也看不到
		}
	}

	if projectID > 0 {
		query = query.Where("project_id = ?", projectID)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if !startTime.IsZero() {
		query = query.Where("created_at >= ?", startTime)
	}
	if !endTime.IsZero() {
		query = query.Where("created_at < ?", endTime)
	}
	if author != "" {
		query = query.Where("mr_author LIKE ?", "%"+author+"%")
	}
	if mrIID != "" {
		query = query.Where("mr_merge_id = ?", mrIID)
	}

	// 子查询：统计每个任务的 pending issue 数量
	pendingSubquery := model.DB.Table("review_issues").
		Select("task_id, COUNT(*) as cnt").
		Where("status = ? AND deleted_at IS NULL", "pending").
		Group("task_id")

	query = query.Joins("LEFT JOIN (?) AS pending_counts ON pending_counts.task_id = tasks.id", pendingSubquery).
		Select("tasks.*, COALESCE(pending_counts.cnt, 0) as pending_issue_count")

	if hasPendingIssues {
		query = query.Where("COALESCE(pending_counts.cnt, 0) > 0")
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = query.Order("created_at DESC").Scopes(model.Paginate(page, pageSize))

	if err := query.Preload("Project").Preload("Pool").Preload("UsedModel").Find(&tasks).Error; err != nil {
		return nil, 0, err
	}

	return tasks, total, nil
}

func (s *TaskService) Get(id uint) (*model.Task, error) {
	var task model.Task
	if err := model.DB.Preload("Project").Preload("Pool").Preload("UsedModel").First(&task, id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *TaskService) Create(data map[string]interface{}) (*model.Task, error) {
	modelID := uint(0)
	if m, ok := data["model_id"].(float64); ok {
		modelID = uint(m)
	}

	mrURL := ""
	if v, ok := data["mr_url"].(string); ok {
		mrURL = v
	}
	mrAuthor := ""
	if v, ok := data["author"].(string); ok {
		mrAuthor = v
	}
	authorDisplayName := ""
	if v, ok := data["author_display_name"].(string); ok {
		authorDisplayName = v
	}
	sourceBranch := ""
	if v, ok := data["source_branch"].(string); ok {
		sourceBranch = v
	}
	targetBranch := ""
	if v, ok := data["target_branch"].(string); ok {
		targetBranch = v
	}
	mrTitle := ""
	if v, ok := data["mr_title"].(string); ok {
		mrTitle = v
	}
	noteID := 0
	if v, ok := data["note_id"].(float64); ok {
		noteID = int(v)
	}

	triggerSource := ""
	if v, ok := data["trigger_source"].(string); ok && v != "" {
		triggerSource = v
	}
	scoreValue := 0
	if v, ok := data["score_value"].(float64); ok && v > 0 {
		scoreValue = int(v)
	}
	aiprompt := ""
	if v, ok := data["ai_prompt"].(string); ok {
		aiprompt = v
	}

	task := model.Task{
		ProjectID:           uint(data["project_id"].(float64)),
		PoolID:              0,
		UsedModelID:         modelID,
		MRMergeID:           int(data["mr_iid"].(float64)),
		MRTitle:             mrTitle,
		MRURL:               mrURL,
		MRAuthor:            mrAuthor,
		MRAuthorDisplayName: authorDisplayName,
		SourceBranch:        sourceBranch,
		TargetBranch:        targetBranch,
		NoteID:              noteID,
		AIPrompt:            aiprompt,
		AIResponseJSON:      "{}", // MySQL JSON 列不接受空字符串
		DimensionScores:     "{}", // MySQL JSON 列不接受空字符串
		TaskType:            "chat",
		Status:              model.TaskPending,
		TriggerType:         data["trigger_type"].(string),
		TriggerSource:       triggerSource,
		ScoreValue:          scoreValue,
	}
	// pool_id 可选（AI 评审任务无资源池）
	if v, ok := data["pool_id"].(float64); ok {
		task.PoolID = uint(v)
	}
	if v, ok := data["task_type"].(string); ok && v != "" {
		task.TaskType = v
	}
	// 如果 pool_id 为 0（无资源池，如 AI 评审任务），Omit 避免外键约束失败
	if task.PoolID == 0 {
		if err := model.DB.Omit("pool_id").Create(&task).Error; err != nil {
			return nil, err
		}
	} else {
		if err := model.DB.Create(&task).Error; err != nil {
			return nil, err
		}
	}
	return &task, nil
}

func (s *TaskService) Execute(taskID uint) error {
	return s.ExecuteWithComment(taskID, "")
}

func (s *TaskService) ExecuteWithComment(taskID uint, commentOverride string) error {
	zap.L().Info("========== TaskService.Execute started ==========", zap.Uint("task_id", taskID))
	var task model.Task
	if err := model.DB.Preload("Project").First(&task, taskID).Error; err != nil {
		zap.L().Error("task not found", zap.Uint("task_id", taskID), zap.Error(err))
		return err
	}

	// 约束检查：同一项目只能有一个 running 的"深度评审"任务（task_type=chat），
	// AI 评审任务（task_type=review）与其不互斥，可以并发执行。
	var runningCount int64
	model.DB.Model(&model.Task{}).Where("project_id = ? AND status = ? AND task_type = ? AND id != ?", task.ProjectID, model.TaskRunning, "chat", taskID).Count(&runningCount)
	if runningCount > 0 {
		zap.L().Info("project has running task, task queued in pending",
			zap.Uint("task_id", taskID),
			zap.Uint("project_id", task.ProjectID),
			zap.Int64("running_count", runningCount))
		return nil
	}

	task.Status = model.TaskRunning
	model.DB.Model(&task).Select("Status").Updates(task)
	zap.L().Info("task status updated to running", zap.Uint("task_id", task.ID))

	// OpenCode 路径注入人工复核意见
	aiPrompt := task.AIPrompt
	var reviewCommentText string
	if commentOverride != "" {
		reviewCommentText = commentOverride
	}
	if reviewCommentText != "" {
		aiPrompt += "\n\n### ⚠️ 人工复核意见（请重点参考）\n" + reviewCommentText
		zap.L().Info("injected user review comment into OpenCode task", zap.Uint("task_id", task.ID))
	}

	zap.L().Info("calling OpencodeService.ExecuteTaskWithSession", zap.Uint("pool_id", task.PoolID))
	opencodeSvc := NewOpencodeService()
	sessionID, aiResponse, err := opencodeSvc.ExecuteTaskWithSession(
		task.ID,
		task.PoolID,
		task.Project.ProjectPath,
		task.Project.Name,
		aiPrompt,
		s.cfg.ProjectBaseDir,
		task.Project.AccessToken,
	)
	if err != nil {
		zap.L().Error("ExecuteTaskWithSession failed", zap.Error(err))

		// 重新查询获取最新的 started_at
		var currentTask model.Task
		model.DB.First(&currentTask, taskID)

		currentTask.Status = model.TaskFailed
		currentTask.ErrorMsg = err.Error()
		now := time.Now()
		currentTask.CompletedAt = &now
		if currentTask.StartedAt != nil {
			currentTask.DurationSec = int(now.Sub(*currentTask.StartedAt).Seconds())
		}
		// 条件更新：只有状态仍是 running 时才写入失败，防止被 TimeoutCheck 覆盖后回写
		res := model.DB.Model(&model.Task{}).Where("id = ? AND status = ?", taskID, model.TaskRunning).Updates(map[string]interface{}{
			"status":       model.TaskFailed,
			"error_msg":    err.Error(),
			"completed_at": now,
			"duration_sec": currentTask.DurationSec,
		})
		if res.Error != nil {
			zap.L().Error("failed to update task status to failed", zap.Uint("task_id", taskID), zap.Error(res.Error))
		}
		if res.RowsAffected == 0 {
			zap.L().Info("OpenCode task no longer running, skip writing failed status", zap.Uint("task_id", taskID))
		}

		// 发送 GitLab MR 评论告知失败
		go func() {
			failComment := "❌ 任务运行失败，请联系管理员处理。"
			if postMRComment(task.Project.ProjectPath, task.Project.AccessToken, task.MRMergeID, failComment, task.NoteID) != nil {
				zap.L().Error("post fail comment to MR failed", zap.Error(err))
			} else {
				zap.L().Info("fail comment posted to MR", zap.Uint("task_id", taskID))
			}
		}()

		// 任务失败后，触发队列中的下一个 pending 任务
		s.startNextPendingTask(task.ProjectID)

		return err
	}

	task.OpencodeSessionID = sessionID
	task.AIResponse = aiResponse
	task.Status = model.TaskSuccess

	var startedAt time.Time
	if err := model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Pluck("started_at", &startedAt).Error; err == nil && !startedAt.IsZero() {
		now := time.Now()
		task.StartedAt = &startedAt
		task.CompletedAt = &now
		task.DurationSec = int(now.Sub(startedAt).Seconds())
		zap.L().Info("task completed, calculating duration", zap.Uint("task_id", task.ID), zap.Int("duration_sec", task.DurationSec), zap.Time("started_at", startedAt), zap.Time("completed_at", *task.CompletedAt))
	}

	model.DB.Model(&task).Select("OpencodeSessionID", "AIResponse", "Status", "CompletedAt", "DurationSec").Where("status = ?", model.TaskRunning).Updates(task)
	// chat 任务也获取 diff 元信息用于详情页展示（异步）
	s.SaveChatTaskDiffFiles(task.ID)
	zap.L().Info("task saved to DB", zap.Uint("task_id", task.ID), zap.Int("duration_sec", task.DurationSec))

	zap.L().Info("task completed", zap.Uint("task_id", task.ID), zap.String("session_id", sessionID))

	// 发送成功评论到 MR（检查任务是否被停止）
	go func() {
		// 重新查询任务状态，确认未被停止
		var t model.Task
		if err := model.DB.Preload("Project").First(&t, taskID).Error; err != nil {
			return
		}
		if t.Status == model.TaskStopped {
			zap.L().Info("task was stopped, skip posting comment", zap.Uint("task_id", taskID))
			return
		}
		// 从数据库获取最新的 AIResponse
		if t.AIResponse == "" {
			zap.L().Info("task AIResponse is empty, skip posting comment", zap.Uint("task_id", taskID))
			return
		}
		prefix := fmt.Sprintf("深度代码审查任务【%d】执行完成，审查报告：\n", taskID)
		comment := prefix + t.AIResponse
		if err := postMRComment(t.Project.ProjectPath, t.Project.AccessToken, t.MRMergeID, comment, t.NoteID); err != nil {
			zap.L().Error("post MR result comment failed", zap.Error(err))
		} else {
			zap.L().Info("MR result comment posted")
		}
	}()

	// 任务完成后，触发队列中的下一个 pending 任务
	s.startNextPendingTask(task.ProjectID)

	return nil
}

// startNextPendingTask 查找同一项目最早的 pending 任务并启动执行
func (s *TaskService) startNextPendingTask(projectID uint) {
	var nextTask model.Task
	if err := model.SilentFirst(model.DB.Where("project_id = ? AND status = ?", projectID, model.TaskPending).Order("created_at ASC"), &nextTask); err != nil {
		zap.L().Debug("no pending tasks in queue", zap.Uint("project_id", projectID))
		return
	}

	zap.L().Info("starting next pending task from queue",
		zap.Uint("task_id", nextTask.ID),
		zap.Uint("project_id", projectID),
		zap.String("task_type", nextTask.TaskType))

	go func(tid uint, taskType string) {
		var err error
		if taskType == "review" {
			err = s.ExecuteAIReviewTask(tid)
		} else {
			err = s.Execute(tid)
		}
		if err != nil {
			zap.L().Error("queued task execution failed", zap.Uint("task_id", tid), zap.Error(err))
		}
	}(nextTask.ID, nextTask.TaskType)
}

func (s *TaskService) UpdateStatus(taskID uint, status model.TaskStatus, response string) error {
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return err
	}

	if status == model.TaskSuccess || status == model.TaskFailed {
		now := time.Now()
		task.CompletedAt = &now
		task.DurationSec = int(now.Sub(*task.StartedAt).Seconds())
	}

	updates := map[string]interface{}{
		"status":       status,
		"completed_at": task.CompletedAt,
		"duration_sec": task.DurationSec,
	}
	if response != "" {
		updates["ai_response"] = response
	}
	res := model.DB.Model(&model.Task{}).Where("id = ? AND status = ?", taskID, model.TaskRunning).Updates(updates)
	if res.Error != nil {
		zap.L().Error("failed to update task status", zap.Uint("task_id", taskID), zap.String("target_status", string(status)), zap.Error(res.Error))
		return res.Error
	}
	if res.RowsAffected == 0 {
		zap.L().Info("task no longer running, skip updating status", zap.Uint("task_id", taskID), zap.String("target_status", string(status)))
	}

	// 注意：任务完成后不再自动删除session，保留供查看对话历史
	// 如需删除，请调用 DELETE /api/v1/tasks/:id/session 接口
	// if task.OpencodeSessionID != "" && (status == model.TaskSuccess || status == model.TaskFailed) {
	// 	opencodeSvc := NewOpencodeService()
	// 	opencodeSvc.DeleteSession(task.PoolID, task.OpencodeSessionID)
	// 	zap.L().Info("session deleted after task completion", zap.String("session_id", task.OpencodeSessionID))
	// }

	return nil
}

func (s *TaskService) Abort(taskID uint) error {
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return err
	}

	opencodeSvc := NewOpencodeService()

	// 如果有 session ID，尝试终止
	if task.OpencodeSessionID != "" {
		zap.L().Info("aborting opencode session", zap.String("session_id", task.OpencodeSessionID))
		if err := opencodeSvc.AbortTask(task.PoolID, task.OpencodeSessionID); err != nil {
			zap.L().Error("abort task failed", zap.Error(err))
		}
		// 任务停止时也不删除session，保留供查看对话历史
		// if task.OpencodeSessionID != "" {
		// 	zap.L().Info("deleting stopped session", zap.String("session_id", task.OpencodeSessionID))
		// 	if err := opencodeSvc.DeleteSession(task.PoolID, task.OpencodeSessionID); err != nil {
		// 		zap.L().Error("delete session failed", zap.Error(err))
		// 	}
		// }
	} else {
		zap.L().Warn("abort task: no session_id found", zap.Uint("task_id", taskID))
	}

	// 通知正在运行的 Pipeline 引擎立即取消（不管当前在哪个阶段/批次）
	notifyPipelineCancel(taskID)

	now := time.Now()
	updates := map[string]interface{}{
		"status":       model.TaskStopped,
		"error_msg":    "手动终止",
		"completed_at": now,
		"duration_sec": func() int {
			if task.StartedAt != nil {
				return int(now.Sub(*task.StartedAt).Seconds())
			}
			return 0
		}(),
	}
	// Abort 统一使用 Where 条件更新，避免覆盖已被修改的状态
	res := model.DB.Model(&model.Task{}).Where("id = ? AND status = ?", task.ID, model.TaskRunning).Updates(updates)
	if res.Error != nil {
		zap.L().Error("abort task update failed", zap.Uint("task_id", taskID), zap.Error(res.Error))
		return res.Error
	}
	if res.RowsAffected == 0 {
		zap.L().Info("abort task: task no longer running, skip updating", zap.Uint("task_id", taskID))
	}

	// 任务被停止后，触发队列中的下一个 pending 任务
	s.startNextPendingTask(task.ProjectID)

	return nil
}

func (s *TaskService) ListReviewComments(taskID uint) ([]model.TaskReviewComment, error) {
	var comments []model.TaskReviewComment
	if err := model.DB.Where("task_id = ?", taskID).Order("retry_round asc").Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

func (s *TaskService) Retry(taskID uint, userReviewComment string, selectedCommentIDs []uint, operatorID uint, clientIP string) error {
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return err
	}

	if task.Status != model.TaskFailed && task.Status != model.TaskPending && task.Status != model.TaskStopped && task.Status != model.TaskTimeout && task.Status != model.TaskSuccess {
		return fmt.Errorf("only failed, stopped, timeout, pending or success tasks can be retried")
	}

	// 拼接选中的历史复核意见 + 新复核意见
	var injectedParts []string
	if len(selectedCommentIDs) > 0 {
		var selected []model.TaskReviewComment
		if err := model.DB.Where("task_id = ? AND id IN ?", taskID, selectedCommentIDs).Order("retry_round asc").Find(&selected).Error; err == nil {
			for _, c := range selected {
				injectedParts = append(injectedParts, fmt.Sprintf("- 【第%d次复核】%s", c.RetryRound, c.Content))
			}
		}
	}

	// 将新复核意见写入独立表
	if userReviewComment != "" {
		comment := model.TaskReviewComment{
			TaskID:     task.ID,
			Content:    userReviewComment,
			RetryRound: task.RetryCount + 1,
			OperatorID: operatorID,
		}
		if err := model.DB.Create(&comment).Error; err != nil {
			zap.L().Error("create task review comment failed", zap.Error(err))
			return fmt.Errorf("保存复核意见失败: %w", err)
		}
		injectedParts = append(injectedParts, fmt.Sprintf("- 【第%d次复核】%s", task.RetryCount+1, userReviewComment))
	}
	injectedText := strings.Join(injectedParts, "\n")

	// 记录操作日志（无论是否有新评论，只要发生重试即记录）
	opDetail := fmt.Sprintf("任务ID:%d, 选中历史意见:%d条", task.ID, len(selectedCommentIDs))
	if userReviewComment != "" {
		opDetail += ", 新增复核意见"
	}
	model.RecordOpLog("任务复核", opDetail, task.ID, operatorID, "success", "", clientIP)

	task.Status = model.TaskPending
	task.ErrorMsg = ""
	task.RetryCount++
	// 重置 started_at，避免 TimeoutCheck 用上一次旧时间判定超时
	now := time.Now()
	task.StartedAt = &now
	// review 类型无资源池，避免外键约束失败
	if task.TaskType == "review" {
		model.DB.Omit("pool_id").Save(&task)
	} else {
		model.DB.Save(&task)
	}

	// 清理旧的 Pipeline 执行记录（防止重试时新旧数据混合展示）
	// 先删除子阶段，再删除父阶段（避免外键约束）
	model.DB.Where("task_id = ? AND parent_id IS NOT NULL", task.ID).Delete(&model.TaskPipelineExecution{})
	model.DB.Where("task_id = ? AND parent_id IS NULL", task.ID).Delete(&model.TaskPipelineExecution{})

	// 根据任务类型调用对应的执行方法（通过闭包传递注入文本）
	if task.TaskType == "review" {
		go func(id uint, injected string) {
			s.ExecuteAIReviewTaskWithComment(id, injected)
		}(taskID, injectedText)
	} else {
		go func(id uint, injected string) {
			s.ExecuteWithComment(id, injected)
		}(taskID, injectedText)
	}
	return nil
}

func (s *TaskService) TimeoutCheck() {
	// 非 review 类型任务使用系统配置中的超时时间
	var sysConfig model.SystemConfig
	timeoutMin := 120
	if err := model.SilentFirst(model.DB, &sysConfig); err == nil && sysConfig.TaskTimeoutMin > 0 {
		timeoutMin = sysConfig.TaskTimeoutMin
	} else if err != nil {
		zap.L().Debug("timeout check using default timeout", zap.Int("timeout_min", timeoutMin), zap.Error(err))
	}

	// AI 评审类任务使用各阶段超时之和 + 宽裕时间，不再受系统配置限制
	reviewTimeoutMin := getReviewTaskTimeoutMin()
	zap.L().Debug("timeout check thresholds", zap.Int("sys_timeout_min", timeoutMin), zap.Int("review_timeout_min", reviewTimeoutMin))

	var tasks []model.Task
	if err := model.DB.Where("status = ?", model.TaskRunning).Find(&tasks).Error; err != nil {
		zap.L().Error("timeout check query failed", zap.Error(err))
		return
	}

	now := time.Now()
	for _, task := range tasks {
		if task.StartedAt == nil {
			continue
		}

		var taskTimeoutMin int
		if task.TaskType == "review" {
			taskTimeoutMin = reviewTimeoutMin
		} else {
			taskTimeoutMin = timeoutMin
		}

		threshold := now.Add(-time.Duration(taskTimeoutMin) * time.Minute)
		if task.StartedAt != nil && !task.StartedAt.Before(threshold) {
			continue // 未超时
		}

		zap.L().Info("task timeout, aborting", zap.Uint("task_id", task.ID), zap.String("task_type", task.TaskType), zap.Duration("elapsed", now.Sub(*task.StartedAt)))

		// 终止 OpenCode session（如存在）
		if task.OpencodeSessionID != "" {
			opencodeSvc := NewOpencodeService()
			if err := opencodeSvc.AbortTask(task.PoolID, task.OpencodeSessionID); err != nil {
				zap.L().Error("abort timeout task session failed", zap.Uint("task_id", task.ID), zap.Error(err))
			}
		}

		// 条件更新为 timeout，避免覆盖已被修改的状态
		updates := map[string]interface{}{
			"status": model.TaskTimeout,
			"error_msg": fmt.Sprintf("任务超时（已运行 %d 分钟，超过 %s类型任务阈值 %d 分钟）", int(now.Sub(*task.StartedAt).Minutes()), func() string {
				if task.TaskType == "review" {
					return "AI评审"
				} else {
					return ""
				}
			}(), taskTimeoutMin),
			"completed_at": now,
			"duration_sec": func() int {
				if task.StartedAt != nil {
					return int(now.Sub(*task.StartedAt).Seconds())
				}
				return 0
			}(),
		}
		res := model.DB.Model(&model.Task{}).Where("id = ? AND status = ?", task.ID, model.TaskRunning).Updates(updates)
		if res.Error != nil {
			zap.L().Error("timeout check update failed", zap.Uint("task_id", task.ID), zap.Error(res.Error))
		} else if res.RowsAffected == 0 {
			zap.L().Info("timeout check: task no longer running, skip updating", zap.Uint("task_id", task.ID))
		}

		// 超时后触发队列中的下一个 pending 任务
		s.startNextPendingTask(task.ProjectID)
	}
}

// getReviewTaskTimeoutMin 计算 AI 评审类任务的超时时间（分钟）。
// 基于 ReviewAgentConfig 中所有启用阶段的 timeout 之和，加上 30 分钟宽裕时间。
func getReviewTaskTimeoutMin() int {
	var cfg model.ReviewAgentConfig
	if err := model.DB.First(&cfg, 1).Error; err != nil {
		zap.L().Warn("getReviewTaskTimeoutMin: failed to load ReviewAgentConfig, using default", zap.Error(err))
		return 300 // 默认 5 小时
	}

	totalSec := 0
	for _, code := range cfg.EnabledStageCodes() {
		sc := cfg.StageConfig(code)
		if sc == nil {
			continue
		}
		t, ok := sc["timeout"]
		if !ok {
			continue
		}
		var timeout int
		switch val := t.(type) {
		case float64:
			timeout = int(val)
		case int:
			timeout = val
		case json.Number:
			iv, _ := val.Int64()
			timeout = int(iv)
		}
		totalSec += timeout
	}

	if totalSec <= 0 {
		zap.L().Debug("getReviewTaskTimeoutMin: no stage timeouts found, using default")
		return 300 // 默认 5 小时
	}

	// 转换为分钟，加上 30 分钟宽裕时间
	timeoutMin := (totalSec / 60) + 30
	zap.L().Debug("getReviewTaskTimeoutMin calculated", zap.Int("stage_timeout_sec", totalSec), zap.Int("timeout_min", timeoutMin))
	return timeoutMin
}

func StringToTaskStatus(s string) model.TaskStatus {
	switch s {
	case "success":
		return model.TaskSuccess
	case "failed":
		return model.TaskFailed
	case "running":
		return model.TaskRunning
	default:
		return model.TaskPending
	}
}

func postMRComment(projectPath, gitlabToken string, mrIID int, comment string, noteID int) error {
	if projectPath == "" || gitlabToken == "" || mrIID == 0 {
		return fmt.Errorf("missing required params")
	}

	protocol := "https"
	if strings.HasPrefix(projectPath, "http://") {
		protocol = "http"
		projectPath = strings.TrimPrefix(projectPath, "http://")
	} else if strings.HasPrefix(projectPath, "https://") {
		projectPath = strings.TrimPrefix(projectPath, "https://")
	}
	parts := strings.SplitN(projectPath, "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("invalid project path: %s", projectPath)
	}
	namespaceProject := parts[1]
	namespaceProject = strings.TrimSuffix(namespaceProject, ".git")

	url := fmt.Sprintf("%s://%s/api/v4/projects/%s/merge_requests/%d/notes",
		protocol, parts[0], strings.ReplaceAll(namespaceProject, "/", "%2F"), mrIID)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	body := map[string]interface{}{"body": comment}
	if noteID > 0 {
		body["note_id"] = noteID
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", gitlabToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post comment failed: %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetTaskMessages 获取任务关联的 OpenCode session 对话记录
// 仅当任务状态为 running 且 opencode_session_id 非空时有效
func (s *TaskService) GetTaskMessages(taskID uint) ([]OpenCodeMessage, error) {
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("task not found: %w", err)
	}

	if task.OpencodeSessionID == "" {
		return nil, fmt.Errorf("task has no associated session")
	}

	// 获取任务关联的资源池
	var pool model.ResourcePool
	if err := model.DB.First(&pool, task.PoolID).Error; err != nil {
		return nil, fmt.Errorf("pool not found: %w", err)
	}

	// 解密密码
	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	// 创建 OpenCode 客户端（使用短超时，30秒足够获取消息列表）
	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}

	// 调用 /session/{id}/message 获取对话列表
	messages, err := client.GetSessionMessages(task.OpencodeSessionID)
	if err != nil {
		return nil, fmt.Errorf("fetch session messages failed: %w", err)
	}

	return messages, nil
}

// SendTaskMessage 向任务的 OpenCode session 异步发送用户消息
func (s *TaskService) SendTaskMessage(taskID uint, content string) error {
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return fmt.Errorf("task not found: %w", err)
	}

	if task.OpencodeSessionID == "" {
		return fmt.Errorf("task has no associated session")
	}

	// 获取项目目录（用于 X-OpenCode-Directory header）
	var project model.Project
	var projectDir string
	if err := model.DB.First(&project, task.ProjectID).Error; err == nil {
		projectDir = s.cfg.ProjectBaseDir + project.Name
	}

	// 获取任务关联的资源池
	var pool model.ResourcePool
	if err := model.DB.First(&pool, task.PoolID).Error; err != nil {
		return fmt.Errorf("pool not found: %w", err)
	}

	// 解密密码
	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	// 创建 OpenCode 客户端
	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}

	// 使用异步接口发送消息，并带上 X-OpenCode-Directory header
	err := client.SendPromptAsync(task.OpencodeSessionID, content, projectDir)
	if err != nil {
		return fmt.Errorf("send async message failed: %w", err)
	}

	zap.L().Info("task async message sent", zap.Uint("task_id", taskID), zap.String("session_id", task.OpencodeSessionID), zap.String("directory", projectDir))
	return nil
}

// 1. 调用 OpenCode API 删除 session
// 2. 清空数据库中的 opencode_session_id
func (s *TaskService) DeleteTaskSession(taskID uint) error {
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return fmt.Errorf("task not found: %w", err)
	}

	if task.OpencodeSessionID == "" {
		return fmt.Errorf("task has no session to delete")
	}

	// 获取任务关联的资源池
	var pool model.ResourcePool
	if err := model.DB.First(&pool, task.PoolID).Error; err != nil {
		return fmt.Errorf("pool not found: %w", err)
	}

	// 解密密码
	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	// 创建 OpenCode 客户端
	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}

	// 调用 OpenCode 删除 session（忽略错误，因为 session 可能已经不存在）
	_, err := client.DeleteSession(task.OpencodeSessionID)
	if err != nil {
		zap.L().Warn("opencode delete session failed, continuing", zap.Error(err), zap.String("session_id", task.OpencodeSessionID))
	}

	// 清空数据库中的 session_id
	model.DB.Model(&task).Update("opencode_session_id", "")
	zap.L().Info("task session deleted", zap.Uint("task_id", taskID), zap.String("session_id", task.OpencodeSessionID))

	return nil
}

// ========== AI 评审任务 ==========

const (
	// charsPerTokenApprox 是 splitIntoBatches / isSingleBatch 估算 token 用的字符/token 比。
	// maxDiffFiles / maxTokensPerBatch 已迁入 ReviewAgentConfig.stage_configs["batch_review_frame"] 并通过
	// service.SysCfgMaxDiffFiles / SysCfgMaxTokensPerBatch 读取。
	charsPerTokenApprox = 4
)

// ExecuteAIReviewTask 执行 AI 评审任务（大模型直连，不走 OpenCode）
func (s *TaskService) ExecuteAIReviewTask(taskID uint) error {
	return s.ExecuteAIReviewTaskWithComment(taskID, "")
}

func (s *TaskService) ExecuteAIReviewTaskWithComment(taskID uint, commentOverride string) error {
	var task model.Task
	if err := model.DB.Preload("Project").Preload("Project.Template").First(&task, taskID).Error; err != nil {
		zap.L().Error("review task not found", zap.Uint("task_id", taskID), zap.Error(err))
		return err
	}

	// Pipeline 评审引擎已默认启用
	zap.L().Info("Pipeline 模式执行任务", zap.Uint("task_id", taskID))
	return s.executePipelineReviewTask(task, commentOverride)
}

func (s *TaskService) failReviewTask(task model.Task, errMsg string) error {
	now := time.Now()
	res := model.DB.Model(&model.Task{}).Where("id = ? AND status = ?", task.ID, model.TaskRunning).Updates(map[string]interface{}{
		"status":       model.TaskFailed,
		"error_msg":    errMsg,
		"completed_at": now,
		"duration_sec": func() int {
			if task.StartedAt != nil {
				return int(now.Sub(*task.StartedAt).Seconds())
			}
			return 0
		}(),
	})
	if res.RowsAffected == 0 {
		zap.L().Info("review task no longer running, skip writing failed status", zap.Uint("task_id", task.ID))
		return fmt.Errorf("%s", errMsg)
	}
	zap.L().Error("review task failed", zap.Uint("task_id", task.ID), zap.String("error", errMsg))
	go s.postReviewComment(task, "❌ AI 评审失败："+errMsg)
	// AI 评审失败后也需要唤醒同项目 pending 队列，避免后续任务饿死
	s.startNextPendingTask(task.ProjectID)
	return fmt.Errorf("%s", errMsg)
}

func (s *TaskService) fetchMRDiffFiles(task model.Task) ([]gitlab.DiffFile, int, int, error) {
	host := extractHostFromPath(task.Project.ProjectPath)
	client := gitlab.NewClient(host, task.Project.AccessToken)

	zap.L().Info("fetching MR diff",
		zap.String("host", host),
		zap.String("project", task.Project.Name),
		zap.Int("gitlab_project_id", task.Project.GitLabProjectID),
		zap.Int("mr_iid", task.MRMergeID))

	files, additions, deletions, err := client.GetMergeRequestDiffFiles(task.Project.GitLabProjectID, task.MRMergeID)
	if err != nil {
		zap.L().Error("get MR diff from gitlab failed",
			zap.String("host", host),
			zap.Int("gitlab_project_id", task.Project.GitLabProjectID),
			zap.Int("mr_iid", task.MRMergeID),
			zap.String("access_token_empty", fmt.Sprintf("%v", task.Project.AccessToken == "")),
			zap.Error(err))
		return nil, 0, 0, fmt.Errorf("获取 MR diff 失败: %w", err)
	}
	if len(files) == 0 {
		zap.L().Warn("MR diff empty",
			zap.String("host", host),
			zap.Int("gitlab_project_id", task.Project.GitLabProjectID),
			zap.Int("mr_iid", task.MRMergeID))
		return nil, 0, 0, fmt.Errorf("MR diff 为空")
	}

	zap.L().Info("diff parsed",
		zap.Int("files", len(files)),
		zap.Int("additions", additions),
		zap.Int("deletions", deletions))

	return files, additions, deletions, nil
}

func (s *TaskService) fetchMRCommits(task model.Task) []gitlab.CommitInfo {
	host := extractHostFromPath(task.Project.ProjectPath)
	client := gitlab.NewClient(host, task.Project.AccessToken)
	commits, err := client.GetMergeRequestCommits(task.Project.GitLabProjectID, task.MRMergeID)
	if err != nil {
		zap.L().Warn("获取 commits 失败", zap.Error(err))
		return []gitlab.CommitInfo{}
	}
	return commits
}

// executePipelineReviewTask Pipeline 结构化评审任务执行
// 复用 engine/builder.go + engine/parser.go 的完整能力
func (s *TaskService) executePipelineReviewTask(task model.Task, commentOverride string) error {
	// 【修复】在进入 Pipeline 前先将任务状态从 pending 转为 running，
	// 确保后续 fetchMRDiffFiles 失败时 failReviewTask 的 WHERE status=running 能命中
	if task.Status == model.TaskPending {
		task.Status = model.TaskRunning
		now := time.Now()
		task.StartedAt = &now
		if err := model.DB.Model(&task).Select("Status", "StartedAt").Updates(task).Error; err != nil {
			zap.L().Warn("Pipeline 任务启动时更新状态失败", zap.Uint("task_id", task.ID), zap.Error(err))
		}
	}

	// 1. 获取 diff 文件
	diffFiles, additions, deletions, err := s.fetchMRDiffFiles(task)
	if err != nil {
		return s.failReviewTask(task, err.Error())
	}

	// 【注意】保存原始 diff 副本（用于漏洞扫描读取 go.mod 等依赖清单）
	var diffFilesRaw []gitlab.DiffFile
	if len(diffFiles) > 0 {
		diffFilesRaw = make([]gitlab.DiffFile, len(diffFiles))
		copy(diffFilesRaw, diffFiles)
	}

	// 【新增】应用文件过滤规则（依赖文件/第三方代码排除）
	// 过滤后的 diffFiles 供除 dependency_scan 外的所有阶段共享
	var filterAgentCfg model.ReviewAgentConfig
	if err := model.DB.First(&filterAgentCfg, 1).Error; err == nil {
		filterCfg := engine.LoadFileFilterConfig(filterAgentCfg)
		lang := ""
		if task.Project.Language != "" {
			lang = task.Project.Language
		}
		matcher := engine.NewFileMatcher(filterCfg, lang)
		var stats engine.FilterStats
		diffFiles, stats = engine.FilterDiffFiles(diffFiles, matcher)

		if stats.Filtered > 0 {
			zap.L().Info("Pipeline: diff 文件已过滤",
				zap.Uint("task_id", task.ID),
				zap.Int("total", stats.Total),
				zap.Int("kept", stats.Kept),
				zap.Int("filtered", stats.Filtered),
				zap.Strings("filtered_paths", stats.FilteredPaths))
		}

		// 如果所有文件都被过滤，记录警告日志（Pipeline 后续阶段会优雅处理空文件列表）
		if stats.Total > 0 && stats.Kept == 0 {
			zap.L().Warn("Pipeline: 所有 diff 文件均已被过滤",
				zap.Uint("task_id", task.ID),
				zap.Int("filtered_count", stats.Filtered),
				zap.Strings("filtered_paths", stats.FilteredPaths))
		}

		// 持久化过滤统计（供前端展示）
		if err := model.DB.Model(&task).Update("diff_filter_stats", engine.MarshalFilterStats(stats)).Error; err != nil {
			zap.L().Warn("Pipeline: 保存 diff 过滤统计失败", zap.Uint("task_id", task.ID), zap.Error(err))
		}
	}

	// 限制最多 N 个文件
	maxDiffFiles := SysCfgMaxDiffFiles()
	if len(diffFiles) > maxDiffFiles {
		zap.L().Warn("diff files exceed limit, truncating",
			zap.Int("original", len(diffFiles)),
			zap.Int("limit", maxDiffFiles))
		diffFiles = diffFiles[:maxDiffFiles]
	}

	// 2. 获取 commits
	commits := s.fetchMRCommits(task)
	commitsText := formatCommitsForReview(commits)

	// 3. 读取系统截断阈值（仅用于 diff_files_json 持久化时的展示截断，不用于大模型 prompt）
	var sysCfg model.SystemConfig
	truncationThreshold := 5000
	if err := model.DB.First(&sysCfg).Error; err == nil && sysCfg.DiffTruncationThreshold > 0 {
		truncationThreshold = sysCfg.DiffTruncationThreshold
	}

	// 4. 加载项目模板
	var projectTemplate model.ProjectTemplate
	if task.Project.TemplateID > 0 {
		model.DB.First(&projectTemplate, task.Project.TemplateID)
	}

	// 5. 加载并合并评审规则（与 Legacy runStructuredAIReview 完全一致）
	var allRules []model.ReviewRule
	if err := model.DB.Find(&allRules).Error; err != nil {
		zap.L().Warn("Pipeline: load all review rules failed", zap.Uint("project_id", task.ProjectID), zap.Error(err))
	}

	var configs []model.ProjectReviewConfig
	if err := model.DB.Where("project_id = ?", task.ProjectID).Find(&configs).Error; err != nil {
		zap.L().Warn("Pipeline: load project review configs failed", zap.Uint("project_id", task.ProjectID), zap.Error(err))
	}
	configMap := make(map[uint]model.ProjectReviewConfig)
	for _, cfg := range configs {
		configMap[cfg.RuleID] = cfg
	}

	var mergedRules []model.ReviewRule
	for _, rule := range allRules {
		if cfg, hasConfig := configMap[rule.ID]; hasConfig {
			if !cfg.IsEnabled {
				continue
			}
			if cfg.Severity != "" {
				rule.Severity = cfg.Severity
			}
		} else {
			if !rule.IsEnabled {
				continue
			}
		}
		mergedRules = append(mergedRules, rule)
	}

	// 6. 规则截断
	maxRules := 5
	if projectTemplate.ID > 0 && projectTemplate.MaxRulesPerReview > 0 {
		maxRules = projectTemplate.MaxRulesPerReview
	}
	selectedRules, truncatedRules := engine.SelectTopRules(mergedRules, maxRules)

	// 7. 解析维度权重
	dimWeights := engine.BuildDimensionWeights(projectTemplate.DimensionWeights, selectedRules)

	// 8. 解析扣分配置
	deductCfg := engine.DefaultDeductScoreConfig()
	if projectTemplate.ID > 0 && projectTemplate.DeductScoreConfig != "" {
		if dc, err := engine.ParseDeductScoreConfig(projectTemplate.DeductScoreConfig); err == nil {
			deductCfg = dc
		}
	}

	// 9. 组合自定义指令 + 人工复核意见
	customInstruction := ""
	if projectTemplate.ID > 0 {
		customInstruction = projectTemplate.CustomInstruction
	}
	if commentOverride != "" {
		if customInstruction != "" {
			customInstruction += "\n\n"
		}
		customInstruction += "### 【人工复核意见】（请重点参考以下意见进行审查）\n" + commentOverride
	}

	// 10. 准备 diff file maps（保留完整 diff 供大模型评审，仅在持久化到 diff_files_json 时截断）
	var fileMaps []map[string]interface{}
	for _, f := range diffFiles {
		fileMaps = append(fileMaps, map[string]interface{}{
			"path":      f.NewPath,
			"old_path":  f.OldPath,
			"diff":      f.Diff,
			"additions": f.Additions,
			"deletions": f.Deletions,
		})
	}
	// 原始未过滤 diff（供 dependency_scan 读取 go.mod / package.json 等依赖清单）
	var fileMapsRaw []map[string]interface{}
	for _, f := range diffFilesRaw {
		fileMapsRaw = append(fileMapsRaw, map[string]interface{}{
			"path":      f.NewPath,
			"old_path":  f.OldPath,
			"diff":      f.Diff,
			"additions": f.Additions,
			"deletions": f.Deletions,
		})
	}

	// 11. AST 上下文提取
	lang := detectLanguage(fileMaps)
	astCtx := pipeline.NewASTExtractor(lang).Extract(fileMaps).Format()
	if astCtx != "" {
		zap.L().Info("Pipeline: AST 上下文提取完成", zap.String("language", lang), zap.Int("chars", len(astCtx)))
	}

	// 12. 组装 PromptContext
	promptCtx := &engine.PromptContext{
		Files:                 diffFiles,
		CommitsText:           commitsText,
		MRTitle:               task.MRTitle,
		CustomInstruction:     customInstruction,
		DimensionWeights:      dimWeights,
		DeductScoreConfig:     deductCfg,
		Rules:                 selectedRules,
		MaxRules:              maxRules,
		ASTContext:            astCtx,
		GitLabCommentTemplate: projectTemplate.GitLabCommentTemplate,
	}
	if sysCfg.DefaultGitLabCommentTemplate != "" && promptCtx.GitLabCommentTemplate == "" {
		promptCtx.GitLabCommentTemplate = sysCfg.DefaultGitLabCommentTemplate
	}

	// 13. 读取全局智能体配置并注入 PromptContext
	var agentCfg model.ReviewAgentConfig
	if err := model.DB.First(&agentCfg, 1).Error; err == nil {
		promptCtx.ShowAgentStatus = agentCfg.ShowAgentStatus
		promptCtx.AgentStatus = buildAgentExecutionStatus(agentCfg)
	}

	// 14. 注入 Pipeline Context
	inputs := map[string]interface{}{
		"diff_files":          fileMaps,    // 过滤后的 diff，供 review/secret_scan/impact_analysis 等共享
		"diff_files_raw":      fileMapsRaw, // 原始未过滤 diff，供 dependency_scan 读取 go.mod/package.json 等
		"commits_text":        commitsText,
		"project_template":    projectTemplate.Prompt,
		"prompt_context":      promptCtx,
		"selected_rules":      selectedRules,
		"truncated_rules":     truncatedRules,
		"dimension_weights":   dimWeights,
		"deduct_score_config": deductCfg,
		"ast_context":         astCtx,
	}

	// 注册 Pipeline 取消信号（支持手动停止任务时立即中断）
	cancelCh := registerPipelineCancel(task.ID)
	defer unregisterPipelineCancel(task.ID)
	inputs["_cancel_ch"] = cancelCh

	// 【P0】将原始 diff 存储到对象存储并写入 mr_diff_meta 索引（异步，不阻塞 Pipeline）
	rawDiffForStore := rebuildRawDiff(diffFilesRaw)
	if rawDiffForStore != "" {
		go func(t model.Task, rd string) {
			storage := GetObjectStorageProvider()
			ds := pipeline.NewDiffStore(pipeline.DefaultDiffStoreConfig, storage)
			parser := diff.NewParser()
			parsedFiles, _ := parser.Parse(rd)
			_, err := ds.StoreDiff(context.Background(), &t, rd, parsedFiles)
			if err != nil {
				zap.L().Warn("StoreDiff failed", zap.Uint("task_id", t.ID), zap.Error(err))
			}
		}(task, rawDiffForStore)
	}

	// 15. 执行 Pipeline 引擎
	eng := pipeline.NewEngine(model.DB, &pipelineLLMAdapter{svc: NewLLMService()})
	broadcaster := func(taskID uint, status string, data map[string]interface{}) {
		if s.TaskPipelineHub == nil {
			return
		}
		b, _ := json.Marshal(data)
		s.TaskPipelineHub.Broadcast(int64(taskID), string(b))
	}
	if err := eng.ExecuteTask(task.ID, inputs, broadcaster); err != nil {
		return s.failReviewTask(task, err.Error())
	}

	// 16. Pipeline 成功后执行后处理
	var updatedTask model.Task
	if err := model.DB.Preload("Project").First(&updatedTask, task.ID).Error; err != nil {
		return err
	}

	// 生成并保存 diff 文件元信息
	meta := prepareDiffFilesMeta(diffFiles, truncationThreshold)
	diffFilesJSONBytes, _ := json.Marshal(meta)
	model.DB.Model(&updatedTask).Update("diff_files_json", string(diffFilesJSONBytes))

	// 保存 ReviewLog
	if err := saveReviewLogFromTask(updatedTask, additions, deletions, commits, updatedTask.ScoreValue); err != nil {
		zap.L().Error("Pipeline: 保存 ReviewLog 失败", zap.Error(err))
	}

	// 发布评论（Markdown 报告应该在 Pipeline PostProcess 中生成并保存到 AIResponse）
	if updatedTask.AIResponse != "" {
		go s.postReviewComment(updatedTask, updatedTask.AIResponse)
	}

	// 阈值检查
	if updatedTask.ScoreValue > 0 {
		s.checkThresholdAndTrigger(updatedTask, updatedTask.ScoreValue)
	}

	// 通知（即时企微 + 延迟队列）
	go func() {
		var notifyTask model.Task
		if err := model.DB.Preload("Project").First(&notifyTask, task.ID).Error; err != nil {
			return
		}
		stats := CalcIssueStats(notifyTask.ID, notifyTask.MRMergeID)
		NewNotifierService().NotifyAIReviewCompleted(notifyTask)
		GetDelayedNotificationQueue().Enqueue(notifyTask, stats)
	}()

	// 唤醒同项目 pending 任务
	s.startNextPendingTask(updatedTask.ProjectID)
	return nil
}

func (s *TaskService) runAIReview(taskID *uint, caller string, diffFiles []gitlab.DiffFile, commitsText, mrTitle, projectTemplate string, modelID uint) (string, int, string, uint, string, error) {
	var reviewReport string
	var userPrompt string
	var actualModelID uint
	var actualModelName string

	if caller == "" {
		caller = "runAIReview"
	}

	if isSingleBatch(diffFiles) {
		userPrompt = buildSingleBatchPrompt(diffFiles, commitsText, mrTitle, projectTemplate)
		result, err := NewLLMService().ChatCompletion(context.Background(), taskID, modelID, caller, "", userPrompt)
		if err != nil {
			return "", 0, "", 0, "", fmt.Errorf("LLM 调用失败: %w", err)
		}
		reviewReport = result.Content
		actualModelID = result.ModelID
		actualModelName = result.ModelName
	} else {
		batchReviews := []string{}
		batches := splitIntoBatches(diffFiles, SysCfgMaxTokensPerBatch())

		for i, batch := range batches {
			batchPrompt := buildBatchPrompt(batch, i+1, len(batches), commitsText, mrTitle, projectTemplate)
			result, err := NewLLMService().ChatCompletion(context.Background(), taskID, modelID, caller, "", batchPrompt)
			if err != nil {
				return "", 0, "", 0, "", fmt.Errorf("批次 %d/%d 评审失败: %w", i+1, len(batches), err)
			}
			batchReviews = append(batchReviews, result.Content)
			// 记录第一个成功批次使用的模型（后续批次应该一致，但这里简单取第一个）
			if i == 0 {
				actualModelID = result.ModelID
				actualModelName = result.ModelName
			}
			zap.L().Info("batch review completed", zap.Int("batch", i+1), zap.Int("total", len(batches)))
		}

		userPrompt = buildSummaryPrompt(batchReviews, commitsText, mrTitle, projectTemplate)
		result, err := NewLLMService().ChatCompletion(context.Background(), taskID, modelID, caller, "", userPrompt)
		if err != nil {
			return "", 0, "", 0, "", fmt.Errorf("汇总评审失败: %w", err)
		}
		reviewReport = result.Content
		actualModelID = result.ModelID
		actualModelName = result.ModelName
	}

	score, _ := extractScoreFromReport(reviewReport)
	zap.L().Info("runAIReview 完成",
		zap.Uint("actualModelID", actualModelID),
		zap.String("actualModelName", actualModelName),
		zap.Int("score", score))
	return reviewReport, score, userPrompt, actualModelID, actualModelName, nil
}

func buildSingleBatchPrompt(files []gitlab.DiffFile, commitsText, mrTitle, projectTemplate string) string {
	var sb strings.Builder
	if projectTemplate != "" {
		sb.WriteString(projectTemplate)
		sb.WriteString("\n\n")
	}
	sb.WriteString("### **重点要求（必须遵守）**\n")
	sb.WriteString("根据审查的结果，为本次评审计算一个分数，分数范围为0-100，格式严格为：AI评分：xx分（例如：AI评分：90分）\n\n")
	sb.WriteString("## 待评审内容\n\n")
	for i, f := range files {
		sb.WriteString(fmt.Sprintf("%d、文件：%s\n", i+1, f.NewPath))
		sb.WriteString("```diff\n")
		sb.WriteString(f.Diff)
		sb.WriteString("\n```\n\n")
	}
	sb.WriteString(fmt.Sprintf("\ncommits：\n%s\n", commitsText))
	sb.WriteString(fmt.Sprintf("\nMR名称：%s", mrTitle))
	return sb.String()
}

func buildBatchPrompt(files []gitlab.DiffFile, batchIndex, totalBatches int, commitsText, mrTitle, projectTemplate string) string {
	var sb strings.Builder
	if projectTemplate != "" {
		sb.WriteString(projectTemplate)
		sb.WriteString("\n\n")
	}
	sb.WriteString(fmt.Sprintf("## 待评审内容（第 %d/%d 批）\n\n", batchIndex, totalBatches))
	for i, f := range files {
		sb.WriteString(fmt.Sprintf("%d、文件：%s\n", i+1, f.NewPath))
		sb.WriteString("```diff\n")
		sb.WriteString(f.Diff)
		sb.WriteString("\n```\n\n")
	}
	sb.WriteString("\n请对以上文件进行代码审查，输出评审意见。本次不需要输出分数。")
	sb.WriteString(fmt.Sprintf("\n\ncommits：\n%s", commitsText))
	sb.WriteString(fmt.Sprintf("\nMR名称：%s", mrTitle))
	return sb.String()
}

func buildSummaryPrompt(batchReviews []string, commitsText, mrTitle, projectTemplate string) string {
	var sb strings.Builder
	if projectTemplate != "" {
		sb.WriteString(projectTemplate)
		sb.WriteString("\n\n")
	}
	sb.WriteString("### **重点要求（必须遵守）**\n")
	sb.WriteString("根据审查的结果，为本次评审计算一个分数，分数范围为0-100，格式严格为：AI评分：xx分（例如：AI评分：90分）\n\n")
	sb.WriteString("## 综合评审请求\n\n")
	sb.WriteString("以下是对各文件代码变更的评审意见：\n\n")
	for i, review := range batchReviews {
		sb.WriteString(fmt.Sprintf("【批次 %d】\n%s\n\n", i+1, review))
	}
	sb.WriteString("请基于以上各文件评审结果，生成本 MR 的综合评审报告，包含：\n")
	sb.WriteString("1. 代码质量评估\n")
	sb.WriteString("2. 潜在问题\n")
	sb.WriteString("3. 改进建议\n")
	sb.WriteString(fmt.Sprintf("\n\ncommits：\n%s", commitsText))
	sb.WriteString(fmt.Sprintf("\nMR名称：%s", mrTitle))
	return sb.String()
}

// truncateDiffInPrompt 截断 prompt 中超过阈值的 diff 代码块
// threshold 为 UTF-8 字符数，每个文件块的 diff 独立判断
func truncateDiffInPrompt(prompt string, threshold int) string {
	if threshold <= 0 {
		threshold = 5000 // 默认兜底
	}
	re := regexp.MustCompile("(?s)```diff\\n(.*?)\\n```")
	return re.ReplaceAllStringFunc(prompt, func(block string) string {
		if utf8.RuneCountInString(block) > threshold {
			return "```diff\n变更内容过大，请在代码库中查看\n```"
		}
		return block
	})
}

func (s *TaskService) postReviewComment(task model.Task, comment string) {
	if err := postMRComment(task.Project.ProjectPath, task.Project.AccessToken,
		task.MRMergeID, comment, 0); err != nil {
		zap.L().Error("发布 MR 评论失败", zap.Error(err))
	} else {
		zap.L().Info("MR 评论发布成功", zap.Uint("task_id", task.ID))
	}
}

func (s *TaskService) checkThresholdAndTrigger(task model.Task, score int) {
	var sysCfg model.SystemConfig
	if err := model.DB.First(&sysCfg).Error; err != nil || sysCfg.ScoreThreshold <= 0 {
		return
	}
	if !sysCfg.DeepReviewEnabled {
		zap.L().Info("深度代码审查已禁用，跳过阈值触发", zap.Uint("task_id", task.ID), zap.Int("score", score))
		return
	}
	if score >= sysCfg.ScoreThreshold {
		return
	}
	zap.L().Info("分数低于阈值，触发深度审查",
		zap.Int("score", score),
		zap.Int("threshold", sysCfg.ScoreThreshold))
	go s.triggerDeepReview(task, sysCfg.ScoreThreshold)
}

// triggerDeepReview 触发深度审查（走 OpenCode + ReviewTemplate）
func (s *TaskService) triggerDeepReview(reviewTask model.Task, threshold int) {
	mrDiff := ""
	if reviewTask.MRMergeID > 0 && reviewTask.Project.ProjectPath != "" {
		mrDiff = fetchMRDiff(reviewTask.Project.ProjectPath, reviewTask.Project.AccessToken, reviewTask.MRMergeID)
	}

	var sysCfg model.SystemConfig
	if err := model.DB.First(&sysCfg).Error; err != nil {
		zap.L().Error("读取系统配置失败", zap.Error(err))
		return
	}

	template := sysCfg.ReviewTemplate
	if template == "" {
		template = sysCfg.AILogTemplate
		zap.L().Warn("review template empty, fallback to ai_log_template")
	}
	if template == "" {
		template = "请先执行以下命令拉取代码：\ngit clone {{CLONE_URL}}\n\n{{USER_INPUT}}\n\n请审查以上代码变更，给出审查意见。"
	}

	cloneURL := reviewTask.Project.ProjectPath
	if reviewTask.Project.AccessToken != "" {
		if !strings.Contains(cloneURL, "://") {
			cloneURL = "https://" + cloneURL
		}
		if !strings.Contains(cloneURL, "@") {
			parts := strings.SplitN(cloneURL, "://", 2)
			cloneURL = parts[0] + "://oauth2:" + reviewTask.Project.AccessToken + "@" + parts[1]
		}
	}

	aiPrompt := strings.ReplaceAll(template, "{{CLONE_URL}}", cloneURL)
	aiPrompt = strings.ReplaceAll(aiPrompt, "{{USER_INPUT}}", "请对这段代码进行深度审查和分析")
	aiPrompt = strings.ReplaceAll(aiPrompt, "{{MR_DIFF}}", mrDiff)
	aiPrompt = strings.ReplaceAll(aiPrompt, "{{MR_AUTHOR}}", reviewTask.MRAuthor)
	aiPrompt = strings.ReplaceAll(aiPrompt, "{{SRC_BRANCH}}", reviewTask.SourceBranch)
	aiPrompt = strings.ReplaceAll(aiPrompt, "{{DEST_BRANCH}}", reviewTask.TargetBranch)
	aiPrompt = strings.ReplaceAll(aiPrompt, "{{AI_BRANCH}}", "AI-"+generateRandomString(4))

	taskData := map[string]interface{}{
		"project_id":     float64(reviewTask.ProjectID),
		"pool_id":        float64(reviewTask.Project.PoolID),
		"mr_iid":         float64(reviewTask.MRMergeID),
		"mr_title":       reviewTask.MRTitle,
		"mr_url":         reviewTask.MRURL,
		"author":         reviewTask.MRAuthor,
		"source_branch":  reviewTask.SourceBranch,
		"target_branch":  reviewTask.TargetBranch,
		"ai_prompt":      aiPrompt,
		"task_type":      "chat",
		"trigger_type":   "auto",
		"trigger_source": "score_threshold",
		"score_value":    float64(reviewTask.ScoreValue),
	}

	deepTask, err := s.Create(taskData)
	if err != nil {
		zap.L().Error("创建深度审查任务失败", zap.Error(err))
		return
	}

	comment := fmt.Sprintf("AI 评审分数为 %d 分，低于阈值 %d 分，已自动触发深度代码审查任务【%d】。",
		reviewTask.ScoreValue, threshold, deepTask.ID)
	go s.postReviewComment(reviewTask, comment)

	go func() {
		if err := s.Execute(deepTask.ID); err != nil {
			zap.L().Error("深度审查任务执行失败", zap.Uint("task_id", deepTask.ID), zap.Error(err))
		}
	}()
}

func extractHostFromPath(projectPath string) string {
	if strings.HasPrefix(projectPath, "http://") {
		return "http://" + strings.SplitN(strings.TrimPrefix(projectPath, "http://"), "/", 2)[0]
	}
	if strings.HasPrefix(projectPath, "https://") {
		return "https://" + strings.SplitN(strings.TrimPrefix(projectPath, "https://"), "/", 2)[0]
	}
	return "https://" + strings.SplitN(projectPath, "/", 2)[0]
}

func formatCommitsForReview(commits []gitlab.CommitInfo) string {
	if len(commits) == 0 {
		return "无"
	}
	var sb strings.Builder
	for _, c := range commits {
		sb.WriteString(fmt.Sprintf("- %s %s\n", c.ShortID, c.Title))
	}
	return sb.String()
}

func isSingleBatch(files []gitlab.DiffFile) bool {
	totalChars := 0
	for _, f := range files {
		totalChars += len(f.Diff)
	}
	return totalChars/charsPerTokenApprox < SysCfgMaxTokensPerBatch()
}

// truncateDiffFilesForStructured 截断 diff 文件以适配单批结构化评审
// 按 diff 长度排序后保留最重要的文件，并截断超长 diff，确保总 token 不超限制
func truncateDiffFilesForStructured(files []gitlab.DiffFile, maxTokens int) []gitlab.DiffFile {
	const promptTemplateTokens = 8000 // prompt 模板（不含 diff）约占 token 数
	availableTokens := maxTokens - promptTemplateTokens
	if availableTokens <= 0 {
		availableTokens = maxTokens / 2
	}
	maxChars := availableTokens * charsPerTokenApprox

	// 复制文件避免修改原始切片
	result := make([]gitlab.DiffFile, len(files))
	copy(result, files)

	// 按 diff 长度降序排序（保留变更量最大的文件）
	sort.Slice(result, func(i, j int) bool {
		return len(result[i].Diff) > len(result[j].Diff)
	})

	totalChars := 0
	keptCount := 0
	for i, f := range result {
		fileChars := len(f.Diff)
		if totalChars+fileChars <= maxChars {
			totalChars += fileChars
			keptCount++
		} else {
			// 剩余额度不足以容纳完整文件时，尝试截断该文件
			remaining := maxChars - totalChars
			if remaining > 200 {
				result[i].Diff = f.Diff[:remaining] + "\n...（diff 已截断，仅保留前段用于结构化评审）"
				totalChars += remaining
				keptCount++
			}
			// 丢弃后续文件
			result = result[:keptCount]
			break
		}
	}

	// 按 NewPath 排序恢复稳定顺序
	sort.Slice(result, func(i, j int) bool {
		return result[i].NewPath < result[j].NewPath
	})

	return result
}

func splitIntoBatches(files []gitlab.DiffFile, maxTokens int) [][]gitlab.DiffFile {
	var batches [][]gitlab.DiffFile
	var currentBatch []gitlab.DiffFile
	currentTokens := 0

	for _, file := range files {
		fileTokens := len(file.Diff) / charsPerTokenApprox
		if fileTokens == 0 {
			fileTokens = 1
		}
		if currentTokens+fileTokens > maxTokens && len(currentBatch) > 0 {
			batches = append(batches, currentBatch)
			currentBatch = []gitlab.DiffFile{file}
			currentTokens = fileTokens
		} else {
			currentBatch = append(currentBatch, file)
			currentTokens += fileTokens
		}
	}
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}
	return batches
}

func extractScoreFromReport(report string) (int, bool) {
	re := regexp.MustCompile(`AI评分\s*[:：]\s*(\d+(?:\.\d+)?)\s*分`)
	matches := re.FindAllStringSubmatch(report, -1)
	if len(matches) == 0 {
		return 0, false
	}
	scoreStr := matches[len(matches)-1][1]
	score, err := strconv.ParseFloat(scoreStr, 64)
	if err != nil {
		return 0, false
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return int(score), true
}

func saveReviewLogFromTask(task model.Task, additions, deletions int, commits []gitlab.CommitInfo, score int) error {
	if task.MRURL == "" {
		return fmt.Errorf("mr_url is empty")
	}

	var log model.MergeRequestReviewLog
	err := model.SilentFirst(model.DB.Where("url = ?", task.MRURL), &log)

	commitsJSON, _ := json.Marshal(commits)
	now := time.Now()

	if err == nil {
		var history []float64
		if log.ScoreHistory != "" {
			json.Unmarshal([]byte(log.ScoreHistory), &history)
		}
		history = append(history, float64(score))
		newHistoryJSON, _ := json.Marshal(history)

		log.Score = float64(score)
		log.ScoreHistory = string(newHistoryJSON)
		log.ReviewCount++
		log.Additions = additions
		log.Deletions = deletions
		log.Commits = string(commitsJSON)
		log.SyncedAt = now
		if log.AuthorDisplayName == "" && task.MRAuthorDisplayName != "" {
			log.AuthorDisplayName = task.MRAuthorDisplayName
		}
		if log.MRTitle == "" {
			log.MRTitle = task.MRTitle
		}
		return model.DB.Save(&log).Error
	}

	log = model.MergeRequestReviewLog{
		URL:               task.MRURL,
		ProjectName:       task.Project.Name,
		Author:            task.MRAuthor,
		AuthorDisplayName: task.MRAuthorDisplayName,
		SourceBranch:      task.SourceBranch,
		TargetBranch:      task.TargetBranch,
		MRID:              task.MRMergeID,
		Score:             float64(score),
		ScoreHistory:      fmt.Sprintf("[%d]", score),
		ReviewCount:       1,
		Additions:         additions,
		Deletions:         deletions,
		MRTitle:           task.MRTitle,
		MRState:           "opened",
		Commits:           string(commitsJSON),
		SyncedAt:          now,
	}
	return model.DB.Create(&log).Error
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ================== 结构化 AI 评审（新增）====================

// runStructuredAIReview 使用结构化输出进行 AI 评审
// 返回：reviewReport(Markdown), score, userPrompt, actualModelID, actualModelName, error
func (s *TaskService) runStructuredAIReview(task model.Task, diffFiles []gitlab.DiffFile, commitsText string, reviewComment string) (string, int, string, uint, string, error) {
	// 1. 加载所有规则，并与项目配置合并（无配置时 fallback 到全局状态）
	var allRules []model.ReviewRule
	if err := model.DB.Find(&allRules).Error; err != nil {
		zap.L().Warn("load all review rules failed", zap.Uint("project_id", task.ProjectID), zap.Error(err))
	}

	var configs []model.ProjectReviewConfig
	if err := model.DB.Where("project_id = ?", task.ProjectID).Find(&configs).Error; err != nil {
		zap.L().Warn("load project review configs failed", zap.Uint("project_id", task.ProjectID), zap.Error(err))
	}
	configMap := make(map[uint]model.ProjectReviewConfig)
	for _, cfg := range configs {
		configMap[cfg.RuleID] = cfg
	}

	var rules []model.ReviewRule
	for _, rule := range allRules {
		if cfg, hasConfig := configMap[rule.ID]; hasConfig {
			// 有项目配置，以配置为准
			if !cfg.IsEnabled {
				continue
			}
			if cfg.Severity != "" {
				rule.Severity = cfg.Severity
			}
		} else {
			// 无项目配置，fallback 到全局启用状态
			if !rule.IsEnabled {
				continue
			}
		}
		rules = append(rules, rule)
	}

	// 1.5 预加载项目及其模板信息（避免 task.Project 空值）
	var project model.Project
	var template model.ProjectTemplate
	if err := model.DB.First(&project, task.ProjectID).Error; err == nil {
		if project.TemplateID > 0 {
			model.DB.First(&template, project.TemplateID)
		}
	}

	// 2. 解析维度权重（使用项目模板配置），自动补齐已启用规则的分类维度
	var dimWeights map[string]engine.DimensionWeight
	if template.ID > 0 {
		dimWeights = engine.BuildDimensionWeights(template.DimensionWeights, rules)
	} else {
		dimWeights = engine.BuildDimensionWeights("", rules)
	}

	// 4. 获取 max_rules_per_review（默认 5）
	maxRules := 5
	if template.ID > 0 && template.MaxRulesPerReview > 0 {
		maxRules = template.MaxRulesPerReview
	}

	// 4.1 规则截断：记录传入和被截断的规则
	selectedRules, truncatedRules := engine.SelectTopRules(rules, maxRules)

	// 5. 解析扣分规则配置
	deductCfg := engine.DefaultDeductScoreConfig()
	if template.ID > 0 && template.DeductScoreConfig != "" {
		if dc, err := engine.ParseDeductScoreConfig(template.DeductScoreConfig); err == nil {
			deductCfg = dc
		}
	}

	// 6. 组装 Prompt - 合并：模板自定义指令 + 人工复核意见
	customInstruction := ""
	if template.ID > 0 {
		customInstruction = template.CustomInstruction
	}
	if reviewComment != "" {
		if customInstruction != "" {
			customInstruction += "\n\n"
		}
		customInstruction += "### 【人工复核意见】（请重点参考以下意见进行审查）\n" + reviewComment
	}

	promptCtx := &engine.PromptContext{
		Files:             diffFiles,
		CommitsText:       commitsText,
		MRTitle:           task.MRTitle,
		CustomInstruction: customInstruction,
		DimensionWeights:  dimWeights,
		DeductScoreConfig: deductCfg,
		Rules:             selectedRules,
		MaxRules:          maxRules,
	}
	userPrompt := engine.BuildReviewPrompt(promptCtx)

	// 6. 构建 JSON Schema Request，使用模板实际维度
	dimNames := make([]string, 0, len(dimWeights))
	for code := range dimWeights {
		dimNames = append(dimNames, code)
	}
	schema := llm.GetReviewJSONSchema(dimNames)
	responseFormat := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "code_review_result",
			Strict: true,
			Schema: schema,
		},
	}

	// 7. 调用 LLM（带结构化输出）
	var actualModelID uint
	var actualModelName string
	var rawContent string
	var llmResponse *llm.ChatResponse

	llmService := NewLLMService()

	if isSingleBatch(diffFiles) {
		result, err := llmService.ChatCompletionStructured(context.Background(), &task.ID, 0, "runAIReviewStructured", "", userPrompt, responseFormat)
		if err != nil {
			return "", 0, userPrompt, 0, "", err
		}
		rawContent = result.Content
		actualModelID = result.ModelID
		actualModelName = result.ModelName
		llmResponse = result.Response
	} else {
		// 多批时：截断 diff 后强制走单批结构化，不再 fallback 到旧模式
		maxTokens := SysCfgMaxTokensPerBatch()
		truncatedFiles := truncateDiffFilesForStructured(diffFiles, maxTokens)
		zap.L().Info("结构化评审多批截断",
			zap.Int("original_files", len(diffFiles)),
			zap.Int("truncated_files", len(truncatedFiles)),
			zap.Uint("task_id", task.ID))
		// 更新 promptCtx 并重建 userPrompt
		promptCtx.Files = truncatedFiles
		userPrompt = engine.BuildReviewPrompt(promptCtx)
		result, err := llmService.ChatCompletionStructured(context.Background(), &task.ID, 0, "runAIReviewStructuredTruncated", "", userPrompt, responseFormat)
		if err != nil {
			return "", 0, userPrompt, 0, "", err
		}
		rawContent = result.Content
		actualModelID = result.ModelID
		actualModelName = result.ModelName
		llmResponse = result.Response
	}

	// 8. Refusal 检测
	if llmResponse != nil && len(llmResponse.Choices) > 0 && llmResponse.Choices[0].Message.Refusal != "" {
		return "", 0, userPrompt, actualModelID, actualModelName,
			fmt.Errorf("模型拒绝回答: %s", llmResponse.Choices[0].Message.Refusal)
	}

	// 9. 解析响应（含重试和 fallback）
	var sysCfg model.SystemConfig
	model.DB.First(&sysCfg)

	retryCfg := &engine.RetryConfig{
		MaxAttempts:       sysCfg.JSONRetryMaxAttempts,
		InitialDelay:      time.Duration(sysCfg.JSONRetryInitialDelaySec) * time.Second,
		BackoffMultiplier: sysCfg.JSONRetryBackoffMultiplier,
		MaxDelay:          time.Duration(sysCfg.JSONRetryMaxDelaySec) * time.Second,
		FallbackStrategy:  sysCfg.JSONRetryFallbackStrategy,
	}
	if retryCfg.MaxAttempts <= 0 {
		retryCfg.MaxAttempts = 3
		retryCfg.InitialDelay = 2 * time.Second
		retryCfg.BackoffMultiplier = 2.0
		retryCfg.MaxDelay = 30 * time.Second
		retryCfg.FallbackStrategy = "regex"
	}

	// 重试回调：重新调用 LLM
	retryCall := func() (string, error) {
		result, err := llmService.ChatCompletionStructured(context.Background(), &task.ID, 0, "retry", "", userPrompt, responseFormat)
		if err != nil {
			return "", err
		}
		return result.Content, nil
	}

	parsedResult, err := engine.ParseReviewResult(rawContent, retryCall, retryCfg, deductCfg)
	if err != nil {
		return "", 0, userPrompt, actualModelID, actualModelName,
			fmt.Errorf("结构化输出解析失败: %w", err)
	}

	// 【P0】行号校正：在入库前校正 issues 的行号
	rawDiff := rebuildRawDiff(diffFiles)
	if rawDiff != "" {
		cfg := config.Load()
		parsedResult.Issues = engine.ApplyLineCorrections(parsedResult.Issues, rawDiff, &cfg.DiffLineMap)
	}

	// 10. 持久化结构化数据
	if err := engine.PersistStructuredReview(task.ID, parsedResult); err != nil {
		zap.L().Warn("persist structured review failed", zap.Error(err))
	}

	// 10.5 持久化本次任务使用的规则（传入 + 截断）
	if err := persistTaskReviewRules(task.ID, selectedRules, truncatedRules); err != nil {
		zap.L().Warn("persist task review rules failed", zap.Uint("task_id", task.ID), zap.Error(err))
	}

	// 11. 组装 Markdown 评论
	commentTemplate := ""
	if template.ID > 0 && template.GitLabCommentTemplate != "" {
		commentTemplate = template.GitLabCommentTemplate
	} else if sysCfg.DefaultGitLabCommentTemplate != "" {
		commentTemplate = sysCfg.DefaultGitLabCommentTemplate
	}

	reviewReport, err := engine.AssembleMarkdownComment(parsedResult, commentTemplate)
	if err != nil {
		// 组装失败，fallback 到原始 JSON 的简易 Markdown
		reviewReport = fmt.Sprintf("## 🤖 AI 代码评审报告\n\n**综合评分：%d/100**\n\n%s",
			parsedResult.TotalScore, parsedResult.Summary)
	}

	zap.L().Info("runStructuredAIReview 完成",
		zap.Uint("actualModelID", actualModelID),
		zap.String("actualModelName", actualModelName),
		zap.Int("score", parsedResult.TotalScore),
		zap.Int("issue_count", len(parsedResult.Issues)))

	return reviewReport, parsedResult.TotalScore, userPrompt, actualModelID, actualModelName, nil
}

// runAIReviewFallback 分批评审 fallback（使用旧模式）
// TODO: 后续实现分批结构化评审
func (s *TaskService) runAIReviewFallback(taskID *uint, diffFiles []gitlab.DiffFile, commitsText, mrTitle, userPrompt string) (string, int, string, uint, string, error) {
	// 简单处理：对第一批做结构化评审，其余忽略
	// 或直接用旧方法
	return s.runAIReview(taskID, "runAIReviewFallback", diffFiles, commitsText, mrTitle, userPrompt, 0)
}

// persistTaskReviewRules 持久化任务实际使用的规则（含被截断记录）
func persistTaskReviewRules(taskID uint, selected []model.ReviewRule, truncated []model.ReviewRule) error {
	return engine.PersistTaskReviewRules(taskID, selected, truncated)
}

// prepareDiffFilesMeta 将 diff 文件列表处理为前端展示所需格式
// 包含文件名、diff 内容和截断标记
func prepareDiffFilesMeta(diffFiles []gitlab.DiffFile, threshold int) []map[string]interface{} {
	const maxDiffChars = 50000 // 单个文件最大存储字符数（约50KB）
	var result []map[string]interface{}
	for _, f := range diffFiles {
		truncated := false
		preview := ""
		diffContent := f.Diff
		block := fmt.Sprintf("```diff\n%s\n```", f.Diff)
		if utf8.RuneCountInString(block) > threshold {
			truncated = true
			lines := strings.Split(f.Diff, "\n")
			if len(lines) > 20 {
				preview = strings.Join(lines[:20], "\n")
				diffContent = preview + "\n... （变更内容过大已截断，请在代码库中查看）\n"
			}
		}
		// 限制未截断文件的存储大小
		if !truncated && len(diffContent) > maxDiffChars {
			truncated = true
			diffContent = diffContent[:maxDiffChars] + "\n... （diff 内容超过存储上限，请在代码库中查看）\n"
		}
		result = append(result, map[string]interface{}{
			"new_path":  f.NewPath,
			"additions": f.Additions,
			"deletions": f.Deletions,
			"truncated": truncated,
			"diff":      diffContent,
		})
	}
	return result
}

// rebuildRawDiff 从 GitLab DiffFile 列表重建完整的 unified diff 文本
func rebuildRawDiff(files []gitlab.DiffFile) string {
	var parts []string
	for _, f := range files {
		if f.Diff != "" {
			parts = append(parts, f.Diff)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// SaveChatTaskDiffFiles 在 chat 任务成功完成后，异步获取并保存 diff 元信息
func (s *TaskService) SaveChatTaskDiffFiles(taskID uint) {
	go func() {
		var task model.Task
		if err := model.DB.Preload("Project").First(&task, taskID).Error; err != nil {
			zap.L().Warn("save chat diff files: task not found", zap.Uint("task_id", taskID))
			return
		}
		if task.MRMergeID == 0 || task.Project.ID == 0 {
			return
		}
		diffFiles, _, _, err := s.fetchMRDiffFiles(task)
		if err != nil {
			zap.L().Warn("save chat diff files: fetch failed", zap.Uint("task_id", taskID), zap.Error(err))
			return
		}
		var sysCfg model.SystemConfig
		threshold := 5000
		if err := model.DB.First(&sysCfg).Error; err == nil && sysCfg.DiffTruncationThreshold > 0 {
			threshold = sysCfg.DiffTruncationThreshold
		}
		if maxDF := SysCfgMaxDiffFiles(); len(diffFiles) > maxDF {
			diffFiles = diffFiles[:maxDF]
		}
		meta := prepareDiffFilesMeta(diffFiles, threshold)
		b, _ := json.Marshal(meta)
		model.DB.Model(&model.Task{}).Where("id = ?", taskID).Update("diff_files_json", string(b))
		zap.L().Info("chat task diff files saved", zap.Uint("task_id", taskID), zap.Int("files", len(meta)))
	}()
}

// GetTaskDiffFiles 获取任务的 diff 文件列表（优先从缓存读取，无缓存时实时获取）
func (s *TaskService) GetTaskDiffFiles(task model.Task) ([]map[string]interface{}, error) {
	// 优先从缓存读取
	if task.DiffFilesJSON != "" && task.DiffFilesJSON != "[]" && task.DiffFilesJSON != "{}" {
		var cached []map[string]interface{}
		if err := json.Unmarshal([]byte(task.DiffFilesJSON), &cached); err == nil && len(cached) > 0 {
			return cached, nil
		}
	}

	// 兜底：实时获取
	if task.MRMergeID == 0 || task.Project.ID == 0 {
		return nil, nil
	}
	diffFiles, _, _, err := s.fetchMRDiffFiles(task)
	if err != nil {
		return nil, err
	}
	var sysCfg model.SystemConfig
	threshold := 5000
	if err := model.DB.First(&sysCfg).Error; err == nil && sysCfg.DiffTruncationThreshold > 0 {
		threshold = sysCfg.DiffTruncationThreshold
	}
	if maxDF := SysCfgMaxDiffFiles(); len(diffFiles) > maxDF {
		diffFiles = diffFiles[:maxDF]
	}
	return prepareDiffFilesMeta(diffFiles, threshold), nil
}

// ListTaskReviewRules 查询任务实际使用的评审规则（含截断记录）
func (s *TaskService) ListTaskReviewRules(taskID uint) (selected []model.TaskReviewRule, truncated []model.TaskReviewRule, total int, err error) {
	var rules []model.TaskReviewRule
	if err := model.DB.Where("task_id = ?", taskID).Order("sort_order ASC").Find(&rules).Error; err != nil {
		return nil, nil, 0, err
	}
	for _, r := range rules {
		if r.WasSelected {
			selected = append(selected, r)
		} else {
			truncated = append(truncated, r)
		}
	}
	return selected, truncated, len(rules), nil
}

// FetchMRDiffFilesForPipeline Pipeline 专用：获取 MR diff 文件（公开接口）
func (s *TaskService) FetchMRDiffFilesForPipeline(task model.Task) ([]gitlab.DiffFile, int, int, error) {
	return s.fetchMRDiffFiles(task)
}

// FetchMRCommitsForPipeline Pipeline 专用：获取 MR commits（公开接口）
func (s *TaskService) FetchMRCommitsForPipeline(task model.Task) []gitlab.CommitInfo {
	return s.fetchMRCommits(task)
}

// FormatCommitsForPipeline Pipeline 专用：格式化 commits 文本（公开接口）
func (s *TaskService) FormatCommitsForPipeline(commits []gitlab.CommitInfo) string {
	return formatCommitsForReview(commits)
}

// pipelineLLMAdapter 将 service 层的 LLM 调用适配到 pipeline 接口
type pipelineLLMAdapter struct {
	svc *LLMService
}

func (a *pipelineLLMAdapter) ChatCompletion(ctx context.Context, taskID *uint, modelID uint, caller, customInstruction, userPrompt string) (*pipeline.ChatResult, error) {
	r, err := a.svc.ChatCompletion(ctx, taskID, modelID, caller, customInstruction, userPrompt)
	if err != nil {
		return nil, err
	}
	return &pipeline.ChatResult{
		Content:      r.Content,
		ModelName:    r.ModelName,
		ModelID:      r.ModelID,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
	}, nil
}

func (a *pipelineLLMAdapter) ChatCompletionStructured(ctx context.Context, taskID *uint, modelID uint, caller, systemPrompt, userPrompt string, responseFormat *llm.ResponseFormat) (*pipeline.StructuredChatResult, error) {
	r, err := a.svc.ChatCompletionStructured(ctx, taskID, modelID, caller, systemPrompt, userPrompt, responseFormat)
	if err != nil {
		return nil, err
	}
	return &pipeline.StructuredChatResult{
		Content:      r.Content,
		ModelName:    r.ModelName,
		ModelID:      r.ModelID,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		Response:     r.Response,
	}, nil
}

// detectLanguage 根据 diff 文件扩展名投票检测项目主语言
func detectLanguage(files []map[string]interface{}) string {
	counts := map[string]int{
		"golang":     0,
		"java":       0,
		"python":     0,
		"javascript": 0,
	}
	for _, f := range files {
		path, _ := f["path"].(string)
		if strings.HasSuffix(path, ".go") {
			counts["golang"]++
		} else if strings.HasSuffix(path, ".java") {
			counts["java"]++
		} else if strings.HasSuffix(path, ".py") {
			counts["python"]++
		} else if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".jsx") || strings.HasSuffix(path, ".tsx") {
			counts["javascript"]++
		}
	}
	maxCount, maxLang := 0, "golang"
	for lang, cnt := range counts {
		if cnt > maxCount {
			maxCount = cnt
			maxLang = lang
		}
	}
	return maxLang
}

// buildAgentExecutionStatus 根据全局智能体配置构建执行状态（用于报告注入）
func buildAgentExecutionStatus(agentCfg model.ReviewAgentConfig) *engine.AgentExecutionStatus {
	status := &engine.AgentExecutionStatus{}

	// 固定顺序遍历（按 Pipeline 执行顺序），避免 map range 随机性
	stageOrder := []struct {
		Code string
		Name string
	}{
		{"trigger_check", "触发过滤器"},
		{"git_clone", "代码克隆"},
		{"code_understanding", "代码理解器"},
		{"dependency_scan", "漏洞扫描"},
		{"secret_scan", "密钥扫描智能体"},
		{"security_audit", "安全审计智能体"},
		{"test_suggestion", "测试建议智能体"},
		{"impact_analysis", "影响分析智能体"},
		{"batch_review_frame", "AI代码评审智能体"},
		{"post_process", "报告发布"},
	}

	enabled := agentCfg.EnabledStageCodes()
	enabledMap := make(map[string]bool)
	for _, code := range enabled {
		enabledMap[code] = true
	}

	for _, s := range stageOrder {
		if enabledMap[s.Code] {
			status.Enabled = append(status.Enabled, s.Name)
		} else {
			status.Skipped = append(status.Skipped, s.Name)
		}
	}

	// 影响说明（根据 skipped 的组合生成，仅说明未启用智能体的影响）
	if !enabledMap["code_understanding"] {
		status.ImpactNotes = append(status.ImpactNotes,
			"本次评审缺少 AST 上下文和知识图谱分析，AI 对函数/结构体/数据流的理解可能受限")
	}
	if !enabledMap["dependency_scan"] {
		status.ImpactNotes = append(status.ImpactNotes,
			"本次评审未执行依赖漏洞扫描，建议对涉及依赖变更的 MR 启用该智能体")
	}
	if !enabledMap["impact_analysis"] {
		status.ImpactNotes = append(status.ImpactNotes,
			"本次评审未执行跨模块影响分析，可能存在未识别的 Breaking Change")
	} else if !enabledMap["code_understanding"] {
		// 影响分析已启用但缺少代码理解器支撑
		status.ImpactNotes = append(status.ImpactNotes,
			"影响分析智能体已启用但代码理解器未启用，跨文件影响范围可能不完整")
	}

	return status
}
