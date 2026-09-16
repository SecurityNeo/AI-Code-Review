package pipeline

import (
	"github.com/ai-optimizer/backend/internal/model"
)

// AgenticConfig Agentic 评审配置
type AgenticConfig struct {
	Enabled          bool
	MaxRounds        int
	MaxTools         int
	Temperature      float64
	BudgetMultiplier float64 // 预算弹性: 1.2 = availableTokens * 1.2
}

// ParseAgenticConfig 从 ReviewAgentConfig 解析 Agentic 配置
func ParseAgenticConfig(agentCfg model.ReviewAgentConfig) AgenticConfig {
	cfg := AgenticConfig{
		Enabled:          false,
		MaxRounds:        5,
		MaxTools:         8,
		Temperature:      0.1,
		BudgetMultiplier: 1.2,
	}

	if sc := agentCfg.StageConfig("batch_review_frame"); sc != nil {
		if v, ok := sc["agentic_mode"].(bool); ok {
			cfg.Enabled = v
		}
		if v, ok := sc["agentic_max_rounds"].(float64); ok && v > 0 {
			cfg.MaxRounds = int(v)
		}
		if v, ok := sc["agentic_max_tools"].(float64); ok && v > 0 {
			cfg.MaxTools = int(v)
		}
		if v, ok := sc["agentic_temperature"].(float64); ok {
			cfg.Temperature = v
		}
		if v, ok := sc["agentic_budget_multiplier"].(float64); ok && v > 0 {
			cfg.BudgetMultiplier = v
		}
	}

	return cfg
}

// getAgenticMaxRounds 从 StageContext 获取最大轮次配置
func getAgenticMaxRounds(ctx StageContext) int {
	if cfg := getAgenticConfigFromCtx(ctx); cfg != nil {
		return cfg.MaxRounds
	}
	return 5
}

// getAgenticMaxTools 从 StageContext 获取最大工具调用次数
func getAgenticMaxTools(ctx StageContext) int {
	if cfg := getAgenticConfigFromCtx(ctx); cfg != nil {
		return cfg.MaxTools
	}
	return 8
}

// getAgenticTemperature 从 StageContext 获取温度配置
func getAgenticTemperature(ctx StageContext) float64 {
	if cfg := getAgenticConfigFromCtx(ctx); cfg != nil {
		return cfg.Temperature
	}
	return 0.1
}

// getBudgetMultiplier 从 StageContext 获取预算弹性倍数
func getBudgetMultiplier(ctx StageContext) float64 {
	if cfg := getAgenticConfigFromCtx(ctx); cfg != nil {
		return cfg.BudgetMultiplier
	}
	// 【Agentic 多轮对话默认 3 倍预算】单批次 Prompt 只是一次性调用，
	// Agentic 需要工具调用来回追加消息，累积 token 可能达到 2-3 倍初始输入。
	return 3.0
}

// getAgentConfigFromCtx 从 StageContext 获取 ReviewAgentConfig
func getAgentConfigFromCtx(ctx StageContext) model.ReviewAgentConfig {
	if v, ok := ctx.GetInput("_agent_config").(model.ReviewAgentConfig); ok {
		return v
	}
	return model.ReviewAgentConfig{}
}

// getAgenticConfigFromCtx 从 StageContext 获取 AgenticConfig
func getAgenticConfigFromCtx(ctx StageContext) *AgenticConfig {
	if v := ctx.GetInput("_agentic_config"); v != nil {
		if cfg, ok := v.(*AgenticConfig); ok {
			return cfg
		}
	}
	return nil
}
