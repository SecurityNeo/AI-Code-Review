package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// NotificationService 站内信/通知中心服务
type NotificationService struct{}

func NewNotificationService() *NotificationService {
	return &NotificationService{}
}

// SendInbox 发送站内信（100% 可靠，不依赖 IM）
// 多租户改造：显式传入 orgID，避免通过 user.default_org_id 推断导致跨组织数据污染
func (s *NotificationService) SendInbox(orgID, userID uint, notifType string, title string, content string, link string) {
	if userID == 0 {
		return
	}
	if orgID == 0 {
		orgID = 1
	}
	// Title 截断：数据库字段 size=200，预留安全余量
	title = truncateRune(title, 190)
	n := model.Notification{
		OrgID:   orgID,
		UserID:  userID,
		Type:    notifType,
		Title:   title,
		Content: content,
		Link:    link,
	}
	if err := model.DB.Create(&n).Error; err != nil {
		zap.L().Error("send inbox notification failed",
			zap.Uint("user_id", userID),
			zap.Uint("org_id", orgID),
			zap.String("type", notifType),
			zap.Error(err))
	}
}

// MarkRead 标记已读
func (s *NotificationService) MarkRead(scope *model.UserAuthScope, userID uint, notifID uint) error {
	db := model.DBWithScope(scope)
	return db.Model(&model.Notification{}).
		Where("id = ? AND user_id = ?", notifID, userID).
		Updates(map[string]interface{}{"is_read": true, "read_at": time.Now()}).Error
}

// MarkAllRead 全部已读
func (s *NotificationService) MarkAllRead(scope *model.UserAuthScope, userID uint) error {
	db := model.DBWithScope(scope)
	return db.Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Updates(map[string]interface{}{"is_read": true, "read_at": time.Now()}).Error
}

// List 站内信列表
func (s *NotificationService) List(scope *model.UserAuthScope, userID uint, notifType string, isRead *bool, page, pageSize int) ([]model.Notification, int64, error) {
	db := model.DBWithScope(scope).Where("user_id = ?", userID)
	if notifType != "" {
		db = db.Where("type = ?", notifType)
	}
	if isRead != nil {
		db = db.Where("is_read = ?", *isRead)
	}
	var total int64
	db.Model(&model.Notification{}).Count(&total)

	var list []model.Notification
	err := db.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// UnreadCount 未读数
func (s *NotificationService) UnreadCount(scope *model.UserAuthScope, userID uint) int64 {
	var count int64
	model.DBWithScope(scope).Model(&model.Notification{}).Where("user_id = ? AND is_read = ?", userID, false).Count(&count)
	return count
}

// --- Issue Stats 统计与模板渲染 ---

// IssueStats 任务完成时的 Issue 统计
type IssueStats struct {
	Total              int
	Pending            int
	Critical           int
	High               int
	Medium             int
	Low                int
	AutoFiltered       int
	Gone               int
	New                int
	ResolvedReappeared int
}

// CalcIssueStats 计算当前任务的 Issue 统计（含历史对比）
func CalcIssueStats(scope *model.UserAuthScope, taskID uint, mrID int) IssueStats {
	var stats IssueStats

	// 当前版本有效 Issue
	var currentIssues []model.ReviewIssue
	model.DBWithScope(scope).Where("task_id = ? AND deleted_at IS NULL", taskID).Find(&currentIssues)
	for _, issue := range currentIssues {
		stats.Total++
		switch issue.Severity {
		case "critical":
			stats.Critical++
		case "high":
			stats.High++
		case "medium":
			stats.Medium++
		case "low":
			stats.Low++
		}
		if issue.Status == model.IssueStatusPending {
			stats.Pending++
		}
		if issue.Status == model.IssueStatusAutoFiltered {
			stats.AutoFiltered++
		}
		if issue.Status == model.IssueStatusPendingInherited {
			// 历史 resolved 问题复现
			stats.ResolvedReappeared++
		}
	}

	// 与上次成功版本对比（基于指纹精确匹配）
	if mrID > 0 {
		var lastTask model.Task
		if err := model.DBWithScope(scope).Where("mr_merge_id = ? AND id < ? AND status = ?", mrID, taskID, model.TaskSuccess).
			Order("id DESC").First(&lastTask).Error; err == nil {
			var lastIssues []model.ReviewIssue
			model.DB.Unscoped().Where("task_id = ?", lastTask.ID).Find(&lastIssues)

			// 建立指纹集合（排除 auto_filtered，因为它们不算"可见"Issue）
			lastFPs := make(map[string]bool)
			for _, iss := range lastIssues {
				if iss.Fingerprint != "" && iss.Status != model.IssueStatusAutoFiltered {
					lastFPs[iss.Fingerprint] = true
				}
			}
			currentFPs := make(map[string]bool)
			for _, iss := range currentIssues {
				if iss.Fingerprint != "" && iss.Status != model.IssueStatusAutoFiltered {
					currentFPs[iss.Fingerprint] = true
				}
			}

			// gone = 上次有但本次没有
			for fp := range lastFPs {
				if !currentFPs[fp] {
					stats.Gone++
				}
			}
			// new = 本次有但上次没有
			for fp := range currentFPs {
				if !lastFPs[fp] {
					stats.New++
				}
			}
		}
	}

	return stats
}

// TemplateContext 模板渲染上下文
type TemplateContext struct {
	Task          model.Task
	Stats         IssueStats
	Project       model.Project
	Developer     string // MR 提交者含 DisplayName
	Steward       string
	Additions     int
	Deletions     int
	DeadlineHours int
	AtRecipient   string
}

// RenderMessage 根据模板和上下文渲染消息（支持 Mustache 条件语法）
func RenderMessage(template string, ctx TemplateContext) string {
	task := ctx.Task
	stats := ctx.Stats

	msg := template
	msg = strings.ReplaceAll(msg, "{{PROJECT_NAME}}", ctx.Project.Name)
	msg = strings.ReplaceAll(msg, "{{TASK_ID}}", fmt.Sprintf("%d", task.ID))
	msg = strings.ReplaceAll(msg, "{{EXECUTION_COUNT}}", fmt.Sprintf("%d", task.ExecutionCount))
	msg = strings.ReplaceAll(msg, "{{MR_IID}}", fmt.Sprintf("%d", task.MRMergeID))
	msg = strings.ReplaceAll(msg, "{{MR_TITLE}}", task.MRTitle)
	msg = strings.ReplaceAll(msg, "{{MR_AUTHOR}}", task.MRAuthor)
	msg = strings.ReplaceAll(msg, "{{DEVELOPER}}", ctx.Developer)
	msg = strings.ReplaceAll(msg, "{{BRANCH}}", task.SourceBranch+" -> "+task.TargetBranch)
	msg = strings.ReplaceAll(msg, "{{SCORE}}", fmt.Sprintf("%d", task.ScoreValue))
	msg = strings.ReplaceAll(msg, "{{CHANGES}}", fmt.Sprintf("+%d/-%d", ctx.Additions, ctx.Deletions))
	msg = strings.ReplaceAll(msg, "{{MR_URL}}", task.MRURL)

	// Issue 统计
	msg = strings.ReplaceAll(msg, "{{ISSUE_TOTAL}}", fmt.Sprintf("%d", stats.Total))
	msg = strings.ReplaceAll(msg, "{{ISSUE_PENDING}}", fmt.Sprintf("%d", stats.Pending))
	msg = strings.ReplaceAll(msg, "{{ISSUE_CRITICAL}}", fmt.Sprintf("%d", stats.Critical))
	msg = strings.ReplaceAll(msg, "{{ISSUE_HIGH}}", fmt.Sprintf("%d", stats.High))
	msg = strings.ReplaceAll(msg, "{{ISSUE_MEDIUM}}", fmt.Sprintf("%d", stats.Medium))
	msg = strings.ReplaceAll(msg, "{{ISSUE_LOW}}", fmt.Sprintf("%d", stats.Low))
	msg = strings.ReplaceAll(msg, "{{ISSUE_AUTO_FILTERED}}", fmt.Sprintf("%d", stats.AutoFiltered))
	msg = strings.ReplaceAll(msg, "{{ISSUE_GONE}}", fmt.Sprintf("%d", stats.Gone))
	msg = strings.ReplaceAll(msg, "{{ISSUE_NEW}}", fmt.Sprintf("%d", stats.New))
	msg = strings.ReplaceAll(msg, "{{ISSUE_RESOLVED_REAPPEARED}}", fmt.Sprintf("%d", stats.ResolvedReappeared))
	msg = strings.ReplaceAll(msg, "{{STEWARD_NAME}}", ctx.Steward)
	msg = strings.ReplaceAll(msg, "{{DEADLINE_HOURS}}", fmt.Sprintf("%d", ctx.DeadlineHours))
	msg = strings.ReplaceAll(msg, "{{AT_RECIPIENT}}", ctx.AtRecipient)

	// Mustache 条件语法 {{#if VAR}}...{{/if}}
	variables := map[string]interface{}{
		"ISSUE_TOTAL": stats.Total, "ISSUE_PENDING": stats.Pending,
		"ISSUE_CRITICAL": stats.Critical, "ISSUE_HIGH": stats.High,
		"ISSUE_MEDIUM": stats.Medium, "ISSUE_LOW": stats.Low,
		"ISSUE_AUTO_FILTERED": stats.AutoFiltered, "ISSUE_GONE": stats.Gone,
		"ISSUE_NEW": stats.New, "ISSUE_RESOLVED_REAPPEARED": stats.ResolvedReappeared,
		"EXECUTION_COUNT": task.ExecutionCount,
	}
	for name, val := range variables {
		startTag := "{{#if " + name + "}}"
		endTag := "{{/if " + name + "}}"
		keep := false
		if v, ok := val.(int); ok && v > 0 {
			keep = true
		}
		msg = renderMustacheBlock(msg, startTag, endTag, keep)
	}

	return msg
}

func renderMustacheBlock(msg, startTag, namedEndTag string, keep bool) string {
	for {
		startIdx := strings.Index(msg, startTag)
		if startIdx == -1 {
			break
		}
		rest := msg[startIdx+len(startTag):]
		// 优先匹配带变量名的闭合标签 {{/if VAR}}
		endIdx := strings.Index(rest, namedEndTag)
		actualEndTag := namedEndTag
		if endIdx == -1 {
			// 兜底：匹配不带变量名的闭合标签 {{/if}}
			genericEnd := "{{/if}}"
			endIdx = strings.Index(rest, genericEnd)
			if endIdx == -1 {
				break
			}
			actualEndTag = genericEnd
		}
		if keep {
			msg = msg[:startIdx] + rest[:endIdx] + rest[endIdx+len(actualEndTag):]
		} else {
			msg = msg[:startIdx] + rest[endIdx+len(actualEndTag):]
		}
	}
	return msg
}

// IMTemplateContext Issue 升级 IM 模板渲染上下文
type IMTemplateContext struct {
	Project      model.Project
	Task         model.Task
	Developer    string
	AtRecipient  string
	IssueCount   int
	IssueList    []IMIssueItem
	IssueID      uint
	IssueMessage string
}

// IMIssueItem 聚合中的单条 Issue
type IMIssueItem struct {
	ID      uint
	Message string
}

// RenderIMTemplate 渲染 IM 升级模板
func RenderIMTemplate(template string, ctx IMTemplateContext) string {
	if template == "" {
		return ""
	}

	// 聚合变量前置处理（{{ISSUE_LIST}} 最长 3500 字节，逐条累加，超限截断）
	issueListRendered := ""
	if strings.Contains(template, "{{ISSUE_LIST}}") {
		var lines []string
		list := ctx.IssueList
		if len(list) == 0 {
			// 单条模式兜底
			list = append(list, IMIssueItem{ID: ctx.IssueID, Message: ctx.IssueMessage})
		}
		const maxBytes = 3500
		currentBytes := 0
		for i, item := range list {
			line := fmt.Sprintf("【%d】%s", item.ID, item.Message)
			lineBytes := len(line)
			if i > 0 {
				lineBytes++ // 换行符 \n
			}
			if currentBytes+lineBytes > maxBytes {
				// 加上这条会超过限制，停止组装
				remaining := len(list) - i
				if remaining > 0 {
					lines = append(lines, fmt.Sprintf("（还有 %d 条...）", remaining))
				} else {
					lines = append(lines, "...")
				}
				break
			}
			lines = append(lines, line)
			currentBytes += lineBytes
		}
		if len(lines) > 0 {
			issueListRendered = strings.Join(lines, "\n")
		}
	}

	replacements := []string{
		"{{PROJECT_NAME}}", ctx.Project.Name,
		"{{TASK_ID}}", fmt.Sprintf("%d", ctx.Task.ID),
		"{{MR_IID}}", fmt.Sprintf("%d", ctx.Task.MRMergeID),
		"{{MR_TITLE}}", ctx.Task.MRTitle,
		"{{MR_AUTHOR}}", ctx.Task.MRAuthor,
		"{{DEVELOPER}}", ctx.Developer,
		"{{AT_RECIPIENT}}", ctx.AtRecipient,
		"{{ISSUE_COUNT}}", fmt.Sprintf("%d", ctx.IssueCount),
		"{{ISSUE_LIST}}", issueListRendered,
		"{{ISSUE_ID}}", fmt.Sprintf("%d", ctx.IssueID),
		"{{ISSUE_MESSAGE}}", ctx.IssueMessage,
	}

	return strings.NewReplacer(replacements...).Replace(template)
}

// GetNotificationRuleTemplate 按 trigger 查询启用的通知规则模板
func GetNotificationRuleTemplate(scope *model.UserAuthScope, trigger string) string {
	var rule model.NotificationRule
	if err := model.DBWithScope(scope).Where("`trigger` = ? AND enabled = ?", trigger, true).Order("id DESC").First(&rule).Error; err == nil {
		if rule.Template == "" {
			zap.L().Warn("notification rule found but template is empty",
				zap.String("trigger", trigger),
				zap.Uint("rule_id", rule.ID),
				zap.String("rule_name", rule.Name),
				zap.String("hint", "请在通知规则中填写消息模板，否则将 fallback 到企业微信机器人中的旧模板"))
		} else {
			zap.L().Info("notification rule template found",
				zap.String("trigger", trigger),
				zap.Uint("rule_id", rule.ID),
				zap.String("rule_name", rule.Name),
				zap.Int("template_len", len(rule.Template)))
		}
		return rule.Template
	} else {
		zap.L().Warn("notification rule template not found",
			zap.String("trigger", trigger),
			zap.Error(err))
	}
	return ""
}

// truncateRune 按 Unicode 字符截断字符串，避免截断多字节字符
func truncateRune(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
