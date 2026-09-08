package handler

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type StatisticsHandler struct{}

func NewStatisticsHandler() *StatisticsHandler {
	return &StatisticsHandler{}
}

// StatisticsResponse 整个统计仪表板的响应结构
type StatisticsResponse struct {
	KPI               KPIStats       `json:"kpi"`
	ProjectActivity   []ProjectItem  `json:"project_activity"`
	DeveloperCommits  []AuthorItem   `json:"developer_commits"`
	DeveloperScores   []AuthorScore  `json:"developer_scores"`
	CodeChanges       []CodeChange   `json:"code_changes"`
	ScoreDistribution ScoreDist      `json:"score_distribution"`
	MRStatus          []StatusItem   `json:"mr_status"`
	LowQualityMRs     []LowQualityMR `json:"low_quality_mrs"`
	Scatter           []ScatterPoint `json:"scatter"`
	Radar             RadarData      `json:"radar"`
	Trend             TrendData      `json:"trend"`
}

type KPIStats struct {
	TotalMRs       int64   `json:"total_mrs"`
	AvgScore       float64 `json:"avg_score"`
	TotalChanges   int64   `json:"total_changes"`
	ActiveProjects int64   `json:"active_projects"`
	TotalProjects  int64   `json:"total_projects"`
}

type ProjectItem struct {
	Project string `json:"project"`
	Count   int64  `json:"count"`
}

type AuthorItem struct {
	Author string `json:"author"`
	Count  int64  `json:"count"`
}

type AuthorScore struct {
	Author string  `json:"author"`
	Avg    float64 `json:"avg"`
	Count  int64   `json:"count"`
}

type CodeChange struct {
	Author    string `json:"author"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
}

type ScoreDist struct {
	Excellent int64 `json:"excellent"`
	Good      int64 `json:"good"`
	Pass      int64 `json:"pass"`
	Fail      int64 `json:"fail"`
}

type StatusItem struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type LowQualityMR struct {
	Project      string  `json:"project"`
	Title        string  `json:"title"`
	Author       string  `json:"author"`
	Score        float64 `json:"score"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	TotalChanges int     `json:"total_changes"`
}

type ScatterPoint struct {
	X           int64   `json:"x"`            // 变更行数
	Y           float64 `json:"y"`            // 评分
	ReviewCount int     `json:"review_count"` // Review 次数
	Author      string  `json:"author"`
	Project     string  `json:"project"`
	MRTitle     string  `json:"mr_title"`
}

type RadarData struct {
	Indicators []RadarIndicator `json:"indicators"`
	Series     []RadarSeries    `json:"series"`
}

type RadarIndicator struct {
	Name string  `json:"name"`
	Max  float64 `json:"max"`
}

type RadarSeries struct {
	Name  string    `json:"name"`
	Value []float64 `json:"value"`
}

type TrendData struct {
	Dates           []string             `json:"dates"`
	Scores          []float64            `json:"scores"`
	MRCounts        []int64              `json:"mr_counts"`
	Changes         []int64              `json:"changes"`
	DeveloperScores map[string][]float64 `json:"developer_scores"` // 每个开发者每天的平均分
}

// DevDailyScore 用于 Go 端按作者/日期聚合
type DevDailyScore struct {
	Author string  `gorm:"column:author"`
	Date   string  `gorm:"column:date"`
	Avg    float64 `gorm:"column:avg"`
	Count  int64   `gorm:"column:cnt"`
}

func (h *StatisticsHandler) Get(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	projectName := c.Query("project_name")
	author := c.Query("author")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	daysStr := c.DefaultQuery("days", "0")
	days, _ := strconv.Atoi(daysStr)

	if startDate == "" && endDate == "" && days > 0 {
		endDate = time.Now().Format("2006-01-02") + " 23:59:59"
		startDate = time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	}

	if endDate != "" && !strings.Contains(endDate, ":") {
		endDate = endDate + " 23:59:59"
	}

	dateCol := "IF(mr_created_at IS NOT NULL AND mr_created_at > '1970-01-01', mr_created_at, synced_at)"

	db := model.DB.Model(&model.MergeRequestReviewLog{})
	// 多租户改造：按组织过滤（admin不过滤）
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}

	// 按用户角色过滤
	db = model.FilterByUser(db, user, "author")
	// 已关闭 MR 不参与评分与代码变更量统计，但 MR 状态分布不排除 closed
	// 使用 Session 复制避免修改原始 db，否则状态分布也会跟着排除 closed
	scoreChangeDB := db.Session(&gorm.Session{}).Where("mr_state != ?", "closed")
	if projectName != "" {
		db = db.Where("project_name = ?", projectName)
		scoreChangeDB = scoreChangeDB.Where("project_name = ?", projectName)
	}
	if author != "" {
		db = db.Where("author = ?", author)
		scoreChangeDB = scoreChangeDB.Where("author = ?", author)
	}
	if startDate != "" {
		db = db.Where(dateCol+" >= ?", startDate)
		scoreChangeDB = scoreChangeDB.Where(dateCol+" >= ?", startDate)
	}
	// endDate 仅在用户真正指定了日期才过滤；days=0 不限制时间时不设 endDate
	if endDate != "" {
		db = db.Where(dateCol+" <= ?", endDate)
		scoreChangeDB = scoreChangeDB.Where(dateCol+" <= ?", endDate)
	}

	var resp StatisticsResponse

	// 1. KPI 统计（排除 closed MR 的评分与代码变更量）
	type kpiAgg struct {
		TotalMRs     int64   `gorm:"column:total_mrs"`
		TotalChanges int64   `gorm:"column:total_changes"`
		AvgScore     float64 `gorm:"column:avg_score"`
		ScoreCount   int64   `gorm:"column:score_count"`
		ActiveProjs  int64   `gorm:"column:active_projs"`
	}
	var kpi kpiAgg
	_ = scoreChangeDB.Session(&gorm.Session{}).Select(
		"COUNT(*) as total_mrs, " +
			"COALESCE(SUM(additions + deletions), 0) as total_changes, " +
			"COALESCE(SUM(CASE WHEN score > 0 THEN score ELSE 0 END), 0) as avg_score, " +
			"COUNT(CASE WHEN score > 0 THEN 1 END) as score_count").
		Scan(&kpi).Error

	resp.KPI.TotalMRs = kpi.TotalMRs
	resp.KPI.TotalChanges = kpi.TotalChanges
	if kpi.ScoreCount > 0 {
		resp.KPI.AvgScore = kpi.AvgScore / float64(kpi.ScoreCount)
	}

	// 活跃项目数（基于同样的筛选）
	var activeProjs int64
	_ = db.Session(&gorm.Session{}).Select("COUNT(DISTINCT project_name) as active_projs").Scan(&activeProjs).Error
	resp.KPI.ActiveProjects = activeProjs

	// 总项目数（当前组织可见项目数）
	var totalProjs int64
	if scope.IsSuperAdmin {
		model.DB.Model(&model.Project{}).Count(&totalProjs)
	} else {
		model.DB.Model(&model.Project{}).Where("org_id IN ?", scope.VisibleOrgIDs).Count(&totalProjs)
	}
	resp.KPI.TotalProjects = totalProjs

	// 2. 项目活跃度 TOP10（MR 数量，不排除 closed）
	var projectActivity []ProjectItem
	_ = db.Session(&gorm.Session{}).Select("project_name as project, COUNT(*) as count").
		Group("project_name").Order("count DESC").Limit(10).
		Scan(&projectActivity).Error
	resp.ProjectActivity = projectActivity

	// 3. 开发者提交统计（全量，MR 数量，不排除 closed）
	var devCommits []AuthorItem
	_ = db.Session(&gorm.Session{}).Select("author, COUNT(*) as count").
		Where("author != ?", "").
		Group("author").Order("count DESC").
		Scan(&devCommits).Error
	resp.DeveloperCommits = devCommits

	// 4. 开发者平均得分（排除 closed MR）
	type devScoreRow struct {
		Author string
		Avg    float64
		Count  int64
	}
	var devScores []devScoreRow
	_ = scoreChangeDB.Session(&gorm.Session{}).Select(
		"author, "+
			"COALESCE(AVG(CASE WHEN score > 0 THEN score END), 0) as avg, "+
			"COUNT(CASE WHEN score > 0 THEN 1 END) as count").
		Where("author != ?", "").
		Group("author").Having("count > 0").Order("avg DESC").
		Scan(&devScores).Error
	for _, d := range devScores {
		resp.DeveloperScores = append(resp.DeveloperScores, AuthorScore{
			Author: d.Author,
			Avg:    d.Avg,
			Count:  d.Count,
		})
	}

	// 5. 人员代码变更统计（排除 closed MR）
	var codeChanges []CodeChange
	_ = scoreChangeDB.Session(&gorm.Session{}).Select(
		"author, COALESCE(SUM(additions), 0) as additions, COALESCE(SUM(deletions), 0) as deletions").
		Where("author != ?", "").
		Group("author").
		Scan(&codeChanges).Error
	// 在 Go 代码中按总变更量排序
	sort.Slice(codeChanges, func(i, j int) bool {
		return codeChanges[i].Additions+codeChanges[i].Deletions > codeChanges[j].Additions+codeChanges[j].Deletions
	})
	resp.CodeChanges = codeChanges

	// 6. 评分分布（排除 closed MR）
	var scoreDist ScoreDist
	_ = scoreChangeDB.Session(&gorm.Session{}).Select(
		"SUM(CASE WHEN score >= 90 THEN 1 ELSE 0 END) as excellent, " +
			"SUM(CASE WHEN score >= 80 AND score < 90 THEN 1 ELSE 0 END) as good, " +
			"SUM(CASE WHEN score >= 60 AND score < 80 THEN 1 ELSE 0 END) as pass, " +
			"SUM(CASE WHEN score < 60 THEN 1 ELSE 0 END) as fail").
		Scan(&scoreDist).Error
	resp.ScoreDistribution = scoreDist

	// 7. MR 状态分布（排除 draft，展示状态本身，不排除 closed）
	var mrStatus []StatusItem
	_ = db.Session(&gorm.Session{}).Select("mr_state as status, COUNT(*) as count").
		Where("mr_state != ?", "draft").
		Group("mr_state").Order("count DESC").
		Scan(&mrStatus).Error
	resp.MRStatus = mrStatus

	// 8. 低质量 MR TOP10（排除 closed MR）
	var lowQuality []LowQualityMR
	_ = scoreChangeDB.Session(&gorm.Session{}).Select(
		"project_name as project, mr_title as title, author, score, additions, deletions, additions + deletions as total_changes").
		Where("score > 0 AND score < 60").
		Order("score ASC").Limit(10).
		Scan(&lowQuality).Error
	resp.LowQualityMRs = lowQuality

	// 9. 散点图数据（排除 closed MR）
	var scatter []ScatterPoint
	_ = scoreChangeDB.Session(&gorm.Session{}).Select(
		"additions + deletions as x, score as y, review_count, author, project_name as project, mr_title").
		Where("score > 0").Order("id DESC").Limit(500).
		Scan(&scatter).Error
	resp.Scatter = scatter

	// 10. 雷达图数据
	var radarProjects []string
	projectsParam := c.Query("projects")
	if projectsParam != "" {
		// 用户指定了要对比的项目列表
		radarProjects = strings.Split(projectsParam, ",")
		// 去空值
		filtered := make([]string, 0, len(radarProjects))
		for _, p := range radarProjects {
			p = strings.TrimSpace(p)
			if p != "" {
				filtered = append(filtered, p)
			}
		}
		radarProjects = filtered
	} else {
		// 默认取前 5 个活跃项目
		_ = db.Session(&gorm.Session{}).Select("project_name").
			Group("project_name").Order("COUNT(*) DESC").Limit(5).
			Pluck("project_name", &radarProjects).Error
	}

	if len(radarProjects) > 0 {
		// 预先计算每个项目的 MR 数量，取最大值作为雷达图"活跃MR数"维度的 Max
		var maxMRCount int64
		projectMRCounts := make(map[string]int64)
		for _, proj := range radarProjects {
			var cnt int64
			_ = model.DB.Model(&model.MergeRequestReviewLog{}).Scopes(model.OrgScope(scope)).
				Where("project_name = ?", proj).
				Count(&cnt).Error
			projectMRCounts[proj] = cnt
			if cnt > maxMRCount {
				maxMRCount = cnt
			}
		}
		if maxMRCount == 0 {
			maxMRCount = 1
		}

		indicators := []RadarIndicator{
			{Name: "平均评分", Max: 100},
			{Name: "变更效率", Max: 100},
			{Name: "Review频次", Max: 10},
			{Name: "合入率", Max: 100},
			{Name: "活跃MR数", Max: float64(maxMRCount)},
			{Name: "低质量占比", Max: 50},
		}
		resp.Radar.Indicators = indicators

		for _, proj := range radarProjects {
			var r struct {
				AvgScore   float64
				Efficiency float64
				AvgReview  float64
				MergeRate  float64
				MRCount    int64
				LowQRate   float64
			}
			_ = model.DB.Model(&model.MergeRequestReviewLog{}).Scopes(model.OrgScope(scope)).
				Where("project_name = ?", proj).
				Where("mr_state != ?", "closed").
				Select(
					"COALESCE(AVG(CASE WHEN score > 0 THEN score END), 0) as avg_score, " +
						"COALESCE(AVG(CASE WHEN additions + deletions > 0 THEN score / (additions + deletions) * 1000 ELSE 0 END), 0) as efficiency, " +
						"COALESCE(AVG(review_count), 0) as avg_review, " +
						"CASE WHEN COUNT(*) > 0 THEN SUM(CASE WHEN mr_state = 'merged' THEN 1 ELSE 0 END) * 100.0 / COUNT(*) ELSE 0 END as merge_rate, " +
						"COUNT(*) as mr_count, " +
						"CASE WHEN COUNT(*) > 0 THEN SUM(CASE WHEN score > 0 AND score < 60 THEN 1 ELSE 0 END) * 100.0 / COUNT(*) ELSE 0 END as low_q_rate").
				Scan(&r).Error

			// 变更效率归一化到 0-100
			efficiency := r.Efficiency
			if efficiency > 100 {
				efficiency = 100
			}

			resp.Radar.Series = append(resp.Radar.Series, RadarSeries{
				Name: proj,
				Value: []float64{
					r.AvgScore,
					efficiency,
					r.AvgReview,
					r.MergeRate,
					float64(r.MRCount),
					r.LowQRate,
				},
			})
		}
	}

	// 11. 时间趋势（基于 mr_created_at，展现 MR 真实创建时间分布）
	// 注意：mr_created_at 可能为 NULL 或 MySQL 零日期，优先用它，无效时回退 synced_at
	// 策略：查全部原始记录在 Go 端聚合，完全避开 SQL 日期聚合的兼容性问题

	type trendSource struct {
		ID          int64      `gorm:"column:id"`
		MRCreatedAt *time.Time `gorm:"column:mr_created_at"`
		SyncedAt    time.Time  `gorm:"column:synced_at"`
		Author      string     `gorm:"column:author"`
		Score       float64    `gorm:"column:score"`
		Additions   int        `gorm:"column:additions"`
		Deletions   int        `gorm:"column:deletions"`
	}

	var sources []trendSource
	trendQueryDB := model.DB.Model(&model.MergeRequestReviewLog{}).Scopes(model.OrgScope(scope)).Select("id, mr_created_at, synced_at, score, additions, deletions")
	// 补上用户角色过滤（Bug：此前遗漏导致普通用户能看到所有人的质量趋势）
	trendQueryDB = model.FilterByUser(trendQueryDB, user, "author")
	if projectName != "" {
		trendQueryDB = trendQueryDB.Where("project_name = ?", projectName)
	}
	if author != "" {
		trendQueryDB = trendQueryDB.Where("author = ?", author)
	}
	_ = trendQueryDB.Scan(&sources).Error

	// 在 Go 端按日期聚合，同时构建开发者每日评分
	type dayAgg struct {
		scoreSum   float64
		scoreCount int64
		mrCount    int64
		changes    int64
	}
	aggMap := make(map[string]*dayAgg)
	// devDateMap[author][date] = {scoreSum, scoreCount, mrCount}
	devDateMap := make(map[string]map[string]*dayAgg)
	var minDate, maxDate time.Time
	hasData := false

	for _, s := range sources {
		// 确定有效日期：优先 mr_created_at，无效时用 synced_at
		var t time.Time
		if s.MRCreatedAt != nil && s.MRCreatedAt.Year() > 1970 {
			t = *s.MRCreatedAt
		} else if s.SyncedAt.Year() > 1970 {
			t = s.SyncedAt
		} else {
			continue
		}

		ds := t.Format("2006-01-02")
		if aggMap[ds] == nil {
			aggMap[ds] = &dayAgg{}
		}
		agg := aggMap[ds]
		agg.mrCount++
		agg.changes += int64(s.Additions + s.Deletions)
		if s.Score > 0 {
			agg.scoreSum += s.Score
			agg.scoreCount++
		}

		// 开发者维度聚合
		author := strings.TrimSpace(s.Author)
		if author != "" {
			if devDateMap[author] == nil {
				devDateMap[author] = make(map[string]*dayAgg)
			}
			if devDateMap[author][ds] == nil {
				devDateMap[author][ds] = &dayAgg{}
			}
			devAgg := devDateMap[author][ds]
			devAgg.mrCount++
			if s.Score > 0 {
				devAgg.scoreSum += s.Score
				devAgg.scoreCount++
			}
		}

		if !hasData || t.Before(minDate) {
			minDate = t
		}
		if !hasData || t.After(maxDate) {
			maxDate = t
		}
		hasData = true
	}

	// 确定趋势时间范围
	var startTrend, endTrend time.Time
	var trendDays int
	if hasData {
		startTrend = minDate.Truncate(24 * time.Hour)
		endTrend = maxDate.Truncate(24 * time.Hour)
		trendDays = int(endTrend.Sub(startTrend).Hours() / 24)
		if trendDays < 1 {
			trendDays = 1
		}
		// 限制最大 365 天
		if trendDays > 365 {
			startTrend = endTrend.AddDate(0, 0, -365)
			trendDays = 365
		}
	} else {
		// 无有效数据：兜底 90 天
		endTrend = time.Now().Local().Truncate(24 * time.Hour)
		startTrend = endTrend.AddDate(0, 0, -90)
		trendDays = 90
	}

	// 生成连续日期序列
	resp.Trend.Dates = make([]string, 0, trendDays+1)
	resp.Trend.Scores = make([]float64, 0, trendDays+1)
	resp.Trend.MRCounts = make([]int64, 0, trendDays+1)
	resp.Trend.Changes = make([]int64, 0, trendDays+1)
	for i := 0; i <= trendDays; i++ {
		d := startTrend.AddDate(0, 0, i)
		ds := d.Format("2006-01-02")
		resp.Trend.Dates = append(resp.Trend.Dates, ds)
		if agg, ok := aggMap[ds]; ok && agg != nil {
			avg := 0.0
			if agg.scoreCount > 0 {
				avg = agg.scoreSum / float64(agg.scoreCount)
			}
			resp.Trend.Scores = append(resp.Trend.Scores, avg)
			resp.Trend.MRCounts = append(resp.Trend.MRCounts, agg.mrCount)
			resp.Trend.Changes = append(resp.Trend.Changes, agg.changes)
		} else {
			resp.Trend.Scores = append(resp.Trend.Scores, 0)
			resp.Trend.MRCounts = append(resp.Trend.MRCounts, 0)
			resp.Trend.Changes = append(resp.Trend.Changes, 0)
		}
	}

	// 生成开发者趋势评分数据
	resp.Trend.DeveloperScores = make(map[string][]float64)
	for author, dateAggMap := range devDateMap {
		scores := make([]float64, 0, trendDays+1)
		for i := 0; i <= trendDays; i++ {
			d := startTrend.AddDate(0, 0, i)
			ds := d.Format("2006-01-02")
			if agg, ok := dateAggMap[ds]; ok && agg != nil && agg.scoreCount > 0 {
				scores = append(scores, agg.scoreSum/float64(agg.scoreCount))
			} else {
				scores = append(scores, 0)
			}
		}
		resp.Trend.DeveloperScores[author] = scores
	}

	c.JSON(200, gin.H{"data": resp})
}
