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
func GenerateToken(userID uint, username string) string {
	token := generateRandomToken()

	// 保存到数据库
	tokenModel := model.Token{
		UserID:    userID,
		Token:     token,
		Username:  username,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}
	model.DB.Create(&tokenModel)

	return token
}

// ValidateToken 验证 token（从数据库查询）
func ValidateToken(token string) (uint, bool) {
	if token == "" {
		return 0, false
	}

	var tokenModel model.Token
	// 使用当前时间（带系统时区）与数据库比较
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

		// 从 Header / Cookie / Query 获取 token
		token := c.GetHeader("Authorization")
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}
		if token == "" {
			token, _ = c.Cookie("auth_token")
		}
		if c.Query("token") != "" {
			token = c.Query("token")
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

		// 加载完整User对象到Context
		var user model.User
		if err := model.DB.First(&user, userID).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
			c.Abort()
			return
		}

		// 校验用户是否被禁用（enabled=false 的用户不应继续访问）
		if !user.Enabled {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户已被禁用"})
			c.Abort()
			return
		}

		c.Set("user_id", userID)
		c.Set("user", user)
		c.Set("role", user.Role)
		c.Set("gitlab_username", user.GitlabUsername)
		c.Set("token", token)
		c.Next()
	}
}

// AdminOnly 仅管理员可访问
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || role != model.RoleAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足，仅管理员可访问"})
			c.Abort()
			return
		}
		c.Next()
	}
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
