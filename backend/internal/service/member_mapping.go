package service

import (
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

type MemberMappingService struct{}

func NewMemberMappingService() *MemberMappingService {
	return &MemberMappingService{}
}

// List 返回映射列表，支持按 git_username 搜索
func (s *MemberMappingService) List(gitUsername string, page, pageSize int) ([]model.MemberMapping, int64, error) {
	var mappings []model.MemberMapping
	var total int64

	query := model.DB.Model(&model.MemberMapping{})
	if gitUsername != "" {
		query = query.Where("git_username LIKE ?", "%"+gitUsername+"%")
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}

	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Limit(pageSize).Offset(offset).Find(&mappings).Error; err != nil {
		return nil, 0, err
	}

	return mappings, total, nil
}

// Get 根据 ID 获取映射
func (s *MemberMappingService) Get(id uint) (*model.MemberMapping, error) {
	var mapping model.MemberMapping
	if err := model.DB.First(&mapping, id).Error; err != nil {
		return nil, err
	}
	return &mapping, nil
}

// GetByGitUsername 根据 Git 用户名和平台查询映射
func (s *MemberMappingService) GetByGitUsername(gitUsername string, platform model.IMPlatform) (*model.MemberMapping, error) {
	var mapping model.MemberMapping
	if err := model.DB.Where("git_username = ? AND im_platform = ? AND enabled = ?", gitUsername, platform, true).First(&mapping).Error; err != nil {
		return nil, err
	}
	return &mapping, nil
}

// Create 创建映射
func (s *MemberMappingService) Create(data map[string]interface{}) (*model.MemberMapping, error) {
	gitUsername, _ := data["git_username"].(string)
	imPlatform, _ := data["im_platform"].(string)
	imUserID, _ := data["im_user_id"].(string)
	displayName, _ := data["display_name"].(string)

	if gitUsername == "" || imUserID == "" {
		return nil, fmt.Errorf("git_username and im_user_id are required")
	}

	platform := model.IMPlatform(imPlatform)
	if platform == "" {
		platform = model.IMPlatformWeCom
	}

	mapping := model.MemberMapping{
		GitUsername: gitUsername,
		IMPlatform:  platform,
		IMUserID:    imUserID,
		DisplayName: displayName,
	}

	if err := model.DB.Create(&mapping).Error; err != nil {
		return nil, err
	}
	return &mapping, nil
}

// Update 更新映射
func (s *MemberMappingService) Update(id uint, data map[string]interface{}) error {
	updates := make(map[string]interface{})

	if v, ok := data["im_user_id"].(string); ok && v != "" {
		updates["im_user_id"] = v
	}
	if v, ok := data["display_name"].(string); ok {
		updates["display_name"] = v
	}
	if v, ok := data["im_platform"].(string); ok && v != "" {
		updates["im_platform"] = model.IMPlatform(v)
	}

	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	return model.DB.Model(&model.MemberMapping{}).Where("id = ?", id).Updates(updates).Error
}

// Delete 删除映射
func (s *MemberMappingService) Delete(id uint) error {
	return model.DB.Delete(&model.MemberMapping{}, id).Error
}

// GetGitUsers 获取系统中已知的 Git 用户名列表（从 Task 表中聚合）
func (s *MemberMappingService) GetGitUsers() ([]string, error) {
	var usernames []string
	if err := model.DB.Model(&model.Task{}).
		Where("mr_author != ?", "").
		Distinct("mr_author").
		Pluck("mr_author", &usernames).Error; err != nil {
		zap.L().Error("get git users failed", zap.Error(err))
		return nil, err
	}
	return usernames, nil
}
