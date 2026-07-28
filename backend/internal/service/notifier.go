package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

type NotifierService struct{}

func NewNotifierService() *NotifierService {
	return &NotifierService{}
}

func (s *NotifierService) List() ([]model.WeComNotifier, error) {
	var notifiers []model.WeComNotifier
	err := model.DB.Order("created_at DESC").Find(&notifiers).Error
	return notifiers, err
}

func (s *NotifierService) Get(id uint) (*model.WeComNotifier, error) {
	var notifier model.WeComNotifier
	if err := model.DB.First(&notifier, id).Error; err != nil {
		return nil, err
	}
	return &notifier, nil
}

func (s *NotifierService) Create(data map[string]interface{}) (*model.WeComNotifier, error) {
	notifier := model.WeComNotifier{
		Name:            data["name"].(string),
		WebhookUrl:      data["webhook_url"].(string),
		MessageTemplate: "",
		Enabled:         false, // 新建时未配置模板，默认禁用
	}

	if err := model.DB.Create(&notifier).Error; err != nil {
		return nil, err
	}
	return &notifier, nil
}

func (s *NotifierService) Update(id uint, data map[string]interface{}) error {
	updates := make(map[string]interface{})

	if v, ok := data["name"].(string); ok {
		updates["name"] = v
	}
	if v, ok := data["webhook_url"].(string); ok && v != "" {
		updates["webhook_url"] = v
	}

	if len(updates) == 0 {
		return nil
	}

	return model.DB.Model(&model.WeComNotifier{}).Where("id = ?", id).Updates(updates).Error
}

// UpdateTemplate 单独更新消息模板
func (s *NotifierService) UpdateTemplate(id uint, template string) error {
	return model.DB.Model(&model.WeComNotifier{}).Where("id = ?", id).Update("message_template", template).Error
}

func (s *NotifierService) Delete(id uint) error {
	return model.DB.Delete(&model.WeComNotifier{}, id).Error
}

func (s *NotifierService) Toggle(id uint, enabled bool) error {
	return model.DB.Model(&model.WeComNotifier{}).Where("id = ?", id).Update("enabled", enabled).Error
}

func (s *NotifierService) Test(id uint) (bool, string, error) {
	notifier, err := s.Get(id)
	if err != nil {
		return false, "", err
	}

	message := "测试消息 - CodeGuard 通知配置成功！"
	if notifier.MessageTemplate != "" {
		message = buildMessageFromRule("task.completed", notifier.MessageTemplate, &mockTask, notifier.ID)
	}

	return s.SendMessage(notifier.WebhookUrl, message, "")
}

// mockTask for template testing
var mockTask = model.Task{
	MRAuthor:     "developer1",
	MRTitle:      "feat: 新增功能示例",
	SourceBranch: "feature/example",
	TargetBranch: "main",
	ScoreValue:   85,
	Project:      model.Project{Name: "示例项目"},
}

func (s *NotifierService) SendMessage(webhookUrl, message string, mentionUserId string) (bool, string, error) {
	if webhookUrl == "" {
		return false, "webhook url is empty", nil
	}

	// 如果指定了 @ 人员，在 markdown 末尾追加
	if mentionUserId != "" {
		message = message + "\n\n<@" + mentionUserId + ">"
	}

	body := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": message,
		},
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", webhookUrl, bytes.NewBuffer(jsonBody))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)), nil
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return false, "", err
	}

	errcode, _ := result["errcode"].(float64)
	if errcode != 0 {
		errmsg, _ := result["errmsg"].(string)
		return false, errmsg, nil
	}

	return true, "发送成功", nil
}

// NotifyAIReviewCompleted 通知 AI 评审完成
func (s *NotifierService) NotifyAIReviewCompleted(task model.Task) {
	zap.L().Info("notify ai review: start",
		zap.Uint("task_id", task.ID),
		zap.Uint("project_id", task.ProjectID),
		zap.String("mr_author", task.MRAuthor),
		zap.String("project_name", task.Project.Name))

	// 获取启用了通知的配置
	var notifiers []model.WeComNotifier
	query := model.DB.Where("enabled = ?", true)
	if task.ProjectID > 0 {
		query = query.Where("project_id IS NULL OR project_id = ?", task.ProjectID)
	}
	if err := query.Find(&notifiers).Error; err != nil {
		zap.L().Error("notify ai review: get notifiers failed", zap.Error(err))
		return
	}

	zap.L().Info("notify ai review: notifiers query result",
		zap.Int("count", len(notifiers)),
		zap.Uint("project_id", task.ProjectID))

	if len(notifiers) == 0 {
		zap.L().Warn("notify ai review: no enabled notifier matched",
			zap.Uint("project_id", task.ProjectID),
			zap.String("hint", "请检查【通知管理 → 企业微信】中是否有启用的通知配置"))
		return
	}

	// 查询开发人员的 IM 用户 ID（用于 @）
	var mentionUserId string
	var member model.TeamMember
	if err := model.DB.Where("gitlab_username = ? AND im_platform = ? AND enabled = ?", task.MRAuthor, "wecom", true).First(&member).Error; err == nil {
		mentionUserId = member.IMUserID
		zap.L().Info("notify ai review: found team member",
			zap.String("gitlab_username", task.MRAuthor),
			zap.String("im_user_id", mentionUserId))
	} else {
		zap.L().Warn("notify ai review: no team member for @mention",
			zap.String("gitlab_username", task.MRAuthor))
	}

	for _, notifier := range notifiers {
		message := buildMessageFromRule("task.completed", notifier.MessageTemplate, &task, notifier.ID)

		success, msg, err := s.SendMessage(notifier.WebhookUrl, message, mentionUserId)
		status := "success"
		errMsg := ""
		if err != nil || !success {
			status = "failed"
			if err != nil {
				errMsg = err.Error()
			} else {
				errMsg = msg
			}
			zap.L().Error("notify ai review: send failed",
				zap.Uint("notifier_id", notifier.ID),
				zap.Error(err),
				zap.String("msg", msg))
		} else {
			zap.L().Info("notify ai review: sent",
				zap.Uint("notifier_id", notifier.ID),
				zap.String("webhook", notifier.WebhookUrl))
		}

		// 记录 IM 投递日志
		log := model.NotificationDeliveryLog{
			NotifierID:     &notifier.ID,
			TaskID:         &task.ID,
			Channel:        "wecom",
			RecipientType:  "steward",
			MessagePreview: truncateRune(message, 200),
			Status:         status,
			ErrorMsg:       errMsg,
		}
		if err := model.DB.Create(&log).Error; err != nil {
			zap.L().Error("notify ai review: failed to save delivery log",
				zap.Uint("notifier_id", notifier.ID),
				zap.Error(err))
		}
	}
}

// buildMessageFromRule 优先读取通知规则模板，其次 fallback 模板，最后默认模板
func buildMessageFromRule(trigger string, fallbackTemplate string, task *model.Task, notifierID uint) string {
	templateStr := GetNotificationRuleTemplate(trigger)
	source := "notification_rule"
	if templateStr == "" {
		templateStr = fallbackTemplate
		source = "notifier_fallback"
	}
	if templateStr == "" {
		templateStr = defaultReviewTemplate()
		source = "default_hardcoded"
	}

	zap.L().Info("buildMessageFromRule template selected",
		zap.String("trigger", trigger),
		zap.Uint("notifier_id", notifierID),
		zap.String("source", source),
		zap.Int("template_len", len(templateStr)))

	// 查询开发人员展示名映射
	developer := task.MRAuthor
	var tm model.TeamMember
	if err := model.DB.Where("gitlab_username = ? AND enabled = ?", task.MRAuthor, true).
		First(&tm).Error; err == nil && tm.DisplayName != "" {
		developer = task.MRAuthor + "(" + tm.DisplayName + ")"
	}

	// 获取 ReviewLog 的代码变更量
	additions, deletions := 0, 0
	var log model.MergeRequestReviewLog
	if err := model.DB.Where("url = ?", task.MRURL).Order("synced_at DESC").First(&log).Error; err == nil {
		additions = log.Additions
		deletions = log.Deletions
	}

	projectName := task.Project.Name
	if projectName == "" {
		projectName = "未知项目"
	}

	stats := CalcIssueStats(task.ID, task.MRMergeID)

	ctx := TemplateContext{
		Task:      *task,
		Stats:     stats,
		Project:   task.Project,
		Developer: developer,
		Additions: additions,
		Deletions: deletions,
		DeadlineHours: 120 - stats.Pending*2,
		AtRecipient: "",
	}
	return RenderMessage(templateStr, ctx)
}

func defaultReviewTemplate() string {
	return "**🔍 AI 代码评审完成**\n\n**项目：** {{PROJECT_NAME}}\n**开发人员：** {{DEVELOPER}}\n**分支：** {{BRANCH}}\n**分数：** **{{SCORE}}** 分\n**变更：** {{CHANGES}}\n**MR：** {{MR_TITLE}}\n**MR 链接：** {{MR_URL}}"
}

// SendMonitorAlert 发送监控告警消息到企业微信
func (s *NotifierService) SendMonitorAlert(webhookUrl, message, mentionUserIDs string) (bool, string, error) {
	if webhookUrl == "" {
		return false, "webhook 为空", nil
	}
	// 拼接 @ 人员
	if mentionUserIDs != "" {
		ids := strings.Split(mentionUserIDs, ",")
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" {
				message = message + "\n\n<@" + id + ">"
			}
		}
	}
	return s.SendMessage(webhookUrl, message, "")
}

// SendResourcePoolAlert 发送资源池告警（异常或恢复）
func SendResourcePoolAlert(pool model.ResourcePool, isRecovery bool, alertDuration, alertCooldown int, notifierID uint, mentionUserIDs string) {
	if notifierID == 0 {
		return
	}
	var notifier model.WeComNotifier
	if err := model.DB.First(&notifier, notifierID).Error; err != nil {
		zap.L().Error("send pool alert: notifier not found", zap.Uint("notifier_id", notifierID), zap.Error(err))
		return
	}
	if notifier.WebhookUrl == "" {
		return
	}

	var msg string
	if isRecovery {
		msg = fmt.Sprintf(
			"✅ **CodeGuard 资源池恢复通知**\n\n"+
				"**资源池**：%s\n"+
				"**状态**：已恢复正常\n"+
				"**Endpoint**：%s",
			pool.Name, pool.OpencodeEndpoint,
		)
	} else {
		duration := "未知"
		if pool.StatusChangedAt != nil {
			d := time.Since(*pool.StatusChangedAt)
			duration = fmt.Sprintf("%d分%d秒", int(d.Minutes()), int(d.Seconds())%60)
		}
		errText := pool.CheckError
		if errText == "" {
			errText = "未知错误"
		}
		msg = fmt.Sprintf(
			"⚠️ **CodeGuard 资源池告警**\n\n"+
				"**资源池**：%s\n"+
				"**状态**：已异常 %s\n"+
				"**错误**：<font color=\"warning\">%s</font>\n"+
				"**Endpoint**：%s",
			pool.Name, duration, errText, pool.OpencodeEndpoint,
		)
	}

	svc := NewNotifierService()
	success, resMsg, err := svc.SendMonitorAlert(notifier.WebhookUrl, msg, mentionUserIDs)
	if err != nil || !success {
		zap.L().Error("send pool alert failed", zap.Uint("pool_id", pool.ID), zap.Error(err), zap.String("msg", resMsg))
	} else {
		zap.L().Info("send pool alert success", zap.Uint("pool_id", pool.ID), zap.Bool("recovery", isRecovery))
	}
}

// SendModelAlert 发送大模型告警（异常或恢复）
func SendModelAlert(m model.LLMModel, isRecovery bool, alertDuration, alertCooldown int, notifierID uint, mentionUserIDs string) {
	if notifierID == 0 {
		return
	}
	var notifier model.WeComNotifier
	if err := model.DB.First(&notifier, notifierID).Error; err != nil {
		zap.L().Error("send model alert: notifier not found", zap.Uint("notifier_id", notifierID), zap.Error(err))
		return
	}
	if notifier.WebhookUrl == "" {
		return
	}

	var msg string
	if isRecovery {
		msg = fmt.Sprintf(
			"✅ **CodeGuard 模型恢复通知**\n\n"+
				"**模型**：%s (%s)\n"+
				"**状态**：已恢复正常\n"+
				"**BaseURL**：%s",
			m.ModelID, m.Provider, m.BaseURL,
		)
	} else {
		duration := "未知"
		if m.StatusChangedAt != nil {
			d := time.Since(*m.StatusChangedAt)
			duration = fmt.Sprintf("%d分%d秒", int(d.Minutes()), int(d.Seconds())%60)
		}
		errText := m.CheckError
		if errText == "" {
			errText = "未知错误"
		}
		msg = fmt.Sprintf(
			"⚠️ **CodeGuard 模型告警**\n\n"+
				"**模型**：%s (%s)\n"+
				"**状态**：已异常 %s\n"+
				"**错误**：<font color=\"warning\">%s</font>\n"+
				"**BaseURL**：%s",
			m.ModelID, m.Provider, duration, errText, m.BaseURL,
		)
	}

	svc := NewNotifierService()
	success, resMsg, err := svc.SendMonitorAlert(notifier.WebhookUrl, msg, mentionUserIDs)
	if err != nil || !success {
		zap.L().Error("send model alert failed", zap.Uint("model_id", m.ID), zap.Error(err), zap.String("msg", resMsg))
	} else {
		zap.L().Info("send model alert success", zap.Uint("model_id", m.ID), zap.Bool("recovery", isRecovery))
	}
}
