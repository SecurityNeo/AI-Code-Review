package service

import (
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// TeamMemberService 团队人员与项目职责管理
type TeamMemberService struct{}

func NewTeamMemberService() *TeamMemberService {
	return &TeamMemberService{}
}

// ==================== TeamMember CRUD ====================

// List 返回人员列表，支持按 gitlab_username / display_name 搜索
func (s *TeamMemberService) List(scope *model.UserAuthScope, search string, page, pageSize int) ([]model.TeamMember, int64, error) {
	db := model.DBWithScope(scope)
	var members []model.TeamMember
	var total int64

	query := db.Model(&model.TeamMember{})
	if search != "" {
		query = query.Where("gitlab_username LIKE ? OR display_name LIKE ? OR username LIKE ?",
			"%"+search+"%", "%"+search+"%", "%"+search+"%")
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
	if err := query.Preload("Responsibilities.Project").Order("created_at DESC").Limit(pageSize).Offset(offset).Find(&members).Error; err != nil {
		return nil, 0, err
	}

	return members, total, nil
}

// Get 根据 ID 获取人员详情（含职责列表）
func (s *TeamMemberService) Get(scope *model.UserAuthScope, id uint) (*model.TeamMember, error) {
	db := model.DBWithScope(scope)
	var member model.TeamMember
	if err := db.Preload("Responsibilities.Project").First(&member, id).Error; err != nil {
		return nil, err
	}
	return &member, nil
}

// GetByGitlabUsername 根据 GitLab 用户名查找人员
func (s *TeamMemberService) GetByGitlabUsername(scope *model.UserAuthScope, gitlabUsername string) (*model.TeamMember, error) {
	if gitlabUsername == "" {
		return nil, fmt.Errorf("gitlab_username is empty")
	}
	db := model.DBWithScope(scope)
	var member model.TeamMember
	if err := db.Where("gitlab_username = ? AND enabled = ?", gitlabUsername, true).First(&member).Error; err != nil {
		return nil, err
	}
	return &member, nil
}

// Create 创建人员
func (s *TeamMemberService) Create(scope *model.UserAuthScope, data map[string]interface{}) (*model.TeamMember, error) {
	username, _ := data["username"].(string)
	displayName, _ := data["display_name"].(string)
	gitlabUsername, _ := data["gitlab_username"].(string)
	imPlatform, _ := data["im_platform"].(string)
	imUserID, _ := data["im_user_id"].(string)
	email, _ := data["email"].(string)
	role, _ := data["role"].(string)

	if gitlabUsername == "" {
		return nil, fmt.Errorf("gitlab_username is required")
	}
	if username == "" {
		username = gitlabUsername
	}
	if imPlatform == "" {
		imPlatform = "wecom"
	}
	if role == "" {
		role = "member"
	}

	member := model.TeamMember{
		Username:       username,
		DisplayName:    displayName,
		GitlabUsername: gitlabUsername,
		IMPlatform:     imPlatform,
		IMUserID:       imUserID,
		Email:          email,
		Role:           role,
		Enabled:        true,
	}

	db := model.DBWithScope(scope)
	if err := db.Create(&member).Error; err != nil {
		return nil, err
	}
	return &member, nil
}

// Update 更新人员基础信息
func (s *TeamMemberService) Update(scope *model.UserAuthScope, id uint, data map[string]interface{}) error {
	updates := make(map[string]interface{})

	if v, ok := data["username"].(string); ok && v != "" {
		updates["username"] = v
	}
	if v, ok := data["display_name"].(string); ok {
		updates["display_name"] = v
	}
	if v, ok := data["gitlab_username"].(string); ok && v != "" {
		updates["gitlab_username"] = v
	}
	if v, ok := data["im_platform"].(string); ok && v != "" {
		updates["im_platform"] = v
	}
	if v, ok := data["im_user_id"].(string); ok {
		updates["im_user_id"] = v
	}
	if v, ok := data["email"].(string); ok {
		updates["email"] = v
	}
	if v, ok := data["role"].(string); ok && v != "" {
		updates["role"] = v
	}
	if v, ok := data["enabled"].(bool); ok {
		updates["enabled"] = v
	}

	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	db := model.DBWithScope(scope)
	return db.Model(&model.TeamMember{}).Where("id = ?", id).Updates(updates).Error
}

// Delete 删除人员（级联删除职责）
func (s *TeamMemberService) Delete(scope *model.UserAuthScope, id uint) error {
	db := model.DBWithScope(scope)
	db.Where("member_id = ?", id).Delete(&model.ProjectResponsibility{})
	return db.Delete(&model.TeamMember{}, id).Error
}

// GetGitUsers 获取系统中已知的 Git 用户名列表（从 Task 表中聚合）
func (s *TeamMemberService) GetGitUsers(scope *model.UserAuthScope) ([]string, error) {
	db := model.DBWithScope(scope)
	var usernames []string
	if err := db.Model(&model.Task{}).
		Where("mr_author != ?", "").
		Distinct("mr_author").
		Pluck("mr_author", &usernames).Error; err != nil {
		zap.L().Error("get git users failed", zap.Error(err))
		return nil, err
	}
	return usernames, nil
}

// ==================== ProjectResponsibility CRUD ====================

// ListResponsibilitiesByMember 列出某人的项目职责
func (s *TeamMemberService) ListResponsibilitiesByMember(scope *model.UserAuthScope, memberID uint) ([]model.ProjectResponsibility, error) {
	db := model.DBWithScope(scope)
	var list []model.ProjectResponsibility
	if err := db.Preload("User").Preload("Project").
		Where("member_id = ?", memberID).
		Order("project_id, scope_type, priority").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListResponsibilitiesByProject 列出某项目的所有职责分配（过滤已禁用用户）
func (s *TeamMemberService) ListResponsibilitiesByProject(scope *model.UserAuthScope, projectID uint) ([]model.ProjectResponsibility, error) {
	// 不使用 DBWithScope：职责的 org_id 可能因历史原因与项目 org_id 不一致。
	// 权限由调用方（Handler）通过项目可见性校验前置保证。
	var list []model.ProjectResponsibility
	if err := model.DB.Preload("User").Preload("Member").Preload("Project").
		Where("project_id = ?", projectID).
		Order("scope_type, priority, created_at").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return filterActiveResponsibilities(list), nil
}

// AddResponsibility 为人员添加项目职责
func (s *TeamMemberService) AddResponsibility(scope *model.UserAuthScope, memberID uint, data map[string]interface{}) (*model.ProjectResponsibility, error) {
	db := model.DBWithScope(scope)
	// 校验 member 存在性
	var member model.TeamMember
	if err := db.First(&member, memberID).Error; err != nil {
		return nil, fmt.Errorf("member not found: %w", err)
	}

	projectIDFloat, _ := data["project_id"].(float64)
	projectID := uint(projectIDFloat)
	scopeType, _ := data["scope_type"].(string)
	scopeValue, _ := data["scope_value"].(string)
	priorityFloat, _ := data["priority"].(float64)
	priority := int(priorityFloat)

	if projectID == 0 {
		return nil, fmt.Errorf("project_id is required")
	}
	if scopeType == "" {
		scopeType = "default"
	}

	// 校验 scope_value：非 default 时必须非空
	if scopeType != "default" && scopeValue == "" {
		return nil, fmt.Errorf("scope_value is required for non-default scope_type")
	}
	if scopeType == "default" {
		scopeValue = ""
	}
	// 语言值标准化
	if scopeType == "language" && scopeValue != "" {
		scopeValue = normalizeLanguage(scopeValue)
	}

	// 查找 member 对应的 user_id（通过 gitlab_username 或 username 关联 users 表）
	var linkedUser model.User
	if err := db.Where("gitlab_username = ? OR username = ?", member.GitlabUsername, member.Username).First(&linkedUser).Error; err != nil {
		zap.L().Warn("AddResponsibility: no matching user found for member",
			zap.Uint("member_id", memberID),
			zap.String("gitlab_username", member.GitlabUsername),
			zap.String("username", member.Username))
	}

	// 检查首要责任人唯一性（priority <= 1 时）
	if err := checkPrimaryStewardConflict(scope, projectID, scopeType, scopeValue, priority, 0); err != nil {
		return nil, err
	}

	// 查询项目获取 org_id
	var project model.Project
	if err := model.DB.Select("org_id").First(&project, projectID).Error; err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}

	resp := model.ProjectResponsibility{
		OrgID:          project.OrgID,
		MemberID:       &memberID,
		UserID:         linkedUser.ID,
		ProjectID:      projectID,
		ScopeType:      scopeType,
		ScopeValue:     scopeValue,
		Priority:       priority,
		NotifyChannels: "[]",
	}
	if err := db.Create(&resp).Error; err != nil {
		return nil, err
	}

	db.Preload("Member").Preload("Project").First(&resp, resp.ID)
	return &resp, nil
}

// UpdateResponsibility 更新职责
func (s *TeamMemberService) UpdateResponsibility(scope *model.UserAuthScope, rid uint, data map[string]interface{}) error {
	db := model.DBWithScope(scope)
	// 1. 查询当前记录以确定最终维度
	var current model.ProjectResponsibility
	if err := db.First(&current, rid).Error; err != nil {
		return fmt.Errorf("responsibility not found: %w", err)
	}

	updates := make(map[string]interface{})

	if v, ok := data["scope_type"].(string); ok && v != "" {
		updates["scope_type"] = v
	}
	if v, ok := data["scope_value"].(string); ok {
		updates["scope_value"] = v
	}
	if v, ok := data["priority"].(float64); ok {
		updates["priority"] = int(v)
	}

	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	// 2. 确定更新后的最终值（用于首要责任人冲突检查）
	finalProjectID := current.ProjectID
	finalScopeType := current.ScopeType
	finalScopeValue := current.ScopeValue
	finalPriority := current.Priority
	if v, ok := updates["priority"].(int); ok {
		finalPriority = v
	}
	if v, ok := updates["scope_type"].(string); ok {
		finalScopeType = v
	}
	if v, ok := updates["scope_value"].(string); ok {
		finalScopeValue = v
	}

	// 3. 检查首要责任人唯一性
	if err := checkPrimaryStewardConflict(scope, finalProjectID, finalScopeType, finalScopeValue, finalPriority, rid); err != nil {
		return err
	}

	return db.Model(&model.ProjectResponsibility{}).Where("id = ?", rid).Updates(updates).Error
}

// DeleteResponsibility 删除职责
func (s *TeamMemberService) DeleteResponsibility(scope *model.UserAuthScope, rid uint) error {
	db := model.DBWithScope(scope)
	return db.Delete(&model.ProjectResponsibility{}, rid).Error
}

// FindStewards 查找项目指定维度的负责人（category → language → default）
// 用于升级通知和服务层查找，返回的 view 已包含 im_user_id
// ⚠️ 每个 scope 查询必须使用独立的 db chain，不能复用同一个 *gorm.DB 变量，
//    否则 GORM 的 Where 条件会叠加。
// ⚠️ 返回结果已过滤掉 users.enabled=false 的记录，确保已禁用人员不会被分配 Issue。
func (s *TeamMemberService) FindStewards(scope *model.UserAuthScope, projectID uint, category, language string) ([]model.ProjectResponsibility, error) {
	// 1. 按 category 匹配
	if category != "" {
		var responsibilities []model.ProjectResponsibility
		db := model.DBWithScope(scope)
		if err := db.Preload("User").Where("project_id = ? AND scope_type = 'rule_category' AND scope_value = ?", projectID, category).
			Order("priority ASC, created_at ASC").
			Find(&responsibilities).Error; err != nil {
			return nil, err
		}
		if active := filterActiveResponsibilities(responsibilities); len(active) > 0 {
			return active, nil
		}
	}

	// 2. 按 language 匹配
	if language != "" {
		var responsibilities []model.ProjectResponsibility
		db := model.DBWithScope(scope)
		normalizedLang := normalizeLanguage(language)
		if err := db.Preload("User").Where("project_id = ? AND scope_type = 'language' AND scope_value = ?", projectID, normalizedLang).
			Order("priority ASC, created_at ASC").
			Find(&responsibilities).Error; err != nil {
			return nil, err
		}
		if active := filterActiveResponsibilities(responsibilities); len(active) > 0 {
			return active, nil
		}
	}

	// 3. fallback 到 default
	var responsibilities []model.ProjectResponsibility
	db := model.DBWithScope(scope)
	if err := db.Preload("User").Where("project_id = ? AND scope_type = 'default' AND scope_value = ''", projectID).
		Order("priority ASC, created_at ASC").
		Find(&responsibilities).Error; err != nil {
		return nil, err
	}
	return filterActiveResponsibilities(responsibilities), nil
}

// filterActiveResponsibilities 过滤掉关联用户已禁用的职责记录
func filterActiveResponsibilities(list []model.ProjectResponsibility) []model.ProjectResponsibility {
	var active []model.ProjectResponsibility
	for _, r := range list {
		// 若 user_id 有效且 User 关联已加载（ID > 0）且用户被禁用，则过滤掉；
		// 若 User 关联未加载（ID == 0），不排除，避免 Preload 失败导致数据丢失
		if r.UserID > 0 && r.User.ID > 0 && !r.User.Enabled {
			continue
		}
		active = append(active, r)
	}
	return active
}

// normalizeLanguage 标准化语言名称，处理常见别名
func normalizeLanguage(lang string) string {
	aliases := map[string]string{
		"go":         "golang",
		"js":         "javascript",
		"node":       "nodejs",
		"node.js":    "nodejs",
		"ts":         "typescript",
		"py":         "python",
		"rb":         "ruby",
		"sh":         "shell",
		"bash":       "shell",
		"zsh":        "shell",
		"cpp":        "cpp",
		"c++":        "cpp",
		"vue":        "frontend",
		"react":      "frontend",
		"angular":    "frontend",
		"html":       "frontend",
		"css":        "frontend",
		"css3":       "frontend",
		"web":        "frontend",
	}
	if normalized, ok := aliases[lang]; ok {
		return normalized
	}
	return lang
}

// checkPrimaryStewardConflict 检查同一项目同一维度下是否已存在首要责任人（priority <= 1）
// 已禁用的用户的职责记录不参与竞争，确保禁用的首要责任人不会阻塞新的配置。
func checkPrimaryStewardConflict(scope *model.UserAuthScope, projectID uint, scopeType, scopeValue string, priority int, excludeRID uint) error {
	if priority > 1 {
		return nil // 仅首要责任人需要唯一性校验
	}
	db := model.DBWithScope(scope)
	var existing model.ProjectResponsibility
	query := db.Where("project_id = ? AND scope_type = ? AND scope_value = ? AND priority <= 1 AND user_id IN (SELECT id FROM users WHERE enabled = ?)",
		projectID, scopeType, scopeValue, true)
	if excludeRID > 0 {
		query = query.Where("id != ?", excludeRID)
	}
	if err := query.First(&existing).Error; err == nil {
		return fmt.Errorf("该项目该维度已存在首要责任人（用户ID:%d），一个项目同一维度只能有一个首要责任人", existing.UserID)
	}
	return nil
}

// ListResponsibilitiesByUser 列出某用户（user_id）的项目职责
func (s *TeamMemberService) ListResponsibilitiesByUser(scope *model.UserAuthScope, userID uint) ([]model.ProjectResponsibility, error) {
	db := model.DBWithScope(scope)
	var list []model.ProjectResponsibility
	if err := db.Preload("Project").Preload("User").
		Where("user_id = ?", userID).
		Order("project_id, scope_type, priority").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// AddResponsibilityByUser 为用户添加项目职责（基于 user_id）
func (s *TeamMemberService) AddResponsibilityByUser(scope *model.UserAuthScope, userID uint, data map[string]interface{}) (*model.ProjectResponsibility, error) {
	db := model.DBWithScope(scope)
	var user model.User
	if err := db.First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	projectIDFloat, _ := data["project_id"].(float64)
	projectID := uint(projectIDFloat)
	scopeType, _ := data["scope_type"].(string)
	scopeValue, _ := data["scope_value"].(string)
	priorityFloat, _ := data["priority"].(float64)
	priority := int(priorityFloat)

	if projectID == 0 {
		return nil, fmt.Errorf("project_id is required")
	}
	if scopeType == "" {
		scopeType = "default"
	}
	if scopeType != "default" && scopeValue == "" {
		return nil, fmt.Errorf("scope_value is required for non-default scope_type")
	}
	if scopeType == "default" {
		scopeValue = ""
	}
	if scopeType == "language" && scopeValue != "" {
		scopeValue = normalizeLanguage(scopeValue)
	}

	// 检查是否已存在相同维度的职责（避免唯一索引冲突）
	var existing model.ProjectResponsibility
	if err := db.Where("user_id = ? AND project_id = ? AND scope_type = ? AND scope_value = ?",
		userID, projectID, scopeType, scopeValue).First(&existing).Error; err == nil {
		return nil, fmt.Errorf("该用户在此项目已存在相同类型的职责（当前优先级:%d），如需调整请先删除或编辑现有职责", existing.Priority)
	}

	// 检查首要责任人唯一性（priority <= 1 时，同一项目同一维度只能有一个首要）
	if err := checkPrimaryStewardConflict(scope, projectID, scopeType, scopeValue, priority, 0); err != nil {
		return nil, err
	}

	// 查询项目获取 org_id
	var project model.Project
	if err := model.DB.Select("org_id").First(&project, projectID).Error; err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}

	resp := model.ProjectResponsibility{
		OrgID:          project.OrgID,
		UserID:         userID,
		ProjectID:      projectID,
		ScopeType:      scopeType,
		ScopeValue:     scopeValue,
		Priority:       priority,
		NotifyChannels: "[]",
	}
	if err := db.Create(&resp).Error; err != nil {
		return nil, err
	}

	db.Preload("User").Preload("Project").First(&resp, resp.ID)
	return &resp, nil
}
