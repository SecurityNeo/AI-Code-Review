package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type UserHandler struct {
	service *service.UserService
}

func NewUserHandler() *UserHandler {
	return &UserHandler{
		service: service.NewUserService(),
	}
}

// Login 用户登录
// POST /api/v1/login
func (h *UserHandler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名和密码不能为空"})
		return
	}

	user, ok := h.service.ValidateLogin(req.Username, req.Password)
	if !ok {
		model.RecordOpLog("用户登录", req.Username, 0, uint(0), "failed", "用户名或密码错误", c.ClientIP())
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	// 生成 token
	token, err := middleware.GenerateToken(user.ID, user.Username)
	if err != nil {
		zap.L().Error("generate token failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成Token失败"})
		return
	}

	model.RecordOpLog("用户登录", user.Username, user.ID, user.ID, "success", "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"message": "登录成功",
		"data": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"role":     user.Role,
			"token":    token,
		},
	})
}

// Logout 用户登出
// POST /api/v1/logout
func (h *UserHandler) Logout(c *gin.Context) {
	token, exists := c.Get("token")
	if exists {
		middleware.DeleteToken(token.(string))
	}
	c.JSON(http.StatusOK, gin.H{"message": "登出成功"})
}

// ChangePassword 修改密码
// PUT /api/v1/users/password
func (h *UserHandler) ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=6"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供旧密码和新密码（新密码至少6位）"})
		return
	}

	// 从上下文中获取当前用户ID（由中间件设置）
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	if err := h.service.ChangePassword(userID.(uint), req.OldPassword, req.NewPassword); err != nil {
		zap.L().Error("change password failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	model.RecordOpLog("修改密码", "用户管理", userID.(uint), userID.(uint), "success", "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "密码修改成功"})
}

// GetCurrentUser 获取当前用户信息
// GET /api/v1/users/me
func (h *UserHandler) GetCurrentUser(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	user, err := h.service.GetByID(userID.(uint))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	// 查询当前组织名称
	var currentOrgName string
	if scope.CurrentOrgID > 0 {
		var org model.Organization
		if err := model.DB.Select("name").First(&org, scope.CurrentOrgID).Error; err == nil {
			currentOrgName = org.Name
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"id":               user.ID,
			"username":         user.Username,
			"display_name":     user.DisplayName,
			"current_org_id":   scope.CurrentOrgID,
			"current_org_role": scope.CurrentOrgRole,
			"current_org_name": currentOrgName,
			"is_super_admin":   scope.IsSuperAdmin,
			"default_org_id":   user.DefaultOrgID,
			"login_type":       user.LoginType,
			"gitlab_username":  user.GitlabUsername,
			"gitlab_email":     user.GitlabEmail,
			"im_platform":      user.IMPlatform,
			"im_user_id":       user.IMUserID,
			"enabled":          user.Enabled,
			"avatar_url":       user.AvatarURL,
		},
	})
}

// ListUsers 用户列表（管理员）
// GET /api/v1/users
// 不再返回已废弃的 users.role 字段
func (h *UserHandler) ListUsers(c *gin.Context) {
	keyword := c.Query("keyword")
	loginType := c.Query("login_type")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}

	users, total, err := h.service.ListUsers(keyword, loginType, page, pageSize)
	if err != nil {
		zap.L().Error("list users failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 批量查询职责数量
	respCounts := make(map[uint]int64)
	if len(users) > 0 {
		userIDs := make([]uint, len(users))
		for i, u := range users {
			userIDs[i] = u.ID
		}
		type result struct {
			UserID uint  `gorm:"column:user_id"`
			Count  int64 `gorm:"column:count"`
		}
		var results []result
		model.DB.Model(&model.ProjectResponsibility{}).
			Select("user_id, COUNT(*) as count").
			Where("user_id IN ?", userIDs).
			Group("user_id").
			Scan(&results)
		for _, r := range results {
			respCounts[r.UserID] = r.Count
		}
	}

	// 批量查询组织名称
	orgNames := make(map[uint]string)
	if len(users) > 0 {
		orgIDs := make([]uint, 0, len(users))
		for _, u := range users {
			if u.DefaultOrgID > 0 {
				orgIDs = append(orgIDs, u.DefaultOrgID)
			}
		}
		if len(orgIDs) > 0 {
			var orgs []model.Organization
			model.DB.Select("id, name").Where("id IN ?", orgIDs).Find(&orgs)
			for _, o := range orgs {
				orgNames[o.ID] = o.Name
			}
		}
	}

	// 批量查询 org_users 中的当前组织角色（按 default_org_id）
	orgRoles := make(map[uint]string)
	if len(users) > 0 {
		userIDs := make([]uint, len(users))
		for i, u := range users {
			userIDs[i] = u.ID
		}
		type OrgUserResult struct {
			UserID uint   `gorm:"column:user_id"`
			OrgID  uint   `gorm:"column:org_id"`
			Role   string `gorm:"column:role"`
		}
		var ouResults []OrgUserResult
		model.DB.Table("org_users").
			Select("user_id, org_id, role").
			Where("user_id IN ? AND status = ?", userIDs, "active").
			Scan(&ouResults)
		for _, r := range ouResults {
			// 若用户有多个 active 绑定，以 default_org_id 对应的为准；否则取第一个
			if _, ok := orgRoles[r.UserID]; !ok {
				orgRoles[r.UserID] = r.Role
			}
		}
	}

	// 不返回密码字段
	list := make([]gin.H, 0, len(users))
	for _, u := range users {
		list = append(list, gin.H{
			"id":                   u.ID,
			"username":             u.Username,
			"display_name":         u.DisplayName,
			"login_type":           u.LoginType,
			"gitlab_username":      u.GitlabUsername,
			"gitlab_email":         u.GitlabEmail,
			"im_platform":          u.IMPlatform,
			"im_user_id":           u.IMUserID,
			"enabled":              u.Enabled,
			"responsibility_count": respCounts[u.ID],
			"avatar_url":           u.AvatarURL,
			"created_at":           u.CreatedAt,
			"org_id":               u.DefaultOrgID,
			"org_name":             orgNames[u.DefaultOrgID],
			"current_org_role":     orgRoles[u.ID],
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": list, "total": total, "page": page, "page_size": pageSize})
}

// CreateUser 创建用户（管理员）
// POST /api/v1/users
// 已废弃的 users.role 不再由前端指定，新用户默认绑定组织角色为 developer
func (h *UserHandler) CreateUser(c *gin.Context) {
	var req struct {
		Username    string `json:"username" binding:"required"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password" binding:"required,min=6"`
		IMPlatform  string `json:"im_platform"`
		IMUserID    string `json:"im_user_id"`
		OrgID       uint   `json:"org_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供用户名、密码（至少6位）"})
		return
	}

	user, err := h.service.CreateUser(req.Username, req.DisplayName, req.Password, req.IMPlatform, req.IMUserID)
	if err != nil {
		zap.L().Error("create user failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 设置默认组织并创建 org_users 关联（developer 角色）
	orgID := req.OrgID
	if orgID == 0 {
		orgID = middleware.GetCurrentOrgID(c)
	}
	if orgID > 0 {
		// 校验组织是否存在（防止 super_admin 传入不存在的 org_id）
		var org model.Organization
		if err := model.DB.First(&org, orgID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "指定的组织不存在"})
			return
		}
		if err := model.DB.Model(user).Update("default_org_id", orgID).Error; err != nil {
			zap.L().Warn("set user default_org_id failed", zap.Uint("user_id", user.ID), zap.Uint("org_id", orgID), zap.Error(err))
		}
		ou := model.OrgUser{
			OrgID:     orgID,
			UserID:    user.ID,
			Role:      "developer",
			Status:    "active",
			IsDefault: true,
		}
		if err := model.DB.Create(&ou).Error; err != nil {
			zap.L().Warn("create org_users record failed", zap.Uint("user_id", user.ID), zap.Uint("org_id", orgID), zap.Error(err))
		}
	}

	currentUserID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	model.RecordOpLog("用户创建", user.Username, user.ID, currentUserID.(uint), "success", "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"message": "用户创建成功",
		"data": gin.H{
			"id":           user.ID,
			"username":     user.Username,
			"display_name": user.DisplayName,
		},
	})
}

// UpdateUser 更新用户信息（管理员）
// PUT /api/v1/users/:id
// 已废弃的 users.role 不再可更新
func (h *UserHandler) UpdateUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		DisplayName *string `json:"display_name"`
		IMPlatform  *string `json:"im_platform"`
		IMUserID    *string `json:"im_user_id"`
		OrgID       uint    `json:"org_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.service.UpdateUser(uint(id), req.DisplayName, req.IMPlatform, req.IMUserID, 0); err != nil {
		zap.L().Error("update user failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 可选：迁移默认组织
	if req.OrgID > 0 {
		// 检查当前用户的 org_users 是否存在目标组织绑定
		var existingOU model.OrgUser
		if err := model.DB.Where("org_id = ? AND user_id = ?", req.OrgID, id).First(&existingOU).Error; err == nil {
			// 已存在绑定：更新 default_org_id + is_default
			model.DB.Model(&model.User{}).Where("id = ?", id).Update("default_org_id", req.OrgID)
			model.DB.Model(&model.OrgUser{}).Where("user_id = ? AND org_id != ?", id, req.OrgID).Update("is_default", false)
			model.DB.Model(&model.OrgUser{}).Where("user_id = ? AND org_id = ?", id, req.OrgID).Update("is_default", true)
		} else {
			// 不存在绑定：新增绑定并设为默认
			model.DB.Model(&model.User{}).Where("id = ?", id).Update("default_org_id", req.OrgID)
			ou := model.OrgUser{
				OrgID:     req.OrgID,
				UserID:    uint(id),
				Role:      "developer",
				Status:    "active",
				IsDefault: true,
			}
			model.DB.Create(&ou)
			model.DB.Model(&model.OrgUser{}).Where("user_id = ? AND org_id != ?", id, req.OrgID).Update("is_default", false)
		}
	}

	currentUserID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	model.RecordOpLog("用户更新", "", uint(id), currentUserID.(uint), "success", "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "用户更新成功"})
}

// DeleteUser 删除用户（管理员）
// DELETE /api/v1/users/:id
func (h *UserHandler) DeleteUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	currentUserID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	if err := h.service.DeleteUser(uint(id), currentUserID.(uint)); err != nil {
		zap.L().Error("delete user failed", zap.Error(err))
		model.RecordOpLog("用户删除", "用户ID:"+c.Param("id"), uint(id), currentUserID.(uint), "failed", err.Error(), c.ClientIP())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	model.RecordOpLog("用户删除", "用户ID:"+c.Param("id"), uint(id), currentUserID.(uint), "success", "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "用户已删除"})
}

// ResetPassword 重置用户密码（管理员）
// POST /api/v1/users/:id/reset-password
func (h *UserHandler) ResetPassword(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	currentUserID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	var req struct {
		NewPassword string `json:"new_password" binding:"required,min=6"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码不能为空且至少6位"})
		return
	}

	if err := h.service.ResetPassword(uint(id), req.NewPassword); err != nil {
		zap.L().Error("reset password failed", zap.Error(err))
		model.RecordOpLog("重置密码", "用户ID:"+c.Param("id"), uint(id), currentUserID.(uint), "failed", err.Error(), c.ClientIP())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	model.RecordOpLog("重置密码", "用户ID:"+c.Param("id"), uint(id), currentUserID.(uint), "success", "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "密码已重置"})
}

// GetUserResponsibilities 获取用户的项目职责列表（基于 user_id）。
// GET /api/v1/users/:id/responsibilities
func (h *UserHandler) GetUserResponsibilities(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户ID"})
		return
	}

	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	list, err := service.NewTeamMemberService().ListResponsibilitiesByUser(nil, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": responsibilitiesToViews(list)})
}

// AddUserResponsibility 为用户添加项目职责（基于 user_id）。
// POST /api/v1/users/:id/responsibilities
func (h *UserHandler) AddUserResponsibility(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户ID"})
		return
	}

	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：校验项目是否属于被分配用户的默认组织
	if projectIDFloat, ok := data["project_id"].(float64); ok && projectIDFloat > 0 {
		var project model.Project
		if err := model.DB.Select("org_id").First(&project, uint(projectIDFloat)).Error; err == nil {
			// 检查用户是否有该组织的 active 绑定（或 default_org_id 匹配）
			if user.DefaultOrgID != project.OrgID {
				var ouCount int64
				model.DB.Model(&model.OrgUser{}).Where("user_id = ? AND org_id = ? AND status = ?", user.ID, project.OrgID, "active").Count(&ouCount)
				if ouCount == 0 {
					c.JSON(http.StatusForbidden, gin.H{"error": "该项目不属于用户所在组织，无法分配职责"})
					return
				}
			}
		}
	}

	resp, err := service.NewTeamMemberService().AddResponsibilityByUser(nil, user.ID, data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	currentUserID, exists := c.Get("user_id")
	if exists {
		model.RecordOpLog("添加用户职责",
			fmt.Sprintf("用户ID:%d 职责ID:%d", userID, resp.ID),
			uint(userID), currentUserID.(uint), "success", "", c.ClientIP())
	}
	c.JSON(http.StatusOK, gin.H{"data": toResponsibilityView(*resp)})
}

// UpdateUserResponsibility 更新用户的项目职责（基于 responsibility id）。
// PUT /api/v1/users/responsibilities/:rid
func (h *UserHandler) UpdateUserResponsibility(c *gin.Context) {
	rid, err := strconv.Atoi(c.Param("rid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的职责ID"})
		return
	}

	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := service.NewTeamMemberService().UpdateResponsibility(nil, uint(rid), data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 查询项目名称用于返回提示
	var resp model.ProjectResponsibility
	model.DB.Preload("Project").First(&resp, uint(rid))
	projectName := ""
	if resp.Project.ID > 0 {
		projectName = resp.Project.Name
	}

	currentUserID, exists := c.Get("user_id")
	if exists {
		model.RecordOpLog("更新用户职责", fmt.Sprintf("职责ID:%d", rid), 0, currentUserID.(uint), "success", "", c.ClientIP())
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("项目%s责任人已更新", projectName)})
}

// DeleteUserResponsibility 删除用户的项目职责（基于 user_id）。
// DELETE /api/v1/users/responsibilities/:rid
func (h *UserHandler) DeleteUserResponsibility(c *gin.Context) {
	rid, err := strconv.Atoi(c.Param("rid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的职责ID"})
		return
	}

	if err := service.NewTeamMemberService().DeleteResponsibility(nil, uint(rid)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	currentUserID, exists := c.Get("user_id")
	if exists {
		model.RecordOpLog("删除用户职责", fmt.Sprintf("职责ID:%d", rid), 0, currentUserID.(uint), "success", "", c.ClientIP())
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func responsibilitiesToViews(list []model.ProjectResponsibility) []ResponsibilityView {
	views := make([]ResponsibilityView, len(list))
	for i, r := range list {
		views[i] = toResponsibilityView(r)
	}
	return views
}
