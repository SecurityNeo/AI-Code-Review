package handler

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type MCPKeyHandler struct{}

func NewMCPKeyHandler() *MCPKeyHandler {
	return &MCPKeyHandler{}
}

// ListKeys 列出所有 MCP API Key
func (h *MCPKeyHandler) ListKeys(c *gin.Context) {
	var keys []model.MCPAPIKey
	query := model.DB.Order("created_at DESC")

	// 搜索
	if keyword := c.Query("keyword"); keyword != "" {
		query = query.Where("name LIKE ?", "%"+keyword+"%")
	}

	// 分页
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	query.Model(&model.MCPAPIKey{}).Count(&total)

	offset := (page - 1) * pageSize
	if err := query.Limit(pageSize).Offset(offset).Find(&keys).Error; err != nil {
		zap.L().Error("list mcp keys failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"data":  keys,
		"total": total,
	})
}

// CreateKey 创建新的 MCP API Key
func (h *MCPKeyHandler) CreateKey(c *gin.Context) {
	var req struct {
		Name        string   `json:"name" binding:"required"`
		Scopes      []string `json:"scopes" binding:"required"`
		IPWhitelist string   `json:"ip_whitelist"`
		RateLimit   int      `json:"rate_limit"`
		ExpiresAt   *string  `json:"expires_at"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 验证 scopes
	validScopes := map[string]bool{
		"tasks:read": true, "tasks:write": true,
		"merge_requests:read": true,
		"workbench:read": true,
		"dashboard:read": true,
		"projects:read": true,
		"notifications:read": true, "notifications:write": true,
		"team:read": true,
		"admin:*": true,
	}
	for _, s := range req.Scopes {
		if !validScopes[s] {
			c.JSON(400, gin.H{"error": "invalid scope: " + s})
			return
		}
	}

	// 生成密钥
	keyPrefix := "sk-cg-" + generateRandomString(8)
	keySecret := generateRandomString(32)
	fullKey := keyPrefix + "-" + keySecret

	// 哈希密钥（只存哈希）
	hash, err := bcrypt.GenerateFromPassword([]byte(fullKey), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to hash key"})
		return
	}

	scopesJSON, _ := json.Marshal(req.Scopes)

	if req.RateLimit <= 0 {
		req.RateLimit = 100
	}

	key := model.MCPAPIKey{
		Name:        req.Name,
		Prefix:      keyPrefix,
		KeyHash:     string(hash),
		Scopes:      string(scopesJSON),
		IPWhitelist: req.IPWhitelist,
		RateLimit:   req.RateLimit,
		Status:      "active",
	}

	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse("2006-01-02", *req.ExpiresAt)
		if err == nil {
			key.ExpiresAt = &t
		}
	}

	if err := model.DB.Create(&key).Error; err != nil {
		zap.L().Error("create mcp key failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"data": gin.H{
			"id":       key.ID,
			"name":     key.Name,
			"prefix":   key.Prefix,
			"scopes":   req.Scopes,
			"full_key": fullKey, // 仅创建时返回一次
			"status":   key.Status,
			"expires_at": key.ExpiresAt,
		},
	})
}

// UpdateKey 更新 MCP API Key
func (h *MCPKeyHandler) UpdateKey(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var req struct {
		Name        string   `json:"name"`
		Scopes      []string `json:"scopes"`
		IPWhitelist string   `json:"ip_whitelist"`
		RateLimit   int      `json:"rate_limit"`
		Status      string   `json:"status"`
		ExpiresAt   *string  `json:"expires_at"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	var key model.MCPAPIKey
	if err := model.DB.First(&key, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "key not found"})
		return
	}

	if req.Name != "" {
		key.Name = req.Name
	}
	if len(req.Scopes) > 0 {
		scopesJSON, _ := json.Marshal(req.Scopes)
		key.Scopes = string(scopesJSON)
	}
	if req.IPWhitelist != "" {
		key.IPWhitelist = req.IPWhitelist
	}
	if req.RateLimit > 0 {
		key.RateLimit = req.RateLimit
	}
	if req.Status != "" {
		key.Status = req.Status
	}
	if req.ExpiresAt != nil {
		if *req.ExpiresAt == "" {
			key.ExpiresAt = nil
		} else {
			t, err := time.Parse("2006-01-02", *req.ExpiresAt)
			if err == nil {
				key.ExpiresAt = &t
			}
		}
	}

	if err := model.DB.Save(&key).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "updated"})
}

// DeleteKey 删除 MCP API Key
func (h *MCPKeyHandler) DeleteKey(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := model.DB.Delete(&model.MCPAPIKey{}, id).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "deleted"})
}

// ListLogs 列出 MCP 调用日志
func (h *MCPKeyHandler) ListLogs(c *gin.Context) {
	query := model.DB.Order("created_at DESC")

	// 过滤
	if apiKeyID := c.Query("api_key_id"); apiKeyID != "" {
		query = query.Where("api_key_id = ?", apiKeyID)
	}
	if toolName := c.Query("tool_name"); toolName != "" {
		query = query.Where("tool_name = ?", toolName)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if imUserID := c.Query("im_user_id"); imUserID != "" {
		query = query.Where("im_user_id = ?", imUserID)
	}
	if start := c.Query("start_time"); start != "" {
		query = query.Where("created_at >= ?", start)
	}
	if end := c.Query("end_time"); end != "" {
		query = query.Where("created_at <= ?", end)
	}

	// 分页
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	query.Model(&model.MCPCallLog{}).Count(&total)

	offset := (page - 1) * pageSize
	var logs []model.MCPCallLog
	if err := query.Limit(pageSize).Offset(offset).Find(&logs).Error; err != nil {
		zap.L().Error("list mcp logs failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"data":  logs,
		"total": total,
	})
}

// generateRandomString 生成随机字符串
func generateRandomString(length int) string {
	b := make([]byte, length/2+length%2)
	if _, err := crand.Read(b); err != nil {
		return strings.Repeat("x", length)
	}
	return hex.EncodeToString(b)[:length]
}

// ========== MCP 诊断工具 ==========

// mcpServerInstance 全局 MCP Server 实例（由 main.go 注入）
var mcpServerInstance interface {
	ToolCount() int
	ToolNames() []string
}

// SetMCPServer 设置全局 MCP Server 实例
func SetMCPServer(s interface {
	ToolCount() int
	ToolNames() []string
}) {
	mcpServerInstance = s
}

// Diagnose MCP 连接诊断
// POST /api/v1/mcp/diagnose
func (h *MCPKeyHandler) Diagnose(c *gin.Context) {
	userIDValue, exists := c.Get("user_id")
	if !exists {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	userID := userIDValue.(uint)

	var req struct {
		APIKeyID   string `json:"api_key_id,omitempty"`
		ClientHost string `json:"client_host,omitempty"`
	}
	_ = c.ShouldBindJSON(&req)

	startedAt := time.Now()
	steps := make([]gin.H, 0, 5)

	// Step 1: API Key 有效性
	step1 := gin.H{"step": 1, "name": "api_key_validity", "display_name": "API Key 有效性"}
	var key model.MCPAPIKey
	if req.APIKeyID != "" {
		model.DB.Where("id = ?", req.APIKeyID).First(&key)
	} else {
		model.DB.Where("status = ?", "active").Order("created_at DESC").First(&key)
	}
	if key.ID == 0 {
		step1["status"] = "failed"
		step1["message"] = "未找到有效的 API Key"
		step1["suggestion"] = "请前往「MCP 密钥管理」创建一个 API Key"
	} else if key.Status != "active" {
		step1["status"] = "failed"
		step1["message"] = "API Key 已被禁用"
		step1["detail"] = gin.H{"key_id": key.ID, "name": key.Name, "status": key.Status}
		step1["suggestion"] = "请启用该 Key 或创建新 Key"
	} else if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
		step1["status"] = "failed"
		step1["message"] = "API Key 已过期"
		step1["detail"] = gin.H{"key_id": key.ID, "name": key.Name, "expires_at": key.ExpiresAt}
		step1["suggestion"] = "请创建新的 API Key"
	} else {
		step1["status"] = "passed"
		step1["message"] = "Key 有效"
		step1["detail"] = gin.H{"key_id": key.ID, "name": key.Name, "prefix": key.Prefix}
	}
	steps = append(steps, step1)

	// Step 2: IM 账号绑定
	step2 := gin.H{"step": 2, "name": "im_binding", "display_name": "IM 账号绑定"}
	var user model.User
	model.DB.First(&user, userID)
	if user.IMUserID == "" {
		step2["status"] = "warning"
		step2["message"] = "未绑定 IM 账号"
		step2["suggestion"] = "前往「个人设置」绑定企业微信/钉钉/飞书账号"
	} else {
		step2["status"] = "passed"
		step2["message"] = "已绑定 " + user.IMPlatform
		step2["detail"] = gin.H{"im_platform": user.IMPlatform, "im_user_id": user.IMUserID}
	}
	steps = append(steps, step2)

	// Step 3: 服务端可达性
	step3 := gin.H{"step": 3, "name": "server_reachability", "display_name": "服务端可达性"}
	host := req.ClientHost
	if host == "" {
		host = "http://localhost:8080"
	}
	probeURL := host + "/mcp/v1"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Head(probeURL)
	if err != nil {
		step3["status"] = "failed"
		step3["message"] = "服务端不可达: " + err.Error()
		step3["detail"] = gin.H{"url": probeURL}
		step3["suggestion"] = "检查 MCP 服务端是否启动，以及网络/防火墙配置"
	} else {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			step3["status"] = "passed"
			step3["message"] = "服务端响应正常"
			step3["detail"] = gin.H{"url": probeURL, "http_status": resp.StatusCode}
		} else {
			step3["status"] = "warning"
			step3["message"] = "服务端返回非 200 状态码: " + strconv.Itoa(resp.StatusCode)
			step3["detail"] = gin.H{"url": probeURL, "http_status": resp.StatusCode}
		}
	}
	steps = append(steps, step3)

	// Step 4: Tool 列表完整性
	step4 := gin.H{"step": 4, "name": "tool_list_integrity", "display_name": "Tool 列表完整性"}
	if mcpServerInstance == nil {
		step4["status"] = "skipped"
		step4["message"] = "MCP Server 实例未初始化，跳过检查"
	} else {
		count := mcpServerInstance.ToolCount()
		names := mcpServerInstance.ToolNames()
		if count == 0 {
			step4["status"] = "failed"
			step4["message"] = "Tool 列表为空"
			step4["suggestion"] = "MCP Server 初始化异常，建议重启服务"
		} else {
			step4["status"] = "passed"
			step4["message"] = fmt.Sprintf("已注册 %d 个 Tool", count)
			step4["detail"] = gin.H{"expected": 15, "actual": count, "tools": names}
		}
	}
	steps = append(steps, step4)

	// Step 5: 数据库连通性测试
	step5 := gin.H{"step": 5, "name": "db_connectivity", "display_name": "数据库连通性"}
	var taskCount int64
	if err := model.DB.Model(&model.Task{}).Limit(1).Count(&taskCount).Error; err != nil {
		step5["status"] = "failed"
		step5["message"] = "数据库查询失败: " + err.Error()
		step5["suggestion"] = "检查数据库连接状态和服务可用性"
	} else {
		step5["status"] = "passed"
		step5["message"] = "数据库连接正常"
	}
	steps = append(steps, step5)

	// 计算总体状态
	overall := "passed"
	passedCount, failedCount, warningCount := 0, 0, 0
	for _, s := range steps {
		switch s["status"].(string) {
		case "passed":
			passedCount++
		case "failed":
			failedCount++
		case "warning":
			warningCount++
		}
	}
	if failedCount > 0 {
		overall = "failed"
	} else if warningCount > 0 {
		overall = "partial"
	}

	summary := fmt.Sprintf("%d/%d 项检查通过", passedCount, len(steps))
	if failedCount > 0 {
		summary += fmt.Sprintf("，%d 项失败", failedCount)
	}
	if warningCount > 0 {
		summary += fmt.Sprintf("，%d 项警告", warningCount)
	}

	duration := time.Since(startedAt).Milliseconds()
	reportID := fmt.Sprintf("diag-%s-%d", time.Now().Format("20060102-150405"), rand.Intn(10000))

	c.JSON(200, gin.H{
		"overall":     overall,
		"summary":     summary,
		"report_id":   reportID,
		"started_at":  startedAt.Format(time.RFC3339),
		"duration_ms": duration,
		"steps":       steps,
	})
}
