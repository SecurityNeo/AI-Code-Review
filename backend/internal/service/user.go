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

	if err == nil {
		// 用户已存在，确保 default_org_id 和 org_users 关联正确（兼容老数据升级）
		if user.DefaultOrgID == 0 {
			user.DefaultOrgID = 1
			if err := model.DB.Model(&user).Update("default_org_id", 1).Error; err != nil {
				zap.L().Error("update admin default_org_id failed", zap.Error(err))
				return fmt.Errorf("update admin default_org_id failed: %w", err)
			}
		}
		var orgUserCount int64
		if err := model.DB.Model(&model.OrgUser{}).Where("user_id = ? AND org_id = 1", user.ID).Count(&orgUserCount).Error; err != nil {
			zap.L().Error("count admin org_user failed", zap.Error(err))
			return fmt.Errorf("count admin org_user failed: %w", err)
		}
		if orgUserCount == 0 {
			if err := model.DB.Create(&model.OrgUser{
				OrgID:     1,
				UserID:    user.ID,
				Role:      "super_admin",
				IsDefault: true,
				Status:    "active",
			}).Error; err != nil {
				zap.L().Error("create admin org_user failed", zap.Error(err))
				return fmt.Errorf("create admin org_user failed: %w", err)
			}
		}
		zap.L().Info("admin user already exists, org relation ensured")
		return nil
	}

	// 创建默认 admin 用户
	hashedPassword, err := encrypt.HashPassword("admin123")
	if err != nil {
		return fmt.Errorf("hash password failed: %w", err)
	}
	admin := model.User{
		Username:       "admin",
		DisplayName:    "系统管理员",
		Password:       hashedPassword,
		Role:           "admin",
		LoginType:      "local",
		DefaultOrgID:   1,
	}
	if err := model.DB.Create(&admin).Error; err != nil {
		return fmt.Errorf("create admin user failed: %w", err)
	}

	// 创建 admin 与根组织的关联
	if err := model.DB.Create(&model.OrgUser{
		OrgID:     1,
		UserID:    admin.ID,
		Role:      "super_admin",
		IsDefault: true,
		Status:    "active",
	}).Error; err != nil {
		zap.L().Error("create admin org_user failed", zap.Error(err))
		return fmt.Errorf("create admin org_user failed: %w", err)
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

// ListUsers 用户列表（分页、搜索）
// 不再支持按已废弃的 users.role 筛选
// 支持按 org_id 过滤（通过 org_users 关联）
func (s *UserService) ListUsers(keyword, loginType string, orgID uint, page, pageSize int) ([]model.User, int64, error) {
	db := model.DB.Model(&model.User{})
	if keyword != "" {
		db = db.Where("username LIKE ? OR display_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if loginType != "" {
		db = db.Where("login_type = ?", loginType)
	}
	// 按组织过滤：通过 org_users 关联查询
	if orgID > 0 {
		db = db.Where("EXISTS (SELECT 1 FROM org_users WHERE org_users.user_id = users.id AND org_users.org_id = ? AND org_users.status = 'active')", orgID)
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
// 新创建的用户默认角色为 developer，不再接受已废弃的 users.role
func (s *UserService) CreateUser(username, displayName, password, imPlatform, imUserID string) (*model.User, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("用户名和密码不能为空")
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
		Role:        model.RoleUser, // 保留字段兼容，不再读取
		LoginType:   "local",
		IMPlatform:  imPlatform,
		IMUserID:    imUserID,
	}
	if err := model.DB.Create(&user).Error; err != nil {
		return nil, fmt.Errorf("create user failed: %w", err)
	}
	return &user, nil
}

// UpdateUser 更新用户信息（管理员操作）
// version 为乐观锁版本号；若传 0 则不进行版本校验（兼容旧客户端）
// nil 指针表示不更新该字段
func (s *UserService) UpdateUser(id uint, displayName, imPlatform, imUserID *string, version int) error {
	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		return fmt.Errorf("用户不存在")
	}
	updates := map[string]interface{}{}
	if displayName != nil {
		updates["display_name"] = *displayName
	}
	// IM 信息：nil 表示不更新，允许显式置空（传空字符串指针）
	if imPlatform != nil {
		updates["im_platform"] = *imPlatform
	}
	if imUserID != nil {
		updates["im_user_id"] = *imUserID
	}
	if len(updates) == 0 {
		return nil
	}

	// 乐观锁更新
	db := model.DB.Model(&model.User{}).Where("id = ?", id)
	if version > 0 {
		db = db.Where("version = ?", version)
	}
	result := db.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if version > 0 && result.RowsAffected == 0 {
		return &model.OptimisticUpdateError{Entity: "User", ID: id, Version: version}
	}
	return nil
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
	// 检查被删除用户是否有 super_admin 角色（基于 org_users.role，废弃 users.role）
	var superAdminCount int64
	model.DB.Model(&model.OrgUser{}).Where("user_id = ? AND role = ? AND status = 'active'", id, "super_admin").Count(&superAdminCount)
	if superAdminCount > 0 {
		return fmt.Errorf("系统管理员账号不允许删除")
	}
	// 删除用户的 token
	model.DB.Where("user_id = ?", id).Delete(&model.Token{})
	// 删除用户的项目职责分配（确保人员退役后 no orphaned responsibilities）
	model.DB.Where("user_id = ?", id).Delete(&model.ProjectResponsibility{})
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
