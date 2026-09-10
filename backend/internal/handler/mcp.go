package handler

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/middleware"
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
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var keys []model.MCPAPIKey
	query := model.DB.Order("created_at DESC")
	if !scope.IsSuperAdmin {
		query = query.Scopes(model.OrgScope(scope))
	}

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

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(keys))
	for _, k := range keys {
		orgIDs = append(orgIDs, k.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range keys {
		keys[i].OrgName = orgNameMap[keys[i].OrgID]
	}

	c.JSON(200, gin.H{
		"data":  keys,
		"total": total,
	})
}

// GetKey 获取单个 MCP API Key 详情
func (h *MCPKeyHandler) GetKey(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	var key model.MCPAPIKey
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Scopes(model.OrgScope(scope))
	}
	if err := db.First(&key, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "key not found"})
		return
	}
	c.JSON(200, gin.H{"data": key})
}

// CreateKey 创建新的 MCP API Key
func (h *MCPKeyHandler) CreateKey(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var req struct {
		Name        string   `json:"name" binding:"required"`
		Scopes      []string `json:"scopes" binding:"required"`
		IPWhitelist string   `json:"ip_whitelist"`
		RateLimit   int      `json:"rate_limit"`
		ExpiresAt   *string  `json:"expires_at"`
		UserID      uint     `json:"user_id"` // 绑定用户（Phase 1：新 Key 强烈建议绑定）
		OrgID       uint     `json:"org_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	if err := validateScopes(req.Scopes); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 如果指定了 user_id，校验用户存在且已启用
	if req.UserID > 0 {
		var user model.User
		dbUser := model.DB
		if !scope.IsSuperAdmin {
			dbUser = dbUser.Scopes(model.OrgScope(scope))
		}
		if err := dbUser.First(&user, req.UserID).Error; err != nil {
			c.JSON(400, gin.H{"error": "绑定的用户不存在"})
			return
		}
		if !user.Enabled {
			c.JSON(400, gin.H{"error": "绑定的用户已被禁用"})
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

	orgID := middleware.GetCurrentOrgID(c)
	if req.OrgID > 0 && scope.IsSuperAdmin {
		orgID = req.OrgID
	}

	key := model.MCPAPIKey{
		OrgID:       orgID, // 多租户改造：注入当前 org_id
		Name:        req.Name,
		Prefix:      keyPrefix,
		KeyHash:     string(hash),
		Scopes:      string(scopesJSON),
		IPWhitelist: req.IPWhitelist,
		RateLimit:   req.RateLimit,
		Status:      "active",
		UserID:      req.UserID,
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
			"user_id":    key.UserID,
			"full_key":   fullKey, // 仅创建时返回一次
			"status":     key.Status,
			"expires_at": key.ExpiresAt,
		},
	})
}

// UpdateKey 更新 MCP API Key
func (h *MCPKeyHandler) UpdateKey(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))

	var req struct {
		Name        string   `json:"name"`
		Scopes      []string `json:"scopes"`
		IPWhitelist string   `json:"ip_whitelist"`
		RateLimit   int      `json:"rate_limit"`
		Status      string   `json:"status"`
		ExpiresAt   *string  `json:"expires_at"`
		UserID      *uint    `json:"user_id"` // nil 表示不修改，0 表示解除绑定
		// 多租户改造：super_admin 可修改所属组织
		OrgID uint `json:"org_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	var key model.MCPAPIKey
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := db.First(&key, id).Error; err != nil {
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
	if req.UserID != nil {
		if *req.UserID > 0 {
			var user model.User
			dbUser := model.DB
			if !scope.IsSuperAdmin {
				dbUser = dbUser.Scopes(model.OrgScope(scope))
			}
			if err := dbUser.First(&user, *req.UserID).Error; err != nil {
				c.JSON(400, gin.H{"error": "绑定的用户不存在"})
				return
			}
			if !user.Enabled {
				c.JSON(400, gin.H{"error": "绑定的用户已被禁用"})
				return
			}
		}
		key.UserID = *req.UserID
	}

	// 多租户改造：super_admin 可修改所属组织
	if req.OrgID > 0 && scope.IsSuperAdmin {
		key.OrgID = req.OrgID
	}

	// 多租户改造：避免 Save 零值覆盖 org_id
	// 已由上方权限控制保证非 super_admin 不会修改 org_id
	if err := model.DB.Save(&key).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "updated"})
}

// DeleteKey 删除 MCP API Key
func (h *MCPKeyHandler) DeleteKey(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	db := model.DB
	if !scope.IsSuperAdmin {
		db = db.Where("org_id IN ?", scope.VisibleOrgIDs)
	}
	if err := db.Delete(&model.MCPAPIKey{}, id).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "deleted"})
}

// ListLogs 列出 MCP 调用日志
func (h *MCPKeyHandler) ListLogs(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	query := model.DB.Order("created_at DESC")
	if !scope.IsSuperAdmin {
		// 多租户改造：只查询当前组织下的 MCP Key 的日志
		query = query.Where("org_id IN ?", scope.VisibleOrgIDs)
	}

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

	// MCPCallLog 没有 OrgID 字段，跳过组织名称填充

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
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

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
