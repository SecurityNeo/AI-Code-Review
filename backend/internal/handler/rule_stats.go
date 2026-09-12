package handler

import (
	"errors"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type RuleStatsHandler struct{}

func NewRuleStatsHandler() *RuleStatsHandler {
	return &RuleStatsHandler{}
}

// ruleStatsAllowedRanges 规则统计允许的 range 值。
// "all" → 起始时间设为 1970-01-01，等价"全部历史"。
var ruleStatsAllowedRanges = map[string]bool{
	"today": true,
	"7d":    true,
	"30d":   true,
	"90d":   true,
	"all":   true,
}

// parseRuleStatsRange 解析规则统计的时间范围。
// range=today / 7d / 30d / 90d / all；非法值默认 7d。
// 返回 [start, end)；"all" 等价于 1970-01-01 ~ now。
func parseRuleStatsRange(c *gin.Context) (time.Time, time.Time) {
	now := time.Now()
	r := c.DefaultQuery("range", "7d")
	if !ruleStatsAllowedRanges[r] {
		r = "7d"
	}
	switch r {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), now
	case "30d":
		return now.AddDate(0, 0, -30), now
	case "90d":
		return now.AddDate(0, 0, -90), now
	case "all":
		return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), now
	default:
		return now.AddDate(0, 0, -7), now
	}
}

// pctString 把分子分母计算为保留 1 位小数的百分比字符串。
// 分母为 0 时返回 "0%"，避免除零异常与无意义的 "NaN%" / "Inf%"。
func pctString(numerator, denominator int64) string {
	if denominator <= 0 {
		return "0%"
	}
	return strconv.FormatFloat(float64(numerator)*100/float64(denominator), 'f', 1, 64) + "%"
}

// severityToCN 把后端枚举值映射为中文显示名。
// 未知值原样返回，避免遗漏枚举导致 UI 显示空白。
func severityToCN(s string) string {
	switch s {
	case "critical":
		return "严重"
	case "high":
		return "高"
	case "medium":
		return "中"
	case "low":
		return "低"
	case "info":
		return "提示"
	default:
		return s
	}
}

// granularityForRange 根据时间范围决定趋势图聚合粒度。
// 规则：今日（range=today）按小时展示；其他（7d/30d/90d/all）按天展示。
// 即使 today 跨日 0:00 ~ now 不超过 24h，也强制 hour 以便前端按"每个小时一个数据点"绘图。
// 兜底：start == end 时回退按天，避免 0 长度。
func granularityForRange(start, end time.Time, rangeKey string) string {
	if rangeKey == "today" && !start.Equal(end) {
		return "hour"
	}
	return "day"
}

// fillTrendBuckets 在 SQL 查询结果的基础上，补齐缺失的桶（缺失字段值 = 0）。
// hour：固定生成今天 00:00 ~ 23:00 共 24 个桶（不受 start/end 影响），让图表点位稳定。
// day：从 start 所在日期 到 end 所在日期，每天一个桶。
// 与 token_usage.generateTrendBuckets 行为一致，确保 today → 24 个整点，7d/30d/... → 连续日期不缺位。
// valueField 指定 gin.H 中需要补 0 的字段名（如 "hit_count"）。
func fillTrendBuckets(data []gin.H, gran string, start, end time.Time, valueField string) []gin.H {
	// 已存在的桶索引
	existing := make(map[string]int64, len(data))
	for _, d := range data {
		if key, ok := d["date"].(string); ok {
			if v, ok := d[valueField].(int64); ok {
				existing[key] = v
			}
		}
	}
	// 生成期望桶列表
	loc := start.Location()
	expected := make([]string, 0, 24)
	if gran == "hour" {
		base := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
		for i := 0; i < 24; i++ {
			expected = append(expected, base.Add(time.Duration(i)*time.Hour).Format("2006-01-02 15:04:05"))
		}
	} else {
		endDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, loc)
		cur := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
		for !cur.After(endDay) {
			expected = append(expected, cur.Format("2006-01-02"))
			cur = cur.AddDate(0, 0, 1)
		}
	}
	// 按期望桶输出，缺失补 0
	out := make([]gin.H, 0, len(expected))
	for _, b := range expected {
		v, ok := existing[b]
		if !ok {
			v = 0
		}
		out = append(out, gin.H{"date": b, valueField: v})
	}
	return out
}

// ruleStatsBaseQuery 构造规则统计的基础查询。
// 关键点：使用默认 scope，过滤掉 soft-deleted 的 ReviewIssue。
// 背景：persistor.go:30-31 在任务重试时 soft delete 旧 issues 再 insert 新的。
// 默认 scope 保证"统计 = 当前可见 issues"，与任务详情列表保持一致。
// 用户过滤：admin 看全部；非 admin 按 GitlabUsername 过滤（与 token_usage 一致）。
// 注意：gorm chain 会累积 JOIN，每次新查询必须调用本函数获取**新 q**，否则
// 会在同一条 SQL 中出现 "Not unique table/alias" 错误（典型场景：同一 handler
// 内多次 q.Select/Scan 复用同一 q 对象）。
func ruleStatsBaseQuery(c *gin.Context) (*gorm.DB, time.Time, time.Time) {
	start, end := parseRuleStatsRange(c)
	q := model.DB.Model(&model.ReviewIssue{}).
		Joins("LEFT JOIN tasks t ON t.id = review_issues.task_id").
		Where("review_issues.created_at >= ? AND review_issues.created_at < ?", start, end)

	// 多租户改造：叠加组织过滤
	scope := middleware.GetAuthScope(c)
	if scope != nil && !scope.IsSuperAdmin {
		q = q.Where("review_issues.org_id IN ?", scope.VisibleOrgIDs)
	}

	if user, ok := middleware.GetUser(c); ok {
		if scope == nil || !scope.HasOrgRole("super_admin", "org_admin") {
			if user.GitlabUsername == "" {
				q = q.Where("1 = 0")
			} else {
				q = q.Where("t.mr_author = ?", user.GitlabUsername)
			}
		}
	}
	return q, start, end
}

// ruleStatsScopedQuery 在 ruleStatsBaseQuery 基础上叠加 category / severity 过滤。
// 用于 Overview 多次独立查询（KPI、趋势、TOP 10、分布），每次返回**独立 q**。
func ruleStatsScopedQuery(c *gin.Context, category, severity string) *gorm.DB {
	q, _, _ := ruleStatsBaseQuery(c)
	if category != "" {
		q = q.Where("review_issues.category = ?", category)
	}
	if severity != "" {
		q = q.Where("review_issues.severity = ?", severity)
	}
	return q
}

// ruleStatsBaseQuery0 是 ruleStatsBaseQuery 的简化版本，仅返回 q。
// 用于 ByRule / RecentIssues 等多次独立查询场景（每次返回**独立 q**）。
func ruleStatsBaseQuery0(c *gin.Context) *gorm.DB {
	q, _, _ := ruleStatsBaseQuery(c)
	return q
}

// Overview 规则命中统计总览
// GET /api/v1/rule-stats/overview?range=7d&category=&severity=
func (h *RuleStatsHandler) Overview(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	category := c.Query("category")
	severity := c.Query("severity")
	_, start, end := ruleStatsBaseQuery(c)
	gran := granularityForRange(start, end, c.Query("range"))

	// 1. KPI 数据
	type kpiRow struct {
		HitRulesCount int64 `gorm:"column:hit_rules_count"`
		TotalHits     int64 `gorm:"column:total_hits"`
		UniqueAuthors int64 `gorm:"column:unique_authors"`
		AcceptedCount int64 `gorm:"column:accepted_count"`
		RejectedCount int64 `gorm:"column:rejected_count"`
		PendingCount  int64 `gorm:"column:pending_count"`
	}
	var k kpiRow
	if err := ruleStatsScopedQuery(c, category, severity).Select(
		`COUNT(DISTINCT CASE WHEN review_issues.rule_code != '' THEN review_issues.rule_code END) AS hit_rules_count,
		COUNT(*) AS total_hits,
		COUNT(DISTINCT CASE WHEN t.mr_author != '' THEN t.mr_author END) AS unique_authors,
		COUNT(CASE WHEN review_issues.status = 'resolved' THEN 1 END) AS accepted_count,
		COUNT(CASE WHEN review_issues.status = 'false_positive' THEN 1 END) AS rejected_count,
		COUNT(CASE WHEN review_issues.status NOT IN ('resolved', 'false_positive') OR review_issues.status IS NULL THEN 1 END) AS pending_count`,
	).Scan(&k).Error; err != nil {
		zap.L().Error("rule stats overview kpi failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 修复率 / 误报率 / 待处理占比：统一基于 total_hits 计算，分母一致，三个百分比之和为 100%。
	// pending 包含 pending / dismissed 状态以及 NULL/空字符串。
	fixRate := pctString(k.AcceptedCount, k.TotalHits)
	rejectRate := pctString(k.RejectedCount, k.TotalHits)
	pendingRate := pctString(k.PendingCount, k.TotalHits)

	// 2. 趋势数据
	var trendSQL string
	if gran == "hour" {
		trendSQL = `DATE_FORMAT(review_issues.created_at, '%Y-%m-%d %H:00:00') AS bucket`
	} else {
		trendSQL = `DATE_FORMAT(review_issues.created_at, '%Y-%m-%d') AS bucket`
	}
	type trendRow struct {
		Bucket string `gorm:"column:bucket"`
		HitN   int64  `gorm:"column:hit_n"`
	}
	var trendRows []trendRow
	if err := ruleStatsScopedQuery(c, category, severity).Select(trendSQL + `, COUNT(*) AS hit_n`).
		Group("bucket").Order("bucket ASC").Scan(&trendRows).Error; err != nil {
		zap.L().Error("rule stats overview trend failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	trend := make([]gin.H, 0, len(trendRows))
	for _, r := range trendRows {
		trend = append(trend, gin.H{"date": r.Bucket, "hit_count": r.HitN})
	}
	// 补齐缺失桶：保证 hour 粒度返回 24 个点，day 粒度返回连续日期
	trend = fillTrendBuckets(trend, gran, start, end, "hit_count")

	// 3. 规则命中 TOP 10
	type topRuleRow struct {
		RuleCode     string `gorm:"column:rule_code"`
		RuleName     string `gorm:"column:rule_name"`
		Category     string `gorm:"column:category"`
		CategoryName string `gorm:"column:category_name"`
		Severity     string `gorm:"column:severity"`
		HitCount     int64  `gorm:"column:hit_count"`
		ProjCount    int64  `gorm:"column:project_count"`
		AuthorCount  int64  `gorm:"column:author_count"`
		Accepted     int64  `gorm:"column:accepted"`
		Rejected     int64  `gorm:"column:rejected"`
	}
	var topRules []topRuleRow
	if err := ruleStatsScopedQuery(c, category, severity).Select(
		`review_issues.rule_code AS rule_code,
		COALESCE(MAX(review_rules.name), review_issues.rule_code) AS rule_name,
		review_issues.category AS category,
		COALESCE(MAX(review_categories.name), 'AI自主发现') AS category_name,
			review_issues.severity AS severity,
		COUNT(*) AS hit_count,
		COUNT(DISTINCT t.project_id) AS project_count,
		COUNT(DISTINCT t.mr_author) AS author_count,
		COUNT(CASE WHEN review_issues.status = 'resolved' THEN 1 END) AS accepted,
		COUNT(CASE WHEN review_issues.status = 'false_positive' THEN 1 END) AS rejected`,
	).
		Joins("LEFT JOIN review_rules ON review_rules.code = review_issues.rule_code").
		Joins("LEFT JOIN review_categories ON review_categories.code = review_issues.category").
		Where("review_issues.rule_code != ''").
		Group("review_issues.rule_code, review_issues.category, review_issues.severity").
		Order("hit_count DESC").
		Limit(10).
		Scan(&topRules).Error; err != nil {
		zap.L().Error("rule stats overview top rules failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	topRulesOut := make([]gin.H, 0, len(topRules))
	for _, r := range topRules {
		topRulesOut = append(topRulesOut, gin.H{
			"rule_code":     r.RuleCode,
			"rule_name":     r.RuleName,
			"category":      r.Category,
			"category_name": r.CategoryName,
			"severity":      r.Severity,
			"severity_name": severityToCN(r.Severity),
			"hit_count":     r.HitCount,
			"project_count": r.ProjCount,
			"author_count":  r.AuthorCount,
			"fix_rate":      pctString(r.Accepted, r.HitCount),
			"reject_rate":   pctString(r.Rejected, r.HitCount),
		})
	}

	// 4. 按 category 分布
	type catRow struct {
		Category     string `gorm:"column:category_code"`
		CategoryName string `gorm:"column:category_name"`
		Cnt          int64  `gorm:"column:cnt"`
	}
	var catRows []catRow
	if err := ruleStatsScopedQuery(c, category, severity).Select(
		`review_issues.category AS category_code,
		COALESCE(NULLIF(MAX(review_categories.name), ''), 'AI自主发现') AS category_name,
		COUNT(*) AS cnt`,
	).
		Joins("LEFT JOIN review_categories ON review_categories.code = review_issues.category").
		Group("review_issues.category").
		Order("cnt DESC").
		Scan(&catRows).Error; err != nil {
		zap.L().Error("rule stats overview by category failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	byCategory := make([]gin.H, 0, len(catRows))
	for _, r := range catRows {
		code := r.Category
		if code == "" {
			code = "ai_self"
		}
		byCategory = append(byCategory, gin.H{
			"category":      code,
			"category_name": r.CategoryName,
			"count":         r.Cnt,
		})
	}

	// 5. 按 severity 分布
	type sevRow struct {
		Severity string `gorm:"column:severity_code"`
		Cnt      int64  `gorm:"column:cnt"`
	}
	var sevRows []sevRow
	if err := ruleStatsScopedQuery(c, category, severity).Select(
		`review_issues.severity AS severity_code,
		COUNT(*) AS cnt`,
	).Group("review_issues.severity").Order("cnt DESC").Scan(&sevRows).Error; err != nil {
		zap.L().Error("rule stats overview by severity failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	bySeverity := make([]gin.H, 0, len(sevRows))
	for _, r := range sevRows {
		code := r.Severity
		if code == "" {
			code = "uncategorized"
		}
		bySeverity = append(bySeverity, gin.H{
			"severity":      code,
			"severity_name": severityToCN(r.Severity),
			"count":         r.Cnt,
		})
	}

	c.JSON(200, gin.H{
		"data": gin.H{
			"kpi": gin.H{
				"hit_rules_count": k.HitRulesCount,
				"total_hits":      k.TotalHits,
				"unique_authors":  k.UniqueAuthors,
				"fix_rate":        fixRate,
				"reject_rate":     rejectRate,
				"pending_rate":    pendingRate,
			},
			"trend":       trend,
			"top_rules":   topRulesOut,
			"by_category": byCategory,
			"by_severity": bySeverity,
			"range":       c.Query("range"),
			"granularity": gran,
		},
	})
}

// ByRule 单条规则命中详情
// GET /api/v1/rule-stats/by-rule/:code?range=7d
func (h *RuleStatsHandler) ByRule(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	code := c.Param("code")
	if code == "" {
		c.JSON(400, gin.H{"error": "rule_code is required"})
		return
	}
	_, start, end := ruleStatsBaseQuery(c)
	gran := granularityForRange(start, end, c.Query("range"))
	applyCode := func() *gorm.DB {
		return ruleStatsBaseQuery0(c).Where("review_issues.rule_code = ?", code)
	}

	// 1. 规则基本信息（同时拿 category 中文名）
	var rule model.ReviewRule
	var categoryName string
	row := model.DB.Table("review_rules r").
		Select("r.id, r.code, r.name, r.category, r.severity, r.language, r.description, r.prompt, r.sort_order, r.is_built_in, r.is_enabled, r.created_at, r.updated_at, COALESCE(c.name, '') AS category_name").
		Joins("LEFT JOIN review_categories c ON c.code = r.category").
		Where("r.code = ?", code).
		Row()
	if err := row.Scan(
		&rule.ID, &rule.Code, &rule.Name, &rule.Category, &rule.Severity,
		&rule.Language, &rule.Description, &rule.Prompt, &rule.SortOrder,
		&rule.IsBuiltIn, &rule.IsEnabled, &rule.CreatedAt, &rule.UpdatedAt,
		&categoryName,
	); err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			zap.L().Error("load rule failed", zap.Error(err))
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		// 规则已删除或不存在时，给一个默认值（保留历史命中数据仍可展示）
		rule = model.ReviewRule{Code: code, Name: code, Category: "", Severity: ""}
	}

	// 2. KPI + 修复率/误报率
	type kpiRow struct {
		TotalHits   int64 `gorm:"column:total_hits"`
		ProjCount   int64 `gorm:"column:project_count"`
		AuthorCount int64 `gorm:"column:author_count"`
		Accepted    int64 `gorm:"column:accepted"`
		Rejected    int64 `gorm:"column:rejected"`
		Pending     int64 `gorm:"column:pending"`
	}
	var k kpiRow
	if err := applyCode().Select(
		`COUNT(*) AS total_hits,
		COUNT(DISTINCT t.project_id) AS project_count,
		COUNT(DISTINCT t.mr_author) AS author_count,
		COUNT(CASE WHEN review_issues.status = 'resolved' THEN 1 END) AS accepted,
		COUNT(CASE WHEN review_issues.status = 'false_positive' THEN 1 END) AS rejected,
		COUNT(CASE WHEN review_issues.status NOT IN ('resolved', 'false_positive') OR review_issues.status IS NULL THEN 1 END) AS pending`,
	).Scan(&k).Error; err != nil {
		zap.L().Error("rule stats by-rule kpi failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	fixRate := pctString(k.Accepted, k.TotalHits)
	rejectRate := pctString(k.Rejected, k.TotalHits)
	pendingRate := pctString(k.Pending, k.TotalHits)

	// 3. 趋势
	var trendSQL string
	if gran == "hour" {
		trendSQL = `DATE_FORMAT(review_issues.created_at, '%Y-%m-%d %H:00:00') AS bucket`
	} else {
		trendSQL = `DATE_FORMAT(review_issues.created_at, '%Y-%m-%d') AS bucket`
	}
	type trendRow struct {
		Bucket string `gorm:"column:bucket"`
		N      int64  `gorm:"column:n"`
	}
	var trendRows []trendRow
	if err := applyCode().Select(trendSQL + `, COUNT(*) AS n`).
		Group("bucket").Order("bucket ASC").Scan(&trendRows).Error; err != nil {
		zap.L().Error("rule stats by-rule trend failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	trend := make([]gin.H, 0, len(trendRows))
	for _, r := range trendRows {
		trend = append(trend, gin.H{"date": r.Bucket, "hit_count": r.N})
	}
	// 补齐缺失桶
	trend = fillTrendBuckets(trend, gran, start, end, "hit_count")

	// 4. 涉及项目 TOP 5（tasks 表只存 project_id，需 JOIN projects 拿 name）
	type projRow struct {
		ProjectID   uint   `gorm:"column:project_id"`
		ProjectName string `gorm:"column:project_name"`
		HitCount    int64  `gorm:"column:hit_count"`
	}
	var projRows []projRow
	if err := applyCode().Select(
		`t.project_id AS project_id,
		COALESCE(MAX(p.name), CONCAT('项目#', t.project_id)) AS project_name,
		COUNT(*) AS hit_count`,
	).
		Joins("LEFT JOIN projects p ON p.id = t.project_id").
		Where("t.project_id > 0").
		Group("t.project_id").
		Order("hit_count DESC").
		Limit(5).
		Scan(&projRows).Error; err != nil {
		zap.L().Error("rule stats by-rule top projects failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	topProjects := make([]gin.H, 0, len(projRows))
	for _, r := range projRows {
		topProjects = append(topProjects, gin.H{
			"project_id":   r.ProjectID,
			"project_name": r.ProjectName,
			"hit_count":    r.HitCount,
		})
	}

	// 5. 涉及作者 TOP 5
	type authorRow struct {
		Author   string `gorm:"column:mr_author"`
		HitCount int64  `gorm:"column:hit_count"`
	}
	var authorRows []authorRow
	if err := applyCode().Select(
		`t.mr_author AS mr_author,
		COUNT(*) AS hit_count`,
	).Where("t.mr_author != ''").
		Group("t.mr_author").
		Order("hit_count DESC").
		Limit(5).
		Scan(&authorRows).Error; err != nil {
		zap.L().Error("rule stats by-rule top authors failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	topAuthors := make([]gin.H, 0, len(authorRows))
	for _, r := range authorRows {
		topAuthors = append(topAuthors, gin.H{
			"author":    r.Author,
			"hit_count": r.HitCount,
		})
	}

	c.JSON(200, gin.H{
		"data": gin.H{
			"rule": gin.H{
				"id":            rule.ID,
				"code":          rule.Code,
				"name":          rule.Name,
				"category":      rule.Category,
				"category_name": categoryName,
				"severity":      rule.Severity,
				"severity_name": severityToCN(rule.Severity),
				"description":   rule.Description,
				"is_enabled":    rule.IsEnabled,
				"is_built_in":   rule.IsBuiltIn,
			},
			"kpi": gin.H{
				"total_hits":    k.TotalHits,
				"project_count": k.ProjCount,
				"author_count":  k.AuthorCount,
				"accepted":      k.Accepted,
				"rejected":      k.Rejected,
				"pending":       k.Pending,
				"fix_rate":      fixRate,
				"reject_rate":   rejectRate,
				"pending_rate":  pendingRate,
			},
			"trend":        trend,
			"top_projects": topProjects,
			"top_authors":  topAuthors,
			"granularity":  gran,
		},
	})
}

// RecentIssues 规则命中明细（钻取用）
// GET /api/v1/rule-stats/recent-issues?rule_code=&range=7d&page=1&page_size=20
func (h *RuleStatsHandler) RecentIssues(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	code := c.Query("rule_code")
	if code == "" {
		c.JSON(400, gin.H{"error": "rule_code is required"})
		return
	}

	page, pageSize := parsePagination(c, 20, 100)
	offset := (page - 1) * pageSize

	var total int64
	if err := ruleStatsBaseQuery0(c).Where("review_issues.rule_code = ?", code).Count(&total).Error; err != nil {
		zap.L().Error("count recent issues failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	type issueRow struct {
		ID          uint      `gorm:"column:id"`
		TaskID      uint      `gorm:"column:task_id"`
		File        string    `gorm:"column:file"`
		LineStart   int       `gorm:"column:line_start"`
		LineEnd     int       `gorm:"column:line_end"`
		CodeSnippet string    `gorm:"column:code_snippet"`
		Message     string    `gorm:"column:message"`
		Suggestion  string    `gorm:"column:suggestion"`
		Status      string    `gorm:"column:status"`
		Severity    string    `gorm:"column:severity"`
		DeductScore int       `gorm:"column:deduct_score"`
		CreatedAt   time.Time `gorm:"column:created_at"`
		ProjectName string    `gorm:"column:project_name"`
		MRTitle     string    `gorm:"column:mr_title"`
		MRAuthor    string    `gorm:"column:mr_author"`
		MRMergeID   int       `gorm:"column:mr_merge_id"`
	}
	var rows []issueRow
	if err := ruleStatsBaseQuery0(c).Where("review_issues.rule_code = ?", code).Select(
		`review_issues.id, review_issues.task_id,
		review_issues.file, review_issues.line_start, review_issues.line_end,
		review_issues.code_snippet,
		review_issues.message, review_issues.suggestion,
		review_issues.status, review_issues.severity, review_issues.deduct_score,
		review_issues.created_at,
		COALESCE(p.name, CONCAT('项目#', t.project_id)) AS project_name,
		COALESCE(t.mr_title, '') AS mr_title,
		COALESCE(t.mr_author, '') AS mr_author,
		COALESCE(t.mr_merge_id, 0) AS mr_merge_id`,
	).
		Joins("LEFT JOIN projects p ON p.id = t.project_id").
		Order("review_issues.created_at DESC, review_issues.id DESC").
		Limit(pageSize).
		Offset(offset).
		Scan(&rows).Error; err != nil {
		zap.L().Error("list recent issues failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"id":            r.ID,
			"task_id":       r.TaskID,
			"file":          r.File,
			"line_start":    r.LineStart,
			"line_end":      r.LineEnd,
			"code_snippet":  r.CodeSnippet,
			"message":       r.Message,
			"suggestion":    r.Suggestion,
			"status":        r.Status,
			"severity":      r.Severity,
			"severity_name": severityToCN(r.Severity),
			"deduct_score":  r.DeductScore,
			"created_at":    r.CreatedAt,
			"project_name":  r.ProjectName,
			"mr_title":      r.MRTitle,
			"mr_author":     r.MRAuthor,
			"mr_merge_id":   r.MRMergeID,
		})
	}

	c.JSON(200, gin.H{
		"data":      out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  int64(offset+len(rows)) < total,
	})
}
