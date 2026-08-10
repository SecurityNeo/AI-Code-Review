package handler

import (
	"errors"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type SystemHandler struct {
	startTime time.Time
}

func NewSystemHandler() *SystemHandler {
	return &SystemHandler{
		startTime: time.Now(),
	}
}

func (h *SystemHandler) GetConfig(c *gin.Context) {
	var cfg model.SystemConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
		cfg = model.SystemConfig{
			TaskTimeoutMin:          30,
			MaxParallelTask:         20,
			LogRetentionDay:         90,
			DiffTruncationThreshold: 5000,
			MaxDiffFiles:            50,
			MaxTokensPerBatch:       100000,
			LLMRetryMaxAttempts:     3,
			LLMRetryInitialDelayMs:  1000,
			LLMRetryBackoffMultiplier: 2.0,
			LLMRetryMaxDelayMs:      30000,
			ScoreThreshold:           60,
			DeepReviewEnabled:        false,
			AlertDurationSec:        300,
			AlertCooldownSec:        3600,
			AlertNotifierID:         0,
			AlertMentionUserIDs:     "",
			JSONRetryMaxAttempts:       3,
			JSONRetryInitialDelaySec:   2,
			JSONRetryBackoffMultiplier: 2.0,
			JSONRetryMaxDelaySec:       30,
			JSONRetryFallbackStrategy:  "regex",
			DefaultDimensionWeights: `{"security":30,"code_quality":25,"readability":20,"maintainability":15,"test_coverage":10}`,
			AILogTemplate: "请先执行以下命令拉取代码：\ngit clone {{CLONE_URL}}\n\n变更摘要：\n{{MR_DIFF}}\n\n{{USER_INPUT}}\n\n请审查以上代码变更，给出审查意见。",
			PipelineEnabled:  false,
			BatchParallelMax: 3,
		}
			if err := model.DB.Create(&cfg).Error; err != nil {
				zap.L().Error("create system config failed", zap.Error(err))
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			// 重新查询以获取完整的记录
			model.DB.First(&cfg)
		} else {
			zap.L().Error("get system config failed", zap.Error(err))
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}

	// 确保只有一条系统配置记录，删除其他记录
	if cfg.ID > 1 {
		model.DB.Where("id > 1").Delete(&model.SystemConfig{})
		zap.L().Info("deleted duplicate system configs, keeping id 1", zap.Uint("kept_id", cfg.ID))
		// 重新查询id=1的记录
		model.DB.First(&cfg)
	}
	c.JSON(200, gin.H{"data": cfg})
}

func (h *SystemHandler) UpdateConfig(c *gin.Context) {
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	var cfg model.SystemConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		c.JSON(404, gin.H{"error": "config not found"})
		return
	}

	// 确保只有一条系统配置记录，删除其他记录
	if cfg.ID > 1 {
		model.DB.Where("id > 1").Delete(&model.SystemConfig{})
		zap.L().Info("deleted duplicate system configs, keeping id 1", zap.Uint("kept_id", cfg.ID))
	}

	// 构建更新字段
	updates := make(map[string]interface{})

	if v, ok := data["gitlab_token"]; ok {
		updates["gitlab_token"] = v.(string)
	}
	if v, ok := data["task_timeout_min"]; ok {
		updates["task_timeout_min"] = int(v.(float64))
	}
	if v, ok := data["sync_interval_sec"]; ok {
		updates["sync_interval_sec"] = int(v.(float64))
	}
	if v, ok := data["mr_sync_interval_sec"]; ok {
		updates["mr_sync_interval_sec"] = int(v.(float64))
	}
	if v, ok := data["max_parallel_task"]; ok {
		updates["max_parallel_task"] = int(v.(float64))
	}
	if v, ok := data["log_retention_day"]; ok {
		updates["log_retention_day"] = int(v.(float64))
	}
	if v, ok := data["ai_log_template"]; ok {
		updates["ai_log_template"] = v.(string)
	}
	if v, ok := data["score_threshold"]; ok {
		val := int(v.(float64))
		if val < 1 {
			val = 1
		} else if val > 100 {
			val = 100
		}
		updates["score_threshold"] = val
	}
	if v, ok := data["deep_review_enabled"]; ok {
		updates["deep_review_enabled"] = v.(bool)
	}
	if v, ok := data["diff_truncation_threshold"]; ok {
		val := int(v.(float64))
		if val < 0 {
			val = 0
		}
		updates["diff_truncation_threshold"] = val
	}
	// 任务设置 - 分批评审阈值
	if v, ok := data["max_diff_files"]; ok {
		val := int(v.(float64))
		if val < 1 {
			val = 1
		} else if val > 500 {
			val = 500
		}
		updates["max_diff_files"] = val
	}
	if v, ok := data["max_tokens_per_batch"]; ok {
		val := int(v.(float64))
		if val < 1000 {
			val = 1000
		} else if val > 2000000 {
			val = 2000000
		}
		updates["max_tokens_per_batch"] = val
	}
	// LLM 调用重试配置
	if v, ok := data["llm_retry_max_attempts"]; ok {
		val := int(v.(float64))
		if val < 1 {
			val = 1
		} else if val > 10 {
			val = 10
		}
		updates["llm_retry_max_attempts"] = val
	}
	if v, ok := data["llm_retry_initial_delay_ms"]; ok {
		val := int(v.(float64))
		if val < 100 {
			val = 100
		} else if val > 60000 {
			val = 60000
		}
		updates["llm_retry_initial_delay_ms"] = val
	}
	if v, ok := data["llm_retry_backoff_multiplier"]; ok {
		val := v.(float64)
		if val < 1.0 {
			val = 1.0
		} else if val > 10.0 {
			val = 10.0
		}
		updates["llm_retry_backoff_multiplier"] = val
	}
	if v, ok := data["llm_retry_max_delay_ms"]; ok {
		val := int(v.(float64))
		if val < 1000 {
			val = 1000
		} else if val > 300000 {
			val = 300000
		}
		updates["llm_retry_max_delay_ms"] = val
	}
	if v, ok := data["alert_duration_sec"]; ok {
		val := int(v.(float64))
		if val < 1 {
			val = 1
		}
		updates["alert_duration_sec"] = val
	}
	if v, ok := data["alert_cooldown_sec"]; ok {
		val := int(v.(float64))
		if val < 300 {
			val = 300
		}
		updates["alert_cooldown_sec"] = val
	}
	if v, ok := data["alert_notifier_id"]; ok {
		updates["alert_notifier_id"] = uint(int(v.(float64)))
	}
	if v, ok := data["alert_mention_user_ids"]; ok {
		updates["alert_mention_user_ids"] = v.(string)
	}
	if v, ok := data["review_template"]; ok {
		updates["review_template"] = v.(string)
	}

	// GitLab OAuth 配置字段
	if v, ok := data["gitlab_oauth_enabled"]; ok {
		updates["gitlab_oauth_enabled"] = v.(bool)
	}
	if v, ok := data["gitlab_base_url"]; ok {
		updates["gitlab_base_url"] = v.(string)
	}
	if v, ok := data["gitlab_oauth_client_id"]; ok {
		updates["gitlab_oauth_client_id"] = v.(string)
	}
	if v, ok := data["gitlab_oauth_client_secret"]; ok {
		updates["gitlab_oauth_client_secret"] = v.(string)
	}
	if v, ok := data["gitlab_oauth_redirect_uri"]; ok {
		updates["gitlab_oauth_redirect_uri"] = v.(string)
	}
	if v, ok := data["gitlab_oauth_auto_create_user"]; ok {
		updates["gitlab_oauth_auto_create_user"] = v.(bool)
	}
	if v, ok := data["gitlab_oauth_skip_verify"]; ok {
		updates["gitlab_oauth_skip_verify"] = v.(bool)
	}

	// JSON Retry 配置字段
	if v, ok := data["pipeline_enabled"]; ok {
		updates["pipeline_enabled"] = v.(bool)
	}
	if v, ok := data["batch_parallel_max"]; ok {
		val := int(v.(float64))
		if val < 1 {
			val = 1
		} else if val > 10 {
			val = 10
		}
		updates["batch_parallel_max"] = val
	}
	if v, ok := data["json_retry_max_attempts"]; ok {
		val := int(v.(float64))
		if val < 0 { val = 0 }
		updates["json_retry_max_attempts"] = val
	}
	if v, ok := data["json_retry_initial_delay_sec"]; ok {
		val := int(v.(float64))
		if val < 0 { val = 0 }
		updates["json_retry_initial_delay_sec"] = val
	}
	if v, ok := data["json_retry_backoff_multiplier"]; ok {
		val := v.(float64)
		if val < 1.0 { val = 1.0 }
		updates["json_retry_backoff_multiplier"] = val
	}
	if v, ok := data["json_retry_max_delay_sec"]; ok {
		val := int(v.(float64))
		if val < 0 { val = 0 }
		updates["json_retry_max_delay_sec"] = val
	}
	if v, ok := data["json_retry_fallback_strategy"]; ok {
		str := v.(string)
		if str != "regex" && str != "markdown" && str != "fail" {
			str = "regex"
		}
		updates["json_retry_fallback_strategy"] = str
	}

	// 模板相关字段
	if v, ok := data["default_dimension_weights"]; ok {
		updates["default_dimension_weights"] = v.(string)
	}
	if v, ok := data["default_gitlab_comment_template"]; ok {
		updates["default_gitlab_comment_template"] = v.(string)
	}

	// 判断是更新哪种模板或系统配置
	isAITemplateOnly := len(data) == 1 && data["ai_log_template"] != nil
	isReviewTemplateOnly := len(data) == 1 && data["review_template"] != nil

	userID, _ := c.Get("user_id")
	if isAITemplateOnly {
		model.RecordOpLog("AI对话模板更新", "AI对话模板", cfg.ID, userID.(uint), "success", "", c.ClientIP())
	} else if isReviewTemplateOnly {
		model.RecordOpLog("代码审查模版更新", "代码审查模版", cfg.ID, userID.(uint), "success", "", c.ClientIP())
	} else {
		model.RecordOpLog("系统配置更新", "系统配置", cfg.ID, userID.(uint), "success", "", c.ClientIP())
	}

	// 执行数据库更新
	if len(updates) > 0 {
		if err := model.DB.Model(&cfg).Updates(updates).Error; err != nil {
			zap.L().Error("update system config failed", zap.Error(err))
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}

	// 重新查询获取最新数据
	model.DB.First(&cfg)

	// 如果 MR 同步间隔有变更，重建定时任务
	if _, ok := data["mr_sync_interval_sec"]; ok {
		service.RebuildMRSyncCron()
	}

	// 主动刷新 sysCfg 缓存：max_diff_files / max_tokens_per_batch / task_timeout_min /
	// llm_retry_* 变更后无需等待 cron（1m）即可立即生效
	if _, ok := data["max_diff_files"]; ok {
		service.RefreshSysCfgCache()
	} else if _, ok := data["max_tokens_per_batch"]; ok {
		service.RefreshSysCfgCache()
	} else if _, ok := data["task_timeout_min"]; ok {
		service.RefreshSysCfgCache()
	} else if _, ok := data["llm_retry_max_attempts"]; ok {
		service.RefreshSysCfgCache()
	} else if _, ok := data["llm_retry_initial_delay_ms"]; ok {
		service.RefreshSysCfgCache()
	} else if _, ok := data["llm_retry_backoff_multiplier"]; ok {
		service.RefreshSysCfgCache()
	} else if _, ok := data["llm_retry_max_delay_ms"]; ok {
		service.RefreshSysCfgCache()
	}

	c.JSON(200, gin.H{"message": "配置已更新", "data": cfg})
}

func (h *SystemHandler) OperationLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	opType := c.Query("type")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	var logs []model.OperationLog
	var total int64

	query := model.DB.Model(&model.OperationLog{})

	if opType != "" {
		query = query.Where("op_type = ?", opType)
	}
	if startDate != "" {
		query = query.Where("created_at >= ?", startDate)
	}
	if endDate != "" {
		query = query.Where("created_at <= ?", endDate+" 23:59:59")
	}

	query.Count(&total)
	query = query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize)
	query.Find(&logs)

	// 批量查询用户名
	userMap := make(map[uint]string)
	var userIDs []uint
	for _, l := range logs {
		if l.OpUserID > 0 {
			userIDs = append(userIDs, l.OpUserID)
		}
	}
	if len(userIDs) > 0 {
		var users []model.User
		model.DB.Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			userMap[u.ID] = u.Username
		}
	}

	// 拼装带用户名的响应
	type logItem struct {
		model.OperationLog
		Username string `json:"username"`
	}
	result := make([]logItem, 0, len(logs))
	for _, l := range logs {
		result = append(result, logItem{
			OperationLog: l,
			Username:     userMap[l.OpUserID],
		})
	}

	c.JSON(200, gin.H{
		"data":  result,
		"total": total,
		"page":  page,
	})
}

func (h *SystemHandler) ClearLogs(c *gin.Context) {
	var cfg model.SystemConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	days := cfg.LogRetentionDay
	if days <= 0 {
		days = 90
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	result := model.DB.Where("created_at < ?", cutoff).Delete(&model.OperationLog{})

	c.JSON(200, gin.H{
		"message":      "清理完成",
		"deleted_rows": result.RowsAffected,
	})
}

func (h *SystemHandler) Info(c *gin.Context) {
	var totalProjects, totalTasks, totalPools, totalModels int64
	var runningTasks, failedTasks int64

	model.DB.Model(&model.Project{}).Count(&totalProjects)
	model.DB.Model(&model.Task{}).Count(&totalTasks)
	model.DB.Model(&model.ResourcePool{}).Count(&totalPools)
	model.DB.Model(&model.LLMModel{}).Count(&totalModels)
	model.DB.Model(&model.Task{}).Where("status = ?", "running").Count(&runningTasks)
	model.DB.Model(&model.Task{}).Where("status = ?", "failed").Count(&failedTasks)

	uptime := time.Since(h.startTime)
	uptimeStr := uptime.Round(time.Minute).String()

	c.JSON(200, gin.H{
		"version":        "v1.0.0",
		"uptime":         uptimeStr,
		"db_status":      "ok",
		"total_projects": totalProjects,
		"total_tasks":    totalTasks,
		"total_pools":    totalPools,
		"total_models":   totalModels,
		"running_tasks":  runningTasks,
		"failed_tasks":   failedTasks,
	})
}
