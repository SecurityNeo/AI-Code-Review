package model

import (
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ReviewAgentConfig AI评审智能体全局配置
type ReviewAgentConfig struct {
	ID              uint   `gorm:"primaryKey" json:"id"`
	EnabledStages   string `gorm:"type:json;not null;column:enabled_stages" json:"enabled_stages"`
	StageConfigs    string `gorm:"type:json;column:stage_configs" json:"stage_configs"`
	TriggerEvents   string `gorm:"type:json;column:trigger_events" json:"trigger_events"`
	ShowAgentStatus bool   `gorm:"not null;default:true;column:show_agent_status" json:"show_agent_status"`

	// 扩展智能体开关（Phase C）
	SecretScanEnabled     bool `gorm:"default:false;column:secret_scan_enabled" json:"secret_scan_enabled"`
	SecurityAuditEnabled  bool `gorm:"default:false;column:security_audit_enabled" json:"security_audit_enabled"`
	TestSuggestionEnabled bool `gorm:"default:false;column:test_suggestion_enabled" json:"test_suggestion_enabled"`
	ImpactAnalysisEnabled bool `gorm:"default:false;column:impact_analysis_enabled" json:"impact_analysis_enabled"`
	LicenseCheckEnabled   bool `gorm:"default:false;column:license_check_enabled" json:"license_check_enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy uint      `json:"updated_by"`
}

func (ReviewAgentConfig) TableName() string {
	return "review_agent_configs"
}

// EnabledStageCodes 将 JSON 字符串解析为阶段编码切片
func (c *ReviewAgentConfig) EnabledStageCodes() []string {
	var codes []string
	if c.EnabledStages == "" || c.EnabledStages == "null" {
		return codes
	}
	if err := json.Unmarshal([]byte(c.EnabledStages), &codes); err != nil {
		zap.L().Warn("ReviewAgentConfig.EnabledStageCodes unmarshal failed", zap.String("enabled_stages", c.EnabledStages), zap.Error(err))
	}
	return codes
}

// SetEnabledStageCodes 将阶段编码切片序列化为 JSON 字符串
func (c *ReviewAgentConfig) SetEnabledStageCodes(codes []string) {
	b, err := json.Marshal(codes)
	if err != nil {
		zap.L().Error("ReviewAgentConfig.SetEnabledStageCodes marshal failed", zap.Error(err))
		return
	}
	c.EnabledStages = string(b)
}

// IsStageEnabled 判断某个阶段编码是否在启用列表中
func (c *ReviewAgentConfig) IsStageEnabled(code string) bool {
	for _, s := range c.EnabledStageCodes() {
		if s == code {
			return true
		}
	}
	return false
}

// StageConfig 读取某个阶段的专属参数
func (c *ReviewAgentConfig) StageConfig(code string) map[string]interface{} {
	var all map[string]map[string]interface{}
	if c.StageConfigs == "" || c.StageConfigs == "null" {
		return nil
	}
	if err := json.Unmarshal([]byte(c.StageConfigs), &all); err != nil {
		zap.L().Warn("ReviewAgentConfig.StageConfig unmarshal failed", zap.String("stage_configs", c.StageConfigs), zap.Error(err))
		return nil
	}
	if cfg, ok := all[code]; ok {
		return cfg
	}
	return nil
}

// SetStageConfig 写入某个阶段的专属参数
func (c *ReviewAgentConfig) SetStageConfig(code string, cfg map[string]interface{}) {
	var all map[string]map[string]interface{}
	if c.StageConfigs != "" && c.StageConfigs != "null" {
		if err := json.Unmarshal([]byte(c.StageConfigs), &all); err != nil {
			zap.L().Warn("ReviewAgentConfig.SetStageConfig unmarshal failed", zap.String("stage_configs", c.StageConfigs), zap.Error(err))
		}
	}
	if all == nil {
		all = make(map[string]map[string]interface{})
	}
	all[code] = cfg
	b, err := json.Marshal(all)
	if err != nil {
		zap.L().Error("ReviewAgentConfig.SetStageConfig marshal failed", zap.Error(err))
		return
	}
	c.StageConfigs = string(b)
}

// CodeUnderstandingDepth 便捷方法：读取 code_understanding 的 AST 分析深度参数
func (c *ReviewAgentConfig) CodeUnderstandingDepth() int {
	cfg := c.StageConfig("code_understanding")
	if cfg == nil {
		return 1 // 默认 1 层
	}
	if d, ok := cfg["depth"].(float64); ok {
		return int(d)
	}
	if d, ok := cfg["depth"].(int); ok {
		return d
	}
	return 1
}

// CodeUnderstandingTimeouts 便捷方法：读取 code_understanding 三阶段超时配置
func (c *ReviewAgentConfig) CodeUnderstandingTimeouts() (contextExtractTimeout int, symbolGraphTimeout int, reportMergeTimeout int) {
	cfg := c.StageConfig("code_understanding")
	if cfg == nil {
		return 30, 60, 10 // 默认超时
	}
	getInt := func(key string, def int) int {
		if v, ok := cfg[key].(float64); ok {
			return int(v)
		}
		if v, ok := cfg[key].(int); ok {
			return v
		}
		return def
	}
	return getInt("context_extract_timeout", 30), getInt("symbol_graph_timeout", 60), getInt("report_merge_timeout", 10)
}

// 兼容旧配置：自动将 context_extract 迁移到 code_understanding
func (c *ReviewAgentConfig) MigrateContextExtractConfig() {
	if c.StageConfigs == "" || c.StageConfigs == "null" {
		return
	}
	var all map[string]interface{}
	if err := json.Unmarshal([]byte(c.StageConfigs), &all); err != nil {
		return
	}
	if _, hasOld := all["context_extract"]; hasOld {
		if _, hasNew := all["code_understanding"]; !hasNew {
			// 旧配置存在但新配置不存在，自动迁移
			all["code_understanding"] = all["context_extract"]
			b, _ := json.Marshal(all)
			c.StageConfigs = string(b)
		}
	}
}

// TriggerEventCodes 将 JSON 字符串解析为触发事件编码切片
func (c *ReviewAgentConfig) TriggerEventCodes() []string {
	var events []string
	if c.TriggerEvents == "" || c.TriggerEvents == "null" {
		return events
	}
	if err := json.Unmarshal([]byte(c.TriggerEvents), &events); err != nil {
		zap.L().Warn("ReviewAgentConfig.TriggerEventCodes unmarshal failed", zap.String("trigger_events", c.TriggerEvents), zap.Error(err))
	}
	return events
}

// SetTriggerEventCodes 将触发事件编码切片序列化为 JSON 字符串
func (c *ReviewAgentConfig) SetTriggerEventCodes(events []string) {
	b, err := json.Marshal(events)
	if err != nil {
		zap.L().Error("ReviewAgentConfig.SetTriggerEventCodes marshal failed", zap.Error(err))
		return
	}
	c.TriggerEvents = string(b)
}

// IsTriggerEventEnabled 判断某个事件编码是否允许触发
func (c *ReviewAgentConfig) IsTriggerEventEnabled(code string) bool {
	for _, e := range c.TriggerEventCodes() {
		if e == code {
			return true
		}
		// 兼容旧配置：merge_request 包含所有子事件
		if e == "merge_request" && strings.HasPrefix(code, "merge_request_") {
			return true
		}
	}
	return false
}
