package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"html/template"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// ReportService 报表服务
type ReportService struct{}

func NewReportService() *ReportService {
	return &ReportService{}
}

// periodInfo 周期信息
type periodInfo struct {
	Name      string
	Title     string
	SubTitle  string
	Days      int
	Start     time.Time
	End       time.Time
	PrevStart time.Time
	PrevEnd   time.Time
}

// devRank 开发者排行
type devRank struct {
	Author            string
	AuthorDisplayName string
	DisplayName       string // 组装好的展示名：廖贞林（liaozhenlin）
	MRCount           int64
	AvgScore          float64
	ReviewCount       int64
	Changes           int64
}

// buildDisplayName 组装展示名：廖贞林（liaozhenlin）
func buildDisplayName(displayName, author string) string {
	if displayName != "" && author != "" {
		return displayName + "（" + author + "）"
	}
	if displayName != "" {
		return displayName
	}
	return author
}

// projectRank 项目排行
type projectRank struct {
	Project       string
	Count         int64
	AvgScore      float64
	LowQualityNum int64
	LowQualityPct string
}

// scoreDist 评分分布
type scoreDist struct {
	Excellent int64
	Good      int64
	Pass      int64
	Fail      int64
	Total     int64
}

// kpiData KPI数据
type kpiData struct {
	TotalMRs     int64
	TotalChanges int64
	AvgScore     float64
	LowQuality   int64
	// 第二行KPI
	Additions   int64     // 新增行数
	Deletions   int64     // 删除行数
	ReviewCount int64     // 代码Review次数
	StateDist   stateDist `gorm:"-"` // MR状态分布（非ORM字段）
}

// momData 环比数据
type momData struct {
	MRsTrend         string
	MRsChange        string
	AvgScoreTrend    string
	AvgScoreChange   string
	LowQualityTrend  string
	LowQualityChange string
	// 第二行KPI环比
	AdditionsTrend    string
	AdditionsChange   string
	ReviewCountTrend  string
	ReviewCountChange string
}

// stateDist 状态分布
type stateDist struct {
	Total  int64
	Merged int64
	Opened int64
	Closed int64
}

// sysStatus 系统状态
type sysStatus struct {
	ReviewSuccessRate string
	PoolHealth        string
	ModelHealth       string
	AvgReviewTime     string
	RuleTotalCount    int64
	VulnTotalCount    int64
}

// tokenUsageData Token 用量统计（用于周报月报，简化版：仅概览，无 TOP 排行）
type tokenUsageData struct {
	HasData          bool    // 控制整章节是否渲染 KV 表（0 时显示占位）
	TotalTokens      int64
	PromptTokens     int64
	CompletionTokens int64
	CallCount        int64
	AvgDurationSec   float64
	SuccessRateText  string // 预格式化 "99.8%"；call_count=0 时为 "N/A"
}

func buildPeriod(reportType string) periodInfo {
	now := time.Now()
	loc := now.Location()
	// 对齐到自然日期0点
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	var days int
	var title, name string
	if reportType == "monthly" {
		days = 30
		title = "AI CodeGuard 代码质量月报"
		name = "本月"
	} else {
		days = 7
		title = "AI CodeGuard 代码质量周报"
		name = "本周"
	}
	end := today.AddDate(0, 0, 1)       // 明天 00:00:00（不包含）
	start := today.AddDate(0, 0, -days) // 如 7 天前 00:00:00
	prevEnd := start
	prevStart := today.AddDate(0, 0, -days*2)

	var subtitle string
	if reportType == "monthly" {
		subtitle = fmt.Sprintf("%d年%02d月  |  %s — %s",
			now.Year(), now.Month(),
			start.Format("01月02日"), end.AddDate(0, 0, -1).Format("01月02日"))
	} else {
		year, week := today.ISOWeek()
		subtitle = fmt.Sprintf("%d年第%d周  |  %s — %s",
			year, week,
			start.Format("01月02日"), end.AddDate(0, 0, -1).Format("01月02日"))
	}
	return periodInfo{Name: name, Title: title, SubTitle: subtitle,
		Days: days, Start: start, End: end, PrevStart: prevStart, PrevEnd: prevEnd}
}

func queryKPI(start, end time.Time) kpiData {
	var k kpiData
	// MR 总数（不排除 closed）
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select("COUNT(*) as total_m_rs").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Scan(&k)
	// 评分与代码变更量（排除 closed MR）
	var scoreChangeKpi struct {
		TotalChanges int64   `gorm:"column:total_changes"`
		AvgScore     float64 `gorm:"column:avg_score"`
		ScoreCount   int64   `gorm:"column:score_count"`
	}
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select("COALESCE(SUM(additions + deletions), 0) as total_changes, "+
			"COALESCE(AVG(CASE WHEN score > 0 THEN score END), 0) as avg_score, "+
			"COUNT(CASE WHEN score > 0 THEN 1 END) as score_count").
		Where("mr_state != ?", "closed").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Scan(&scoreChangeKpi)
	k.TotalChanges = scoreChangeKpi.TotalChanges
	k.AvgScore = scoreChangeKpi.AvgScore

	// 低质量 MR（排除 closed）
	var lq int64
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Where("score > 0 AND score < 60").
		Where("mr_state != ?", "closed").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Count(&lq)
	k.LowQuality = lq

	// -------- 第二行 KPI --------
	// 新增/删除行数（排除 closed MR）
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select("COALESCE(SUM(additions), 0) as additions, COALESCE(SUM(deletions), 0) as deletions").
		Where("mr_state != ?", "closed").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Scan(&k)

	// 代码Review次数（不排除 closed）
	var rc int64
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select("COALESCE(SUM(review_count), 0)").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Scan(&rc)
	k.ReviewCount = rc

	// MR状态分布
	k.StateDist = queryStateDist(start, end)

	return k
}

func calcMOM(cur, prev kpiData, periodName string) momData {
	label := "环比"
	if strings.Contains(periodName, "月") {
		label = "环比上月"
	} else {
		label = "环比上周"
	}
	m := momData{MRsTrend: "flat", AvgScoreTrend: "flat", LowQualityTrend: "flat",
		MRsChange: "— " + label + "持平", AvgScoreChange: "— " + label + "持平", LowQualityChange: "— " + label + "持平"}

	// MR count
	if prev.TotalMRs == 0 && cur.TotalMRs > 0 {
		m.MRsTrend = "up"
		m.MRsChange = "↑ 新增"
	} else if prev.TotalMRs > 0 {
		p := float64(cur.TotalMRs-prev.TotalMRs) / float64(prev.TotalMRs) * 100
		if p > 0 {
			m.MRsTrend = "up"
			m.MRsChange = fmt.Sprintf("↑ %.1f%% %s", p, label)
		} else if p < 0 {
			m.MRsTrend = "down"
			m.MRsChange = fmt.Sprintf("↓ %.1f%% %s", -p, label)
		}
	}

	// Avg score
	if prev.AvgScore <= 0 && cur.AvgScore > 0 {
		m.AvgScoreTrend = "up"
		m.AvgScoreChange = "↑ 提升"
	} else if prev.AvgScore > 0 {
		p := (cur.AvgScore - prev.AvgScore) / prev.AvgScore * 100
		if p > 0.1 {
			m.AvgScoreTrend = "up"
			m.AvgScoreChange = fmt.Sprintf("↑ %.1f%% %s", p, label)
		} else if p < -0.1 {
			m.AvgScoreTrend = "down"
			m.AvgScoreChange = fmt.Sprintf("↓ %.1f%% %s", -p, label)
		}
	}

	// Low quality (absolute count)
	delta := cur.LowQuality - prev.LowQuality
	if delta > 0 {
		m.LowQualityTrend = "up"
		m.LowQualityChange = fmt.Sprintf("↑ %d 个 %s", delta, label)
	} else if delta < 0 {
		m.LowQualityTrend = "down"
		m.LowQualityChange = fmt.Sprintf("↓ %d 个 %s", -delta, label)
	}

	// -------- 第二行 KPI 环比 --------
	// 代码变更量（用 total_changes = additions + deletions 计算环比）
	m.AdditionsTrend = "flat"
	m.AdditionsChange = "— " + label + "持平"
	if prev.Additions+prev.Deletions == 0 && cur.Additions+cur.Deletions > 0 {
		m.AdditionsTrend = "up"
		m.AdditionsChange = "↑ 新增"
	} else if prev.Additions+prev.Deletions > 0 {
		p := float64((cur.Additions+cur.Deletions)-(prev.Additions+prev.Deletions)) / float64(prev.Additions+prev.Deletions) * 100
		if p > 0 {
			m.AdditionsTrend = "up"
			m.AdditionsChange = fmt.Sprintf("↑ %.1f%% %s", p, label)
		} else if p < 0 {
			m.AdditionsTrend = "down"
			m.AdditionsChange = fmt.Sprintf("↓ %.1f%% %s", -p, label)
		}
	}

	// ReviewCount
	m.ReviewCountTrend = "flat"
	m.ReviewCountChange = "— " + label + "持平"
	if prev.ReviewCount == 0 && cur.ReviewCount > 0 {
		m.ReviewCountTrend = "up"
		m.ReviewCountChange = "↑ 新增"
	} else if prev.ReviewCount > 0 {
		p := float64(cur.ReviewCount-prev.ReviewCount) / float64(prev.ReviewCount) * 100
		if p > 0 {
			m.ReviewCountTrend = "up"
			m.ReviewCountChange = fmt.Sprintf("↑ %.1f%% %s", p, label)
		} else if p < 0 {
			m.ReviewCountTrend = "down"
			m.ReviewCountChange = fmt.Sprintf("↓ %.1f%% %s", -p, label)
		}
	}

	return m
}

func queryDevRanks(start, end time.Time) []devRank {
	var list []devRank
	model.DB.Raw(`
		SELECT author, 
			MAX(author_display_name) as author_display_name,
			COUNT(*) as mr_count,
			COALESCE(AVG(CASE WHEN score > 0 THEN score END), 0) as avg_score,
			COALESCE(SUM(review_count), 0) as review_count,
			COALESCE(SUM(additions + deletions), 0) as changes
		FROM merge_request_review_logs
		WHERE author != ''
			AND mr_state != 'closed'
			AND COALESCE(mr_created_at, synced_at) >= ?
			AND COALESCE(mr_created_at, synced_at) < ?
		GROUP BY author
		HAVING mr_count > 0
		ORDER BY avg_score DESC, mr_count DESC
		LIMIT 5`, start, end).Scan(&list)
	for i := range list {
		list[i].DisplayName = buildDisplayName(list[i].AuthorDisplayName, list[i].Author)
	}
	return list
}

func queryProjectRanks(start, end time.Time) []projectRank {
	var list []projectRank
	model.DB.Raw(`
		SELECT project_name as project, COUNT(*) as count,
			COALESCE(AVG(CASE WHEN score > 0 THEN score END), 0) as avg_score,
			SUM(CASE WHEN score > 0 AND score < 60 THEN 1 ELSE 0 END) as low_quality_num
		FROM merge_request_review_logs
		WHERE mr_state != 'closed'
			AND COALESCE(mr_created_at, synced_at) >= ?
			AND COALESCE(mr_created_at, synced_at) < ?
		GROUP BY project_name
		ORDER BY count DESC
		LIMIT 5`, start, end).Scan(&list)
	for i := range list {
		if list[i].Count > 0 {
			list[i].LowQualityPct = fmt.Sprintf("%.0f%%", float64(list[i].LowQualityNum*100)/float64(list[i].Count))
		} else {
			list[i].LowQualityPct = "0%"
		}
	}
	return list
}

func queryLowQuality(start, end time.Time) []struct {
	Project             string  `json:"project"`
	Title               string  `json:"title"`
	Author              string  `json:"author"`
	AuthorDisplayName   string  `json:"author_display_name"`
	DisplayName         string  `json:"display_name"`
	Score               float64 `json:"score"`
	Additions           int     `json:"additions"`
	Deletions           int     `json:"deletions"`
} {
	var list []struct {
		Project           string  `json:"project"`
		Title             string  `json:"title"`
		Author            string  `json:"author"`
		AuthorDisplayName string  `json:"author_display_name"`
		DisplayName       string  `json:"display_name"`
		Score             float64 `json:"score"`
		Additions         int     `json:"additions"`
		Deletions         int     `json:"deletions"`
	}
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select("project_name as project, mr_title as title, author, MAX(author_display_name) as author_display_name, score, additions, deletions").
		Where("score > 0 AND score < 60").
		Where("mr_state != ?", "closed").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Group("project_name, mr_title, author, score, additions, deletions").
		Order("score ASC").Limit(5).
		Scan(&list)
	for i := range list {
		list[i].DisplayName = buildDisplayName(list[i].AuthorDisplayName, list[i].Author)
	}
	return list
}

func queryScoreDist(start, end time.Time) scoreDist {
	var d scoreDist
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select(
			"SUM(CASE WHEN score >= 90 THEN 1 ELSE 0 END) as excellent, "+
				"SUM(CASE WHEN score >= 80 AND score < 90 THEN 1 ELSE 0 END) as good, "+
				"SUM(CASE WHEN score >= 60 AND score < 80 THEN 1 ELSE 0 END) as pass, "+
				"SUM(CASE WHEN score < 60 THEN 1 ELSE 0 END) as fail, "+
				"COUNT(*) as total").
		Where("mr_state != ?", "closed").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Scan(&d)
	return d
}

func queryDevChanges(start, end time.Time) []struct {
	Author            string `json:"author"`
	AuthorDisplayName string `json:"author_display_name"`
	DisplayName       string `json:"display_name"`
	Changes           int64  `json:"changes"`
	Additions         int64  `json:"additions"`
	Deletions         int64  `json:"deletions"`
} {
	var list []struct {
		Author            string `json:"author"`
		AuthorDisplayName string `json:"author_display_name"`
		DisplayName       string `json:"display_name"`
		Changes           int64  `json:"changes"`
		Additions         int64  `json:"additions"`
		Deletions         int64  `json:"deletions"`
	}
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select("author, MAX(author_display_name) as author_display_name, COALESCE(SUM(additions + deletions), 0) as changes, COALESCE(SUM(additions), 0) as additions, COALESCE(SUM(deletions), 0) as deletions").
		Where("author != ''").
		Where("mr_state != ?", "closed").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Group("author").Order("changes DESC").Limit(5).
		Scan(&list)
	for i := range list {
		list[i].DisplayName = buildDisplayName(list[i].AuthorDisplayName, list[i].Author)
	}
	return list
}

func queryStateDist(start, end time.Time) stateDist {
	var s stateDist
	model.DB.Model(&model.MergeRequestReviewLog{}).
		Select(
			"COUNT(*) as total, "+
				"SUM(CASE WHEN mr_state = 'merged' THEN 1 ELSE 0 END) as merged, "+
				"SUM(CASE WHEN mr_state = 'opened' THEN 1 ELSE 0 END) as opened, "+
				"SUM(CASE WHEN mr_state = 'closed' THEN 1 ELSE 0 END) as closed").
		Where("COALESCE(mr_created_at, synced_at) >= ?", start).
		Where("COALESCE(mr_created_at, synced_at) < ?", end).
		Scan(&s)
	return s
}

// queryTokenUsage 查询本周期 LLM 调用消耗概览。
// 数据源：llm_call_logs，过滤 call_type='score'，与 token-usage.html 保持一致。
// 周报月报是 admin 视角，不做用户级过滤。
func queryTokenUsage(start, end time.Time) tokenUsageData {
	var stats struct {
		TotalTokens      int64   `gorm:"column:total_tokens"`
		PromptTokens     int64   `gorm:"column:prompt_tokens"`
		CompletionTokens int64   `gorm:"column:completion_tokens"`
		CallCount        int64   `gorm:"column:call_count"`
		FailedCount      int64   `gorm:"column:failed_count"`
		AvgDurationMs    float64 `gorm:"column:avg_duration_ms"`
	}
	model.DB.Model(&model.LLMCallLog{}).
		Select(`COALESCE(SUM(total_tokens), 0)            AS total_tokens,
			COALESCE(SUM(prompt_tokens), 0)               AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0)           AS completion_tokens,
			COUNT(*)                                      AS call_count,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) AS failed_count,
			COALESCE(AVG(duration_ms), 0)                 AS avg_duration_ms`).
		Where("call_type = ?", model.CallTypeScore).
		Where("created_at >= ? AND created_at < ?", start, end).
		Scan(&stats)

	t := tokenUsageData{
		TotalTokens:      stats.TotalTokens,
		PromptTokens:     stats.PromptTokens,
		CompletionTokens: stats.CompletionTokens,
		CallCount:        stats.CallCount,
		AvgDurationSec:   stats.AvgDurationMs / 1000.0,
	}
	t.HasData = stats.CallCount > 0
	if stats.CallCount > 0 {
		successCount := stats.CallCount - stats.FailedCount
		t.SuccessRateText = fmt.Sprintf("%.1f%%", float64(successCount*100)/float64(stats.CallCount))
	} else {
		t.SuccessRateText = "N/A"
	}
	return t
}

func querySysStatus() sysStatus {
	// 审查成功率（基于 task 表，排除 stopped 状态）
	var taskStats struct {
		Total int64
		Done  int64
	}
	now := time.Now()
	model.DB.Model(&model.Task{}).
		Select("COUNT(*) as total, COUNT(CASE WHEN status = 'success' THEN 1 END) as done").
		Where("status != ?", model.TaskStopped).
		Where("created_at >= ?", now.AddDate(0, 0, -7)).
		Scan(&taskStats)
	successRate := "N/A"
	if taskStats.Total > 0 {
		successRate = fmt.Sprintf("%.1f%%", float64(taskStats.Done*100)/float64(taskStats.Total))
	}

	// 资源池健康度
	var poolStats struct {
		Total   int64
		Healthy int64
	}
	model.DB.Model(&model.ResourcePool{}).
		Select("COUNT(*) as total, COUNT(CASE WHEN status = 'active' THEN 1 END) as healthy").
		Scan(&poolStats)
	poolHealth := fmt.Sprintf("%d/%d", poolStats.Healthy, poolStats.Total)

	// 平均审查耗时（使用 duration_sec 字段）
	var avgTime struct{ AvgSec float64 }
	model.DB.Model(&model.Task{}).
		Select("COALESCE(AVG(duration_sec), 0) as avg_sec").
		Where("created_at >= ? AND duration_sec > 0", now.AddDate(0, 0, -7)).
		Scan(&avgTime)
	avgReview := "N/A"
	if avgTime.AvgSec > 0 {
		m := int(avgTime.AvgSec) / 60
		sec := int(avgTime.AvgSec) % 60
		avgReview = fmt.Sprintf("%d分 %d秒", m, sec)
	}

	// 大模型健康度
	var modelStats struct {
		Total   int64
		Healthy int64
	}
	model.DB.Model(&model.LLMModel{}).
		Select("COUNT(*) as total, COUNT(CASE WHEN status = 'active' THEN 1 END) as healthy").
		Scan(&modelStats)
	modelHealth := fmt.Sprintf("%d/%d", modelStats.Healthy, modelStats.Total)

	// 评审规则总数
	var ruleTotal int64
	model.DB.Model(&model.ReviewRule{}).Count(&ruleTotal)

	// 漏洞库总数
	var vulnTotal int64
	model.DB.Model(&model.VulnerabilityRecord{}).Count(&vulnTotal)

	return sysStatus{
		ReviewSuccessRate: successRate,
		PoolHealth:        poolHealth,
		ModelHealth:       modelHealth,
		AvgReviewTime:     avgReview,
		RuleTotalCount:    ruleTotal,
		VulnTotalCount:    vulnTotal,
	}
}

// GenerateHTML 生成报告 HTML
func (s *ReportService) GenerateHTML(reportType string) (string, error) {
	p := buildPeriod(reportType)
	curKpi := queryKPI(p.Start, p.End)
	prevKpi := queryKPI(p.PrevStart, p.PrevEnd)
	mom := calcMOM(curKpi, prevKpi, p.Name)
	devRanks := queryDevRanks(p.Start, p.End)
	projectRanks := queryProjectRanks(p.Start, p.End)
	lowQuality := queryLowQuality(p.Start, p.End)
	dist := queryScoreDist(p.Start, p.End)
	devChanges := queryDevChanges(p.Start, p.End)
	sys := querySysStatus()
	tokenUsage := queryTokenUsage(p.Start, p.End)

	// 获取本周/本月总MR数用于百分比计算
	if dist.Total == 0 {
		dist.Total = 1
	}

	data := map[string]interface{}{
		"Title":          p.Title,
		"SubTitle":       p.SubTitle,
		"PeriodName":     p.Name,
		"TotalMRs":       curKpi.TotalMRs,
		"TotalChanges":   curKpi.TotalChanges,
		"AvgScore":       fmt.Sprintf("%.1f", curKpi.AvgScore),
		"LowQuality":     curKpi.LowQuality,
		"MomMRs":         mom.MRsChange,
		"MomMRTrend":     mom.MRsTrend,
		"MomAvg":         mom.AvgScoreChange,
		"MomAvgTrend":    mom.AvgScoreTrend,
		"MomLQ":          mom.LowQualityChange,
		"MomLQTrend":     mom.LowQualityTrend,
		// 第二行 KPI
		"Additions":           curKpi.Additions,
		"Deletions":           curKpi.Deletions,
		"ReviewCount":         curKpi.ReviewCount,
		"StateDist":           curKpi.StateDist,
		"MomAdditions":        mom.AdditionsChange,
		"MomAdditionsTrend":   mom.AdditionsTrend,
		"MomReviewCount":      mom.ReviewCountChange,
		"MomReviewCountTrend": mom.ReviewCountTrend,
		"Dist":                dist,
		"DevRanks":            devRanks,
		"ProjectRanks":        projectRanks,
		"LowQualityList":      lowQuality,
		"DevChanges":          devChanges,
		"SysStatus":           sys,
		"TokenUsage":          tokenUsage,
		"Now":                 time.Now().Format("2006-01-02 15:04"),
	}

	return s.renderTemplate(data)
}

// SendEmail 发送邮件
func (s *ReportService) SendEmail(reportType string, html string, groupNames []string) error {
	var cfg model.SMTPConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		return fmt.Errorf("smtp not configured")
	}

	query := model.DB.Where("enabled = ?", true)
	if len(groupNames) > 0 {
		query = query.Where("group_name IN ?", groupNames)
	}
	var recipients []model.ReportRecipient
	query.Find(&recipients)
	if len(recipients) == 0 {
		return fmt.Errorf("no enabled recipients for selected groups")
	}

	p := buildPeriod(reportType)
	subject := fmt.Sprintf("【AI CodeGuard】%s — %s", p.Title, time.Now().Format("2006-01-02"))

	toEmails := make([]string, 0, len(recipients))
	var invalidEmails []string
	for _, r := range recipients {
		email := strings.TrimSpace(r.Email)
		if email == "" {
			invalidEmails = append(invalidEmails, fmt.Sprintf("id=%d(name=%s)邮箱为空", r.ID, r.Name))
			continue
		}
		toEmails = append(toEmails, email)
	}
	if len(invalidEmails) > 0 {
		zap.L().Warn("report mail skip invalid recipients", zap.Strings("invalid", invalidEmails))
	}
	if len(toEmails) == 0 {
		return fmt.Errorf("no valid recipients for selected groups")
	}

	mime := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
	msg := fmt.Sprintf("Subject: %s\nFrom: %s <%s>\nTo: %s\n%s\n%s",
		subject, cfg.FromName, cfg.FromEmail, strings.Join(toEmails, ","), mime, html)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	err := sendMailWithLogin(addr, cfg.FromEmail, toEmails, []byte(msg), cfg.Username, cfg.Password, cfg.UseTLS)
	if err != nil {
		return fmt.Errorf("send email failed: %w", err)
	}

	zap.L().Info("report mail sent", zap.String("type", reportType), zap.Strings("to", toEmails), zap.Strings("groups", groupNames))
	return nil
}

// ResendEmail 根据历史日志重新发送邮件
func (s *ReportService) ResendEmail(log *model.ReportLog) error {
	var cfg model.SMTPConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		return fmt.Errorf("smtp not configured")
	}

	// 从历史记录的 recipients 获取邮箱列表
	var recipients []model.ReportRecipient
	if err := json.Unmarshal([]byte(log.Recipients), &recipients); err != nil {
		return fmt.Errorf("invalid recipients data")
	}

	toEmails := make([]string, 0, len(recipients))
	for _, r := range recipients {
		email := strings.TrimSpace(r.Email)
		if email != "" {
			toEmails = append(toEmails, email)
		}
	}
	if len(toEmails) == 0 {
		return fmt.Errorf("no valid recipients")
	}

	p := buildPeriod(log.ReportType)
	subject := fmt.Sprintf("【AI CodeGuard】%s 重发 — %s", p.Title, time.Now().Format("2006-01-02"))

	mime := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
	msg := fmt.Sprintf("Subject: %s\nFrom: %s <%s>\nTo: %s\n%s\n%s",
		subject, cfg.FromName, cfg.FromEmail, strings.Join(toEmails, ","), mime, log.HtmlContent)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	if err := sendMailWithLogin(addr, cfg.FromEmail, toEmails, []byte(msg), cfg.Username, cfg.Password, cfg.UseTLS); err != nil {
		return fmt.Errorf("send email failed: %w", err)
	}

	zap.L().Info("report mail resent", zap.Uint("log_id", log.ID), zap.String("type", log.ReportType), zap.Strings("to", toEmails))
	return nil
}

// TestSMTP 测试 SMTP 连接
func (s *ReportService) TestSMTP(cfg *model.SMTPConfig) error {
	mime := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
	body := "<h2>测试成功</h2><p>您的 SMTP 配置已验证通过，可以正常发送报告邮件。</p>"
	msg := fmt.Sprintf("Subject: %s\nFrom: %s <%s>\nTo: %s\n%s\n%s",
		"【AI CodeGuard】SMTP 连接测试", cfg.FromName, cfg.FromEmail, cfg.FromEmail, mime, body)
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	return sendMailWithLogin(addr, cfg.FromEmail, []string{cfg.FromEmail}, []byte(msg), cfg.Username, cfg.Password, cfg.UseTLS)
}

// BuildCronExpression 根据配置生成 Cron 表达式
func (s *ReportService) BuildCronExpression(cfg *model.ReportConfig) string {
	if cfg.ReportType == "weekly" {
		return fmt.Sprintf("0 %d %d * * %d", cfg.SendMinute, cfg.SendHour, cfg.SendDayOfWeek)
	}
	return fmt.Sprintf("0 %d %d %d * *", cfg.SendMinute, cfg.SendHour, cfg.SendDayOfMonth)
}

// padPct 保证百分比条总和接近 100，避免四舍五入后加起来不到 100%
func padPct(parts []float64) []int {
	vals := make([]int, len(parts))
	sum := 0
	for i, v := range parts {
		vals[i] = int(v + 0.5)
		sum += vals[i]
	}
	if sum > 0 && sum != 100 && len(vals) > 0 {
		vals[0] += 100 - sum
	}
	return vals
}

func (s *ReportService) renderTemplate(data map[string]interface{}) (string, error) {
	// 评分分布百分比
	dist := data["Dist"].(scoreDist)
	total := float64(dist.Total)
	if total <= 0 {
		total = 1
	}
	pctParts := []float64{
		float64(dist.Excellent*100) / total,
		float64(dist.Good*100) / total,
		float64(dist.Pass*100) / total,
		float64(dist.Fail*100) / total,
	}
	pctInts := padPct(pctParts)
	data["DistExcellentPct"] = pctInts[0]
	data["DistGoodPct"] = pctInts[1]
	data["DistPassPct"] = pctInts[2]
	data["DistFailPct"] = pctInts[3]

	// 状态分布百分比
	sd := data["StateDist"].(stateDist)
	stTotal := sd.Total
	if stTotal <= 0 {
		stTotal = 1
	}
	stateParts := []float64{
		float64(sd.Merged*100) / float64(stTotal),
		float64(sd.Opened*100) / float64(stTotal),
		float64(sd.Closed*100) / float64(stTotal),
	}
	stateInts := padPct(stateParts)
	data["StateMergedPct"] = stateInts[0]
	data["StateOpenedPct"] = stateInts[1]
	data["StateClosedPct"] = stateInts[2]

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"ge":  func(a, b float64) bool { return a >= b },
		"le":  func(a, b float64) bool { return a <= b },
		"perc": func(n, t int64) string {
			if t == 0 {
				return "0%"
			}
			return fmt.Sprintf("%.0f%%", float64(n*100)/float64(t))
		},
		"scoreBadgeClass": func(score float64) string {
			if score >= 90 {
				return "score-excellent"
			}
			if score >= 80 {
				return "score-good"
			}
			if score >= 60 {
				return "score-pass"
			}
			return "score-fail"
		},
		"rankClass": func(i int) string {
			if i == 0 {
				return "rank-1"
			}
			if i == 1 {
				return "rank-2"
			}
			if i == 2 {
				return "rank-3"
			}
			return "rank-other"
		},
		"numberFormat": func(n int64) string {
			// 短格式化：23756 → 23.8K，532 → 532
			if n >= 1000000 {
				return fmt.Sprintf("%.1fM", float64(n)/1000000)
			}
			if n >= 1000 {
				return fmt.Sprintf("%.1fK", float64(n)/1000)
			}
			return fmt.Sprintf("%d", n)
		},
	}

	tmpl, err := template.New("report").Funcs(funcMap).Parse(reportTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const reportTemplate = `<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN" "http://www.w3.org/TR/html4/loose.dtd">
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=UTF-8">
<title>{{.Title}}</title>
</head>
<body style="margin:0;padding:0;" bgcolor="#e8eaed">
<table cellpadding="0" cellspacing="0" border="0" width="852" bgcolor="#e8eaed"><tr><td align="center">
<table cellpadding="0" cellspacing="0" border="0" width="900" bgcolor="#ffffff">
<tr><td>

<!-- Header -->
<table cellpadding="0" cellspacing="0" border="0" width="900" bgcolor="#667eea"><tr><td align="center" style="padding:32px 28px;">
<font face="Arial,Helvetica,sans-serif" size="5" color="#ffffff"><b>{{.Title}}</b></font><br><br>
<font face="Arial,Helvetica,sans-serif" size="2" color="#ffffff">{{.SubTitle}}</font>
</td></tr></table>

<!-- Body -->
<table cellpadding="0" cellspacing="0" border="0" width="900"><tr><td style="padding:32px 28px;">

<!-- KPI -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td>
<!-- 第一行 -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr>
<td width="33.33%" valign="top" style="padding:0 2px 4px 0;">
<table cellpadding="0" cellspacing="0" border="0" width="100%" bgcolor="#f8f9ff"><tr><td align="center" style="padding:16px 6px;">
<font face="Arial,Helvetica,sans-serif" size="6" color="#667eea"><b>{{.TotalMRs}}</b></font><br>
<font face="Arial,Helvetica,sans-serif" size="1" color="#666666">新增 MR</font><br>
<font face="Arial,Helvetica,sans-serif" size="1">{{if eq .MomMRTrend "up"}}<font color="#4caf50">{{.MomMRs}}</font>{{else if eq .MomMRTrend "down"}}<font color="#f44336">{{.MomMRs}}</font>{{else}}<font color="#999999">{{.MomMRs}}</font>{{end}}</font>
</td></tr></table>
</td>
<td width="33.33%" valign="top" style="padding:0 2px 4px 2px;">
<table cellpadding="0" cellspacing="0" border="0" width="100%" bgcolor="#f8f9ff"><tr><td align="center" style="padding:16px 6px;">
<font face="Arial,Helvetica,sans-serif" size="6" color="#667eea"><b>{{.AvgScore}}</b></font><br>
<font face="Arial,Helvetica,sans-serif" size="1" color="#666666">平均评分</font><br>
<font face="Arial,Helvetica,sans-serif" size="1">{{if eq .MomAvgTrend "up"}}<font color="#4caf50">{{.MomAvg}}</font>{{else if eq .MomAvgTrend "down"}}<font color="#f44336">{{.MomAvg}}</font>{{else}}<font color="#999999">{{.MomAvg}}</font>{{end}}</font>
</td></tr></table>
</td>
<td width="33.33%" valign="top" style="padding:0 0 4px 2px;">
<table cellpadding="0" cellspacing="0" border="0" width="100%" bgcolor="#f8f9ff"><tr><td align="center" style="padding:16px 6px;">
<font face="Arial,Helvetica,sans-serif" size="6" color="#667eea"><b>{{.LowQuality}}</b></font><br>
<font face="Arial,Helvetica,sans-serif" size="1" color="#666666">低质量 MR</font><br>
<font face="Arial,Helvetica,sans-serif" size="1">{{if eq .MomLQTrend "down"}}<font color="#4caf50">{{.MomLQ}}</font>{{else if eq .MomLQTrend "up"}}<font color="#f44336">{{.MomLQ}}</font>{{else}}<font color="#999999">{{.MomLQ}}</font>{{end}}</font>
</td></tr></table>
</td>
</tr></table>
<!-- 第二行 -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr>
<td width="33.33%" valign="top" style="padding:4px 2px 0 0;">
<table cellpadding="0" cellspacing="0" border="0" width="100%" bgcolor="#f8f9ff"><tr><td align="center" style="padding:16px 6px;">
<font face="Arial,Helvetica,sans-serif" size="6" color="#667eea"><b>{{numberFormat .TotalChanges}}</b></font><br>
<font face="Arial,Helvetica,sans-serif" size="1" color="#666666">代码变更量</font><br>
<font face="Arial,Helvetica,sans-serif" size="1">{{if eq .MomAdditionsTrend "up"}}<font color="#4caf50">{{.MomAdditions}}</font>{{else if eq .MomAdditionsTrend "down"}}<font color="#f44336">{{.MomAdditions}}</font>{{else}}<font color="#999999">{{.MomAdditions}}</font>{{end}}</font>
</td></tr></table>
</td>
<td width="33.33%" valign="top" style="padding:4px 2px 0 2px;">
<table cellpadding="0" cellspacing="0" border="0" width="100%" bgcolor="#f8f9ff"><tr><td align="center" style="padding:16px 6px;">
<font face="Arial,Helvetica,sans-serif" size="6" color="#667eea"><b>{{.ReviewCount}}</b></font><br>
<font face="Arial,Helvetica,sans-serif" size="1" color="#666666">代码 Review 次数</font><br>
<font face="Arial,Helvetica,sans-serif" size="1">{{if eq .MomReviewCountTrend "up"}}<font color="#4caf50">{{.MomReviewCount}}</font>{{else if eq .MomReviewCountTrend "down"}}<font color="#f44336">{{.MomReviewCount}}</font>{{else}}<font color="#999999">{{.MomReviewCount}}</font>{{end}}</font>
</td></tr></table>
</td>
<td width="33.33%" valign="top" style="padding:4px 0 0 2px;">
<table cellpadding="0" cellspacing="0" border="0" width="100%" bgcolor="#f8f9ff"><tr><td align="center" style="padding:16px 6px;">
<font face="Arial,Helvetica,sans-serif" size="6" color="#667eea"><b>{{.StateDist.Merged}}/{{.StateDist.Opened}}/{{.StateDist.Closed}}</b></font><br>
<font face="Arial,Helvetica,sans-serif" size="1" color="#666666">merged / opened / closed</font><br>
<font face="Arial,Helvetica,sans-serif" size="1" color="#999999">MR 状态分布</font>
</td></tr></table>
</td>
</tr></table>
</td></tr></table>

<br>
<!-- Score Distribution -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td>
<font face="Arial,Helvetica,sans-serif" size="3" color="#1a1a2e"><b>&#128202; 评分分布情况</b></font>
<br><br>
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr>
{{if gt .Dist.Excellent 0}}<td width="{{.DistExcellentPct}}%" align="center" bgcolor="#4caf50" height="24"><font face="Arial,Helvetica,sans-serif" size="1" color="#ffffff"><b>{{.DistExcellentPct}}%</b></font></td>{{end}}
{{if gt .Dist.Good 0}}<td width="{{.DistGoodPct}}%" align="center" bgcolor="#2196f3" height="24"><font face="Arial,Helvetica,sans-serif" size="1" color="#ffffff"><b>{{.DistGoodPct}}%</b></font></td>{{end}}
{{if gt .Dist.Pass 0}}<td width="{{.DistPassPct}}%" align="center" bgcolor="#ff9800" height="24"><font face="Arial,Helvetica,sans-serif" size="1" color="#ffffff"><b>{{.DistPassPct}}%</b></font></td>{{end}}
{{if gt .Dist.Fail 0}}<td width="{{.DistFailPct}}%" align="center" bgcolor="#f44336" height="24"><font face="Arial,Helvetica,sans-serif" size="1" color="#ffffff"><b>{{.DistFailPct}}%</b></font></td>{{end}}
</tr></table>
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr>
<td width="25%" align="center" style="padding-top:6px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><font color="#4caf50">&#9679;</font> 优秀 {{.Dist.Excellent}}个</font></td>
<td width="25%" align="center" style="padding-top:6px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><font color="#2196f3">&#9679;</font> 良好 {{.Dist.Good}}个</font></td>
<td width="25%" align="center" style="padding-top:6px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><font color="#ff9800">&#9679;</font> 及格 {{.Dist.Pass}}个</font></td>
<td width="25%" align="center" style="padding-top:6px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><font color="#f44336">&#9679;</font> 不及格 {{.Dist.Fail}}个</font></td>
</tr></table>
</td></tr></table>

<br><br>
<!-- Developer Rankings -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td>
<font face="Arial,Helvetica,sans-serif" size="3" color="#1a1a2e"><b>&#127942; 开发者排行榜TOP5</b></font>
<br><br>
<table cellpadding="0" cellspacing="0" border="0" width="852">
<tr bgcolor="#fafafc">
<td style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>排名</b></font></td>
<td style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>开发者</b></font></td>
<td align="center" style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>提交数</b></font></td>
<td align="center" style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>平均分</b></font></td>
<td align="center" style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>Review次数</b></font></td>
</tr>
{{range $i, $d := .DevRanks}}
<tr>
<td style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">{{add $i 1}}</font></td>
<td style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333">{{if lt $i 3}}<b>{{$d.DisplayName}}</b>{{else}}{{$d.DisplayName}}{{end}}</font></td>
<td align="center" style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2">{{$d.MRCount}}</font></td>
<td align="center" style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2">{{printf "%.1f" $d.AvgScore}}</font></td>
<td align="center" style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2">{{$d.ReviewCount}}</font></td>
</tr>
{{end}}
{{if not .DevRanks}}<tr><td colspan="5" align="center" style="padding:20px;"><font face="Arial,Helvetica,sans-serif" size="2" color="#999999">暂无数据</font></td></tr>{{end}}
</table>
</td></tr></table>

<br><br>
<!-- Project Rankings -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td>
<font face="Arial,Helvetica,sans-serif" size="3" color="#1a1a2e"><b>&#128193; 项目活跃度 TOP5</b></font>
<br><br>
<table cellpadding="0" cellspacing="0" border="0" width="852">
<tr bgcolor="#fafafc">
<td style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>排名</b></font></td>
<td style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>项目</b></font></td>
<td align="center" style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>MR数</b></font></td>
<td align="center" style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>平均分</b></font></td>
<td align="center" style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>低质量占比</b></font></td>
</tr>
{{range $i, $p := .ProjectRanks}}
<tr>
<td style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">{{add $i 1}}</font></td>
<td style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333">{{if lt $i 3}}<b>{{$p.Project}}</b>{{else}}{{$p.Project}}{{end}}</font></td>
<td align="center" style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2">{{$p.Count}}</font></td>
<td align="center" style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2">{{printf "%.1f" $p.AvgScore}}</font></td>
<td align="center" style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2">{{$p.LowQualityPct}}</font></td>
</tr>
{{end}}
{{if not .ProjectRanks}}<tr><td colspan="5" align="center" style="padding:20px;"><font face="Arial,Helvetica,sans-serif" size="2" color="#999999">暂无数据</font></td></tr>{{end}}
</table>
</td></tr></table>

<br><br>
<!-- Developer Changes -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td>
<font face="Arial,Helvetica,sans-serif" size="3" color="#1a1a2e"><b>&#128221; 人员代码变更 TOP5</b></font>
<br><br>
<table cellpadding="0" cellspacing="0" border="0" width="852">
<tr bgcolor="#fafafc">
<td style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>排名</b></font></td>
<td style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>开发者</b></font></td>
<td align="center" style="padding:8px 10px;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>代码变更</b></font></td>
</tr>
{{range $i, $d := .DevChanges}}
<tr>
<td style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">{{add $i 1}}</font></td>
<td style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333">{{if lt $i 3}}<b>{{$d.DisplayName}}</b>{{else}}{{$d.DisplayName}}{{end}}</font></td>
<td align="center" style="padding:10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2">{{$d.Changes}}(<font color="#4caf50">+{{$d.Additions}}</font>/<font color="#f44336">-{{$d.Deletions}}</font>)</font></td>
</tr>
{{end}}
{{if not .DevChanges}}<tr><td colspan="3" align="center" style="padding:20px;"><font face="Arial,Helvetica,sans-serif" size="2" color="#999999">暂无数据</font></td></tr>{{end}}
</table>
</td></tr></table>

<br><br>
<!-- Low Quality Alerts -->
{{if .LowQualityList}}
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td style="padding:20px 0px;">
<font face="Arial,Helvetica,sans-serif" size="3" color="#1a1a2e"><b>&#128680; 低质量 MR 列表（评分 &lt; 60）</b></font>
<br><br>
<table cellpadding="0" cellspacing="0" border="0" width="852">
  <tr>
    <td width="12%" style="padding:6px 8px;border-bottom:1px solid #e0e0e0;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>项目</b></font></td>
    <td width="53%" style="padding:6px 8px;border-bottom:1px solid #e0e0e0;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>MR 标题</b></font></td>
    <td width="20%" align="center" style="padding:6px 8px;border-bottom:1px solid #e0e0e0;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>作者</b></font></td>
    <td width="5%" align="center" style="padding:6px 8px;border-bottom:1px solid #e0e0e0;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>评分</b></font></td>
    <td width="10%" align="center" style="padding:6px 8px;border-bottom:1px solid #e0e0e0;"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666"><b>变更量</b></font></td>
  </tr>
{{range .LowQualityList}}
  <tr>
    <td width="12%" style="padding:8px;border-bottom:1px solid #f0f0f0;" valign="top"><font face="Arial,Helvetica,sans-serif" size="1" color="#333333"><b>{{.Project}}</b></font></td>
    <td width="53%" style="padding:8px;border-bottom:1px solid #f0f0f0;word-break:break-all;" valign="top"><font face="Arial,Helvetica,sans-serif" size="1" color="#333333">{{.Title}}</font></td>
    <td width="20%" align="center" style="padding:8px;border-bottom:1px solid #f0f0f0;" valign="top"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666">{{.DisplayName}}</font></td>
    <td width="5%" align="center" style="padding:8px;border-bottom:1px solid #f0f0f0;" valign="top"><font face="Arial,Helvetica,sans-serif" size="2" color="#c62828"><b>{{printf "%.0f" .Score}}</b></font></td>
    <td width="10%" align="center" style="padding:8px;border-bottom:1px solid #f0f0f0;" valign="top"><font face="Arial,Helvetica,sans-serif" size="1" color="#666666">+{{.Additions}} / -{{.Deletions}}</font></td>
  </tr>
{{end}}
</table>
</td></tr></table>
<br>
{{end}}

<br><br>
<!-- AI 评审 Token 用量（简化版：仅概览，无 TOP 排行；风格复用系统状态区 KV 表） -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td>
<font face="Arial,Helvetica,sans-serif" size="3" color="#1a1a2e"><b>&#128202; Token用量</b></font>
<br><br>
{{if .TokenUsage.HasData}}
<font face="Arial,Helvetica,sans-serif" size="1" color="#666666">{{.PeriodName}} LLM 调用消耗概览</font>
<br>
<table cellpadding="0" cellspacing="0" border="0" width="852">
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">总 Token</font></td>
    <td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{numberFormat .TokenUsage.TotalTokens}}</b></font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">调用次数</font></td>
    <td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{.TokenUsage.CallCount}} 次</b></font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">输入/输出 token</font></td>
    <td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{numberFormat .TokenUsage.PromptTokens}} / {{numberFormat .TokenUsage.CompletionTokens}}</b></font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">平均耗时</font></td>
    <td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{printf "%.1f" .TokenUsage.AvgDurationSec}} 秒</b></font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">调用成功率</font></td>
    <td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{.TokenUsage.SuccessRateText}}</b></font></td></tr>
</table>
{{else}}
<table cellpadding="0" cellspacing="0" border="0" width="852">
<tr><td align="center" style="padding:20px;border-top:1px solid #f0f0f5;">
<font face="Arial,Helvetica,sans-serif" size="2" color="#999999">{{.PeriodName}}暂无 LLM 调用记录</font>
</td></tr></table>
{{end}}
</td></tr></table>

<br><br><br>

<!-- System Status -->
<table cellpadding="0" cellspacing="0" border="0" width="852"><tr><td>
<font face="Arial,Helvetica,sans-serif" size="3" color="#1a1a2e"><b>&#9881;&#65039; 系统状态</b></font>
<br><br>
<table cellpadding="0" cellspacing="0" border="0" width="852">
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">AI 审查成功率</font></td>
<td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{.SysStatus.ReviewSuccessRate}}</b></font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">资源池健康度</font></td>
<td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#4caf50"><b>&#9989; 正常</b></font><font face="Arial,Helvetica,sans-serif" size="2" color="#666666"> ({{.SysStatus.PoolHealth}})</font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">大模型健康度</font></td>
<td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#4caf50"><b>&#9989; 正常</b></font><font face="Arial,Helvetica,sans-serif" size="2" color="#666666"> ({{.SysStatus.ModelHealth}})</font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">平均审查耗时</font></td>
<td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{.SysStatus.AvgReviewTime}}</b></font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">评审规则总数</font></td>
<td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{.SysStatus.RuleTotalCount}} 条</b></font></td></tr>
<tr><td style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#666666">漏洞库总数</font></td>
<td align="right" style="padding:8px 10px;border-top:1px solid #f0f0f5;"><font face="Arial,Helvetica,sans-serif" size="2" color="#333333"><b>{{.SysStatus.VulnTotalCount}} 条</b></font></td></tr>
</table>
</td></tr></table>

</td></tr></table>

<!-- Footer -->
<table cellpadding="0" cellspacing="0" border="0" width="900" bgcolor="#f8f9ff"><tr><td align="center" style="padding:20px 28px;border-top:1px solid #e8e9f0;">
<font face="Arial,Helvetica,sans-serif" size="1" color="#999999">本报告由 <b>AI CodeGuard</b> 自动生成 — {{.Now}}</font>
</td></tr></table>

</td></tr></table>
</td></tr></table>
</body>
</html>`
