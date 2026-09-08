package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ReportHandler struct{}

func NewReportHandler() *ReportHandler {
	return &ReportHandler{}
}

// ============ SMTP 配置 ============

func (h *ReportHandler) GetSMTPConfig(c *gin.Context) {
	if _, ok := currentUserOrAbort(c); !ok {
		return
	}
	var cfg model.SMTPConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		c.JSON(200, gin.H{"data": nil})
		return
	}
	c.JSON(200, gin.H{"data": cfg})
}

func (h *ReportHandler) SaveSMTPConfig(c *gin.Context) {
	_, ok := currentUserOrAbort(c)
	if !ok {
		return
	}
	scope := middleware.GetAuthScope(c)
	if scope == nil || !scope.HasOrgRole("super_admin", "org_admin") {
		c.JSON(403, gin.H{"error": "admin required"})
		return
	}
	var req struct {
		Host      string `json:"host" binding:"required"`
		Port      int    `json:"port" binding:"required"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		FromEmail string `json:"from_email" binding:"required"`
		FromName  string `json:"from_name"`
		UseTLS    bool   `json:"use_tls"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	var cfg model.SMTPConfig
	model.DB.First(&cfg)
	cfg.Host = req.Host
	cfg.Port = req.Port
	cfg.Username = req.Username
	cfg.Password = req.Password
	cfg.FromEmail = req.FromEmail
	cfg.FromName = req.FromName
	cfg.UseTLS = req.UseTLS
	cfg.IsDefault = true

	// 多租户改造：SMTPConfig 无 org_id，使用 Updates 精确更新字段，避免零值覆盖
	model.DB.Model(&cfg).Updates(map[string]interface{}{
		"host":       cfg.Host,
		"port":       cfg.Port,
		"username":   cfg.Username,
		"password":   cfg.Password,
		"from_email": cfg.FromEmail,
		"from_name":  cfg.FromName,
		"use_tls":    cfg.UseTLS,
		"is_default": cfg.IsDefault,
	})
	c.JSON(200, gin.H{"message": "saved"})
}

func (h *ReportHandler) TestSMTP(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	var req struct {
		Host      string `json:"host" binding:"required"`
		Port      int    `json:"port" binding:"required"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		FromEmail string `json:"from_email" binding:"required"`
		FromName  string `json:"from_name"`
		UseTLS    bool   `json:"use_tls"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	cfg := model.SMTPConfig{
		Host:      req.Host,
		Port:      req.Port,
		Username:  req.Username,
		Password:  req.Password,
		FromEmail: req.FromEmail,
		FromName:  req.FromName,
		UseTLS:    req.UseTLS,
	}
	if err := service.NewReportService().TestSMTP(&cfg); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "SMTP test email sent"})
}

// ============ 接收人管理 ============

func (h *ReportHandler) ListRecipients(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	var list []model.ReportRecipient
	db := model.DB.Order("id DESC")
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	db.Find(&list)

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(list))
	for _, r := range list {
		orgIDs = append(orgIDs, r.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range list {
		list[i].OrgName = orgNameMap[list[i].OrgID]
	}

	c.JSON(200, gin.H{"data": list})
}

func (h *ReportHandler) CreateRecipient(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	var r model.ReportRecipient
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if r.GroupName == "" {
		r.GroupName = "默认分组"
	}
	// 多租户改造：注入当前 org_id（非 super_admin 禁止客户端传入）
	if r.OrgID == 0 || !scope.IsSuperAdmin {
		r.OrgID = middleware.GetCurrentOrgID(c)
	}
	model.DB.Create(&r)
	c.JSON(200, gin.H{"data": r})
}

func (h *ReportHandler) UpdateRecipient(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var r model.ReportRecipient
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	if err := db.First(&r, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	var req struct {
		Name      string `json:"name"`
		Email     string `json:"email"`
		GroupName string `json:"group_name"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Name != "" {
		r.Name = req.Name
	}
	if req.Email != "" {
		r.Email = req.Email
	}
	if req.GroupName != "" {
		r.GroupName = req.GroupName
	}
	if req.Enabled != nil {
		r.Enabled = *req.Enabled
	}
	// 多租户改造：避免 Save 零值覆盖 org_id
	model.DB.Omit("org_id").Save(&r)
	c.JSON(200, gin.H{"data": r})
}

func (h *ReportHandler) DeleteRecipient(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	db.Delete(&model.ReportRecipient{}, id)
	c.JSON(200, gin.H{"message": "deleted"})
}

// ============ 报告配置 ============

func (h *ReportHandler) GetReportConfig(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	reportType := c.Param("type")
	if reportType != "weekly" && reportType != "monthly" {
		c.JSON(400, gin.H{"error": "invalid type"})
		return
	}

	var cfg model.ReportConfig
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	if err := db.Where("report_type = ?", reportType).First(&cfg).Error; err != nil {
		// 首次访问时创建默认配置
		cfg.ReportType = reportType
		cfg.OrgID = middleware.GetCurrentOrgID(c)
		if reportType == "weekly" {
			cfg.DataPeriodDays = 7
			cfg.SendDayOfWeek = 1 // 周一
			cfg.SendHour = 9
			cfg.SendMinute = 0
			cfg.CronExpr = "0 0 9 * * 1"
		} else {
			cfg.DataPeriodDays = 30
			cfg.SendDayOfMonth = 1
			cfg.SendHour = 9
			cfg.SendMinute = 0
			cfg.CronExpr = "0 0 9 1 * *"
		}
		cfg.GenerateEnabled = false
		cfg.SendEnabled = false
		cfg.Enabled = false // 兼容旧字段
		model.DB.Create(&cfg)
	}
	c.JSON(200, gin.H{"data": cfg})
}

func (h *ReportHandler) SaveReportConfig(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	reportType := c.Param("type")
	if reportType != "weekly" && reportType != "monthly" {
		c.JSON(400, gin.H{"error": "invalid type"})
		return
	}

	var req struct {
		GenerateEnabled bool     `json:"generate_enabled"`
		SendEnabled     bool     `json:"send_enabled"`
		Enabled         bool     `json:"enabled"`
		SendGroups      []string `json:"send_groups"`
		DataPeriodDays  int      `json:"data_period_days"`
		SendHour        int      `json:"send_hour"`
		SendMinute      int      `json:"send_minute"`
		SendDayOfWeek   int      `json:"send_day_of_week"`
		SendDayOfMonth  int      `json:"send_day_of_month"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	var cfg model.ReportConfig
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	db.Where("report_type = ?", reportType).First(&cfg)
	cfg.ReportType = reportType
	cfg.GenerateEnabled = req.GenerateEnabled
	cfg.SendEnabled = req.SendEnabled
	// 兼容旧字段：任一开启则 Enabled=true
	cfg.Enabled = req.GenerateEnabled || req.SendEnabled || req.Enabled
	// 保存发送分组
	if len(req.SendGroups) > 0 {
		groupsJSON, _ := json.Marshal(req.SendGroups)
		cfg.SendGroups = string(groupsJSON)
	} else {
		cfg.SendGroups = ""
	}
	cfg.DataPeriodDays = req.DataPeriodDays
	cfg.SendHour = req.SendHour
	cfg.SendMinute = req.SendMinute
	cfg.SendDayOfWeek = req.SendDayOfWeek
	cfg.SendDayOfMonth = req.SendDayOfMonth
	// 重新生成 Cron 表达式
	cfg.CronExpr = service.NewReportService().BuildCronExpression(&cfg)
	// 多租户改造：避免 Save 零值覆盖 org_id
	model.DB.Omit("org_id").Save(&cfg)
	// 热重载 cron
	service.ReloadReportCron()
	c.JSON(200, gin.H{"message": "saved", "cron_expr": cfg.CronExpr})
}

// ============ 预览与发送 ============

func (h *ReportHandler) PreviewReport(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	reportType := c.Param("type")
	if reportType != "weekly" && reportType != "monthly" {
		c.JSON(400, gin.H{"error": "invalid type"})
		return
	}

	html, err := service.NewReportService().GenerateHTML(scope, reportType)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (h *ReportHandler) SendReport(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	reportType := c.Param("type")
	if reportType != "weekly" && reportType != "monthly" {
		c.JSON(400, gin.H{"error": "invalid type"})
		return
	}

	// 读取发送分组
	var req struct {
		Groups []string `json:"groups"`
	}
	c.ShouldBindJSON(&req)

	query := model.DBWithScope(scope).Where("enabled = ?", true)
	if len(req.Groups) > 0 {
		query = query.Where("group_name IN ?", req.Groups)
	}
	var recipients []model.ReportRecipient
	query.Find(&recipients)
	recipientsJSON, _ := json.Marshal(recipients)

	svc := service.NewReportService()
	html, err := svc.GenerateHTML(scope, reportType)
	if err != nil {
		log := model.ReportLog{
			OrgID:       scope.CurrentOrgID,
			ReportType:  reportType,
			TriggerType: "manual",
			Status:      "generated_failed",
			Recipients:  string(recipientsJSON),
			ErrorMsg:    err.Error(),
			SentAt:      time.Now(),
		}
		model.DB.Create(&log)
		c.JSON(500, gin.H{"error": "generate failed: " + err.Error()})
		return
	}

	if err := svc.SendEmail(scope, reportType, html, req.Groups); err != nil {
		log := model.ReportLog{
			OrgID:       scope.CurrentOrgID,
			ReportType:  reportType,
			TriggerType: "manual",
			Status:      "sent_failed",
			Recipients:  string(recipientsJSON),
			HtmlContent: html,
			ErrorMsg:    err.Error(),
			SentAt:      time.Now(),
		}
		model.DB.Create(&log)
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	log := model.ReportLog{
		OrgID:       scope.CurrentOrgID,
		ReportType:  reportType,
		TriggerType: "manual",
		Status:      "sent_success",
		Recipients:  string(recipientsJSON),
		HtmlContent: html,
		SentAt:      time.Now(),
	}
	model.DB.Create(&log)
	c.JSON(200, gin.H{"message": "报告已生成并发送"})
}

// ============ 日志 ============

func (h *ReportHandler) ListLogs(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	reportType := c.Query("type")
	status := c.Query("status")

	var total int64
	db := model.DB.Model(&model.ReportLog{})
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	if reportType != "" {
		db = db.Where("report_type = ?", reportType)
	}
	if status != "" {
		db = db.Where("status = ?", status)
	}
	db.Count(&total)

	var logs []model.ReportLog
	db.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs)

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(logs))
	for _, l := range logs {
		orgIDs = append(orgIDs, l.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range logs {
		logs[i].OrgName = orgNameMap[logs[i].OrgID]
	}

	c.JSON(200, gin.H{"data": logs, "total": total, "page": page, "page_size": pageSize})
}

func (h *ReportHandler) GetReportLogHTML(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	var log model.ReportLog
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	if err := db.First(&log, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if log.HtmlContent == "" {
		c.JSON(404, gin.H{"error": "no html content"})
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, log.HtmlContent)
}

func (h *ReportHandler) DeleteLog(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	if err := db.Delete(&model.ReportLog{}, id).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "deleted"})
}

func (h *ReportHandler) ResendReport(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	var log model.ReportLog
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	if err := db.First(&log, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "report log not found"})
		return
	}

	if log.HtmlContent == "" {
		c.JSON(400, gin.H{"error": "no html content in this report"})
		return
	}

	if err := service.NewReportService().ResendEmail(scope, &log); err != nil {
		// 记录新日志
		newLog := model.ReportLog{
			OrgID:       log.OrgID,
			ReportType:  log.ReportType,
			TriggerType: "manual",
			Status:      "sent_failed",
			Recipients:  log.Recipients,
			HtmlContent: log.HtmlContent,
			ErrorMsg:    err.Error(),
			SentAt:      time.Now(),
		}
		model.DB.Create(&newLog)
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 记录新日志
	newLog := model.ReportLog{
		OrgID:       log.OrgID,
		ReportType:  log.ReportType,
		TriggerType: "manual",
		Status:      "sent_success",
		Recipients:  log.Recipients,
		HtmlContent: log.HtmlContent,
		SentAt:      time.Now(),
	}
	model.DB.Create(&newLog)
	c.JSON(200, gin.H{"message": "报告已重新发送"})
}

// InitReportConfigs 初始化周报/月报默认配置（归属根组织）
func InitReportConfigs() {
	for _, t := range []string{"weekly", "monthly"} {
		var cfg model.ReportConfig
		err := model.DB.Where("report_type = ? AND org_id = 1", t).First(&cfg).Error
		if err == nil {
			continue // 已存在，跳过
		}
		// 不存在则插入默认值
		cfg = model.ReportConfig{
			OrgID:           1,
			ReportType:      t,
			SendHour:        9,
			SendMinute:      0,
			GenerateEnabled: false,
			SendEnabled:     false,
			Enabled:         false,
		}
		if t == "weekly" {
			cfg.DataPeriodDays = 7
			cfg.SendDayOfWeek = 1
		} else {
			cfg.DataPeriodDays = 30
			cfg.SendDayOfMonth = 1
		}
		cfg.CronExpr = service.NewReportService().BuildCronExpression(&cfg)
		if err := model.DB.Create(&cfg).Error; err != nil {
			zap.L().Error("init report config failed", zap.String("type", t), zap.Error(err))
		}
	}
}
