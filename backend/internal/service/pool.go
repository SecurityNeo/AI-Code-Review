package service

import (
	"errors"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/encrypt"
	"go.uber.org/zap"
)

type PoolService struct{}

func NewPoolService() *PoolService {
	return &PoolService{}
}

func (s *PoolService) List(scope *model.UserAuthScope, name string) ([]model.ResourcePool, error) {
	if scope == nil {
		return nil, errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	var pools []model.ResourcePool
	query := db
	if name != "" {
		query = query.Where("name LIKE ?", "%"+name+"%")
	}
	if err := query.Find(&pools).Error; err != nil {
		return nil, err
	}
	return pools, nil
}

func (s *PoolService) Get(scope *model.UserAuthScope, id uint) (*model.ResourcePool, error) {
	if scope == nil {
		return nil, errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	var pool model.ResourcePool
	if err := db.First(&pool, id).Error; err != nil {
		return nil, err
	}
	return &pool, nil
}

func (s *PoolService) Create(scope *model.UserAuthScope, data map[string]interface{}) (*model.ResourcePool, error) {
	if scope == nil {
		return nil, errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	password, _ := encrypt.Encrypt(data["opencode_password"].(string))
	
	// 多租户改造：从 data 中读取 org_id（handler 层已注入）
	var orgID uint = 1
	if oid, ok := data["org_id"]; ok {
		orgID = uint(oid.(float64))
	}
	
	pool := model.ResourcePool{
		OrgID:            orgID,
		Name:             data["name"].(string),
		OpencodeEndpoint: data["opencode_endpoint"].(string),
		OpencodeUsername: data["opencode_username"].(string),
		OpencodePassword: password,
		MaxParallel:      int(data["max_parallel"].(float64)),
		CheckIntervalSec: int(data["check_interval_sec"].(float64)),
		Status:           model.PoolActive,
	}
	if err := db.Create(&pool).Error; err != nil {
		return nil, err
	}
	return &pool, nil
}

func (s *PoolService) Update(scope *model.UserAuthScope, id uint, data map[string]interface{}) error {
	if scope == nil {
		return errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	var pool model.ResourcePool
	if err := db.First(&pool, id).Error; err != nil {
		return err
	}
	return db.Model(&model.ResourcePool{}).Where("id = ?", id).Updates(data).Error
}

func (s *PoolService) Delete(scope *model.UserAuthScope, id uint) error {
	if scope == nil {
		return errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	var pool model.ResourcePool
	if err := db.First(&pool, id).Error; err != nil {
		return err
	}
	return db.Delete(&model.ResourcePool{}, id).Error
}

func (s *PoolService) Toggle(scope *model.UserAuthScope, id uint) error {
	if scope == nil {
		return errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	var pool model.ResourcePool
	if err := db.First(&pool, id).Error; err != nil {
		return err
	}
	var newStatus model.PoolStatus
	if pool.Status == model.PoolActive {
		newStatus = model.PoolInactive
	} else {
		newStatus = model.PoolActive
	}
	return db.Model(&model.ResourcePool{}).Where("id = ?", id).Update("status", newStatus).Error
}

func (s *PoolService) SetDefault(scope *model.UserAuthScope, id uint) error {
	if scope == nil {
		return errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	tx := db.Begin()
	if err := tx.Model(&model.ResourcePool{}).Where("id = ?", id).Update("is_default", true).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Model(&model.ResourcePool{}).Where("id != ?", id).Update("is_default", false).Error; err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	return nil
}

func (s *PoolService) UnsetDefault(scope *model.UserAuthScope) error {
	if scope == nil {
		return errors.New("scope is required")
	}
	db := model.DBWithScope(scope)
	return db.Model(&model.ResourcePool{}).Where("is_default = ?", true).Update("is_default", false).Error
}

func (s *PoolService) CheckConnectivity(id uint) (bool, string, string, error) {
	opencodeSvc := NewOpencodeService()
	return opencodeSvc.CheckConnectivity(id)
}

func (s *PoolService) HealthCheckAll() {
	zap.L().Debug("pool health check running")
	var pools []model.ResourcePool
	if err := model.DB.Where("status != ?", model.PoolInactive).Find(&pools).Error; err != nil {
		zap.L().Error("health check failed", zap.Error(err))
		return
	}
	for _, pool := range pools {
		connected, errMsg, version, _ := s.CheckConnectivity(pool.ID)
		status := model.PoolActive
		if !connected {
			status = model.PoolError
		}
		updates := map[string]interface{}{
			"status":      status,
			"check_error": truncateCheckError(errMsg),
		}
		if connected && version != "" {
			updates["opencode_version"] = version
		}
		model.DB.Model(&pool).Updates(updates)
	}
}

func (s *PoolService) StartHealthCheckDaemon() {
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			s.runPoolHealthChecks()
		}
	}()
	zap.L().Info("pool health check daemon started")
}

func (s *PoolService) runPoolHealthChecks() {
	var pools []model.ResourcePool
	if err := model.DB.Where("status != ?", model.PoolInactive).Find(&pools).Error; err != nil {
		zap.L().Error("pool health check failed", zap.Error(err))
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
	for _, pool := range pools {
		interval := pool.CheckIntervalSec
		if interval <= 0 {
			interval = 5
		}
		shouldCheck := pool.LastCheckAt == nil || now.After(pool.LastCheckAt.Add(time.Duration(interval)*time.Second))
		if !shouldCheck {
			continue
		}
		// 提前更新 last_check_at，避免 CheckConnectivity 阻塞期间其他 tick 重复检查同一资源池
		model.DB.Model(&pool).Update("last_check_at", now)

		connected, errMsg, version, _ := s.CheckConnectivity(pool.ID)
		prevStatus := pool.Status
		newStatus := model.PoolActive
		if !connected {
			newStatus = model.PoolError
		}

		updates := map[string]interface{}{
			"status":      newStatus,
			"check_error": truncateCheckError(errMsg),
		}
		if connected && version != "" {
			updates["opencode_version"] = version
		}

		statusChanged := string(prevStatus) != string(newStatus)
		if statusChanged {
			updates["status_changed_at"] = now
		}

		if err := model.DB.Model(&pool).Updates(updates).Error; err != nil {
			zap.L().Error("pool update status failed", zap.Uint("pool_id", pool.ID), zap.Error(err))
			continue
		}

		// 恢复通知
		if statusChanged && newStatus == model.PoolActive && prevStatus == model.PoolError {
			scope := &model.UserAuthScope{VisibleOrgIDs: []uint{pool.OrgID}}
			SendResourcePoolAlert(scope, pool, true, alertDuration, alertCooldown, notifierID, mentionIDs)
			// 恢复时重置告警时间点，下次异常重新开始计时
			model.DB.Model(&pool).Update("last_alert_at", nil)
		}

		// 异常持续达标 + 冷却期满足则告警
		if newStatus == model.PoolError {
			var sct time.Time
			if pool.StatusChangedAt != nil {
				sct = *pool.StatusChangedAt
			}
			if statusChanged {
				// 刚变为异常，需要等待持续达标
				zap.L().Info("pool entered error state, waiting for alert duration",
					zap.Uint("pool_id", pool.ID),
					zap.Int("alert_duration_sec", alertDuration))
				continue
			}
			elapsed := int(now.Sub(sct).Seconds())
			if elapsed < alertDuration {
				zap.L().Info("pool error not reached alert duration yet",
					zap.Uint("pool_id", pool.ID),
					zap.Int("elapsed_sec", elapsed),
					zap.Int("alert_duration_sec", alertDuration))
				continue
			}
			lastAlert := time.Time{}
			if pool.LastAlertAt != nil {
				lastAlert = *pool.LastAlertAt
			}
			if pool.LastAlertAt == nil {
				// 从未告警过，直接触发
				zap.L().Info("pool alert triggered (first alert)",
					zap.Uint("pool_id", pool.ID),
					zap.Int("elapsed_sec", elapsed))
				scope := &model.UserAuthScope{VisibleOrgIDs: []uint{pool.OrgID}}
				SendResourcePoolAlert(scope, pool, false, alertDuration, alertCooldown, notifierID, mentionIDs)
				model.DB.Model(&pool).Update("last_alert_at", now)
			} else {
				sinceLastAlert := int(now.Sub(lastAlert).Seconds())
				if sinceLastAlert >= alertCooldown {
					zap.L().Info("pool alert triggered (cooldown passed)",
						zap.Uint("pool_id", pool.ID),
						zap.Int("since_last_alert_sec", sinceLastAlert),
						zap.Int("cooldown_sec", alertCooldown))
					scope := &model.UserAuthScope{VisibleOrgIDs: []uint{pool.OrgID}}
					SendResourcePoolAlert(scope, pool, false, alertDuration, alertCooldown, notifierID, mentionIDs)
					model.DB.Model(&pool).Update("last_alert_at", now)
				} else {
					zap.L().Info("pool alert skipped (in cooldown)",
						zap.Uint("pool_id", pool.ID),
						zap.Int("since_last_alert_sec", sinceLastAlert),
						zap.Int("cooldown_sec", alertCooldown),
						zap.Int("remaining_sec", alertCooldown-sinceLastAlert))
				}
			}
		}
	}
}
