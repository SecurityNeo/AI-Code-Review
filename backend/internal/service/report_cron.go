package service

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

var (
	reportCron    *cron.Cron
	reportCronIDs map[string]cron.EntryID
	reportCronMu  sync.Mutex
)

func SetReportCron(c *cron.Cron) {
	reportCron = c
	reportCronIDs = make(map[string]cron.EntryID)
}

func InitReportCron() {
	ReloadReportCron()
}

func ReloadReportCron() {
	reportCronMu.Lock()
	defer reportCronMu.Unlock()

	if reportCron == nil {
		zap.L().Warn("ReloadReportCron called but reportCron is nil")
		return
	}

	// Remove existing report cron entries
	for t, id := range reportCronIDs {
		reportCron.Remove(id)
		zap.L().Info("removed report cron", zap.String("type", t))
		delete(reportCronIDs, t)
	}

	var configs []model.ReportConfig
	model.DB.Find(&configs)

	for _, cfg := range configs {
		if cfg.CronExpr == "" {
			continue
		}
		expr := cfg.CronExpr
		isGenerate := cfg.Enabled || cfg.GenerateEnabled
		isSend := cfg.Enabled || cfg.SendEnabled
		if !isGenerate {
			continue
		}

		// Capture variables for closure
		reportType := cfg.ReportType
		shouldSend := isSend
		orgID := cfg.OrgID
		cfgSendGroups := cfg.SendGroups

		cronKey := fmt.Sprintf("%s-%d", reportType, cfg.OrgID)

		id, err := reportCron.AddFunc(expr, func() {
			svc := NewReportService()

			// 解析配置中的发送分组
			var sendGroups []string
			if cfgSendGroups != "" {
				json.Unmarshal([]byte(cfgSendGroups), &sendGroups)
			}

			// 构造 cron 专用 scope，限定为当前组织
			cronScope := &model.UserAuthScope{
				VisibleOrgIDs: []uint{orgID},
				CurrentOrgID:  orgID,
			}

			// Query recipients for logging (按组织隔离)
			query := model.DBWithScope(cronScope).Where("enabled = ?", true)
			if len(sendGroups) > 0 {
				query = query.Where("group_name IN ?", sendGroups)
			}
			var recipients []model.ReportRecipient
			query.Find(&recipients)
			recipientsJSON, _ := json.Marshal(recipients)

			html, err := svc.GenerateHTML(cronScope, reportType)
			if err != nil {
				zap.L().Error("report auto generate failed",
					zap.String("type", reportType),
					zap.Uint("org_id", orgID),
					zap.Error(err))
				log := model.ReportLog{
					OrgID:       orgID,
					ReportType:  reportType,
					TriggerType: "auto",
					Status:      "generated_failed",
					Recipients:  string(recipientsJSON),
					ErrorMsg:    err.Error(),
					SentAt:      time.Now(),
				}
				model.DB.Create(&log)
				return
			}

			if !shouldSend {
				zap.L().Info("report auto generated (send disabled)",
					zap.String("type", reportType),
					zap.Uint("org_id", orgID))
				return
			}

			if err := svc.SendEmail(cronScope, reportType, html, sendGroups); err != nil {
				log := model.ReportLog{
					OrgID:       orgID,
					ReportType:  reportType,
					TriggerType: "auto",
					Status:      "sent_failed",
					Recipients:  string(recipientsJSON),
					HtmlContent: html,
					ErrorMsg:    err.Error(),
					SentAt:      time.Now(),
				}
				model.DB.Create(&log)
				zap.L().Error("report auto send failed",
					zap.String("type", reportType),
					zap.Uint("org_id", orgID),
					zap.Error(err))
				return
			}

			log := model.ReportLog{
				OrgID:       uint(orgID),
				ReportType:  reportType,
				TriggerType: "auto",
				Status:      "sent_success",
				Recipients:  string(recipientsJSON),
				HtmlContent: html,
				SentAt:      time.Now(),
			}
			model.DB.Create(&log)
			zap.L().Info("report auto sent",
				zap.String("type", reportType),
				zap.Uint("org_id", orgID))
		})

		if err != nil {
			zap.L().Error("report cron register failed",
				zap.String("type", reportType),
				zap.Uint("org_id", orgID),
				zap.String("expr", expr),
				zap.Error(err))
		} else {
			reportCronIDs[cronKey] = id
			zap.L().Info("report cron registered",
				zap.String("type", reportType),
				zap.Uint("org_id", orgID),
				zap.String("expr", expr))
		}
	}
}
