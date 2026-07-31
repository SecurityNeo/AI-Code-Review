package handler

import (
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

	// 不返回密码字段
	list := make([]gin.H, 0, len(users))
	for _, u := range users {
		list = append(list, gin.H{
			"id":              u.ID,
			"username":        u.Username,
			"display_name":    u.DisplayName,
			"role":            u.Role,
			"login_type":      u.LoginType,
			"gitlab_username": u.GitlabUsername,
			"gitlab_email":    u.GitlabEmail,
			"avatar_url":      u.AvatarURL,
			"created_at":      u.CreatedAt,
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
