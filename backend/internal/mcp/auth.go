package mcp

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"golang.org/x/crypto/bcrypt"
)

const (
	HeaderAPIKey     = "X-API-Key"
	HeaderIMUserID   = "X-IM-User-ID"
	HeaderIMProvider = "X-IM-Provider"
)

// AuthContext MCP 认证后的上下文信息
type AuthContext struct {
	APIKey       *model.MCPAPIKey
	User         *model.User  // 统一身份源（替换原 TeamMember）
	UserID       uint         // 对应 users 表 ID
	IMUserID     string
	IMProvider   string
	Scopes       []string
	IsAdmin      bool
	ClientIP     string
}

// HasScope 检查是否有指定 Scope
func (ctx *AuthContext) HasScope(scope string) bool {
	for _, s := range ctx.Scopes {
		if s == scope || s == "*" {
			return true
		}
		// admin:* 通配
		if strings.HasSuffix(s, ":*") && strings.HasPrefix(scope, strings.TrimSuffix(s, "*")) {
			return true
		}
	}
	return false
}

// checkAPIKey 验证 API Key（明文 vs bcrypt 哈希）
func checkAPIKey(plainKey, hash string) bool {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plainKey)); err == nil {
		return true
	}
	return false
}

// extractKeyPrefix 从完整 API Key 中提取前缀
// 格式：sk-cg-<prefix>-<secret>，返回 sk-cg-<prefix>
func extractKeyPrefix(fullKey string) string {
	parts := strings.Split(fullKey, "-")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], "-")
	}
	return ""
}

// getClientIP 获取客户端真实 IP
func getClientIP(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	xri := r.Header.Get("X-Real-Ip")
	if xri != "" {
		return xri
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// isIPAllowed 检查 IP 是否在白名单
func isIPAllowed(clientIP, whitelist string) bool {
	allowed := strings.Split(whitelist, ",")
	for _, ip := range allowed {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if ip == clientIP {
			return true
		}
		// CIDR 支持
		if strings.Contains(ip, "/") {
			_, ipNet, err := net.ParseCIDR(ip)
			if err == nil {
				client := net.ParseIP(clientIP)
				if client != nil && ipNet.Contains(client) {
					return true
				}
			}
		}
	}
	return false
}

// respondError 返回 JSON 错误响应
func respondError(w http.ResponseWriter, code int, errCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":      true,
		"error_code": errCode,
		"message":    message,
	})
}
