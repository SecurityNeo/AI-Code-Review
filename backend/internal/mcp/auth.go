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
	HeaderAPIKey    = "X-API-Key"
	IMPlatformWeCom = "wecom"
)

// AuthContext MCP 认证后的上下文信息
type AuthContext struct {
	APIKey     *model.MCPAPIKey
	User       *model.User // 统一身份源（替换原 TeamMember）
	UserID     uint        // 对应 users 表 ID
	IMUserID   string
	IMProvider string
	Scopes     []string
	IsAdmin    bool
	ClientIP   string
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

// respondError 返回 MCP JSON-RPC 格式错误（AuthMiddleware 专用，兼容旧客户端）
func respondError(w http.ResponseWriter, httpCode int, errCode, message string) {
	// 映射内部错误码到 MCP JSON-RPC 标准/自定义错误码
	mcpCode := ErrInvalidRequest
	switch errCode {
	case "UNAUTHORIZED":
		mcpCode = ErrUnauthorized
	case "FORBIDDEN":
		mcpCode = ErrUnauthorized // IP 白名单拒绝视为认证失败（无更合适的标准 code）
	case "INTERNAL_ERROR":
		mcpCode = ErrInternalError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(MCPResponse{
		JSONRPC: "2.0",
		ID:      nil,
		Error: &MCPError{
			Code:    mcpCode,
			Message: message,
		},
	})
}
