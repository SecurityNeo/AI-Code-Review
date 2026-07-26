package service

import (
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// WorkdayCalculator 工作日计算器
type WorkdayCalculator struct {
	holidays map[string]bool // YYYY-MM-DD -> true = 节假日；也可反向用于调休 true = 上班
	workdays [7]bool         // 周一到周日是否上班（默认周一到周五 true，周六日 false）
}

var defaultWorkdayCalc *WorkdayCalculator

func InitWorkdayCalculator() {
	calc := &WorkdayCalculator{
		holidays: make(map[string]bool),
		workdays: [7]bool{false, true, true, true, true, true, false}, // 日一开始；日=非工作日
	}
	// 加载数据库中已配置的节假日
	var holidays []model.Holiday
	model.DB.Find(&holidays)
	for _, h := range holidays {
		// is_workday=true 表示调休上班日（周末上班），不是 holiday
		calc.holidays[h.Date] = !h.IsWorkday
	}
	defaultWorkdayCalc = calc
}

func GetWorkdayCalculator() *WorkdayCalculator {
	if defaultWorkdayCalc == nil {
		InitWorkdayCalculator()
	}
	return defaultWorkdayCalc
}

// AddWorkHours 在基础时间上增加指定的工作小时数
func (c *WorkdayCalculator) AddWorkHours(t time.Time, hours int) time.Time {
	remaining := hours
	cur := t
	for remaining > 0 {
		if c.IsWorkTime(cur) {
			cur = cur.Add(time.Hour)
			remaining--
		} else {
			cur = c.NextWorkDayStart(cur)
		}
	}
	return cur
}

// IsWorkTime 判定某个时间点是否在工作时段
// 简化模型：每天工作时段为 00:00-23:59（只要当天是工作日就算有效时间）
// 这样 "24 工作小时" = 3 个工作日，不区分上下班
func (c *WorkdayCalculator) IsWorkTime(t time.Time) bool {
	dateStr := t.Format("2006-01-02")
	// 如果是周末但配置为调休上班 → true
	// 如果是工作日但配置为节假日 → false
	if val, ok := c.holidays[dateStr]; ok {
		return !val // val=true 表示节假日，返回 false；val=false 表示调休上班，返回 true
	}
	weekday := t.Weekday() // Sunday=0, Monday=1, ..., Saturday=6
	return c.workdays[weekday]
}

// NextWorkDayStart 返回下一个工作日的起始时刻
func (c *WorkdayCalculator) NextWorkDayStart(t time.Time) time.Time {
	next := t.AddDate(0, 0, 1).Truncate(24 * time.Hour)
	for !c.IsWorkTime(next) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// WorkHoursBetween 计算两个时间点之间的工作小时数
func (c *WorkdayCalculator) WorkHoursBetween(start, end time.Time) int {
	if end.Before(start) || end.Equal(start) {
		return 0
	}
	// 上限 5000 小时避免死循环
	maxIter := 5000
	count := 0
	cur := start
	for cur.Before(end) && count < maxIter {
		if c.IsWorkTime(cur) {
			count++
		}
		cur = cur.Add(time.Hour)
	}
	return count
}

const (
	EscalationLevelNone        = 0 // 刚创建
	EscalationLevel24h         = 1 // T+24h 提醒开发者
	EscalationLevel72h         = 2 // T+72h 强提醒开发者+抄送负责人
	EscalationLevel120hSteward = 3 // T+120h 正式升级给负责人
	EscalationLevel240hAdmin   = 4 // T+240h 通知管理员
	EscalationLevel360hArchive = 5 // T+360h 自动归档
)

// EscalationService 升级路由服务
type EscalationService struct {
	calc     *WorkdayCalculator
	notifSvc *NotificationService
}

func NewEscalationService() *EscalationService {
	return &EscalationService{
		calc:     GetWorkdayCalculator(),
		notifSvc: NewNotificationService(),
	}
}

// RunDailyEscalation 每日定时运行升级检查（建议每小时执行一次）
func (s *EscalationService) RunDailyEscalation() {
	now := time.Now()
	var issues []model.ReviewIssue
	model.DB.Where("deleted_at IS NULL AND status = ?", model.IssueStatusPending).Find(&issues)

	for i := range issues {
		issue := &issues[i]
		if issue.OriginalCreatedAt == nil {
			continue
		}
		ageH := s.calc.WorkHoursBetween(*issue.OriginalCreatedAt, now)
		s.processEscalation(issue, ageH)
	}

	// 批量积压告警
	s.checkBatchAlerts()
}

func (s *EscalationService) processEscalation(issue *model.ReviewIssue, ageHours int) {
	var nextLevel int
	switch {
	case ageHours >= 360:
		nextLevel = EscalationLevel360hArchive
	case ageHours >= 240:
		nextLevel = EscalationLevel240hAdmin
	case ageHours >= 120:
		nextLevel = EscalationLevel120hSteward
	case ageHours >= 72:
		nextLevel = EscalationLevel72h
	case ageHours >= 24:
		nextLevel = EscalationLevel24h
	default:
		return
	}

	if issue.EscalationLevel >= nextLevel {
		return // 已处理过该层级
	}

	// 加载关联信息
	var task model.Task
	model.DB.First(&task, issue.TaskID)
	var project model.Project
	model.DB.First(&project, task.ProjectID)

	switch nextLevel {
	case EscalationLevel24h:
		// 提醒开发者
		if issue.OwnerID != nil {
			s.notifSvc.SendInbox(*issue.OwnerID, model.NotificationTypeIssueEscalation,
				fmt.Sprintf("Issue #%d 已超期 1 个工作日", issue.ID),
				fmt.Sprintf("项目：%s\nMR：%s\n问题：%s\n请尽快处理，2 天后将通知项目负责人。",
					project.Name, task.MRTitle, issue.Message),
				"",
			)
		}

	case EscalationLevel72h:
		// 强提醒开发者 + 抄送负责人
		if issue.OwnerID != nil {
			s.notifSvc.SendInbox(*issue.OwnerID, model.NotificationTypeIssueEscalation,
				fmt.Sprintf("Issue #%d 已超期 3 个工作日", issue.ID),
				fmt.Sprintf("项目：%s\nMR：%s\n问题：%s\n已超期 3 天，请立即处理。",
					project.Name, task.MRTitle, issue.Message),
				"",
			)
		}
		stewards := s.findStewards(task.ProjectID, issue.Category)
		for _, st := range stewards {
			s.notifSvc.SendInbox(st.UserID, model.NotificationTypeIssueEscalation,
				fmt.Sprintf("协助督促：Issue #%d", issue.ID),
				fmt.Sprintf("开发者 %s 的 Issue 已超期 3 天未处理，请协助督促。\n项目：%s\nMR：%s\n问题：%s",
					task.MRAuthor, project.Name, task.MRTitle, issue.Message),
				"",
			)
		}

	case EscalationLevel120hSteward:
		// 正式升级给项目负责人，current_owner 变更
		stewards := s.findStewards(task.ProjectID, issue.Category)
		if len(stewards) > 0 {
			steward := stewards[0]
			model.DB.Model(issue).UpdateColumn("current_owner_id", steward.UserID)
			s.notifSvc.SendInbox(steward.UserID, model.NotificationTypeIssueEscalation,
				fmt.Sprintf("Issue #%d 已升级给您处理", issue.ID),
				fmt.Sprintf("开发者 %s 的 Issue 已超期 5 天，现升级给您协助处理。\n项目：%s\nMR：%s\n问题：%s",
					task.MRAuthor, project.Name, task.MRTitle, issue.Message),
				"",
			)
		}
		if issue.OwnerID != nil {
			s.notifSvc.SendInbox(*issue.OwnerID, model.NotificationTypeIssueEscalation,
				fmt.Sprintf("Issue #%d 已升级给项目负责人", issue.ID),
				"您的 Issue 因超期未处理，已升级给项目负责人协助推进。",
				"",
			)
		}

	case EscalationLevel240hAdmin:
		// 通知管理员
		var admins []model.User
		model.DB.Where("role = ?", model.RoleAdmin).Find(&admins)
		for _, admin := range admins {
			s.notifSvc.SendInbox(admin.ID, model.NotificationTypeIssueEscalation,
				fmt.Sprintf("【全局告警】项目 %s 有 Issue 超期 10 天", project.Name),
				fmt.Sprintf("Issue #%d 已超期 10 天未闭环，请关注。\nMR：%s\n问题：%s",
					issue.ID, task.MRTitle, issue.Message),
				"",
			)
		}

	case EscalationLevel360hArchive:
		// 自动归档
		model.DB.Model(issue).Updates(map[string]interface{}{
			"status":          model.IssueStatusAutoArchived,
			"escalation_level": EscalationLevel360hArchive,
		})
		// 通知相关人员
		recipients := make(map[uint]bool)
		if issue.OwnerID != nil {
			recipients[*issue.OwnerID] = true
		}
		if issue.CurrentOwnerID != nil {
			recipients[*issue.CurrentOwnerID] = true
		}
		for uid := range recipients {
			s.notifSvc.SendInbox(uid, model.NotificationTypeAutoArchived,
				fmt.Sprintf("Issue #%d 已自动归档", issue.ID),
				fmt.Sprintf("因超期 15 个工作日未处理，系统已自动归档。\n项目：%s\nMR：%s",
					project.Name, task.MRTitle),
				"",
			)
		}
		zap.L().Info("issue auto archived", zap.Uint("issue_id", issue.ID), zap.Int("age_hours", ageHours))
		return // 归档后不再更新 escalation_level（已修改 status）
	}

	// 更新已处理层级
	model.DB.Model(issue).UpdateColumn("escalation_level", nextLevel)
}

// findStewards 查找项目环节负责人
func (s *EscalationService) findStewards(projectID uint, category string) []model.ProjectSteward {
	var stewards []model.ProjectSteward
	// 先按 category 匹配
	if category != "" {
		model.DB.Where("project_id = ? AND role_type = ?", projectID, category).
			Order("priority ASC, created_at ASC").Find(&stewards)
		if len(stewards) > 0 {
			return stewards
		}
	}
	// fallback 到 default
	model.DB.Where("project_id = ? AND role_type = ?", projectID, "default").
		Order("priority ASC, created_at ASC").Find(&stewards)
	return stewards
}

// checkBatchAlerts 项目批量积压告警
func (s *EscalationService) checkBatchAlerts() {
	type Result struct {
		ProjectID      uint
		PendingCount   int64
	}
	var results []Result
	model.DB.Raw(`
		SELECT project_id, COUNT(*) AS pending_count
		FROM review_issues
		WHERE deleted_at IS NULL AND status = ?
		GROUP BY project_id
		HAVING pending_count >= 50
	`, model.IssueStatusPending).Scan(&results)

	for _, r := range results {
		var project model.Project
		if err := model.DB.First(&project, r.ProjectID).Error; err != nil {
			continue
		}
		stewards := s.findStewards(r.ProjectID, "")
		for _, st := range stewards {
			s.notifSvc.SendInbox(st.UserID, model.NotificationTypeBatchAlert,
				fmt.Sprintf("【项目告警】%s 积压 %d 条未处理 Issue", project.Name, r.PendingCount),
				fmt.Sprintf("项目 %s 当前有 %d 条 Issue 待处理，建议关注。", project.Name, r.PendingCount),
				"",
			)
		}
		// 通知管理员
		var admins []model.User
		model.DB.Where("role = ?", model.RoleAdmin).Find(&admins)
		for _, admin := range admins {
			s.notifSvc.SendInbox(admin.ID, model.NotificationTypeBatchAlert,
				fmt.Sprintf("【全局告警】项目 %s 积压 %d 条 Issue", project.Name, r.PendingCount),
				fmt.Sprintf("项目 %s 当前有 %d 条 Issue 待处理，请关注。", project.Name, r.PendingCount),
				"",
			)
		}
	}
}

// SendDailyDigest 每日摘要（09:00 执行）
func (s *EscalationService) SendDailyDigest() {
	type UserStat struct {
		UserID       uint
		PendingCount int64
	}
	var stats []UserStat
	model.DB.Raw(`
		SELECT current_owner_id AS user_id, COUNT(*) AS pending_count
		FROM review_issues
		WHERE deleted_at IS NULL AND status = ? AND current_owner_id > 0
		GROUP BY current_owner_id
	`, model.IssueStatusPending).Scan(&stats)

	for _, st := range stats {
		s.notifSvc.SendInbox(st.UserID, model.NotificationTypeDailyDigest,
			fmt.Sprintf("今日待办：您有 %d 条 Issue 待处理", st.PendingCount),
			fmt.Sprintf("您当前有 %d 条 Issue 等待处理，请及时闭环。", st.PendingCount),
			"",
		)
	}
}
