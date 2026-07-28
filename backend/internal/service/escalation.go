package service

import (
	"encoding/json"
	"fmt"
	"strings"
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

type escalationAlert struct {
	userID uint
	level  int
	typ    string
	title  string
	items  []string
}

// EscalationService 升级路由服务
type EscalationService struct {
	calc           *WorkdayCalculator
	notifSvc       *NotificationService
	notifierSvc    *NotifierService
	alertCollector map[string]*escalationAlert // key = "userID:level"
}

func NewEscalationService() *EscalationService {
	return &EscalationService{
		calc:        GetWorkdayCalculator(),
		notifSvc:    NewNotificationService(),
		notifierSvc: NewNotifierService(),
	}
}

// EscalationStage 升级阶段配置
type EscalationStage struct {
	Level          int    `json:"level"`
	ThresholdHours int    `json:"threshold_hours"`
	Recipient      string `json:"recipient"`
	NotifyIM       bool   `json:"notify_im"`
}

func (s *EscalationService) loadEscalationConfig() []EscalationStage {
	var rules []model.NotificationRule
	model.DB.Where("`trigger` = ? AND enabled = ?", "issue.escalation", true).Find(&rules)
	for _, rule := range rules {
		if rule.EscalationConfig != "" {
			var stages []EscalationStage
			if err := json.Unmarshal([]byte(rule.EscalationConfig), &stages); err == nil && len(stages) > 0 {
				return stages
			}
		}
	}
	// fallback default
	return []EscalationStage{
		{Level: 1, ThresholdHours: 24, Recipient: "owner"},
		{Level: 2, ThresholdHours: 72, Recipient: "owner+steward"},
		{Level: 3, ThresholdHours: 120, Recipient: "steward"},
		{Level: 4, ThresholdHours: 240, Recipient: "admin"},
		{Level: 5, ThresholdHours: 360, Recipient: "auto_archive"},
	}
}

func (s *EscalationService) collectAlert(userID uint, level int, typ, title, item string) {
	key := fmt.Sprintf("%d:%d", userID, level)
	if s.alertCollector == nil {
		s.alertCollector = make(map[string]*escalationAlert)
	}
	batch, ok := s.alertCollector[key]
	if !ok {
		batch = &escalationAlert{userID: userID, level: level, typ: typ, title: title, items: make([]string, 0)}
		s.alertCollector[key] = batch
	}
	batch.items = append(batch.items, item)
}

func (s *EscalationService) flushAlerts() {
	for _, batch := range s.alertCollector {
		var content string
		if len(batch.items) == 1 {
			content = batch.items[0]
		} else {
			content = fmt.Sprintf("您有 %d 条 Issue 同时达到该升级节点：\n\n%s", len(batch.items), strings.Join(batch.items, "\n---\n"))
		}
		s.notifSvc.SendInbox(batch.userID, batch.typ, batch.title, content, "")
	}
	s.alertCollector = nil
}

// RunDailyEscalation 每日定时运行升级检查（建议每小时执行一次）
// 分页处理，避免 OOM
func (s *EscalationService) RunDailyEscalation() {
	now := time.Now()
	batchSize := 500
	offset := 0

	for {
		var issues []model.ReviewIssue
		model.DB.Where("deleted_at IS NULL AND status IN (?)", []string{model.IssueStatusPending, model.IssueStatusPendingInherited}).
			Order("id ASC").Limit(batchSize).Offset(offset).Find(&issues)
		if len(issues) == 0 {
			break
		}

		for i := range issues {
			issue := &issues[i]
			// 老数据兜底：OriginalCreatedAt 为空时用 CreatedAt 填充
			if issue.OriginalCreatedAt == nil || issue.OriginalCreatedAt.IsZero() {
				model.DB.Model(issue).UpdateColumn("original_created_at", issue.CreatedAt)
				issue.OriginalCreatedAt = &issue.CreatedAt
			}
		}

		// 预加载关联 Task 和 Project，避免 N+1
		taskMap, projectMap := s.preloadTaskProjects(issues)

		for i := range issues {
			issue := &issues[i]
			ageH := s.calc.WorkHoursBetween(*issue.OriginalCreatedAt, now)
			s.processEscalation(issue, ageH, taskMap, projectMap)
		}

		// 每批处理完后 flush 聚合通知（避免单页内重复刷屏）
		s.flushAlerts()

		if len(issues) < batchSize {
			break
		}
		offset += batchSize
	}

	// 批量积压告警
	s.checkBatchAlerts()
	
	// IM 投递失败自动管理员告警
	s.checkDeliveryFailures()
}

// preloadTaskProjects 批量预加载 Issue 列表关联的 Task 和 Project
func (s *EscalationService) preloadTaskProjects(issues []model.ReviewIssue) (map[uint]model.Task, map[uint]model.Project) {
	taskIDs := make(map[uint]struct{})
	projectIDs := make(map[uint]struct{})
	for _, issue := range issues {
		taskIDs[issue.TaskID] = struct{}{}
	}
	var tasks []model.Task
	if len(taskIDs) > 0 {
		ids := make([]uint, 0, len(taskIDs))
		for id := range taskIDs {
			ids = append(ids, id)
		}
		model.DB.Where("id IN ?", ids).Find(&tasks)
		for _, t := range tasks {
			projectIDs[t.ProjectID] = struct{}{}
		}
	}
	var projects []model.Project
	if len(projectIDs) > 0 {
		ids := make([]uint, 0, len(projectIDs))
		for id := range projectIDs {
			ids = append(ids, id)
		}
		model.DB.Where("id IN ?", ids).Find(&projects)
	}
	taskMap := make(map[uint]model.Task, len(tasks))
	for _, t := range tasks {
		taskMap[t.ID] = t
	}
	projectMap := make(map[uint]model.Project, len(projects))
	for _, p := range projects {
		projectMap[p.ID] = p
	}
	return taskMap, projectMap
}

func (s *EscalationService) processEscalation(issue *model.ReviewIssue, ageHours int, taskMap map[uint]model.Task, projectMap map[uint]model.Project) {
	stages := s.loadEscalationConfig()
	var nextLevel int
	for _, st := range stages {
		if ageHours >= st.ThresholdHours && st.Level > nextLevel {
			nextLevel = st.Level
		}
	}
	if nextLevel == 0 || issue.EscalationLevel >= nextLevel {
		return // 未达阈值或已处理过该层级
	}

	task, ok1 := taskMap[issue.TaskID]
	project, ok2 := projectMap[task.ProjectID]
	if !ok1 || !ok2 {
		return // 关联数据缺失，跳过
	}

	switch nextLevel {
	case EscalationLevel24h:
		if issue.OwnerID != nil {
			s.collectAlert(*issue.OwnerID, nextLevel, model.NotificationTypeIssueEscalation,
				"Issue 超期提醒（1 个工作日）",
				fmt.Sprintf("Issue #%d 已超期 1 个工作日\n项目：%s\nMR：%s\n问题：%s\n请尽快处理，2 天后将通知项目负责人。",
					issue.ID, project.Name, task.MRTitle, issue.Message))
		}

	case EscalationLevel72h:
		stewards := s.findStewards(task.ProjectID, issue.Category, project.Language)
		if issue.OwnerID != nil {
			s.collectAlert(*issue.OwnerID, nextLevel, model.NotificationTypeIssueEscalation,
				"Issue 超期提醒（3 个工作日）",
				fmt.Sprintf("Issue #%d 已超期 3 个工作日\n项目：%s\nMR：%s\n问题：%s\n已超期 3 天，请立即处理。",
					issue.ID, project.Name, task.MRTitle, issue.Message))
		}
		for _, st := range stewards {
			s.collectAlert(st.MemberID, nextLevel, model.NotificationTypeIssueEscalation,
				"Issue 协助督促（3 个工作日）",
				fmt.Sprintf("协助督促：Issue #%d\n开发者 %s 的 Issue 已超期 3 天未处理，请协助督促。\n项目：%s\nMR：%s\n问题：%s",
					issue.ID, task.MRAuthor, project.Name, task.MRTitle, issue.Message))
		}
		s.sendEscalationIM(project, issue, task, "72h", stewards)

	case EscalationLevel120hSteward:
		stewards := s.findStewards(task.ProjectID, issue.Category, project.Language)
		if len(stewards) > 0 {
			model.DB.Model(issue).UpdateColumn("current_owner_id", stewards[0].MemberID)
			for _, st := range stewards {
				s.collectAlert(st.MemberID, nextLevel, model.NotificationTypeIssueEscalation,
					"Issue 正式升级（5 个工作日）",
					fmt.Sprintf("Issue #%d 已升级给您处理\n开发者 %s 的 Issue 已超期 5 天，现升级给您协助处理。\n项目：%s\nMR：%s\n问题：%s",
						issue.ID, task.MRAuthor, project.Name, task.MRTitle, issue.Message))
			}
			s.sendEscalationIM(project, issue, task, "120h", stewards)
		}
		if issue.OwnerID != nil {
			s.collectAlert(*issue.OwnerID, nextLevel, model.NotificationTypeIssueEscalation,
				"Issue 已升级给项目负责人",
				fmt.Sprintf("Issue #%d 已升级给项目负责人\n您的 Issue 因超期未处理，已升级给项目负责人协助推进。", issue.ID))
		}

	case EscalationLevel240hAdmin:
		var admins []model.User
		model.DB.Where("role = ?", model.RoleAdmin).Find(&admins)
		for _, admin := range admins {
			s.collectAlert(admin.ID, nextLevel, model.NotificationTypeIssueEscalation,
				"【全局告警】Issue 超期 10 天",
				fmt.Sprintf("【全局告警】项目 %s 有 Issue 超期 10 天\nIssue #%d 已超期 10 天未闭环，请关注。\nMR：%s\n问题：%s",
					project.Name, issue.ID, task.MRTitle, issue.Message))
		}

	case EscalationLevel360hArchive:
		model.DB.Model(issue).Updates(map[string]interface{}{
			"status":           model.IssueStatusAutoArchived,
			"escalation_level": EscalationLevel360hArchive,
		})
		recipients := make(map[uint]bool)
		if issue.OwnerID != nil {
			recipients[*issue.OwnerID] = true
		}
		if issue.CurrentOwnerID != nil {
			recipients[*issue.CurrentOwnerID] = true
		}
		for uid := range recipients {
			s.collectAlert(uid, nextLevel, model.NotificationTypeAutoArchived,
				"Issue 已自动归档",
				fmt.Sprintf("Issue #%d 已自动归档\n因超期 15 个工作日未处理，系统已自动归档。\n项目：%s\nMR：%s",
					issue.ID, project.Name, task.MRTitle))
		}
		zap.L().Info("issue auto archived", zap.Uint("issue_id", issue.ID), zap.Int("age_hours", ageHours))
		return // 归档后不再更新 escalation_level
	}

	// 更新已处理层级
	model.DB.Model(issue).UpdateColumn("escalation_level", nextLevel)
}

// findStewards 查找项目指定维度的负责人（category → language → default）
func (s *EscalationService) findStewards(projectID uint, category, language string) []model.ProjectResponsibility {
	var responsibilities []model.ProjectResponsibility
	db := model.DB.Preload("Member").Where("project_id = ?", projectID)

	// 1. 按 category 匹配
	if category != "" {
		if err := db.Where("scope_type = 'rule_category' AND scope_value = ?", category).
			Order("priority ASC, created_at ASC").
			Find(&responsibilities).Error; err == nil && len(responsibilities) > 0 {
			return responsibilities
		}
	}
	// 2. 按 language 匹配
	if language != "" {
		if err := db.Where("scope_type = 'language' AND scope_value = ?", language).
			Order("priority ASC, created_at ASC").
			Find(&responsibilities).Error; err == nil && len(responsibilities) > 0 {
			return responsibilities
		}
	}
	// 3. fallback 到 default
	if err := db.Where("scope_type = 'default' AND scope_value = ''").
		Order("priority ASC, created_at ASC").
		Find(&responsibilities).Error; err != nil {
		zap.L().Error("findStewards fallback query failed", zap.Error(err))
	}
	return responsibilities
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
		stewards := s.findStewards(r.ProjectID, "", "")
		for _, st := range stewards {
			s.notifSvc.SendInbox(st.MemberID, model.NotificationTypeBatchAlert,
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
		WHERE deleted_at IS NULL AND status IN (?) AND current_owner_id > 0
		GROUP BY current_owner_id
	`, []string{model.IssueStatusPending, model.IssueStatusPendingInherited}).Scan(&stats)

	for _, st := range stats {
		s.notifSvc.SendInbox(st.UserID, model.NotificationTypeDailyDigest,
			fmt.Sprintf("今日待办：您有 %d 条 Issue 待处理", st.PendingCount),
			fmt.Sprintf("您当前有 %d 条 Issue 等待处理，请及时闭环。", st.PendingCount),
			"",
		)
	}
}

// sendEscalationIM 发送升级 IM 消息到项目群
func (s *EscalationService) sendEscalationIM(project model.Project, issue *model.ReviewIssue, task model.Task, stage string, stewards []model.ProjectResponsibility) {
	// 查找项目相关的 WeCom Notifier
	var notifiers []model.WeComNotifier
	db := model.DB.Where("enabled = ?", true)
	if project.ID > 0 {
		db = db.Where("project_id IS NULL OR project_id = ?", project.ID)
	} else {
		db = db.Where("project_id IS NULL")
	}
	db.Find(&notifiers)
	if len(notifiers) == 0 {
		return
	}

	// 构建 @ 列表（stewards 已预加载 Member，直接取 IMUserID）
	var mentions []string
	for _, st := range stewards {
		if st.Member.IMUserID != "" {
			mentions = append(mentions, fmt.Sprintf("<@%s>", st.Member.IMUserID))
		}
	}
	mentionStr := ""
	if len(mentions) > 0 {
		mentionStr = "\n" + strings.Join(mentions, " ")
	}

	var msg string
	templateStr := GetNotificationRuleTemplate("issue.escalation")
	if templateStr != "" {
		stats := CalcIssueStats(task.ID, task.MRMergeID)
		ctx := TemplateContext{
			Task:        task,
			Stats:       stats,
			Project:     project,
			Developer:   task.MRAuthor,
			AtRecipient: strings.Join(mentions, " "),
		}
		msg = RenderMessage(templateStr, ctx)
	} else {
		var stageText string
		switch stage {
		case "72h":
			stageText = "已超期 3 个工作日"
		case "120h":
			stageText = "已超期 5 个工作日，正式升级"
		default:
			stageText = "升级提醒"
		}
		msg = fmt.Sprintf("**⚠️ Issue 升级告警**\n\n**项目：** %s\n**MR：** !%d %s\n**开发者：** %s\n**问题：** %s\n**状态：** %s\n\n> 请在 2 个工作日内处理，否则将自动归档。%s",
			project.Name, task.MRMergeID, task.MRTitle, task.MRAuthor, issue.Message, stageText, mentionStr)
	}

	for _, notifier := range notifiers {
		ok, detail, err := s.notifierSvc.SendMessage(notifier.WebhookUrl, msg, "")
		log := model.NotificationDeliveryLog{
			NotifierID:     &notifier.ID,
			TaskID:         &task.ID,
			Channel:        "wecom",
			RecipientType:  "steward",
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
}

// checkDeliveryFailures 检查当日 IM 投递失败并告警管理员
func (s *EscalationService) checkDeliveryFailures() {
	var failedCount int64
	todayStart := time.Now().Truncate(24 * time.Hour)
	model.DB.Model(&model.NotificationDeliveryLog{}).
		Where("status = ? AND created_at >= ?", "failed", todayStart).
		Count(&failedCount)
	if failedCount == 0 {
		return
	}
	var admins []model.User
	model.DB.Where("role = ?", model.RoleAdmin).Find(&admins)
	for _, admin := range admins {
		s.notifSvc.SendInbox(admin.ID, model.NotificationTypeBatchAlert,
			fmt.Sprintf("【系统告警】今日 IM 投递失败 %d 次", failedCount),
			fmt.Sprintf("今日企微/IM 消息投递失败 %d 次，请检查 Webhook 配置或网络状态。", failedCount),
			"",
		)
	}
}
