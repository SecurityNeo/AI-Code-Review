package depaudit

import (
	"errors"
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultConfig 返回组织默认配置（未持久化）
func DefaultConfig(orgID uint) *model.DependencyAuditConfig {
	return &model.DependencyAuditConfig{
		OrgID:                 orgID,
		LockfilePrecise:       true,
		IncludeTransitive:     false,
		ReachabilityEnabled:   false,
		ScheduleEnabled:       true,
		LicenseComplianceMode: ModeReportOnly,
		DefaultLicenseAction:  ActionReview,
		RunsKeep:              30,
		TaskKeepDays:          30,
	}
}

// GetConfig 读取组织配置，不存在时返回默认值（不写库）
func GetConfig(db *gorm.DB, orgID uint) (*model.DependencyAuditConfig, error) {
	var cfg model.DependencyAuditConfig
	err := db.Where("org_id = ?", orgID).First(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DefaultConfig(orgID), nil
		}
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig 保存组织配置（原子 upsert）。校验枚举值，避免脏数据。
func SaveConfig(db *gorm.DB, cfg *model.DependencyAuditConfig) error {
	if cfg.OrgID == 0 {
		return errors.New("org_id required")
	}
	if cfg.LicenseComplianceMode == "" {
		cfg.LicenseComplianceMode = ModeReportOnly
	}
	if cfg.LicenseComplianceMode != ModeReportOnly && cfg.LicenseComplianceMode != ModeEnforced {
		return fmt.Errorf("invalid license_compliance_mode: %s", cfg.LicenseComplianceMode)
	}
	if cfg.DefaultLicenseAction == "" {
		cfg.DefaultLicenseAction = ActionReview
	}
	if cfg.DefaultLicenseAction != ActionAllow && cfg.DefaultLicenseAction != ActionReview {
		return fmt.Errorf("invalid default_license_action: %s", cfg.DefaultLicenseAction)
	}
	if cfg.RunsKeep <= 0 {
		cfg.RunsKeep = 30
	}
	if cfg.RunsKeep > 1000 {
		cfg.RunsKeep = 1000
	}
	if cfg.TaskKeepDays <= 0 {
		cfg.TaskKeepDays = 30
	}
	if cfg.TaskKeepDays > 3650 {
		cfg.TaskKeepDays = 3650
	}
	cfg.ID = 0 // 以 org_id 唯一键 upsert，忽略客户端传入的主键

	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "org_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"lockfile_precise", "include_transitive", "reachability_enabled",
			"schedule_enabled", "license_compliance_mode", "allowed_licenses",
			"forbidden_licenses", "review_required_licenses", "default_license_action",
			"runs_keep", "task_keep_days", "updated_at",
		}),
	}).Create(cfg).Error
}
