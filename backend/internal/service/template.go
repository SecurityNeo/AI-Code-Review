package service

import (
	"github.com/ai-optimizer/backend/internal/model"
)

type TemplateService struct{}

func NewTemplateService() *TemplateService {
	return &TemplateService{}
}

func (s *TemplateService) List(scope *model.UserAuthScope) ([]model.ProjectTemplate, error) {
	db := model.DBWithScope(scope)
	var templates []model.ProjectTemplate
	if err := db.Order("created_at DESC").Find(&templates).Error; err != nil {
		return nil, err
	}
	return templates, nil
}

func (s *TemplateService) Get(scope *model.UserAuthScope, id uint) (*model.ProjectTemplate, error) {
	db := model.DBWithScope(scope)
	var t model.ProjectTemplate
	if err := db.First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *TemplateService) Create(scope *model.UserAuthScope, t *model.ProjectTemplate) error {
	db := model.DBWithScope(scope)
	if t.DimensionWeights == "" {
		t.DimensionWeights = "{}"
	}
	return db.Create(t).Error
}

func (s *TemplateService) Update(scope *model.UserAuthScope, id uint, fields map[string]interface{}) error {
	db := model.DBWithScope(scope)
	return db.Model(&model.ProjectTemplate{}).Where("id = ?", id).Updates(fields).Error
}

func (s *TemplateService) Delete(scope *model.UserAuthScope, id uint) error {
	db := model.DBWithScope(scope)
	// Check if any project uses this template
	var count int64
	db.Model(&model.Project{}).Where("template_id = ?", id).Count(&count)
	if count > 0 {
		return ErrTemplateInUse
	}
	return db.Delete(&model.ProjectTemplate{}, id).Error
}

func (s *TemplateService) Clone(scope *model.UserAuthScope, id uint, newName string) (*model.ProjectTemplate, error) {
	db := model.DBWithScope(scope)
	var original model.ProjectTemplate
	if err := db.First(&original, id).Error; err != nil {
		return nil, err
	}
	clone := model.ProjectTemplate{
		OrgID:                 original.OrgID,
		Name:                  newName,
		Description:           original.Description,
		Prompt:                original.Prompt,
		CustomInstruction:     original.CustomInstruction,
		DimensionWeights:      original.DimensionWeights,
		MaxRulesPerReview:     original.MaxRulesPerReview,
		GitLabCommentTemplate: original.GitLabCommentTemplate,
	}
	if err := db.Create(&clone).Error; err != nil {
		return nil, err
	}
	return &clone, nil
}

var ErrTemplateInUse = &templateError{"模板正在被项目使用，无法删除"}

type templateError struct {
	msg string
}

func (e *templateError) Error() string {
	return e.msg
}
