package service

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type ModelService struct{}

var (
	ErrModelNotFound        = errors.New("模型不存在")
	ErrCannotDeleteDefault  = errors.New("不能删除默认模型")
	ErrModelExists          = errors.New("模型已存在")
	ErrCannotDisablePrimary = errors.New("不能禁用主模型，请先取消主模型后再禁用")
	ErrCannotDisableDefault = errors.New("不能禁用默认模型，请先取消默认后再禁用")
	ErrBackupOrderConflict  = errors.New("备用顺序冲突，该顺序已被其他模型占用")
)

func NewModelService() *ModelService {
	return &ModelService{}
}

func (s *ModelService) List(page, pageSize int, keyword string) ([]model.LLMModel, int64, error) {
	return s.ListByType(page, pageSize, keyword, "")
}

func (s *ModelService) ListByType(page, pageSize int, keyword, modelType string) ([]model.LLMModel, int64, error) {
	var models []model.LLMModel
	var total int64

	db := model.DB.Model(&model.LLMModel{})
	if keyword != "" {
		db = db.Where("model_id LIKE ?", "%"+keyword+"%")
	}
	if modelType != "" {
		db = db.Where("model_type = ?", modelType)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := db.Scopes(model.Paginate(page, pageSize)).
		Order("created_at DESC").
		Find(&models).Error; err != nil {
		return nil, 0, err
	}

	// Mask API key for display
	for i := range models {
		if models[i].APIKey != "" {
			models[i].APIKey = maskAPIKey(models[i].APIKey)
		}
	}

	return models, total, nil
}

func (s *ModelService) Get(id uint) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := model.DB.First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrModelNotFound
		}
		return nil, err
	}

	// Mask API key
	if m.APIKey != "" {
		m.APIKey = maskAPIKey(m.APIKey)
	}

	return &m, nil
}

func (s *ModelService) GetDefault() (*model.LLMModel, error) {
	var m model.LLMModel
	if err := model.DB.Where("is_default = ?", true).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrModelNotFound
		}
		return nil, err
	}

	if m.APIKey != "" {
		m.APIKey = maskAPIKey(m.APIKey)
	}

	return &m, nil
}

func (s *ModelService) Create(req *CreateModelRequest) (*model.LLMModel, error) {
	// Check if provider + model_id combination exists
	var count int64
	model.DB.Model(&model.LLMModel{}).
		Where("provider = ? AND model_id = ?", req.Provider, req.ModelID).
		Count(&count)
	if count > 0 {
		return nil, ErrModelExists
	}

	modelType := req.ModelType
	if modelType == "" {
		modelType = "llm"
	}

	// If setting as primary, unset other primaries first (only for LLM type)
	if req.IsPrimary && modelType == "llm" {
		model.DB.Model(&model.LLMModel{}).Where("is_primary = ?", true).Update("is_primary", false)
	}

	m := &model.LLMModel{
		ModelType:        modelType,
		Provider:         req.Provider,
		ModelID:          req.ModelID,
		BaseURL:          req.BaseURL,
		APIKey:           req.APIKey, // 明文存储
		MaxTokens:        req.MaxTokens,
		TimeoutSec:       req.TimeoutSec,
		CheckIntervalSec: req.CheckIntervalSec,
		Temperature:      req.Temperature,
		IsDefault:        req.IsDefault,
		IsPrimary:        req.IsPrimary && modelType == "llm",
		BackupOrder:      req.BackupOrder,
	}

	if m.CheckIntervalSec <= 0 {
		m.CheckIntervalSec = 5
	}

	if err := model.DB.Create(m).Error; err != nil {
		return nil, err
	}

	m.APIKey = maskAPIKey(req.APIKey) // Mask for return
	return m, nil
}

func (s *ModelService) Update(id uint, req *UpdateModelRequest) error {
	m, err := s.Get(id)
	if err != nil {
		return err
	}

	if req.BackupOrder != nil && *req.BackupOrder > 0 {
		var conflict int64
		model.DB.Model(&model.LLMModel{}).
			Where("backup_order = ? AND id != ? AND status != ?", *req.BackupOrder, id, "inactive").
			Count(&conflict)
		if conflict > 0 {
			return ErrBackupOrderConflict
		}
	}

	updates := map[string]interface{}{}

	if req.ModelType != nil && *req.ModelType != "" {
		updates["model_type"] = *req.ModelType
	}
	if req.BaseURL != nil && *req.BaseURL != "" {
		updates["base_url"] = *req.BaseURL
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		updates["max_tokens"] = *req.MaxTokens
	}
	if req.TimeoutSec != nil && *req.TimeoutSec > 0 {
		updates["timeout_sec"] = *req.TimeoutSec
	}
	if req.CheckIntervalSec != nil && *req.CheckIntervalSec > 0 {
		updates["check_interval_sec"] = *req.CheckIntervalSec
	}
	if req.Temperature != nil {
		updates["temperature"] = *req.Temperature
	}

	// Only update model_id if provided and different
	if req.ModelID != nil && *req.ModelID != "" && *req.ModelID != m.ModelID {
		var count int64
		model.DB.Model(&model.LLMModel{}).
			Where("provider = ? AND model_id = ? AND id != ?", m.Provider, *req.ModelID, id).
			Count(&count)
		if count > 0 {
			return ErrModelExists
		}
		updates["model_id"] = *req.ModelID
	}

	// Handle API key update
	if req.APIKey != nil && *req.APIKey != "" {
		updates["api_key"] = *req.APIKey
	}

	// Handle primary / backup update (only for LLM type)
	if req.IsPrimary != nil && *req.IsPrimary != m.IsPrimary {
		if *req.IsPrimary {
			model.DB.Model(&model.LLMModel{}).Where("is_primary = ?", true).Update("is_primary", false)
			updates["backup_order"] = 0
		}
		updates["is_primary"] = *req.IsPrimary
	}

	if req.BackupOrder != nil && *req.BackupOrder != m.BackupOrder {
		if *req.BackupOrder > 0 {
			updates["is_primary"] = false
		}
		updates["backup_order"] = *req.BackupOrder
	}

	if req.IsDefault != nil {
		updates["is_default"] = *req.IsDefault
	}

	if len(updates) == 0 {
		return nil
	}

	if err := model.DB.Model(&model.LLMModel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}

	return nil
}

func (s *ModelService) Delete(id uint) error {
	m, err := s.GetForUpdate(id)
	if err != nil {
		return err
	}

	if m.IsDefault {
		return ErrCannotDeleteDefault
	}

	return model.DB.Delete(&model.LLMModel{}, id).Error
}

func (s *ModelService) SetDefault(id uint) error {
	_, err := s.GetForUpdate(id)
	if err != nil {
		return err
	}

	// Unset all other defaults
	if err := model.DB.Model(&model.LLMModel{}).Where("is_default = ?", true).Update("is_default", false).Error; err != nil {
		return err
	}

	// Set this one as default
	return model.DB.Model(&model.LLMModel{}).Where("id = ?", id).Update("is_default", true).Error
}

func (s *ModelService) UnsetDefault(id uint) error {
	_, err := s.GetForUpdate(id)
	if err != nil {
		return err
	}

	return model.DB.Model(&model.LLMModel{}).Where("id = ?", id).Update("is_default", false).Error
}

func (s *ModelService) CheckConnectivity(id uint) (bool, error) {
	var m model.LLMModel
	var err error
	if err = model.DB.First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrModelNotFound
		}
		return false, err
	}

	provider := llm.Provider(m.Provider)
	err = llm.CheckConnectivity(provider, m.BaseURL, m.APIKey, time.Duration(m.TimeoutSec)*time.Second, m.ModelID)
	if err != nil {
		zap.L().Debug("model connectivity check failed",
			zap.String("provider", m.Provider),
			zap.String("model_id", m.ModelID),
			zap.Error(err))
		return false, err
	}

	return true, nil
}

// StartHealthCheckDaemon 启动模型健康检查守护进程
func (s *ModelService) StartHealthCheckDaemon() {
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			s.runModelHealthChecks()
		}
	}()
	zap.L().Info("model health check daemon started")
}

func (s *ModelService) runModelHealthChecks() {
	var models []model.LLMModel
	if err := model.DB.Where("status != ?", "inactive").Find(&models).Error; err != nil {
		zap.L().Error("model health check failed", zap.Error(err))
		return
	}

	// 读取系统告警配置
	var cfg model.SystemConfig
	alertDuration, alertCooldown := 300, 3600
	notifierID := uint(0)
	mentionIDs := ""
	if err := model.SilentFirst(model.DB, &cfg); err == nil {
		alertDuration = cfg.AlertDurationSec
		if alertDuration <= 0 {
			alertDuration = 300
		}
		alertCooldown = cfg.AlertCooldownSec
		if alertCooldown <= 0 {
			alertCooldown = 3600
		}
		notifierID = cfg.AlertNotifierID
		mentionIDs = cfg.AlertMentionUserIDs
	}

	now := time.Now()
	for _, m := range models {
		interval := m.CheckIntervalSec
		if interval <= 0 {
			interval = 5
		}
		shouldCheck := m.LastCheckAt == nil || now.After(m.LastCheckAt.Add(time.Duration(interval)*time.Second))
		if !shouldCheck {
			continue
		}
		// 提前更新 last_check_at，避免 CheckConnectivity 阻塞期间其他 tick 重复检查同一模型
		model.DB.Model(&m).Update("last_check_at", now)

		connected, err := s.CheckConnectivity(m.ID)
		errMsg := ""
		if !connected && err != nil {
			errMsg = err.Error()
		}
		prevStatus := m.Status
		newStatus := "active"
		if !connected {
			newStatus = "error"
		}

		updates := map[string]interface{}{
			"status":      newStatus,
			"check_error": truncateCheckError(errMsg),
		}
		statusChanged := prevStatus != newStatus
		if statusChanged {
			updates["status_changed_at"] = now
		}

		if err := model.DB.Model(&m).Updates(updates).Error; err != nil {
			zap.L().Error("model update status failed", zap.Uint("model_id", m.ID), zap.Error(err))
			continue
		}

		// 恢复通知
		if statusChanged && newStatus == "active" && prevStatus == "error" {
			SendModelAlert(m, true, alertDuration, alertCooldown, notifierID, mentionIDs)
			// 恢复时重置告警时间点，下次异常重新开始计时
			model.DB.Model(&m).Update("last_alert_at", nil)
		}

		// 异常持续达标 + 冷却期满足则告警
		if newStatus == "error" {
			sct := time.Time{}
			if m.StatusChangedAt != nil {
				sct = *m.StatusChangedAt
			}
			if statusChanged {
				zap.L().Info("model entered error state, waiting for alert duration",
					zap.Uint("model_id", m.ID),
					zap.Int("alert_duration_sec", alertDuration))
				continue
			}
			elapsed := int(now.Sub(sct).Seconds())
			if elapsed < alertDuration {
				zap.L().Info("model error not reached alert duration yet",
					zap.Uint("model_id", m.ID),
					zap.Int("elapsed_sec", elapsed),
					zap.Int("alert_duration_sec", alertDuration))
				continue
			}
			lastAlert := time.Time{}
			if m.LastAlertAt != nil {
				lastAlert = *m.LastAlertAt
			}
			if m.LastAlertAt == nil {
				zap.L().Info("model alert triggered (first alert)",
					zap.Uint("model_id", m.ID),
					zap.Int("elapsed_sec", elapsed))
				SendModelAlert(m, false, alertDuration, alertCooldown, notifierID, mentionIDs)
				model.DB.Model(&m).Update("last_alert_at", now)
			} else {
				sinceLastAlert := int(now.Sub(lastAlert).Seconds())
				if sinceLastAlert >= alertCooldown {
					zap.L().Info("model alert triggered (cooldown passed)",
						zap.Uint("model_id", m.ID),
						zap.Int("since_last_alert_sec", sinceLastAlert),
						zap.Int("cooldown_sec", alertCooldown))
					SendModelAlert(m, false, alertDuration, alertCooldown, notifierID, mentionIDs)
					model.DB.Model(&m).Update("last_alert_at", now)
				} else {
					zap.L().Info("model alert skipped (in cooldown)",
						zap.Uint("model_id", m.ID),
						zap.Int("since_last_alert_sec", sinceLastAlert),
						zap.Int("cooldown_sec", alertCooldown),
						zap.Int("remaining_sec", alertCooldown-sinceLastAlert))
				}
			}
		}
	}
}

// GetForUpdate returns model for update
func (s *ModelService) GetForUpdate(id uint) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := model.DB.First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrModelNotFound
		}
		return nil, err
	}

	return &m, nil
}

// Disable 禁用模型
func (s *ModelService) Disable(id uint) error {
	m, err := s.GetForUpdate(id)
	if err != nil {
		return err
	}

	if m.IsPrimary {
		return ErrCannotDisablePrimary
	}
	if m.IsDefault {
		return ErrCannotDisableDefault
	}

	return model.DB.Model(&model.LLMModel{}).Where("id = ?", id).Update("status", "inactive").Error
}

// Enable 启用模型
func (s *ModelService) Enable(id uint) error {
	_, err := s.GetForUpdate(id)
	if err != nil {
		return err
	}
	return model.DB.Model(&model.LLMModel{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":      "active",
		"check_error": "",
	}).Error
}

// CheckConnectivityByConfig 测试配置连通性（不保存）
func (s *ModelService) CheckConnectivityByConfig(provider, baseURL, apiKey, modelID string, timeoutSec int) (bool, error) {
	provider = NormalizeProvider(provider)
	err := llm.CheckConnectivity(llm.Provider(provider), baseURL, apiKey, time.Duration(timeoutSec)*time.Second, modelID)
	if err != nil {
		return false, err
	}
	return true, nil
}

// CheckEmbeddingConnectivityByConfig tests embedding endpoint connectivity.
func (s *ModelService) CheckEmbeddingConnectivityByConfig(baseURL, apiKey, modelID string, timeoutSec int) (bool, error) {
	if err := llm.CheckEmbeddingConnectivity(baseURL, apiKey, modelID, time.Duration(timeoutSec)*time.Second); err != nil {
		return false, err
	}
	return true, nil
}

// GetModelForJob returns the model configuration for creating K8s job
func (s *ModelService) GetModelForJob(modelID uint) (string, string, error) {
	var m model.LLMModel
	if modelID > 0 {
		if err := model.DB.First(&m, modelID).Error; err != nil {
			return "", "", err
		}
	} else {
		// Use default model
		if err := model.DB.Where("is_default = ?", true).First(&m).Error; err != nil {
			return "", "", fmt.Errorf("no default model configured")
		}
	}

	return m.BaseURL, m.APIKey, nil
}

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// --- Request DTOs ---

type CreateModelRequest struct {
	ModelType        string  `json:"model_type"`
	Provider         string  `json:"provider" binding:"required"`
	ModelID          string  `json:"model_id" binding:"required"`
	BaseURL          string  `json:"base_url" binding:"required"`
	APIKey           string  `json:"api_key" binding:"required"`
	MaxTokens        int     `json:"max_tokens"`
	TimeoutSec       int     `json:"timeout_sec"`
	CheckIntervalSec int     `json:"check_interval_sec"`
	Temperature      float64 `json:"temperature"`
	IsDefault        bool    `json:"is_default"`
	IsPrimary        bool    `json:"is_primary"`
	BackupOrder      int     `json:"backup_order"`
}

type UpdateModelRequest struct {
	ModelType        *string  `json:"model_type,omitempty"`
	ModelID          *string  `json:"model_id,omitempty"`
	BaseURL          *string  `json:"base_url,omitempty"`
	APIKey           *string  `json:"api_key,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	TimeoutSec       *int     `json:"timeout_sec,omitempty"`
	CheckIntervalSec *int     `json:"check_interval_sec,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	IsDefault        *bool    `json:"is_default,omitempty"`
	IsPrimary        *bool    `json:"is_primary,omitempty"`
	BackupOrder      *int     `json:"backup_order,omitempty"`
}

// NormalizeProvider normalizes provider string to standard format
func NormalizeProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "openai":
		return "openai"
	case "anthropic", "claude":
		return "anthropic"
	case "azure", "openai azure":
		return "azure"
	case "deepseek":
		return "deepseek"
	case "vllm", "custom":
		return "vllm"
	default:
		return "vllm"
	}
}

// truncateCheckError 防御性截断 check_error 字符串。
// 数据库列已改为 TEXT（最大 64KB），但 LiteLLM/OpenCode 错误链偶尔会带 SSL 堆栈等冗长内容。
// 截断到 4000 字符既保证完整可读，又避免极端情况溢出（也保留截断标记便于排查）。
func truncateCheckError(s string) string {
	const maxLen = 4000
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + "...(已截断)"
}
