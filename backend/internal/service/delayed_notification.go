package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// DelayedNotificationQueue 延迟通知合并队列（内存实现，后续可迁移 Redis）
type DelayedNotificationQueue struct {
	mu   sync.RWMutex
	jobs map[uint]*pendingNotifyJob // task_id -> job
}

type pendingNotifyJob struct {
	TaskID         uint
	FirstEnqueueAt time.Time
	FireAt         time.Time
	Payload        notifyPayload
}

type notifyPayload struct {
	ProjectName       string
	MRAuthor          string
	MRTitle           string
	MRURL             string
	ExecutionCount    int
	Total             int
	Pending           int
	Critical          int
	High              int
	Medium            int
	Low               int
	AutoFiltered      int
	Gone              int
	New               int
	DeveloperUsername string
	DeveloperName     string
}

var (
	defaultDelayQueue     *DelayedNotificationQueue
	defaultDelayQueueOnce sync.Once
)

func GetDelayedNotificationQueue() *DelayedNotificationQueue {
	defaultDelayQueueOnce.Do(func() {
		defaultDelayQueue = &DelayedNotificationQueue{
			jobs: make(map[uint]*pendingNotifyJob),
		}
		// 启动后台扫描器
		go defaultDelayQueue.backgroundScanner()
	})
	return defaultDelayQueue
}

// Enqueue 任务完成时调用，合并重试通知
func (q *DelayedNotificationQueue) Enqueue(task model.Task, stats IssueStats) {
	// 尝试根据 MRAuthor 查找 DisplayName
	devName := task.MRAuthor
	var mapping model.MemberMapping
	if err := model.DB.Where("git_username = ? AND im_platform = ? AND enabled = ?", task.MRAuthor, model.IMPlatformWeCom, true).First(&mapping).Error; err == nil && mapping.DisplayName != "" {
		devName = mapping.DisplayName
	}

	payload := notifyPayload{
		ProjectName:       task.Project.Name,
		MRAuthor:          task.MRAuthor,
		MRTitle:           task.MRTitle,
		MRURL:             task.MRURL,
		ExecutionCount:    task.ExecutionCount,
		Total:             stats.Total,
		Pending:           stats.Pending,
		Critical:          stats.Critical,
		High:              stats.High,
		Medium:            stats.Medium,
		Low:               stats.Low,
		AutoFiltered:      stats.AutoFiltered,
		Gone:              stats.Gone,
		New:               stats.New,
		DeveloperUsername: task.MRAuthor,
		DeveloperName:     devName,
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if existing, ok := q.jobs[task.ID]; ok {
		// 已存在，覆盖 payload，但保持首次入队时间
		existing.Payload = payload
		zap.L().Info("delayed notify updated", zap.Uint("task_id", task.ID))
		return
	}

	// 首次入队
	delay := 15 * time.Minute
	if task.ExecutionCount <= 1 {
		delay = 2 * time.Minute // 首次运行快速通知
	}

	now := time.Now()
	q.jobs[task.ID] = &pendingNotifyJob{
		TaskID:         task.ID,
		FirstEnqueueAt: now,
		FireAt:         now.Add(delay),
		Payload:        payload,
	}
	zap.L().Info("delayed notify enqueued",
		zap.Uint("task_id", task.ID),
		zap.Duration("delay", delay))
}

// backgroundScanner 后台扫描器，每分钟检查到期任务
func (q *DelayedNotificationQueue) backgroundScanner() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		q.fireDueJobs()
	}
}

func (q *DelayedNotificationQueue) fireDueJobs() {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	for taskID, job := range q.jobs {
		if !job.FireAt.Before(now) && !job.FireAt.Equal(now) {
			continue
		}
		// 到期，执行通知
		q.executeJob(taskID, job)
		delete(q.jobs, taskID)
	}
}

func (q *DelayedNotificationQueue) executeJob(taskID uint, job *pendingNotifyJob) {
	payload := job.Payload

	// 1. 站内信通知开发者（MR 提交者）
	var mapping model.MemberMapping
	var mentionUserID string
	if err := model.DB.Where("git_username = ? AND im_platform = ? AND enabled = ?", payload.MRAuthor, model.IMPlatformWeCom, true).First(&mapping).Error; err == nil {
		mentionUserID = mapping.IMUserID
	}

	// 查找用户 ID 用于站内信
	var user model.User
	ownerID := uint(0)
	if err := model.DB.Where("gitlab_username = ?", payload.MRAuthor).First(&user).Error; err == nil {
		ownerID = user.ID
	}

	notifSvc := NewNotificationService()

	// 构建站内信内容
	link := payload.MRURL
	if link == "" {
		link = fmt.Sprintf("/tasks.html?id=%d", taskID)
	}
	notifSvc.SendInbox(ownerID, model.NotificationTypeTaskCompleted,
		fmt.Sprintf("MR !%s 评审已完成（第 %d 次）", payload.MRTitle, payload.ExecutionCount),
		buildInboxContent(payload),
		link,
	)

	// 2. 企微群 IM 推送（仅推送任务所属项目 + 全局的 notifier）
	var task model.Task
	model.DB.First(&task, taskID)
	var notifiers []model.WeComNotifier
	db := model.DB.Where("enabled = ?", true)
	if task.ProjectID > 0 {
		db = db.Where("project_id IS NULL OR project_id = ?", task.ProjectID)
	} else {
		db = db.Where("project_id IS NULL")
	}
	db.Find(&notifiers)
	for _, notifier := range notifiers {
		msg := buildIMMarkdown(payload, mentionUserID)
		notifierSvc := NewNotifierService()
		ok, detail, err := notifierSvc.SendMessage(notifier.WebhookUrl, msg, "")
		// 记录投递日志
		log := model.NotificationDeliveryLog{
			NotifierID:     &notifier.ID,
			TaskID:         &taskID,
			Channel:        "wecom",
			RecipientType:  "mr_author",
			MessagePreview: truncate(msg, 200),
			Status:         "success",
		}
		if !ok || err != nil {
			log.Status = "failed"
			log.ErrorMsg = detail
			if err != nil {
				log.ErrorMsg = err.Error()
			}
		}
		model.DB.Create(&log)
	}

	zap.L().Info("delayed notify fired",
		zap.Uint("task_id", taskID),
		zap.Int("pending", payload.Pending))
}

func buildInboxContent(payload notifyPayload) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("项目：%s\n", payload.ProjectName))
	sb.WriteString(fmt.Sprintf("MR：%s\n", payload.MRTitle))
	sb.WriteString(fmt.Sprintf("本次发现：%d 条 Issue\n", payload.Total))
	if payload.Critical > 0 {
		sb.WriteString(fmt.Sprintf("🔴 严重：%d 条\n", payload.Critical))
	}
	if payload.High > 0 {
		sb.WriteString(fmt.Sprintf("🟡 高危：%d 条\n", payload.High))
	}
	if payload.Medium > 0 {
		sb.WriteString(fmt.Sprintf("🟢 中危：%d 条\n", payload.Medium))
	}
	if payload.AutoFiltered > 0 {
		sb.WriteString(fmt.Sprintf("\n🔍 智能过滤：%d 条历史已判定问题已自动过滤\n", payload.AutoFiltered))
	}
	sb.WriteString(fmt.Sprintf("\n当前待处理：%d 条", payload.Pending))
	return sb.String()
}

func buildIMMarkdown(payload notifyPayload, mentionUserID string) string {
	// 非工作时间（22:00-09:00）降级 @ 为纯文本，避免强提醒打扰
	if mentionUserID != "" && isQuietHours() {
		mentionUserID = ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**🔍 AI 代码评审任务完成**\n\n"))
	sb.WriteString(fmt.Sprintf("**项目：** %s\n", payload.ProjectName))
	sb.WriteString(fmt.Sprintf("**MR：** !%s\n", payload.MRTitle))
	sb.WriteString(fmt.Sprintf("**提交者：** %s\n", payload.DeveloperName))
	sb.WriteString(fmt.Sprintf("**运行次数：** 第 %d 次\n\n", payload.ExecutionCount))

	sb.WriteString(fmt.Sprintf("**📊 Issue 统计**\n"))
	sb.WriteString(fmt.Sprintf("本次发现：%d 条\n", payload.Total))
	if payload.Critical > 0 {
		sb.WriteString(fmt.Sprintf("🔴 严重：%d 条\n", payload.Critical))
	}
	if payload.High > 0 {
		sb.WriteString(fmt.Sprintf("🟡 高危：%d 条\n", payload.High))
	}
	if payload.Medium > 0 {
		sb.WriteString(fmt.Sprintf("🟢 中危：%d 条\n", payload.Medium))
	}
	if payload.Low > 0 {
		sb.WriteString(fmt.Sprintf("⚪ 低危：%d 条\n", payload.Low))
	}

	if payload.AutoFiltered > 0 {
		sb.WriteString(fmt.Sprintf("\n🔍 智能过滤：%d 条历史已判定问题已自动过滤\n", payload.AutoFiltered))
	}
	if payload.Gone > 0 {
		sb.WriteString(fmt.Sprintf("🧹 已清除：%d 条上次问题本次未复现\n", payload.Gone))
	}
	if payload.New > 0 {
		sb.WriteString(fmt.Sprintf("✨ 新增：%d 条\n", payload.New))
	}

	sb.WriteString(fmt.Sprintf("\n**📋 当前待处理：%d 条**\n", payload.Pending))
	sb.WriteString("⏰ 请在 2 个工作日内处理，超期将通知项目负责人\n")

	if mentionUserID != "" {
		sb.WriteString(fmt.Sprintf("\n<@%s>", mentionUserID))
	}

	return sb.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// isQuietHours 判断当前是否处于静默时段（22:00-09:00）
func isQuietHours() bool {
	hour := time.Now().Hour()
	return hour >= 22 || hour < 9
}

// 兼容旧 notifier.go 中可能需要的辅助函数
func jsonMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
