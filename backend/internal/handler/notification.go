package handler

import (
	"fmt"
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
		Where("deleted_at IS NULL AND status IN (?)", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}).Count(&totalPending)

	// overdue: pending > 120 work hours（简化用 5 天）
	overdueSince := time.Now().AddDate(0, 0, -5)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND status IN (?) AND original_created_at < ?", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}, overdueSince).Count(&overdue)

	// IM 投递失败统计
	var imFailed int64
	model.DB.Model(&model.NotificationDeliveryLog{}).
		Where("status = ? AND created_at >= ?", "failed", todayStart).Count(&imFailed)

	// 7 天闭环率
	sevenDaysAgo := time.Now().AddDate(0, 0, -7)
	var weekCreated, weekClosed int64
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND created_at >= ?", sevenDaysAgo).Count(&weekCreated)
	model.DB.Model(&model.ReviewIssue{}).
		Where("deleted_at IS NULL AND resolved_at >= ? AND status IN (?)", sevenDaysAgo,
			[]string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}).Count(&weekClosed)
	closeRate := float64(0)
	if weekCreated > 0 {
		closeRate = float64(weekClosed) / float64(weekCreated) * 100
	}

	// 闭环速度中位数（仅取最近 30 天已闭环的）
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	type ResolveDuration struct {
		DurationSec float64
	}
	var durations []ResolveDuration
	model.DB.Raw(`
		SELECT TIMESTAMPDIFF(SECOND, original_created_at, resolved_at) AS duration_sec
		FROM review_issues
		WHERE deleted_at IS NULL AND status IN (?) AND resolved_at IS NOT NULL
			AND resolved_at >= ?
	`, []string{model.IssueStatusResolved, model.IssueStatusFalsePositive, model.IssueStatusIgnored}, thirtyDaysAgo).Scan(&durations)
	medianDays := float64(0)
	if len(durations) > 0 {
		vals := make([]float64, len(durations))
		for i, d := range durations {
			vals[i] = d.DurationSec / 86400.0
		}
		medianDays = medianFloat64(vals)
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"today_new":          todayNew,
		"total_pending":      totalPending,
		"overdue":            overdue,
		"im_failed":          imFailed,
		"week_close_rate":    fmt.Sprintf("%.1f%%", closeRate),
		"median_resolve_days": fmt.Sprintf("%.1f", medianDays),
	}})
}

func medianFloat64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	// 简单冒泡排序（数据量小，够用）
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

// DeleteOldRead 删除 30 天前已读通知
func (h *NotificationHandler) DeleteOldRead(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	cutoff := time.Now().AddDate(0, 0, -30)
	result := model.DB.Where("user_id = ? AND is_read = ? AND created_at < ?", user.ID, true, cutoff).Delete(&model.Notification{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok", "deleted_count": result.RowsAffected})
}

// --- 通知规则模板管理（admin only） ---

// ListRules 通知规则列表
func (h *NotificationHandler) ListRules(c *gin.Context) {
	var rules []model.NotificationRule
	model.DB.Order("id DESC").Find(&rules)
	c.JSON(http.StatusOK, gin.H{"data": rules})
}

// CreateRule 创建通知规则
func (h *NotificationHandler) CreateRule(c *gin.Context) {
	var rule model.NotificationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Create(&rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rule})
}

// UpdateRule 更新通知规则
func (h *NotificationHandler) UpdateRule(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var rule model.NotificationRule
	if err := model.DB.First(&rule, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Model(&rule).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rule})
}

// DeleteRule 删除通知规则
func (h *NotificationHandler) DeleteRule(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := model.DB.Delete(&model.NotificationRule{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}
