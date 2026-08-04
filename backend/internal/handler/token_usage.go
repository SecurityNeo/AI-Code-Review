package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// callerLabelMap 调用方英文标识 → 中文名称
var callerLabelMap = map[string]string{
	"score":                 "代码评审",
	"rule_refine":           "智能提炼",
	"sandbox_test":          "模拟测试",
	"similar_check":         "相似检测",
	"rule_incubator":        "规则孵化平台",
	"retry":                 "解析重试",
	"runAIReview":           "AI 评审",
	"runAIReviewStructured": "AI 评审（结构化）",
	"runAIReviewFallback":   "AI 评审（分批）",
	"callLLMAPI":            "LLM 调用",
}

// incubatorCallers 是规则孵化平台相关的 caller 标识列表
var incubatorCallers = []string{"rule_refine", "sandbox_test", "similar_check"}

// incubatorCallersInSQL 生成用于 SQL IN 子句的字符串（如 'rule_refine','sandbox_test'）
func incubatorCallersInSQL() string {
	return "'" + strings.Join(incubatorCallers, "','") + "'"
}

type TokenUsageHandler struct{}

func NewTokenUsageHandler() *TokenUsageHandler {
	return &TokenUsageHandler{}
}

// 合法 range 白名单
var allowedRanges = map[string]bool{
	"today": true,
	"7d":    true,
	"30d":   true,
}

// parseRange 解析时间范围参数，返回起止时间（滚动窗口）。
// range=today → 今天 00:00 ~ 现在
// range=7d   → 现在前 7*24h ~ 现在（默认，滚动窗口）
// range=30d  → 现在前 30*24h ~ 现在
// 非法值默认按 7d 处理（避免静默接受任意字符串）。
func parseRange(c *gin.Context) (time.Time, time.Time) {
	now := time.Now()
	r := c.DefaultQuery("range", "7d")
	if !allowedRanges[r] {
		r = "7d"
	}
	switch r {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), now
	case "30d":
		return now.AddDate(0, 0, -30), now
	default:
		return now.AddDate(0, 0, -7), now
	}
}

// parsePagination 解析分页参数，返回 (page, pageSize)，带边界保护。
func parsePagination(c *gin.Context, defaultSize, maxSize int) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(defaultSize)))
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > maxSize {
		pageSize = defaultSize
	}
	return page, pageSize
}

// currentUserOrAbort 获取当前登录用户；未登录或 token 无效返 401。
// 统一处理与 dashboard.go 风格一致。
func currentUserOrAbort(c *gin.Context) (model.User, bool) {
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return model.User{}, false
	}
	return user, true
}

// scopedQuery 构造带 call_type + 时间 + 用户过滤的基础查询。
// 通过 LEFT JOIN tasks 把用户过滤下沉到 SQL，避免子查询性能问题。
// 权限语义：admin 看全部；普通用户必须绑定 GitlabUsername，否则返回空结果（防止越权）。
func scopedQuery(c *gin.Context, callType string) *gorm.DB {
	start, end := parseRange(c)
	q := model.DB.Table("llm_call_logs l").
		Joins("LEFT JOIN tasks t ON t.id = l.task_id").
		Where("l.call_type = ?", callType).
		Where("l.created_at >= ? AND l.created_at < ?", start, end)
	if user, ok := middleware.GetUser(c); ok {
		if user.Role != model.RoleAdmin {
			if user.GitlabUsername == "" {
				// 非 admin 且未绑定 Gitlab 账号 → 无权访问任何数据
				q = q.Where("1 = 0")
			} else {
				q = q.Where("t.mr_author = ?", user.GitlabUsername)
			}
		}
	}
	return q
}

// respondDBError 统一错误响应：记详细日志，对外只返回"查询失败"。
func respondDBError(c *gin.Context, op string, err error) {
	zap.L().Error("token-usage "+op+" failed", zap.Error(err))
	c.JSON(500, gin.H{"error": "查询失败"})
}

// scanAggregates 聚合查询结果扫描辅助：row.Scan 在无数据时返回 sql.ErrNoRows，
// 此时应返回零值结构体而不是 500。
func scanAggregates(row *sql.Row, dest ...interface{}) error {
	if err := row.Scan(dest...); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// GetOverview KPI 总览
// GET /api/v1/token-usage/overview?range=7d
func (h *TokenUsageHandler) GetOverview(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	q := scopedQuery(c, model.CallTypeScore)
	type result struct {
		TotalTokens      int64   `json:"total_tokens"`
		PromptTokens     int64   `json:"prompt_tokens"`
		CompletionTokens int64   `json:"completion_tokens"`
		CachedTokens     int64   `json:"cached_tokens"`
		CallCount        int64   `json:"call_count"`
		SuccessCount     int64   `json:"success_count"`
		FailedCount      int64   `json:"failed_count"`
		AvgDurationMs    float64 `json:"avg_duration_ms"`
	}
	var r result
	// Session 隔离：GORM Row() 内部复用 Statement 会污染 builder，独立 Session 避免影响 q
	row := q.Session(&gorm.Session{}).Select(`
		COALESCE(SUM(l.total_tokens), 0)        AS total_tokens,
		COALESCE(SUM(l.prompt_tokens), 0)       AS prompt_tokens,
		COALESCE(SUM(l.completion_tokens), 0)   AS completion_tokens,
		COALESCE(SUM(l.cached_tokens), 0)       AS cached_tokens,
		COUNT(*)                                AS call_count,
		COALESCE(SUM(CASE WHEN l.status = 'success' THEN 1 ELSE 0 END), 0) AS success_count,
		COALESCE(SUM(CASE WHEN l.status = 'failed'  THEN 1 ELSE 0 END), 0) AS failed_count,
		COALESCE(AVG(l.duration_ms), 0)         AS avg_duration_ms
	`).Row()
	if err := scanAggregates(row,
		&r.TotalTokens, &r.PromptTokens, &r.CompletionTokens, &r.CachedTokens,
		&r.CallCount, &r.SuccessCount, &r.FailedCount, &r.AvgDurationMs); err != nil {
		respondDBError(c, "overview", err)
		return
	}
	c.JSON(200, gin.H{"data": r})
}

// GetTrend Token 用量趋势
// GET /api/v1/token-usage/trend?granularity=day|hour&range=7d
func (h *TokenUsageHandler) GetTrend(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	gran := c.DefaultQuery("granularity", "day")
	start, end := parseRange(c)
	q := scopedQuery(c, model.CallTypeScore)

	// gran 仅接受白名单值；写死两条 SQL 避免任何动态拼接。
	// 统一 bucket 为字符串（DATE_FORMAT 返回 VARCHAR，Scan 到 time.Time 会被驱动以 []uint8 拒绝）。
	const daySQL = `DATE_FORMAT(l.created_at, '%Y-%m-%d') AS bucket,
		COALESCE(SUM(l.prompt_tokens), 0)     AS prompt_tokens,
		COALESCE(SUM(l.completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(l.total_tokens), 0)      AS total_tokens`
	const hourSQL = `DATE_FORMAT(l.created_at, '%Y-%m-%d %H:00:00') AS bucket,
		COALESCE(SUM(l.prompt_tokens), 0)     AS prompt_tokens,
		COALESCE(SUM(l.completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(l.total_tokens), 0)      AS total_tokens`

	selectSQL := daySQL
	if gran == "hour" {
		selectSQL = hourSQL
	}

	var rows []struct {
		Bucket           string `json:"-"`
		PromptTokens     int64  `json:"prompt_tokens"`
		CompletionTokens int64  `json:"completion_tokens"`
		TotalTokens      int64  `json:"total_tokens"`
	}
	if err := q.Select(selectSQL).
		Group("bucket").
		Order("bucket ASC").
		Scan(&rows).Error; err != nil {
		respondDBError(c, "trend", err)
		return
	}

	// 按 SQL 结果聚合成 map
	type agg struct{ Prompt, Completion, Total int64 }
	bucketData := make(map[string]agg, len(rows))
	for _, r := range rows {
		bucketData[r.Bucket] = agg{r.PromptTokens, r.CompletionTokens, r.TotalTokens}
	}

	// 生成完整期望桶列表（缺失的桶自动补 0），保证 hour 粒度展示 24 个点、day 粒度展示连续日期
	expected := generateTrendBuckets(gran, start, end)

	out := make([]gin.H, 0, len(expected))
	for _, b := range expected {
		d := bucketData[b]
		out = append(out, gin.H{
			"date":              b,
			"prompt_tokens":     d.Prompt,
			"completion_tokens": d.Completion,
			"total_tokens":      d.Total,
		})
	}
	c.JSON(200, gin.H{"data": out})
}

// generateTrendBuckets 按 granularity 在 [start, end] 范围内生成所有期望桶的时间字符串。
// hour：固定生成今天 00:00 ~ 23:00 共 24 个桶（不受 start/end 影响），让图表点位稳定。
// day：从 start 所在日期 到 end 所在日期，每天一个桶。
func generateTrendBuckets(gran string, start, end time.Time) []string {
	loc := start.Location()
	buckets := make([]string, 0, 24)
	if gran == "hour" {
		base := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
		for i := 0; i < 24; i++ {
			buckets = append(buckets, base.Add(time.Duration(i)*time.Hour).Format("2006-01-02 15:04:05"))
		}
		return buckets
	}
	endDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, loc)
	cur := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	for !cur.After(endDay) {
		buckets = append(buckets, cur.Format("2006-01-02"))
		cur = cur.AddDate(0, 0, 1)
	}
	return buckets
}

// GetByModel 按模型聚合
// GET /api/v1/token-usage/by-model?range=7d
func (h *TokenUsageHandler) GetByModel(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	q := scopedQuery(c, model.CallTypeScore)
	type row struct {
		ModelID          *uint  `json:"model_id"`
		ModelName        string `json:"model_name"`
		Provider         string `json:"provider"`
		TotalTokens      int64  `json:"total_tokens"`
		PromptTokens     int64  `json:"prompt_tokens"`
		CompletionTokens int64  `json:"completion_tokens"`
		CallCount        int64  `json:"call_count"`
	}
	var rows []row
	if err := q.Select(`l.model_id,
		COALESCE(NULLIF(l.model_name, ''), '未知模型') AS model_name,
		l.provider,
		SUM(l.total_tokens)      AS total_tokens,
		SUM(l.prompt_tokens)     AS prompt_tokens,
		SUM(l.completion_tokens) AS completion_tokens,
		COUNT(*)                 AS call_count`).
		Group("l.model_id, l.model_name, l.provider").
		Order("total_tokens DESC").
		Scan(&rows).Error; err != nil {
		respondDBError(c, "by-model", err)
		return
	}
	c.JSON(200, gin.H{"data": rows})
}

// GetByProject 按项目聚合
// GET /api/v1/token-usage/by-project?range=7d&page=1&page_size=10
func (h *TokenUsageHandler) GetByProject(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	page, pageSize := parsePagination(c, 10, 100)
	q := scopedQuery(c, model.CallTypeScore)

	type row struct {
		ProjectID        *uint  `json:"project_id"`
		ProjectName      string `json:"project_name"`
		TotalTokens      int64  `json:"total_tokens"`
		PromptTokens     int64  `json:"prompt_tokens"`
		CompletionTokens int64  `json:"completion_tokens"`
		TaskCount        int64  `json:"task_count"`
		CallCount        int64  `json:"call_count"`
	}
	var rows []row
	incubatorsSQL := incubatorCallersInSQL()
	if err := q.Select(fmt.Sprintf(`
		CASE WHEN l.caller IN (%s) THEN 0 ELSE p.id END AS project_id,
		CASE WHEN l.caller IN (%s) THEN '规则孵化平台' ELSE COALESCE(NULLIF(p.name, ''), '未分类项目') END AS project_name,
		SUM(l.total_tokens)        AS total_tokens,
		SUM(l.prompt_tokens)       AS prompt_tokens,
		SUM(l.completion_tokens)   AS completion_tokens,
		COUNT(DISTINCT l.task_id)  AS task_count,
		COUNT(*)                   AS call_count`, incubatorsSQL, incubatorsSQL)).
		Joins("LEFT JOIN projects p ON p.id = t.project_id").
		Group(fmt.Sprintf(`CASE WHEN l.caller IN (%s) THEN 0 ELSE p.id END, CASE WHEN l.caller IN (%s) THEN '规则孵化平台' ELSE COALESCE(NULLIF(p.name, ''), '未分类项目') END`, incubatorsSQL, incubatorsSQL)).
		Order("total_tokens DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error; err != nil {
		respondDBError(c, "by-project", err)
		return
	}
	c.JSON(200, gin.H{"data": rows})
}

// GetByAuthor 按作者聚合
// GET /api/v1/token-usage/by-author?range=7d&page=1&page_size=10
func (h *TokenUsageHandler) GetByAuthor(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	page, pageSize := parsePagination(c, 10, 100)
	q := scopedQuery(c, model.CallTypeScore)

	type row struct {
		Author           string `json:"author"`
		TotalTokens      int64  `json:"total_tokens"`
		PromptTokens     int64  `json:"prompt_tokens"`
		CompletionTokens int64  `json:"completion_tokens"`
		TaskCount        int64  `json:"task_count"`
		CallCount        int64  `json:"call_count"`
	}
	var rows []row
	incubatorsSQL := incubatorCallersInSQL()
	if err := q.Select(fmt.Sprintf(`
		CASE WHEN l.caller IN (%s) THEN '规则孵化平台' ELSE COALESCE(NULLIF(t.mr_author, ''), '未分类作者') END AS author,
		SUM(l.total_tokens)        AS total_tokens,
		SUM(l.prompt_tokens)       AS prompt_tokens,
		SUM(l.completion_tokens)   AS completion_tokens,
		COUNT(DISTINCT l.task_id)  AS task_count,
		COUNT(*)                   AS call_count`, incubatorsSQL)).
		Group(fmt.Sprintf(`CASE WHEN l.caller IN (%s) THEN '规则孵化平台' ELSE COALESCE(NULLIF(t.mr_author, ''), '未分类作者') END`, incubatorsSQL)).
		Order("total_tokens DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error; err != nil {
		respondDBError(c, "by-author", err)
		return
	}
	c.JSON(200, gin.H{"data": rows})
}

// GetByCallType 按调用类型聚合
// GET /api/v1/token-usage/by-call-type?range=7d
func (h *TokenUsageHandler) GetByCallType(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	q := scopedQuery(c, "")
	type row struct {
		CallType    string `json:"call_type"`
		TotalTokens int64  `json:"total_tokens"`
		CallCount   int64  `json:"call_count"`
	}
	var rows []row
	if err := q.Select(`COALESCE(NULLIF(l.call_type, ''), 'unknown') AS call_type,
		SUM(l.total_tokens) AS total_tokens,
		COUNT(*)            AS call_count`).
		Group("l.call_type").
		Order("total_tokens DESC").
		Scan(&rows).Error; err != nil {
		respondDBError(c, "by-call-type", err)
		return
	}
	c.JSON(200, gin.H{"data": rows})
}

// ListCalls 调用明细（分页+筛选）
// GET /api/v1/token-usage/calls?range=7d&model_name=&caller=&status=&page=1&page_size=20
func (h *TokenUsageHandler) ListCalls(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	page, pageSize := parsePagination(c, 20, 200)
	q := scopedQuery(c, model.CallTypeScore)

	if v := c.Query("model_name"); v != "" {
		q = q.Where("l.model_name = ?", v)
	}
	if v := c.Query("caller"); v != "" {
		q = q.Where("l.caller = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("l.status = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondDBError(c, "calls count", err)
		return
	}

	type row struct {
		ID               uint      `json:"id"`
		TaskID           *uint     `json:"task_id"`
		ModelName        string    `json:"model_name"`
		Provider         string    `json:"provider"`
		Caller           string    `json:"caller"`
		CallType         string    `json:"call_type"`
		PromptTokens     int       `json:"prompt_tokens"`
		CompletionTokens int       `json:"completion_tokens"`
		TotalTokens      int       `json:"total_tokens"`
		DurationMs       int       `json:"duration_ms"`
		Status           string    `json:"status"`
		ErrorMsg         string    `json:"error_msg"`
		CreatedAt        time.Time `json:"created_at"`
	}
	var rows []row
	// 显式 Select 限定 llm_call_logs 列：LEFT JOIN tasks 后两表都有 status 列，
	// 默认 SELECT * 会同时返回 l.status 和 t.status，Scan 按列序后者覆盖前者，
	// 导致列表中所有调用状态被错误显示为 task 最终状态。
	if err := q.Select(`l.id, l.task_id, l.model_name, l.provider, l.caller, l.call_type,
		l.prompt_tokens, l.completion_tokens, l.total_tokens,
		l.duration_ms, l.status, l.error_msg, l.created_at`).
		Order("l.created_at DESC, l.id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error; err != nil {
		respondDBError(c, "calls list", err)
		return
	}
	for i := range rows {
		if label, ok := callerLabelMap[rows[i].Caller]; ok {
			rows[i].Caller = label
		}
	}
	c.JSON(200, gin.H{
		"data":      rows,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetByTask 单任务聚合（任务详情页用）
// GET /api/v1/token-usage/by-task?task_id=xxx
func (h *TokenUsageHandler) GetByTask(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	taskIDStr := c.Query("task_id")
	taskID, err := strconv.ParseUint(taskIDStr, 10, 32)
	if err != nil || taskID == 0 {
		c.JSON(400, gin.H{"error": "无效的 task_id"})
		return
	}

	// 合并权限校验到主查询：普通用户只能看自己的 task
	// 与 model.FilterByUser 保持一致：GitlabUsername 为空的非 admin 拒绝访问
	// 不存在或无权限 → 返回 404（避免信息泄露区分"不存在"和"无权访问"）
	base := model.DB.Table("llm_call_logs l").
		Joins("LEFT JOIN tasks t ON t.id = l.task_id").
		Where("l.task_id = ? AND l.call_type = ?", taskID, model.CallTypeScore)
	if user, ok := middleware.GetUser(c); ok {
		if user.Role != model.RoleAdmin {
			if user.GitlabUsername == "" {
				c.JSON(404, gin.H{"error": "任务不存在或无权访问"})
				return
			}
			base = base.Where("t.mr_author = ?", user.GitlabUsername)
		}
	}

	type summary struct {
		CallCount        int64   `json:"call_count"`
		PromptTokens     int64   `json:"prompt_tokens"`
		CompletionTokens int64   `json:"completion_tokens"`
		TotalTokens      int64   `json:"total_tokens"`
		CachedTokens     int64   `json:"cached_tokens"`
		AvgDurationMs    float64 `json:"avg_duration_ms"`
		SuccessCount     int64   `json:"success_count"`
		FailedCount      int64   `json:"failed_count"`
	}
	var s summary
	// Session 隔离：GORM Row() 内部复用 Statement 会污染 builder，导致 base 后续查询重复 JOIN
	row := base.Session(&gorm.Session{}).Select(`COUNT(*)                                          AS call_count,
		COALESCE(SUM(l.prompt_tokens), 0)                        AS prompt_tokens,
		COALESCE(SUM(l.completion_tokens), 0)                    AS completion_tokens,
		COALESCE(SUM(l.total_tokens), 0)                         AS total_tokens,
		COALESCE(SUM(l.cached_tokens), 0)                        AS cached_tokens,
		COALESCE(AVG(l.duration_ms), 0)                          AS avg_duration_ms,
		COALESCE(SUM(CASE WHEN l.status = 'success' THEN 1 ELSE 0 END), 0) AS success_count,
		COALESCE(SUM(CASE WHEN l.status = 'failed'  THEN 1 ELSE 0 END), 0) AS failed_count`).Row()
	if err := scanAggregates(row, &s.CallCount, &s.PromptTokens, &s.CompletionTokens, &s.TotalTokens,
		&s.CachedTokens, &s.AvgDurationMs, &s.SuccessCount, &s.FailedCount); err != nil {
		respondDBError(c, "by-task summary", err)
		return
	}
	if s.CallCount == 0 {
		c.JSON(404, gin.H{"error": "任务不存在或无权访问"})
		return
	}

	// calls 列表分页，按 created_at DESC 最近优先展示
	type call struct {
		ID               uint      `json:"id"`
		ModelName        string    `json:"model_name"`
		Caller           string    `json:"caller"`
		CallType         string    `json:"call_type"`
		PromptTokens     int       `json:"prompt_tokens"`
		CompletionTokens int       `json:"completion_tokens"`
		TotalTokens      int       `json:"total_tokens"`
		DurationMs       int       `json:"duration_ms"`
		Status           string    `json:"status"`
		ErrorMsg         string    `json:"error_msg"`
		CreatedAt        time.Time `json:"created_at"`
	}
	page, pageSize := parsePagination(c, 20, 100)
	offset := (page - 1) * pageSize

	var calls []call
	if err := base.Order("l.created_at DESC, l.id DESC").
		Limit(pageSize).
		Offset(offset).
		Select(`l.id, l.model_name, l.caller, l.call_type,
			l.prompt_tokens, l.completion_tokens, l.total_tokens,
			l.duration_ms, l.status, l.error_msg, l.created_at`).
		Scan(&calls).Error; err != nil {
		respondDBError(c, "by-task calls", err)
		return
	}

	// has_more 判断是否还有下一页；避免前端算分页边界
	hasMore := int64(offset+len(calls)) < s.CallCount

	c.JSON(200, gin.H{
		"summary":   s,
		"calls":     calls,
		"total":     s.CallCount,
		"page":      page,
		"page_size": pageSize,
		"has_more":  hasMore,
	})
}

// GetTokenSummary 首页摘要（今日 KPI + 7 天趋势）
// GET /api/v1/dashboard/token-summary
func (h *TokenUsageHandler) GetTokenSummary(c *gin.Context) {
	user, ok := currentUserOrAbort(c)
	if !ok {
		return
	}
	// 普通用户不展示（首页摘要为成本视图，admin only）
	if user.Role != model.RoleAdmin {
		c.JSON(200, gin.H{"data": nil})
		return
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	type todayRow struct {
		TotalTokens   int64   `json:"total_tokens"`
		CallCount     int64   `json:"call_count"`
		AvgDurationMs float64 `json:"avg_duration_ms"`
	}
	var today todayRow
	if err := model.DB.Model(&model.LLMCallLog{}).
		Where("call_type = ? AND created_at >= ?", model.CallTypeScore, todayStart).
		Select(`COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COUNT(*)                            AS call_count,
			COALESCE(AVG(duration_ms), 0)       AS avg_duration_ms`).
		Scan(&today).Error; err != nil {
		respondDBError(c, "summary today", err)
		return
	}

	type trendRow struct {
		Day              time.Time `json:"-"`
		PromptTokens     int64     `json:"prompt_tokens"`
		CompletionTokens int64     `json:"completion_tokens"`
	}
	// 7 天滚动窗口（含今天）
	weekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -6)
	var trends []trendRow
	if err := model.DB.Model(&model.LLMCallLog{}).
		Where("call_type = ? AND created_at >= ?", model.CallTypeScore, weekStart).
		Select(`DATE(created_at) AS day,
			SUM(prompt_tokens)     AS prompt_tokens,
			SUM(completion_tokens) AS completion_tokens`).
		Group("day").
		Order("day ASC").
		Scan(&trends).Error; err != nil {
		respondDBError(c, "summary trend", err)
		return
	}

	trendOut := make([]gin.H, 0, len(trends))
	for _, t := range trends {
		trendOut = append(trendOut, gin.H{
			"date":             t.Day.Format("01-02"),
			"prompt_tokens":    t.PromptTokens,
			"completion_tokens": t.CompletionTokens,
		})
	}

	c.JSON(200, gin.H{
		"today_tokens":     today.TotalTokens,
		"today_calls":      today.CallCount,
		"avg_duration_ms":  today.AvgDurationMs,
		"trend_7d":         trendOut,
	})
}
