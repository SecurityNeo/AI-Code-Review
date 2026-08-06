package service

import (
	"errors"
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/encrypt"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type UserService struct{}

func NewUserService() *UserService {
	return &UserService{}
}

// InitAdmin 初始化 admin 用户（如果不存在）
func (s *UserService) InitAdmin() error {
	var user model.User
	err := model.SilentFirst(model.DB.Where("username = ?", "admin"), &user)

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query admin user failed: %w", err)
	}

	// 用户已存在
	if err == nil {
		zap.L().Info("admin user already exists")
		return nil
	}

	// 创建默认 admin 用户
	hashedPassword, err := encrypt.HashPassword("admin123")
	if err != nil {
		return fmt.Errorf("hash password failed: %w", err)
	}
	admin := model.User{
		Username:    "admin",
		DisplayName: "系统管理员",
		Password:    hashedPassword,
		Role:        "admin",
		LoginType:   "local",
	}
	if err := model.DB.Create(&admin).Error; err != nil {
		return fmt.Errorf("create admin user failed: %w", err)
	}
	zap.L().Info("admin user created with default password: admin123")
	return nil
}

// ValidateLogin 验证登录
func (s *UserService) ValidateLogin(username, password string) (*model.User, bool) {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, false
	}

	// 兼容旧数据：password 为空时（GitLab 用户或迁移数据）不能走本地密码验证
	if user.Password == "" {
		return nil, false
	}

	if !encrypt.CheckPassword(password, user.Password) {
		return nil, false
	}

	return &user, true
}

// ChangePassword 修改密码
func (s *UserService) ChangePassword(userID uint, oldPassword, newPassword string) error {
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return fmt.Errorf("user not found")
	}

	// 验证旧密码
	if !encrypt.CheckPassword(oldPassword, user.Password) {
		return fmt.Errorf("旧密码错误")
	}

	// 哈希新密码
	hashedPassword, err := encrypt.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password failed: %w", err)
	}

	// 更新密码
	if err := model.DB.Model(&user).Update("password", hashedPassword).Error; err != nil {
		return fmt.Errorf("update password failed: %w", err)
	}

	return nil
}

// GetByID 根据ID获取用户
func (s *UserService) GetByID(id uint) (*model.User, error) {
	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// ListUsers 用户列表（分页、搜索、角色筛选）
func (s *UserService) ListUsers(keyword, role, loginType string, page, pageSize int) ([]model.User, int64, error) {
	db := model.DB.Model(&model.User{})
	if keyword != "" {
		db = db.Where("username LIKE ? OR display_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if role != "" {
		db = db.Where("role = ?", role)
	}
	if loginType != "" {
		db = db.Where("login_type = ?", loginType)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var users []model.User
	if err := db.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

// CreateUser 创建本地用户（管理员操作）
func (s *UserService) CreateUser(username, displayName, password, role string) (*model.User, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("用户名和密码不能为空")
	}
	if role != model.RoleAdmin && role != model.RoleUser {
		return nil, fmt.Errorf("角色必须为 admin 或 user")
	}
	// 检查用户名是否已存在
	var exist model.User
	if err := model.DB.Where("username = ?", username).First(&exist).Error; err == nil {
		return nil, fmt.Errorf("用户名 %s 已存在", username)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	hashedPassword, err := encrypt.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password failed: %w", err)
	}
	user := model.User{
		Username:    username,
		DisplayName: displayName,
		Password:    hashedPassword,
		Role:        role,
		LoginType:   "local",
	}
	if err := model.DB.Create(&user).Error; err != nil {
		return nil, fmt.Errorf("create user failed: %w", err)
	}
	return &user, nil
}

// UpdateUser 更新用户信息（管理员操作）
func (s *UserService) UpdateUser(id uint, displayName, role string) error {
	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		return fmt.Errorf("用户不存在")
	}
	updates := map[string]interface{}{}
	if displayName != "" {
		updates["display_name"] = displayName
	}
	if role != "" {
		if role != model.RoleAdmin && role != model.RoleUser {
			return fmt.Errorf("角色必须为 admin 或 user")
		}
		updates["role"] = role
	}
	if len(updates) == 0 {
		return nil
	}
	return model.DB.Model(&user).Updates(updates).Error
}

// DeleteUser 删除用户（管理员操作）
func (s *UserService) DeleteUser(id uint, currentUserID uint) error {
	if id == currentUserID {
		return fmt.Errorf("不能删除当前登录用户")
	}
	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		return fmt.Errorf("用户不存在")
	}
	if user.Role == model.RoleAdmin {
		return fmt.Errorf("管理员账号不允许删除")
	}
	// 删除用户的 token
	model.DB.Where("user_id = ?", id).Delete(&model.Token{})
	return model.DB.Delete(&user).Error
}

// ResetPassword 重置用户密码（管理员操作）
func (s *UserService) ResetPassword(id uint, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("新密码不能为空")
	}
	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		return fmt.Errorf("用户不存在")
	}
	if user.LoginType == "gitlab" {
		return fmt.Errorf("GitLab 用户不支持修改本地密码")
	}
	hashedPassword, err := encrypt.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password failed: %w", err)
	}
	return model.DB.Model(&user).Update("password", hashedPassword).Error
}
