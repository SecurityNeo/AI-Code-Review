package handler

import (
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

	// 待处理列表（按超期风险排序：escalation_level 越低越优先 > severity > original_created_at ASC）
	var pendingIssues []model.ReviewIssue
	model.DB.Where("deleted_at IS NULL AND status IN (?) AND (owner_id = ? OR current_owner_id = ?)",
		[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, user.ID, user.ID).
		Order("escalation_level ASC, severity DESC, original_created_at ASC").
		Limit(20).Find(&pendingIssues)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"pending_count":  pendingCount,
		"critical_count": criticalCount,
		"high_count":     highCount,
		"week_received":  weekReceived,
		"week_resolved":  weekResolved,
		"issues":         pendingIssues,
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
		Where("deleted_at IS NULL AND status = ?", model.IssueStatusPending).Count(&totalPending)

	// overdue: pending > 120 work hours（简化用 5 天）
	overdueSince := time.Now().AddDate(0, 0, -5)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status = ? AND original_created_at < ?", model.IssueStatusPending, overdueSince).Count(&overdue)

	// IM 投递失败统计
	var imFailed int64
	todayStart = time.Now().Truncate(24 * time.Hour)
	model.DB.Model(&model.NotificationDeliveryLog{}).
		Where("status = ? AND created_at >= ?", "failed", todayStart).Count(&imFailed)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"today_new":     todayNew,
		"total_pending": totalPending,
		"overdue":       overdue,
		"im_failed":     imFailed,
	}})
}
