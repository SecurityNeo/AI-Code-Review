package service

import (
	"github.com/ai-optimizer/backend/internal/model"
)

type TemplateService struct{}

func NewTemplateService() *TemplateService {
	return &TemplateService{}
}

func (s *TemplateService) List() ([]model.ProjectTemplate, error) {
	var templates []model.ProjectTemplate
	if err := model.DB.Order("created_at DESC").Find(&templates).Error; err != nil {
		return nil, err
	}
	return templates, nil
}

func (s *TemplateService) Get(id uint) (*model.ProjectTemplate, error) {
	var t model.ProjectTemplate
	if err := model.DB.First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *TemplateService) Create(t *model.ProjectTemplate) error {
	if t.DimensionWeights == "" {
		t.DimensionWeights = "{}"
	}
	return model.DB.Create(t).Error
}

func (s *TemplateService) Update(id uint, fields map[string]interface{}) error {
	return model.DB.Model(&model.ProjectTemplate{}).Where("id = ?", id).Updates(fields).Error
}

func (s *TemplateService) Delete(id uint) error {
	// Check if any project uses this template
	var count int64
	model.DB.Model(&model.Project{}).Where("template_id = ?", id).Count(&count)
	if count > 0 {
		return ErrTemplateInUse
	}
	return model.DB.Delete(&model.ProjectTemplate{}, id).Error
}

func (s *TemplateService) Clone(id uint, newName string) (*model.ProjectTemplate, error) {
	var original model.ProjectTemplate
	if err := model.DB.First(&original, id).Error; err != nil {
		return nil, err
	}
	clone := model.ProjectTemplate{
		Name:                  newName,
		Description:           original.Description,
		Prompt:                original.Prompt,
		CustomInstruction:     original.CustomInstruction,
		DimensionWeights:      original.DimensionWeights,
		MaxRulesPerReview:     original.MaxRulesPerReview,
		GitLabCommentTemplate: original.GitLabCommentTemplate,
	}
	if err := model.DB.Create(&clone).Error; err != nil {
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
