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
	token := middleware.GenerateToken(user.ID, user.Username)

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

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"id":              user.ID,
			"username":        user.Username,
			"display_name":    user.DisplayName,
			"role":            user.Role,
			"login_type":      user.LoginType,
			"gitlab_username": user.GitlabUsername,
			"gitlab_email":    user.GitlabEmail,
			"im_platform":     user.IMPlatform,
			"im_user_id":      user.IMUserID,
			"enabled":         user.Enabled,
			"avatar_url":      user.AvatarURL,
		},
	})
}

// ListUsers 用户列表（管理员）
// GET /api/v1/users
func (h *UserHandler) ListUsers(c *gin.Context) {
	keyword := c.Query("keyword")
	role := c.Query("role")
	loginType := c.Query("login_type")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}

	users, total, err := h.service.ListUsers(keyword, role, loginType, page, pageSize)
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

	// 不返回密码字段
	list := make([]gin.H, 0, len(users))
	for _, u := range users {
		list = append(list, gin.H{
			"id":                   u.ID,
			"username":             u.Username,
			"display_name":         u.DisplayName,
			"role":                 u.Role,
			"login_type":           u.LoginType,
			"gitlab_username":      u.GitlabUsername,
			"gitlab_email":         u.GitlabEmail,
			"im_platform":          u.IMPlatform,
			"im_user_id":           u.IMUserID,
			"enabled":              u.Enabled,
			"responsibility_count": respCounts[u.ID],
			"avatar_url":           u.AvatarURL,
			"created_at":           u.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": list, "total": total, "page": page, "page_size": pageSize})
}

// CreateUser 创建用户（管理员）
// POST /api/v1/users
func (h *UserHandler) CreateUser(c *gin.Context) {
	var req struct {
		Username    string `json:"username" binding:"required"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password" binding:"required,min=6"`
		Role        string `json:"role" binding:"required,oneof=admin user"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供用户名、密码（至少6位）和角色(admin/user)"})
		return
	}

	user, err := h.service.CreateUser(req.Username, req.DisplayName, req.Password, req.Role)
	if err != nil {
		zap.L().Error("create user failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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
			"role":         user.Role,
		},
	})
}

// UpdateUser 更新用户信息（管理员）
// PUT /api/v1/users/:id
func (h *UserHandler) UpdateUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		DisplayName string `json:"display_name"`
		Role        string `json:"role" binding:"omitempty,oneof=admin user"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.service.UpdateUser(uint(id), req.DisplayName, req.Role); err != nil {
		zap.L().Error("update user failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	currentUserID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	model.RecordOpLog("用户更新", "用户ID:"+c.Param("id"), uint(id), currentUserID.(uint), "success", "", c.ClientIP())
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

	list, err := service.NewTeamMemberService().ListResponsibilitiesByUser(user.ID)
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

	resp, err := service.NewTeamMemberService().AddResponsibilityByUser(user.ID, data)
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

	if err := service.NewTeamMemberService().UpdateResponsibility(uint(rid), data); err != nil {
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

	if err := service.NewTeamMemberService().DeleteResponsibility(uint(rid)); err != nil {
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
