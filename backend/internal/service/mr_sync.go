package service

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/gitlab"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	mrSyncCron    *cron.Cron
	mrSyncEntryID cron.EntryID
	mrSyncMu      sync.Mutex
)

// InitMRSyncCron 在 main.go 启动时初始化 MR 同步定时任务
func InitMRSyncCron(c *cron.Cron) {
	mrSyncCron = c
	RebuildMRSyncCron(nil)
}

// RebuildMRSyncCron 重建 MR 同步定时任务（配置更新后调用）
func RebuildMRSyncCron(scope *model.UserAuthScope) {
	mrSyncMu.Lock()
	defer mrSyncMu.Unlock()

	if mrSyncCron == nil {
		zap.L().Warn("mr sync cron not initialized")
		return
	}

	// 移除旧任务
	if mrSyncEntryID > 0 {
		mrSyncCron.Remove(mrSyncEntryID)
		mrSyncEntryID = 0
	}

	db := model.DBWithScope(scope)
	var cfg model.SystemConfig
	if err := model.SilentFirst(db, &cfg); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			zap.L().Info("mr sync: system config not found, skipping")
		} else {
			zap.L().Error("mr sync: get config failed", zap.Error(err))
		}
		return
	}

	if cfg.MRSyncIntervalSec <= 0 {
		zap.L().Info("mr sync: disabled (interval <= 0)")
		return
	}

	spec := fmt.Sprintf("@every %ds", cfg.MRSyncIntervalSec)
	id, err := mrSyncCron.AddFunc(spec, func() {
		NewMRSyncService().SyncOpenedMRs(scope)
	})
	if err != nil {
		zap.L().Error("mr sync: add cron job failed", zap.String("spec", spec), zap.Error(err))
		return
	}

	mrSyncEntryID = id
	zap.L().Info("mr sync cron scheduled", zap.Int("interval_sec", cfg.MRSyncIntervalSec), zap.String("spec", spec))
}

// MRSyncService 提供 MR 状态同步能力
type MRSyncService struct{}

// NewMRSyncService 创建 MR 同步服务
func NewMRSyncService() *MRSyncService {
	return &MRSyncService{}
}

// SyncOpenedMRs 轮询 GitLab API，刷新本地非 merged 状态 MR 的 mr_state、is_draft 等字段
func (s *MRSyncService) SyncOpenedMRs(scope *model.UserAuthScope) {
	db := model.DBWithScope(scope)
	var cfg model.SystemConfig
	if err := model.SilentFirst(db, &cfg); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			zap.L().Info("mr sync: system config not found, skipping")
		} else {
			zap.L().Error("mr sync: get config failed", zap.Error(err))
		}
		return
	}

	if cfg.MRSyncIntervalSec <= 0 {
		return
	}

	// 每次最多同步 50 条最久未同步的 非 merged 状态 MR（opened、closed 都需要刷新）
	var logs []model.MergeRequestReviewLog
	if err := db.Where("mr_state != ?", "merged").
		Order("synced_at ASC").
		Limit(50).
		Find(&logs).Error; err != nil {
		zap.L().Error("mr sync: query non-merged mr failed", zap.Error(err))
		return
	}

	if len(logs) == 0 {
		return
	}

	// 缓存项目配置，避免重复查库
	projectCache := make(map[string]*model.Project)

	for i := range logs {
		log := &logs[i]
		project := s.getProjectFromCache(scope, log.ProjectName, log.OrgID, projectCache)
		if project == nil || project.GitLabProjectID == 0 {
			continue
		}

		token := project.AccessToken
		if token == "" {
			token = cfg.GitlabToken
		}
		if token == "" {
			zap.L().Warn("mr sync: no token available", zap.String("project", log.ProjectName))
			continue
		}

		// 调用 GitLab API 刷新详情
		if err := gitlab.FetchMRDetails(log, project.GitLabProjectID, token); err != nil {
			zap.L().Warn("mr sync: fetch details failed", zap.String("url", log.URL), zap.Error(err))
			continue
		}

		// 更新 synced_at 并保存
		now := time.Now()
		log.SyncedAt = now
		if err := db.Omit("org_id").Save(log).Error; err != nil { // ⚠️ 多租户改造：Omit org_id 防止零值覆盖
			zap.L().Error("mr sync: save log failed", zap.Uint("id", log.ID), zap.Error(err))
		}
	}
}

func (s *MRSyncService) getProjectFromCache(scope *model.UserAuthScope, projectName string, orgID uint, cache map[string]*model.Project) *model.Project {
	cacheKey := fmt.Sprintf("%d:%s", orgID, projectName)
	if p, ok := cache[cacheKey]; ok {
		return p
	}
	db := model.DBWithScope(scope)
	var project model.Project
	if err := db.Where("name = ? AND org_id = ?", projectName, orgID).First(&project).Error; err != nil {
		cache[cacheKey] = nil
		return nil
	}
	cache[cacheKey] = &project
	return &project
}
