package service

import (
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// NotificationService 站内信/通知中心服务
type NotificationService struct{}

func NewNotificationService() *NotificationService {
	return &NotificationService{}
}

// SendInbox 发送站内信（100% 可靠，不依赖 IM）
func (s *NotificationService) SendInbox(userID uint, notifType string, title string, content string, link string) {
	if userID == 0 {
		return
	}
	n := model.Notification{
		UserID:  userID,
		Type:    notifType,
		Title:   title,
		Content: content,
		Link:    link,
	}
	if err := model.DB.Create(&n).Error; err != nil {
		zap.L().Error("send inbox notification failed",
			zap.Uint("user_id", userID),
			zap.String("type", notifType),
			zap.Error(err))
	}
}

// MarkRead 标记已读
func (s *NotificationService) MarkRead(userID uint, notifID uint) error {
	return model.DB.Model(&model.Notification{}).
		Where("id = ? AND user_id = ?", notifID, userID).
		Updates(map[string]interface{}{"is_read": true, "read_at": "NOW()"}).Error
}

// MarkAllRead 全部已读
func (s *NotificationService) MarkAllRead(userID uint) error {
	return model.DB.Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Updates(map[string]interface{}{"is_read": true, "read_at": "NOW()"}).Error
}

// List 站内信列表
func (s *NotificationService) List(userID uint, notifType string, isRead *bool, page, pageSize int) ([]model.Notification, int64, error) {
	db := model.DB.Where("user_id = ?", userID)
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
func (s *NotificationService) UnreadCount(userID uint) int64 {
	var count int64
	model.DB.Model(&model.Notification{}).Where("user_id = ? AND is_read = ?", userID, false).Count(&count)
	return count
}

// --- Issue Stats 统计与模板渲染 ---

// IssueStats 任务完成时的 Issue 统计
type IssueStats struct {
	Total           int
	Pending         int
	Critical        int
	High            int
	Medium          int
	Low             int
	AutoFiltered    int
	Gone            int
	New             int
	ResolvedReappeared int
}

// CalcIssueStats 计算当前任务的 Issue 统计（含历史对比）
func CalcIssueStats(taskID uint, mrID int) IssueStats {
	var stats IssueStats

	// 当前版本有效 Issue
	var currentIssues []model.ReviewIssue
	model.DB.Where("task_id = ? AND deleted_at IS NULL", taskID).Find(&currentIssues)
	for _, issue := range currentIssues {
		stats.Total++
		switch issue.Severity {
		case "critical": stats.Critical++
		case "high": stats.High++
		case "medium": stats.Medium++
		case "low": stats.Low++
		}
		if issue.Status == model.IssueStatusPending {
			stats.Pending++
		}
		if issue.Status == model.IssueStatusAutoFiltered {
			stats.AutoFiltered++
		}
		if issue.Status == model.IssueStatusPending && issue.InheritedFromIssueID != nil {
			// 曾 resolved 又出现的
			var parent model.ReviewIssue
			if model.DB.Unscoped().Where("id = ?", *issue.InheritedFromIssueID).First(&parent).Error == nil {
				if parent.Status == model.IssueStatusResolved {
					stats.ResolvedReappeared++
				}
			}
		}
	}

	// 与上次成功版本对比
	if mrID > 0 {
		var lastTask model.Task
		if err := model.DB.Where("mr_iid = ? AND id < ? AND status = ?", mrID, taskID, model.TaskSuccess).
			Order("id DESC").First(&lastTask).Error; err == nil {
			var lastIssues []model.ReviewIssue
			// 上次任务的 Issue 可能已被 soft delete（重试后），需用 Unscoped 查全量
			model.DB.Unscoped().Where("task_id = ?", lastTask.ID).Find(&lastIssues)
			lastTotal := len(lastIssues)
			// "gone" = 上次有但本次没有（且非 auto_filtered）
			// 简化：上次总数 - (本次总数 - auto_filtered) = gone
			visibleCurrent := stats.Total - stats.AutoFiltered
			stats.Gone = lastTotal - visibleCurrent
			if stats.Gone < 0 {
				stats.Gone = 0
			}
			stats.New = visibleCurrent - (lastTotal - stats.Gone)
			if stats.New < 0 {
				stats.New = 0
			}
		}
	}

	return stats
}

// TemplateContext 模板渲染上下文
type TemplateContext struct {
	Task      model.Task
	Stats     IssueStats
	Project   model.Project
	Developer string // MR 提交者含 DisplayName
	Steward   string
}

// RenderMessage 根据模板和上下文渲染消息
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

	return msg
}
