package service

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-optimizer/backend/config"
	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/encrypt"
	"github.com/ai-optimizer/backend/pkg/gitlab"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type TaskService struct {
	cfg *config.Config
}

func NewTaskService() *TaskService {
	return &TaskService{
		cfg: config.Load(),
	}
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
	// 按用户角色过滤：admin不过滤，user只能看自己的
	query = model.FilterByUser(query, user, "mr_author")

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
		AIResponseJSON:      "{}",  // MySQL JSON 列不接受空字符串
		DimensionScores:     "{}",  // MySQL JSON 列不接受空字符串
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
	// 从数据库获取超时配置
	var sysConfig model.SystemConfig
	timeoutMin := 120
	if err := model.SilentFirst(model.DB, &sysConfig); err == nil && sysConfig.TaskTimeoutMin > 0 {
		timeoutMin = sysConfig.TaskTimeoutMin
	} else if err != nil {
		zap.L().Debug("timeout check using default timeout", zap.Int("timeout_min", timeoutMin), zap.Error(err))
	}

	var tasks []model.Task
	thresholdTime := time.Now().Add(-time.Duration(timeoutMin) * time.Minute)
	if err := model.DB.Where("status = ? AND started_at < ?",
		model.TaskRunning,
		thresholdTime).Find(&tasks).Error; err != nil {
		zap.L().Error("timeout check query failed", zap.Error(err))
		return
	}

	for _, task := range tasks {
		zap.L().Info("task timeout, aborting", zap.Uint("task_id", task.ID), zap.Duration("elapsed", time.Since(*task.StartedAt)))

		// 终止 OpenCode session（如存在）
		if task.OpencodeSessionID != "" {
			opencodeSvc := NewOpencodeService()
			if err := opencodeSvc.AbortTask(task.PoolID, task.OpencodeSessionID); err != nil {
				zap.L().Error("abort timeout task session failed", zap.Uint("task_id", task.ID), zap.Error(err))
			}
		}

		// 条件更新为 timeout，避免覆盖已被修改的状态
		now := time.Now()
		updates := map[string]interface{}{
			"status":       model.TaskTimeout,
			"error_msg":    "任务超时",
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
	// maxDiffFiles / maxTokensPerBatch 已迁入 SystemConfig 并通过 service.SysCfgMaxDiffFiles /
	// SysCfgMaxTokensPerBatch 读取（缓存 5min，UpdateConfig 后主动 RefreshSysCfgCache 立即生效）。
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

	// ExecuteAIReviewTask uses Omit("pool_id") because AI review tasks don't have a pool
	// and pool_id=0 violates the fk_tasks_pool foreign key constraint
	startedAt := time.Now()
	task.Status = model.TaskRunning
	task.StartedAt = &startedAt
	model.DB.Omit("pool_id").Save(&task)

	zap.L().Info("========== AI Review Task started ==========",
		zap.Uint("task_id", task.ID),
		zap.String("project", task.Project.Name),
		zap.Int("mr_iid", task.MRMergeID))

	// 获取 diff 文件
	diffFiles, additions, deletions, err := s.fetchMRDiffFiles(task)
	if err != nil {
		return s.failReviewTask(task, err.Error())
	}

	// 限制最多 N 个文件（SystemConfig.max_diff_files，默认 50）
	maxDiffFiles := SysCfgMaxDiffFiles()
	if len(diffFiles) > maxDiffFiles {
		zap.L().Warn("diff files exceed limit, truncating",
			zap.Int("original", len(diffFiles)),
			zap.Int("limit", maxDiffFiles))
		diffFiles = diffFiles[:maxDiffFiles]
	}

	// 获取 commits
	commits := s.fetchMRCommits(task)
	commitsText := formatCommitsForReview(commits)

	// 注：projectTemplate 已在 runStructuredAIReview 中使用 template 配置替代
	// 保留旧逻辑以兼容其他代码路径
	_ = task.Project.TemplateID

	// 注入人工复核意见
	var reviewCommentText string
	if commentOverride != "" {
		reviewCommentText = commentOverride
	}
	// 结构化评审时，人工复核意见通过 CustomInstruction 传递
	// 这里先保存，后续在 runStructuredAIReview 中使用
	_ = reviewCommentText

	// 执行结构化 AI 评审（传入复核意见）
	reviewReport, score, _, actualModelID, _, err := s.runStructuredAIReview(task, diffFiles, commitsText, reviewCommentText)
	if err != nil {
		return s.failReviewTask(task, err.Error())
	}
	// 生成 diff 文件元信息用于详情页展示
	var sysCfg model.SystemConfig
	truncationThreshold := 5000
	if err := model.DB.First(&sysCfg).Error; err == nil && sysCfg.DiffTruncationThreshold > 0 {
		truncationThreshold = sysCfg.DiffTruncationThreshold
	}
	meta := prepareDiffFilesMeta(diffFiles, truncationThreshold)
	diffFilesJSONBytes, _ := json.Marshal(meta)
	diffFilesJSON := string(diffFilesJSONBytes)

	// 更新 Task（含实际使用的大模型信息）
	// 条件更新：只有状态仍是 running 时才写入成功，防止被 TimeoutCheck 覆盖后回写
	now := time.Now()
	res := model.DB.Model(&model.Task{}).Where("id = ? AND status = ?", task.ID, model.TaskRunning).Updates(map[string]interface{}{
		"status":          model.TaskSuccess,
		"ai_response":     reviewReport,
		"score_value":     score,
		"model_id":        actualModelID,
		"started_at":      startedAt,
		"completed_at":    now,
		"duration_sec":    int(now.Sub(startedAt).Seconds()),
		"diff_files_json": diffFilesJSON,
	})
	if res.Error != nil {
		zap.L().Error("failed to update review task status to success", zap.Uint("task_id", task.ID), zap.Uint("used_model_id", actualModelID), zap.Error(res.Error))
	}
	if res.RowsAffected == 0 {
		zap.L().Info("review task no longer running, skip writing success status", zap.Uint("task_id", task.ID))
	} else {
		zap.L().Info("review 任务保存成功", zap.Uint("task_id", task.ID), zap.Uint("used_model_id", actualModelID))
	}

	// 保存 ReviewLog
	if err := saveReviewLogFromTask(task, additions, deletions, commits, score); err != nil {
		zap.L().Error("保存 ReviewLog 失败", zap.Error(err))
	}

	// 发布评论
	go s.postReviewComment(task, reviewReport)

	// 发送 AI 评审完成通知
	go func() {
		// 重新加载 task 含 Project 关联，用于通知模板渲染
		var notifyTask model.Task
		if err := model.DB.Preload("Project").First(&notifyTask, task.ID).Error; err == nil {
			NewNotifierService().NotifyAIReviewCompleted(notifyTask)
		}
	}()

	// 阈值检查
	if score > 0 {
		// 同步更新内存中的 ScoreValue，避免 triggerDeepReview 读取到旧值
		task.ScoreValue = score
		s.checkThresholdAndTrigger(task, score)
	}

	// review 任务完成后触发队列，唤醒同项目的 pending 任务
	s.startNextPendingTask(task.ProjectID)

	zap.L().Info("========== AI Review Task completed ==========",
		zap.Uint("task_id", task.ID),
		zap.Int("score", score))
	return nil
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
		result, err := NewLLMService().ChatCompletion(taskID, modelID, caller, "", userPrompt)
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
			result, err := NewLLMService().ChatCompletion(taskID, modelID, caller, "", batchPrompt)
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
		result, err := NewLLMService().ChatCompletion(taskID, modelID, caller, "", userPrompt)
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
		result, err := llmService.ChatCompletionStructured(&task.ID, 0, "runAIReviewStructured", "", userPrompt, responseFormat)
		if err != nil {
			return "", 0, userPrompt, 0, "", err
		}
		rawContent = result.Content
		actualModelID = result.ModelID
		actualModelName = result.ModelName
		llmResponse = result.Response
	} else {
		// 分批处理（暂不实现结构化分批，fallback 到旧模式）
		// TODO: 实现分批结构化评审
		return s.runAIReviewFallback(&task.ID, diffFiles, commitsText, task.MRTitle, userPrompt)
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
		result, err := llmService.ChatCompletionStructured(&task.ID, 0, "retry", "", userPrompt, responseFormat)
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
	return model.DB.Transaction(func(tx *gorm.DB) error {
		// 先清理旧记录（幂等写入，避免重试时重复）
		if err := tx.Where("task_id = ?", taskID).Delete(&model.TaskReviewRule{}).Error; err != nil {
			return fmt.Errorf("delete old task review rules failed: %w", err)
		}

		var records []model.TaskReviewRule
		now := time.Now()

		for i, rule := range selected {
			records = append(records, model.TaskReviewRule{
				TaskID:      taskID,
				RuleID:      &rule.ID,
				RuleCode:    rule.Code,
				Name:        rule.Name,
				Category:    rule.Category,
				Severity:    rule.Severity,
				SortOrder:   i + 1,
				WasSelected: true,
				IssueCount:  0,
				CreatedAt:   now,
			})
		}
		for i, rule := range truncated {
			records = append(records, model.TaskReviewRule{
				TaskID:      taskID,
				RuleID:      &rule.ID,
				RuleCode:    rule.Code,
				Name:        rule.Name,
				Category:    rule.Category,
				Severity:    rule.Severity,
				SortOrder:   len(selected) + i + 1,
				WasSelected: false,
				IssueCount:  0,
				CreatedAt:   now,
			})
		}

		if err := tx.Create(&records).Error; err != nil {
			return fmt.Errorf("create task review rules failed: %w", err)
		}

		zap.L().Info("task review rules persisted",
			zap.Uint("task_id", taskID),
			zap.Int("selected", len(selected)),
			zap.Int("truncated", len(truncated)))

		return nil
	})
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
			"new_path":   f.NewPath,
			"additions":  f.Additions,
			"deletions":  f.Deletions,
			"truncated":  truncated,
			"diff":       diffContent,
		})
	}
	return result
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
