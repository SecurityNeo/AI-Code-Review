package service

import (
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type ProjectService struct{}

func NewProjectService() *ProjectService {
	return &ProjectService{}
}

func (s *ProjectService) List(scope *model.UserAuthScope, page, pageSize int, keyword, status, source string, projectIDs []uint, filterOrgIDs []uint) ([]model.Project, int64, error) {
	var projects []model.Project
	var total int64

	db := model.DBWithScope(scope).Model(&model.Project{})
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
	// 按组织ID过滤（分配职责场景优先，含后代组织）
	if len(filterOrgIDs) > 0 {
		db = db.Where("org_id IN ?", filterOrgIDs)
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

// isZeroID 判断外键值是否需要按 NULL 处理（兼容 int/uint/float64/json 等类型）
func isZeroID(v interface{}) bool {
	switch x := v.(type) {
	case nil:
		return true
	case int:
		return x == 0
	case int32:
		return x == 0
	case int64:
		return x == 0
	case uint:
		return x == 0
	case uint32:
		return x == 0
	case uint64:
		return x == 0
	case float64:
		return x == 0
	case string:
		return x == "" || x == "0"
	}
	return false
}

// redactFields 复制并脱敏敏感字段，避免明文写入日志
func redactFields(fields map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		if k == "access_token" {
			out[k] = "***"
		} else {
			out[k] = v
		}
	}
	return out
}

func (s *ProjectService) Update(id uint, fields map[string]interface{}) error {
	zap.L().Info("project update begin", zap.Uint("id", id), zap.Any("fields", redactFields(fields)))

	// 外键字段为空时必须写入 NULL（写 0 会触发外键约束失败）
	for _, fk := range []string{"default_model_id", "template_id", "pool_id"} {
		if v, ok := fields[fk]; ok && isZeroID(v) {
			delete(fields, fk)
			if err := model.DB.Model(&model.Project{}).Where("id = ?", id).
				UpdateColumn(fk, nil).Error; err != nil {
				zap.L().Error("project update: clear foreign key failed", zap.Uint("id", id), zap.String("column", fk), zap.Error(err))
				return err
			}
			zap.L().Info("project update: foreign key cleared", zap.Uint("id", id), zap.String("column", fk))
		}
	}

	// 剩余字段一次性更新
	if len(fields) > 0 {
		if err := model.DB.Model(&model.Project{}).Where("id = ?", id).Updates(fields).Error; err != nil {
			zap.L().Error("project update: remaining fields failed", zap.Uint("id", id), zap.Any("remaining_fields", redactFields(fields)), zap.Error(err))
			return err
		}
		zap.L().Info("project update: remaining fields success", zap.Uint("id", id), zap.Any("remaining_fields", redactFields(fields)))
	}

	return nil
}

func (s *ProjectService) Create(data *model.Project) error {
	// 外键字段为空时写 NULL（写 0 会触发外键约束失败）；同一事务内关闭外键检查以兼容历史行为
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		tx.Exec("SET FOREIGN_KEY_CHECKS=0")
		defer tx.Exec("SET FOREIGN_KEY_CHECKS=1") // 同连接内恢复，避免污染连接池
		createDB := tx
		if data.TemplateID == 0 {
			createDB = createDB.Omit("template_id")
		}
		if data.PoolID == 0 {
			createDB = createDB.Omit("pool_id")
		}
		if data.DefaultModelID == nil {
			createDB = createDB.Omit("default_model_id")
		}
		return createDB.Create(data).Error
	}); err != nil {
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
