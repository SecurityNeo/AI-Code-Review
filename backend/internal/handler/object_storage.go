package handler

import (
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
)

// ObjectStorageHandler 对象存储配置管理
type ObjectStorageHandler struct{}

func NewObjectStorageHandler() *ObjectStorageHandler {
	return &ObjectStorageHandler{}
}

// ListConfigs 列表
func (h *ObjectStorageHandler) ListConfigs(c *gin.Context) {
	var configs []model.ObjectStorageConfig
	if err := model.DB.Order("id ASC").Find(&configs).Error; err != nil {
		c.JSON(500, gin.H{"error": "查询失败"})
		return
	}
	c.JSON(200, configs)
}

// GetConfig 详情
func (h *ObjectStorageHandler) GetConfig(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cfg model.ObjectStorageConfig
	if err := model.DB.First(&cfg, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "配置不存在"})
		return
	}
	c.JSON(200, cfg)
}

// CreateConfig 创建（含连接测试）
func (h *ObjectStorageHandler) CreateConfig(c *gin.Context) {
	var req storageConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	cfg := model.ObjectStorageConfig{
		Name:      req.Name,
		Enabled:   req.Enabled,
		IsDefault: req.IsDefault,
		Type:      req.Type,
		Region:    req.Region,
		Bucket:    req.Bucket,
		Endpoint:  req.Endpoint,
		Prefix:    req.Prefix,
		UseSSL:    req.UseSSL,
	}

	// 加密凭据
	if req.AccessKey != "" {
		encryptedAK, err := service.EncryptString(req.AccessKey)
		if err != nil {
			c.JSON(500, gin.H{"error": "加密 AccessKey 失败"})
			return
		}
		cfg.AccessKey = encryptedAK
		cfg.AccessKeyMask = service.MaskString(req.AccessKey)
	}
	if req.SecretKey != "" {
		encryptedSK, err := service.EncryptString(req.SecretKey)
		if err != nil {
			c.JSON(500, gin.H{"error": "加密 SecretKey 失败"})
			return
		}
		cfg.SecretKey = encryptedSK
		cfg.SecretKeyMask = service.MaskString(req.SecretKey)
	}
	if req.RetentionDays > 0 {
		cfg.RetentionDays = req.RetentionDays
	}
	if req.MaxObjectSize > 0 {
		cfg.MaxObjectSize = req.MaxObjectSize
	}

	// 连接测试
	if req.TestConnection {
		provider, err := service.BuildStorageFromConfig(cfg)
		if err != nil {
			c.JSON(400, gin.H{"error": "凭据解析失败: " + err.Error()})
			return
		}
		ok, msg := provider.CheckConnection()
		if !ok {
			c.JSON(400, gin.H{"error": "连接测试失败: " + msg})
			return
		}
		cfg.ConnectionStatus = "ok"
		now := time.Now()
		cfg.LastCheckedAt = &now
	}

	// 如果设为默认，取消其他默认
	if cfg.IsDefault {
		model.DB.Model(&model.ObjectStorageConfig{}).Where("is_default = ?", true).Update("is_default", false)
	}

	if err := model.DB.Create(&cfg).Error; err != nil {
		c.JSON(500, gin.H{"error": "创建失败"})
		return
	}

	// 如果设为默认，立即生效
	if cfg.IsDefault {
		_ = service.InitObjectStorageProvider()
	}

	c.JSON(200, cfg)
}

// UpdateConfig 更新
func (h *ObjectStorageHandler) UpdateConfig(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cfg model.ObjectStorageConfig
	if err := model.DB.First(&cfg, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "配置不存在"})
		return
	}

	var req storageConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"name":             req.Name,
		"enabled":          req.Enabled,
		"is_default":       req.IsDefault,
		"type":             req.Type,
		"region":           req.Region,
		"bucket":           req.Bucket,
		"endpoint":         req.Endpoint,
		"prefix":           req.Prefix,
		"use_ssl":          req.UseSSL,
		"retention_days":   req.RetentionDays,
		"max_object_size":  req.MaxObjectSize,
	}

	// 凭据：如果传入新的则更新
	if req.AccessKey != "" {
		encryptedAK, err := service.EncryptString(req.AccessKey)
		if err != nil {
			c.JSON(500, gin.H{"error": "加密失败"})
			return
		}
		updates["access_key"] = encryptedAK
		updates["access_key_mask"] = service.MaskString(req.AccessKey)
	}
	if req.SecretKey != "" {
		encryptedSK, err := service.EncryptString(req.SecretKey)
		if err != nil {
			c.JSON(500, gin.H{"error": "加密失败"})
			return
		}
		updates["secret_key"] = encryptedSK
		updates["secret_key_mask"] = service.MaskString(req.SecretKey)
	}

	// 连接测试
	if req.TestConnection {
		// 构建临时配置用于测试
		testCfg := cfg
		if req.Type != "" {
			testCfg.Type = req.Type
		}
		if req.Region != "" {
			testCfg.Region = req.Region
		}
		if req.Bucket != "" {
			testCfg.Bucket = req.Bucket
		}
		if req.Endpoint != "" {
			testCfg.Endpoint = req.Endpoint
		}
		if req.Prefix != "" {
			testCfg.Prefix = req.Prefix
		}
		testCfg.UseSSL = req.UseSSL
		if req.AccessKey != "" {
			testCfg.AccessKey = updates["access_key"].(string)
		}
		if req.SecretKey != "" {
			testCfg.SecretKey = updates["secret_key"].(string)
		}

		provider, err := service.BuildStorageFromConfig(testCfg)
		if err != nil {
			c.JSON(400, gin.H{"error": "凭据解析失败: " + err.Error()})
			return
		}
		ok, msg := provider.CheckConnection()
		if !ok {
			c.JSON(400, gin.H{"error": "连接测试失败: " + msg})
			return
		}
		updates["connection_status"] = "ok"
		now := time.Now()
		updates["last_checked_at"] = now
		updates["last_error"] = ""
	}

	// 默认配置互斥
	if req.IsDefault {
		model.DB.Model(&model.ObjectStorageConfig{}).Where("is_default = ? AND id != ?", true, id).Update("is_default", false)
	}

	if err := model.DB.Model(&cfg).Updates(updates).Error; err != nil {
		c.JSON(500, gin.H{"error": "更新失败"})
		return
	}

	// 如果更新的是当前默认配置且 enabled 变化，可能需要重新初始化 provider
	if cfg.IsDefault {
		_ = service.InitObjectStorageProvider()
	}

	// 重新查询返回（脱敏）
	model.DB.First(&cfg, id)
	c.JSON(200, cfg)
}

// DeleteConfig 删除
func (h *ObjectStorageHandler) DeleteConfig(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cfg model.ObjectStorageConfig
	wasDefault := false
	if err := model.DB.First(&cfg, id).Error; err == nil {
		wasDefault = cfg.IsDefault
	}
	if err := model.DB.Delete(&model.ObjectStorageConfig{}, id).Error; err != nil {
		c.JSON(500, gin.H{"error": "删除失败"})
		return
	}
	if wasDefault {
		_ = service.InitObjectStorageProvider()
	}
	c.JSON(200, gin.H{"message": "删除成功"})
}

// TestConfig 测试连接（不保存）
func (h *ObjectStorageHandler) TestConfig(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cfg model.ObjectStorageConfig
	if err := model.DB.First(&cfg, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "配置不存在"})
		return
	}

	provider, err := service.BuildStorageFromConfig(cfg)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "message": "凭据解析失败: " + err.Error()})
		return
	}

	ok, msg := provider.CheckConnection()
	status := "ok"
	if !ok {
		status = "error"
	}

	// 更新状态
	updates := map[string]interface{}{
		"connection_status": status,
		"last_error":        msg,
		"last_checked_at":   time.Now(),
	}
	model.DB.Model(&cfg).Updates(updates)

	c.JSON(200, gin.H{"success": ok, "message": msg})
}

// SetDefault 设为默认
func (h *ObjectStorageHandler) SetDefault(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cfg model.ObjectStorageConfig
	if err := model.DB.First(&cfg, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "配置不存在"})
		return
	}

	model.DB.Model(&model.ObjectStorageConfig{}).Where("is_default = ?", true).Update("is_default", false)
	model.DB.Model(&cfg).Update("is_default", true)

	// 重新初始化 provider
	_ = service.InitObjectStorageProvider()

	c.JSON(200, gin.H{"message": "已设为默认"})
}

// storageConfigRequest 请求体
type storageConfigRequest struct {
	Name          string `json:"name" binding:"required"`
	Enabled       bool   `json:"enabled"`
	IsDefault     bool   `json:"is_default"`
	Type          string `json:"type" binding:"required"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket" binding:"required"`
	Endpoint      string `json:"endpoint"`
	Prefix        string `json:"prefix"`
	UseSSL        bool   `json:"use_ssl"`
	AccessKey     string `json:"access_key"`
	SecretKey     string `json:"secret_key"`
	RetentionDays int    `json:"retention_days"`
	MaxObjectSize int64  `json:"max_object_size"`
	TestConnection bool  `json:"test_connection"`
}

// helper
func init() {
	// ensure service.Now is available
}
