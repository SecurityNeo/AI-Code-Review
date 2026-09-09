package service

import (
	"errors"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// ReviewRuleService 评审规则服务
type ReviewRuleService struct{}

func NewReviewRuleService() *ReviewRuleService {
	return &ReviewRuleService{}
}

// List 获取规则库列表（含近 7 天命中次数）
func (s *ReviewRuleService) List(scope *model.UserAuthScope, category, language, isEnabled, keyword string, page, pageSize int) ([]model.ReviewRule, int64, map[string]int64, error) {
	db := model.DB.Model(&model.ReviewRule{})
	if scope != nil && !scope.IsSuperAdmin {
		db = db.Where("org_id IN ? OR is_built_in = ?", scope.VisibleOrgIDs, true)
	}
	if category != "" {
		db = db.Where("category = ?", category)
	}
	if language != "" {
		db = db.Where("language = ?", language)
	}
	if isEnabled != "" {
		db = db.Where("is_enabled = ?", isEnabled == "true")
	}
	if keyword != "" {
		db = db.Where("name LIKE ? OR code LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, nil, err
	}

	var rules []model.ReviewRule
	if err := db.Order("sort_order ASC, id ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rules).Error; err != nil {
		return nil, 0, nil, err
	}

	hitMap := map[string]int64{}
	if len(rules) > 0 {
		sevenDaysAgo := time.Now().AddDate(0, 0, -7)
		type hitRow struct {
			RuleCode string `gorm:"column:rule_code"`
			Cnt      int64  `gorm:"column:cnt"`
		}
		var rows []hitRow
		dbHit := model.DBWithScope(scope).Table("review_issues").
			Select("rule_code, COUNT(*) AS cnt").
			Where("rule_code != '' AND created_at >= ?", sevenDaysAgo)
		if err := dbHit.Group("rule_code").
			Scan(&rows).Error; err != nil {
			zap.L().Warn("load hit_count_7d failed", zap.Error(err))
		}
		for _, r := range rows {
			hitMap[r.RuleCode] = r.Cnt
		}
	}

	return rules, total, hitMap, nil
}

// Tree 按语言、维度分组返回规则树
func (s *ReviewRuleService) Tree(scope *model.UserAuthScope) (map[string]map[string][]model.ReviewRule, error) {
	db := model.DB.Where("is_enabled = ?", true).Order("sort_order ASC")
	if scope != nil && !scope.IsSuperAdmin {
		db = db.Where("org_id IN ? OR is_built_in = ?", scope.VisibleOrgIDs, true)
	}
	var rules []model.ReviewRule
	if err := db.Find(&rules).Error; err != nil {
		return nil, err
	}

	tree := make(map[string]map[string][]model.ReviewRule)
	for _, rule := range rules {
		if _, ok := tree[rule.Language]; !ok {
			tree[rule.Language] = make(map[string][]model.ReviewRule)
		}
		tree[rule.Language][rule.Category] = append(tree[rule.Language][rule.Category], rule)
	}

	return tree, nil
}

// BatchEnable 批量更新规则启用状态（内置规则仅系统管理员可操作）
func (s *ReviewRuleService) BatchEnable(scope *model.UserAuthScope, ruleIDs []uint, isEnabled bool) error {
	var rules []model.ReviewRule
	if err := model.DB.Where("id IN ?", ruleIDs).Find(&rules).Error; err != nil {
		return err
	}
	if scope != nil && !scope.IsSuperAdmin {
		for _, r := range rules {
			if r.IsBuiltIn {
				return errors.New("内置规则仅系统管理员可开启/关闭")
			}
			found := false
			for _, vid := range scope.VisibleOrgIDs {
				if r.OrgID == vid {
					found = true
					break
				}
			}
			if !found {
				return errors.New("无权操作该规则")
			}
		}
	}
	db := model.DBWithScope(scope).Model(&model.ReviewRule{})
	return db.Where("id IN ?", ruleIDs).Update("is_enabled", isEnabled).Error
}

// Create 创建自定义规则
func (s *ReviewRuleService) Create(scope *model.UserAuthScope, rule *model.ReviewRule) error {
	db := model.DBWithScope(scope)
	if err := db.Create(rule).Error; err != nil {
		return err
	}
	// 兜底：无论数据库列默认值如何，强制覆盖为 false
	db.Model(rule).UpdateColumn("is_built_in", false)
	rule.IsBuiltIn = false
	autoCreateProjectReviewConfigsForRule(scope, rule.OrgID, rule.ID)
	return nil
}

// Get 根据 ID 获取规则
func (s *ReviewRuleService) Get(scope *model.UserAuthScope, id uint) (*model.ReviewRule, error) {
	var rule model.ReviewRule
	db := model.DB
	if scope != nil && !scope.IsSuperAdmin {
		db = db.Where("org_id IN ? OR is_built_in = ?", scope.VisibleOrgIDs, true)
	}
	if err := db.First(&rule, id).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

// Update 编辑自定义规则
func (s *ReviewRuleService) Update(scope *model.UserAuthScope, id uint, fields map[string]interface{}) error {
	db := model.DBWithScope(scope)
	var rule model.ReviewRule
	if err := db.First(&rule, id).Error; err != nil {
		return err
	}
	return db.Model(&rule).Updates(fields).Error
}

// Delete 删除自定义规则
func (s *ReviewRuleService) Delete(scope *model.UserAuthScope, id uint) error {
	db := model.DBWithScope(scope)
	var rule model.ReviewRule
	if err := db.First(&rule, id).Error; err != nil {
		return err
	}
	return db.Delete(&rule).Error
}

// autoCreateProjectReviewConfigsForRule 为所有已有项目自动插入新规则的默认配置（默认禁用）
func autoCreateProjectReviewConfigsForRule(scope *model.UserAuthScope, orgID uint, ruleID uint) {
	db := model.DBWithScope(scope)
	var projects []model.Project
	if err := db.Where("org_id = ?", orgID).Find(&projects).Error; err != nil {
		zap.L().Warn("auto create project review configs: find projects failed", zap.Error(err))
		return
	}

	for _, p := range projects {
		// 检查是否已存在
		var count int64
		db.Model(&model.ProjectReviewConfig{}).
			Where("org_id = ? AND project_id = ? AND rule_id = ?", orgID, p.ID, ruleID).
			Count(&count)
		if count > 0 {
			continue
		}

		cfg := model.ProjectReviewConfig{
			OrgID:     orgID,
			ProjectID: p.ID,
			RuleID:    ruleID,
			IsEnabled: false, // 默认禁用，用户需要手动在项目中启用
			Severity:  "",
		}
		if err := db.Create(&cfg).Error; err != nil {
			zap.L().Warn("auto create project review config failed",
				zap.Uint("project_id", p.ID),
				zap.Uint("rule_id", ruleID),
				zap.Error(err))
		}
	}
}
