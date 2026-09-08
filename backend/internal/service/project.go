package service

import (
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

type ProjectService struct{}

func NewProjectService() *ProjectService {
	return &ProjectService{}
}

func (s *ProjectService) List(page, pageSize int, keyword, status, source string, projectIDs []uint, filterOrgID uint) ([]model.Project, int64, error) {
	var projects []model.Project
	var total int64

	db := model.DB.Model(&model.Project{})
	if keyword != "" {
		db = db.Where("name LIKE ? OR project_path LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if status == "active" {
		db = db.Where("ai_enabled = ?", true)
	} else if status == "inactive" {
		db = db.Where("ai_enabled = ?", false)
	}
	if source != "" {
		db = db.Where("source = ?", source)
	}
	// 按组织ID过滤（分配职责场景优先）
	if filterOrgID > 0 {
		db = db.Where("org_id = ?", filterOrgID)
	} else if len(projectIDs) > 0 {
		// 按项目ID过滤（非管理员默认行为）
		db = db.Where("id IN ?", projectIDs)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := db.Scopes(model.Paginate(page, pageSize)).
		Preload("Template").
		Preload("Pool").
		Preload("Model").
		Order("created_at DESC").
		Find(&projects).Error; err != nil {
		return nil, 0, err
	}

	// 规则库规则总数（所有项目共享同一套规则，查询一次即可）
	var allRules []model.ReviewRule
	model.DB.Find(&allRules)

	for i := range projects {
		var tasks []model.Task
		model.DB.Where("project_id = ?", projects[i].ID).
			Order("created_at DESC").
			Limit(5).
			Find(&tasks)
		projects[i].Tasks = tasks

		// 统计该项目实际启用的规则数
		// 业务逻辑：项目未配置某规则时使用全局状态，配置了则以项目配置为准
		var configs []model.ProjectReviewConfig
		model.DB.Where("project_id = ?", projects[i].ID).Find(&configs)
		configMap := make(map[uint]bool)
		for _, c := range configs {
			configMap[c.RuleID] = c.IsEnabled
		}

		enabledCount := 0
		for _, rule := range allRules {
			if projectEnabled, hasConfig := configMap[rule.ID]; hasConfig {
				// 项目有配置，以项目配置为准
				if projectEnabled {
					enabledCount++
				}
			} else {
				// 项目无配置，以全局规则状态为准
				if rule.IsEnabled {
					enabledCount++
				}
			}
		}

		// 规则库规则总数
		totalCount := len(allRules)

		projects[i].EnabledRuleCount = enabledCount
		projects[i].TotalRuleCount = totalCount
	}

	return projects, total, nil
}

func (s *ProjectService) Get(id uint) (*model.Project, error) {
	var p model.Project
	if err := model.DB.Preload("Template").Preload("Pool").Preload("Model").First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *ProjectService) Update(id uint, fields map[string]interface{}) error {
	zap.L().Info("project update begin", zap.Uint("id", id), zap.Any("fields", fields))

	// 如有 default_model_id=null，先单独清空该列（GORM Updates 无法直接写入 nil）
	if v, ok := fields["default_model_id"]; ok && v == nil {
		delete(fields, "default_model_id")
		if err := model.DB.Model(&model.Project{}).Where("id = ?", id).
			UpdateColumn("default_model_id", nil).Error; err != nil {
			zap.L().Error("project update: clear default_model_id failed", zap.Uint("id", id), zap.Error(err))
			return err
		}
		zap.L().Info("project update: default_model_id cleared", zap.Uint("id", id))
	}

	// template_id=0 时也要显式清空，否则 GORM Updates 会跳过零值
	if v, ok := fields["template_id"]; ok {
		if iv, ok2 := v.(int); ok2 && iv == 0 {
			delete(fields, "template_id")
			if err := model.DB.Model(&model.Project{}).Where("id = ?", id).
				UpdateColumn("template_id", 0).Error; err != nil {
				zap.L().Error("project update: clear template_id failed", zap.Uint("id", id), zap.Error(err))
				return err
			}
			zap.L().Info("project update: template_id cleared", zap.Uint("id", id))
		}
	}

	// 剩余字段一次性更新
	if len(fields) > 0 {
		if err := model.DB.Model(&model.Project{}).Where("id = ?", id).Updates(fields).Error; err != nil {
			zap.L().Error("project update: remaining fields failed", zap.Uint("id", id), zap.Any("remaining_fields", fields), zap.Error(err))
			return err
		}
		zap.L().Info("project update: remaining fields success", zap.Uint("id", id), zap.Any("remaining_fields", fields))
	}

	return nil
}

func (s *ProjectService) Create(data *model.Project) error {
	// Disable foreign key checks for template_id
	model.DB.Exec("SET FOREIGN_KEY_CHECKS=0")
	defer model.DB.Exec("SET FOREIGN_KEY_CHECKS=1")
	if err := model.DB.Create(data).Error; err != nil {
		return err
	}

	// 为新建项目生成默认规则配置（否则项目列表规则数始终为 0/0）
	var rules []model.ReviewRule
	model.DB.Where("is_enabled = ? AND (language = 'common' OR language = ?)", true, data.Language).Find(&rules)
	for _, rule := range rules {
		cfg := model.ProjectReviewConfig{
			ProjectID: data.ID,
			RuleID:    rule.ID,
			IsEnabled: true,
			Severity:  "", // 使用规则默认级别
		}
		if err := model.DB.Create(&cfg).Error; err != nil {
			zap.L().Warn("init project review config after create failed",
				zap.Uint("project_id", data.ID), zap.Uint("rule_id", rule.ID), zap.Error(err))
		}
	}
	zap.L().Info("project review configs initialized after creation",
		zap.Uint("project_id", data.ID), zap.Int("rules", len(rules)))
	return nil
}

func (s *ProjectService) Delete(id uint) error {
	// 检查是否有运行中任务
	var count int64
	model.DB.Model(&model.Task{}).Where("project_id = ? AND status = ?", id, model.TaskRunning).Count(&count)
	if count > 0 {
		return fmt.Errorf("存在运行中的任务，无法删除")
	}
	return model.DB.Delete(&model.Project{}, id).Error
}

func (s *ProjectService) GetProjectTasks(projectID uint, page, pageSize int) ([]model.Task, int64, error) {
	var tasks []model.Task
	var total int64

	db := model.DB.Where("project_id = ?", projectID)
	if err := db.Model(&model.Task{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Scopes(model.Paginate(page, pageSize)).Order("created_at DESC").Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

// Options 返回项目名称列表（用于下拉框选择）
type ProjectOption struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

func (s *ProjectService) Options(projectIDs []uint) ([]ProjectOption, error) {
	var opts []ProjectOption
	db := model.DB.Model(&model.Project{}).
		Select("id", "name").
		Order("name ASC")
	if len(projectIDs) > 0 {
		db = db.Where("id IN ?", projectIDs)
	}
	if err := db.Find(&opts).Error; err != nil {
		return nil, err
	}
	return opts, nil
}
