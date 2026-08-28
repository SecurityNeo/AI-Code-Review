package handler

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

var validMCPScopes = map[string]bool{
	"tasks:read": true, "tasks:write": true,
	"issues:read": true, "issues:write": true,
	"merge_requests:read": true,
	"workbench:read":      true,
	"dashboard:read":      true,
	"projects:read":       true,
	"notifications:read":  true, "notifications:write": true,
	"team:read": true,
	"admin:*":   true,
}

func validateScopes(scopes []string) error {
	for _, s := range scopes {
		if !validMCPScopes[s] {
			return fmt.Errorf("无效的权限范围: %s", s)
		}
	}
	return nil
}

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

// GetKey 获取单个 MCP API Key 详情
func (h *MCPKeyHandler) GetKey(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var key model.MCPAPIKey
	if err := model.DB.First(&key, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "key not found"})
		return
	}
	c.JSON(200, gin.H{"data": key})
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

	if err := validateScopes(req.Scopes); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
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
			"id":         key.ID,
			"name":       key.Name,
			"prefix":     key.Prefix,
			"scopes":     req.Scopes,
			"full_key":   fullKey, // 仅创建时返回一次
			"status":     key.Status,
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
		if err := validateScopes(req.Scopes); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
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

// ListMCPTools 返回 MCP 能力中心工具元数据（支持分页）
func ListMCPTools(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "15"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 15
	}

	var total int64
	model.DB.Model(&model.MCPTool{}).Count(&total)

	var tools []model.MCPTool
	offset := (page - 1) * pageSize
	if err := model.DB.Order("sort_order ASC").Limit(pageSize).Offset(offset).Find(&tools).Error; err != nil {
		zap.L().Error("list mcp tools failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	items := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		items = append(items, map[string]interface{}{
			"tool_name":        t.ToolName,
			"display_name":     t.DisplayName,
			"description":      t.Description,
			"category":         t.Category,
			"icon":             t.Icon,
			"color":            t.Color,
			"tags":             t.Tags,
			"params":           t.Params,
			"phrases":          t.Phrases,
			"requires":         t.Requires,
			"response_example": t.ResponseExample,
			"dangerous":        t.Dangerous,
			"sort_order":       t.SortOrder,
		})
	}

	c.JSON(200, gin.H{
		"data":      items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
