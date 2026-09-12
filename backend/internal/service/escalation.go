package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
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

// EffectiveWorkHoursBetween 计算两个时间点之间的有效工作小时数
// 在 WorkHoursBetween 的基础上额外排除每日免打扰时段
func (c *WorkdayCalculator) EffectiveWorkHoursBetween(start, end time.Time, quietStart, quietEnd string) int {
	if end.Before(start) || end.Equal(start) {
		return 0
	}
	maxIter := 5000
	count := 0
	cur := start
	for cur.Before(end) && count < maxIter {
		if c.IsWorkTime(cur) && !isTimeInQuietHours(cur, quietStart, quietEnd) {
			count++
		}
		cur = cur.Add(time.Hour)
	}
	return count
}

// isTimeInQuietHours 判断指定时间点是否在免打扰时段内（支持跨天，如 22:00-09:00）
func isTimeInQuietHours(t time.Time, quietStart, quietEnd string) bool {
	if quietStart == "" || quietEnd == "" {
		return false
	}
	start, err1 := time.Parse("15:04", quietStart)
	end, err2 := time.Parse("15:04", quietEnd)
	if err1 != nil || err2 != nil {
		return false
	}
	nowMin := t.Hour()*60 + t.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()

	if startMin < endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	return nowMin >= startMin || nowMin < endMin
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
	orgID  uint
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
	imCollector    map[string]*imBatch         // key = "taskID:stage"
	quietStart     string                      // 免打扰开始时间 HH:MM
	quietEnd       string                      // 免打扰结束时间 HH:MM
}

// imBatch IM 聚合批次
type imBatch struct {
	project        model.Project
	task           model.Task
	stage          string
	level          int
	thresholdHours int       // 当前阶段的阈值小时数，用于动态渲染时间描述
	mentions       []string  // "<@im_user_id>" 格式
	issues         []imIssue // 结构化 Issue 列表
	imTemplate     string    // IM 模板（空=使用默认硬编码）
}

type imTarget struct {
	project        model.Project
	task           model.Task
	stage          string
	level          int
	thresholdHours int
	mentions       []string
	imTemplate     string
}

type imIssue struct {
	ID      uint
	Message string
}

// NewEscalationService 创建升级服务
func NewEscalationService() *EscalationService {
	return &EscalationService{
		calc:        GetWorkdayCalculator(),
		notifSvc:    NewNotificationService(),
		notifierSvc: NewNotifierService(),
	}
}

// formatDuration 将小时数转为中文时间描述
func formatDuration(hours int) string {
	if hours < 24 {
		return fmt.Sprintf("%d 小时", hours)
	}
	days := hours / 24
	rem := hours % 24
	if rem == 0 {
		return fmt.Sprintf("%d 天", days)
	}
	return fmt.Sprintf("%d 天 %d 小时", days, rem)
}

// EscalationStage 升级阶段配置
type EscalationStage struct {
	Level          int    `json:"level"`
	ThresholdHours int    `json:"threshold_hours"`
	Recipient      string `json:"recipient"`
	NotifyIM       bool   `json:"notify_im"`
	IMTemplate     string `json:"im_template"` // IM 消息模板（Markdown），空则使用默认文案
}

func (s *EscalationService) loadEscalationConfig() []EscalationStage {
	// 兼容旧调用：默认加载根组织配置
	return s.loadEscalationConfigForOrg(1)
}

func (s *EscalationService) loadEscalationConfigForOrg(orgID uint) []EscalationStage {
	var rules []model.NotificationRule
	model.DB.Where("`trigger` = ? AND enabled = ? AND org_id = ?", "issue.escalation", true, orgID).Find(&rules)

	// 默认清空免打扰时段，避免旧规则禁用后 quiet hours 残留
	s.quietStart = ""
	s.quietEnd = ""

	if len(rules) > 0 {
		rule := rules[0]
		s.quietStart = rule.QuietHoursStart
		s.quietEnd = rule.QuietHoursEnd
		if rule.EscalationConfig != "" && rule.EscalationConfig != "{}" {
			var stages []EscalationStage
			if err := json.Unmarshal([]byte(rule.EscalationConfig), &stages); err == nil && len(stages) > 0 {
				// 防御：旧数据可能没有 level / im_template，使用索引+1 兜底
				for i := range stages {
					if stages[i].Level == 0 {
						stages[i].Level = i + 1
					}
				}
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

// getNotificationBaseline 读取通知规则生效起点
func (s *EscalationService) getNotificationBaseline() time.Time {
	var setting model.NotificationGlobalSetting
	if err := model.SilentFirst(model.DB, &setting, 1); err != nil {
		return time.Time{}
	}
	if setting.NotificationBaselineAt == nil {
		return time.Time{}
	}
	return *setting.NotificationBaselineAt
}

// applyNotificationBaseline 将生效起点过滤条件应用到查询
func (s *EscalationService) applyNotificationBaseline(query *gorm.DB) *gorm.DB {
	baseline := s.getNotificationBaseline()
	if baseline.IsZero() {
		return query
	}
	return query.Where("original_created_at >= ?", baseline)
}

func (s *EscalationService) collectAlert(userID, orgID uint, level int, typ, title, item string) {
	key := fmt.Sprintf("%d:%d", userID, level)
	if s.alertCollector == nil {
		s.alertCollector = make(map[string]*escalationAlert)
	}
	batch, ok := s.alertCollector[key]
	if !ok {
		batch = &escalationAlert{userID: userID, orgID: orgID, level: level, typ: typ, title: title, items: make([]string, 0)}
		s.alertCollector[key] = batch
	}
	batch.items = append(batch.items, item)
}

func (s *EscalationService) flushAlerts() {
	if s.isQuietHours() {
		zap.L().Debug("flushAlerts: in quiet hours, skipping alert send",
			zap.Int("batch_count", len(s.alertCollector)))
		s.alertCollector = nil
		return
	}
	for _, batch := range s.alertCollector {
		var content string
		if len(batch.items) == 1 {
			content = batch.items[0]
		} else {
			content = fmt.Sprintf("您有 %d 条 Issue 同时达到该升级节点：\n\n%s", len(batch.items), strings.Join(batch.items, "\n---\n"))
		}
		s.notifSvc.SendInbox(batch.orgID, batch.userID, batch.typ, batch.title, content, "")
	}
	s.alertCollector = nil
}

// RunDailyEscalation 每日定时运行升级检查（建议每小时执行一次）
// 多租户改造：接受 scope，若 scope 指定了 org，则只处理该 org；否则处理所有活跃 org
// 分页处理，避免 OOM
func (s *EscalationService) RunDailyEscalation(scope *model.UserAuthScope) {
	now := time.Now()
	zap.L().Debug("RunDailyEscalation started", zap.Time("now", now), zap.Uint("org_id", scope.CurrentOrgID))
	defer func() {
		if r := recover(); r != nil {
			zap.L().Error("RunDailyEscalation panic recovered", zap.Any("recover", r))
		}
	}()

	if scope != nil && scope.CurrentOrgID > 0 {
		s.runEscalationForOrg(scope.CurrentOrgID, now)
		zap.L().Debug("RunDailyEscalation completed for org", zap.Uint("org_id", scope.CurrentOrgID))
		return
	}

	// 兼容旧调用：未传 scope 时遍历所有活跃组织
	var orgs []model.Organization
	model.DB.Where("status = ?", "active").Find(&orgs)
	if len(orgs) == 0 {
		// fallback: 至少处理根组织
		orgs = append(orgs, model.Organization{ID: 1, Name: "Root Organization"})
	}
	for _, org := range orgs {
		s.runEscalationForOrg(org.ID, now)
	}

		zap.L().Debug("RunDailyEscalation completed", zap.Int("org_count", len(orgs)))
}

// runEscalationForOrg 为指定组织运行升级检查
func (s *EscalationService) runEscalationForOrg(orgID uint, now time.Time) {
	zap.L().Debug("runEscalationForOrg", zap.Uint("org_id", orgID))

	// 1. 加载该组织的升级配置
	stages := s.loadEscalationConfigForOrg(orgID)

	// 2. 冻结期入口拦截
	if s.shouldFreezeExecution() {
		zap.L().Debug("runEscalationForOrg skipped: in frozen period", zap.Uint("org_id", orgID))
		return
	}

	batchSize := 500
	offset := 0

	for {
		var issues []model.ReviewIssue
		query := model.DB.Where("deleted_at IS NULL AND status IN (?) AND org_id = ?",
			[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, orgID)
		query = s.applyNotificationBaseline(query)
		query.Order("id ASC").Limit(batchSize).Offset(offset).Find(&issues)
		if len(issues) == 0 {
			break
		}

		for i := range issues {
			issue := &issues[i]
			if issue.OriginalCreatedAt == nil || issue.OriginalCreatedAt.IsZero() {
				model.DB.Model(issue).UpdateColumn("original_created_at", issue.CreatedAt)
				issue.OriginalCreatedAt = &issue.CreatedAt
			}
		}

		taskMap, projectMap := s.preloadTaskProjects(issues)

		for i := range issues {
			issue := &issues[i]
			ageH := s.calc.EffectiveWorkHoursBetween(*issue.OriginalCreatedAt, now, s.quietStart, s.quietEnd)
			s.processEscalation(issue, ageH, stages, taskMap, projectMap)
		}

		s.flushAlerts()
		s.flushIMs()

		if len(issues) < batchSize {
			break
		}
		offset += batchSize
	}

	s.checkBatchAlertsForOrg(orgID)
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

func (s *EscalationService) processEscalation(issue *model.ReviewIssue, ageHours int, stages []EscalationStage, taskMap map[uint]model.Task, projectMap map[uint]model.Project) {
	// 1. 计算应到达的最高 level，并构建 level→stage 映射
	maxLevel := 0
	stageMap := make(map[int]EscalationStage)
	for _, st := range stages {
		stageMap[st.Level] = st
		if ageHours >= st.ThresholdHours && st.Level > maxLevel {
			maxLevel = st.Level
		}
	}
	if maxLevel == 0 || issue.EscalationLevel >= maxLevel {
		return // 未达阈值或已全部处理
	}

	task, ok1 := taskMap[issue.TaskID]
	project, ok2 := projectMap[task.ProjectID]
	if !ok1 || !ok2 {
		return // 关联数据缺失，跳过
	}

	// DEBUG: log the stage map keys
	var stageKeys []int
	for k := range stageMap {
		stageKeys = append(stageKeys, k)
	}
	zap.L().Debug("processEscalation debug",
		zap.Uint("issue_id", issue.ID),
		zap.Int("age_hours", ageHours),
		zap.Int("escalation_level", issue.EscalationLevel),
		zap.Int("max_level", maxLevel),
		zap.Ints("stage_keys", stageKeys))

	// 2. 逐层补齐：从 issue.EscalationLevel+1 到 maxLevel，每层都执行一次
	for level := issue.EscalationLevel + 1; level <= maxLevel; level++ {
		stage, ok := stageMap[level]
		if !ok {
			zap.L().Debug("processEscalation: stage not found for level",
				zap.Uint("issue_id", issue.ID),
				zap.Int("level", level))
			continue // 该 level 未配置，跳过
		}

		zap.L().Debug("processEscalation: processing level",
			zap.Uint("issue_id", issue.ID),
			zap.Int("level", level),
			zap.Int("threshold", stage.ThresholdHours),
			zap.Bool("notify_im", stage.NotifyIM),
			zap.Int("im_template_len", len(stage.IMTemplate)))

		switch level {
		case EscalationLevel24h:
			if issue.OwnerID != nil {
				dur := formatDuration(stage.ThresholdHours)
				s.collectAlert(*issue.OwnerID, project.OrgID, level, model.NotificationTypeIssueEscalation,
					"Issue 超期提醒（已超期 "+dur+"）",
					fmt.Sprintf("Issue #%d 已超期 %s\n项目：%s\nMR：%s\n问题：%s\n请尽快处理，否则将进一步升级。",
						issue.ID, dur, project.Name, task.MRTitle, issue.Message))
			}

		case EscalationLevel72h:
			stewards := s.findStewards(task.ProjectID, issue.Category, project.Language)
			zap.L().Debug("processEscalation Level2 findStewards",
				zap.Uint("issue_id", issue.ID),
				zap.Int("steward_count", len(stewards)))
			if issue.OwnerID != nil {
				dur := formatDuration(stage.ThresholdHours)
				s.collectAlert(*issue.OwnerID, project.OrgID, level, model.NotificationTypeIssueEscalation,
					"Issue 超期提醒（已超期 "+dur+"）",
					fmt.Sprintf("Issue #%d 已超期 %s\n项目：%s\nMR：%s\n问题：%s\n请立即处理，否则将进一步升级。",
						issue.ID, dur, project.Name, task.MRTitle, issue.Message))
			}
			// 收集有 IM 的 steward 用于兜底通知（即使映射失败也能通过 IM 通知到）
			var imMentions []string
			if len(stewards) > 0 {
				// 更新 current_owner_id，让 steward 能在 Dashboard 看到
				primaryUserID := stewards[0].UserID
				if primaryUserID > 0 {
					model.DB.Model(issue).UpdateColumn("current_owner_id", primaryUserID)
					issue.CurrentOwnerID = &primaryUserID
				}
				for _, st := range stewards {
					notifyUserID := st.UserID
					if notifyUserID == 0 {
						zap.L().Debug("escalation L2: skip steward alert, no mapped user id",
							zap.Uint("user_id", st.UserID),
							zap.String("gitlab_username", st.User.GitlabUsername))
					} else {
						s.collectAlert(notifyUserID, project.OrgID, level, model.NotificationTypeIssueEscalation,
							"Issue 协助督促（已超期 "+formatDuration(stage.ThresholdHours)+"）",
							fmt.Sprintf("协助督促：Issue #%d\n任务ID：%d\n项目：%s\nMR：%s\n开发者：%s\n问题：%s\n请协助督促处理。",
								issue.ID, task.ID, project.Name, task.MRTitle, task.MRAuthor, issue.Message))
					}
					// 收集 IMUserID 用于兜底（IM 链路不依赖 users.id）
					if st.User.IMUserID != "" {
						imMentions = append(imMentions, fmt.Sprintf("<@%s>", st.User.IMUserID))
					}
				}
				// 最后发送一条 IM 兜底通知（包含所有 steward 的 @mention）
				if len(imMentions) > 0 && stage.NotifyIM {
					s.collectIM(imTarget{
						project:        project,
						task:           task,
						stage:          "72h",
						level:          level,
						thresholdHours: stage.ThresholdHours,
						mentions:       imMentions,
						imTemplate:     stage.IMTemplate,
					}, issue.ID, issue.Message)
				}
			}

		case EscalationLevel120hSteward:
			stewards := s.findStewards(task.ProjectID, issue.Category, project.Language)
			zap.L().Debug("processEscalation Level3 findStewards",
				zap.Uint("issue_id", issue.ID),
				zap.Int("steward_count", len(stewards)),
				zap.String("category", issue.Category),
				zap.String("language", project.Language))
			if len(stewards) > 0 {
				primaryUserID := stewards[0].UserID
				if primaryUserID > 0 {
					model.DB.Model(issue).UpdateColumn("current_owner_id", primaryUserID)
					// 同步更新内存指针，确保后续归档通知能发给新责任人
					issue.CurrentOwnerID = &primaryUserID
				}
				dur := formatDuration(stage.ThresholdHours)
				for _, st := range stewards {
					notifyUserID := st.UserID
					if notifyUserID == 0 {
						zap.L().Debug("escalation L3: skip steward alert, no mapped user id",
							zap.Uint("user_id", st.UserID),
							zap.String("gitlab_username", st.User.GitlabUsername))
						continue
					}
					s.collectAlert(notifyUserID, project.OrgID, level, model.NotificationTypeIssueEscalation,
						"Issue 正式升级（已超期 "+dur+"）",
						fmt.Sprintf("Issue #%d 已升级给您处理\n任务ID：%d\n项目：%s\nMR：%s\n开发者：%s\n问题：%s\n请尽快协助处理。",
							issue.ID, task.ID, project.Name, task.MRTitle, task.MRAuthor, issue.Message))
				}
				if stage.NotifyIM {
					mentions := buildStewardMentions(stewards)
					zap.L().Debug("processEscalation Level3 collectIM",
						zap.Uint("issue_id", issue.ID),
						zap.Int("mention_count", len(mentions)),
						zap.Strings("mentions", mentions),
						zap.Bool("im_template_empty", stage.IMTemplate == ""))
					s.collectIM(imTarget{
						project:        project,
						task:           task,
						stage:          "120h",
						level:          level,
						thresholdHours: stage.ThresholdHours,
						mentions:       mentions,
						imTemplate:     stage.IMTemplate,
					}, issue.ID, issue.Message)
				}
			} else {
				zap.L().Debug("processEscalation Level3: no stewards found, skipping IM",
					zap.Uint("issue_id", issue.ID),
					zap.Uint("project_id", task.ProjectID),
					zap.String("category", issue.Category))
			}
			if issue.OwnerID != nil {
				dur := formatDuration(stage.ThresholdHours)
				s.collectAlert(*issue.OwnerID, project.OrgID, level, model.NotificationTypeIssueEscalation,
					"Issue 已升级给项目负责人",
					fmt.Sprintf("Issue #%d 已升级给项目负责人\n您的 Issue 因超期 %s 未处理，已升级给项目负责人协助推进。", issue.ID, dur))
			}

		case EscalationLevel240hAdmin:
			// 多租户改造：users 表没有 org_id，通过 org_users 查询管理员
			var adminUserIDs []uint
			model.DB.Model(&model.OrgUser{}).
				Select("user_id").
				Where("org_id = ? AND role IN (?)", project.OrgID, []string{"org_admin", "super_admin"}).
				Pluck("user_id", &adminUserIDs)
			var admins []model.User
			if len(adminUserIDs) > 0 {
				model.DB.Where("id IN ?", adminUserIDs).Find(&admins)
			}
			dur := formatDuration(stage.ThresholdHours)
			for _, admin := range admins {
				s.collectAlert(admin.ID, project.OrgID, level, model.NotificationTypeIssueEscalation,
					"【全局告警】Issue 超期 "+dur,
					fmt.Sprintf("【全局告警】项目 %s 有 Issue 超期 %s 未闭环，请关注。\nIssue #%d\nMR：%s\n问题：%s",
						project.Name, dur, issue.ID, task.MRTitle, issue.Message))
			}
			if stage.NotifyIM {
				mentions := s.buildAdminMentions(admins)
				zap.L().Debug("processEscalation Level4 collectIM",
					zap.Uint("issue_id", issue.ID),
					zap.Int("mention_count", len(mentions)),
					zap.Strings("mentions", mentions))
				s.collectIM(imTarget{
					project:        project,
					task:           task,
					stage:          "240h",
					level:          level,
					thresholdHours: stage.ThresholdHours,
					mentions:       mentions,
					imTemplate:     stage.IMTemplate,
				}, issue.ID, issue.Message)
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
			dur := formatDuration(stage.ThresholdHours)
			for uid := range recipients {
				s.collectAlert(uid, project.OrgID, level, model.NotificationTypeAutoArchived,
					"Issue 已自动归档",
					fmt.Sprintf("Issue #%d 已自动归档\n因超期 %s 未处理，系统已自动归档。\n项目：%s\nMR：%s",
						issue.ID, dur, project.Name, task.MRTitle))
			}
			var admins []model.User
			model.DB.Joins("JOIN org_users ON users.id = org_users.user_id").
				Where("org_users.org_id = ? AND org_users.role IN ? AND org_users.status = 'active' AND users.enabled = ?", project.OrgID, []string{"super_admin", "org_admin"}, true).
				Find(&admins)
			for _, admin := range admins {
				s.collectAlert(admin.ID, project.OrgID, level, model.NotificationTypeAutoArchived,
					"【全局通知】Issue 自动归档",
					fmt.Sprintf("【全局通知】项目 %s 有 Issue 已自动归档\nIssue #%d 因超期 %s 未处理，系统已自动归档。\nMR：%s",
						project.Name, issue.ID, dur, task.MRTitle))
			}
			zap.L().Debug("issue auto archived", zap.Uint("issue_id", issue.ID), zap.Int("age_hours", ageHours))
			return // 归档后直接退出，不再更新 escalation_level（已在上面 update 中设置）
		}
	}

	// 3. 统一更新已处理层级到最高 level
	model.DB.Model(issue).Updates(map[string]interface{}{"escalation_level": maxLevel})
}

// findStewards 查找项目指定维度的负责人（category → language → default）
// ⚠️ 关键修复：每个 scope 查询必须使用独立的 db chain，不能复用同一个 *gorm.DB 变量，
//    否则 GORM 的 Where 条件会叠加（如 scope_type='rc' AND scope_type='default'），导致 fallback 永远返回 0 行。
// ⚠️ 返回结果已过滤掉 users.enabled=false 的记录，避免已禁用人员被分配 Issue 或收到通知。
func (s *EscalationService) findStewards(projectID uint, category, language string) []model.ProjectResponsibility {
	zap.L().Debug("findStewards called",
		zap.Uint("project_id", projectID),
		zap.String("category", category),
		zap.String("language", language))

	// 1. 按 category 匹配
	if category != "" {
		var catRes []model.ProjectResponsibility
		if err := model.DB.Preload("User").Where("project_id = ? AND scope_type = 'rule_category' AND scope_value = ?", projectID, category).
			Order("priority ASC, created_at ASC").
			Find(&catRes).Error; err == nil && len(catRes) > 0 {
			if active := filterActiveResponsibilities(catRes); len(active) > 0 {
				zap.L().Debug("findStewards: matched by category",
					zap.Uint("project_id", projectID),
					zap.String("category", category),
					zap.Int("count", len(active)))
				return active
			}
		}
		zap.L().Debug("findStewards: no active category match",
			zap.Uint("project_id", projectID),
			zap.String("category", category))
	}
	// 2. 按 language 匹配
	if language != "" {
		var langRes []model.ProjectResponsibility
		normalizedLang := normalizeLanguage(language)
		if err := model.DB.Preload("User").Where("project_id = ? AND scope_type = 'language' AND scope_value = ?", projectID, normalizedLang).
			Order("priority ASC, created_at ASC").
			Find(&langRes).Error; err == nil && len(langRes) > 0 {
			if active := filterActiveResponsibilities(langRes); len(active) > 0 {
				zap.L().Debug("findStewards: matched by language",
					zap.Uint("project_id", projectID),
					zap.String("language", normalizedLang),
					zap.Int("count", len(active)))
				return active
			}
		}
		zap.L().Debug("findStewards: no active language match",
			zap.Uint("project_id", projectID),
			zap.String("language", normalizedLang))
	}
	// 3. fallback 到 default
	var defRes []model.ProjectResponsibility
	if err := model.DB.Preload("User").Where("project_id = ? AND scope_type = 'default' AND scope_value = ''", projectID).
		Order("priority ASC, created_at ASC").
		Find(&defRes).Error; err != nil {
		zap.L().Error("findStewards fallback query failed", zap.Error(err))
	}
	active := filterActiveResponsibilities(defRes)
	zap.L().Debug("findStewards: fallback result",
		zap.Uint("project_id", projectID),
		zap.Int("total", len(defRes)),
		zap.Int("active", len(active)))
	return active
}

// buildStewardMentions 从 ProjectResponsibility 构建 @ 列表
func buildStewardMentions(stewards []model.ProjectResponsibility) []string {
	var mentions []string
	for _, st := range stewards {
		if st.User.IMUserID != "" {
			mentions = append(mentions, fmt.Sprintf("<@%s>", st.User.IMUserID))
		}
	}
	return mentions
}

// buildAdminMentions 从 User 列表构建 @ 列表
func (s *EscalationService) buildAdminMentions(admins []model.User) []string {
	var mentions []string
	for _, admin := range admins {
		if admin.IMUserID != "" {
			mentions = append(mentions, fmt.Sprintf("<@%s>", admin.IMUserID))
		}
	}
	return mentions
}

// checkBatchAlerts 项目批量积压告警
func (s *EscalationService) checkBatchAlerts() {
	// 兼容旧调用：遍历所有组织
	var orgs []model.Organization
	model.DB.Where("status = ?", "active").Find(&orgs)
	for _, org := range orgs {
		s.checkBatchAlertsForOrg(org.ID)
	}
}

func (s *EscalationService) checkBatchAlertsForOrg(orgID uint) {
	// 原有的 checkBatchAlerts 逻辑（按组织过滤后执行）

	type Result struct {
		ProjectID    uint
		PendingCount int64
	}
	var results []Result
	model.DB.Raw(`
		SELECT t.project_id, COUNT(ri.id) AS pending_count
		FROM review_issues ri
		INNER JOIN tasks t ON t.id = ri.task_id
		WHERE ri.deleted_at IS NULL AND ri.status = ? AND t.org_id = ?
		GROUP BY t.project_id
		HAVING pending_count >= 50
	`, model.IssueStatusPending, orgID).Scan(&results)

	for _, r := range results {
		var project model.Project
		if err := model.DB.Where("org_id = ?", orgID).First(&project, r.ProjectID).Error; err != nil {
			continue
		}
		stewards := s.findStewards(r.ProjectID, "", "")
		for _, st := range stewards {
			notifyUserID := st.UserID
			if notifyUserID == 0 {
				continue
			}
			s.notifSvc.SendInbox(orgID, notifyUserID, model.NotificationTypeBatchAlert,
				fmt.Sprintf("【项目告警】%s 积压 %d 条未处理 Issue", project.Name, r.PendingCount),
				fmt.Sprintf("项目 %s 当前有 %d 条 Issue 待处理，建议关注。", project.Name, r.PendingCount),
				"",
			)
		}
		// 通知管理员
		var admins []model.User
		model.DB.Joins("JOIN org_users ON users.id = org_users.user_id").
			Where("org_users.org_id = ? AND org_users.role IN ? AND org_users.status = 'active' AND users.enabled = ?", orgID, []string{"super_admin", "org_admin"}, true).
			Find(&admins)
		for _, admin := range admins {
			s.notifSvc.SendInbox(orgID, admin.ID, model.NotificationTypeBatchAlert,
				fmt.Sprintf("【全局告警】项目 %s 积压 %d 条 Issue", project.Name, r.PendingCount),
				fmt.Sprintf("项目 %s 当前有 %d 条 Issue 待处理，请关注。", project.Name, r.PendingCount),
				"",
			)
		}
	}
}

// SendDailyDigest 每日摘要（09:00 执行）
// 多租户改造：接受 scope，若 scope 指定了 org，则只处理该 org；否则处理所有活跃 org
func (s *EscalationService) SendDailyDigest(scope *model.UserAuthScope) {
	zap.L().Debug("SendDailyDigest started", zap.Time("now", time.Now()), zap.Uint("org_id", scope.CurrentOrgID))
	defer func() {
		if r := recover(); r != nil {
			zap.L().Error("SendDailyDigest panic recovered", zap.Any("recover", r))
		}
	}()

	if scope != nil && scope.CurrentOrgID > 0 {
		s.sendDailyDigestForOrg(scope.CurrentOrgID)
		return
	}

	// 兼容旧调用：未传 scope 时遍历所有活跃组织
	var orgs []model.Organization
	model.DB.Where("status = ?", "active").Find(&orgs)
	for _, org := range orgs {
		s.sendDailyDigestForOrg(org.ID)
	}
}

func (s *EscalationService) sendDailyDigestForOrg(orgID uint) {
	// 加载该组织的配置以获取 quiet hours，避免跨组织配置污染
	_ = s.loadEscalationConfigForOrg(orgID)

	if s.shouldFreezeExecution() {
		zap.L().Debug("SendDailyDigest skipped: in frozen period", zap.Uint("org_id", orgID))
		return
	}

	// 加载通知规则生效起点
	baseline := s.getNotificationBaseline()

	type UserStat struct {
		UserID       uint
		PendingCount int64
	}
	var stats []UserStat
	rawSQL := `
		SELECT current_owner_id AS user_id, COUNT(*) AS pending_count
		FROM review_issues
		WHERE deleted_at IS NULL AND org_id = ? AND status IN (?) AND current_owner_id > 0
	`
	args := []interface{}{orgID, []string{model.IssueStatusPending, model.IssueStatusPendingInherited}}
	if !baseline.IsZero() {
		rawSQL += ` AND original_created_at >= ?`
		args = append(args, baseline)
	}
	rawSQL += ` GROUP BY current_owner_id`
	model.DB.Raw(rawSQL, args...).Scan(&stats)

	for _, st := range stats {
		s.notifSvc.SendInbox(orgID, st.UserID, model.NotificationTypeDailyDigest,
			fmt.Sprintf("今日待办：您有 %d 条 Issue 待处理", st.PendingCount),
			fmt.Sprintf("您当前有 %d 条 Issue 等待处理，请及时闭环。", st.PendingCount),
			"",
		)
	}
}

// findProjectNotifier 查找项目专属机器人，无专属时返回全局机器人
func (s *EscalationService) findProjectNotifier(project model.Project) *model.WeComNotifier {
	// 1. 优先查项目专属机器人（取第一个启用的）
	if project.ID > 0 {
		var specific model.WeComNotifier
		if err := model.DB.Where("enabled = ? AND org_id = ? AND project_id = ?", true, project.OrgID, project.ID).
			Order("created_at ASC").First(&specific).Error; err == nil {
			return &specific
		}
	}
	// 2. 无专属时，查全局机器人（取第一个启用的）
	var global model.WeComNotifier
	if err := model.DB.Where("enabled = ? AND org_id = ? AND project_id IS NULL", true, project.OrgID).
		Order("created_at ASC").First(&global).Error; err == nil {
		return &global
	}
	return nil
}

// sendEscalationIMRaw 底层发送 IM（模板中已含 {{AT_RECIPIENT}}，不再追加）
func (s *EscalationService) sendEscalationIMRaw(project model.Project, task model.Task, mentions []string, recipientType string, msg string) {
	notifier := s.findProjectNotifier(project)
	if notifier == nil {
		zap.L().Debug("sendEscalationIMRaw: no notifier found",
			zap.Uint("project_id", project.ID),
			zap.Uint("task_id", task.ID))
		return
	}

	// 模板中 {{AT_RECIPIENT}} 已包含 @ 语法，不再由 SendMessage 追加
	ok, detail, err := s.notifierSvc.SendMessage(notifier.WebhookUrl, msg, "")
	log := model.NotificationDeliveryLog{
		NotifierID:     &notifier.ID,
		TaskID:         &task.ID,
		Channel:        "wecom",
		RecipientType:  recipientType,
		MessagePreview: truncateRune(msg, 200),
		Status:         "success",
	}
	if !ok || err != nil {
		log.Status = "failed"
		log.ErrorMsg = detail
		if err != nil {
			log.ErrorMsg = err.Error()
		}
	}
	result := model.DB.Create(&log)
	if result.Error != nil {
		zap.L().Error("sendEscalationIMRaw: failed to create delivery log",
			zap.Uint("task_id", task.ID),
			zap.Error(result.Error))
	}
	zap.L().Debug("sendEscalationIMRaw: sent",
		zap.Uint("task_id", task.ID),
		zap.String("recipient_type", recipientType),
		zap.Bool("ok", ok),
		zap.String("status", log.Status))
}

// recipientType 根据 level 返回 delivery log 的 recipient_type
func recipientType(level int) string {
	if level == 4 {
		return "admin"
	}
	return "steward"
}

// collectIM 将单条 IM 收入聚合桶（按 taskID + stage 聚合）
func (s *EscalationService) collectIM(target imTarget, issueID uint, issueMessage string) {
	key := fmt.Sprintf("%d:%s", target.task.ID, target.stage)
	if s.imCollector == nil {
		s.imCollector = make(map[string]*imBatch)
	}
	batch, ok := s.imCollector[key]
	if !ok {
		batch = &imBatch{
			project:        target.project,
			task:           target.task,
			stage:          target.stage,
			level:          target.level,
			thresholdHours: target.thresholdHours,
			mentions:       target.mentions,
			issues:         make([]imIssue, 0),
			imTemplate:     target.imTemplate,
		}
		s.imCollector[key] = batch
		zap.L().Debug("collectIM: created new batch",
			zap.String("key", key),
			zap.Int("level", target.level),
			zap.String("stage", target.stage),
			zap.Int("mention_count", len(target.mentions)))
	}
	batch.issues = append(batch.issues, imIssue{ID: issueID, Message: issueMessage})
	zap.L().Debug("collectIM: added issue to batch",
		zap.String("key", key),
		zap.Uint("issue_id", issueID),
		zap.Int("batch_size", len(batch.issues)))
}

// isQuietHours 判断当前是否处于规则配置的免打扰时段
func (s *EscalationService) isQuietHours() bool {
	if s.quietStart == "" || s.quietEnd == "" {
		return false
	}
	start, err1 := time.Parse("15:04", s.quietStart)
	end, err2 := time.Parse("15:04", s.quietEnd)
	if err1 != nil || err2 != nil {
		return false
	}
	now := time.Now()
	nowMin := now.Hour()*60 + now.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()

	if startMin < endMin {
		// 不跨天，例如 09:00-18:00
		return nowMin >= startMin && nowMin < endMin
	}
	// 跨天，例如 22:00-09:00 或 00:00-09:00
	return nowMin >= startMin || nowMin < endMin
}

// shouldFreezeExecution 判断当前是否处于冻结期（免打扰时段或节假日）
func (s *EscalationService) shouldFreezeExecution() bool {
	if s.isQuietHours() {
		return true
	}
	if !s.calc.IsWorkTime(time.Now()) {
		return true
	}
	return false
}

// flushIMs 统一 flush 聚合后的 IM 消息
func (s *EscalationService) flushIMs() {
	if len(s.imCollector) == 0 {
		zap.L().Debug("flushIMs: no batches to flush")
		return
	}
	if s.isQuietHours() {
		zap.L().Debug("flushIMs: in quiet hours, skipping IM send",
			zap.String("quiet_start", s.quietStart),
			zap.String("quiet_end", s.quietEnd),
			zap.Int("batch_count", len(s.imCollector)))
		s.imCollector = nil
		return
	}
	zap.L().Debug("flushIMs: flushing batches", zap.Int("batch_count", len(s.imCollector)))
	for _, batch := range s.imCollector {
		var msg string
		if batch.imTemplate != "" {
			msg = renderIMFromTemplate(batch)
		} else {
			msg = renderIMDefault(batch)
		}
		zap.L().Debug("flushIMs: sending batch",
			zap.Int("level", batch.level),
			zap.String("stage", batch.stage),
			zap.Int("issue_count", len(batch.issues)),
			zap.Int("msg_len", len(msg)))
		s.sendEscalationIMRaw(batch.project, batch.task, batch.mentions, recipientType(batch.level), msg)
	}
	s.imCollector = nil
}

// buildIMTemplateContext 从聚合批次构建模板渲染上下文
func buildIMTemplateContext(batch *imBatch) IMTemplateContext {
	var issueList []IMIssueItem
	for _, iss := range batch.issues {
		issueList = append(issueList, IMIssueItem{ID: iss.ID, Message: iss.Message})
	}
	ctx := IMTemplateContext{
		Project:      batch.project,
		Task:         batch.task,
		Developer:    batch.task.MRAuthor,
		AtRecipient:  strings.Join(batch.mentions, " "),
		IssueCount:   len(batch.issues),
		IssueList:    issueList,
		IssueID:      0,
		IssueMessage: "",
	}
	if len(batch.issues) > 0 {
		ctx.IssueID = batch.issues[0].ID
		ctx.IssueMessage = batch.issues[0].Message
	}
	return ctx
}

// renderIMFromTemplate 使用配置模板渲染 IM 消息
func renderIMFromTemplate(batch *imBatch) string {
	ctx := buildIMTemplateContext(batch)
	return RenderIMTemplate(batch.imTemplate, ctx)
}

// renderIMDefault 使用动态阈值渲染 IM 消息
func renderIMDefault(batch *imBatch) string {
	var stageText string
	switch batch.level {
	case 2:
		stageText = "已超期 " + formatDuration(batch.thresholdHours)
	case 3:
		stageText = "已超期 " + formatDuration(batch.thresholdHours) + "，正式升级"
	case 4:
		stageText = "已超期 " + formatDuration(batch.thresholdHours)
	default:
		stageText = "升级提醒"
	}

	if len(batch.issues) == 1 {
		msg := fmt.Sprintf("**Issue #%d %s**\n问题：%s",
			batch.issues[0].ID, stageText, batch.issues[0].Message)
		if len(batch.mentions) > 0 {
			msg += "\n\n" + strings.Join(batch.mentions, " ")
		}
		return msg
	}

	var lines []string
	for _, issue := range batch.issues {
		lines = append(lines, fmt.Sprintf("**Issue #%d** %s\n问题：%s", issue.ID, stageText, issue.Message))
	}
	msg := fmt.Sprintf("**⚠️ Issue 升级告警（%d 条）**\n\n项目：%s\nMR：!%d %s\n\n%s",
		len(batch.issues), batch.project.Name, batch.task.MRMergeID, batch.task.MRTitle,
		strings.Join(lines, "\n---\n"))
	// 如果 mentions 非空，追加到消息末尾（@ 人员）
	if len(batch.mentions) > 0 {
		msg += "\n\n" + strings.Join(batch.mentions, " ")
	}
	return msg
}

// checkDeliveryFailuresForOrg 检查当日 IM 投递失败并告警管理员（按组织）
func (s *EscalationService) checkDeliveryFailuresForOrg(orgID uint) {
	var failedCount int64
	todayStart := time.Now().Truncate(24 * time.Hour)
	// TODO: NotificationDeliveryLog 尚无 org_id，当前按全局统计；待模型扩展后改为 org 过滤
	model.DB.Model(&model.NotificationDeliveryLog{}).
		Where("status = ? AND created_at >= ?", "failed", todayStart).
		Count(&failedCount)
	if failedCount == 0 {
		return
	}
	var admins []model.User
	model.DB.Joins("JOIN org_users ON users.id = org_users.user_id").
		Where("org_users.org_id = ? AND org_users.role IN ? AND org_users.status = 'active' AND users.enabled = ?", orgID, []string{"super_admin", "org_admin"}, true).
		Find(&admins)
	for _, admin := range admins {
		s.notifSvc.SendInbox(orgID, admin.ID, model.NotificationTypeBatchAlert,
			fmt.Sprintf("【系统告警】今日 IM 投递失败 %d 次", failedCount),
			fmt.Sprintf("今日企微/IM 消息投递失败 %d 次，请检查 Webhook 配置或网络状态。", failedCount),
			"",
		)
	}
}
