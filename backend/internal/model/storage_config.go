package model

import "time"

// ObjectStorageConfig 对象存储配置（独立表，支持多存储后端）
type ObjectStorageConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	OrgID     uint      `gorm:"column:org_id;not null;default:1;index:idx_org_id" json:"org_id"`
	OrgName   string    `gorm:"-" json:"org_name,omitempty"`
	Name      string    `gorm:"size:64;not null" json:"name"` // 配置名称（原uniqueIndex已移除，支持同名多配置）
	IsDefault bool      `gorm:"default:false" json:"is_default"`
	Enabled   bool      `gorm:"default:false" json:"enabled"`

	// 连接参数
	Type     string `gorm:"size:20;not null" json:"type"`              // cos / s3 / minio / oss
	Region   string `gorm:"size:64" json:"region"`
	Bucket   string `gorm:"size:128;not null" json:"bucket"`
	Endpoint string `gorm:"size:512" json:"endpoint"`                  // 自定义 endpoint（MinIO）
	Prefix   string `gorm:"size:128;default:'codeguard'" json:"prefix"` // 对象前缀
	UseSSL   bool   `gorm:"default:true" json:"use_ssl"`

	// 凭据（加密存储）
	AccessKey     string `gorm:"size:255" json:"-"` // 查询时脱敏，写入时加密
	SecretKey     string `gorm:"size:255" json:"-"` // 查询时脱敏，写入时加密
	AccessKeyMask string `gorm:"size:50" json:"access_key_mask"`
	SecretKeyMask string `gorm:"size:50" json:"secret_key_mask"`

	// 高级选项
	RetentionDays int   `gorm:"default:30" json:"retention_days"`
	MaxObjectSize int64 `gorm:"default:52428800" json:"max_object_size"` // 50MB

	// 状态
	ConnectionStatus string     `gorm:"size:20;default:'unknown'" json:"connection_status"`
	LastError        string     `gorm:"size:512" json:"last_error"`
	LastCheckedAt    *time.Time `json:"last_checked_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
