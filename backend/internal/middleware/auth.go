package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
)

// GenerateToken 生成新 token（数据库持久化）
func GenerateToken(userID uint, username string) (string, error) {
	token := generateRandomToken()

	tokenModel := model.Token{
		UserID:    userID,
		Token:     token,
		Username:  username,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}
	if err := model.DB.Create(&tokenModel).Error; err != nil {
		return "", err
	}

	return token, nil
}

// ValidateToken 验证 token（从数据库查询）
func ValidateToken(token string) (uint, bool) {
	if token == "" {
		return 0, false
	}

	var tokenModel model.Token
	if err := model.DB.Where("token = ? AND expires_at > ?", token, time.Now()).First(&tokenModel).Error; err != nil {
		return 0, false
	}

	return tokenModel.UserID, true
}

// DeleteToken 删除 token
func DeleteToken(token string) {
	if token == "" {
		return
	}
	model.DB.Where("token = ?", token).Delete(&model.Token{})
}

// generateRandomToken 生成随机 token
func generateRandomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Auth 认证中间件 - 验证Token并加载完整User对象到Context
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 白名单 - 不需要认证的路径
		whiteList := []string{
			"/api/v1/login",
			"/api/v1/logout",
			"/api/v1/auth/gitlab",
			"/api/v1/auth/gitlab/callback",
			"/api/v1/gitlab-instances/public",
			"/health",
		}

		for _, path := range whiteList {
			if c.Request.URL.Path == path {
				c.Next()
				return
			}
		}

		// 静态文件也不需要认证
		if !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}

		// 从 Header / Cookie 获取 token
		token := c.GetHeader("Authorization")
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}
		if token == "" {
			token, _ = c.Cookie("auth_token")
		}

		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
			c.Abort()
			return
		}

		userID, ok := ValidateToken(token)
		if !ok {
			tokenDebug := token
			if len(tokenDebug) > 8 {
				tokenDebug = tokenDebug[:8] + "..."
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期，请重新登录", "token_debug": tokenDebug, "now": time.Now().Format(time.RFC3339)})
			c.Abort()
			return
		}

		// 加载完整User对象到Context（users.role 已废弃，不再用于权限判定）
		var user model.User
		if err := model.DB.First(&user, userID).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
			c.Abort()
			return
		}

		// 校验用户是否被禁用
		if !user.Enabled {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户已被禁用"})
			c.Abort()
			return
		}

		c.Set("user_id", userID)
		c.Set("user", user)
		c.Set("token", token)

		// 构建用户认证范围（基于 org_users.role，废弃 users.role）
		scope, err := model.BuildAuthScope(user.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "构建用户认证范围失败"})
			c.Abort()
			return
		}

		// 如果用户无组织归属，返回403（super_admin豁免）
		if len(scope.VisibleOrgIDs) == 0 && !scope.IsSuperAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "用户未分配组织，请联系管理员"})
			c.Abort()
			return
		}

		c.Set("auth_scope", scope)
		// 保留 role 字段兼容旧代码，但值为 CurrentOrgRole
		c.Set("role", scope.CurrentOrgRole)
		c.Next()
	}
}

// RequireOrgAdmin 要求组织管理员及以上角色（super_admin / org_admin）
func RequireOrgAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		scope := GetAuthScope(c)
		if scope == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}
		if !scope.HasOrgRole("super_admin", "org_admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足，仅组织管理员可访问"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireSystemAdmin 要求系统管理员角色（super_admin）
func RequireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		scope := GetAuthScope(c)
		if scope == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}
		if scope.CurrentOrgRole != "super_admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足，仅系统管理员可访问"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// AdminOnly 废弃：保留函数签名但内部转发到 RequireOrgAdmin，逐步替换
// Deprecated: 使用 RequireOrgAdmin 或 RequireSystemAdmin 替代
func AdminOnly() gin.HandlerFunc {
	return RequireOrgAdmin()
}

// GetUser 从Context中获取当前登录用户
func GetUser(c *gin.Context) (model.User, bool) {
	v, ok := c.Get("user")
	if !ok {
		return model.User{}, false
	}
	user, ok := v.(model.User)
	return user, ok
}
