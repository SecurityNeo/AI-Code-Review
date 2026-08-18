package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
)

// ReviewAgentConfigService 智能体全局配置服务
type ReviewAgentConfigService struct{}

func NewReviewAgentConfigService() *ReviewAgentConfigService {
	return &ReviewAgentConfigService{}
}

// 已知阶段编码白名单（前端可配置的阶段）
var validStageCodes = map[string]bool{
	"trigger_check": true, "git_clone": true, "code_understanding": true,
	"dependency_scan": true,
	"secret_scan":     true, "security_audit": true,
	"test_suggestion": true, "impact_analysis": true,
	"batch_review_frame": true, "review_arbitration": true, "post_process": true,
}

// 已知触发事件白名单
var validTriggerEvents = map[string]bool{
	"merge_request":        true, // 兼容旧配置：包含所有子事件
	"merge_request_open":   true,
	"merge_request_update": true,
	"merge_request_reopen": true,
}

// GetOrDefault 获取当前配置，若不存在则返回默认配置
func (s *ReviewAgentConfigService) GetOrDefault() model.ReviewAgentConfig {
	var cfg model.ReviewAgentConfig
	if err := model.DB.First(&cfg, 1).Error; err != nil {
		return defaultReviewAgentConfig()
	}
	// 兼容旧配置：自动将 mandatory 阶段加入 enabled_stages
	stages := cfg.EnabledStageCodes()
	needsUpdate := false
	// review_arbitration 是 mandatory 阶段，必须存在
	if !contains(stages, "review_arbitration") {
		stages = append(stages, "review_arbitration")
		needsUpdate = true
	}
	// 兼容旧配置：根据布尔字段同步扩展阶段到 enabled_stages
	extStageMap := map[string]bool{
		"secret_scan":     cfg.SecretScanEnabled,
		"security_audit":  cfg.SecurityAuditEnabled,
		"test_suggestion": cfg.TestSuggestionEnabled,
		"impact_analysis": cfg.ImpactAnalysisEnabled,
	}
	for code, enabled := range extStageMap {
		if enabled && !contains(stages, code) {
			stages = append(stages, code)
			needsUpdate = true
		}
	}
	// 同时确保有 stage_configs 中的 review_arbitration 超时配置
	if sc := cfg.StageConfig("review_arbitration"); sc == nil {
		cfg.SetStageConfig("review_arbitration", map[string]interface{}{"timeout": 60})
		needsUpdate = true
	}
	// 兼容旧配置：为扩展智能体补充 llm_enhance_timeout（Phase 3 LLM 增强超时）
	extStages := []string{"secret_scan", "security_audit", "test_suggestion", "impact_analysis"}
	for _, code := range extStages {
		sc := cfg.StageConfig(code)
		if sc == nil {
			sc = map[string]interface{}{"timeout": 30}
		}
		if _, ok := sc["llm_enhance_timeout"]; !ok {
			sc["llm_enhance_timeout"] = 300
			cfg.SetStageConfig(code, sc)
			needsUpdate = true
		}
	}
	// 兼容旧配置：batch_review_frame 补充 max_diff_files 和 max_tokens_per_batch
	if brCfg := cfg.StageConfig("batch_review_frame"); brCfg != nil {
		if _, ok := brCfg["max_diff_files"]; !ok {
			brCfg["max_diff_files"] = 50
			needsUpdate = true
		}
		if _, ok := brCfg["max_tokens_per_batch"]; !ok {
			brCfg["max_tokens_per_batch"] = 100000
			needsUpdate = true
		}
		if needsUpdate {
			cfg.SetStageConfig("batch_review_frame", brCfg)
		}
	}
	if needsUpdate {
		cfg.SetEnabledStageCodes(stages)
		// 静默更新数据库，不抛错
		model.DB.Model(&model.ReviewAgentConfig{}).
			Where("id = ?", cfg.ID).
			Updates(map[string]interface{}{
				"enabled_stages": cfg.EnabledStages,
				"stage_configs":  cfg.StageConfigs,
			})
	}
	return cfg
}

// DependencyViolationError 依赖校验失败时返回的自定义错误类型
// 携带结构化的违规列表，供前端精确定位标红卡片
type DependencyViolationError struct {
	Violations []model.StageDependencyViolation
}

func (e *DependencyViolationError) Error() string {
	msgs := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		msgs[i] = v.Message
	}
	return fmt.Sprintf("智能体配置依赖校验失败：%s", strings.Join(msgs, "；"))
}

// Save 保存智能体全局配置（整表覆盖更新）
func (s *ReviewAgentConfigService) Save(
	stages []string,
	stageConfigs map[string]interface{},
	triggerEvents []string,
	showStatus bool,
	userID uint,
) error {
	// 保证 batch_review_frame 始终启用（代码评审核心智能体不可禁用）
	if !contains(stages, "batch_review_frame") {
		stages = append(stages, "batch_review_frame")
	}

	// 校验 enabled_stages
	hasPostProcess := false
	hasReviewArbitration := false
	for _, code := range stages {
		if code == "post_process" {
			hasPostProcess = true
		}
		if code == "review_arbitration" {
			hasReviewArbitration = true
		}
		if !validStageCodes[code] {
			return fmt.Errorf("未知的阶段编码: %s", code)
		}
	}
	if !hasPostProcess {
		return fmt.Errorf("post_process 阶段不可禁用")
	}
	if !hasReviewArbitration {
		return fmt.Errorf("review_arbitration 阶段不可禁用")
	}

	// 校验 trigger_events
	for _, ev := range triggerEvents {
		if !validTriggerEvents[ev] {
			return fmt.Errorf("未知的触发事件: %s", ev)
		}
	}

	// 校验 stage_configs 中的数值参数
	if stageConfigs != nil {
		if cuCfg, ok := stageConfigs["code_understanding"].(map[string]interface{}); ok {
			// 校验 AST 分析深度
			if depth, ok := cuCfg["depth"]; ok {
				var d int
				switch v := depth.(type) {
				case float64:
					d = int(v)
				case int:
					d = v
				case json.Number:
					id, _ := v.Int64()
					d = int(id)
				case int64:
					d = int(v)
				default:
					d = -1
				}
				if d < 0 || d > 2 {
					return fmt.Errorf("code_understanding.depth 必须在 0-2 之间")
				}
			}
			// 校验三阶段超时时间
			for _, key := range []string{"context_extract_timeout", "symbol_graph_timeout", "report_merge_timeout"} {
				if t, ok := cuCfg[key]; ok {
					var tv int
					switch v := t.(type) {
					case float64:
						tv = int(v)
					case int:
						tv = v
					case json.Number:
						iv, _ := v.Int64()
						tv = int(iv)
					case int64:
						tv = int(v)
					default:
						tv = -1
					}
					if tv < 1 || tv > 300 {
						return fmt.Errorf("code_understanding.%s 必须在 1-300 秒之间", key)
					}
				}
			}
		}
		// 校验 batch_review_frame.parallel_max
		if brCfg, ok := stageConfigs["batch_review_frame"].(map[string]interface{}); ok {
			if pm, ok := brCfg["parallel_max"]; ok {
				var v int
				switch val := pm.(type) {
				case float64:
					v = int(val)
				case int:
					v = val
				case json.Number:
					iv, _ := val.Int64()
					v = int(iv)
				default:
					v = -1
				}
				if v < 1 || v > 10 {
					return fmt.Errorf("batch_review_frame.parallel_max 必须在 1-10 之间")
				}
			}
		}
		// 校验所有阶段的 timeout（5-3600 秒）
		for code, cfg := range stageConfigs {
			if m, ok := cfg.(map[string]interface{}); ok {
				if t, ok := m["timeout"]; ok {
					var v int
					switch val := t.(type) {
					case float64:
						v = int(val)
					case int:
						v = val
					case json.Number:
						iv, _ := val.Int64()
						v = int(iv)
					default:
						v = -1
					}
					if v < 5 || v > 3600 {
						return fmt.Errorf("%s.timeout 必须在 5-3600 秒之间", code)
					}
				}
			}
		}
	}
	// 【修改】不再校验已启用阶段的超时时间之和是否超过系统任务总超时
	// AI 评审类任务的超时控制已转移到 TaskService.TimeoutCheck()，
	// 使用各阶段超时之和 + 30 分钟宽裕时间独立计算。

	// 构造临时配置对象进行依赖校验
	tmpCfg := model.ReviewAgentConfig{}
	tmpCfg.SetEnabledStageCodes(stages)
	tmpCfg.SecretScanEnabled = contains(stages, "secret_scan")
	tmpCfg.SecurityAuditEnabled = contains(stages, "security_audit")
	tmpCfg.TestSuggestionEnabled = contains(stages, "test_suggestion")
	tmpCfg.ImpactAnalysisEnabled = contains(stages, "impact_analysis")
	if ok, violations := tmpCfg.ValidateEnabledStages(); !ok {
		return &DependencyViolationError{Violations: violations}
	}

	jsonBytes, err := json.Marshal(stageConfigs)
	if err != nil {
		return fmt.Errorf("序列化 stage_configs 失败: %w", err)
	}

	// 从 stages 数组推导扩展阶段状态，保持模型字段同步
	cfg := model.ReviewAgentConfig{
		ID:                    1,
		StageConfigs:          string(jsonBytes),
		ShowAgentStatus:       showStatus,
		SecretScanEnabled:     contains(stages, "secret_scan"),
		SecurityAuditEnabled:  contains(stages, "security_audit"),
		TestSuggestionEnabled: contains(stages, "test_suggestion"),
		ImpactAnalysisEnabled: contains(stages, "impact_analysis"),
		LicenseCheckEnabled:   contains(stages, "license_check"),
		UpdatedBy:             userID,
	}
	cfg.SetEnabledStageCodes(stages)
	cfg.SetTriggerEventCodes(triggerEvents)

	// 只更新目标字段，避免触碰 created_at
	return model.DB.Model(&model.ReviewAgentConfig{}).
		Where("id = ?", 1).
		Updates(map[string]interface{}{
			"enabled_stages":          cfg.EnabledStages,
			"stage_configs":           cfg.StageConfigs,
			"trigger_events":          cfg.TriggerEvents,
			"show_agent_status":       showStatus,
			"secret_scan_enabled":     cfg.SecretScanEnabled,
			"security_audit_enabled":  cfg.SecurityAuditEnabled,
			"test_suggestion_enabled": cfg.TestSuggestionEnabled,
			"impact_analysis_enabled": cfg.ImpactAnalysisEnabled,
			"license_check_enabled":   cfg.LicenseCheckEnabled,
			"updated_by":              userID,
		}).Error
}

func defaultReviewAgentConfig() model.ReviewAgentConfig {
	cfg := model.ReviewAgentConfig{
		ID:              1,
		ShowAgentStatus: true,
	}
	cfg.SetEnabledStageCodes([]string{
		"trigger_check", "git_clone", "code_understanding",
		"dependency_scan", "batch_review_frame", "review_arbitration", "post_process",
	})
	cfg.SetStageConfig("code_understanding", map[string]interface{}{
		"depth":                   1,
		"context_extract_timeout": 30,
		"symbol_graph_timeout":    60,
		"report_merge_timeout":    10,
	})
	cfg.SetStageConfig("git_clone", map[string]interface{}{
		"timeout": 60,
	})
	cfg.SetStageConfig("dependency_scan", map[string]interface{}{
		"timeout": 30,
	})
	cfg.SetStageConfig("secret_scan", map[string]interface{}{
		"timeout":             30,
		"llm_enhance_timeout": 300,
	})
	cfg.SetStageConfig("security_audit", map[string]interface{}{
		"timeout":             30,
		"llm_enhance_timeout": 300,
	})
	cfg.SetStageConfig("test_suggestion", map[string]interface{}{
		"timeout":             30,
		"llm_enhance_timeout": 300,
	})
	cfg.SetStageConfig("impact_analysis", map[string]interface{}{
		"timeout":             30,
		"llm_enhance_timeout": 300,
	})
	cfg.SetStageConfig("batch_review_frame", map[string]interface{}{
		"timeout":              300,
		"parallel_max":         3,
		"max_diff_files":       50,
		"max_tokens_per_batch": 100000,
	})
	cfg.SetStageConfig("review_arbitration", map[string]interface{}{
		"timeout": 60,
	})
	cfg.SetStageConfig("post_process", map[string]interface{}{
		"timeout": 30,
	})
	cfg.SetStageConfig("trigger_check", map[string]interface{}{
		"timeout": 5,
	})
	return cfg
}

func contains(arr []string, s string) bool {
	for _, v := range arr {
		if v == s {
			return true
		}
	}
	return false
}
