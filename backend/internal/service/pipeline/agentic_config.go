package pipeline

import (
	"github.com/ai-optimizer/backend/internal/model"
)

// AgenticConfig Agentic 评审配置
type AgenticConfig struct {
	Enabled           bool
	MaxRounds         int
	MaxTools          int
	Temperature       float64
	BudgetMultiplier  float64 // 预算弹性: 1.2 = availableTokens * 1.2
	MaxMessages       int     // 消息条数软上限，超过则强制进入最终轮（0 表示按 MaxTools 推导）
	HistoryTokenLimit int     // 历史消息累积 token 超此值触发截断（0 表示用默认 80000）
}

const (
	// agenticMaxMessagesSlack 消息条数软上限相对 max_tools 的余量
	agenticMaxMessagesSlack = 4
	// minAgenticMaxMessages 消息条数软上限的下限保护
	minAgenticMaxMessages = 6
	// defaultAgenticHistoryTokenLimit 历史消息 token 截断默认阈值
	defaultAgenticHistoryTokenLimit = 80000
)

// defaultAgenticMaxMessages 根据最大工具调用次数推导消息条数软上限。
// 消息增长：初始 2 条（system+user）+ 每轮 1 条 assistant + 每条工具结果 1 条 tool。
// 允许 maxTools 次工具调用基本执行完，再留 agenticMaxMessagesSlack 条余量用于收敛。
func defaultAgenticMaxMessages(maxTools int) int {
	n := maxTools + agenticMaxMessagesSlack
	if n < minAgenticMaxMessages {
		n = minAgenticMaxMessages
	}
	return n
}

// ParseAgenticConfig 从 ReviewAgentConfig 解析 Agentic 配置
func ParseAgenticConfig(agentCfg model.ReviewAgentConfig) AgenticConfig {
	cfg := AgenticConfig{
		Enabled:           false,
		MaxRounds:         5,
		MaxTools:          8,
		Temperature:       0.1,
		BudgetMultiplier:  1.2,
		MaxMessages:       0, // 解析后按 MaxTools 推导
		HistoryTokenLimit: 0, // 解析后用默认值
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
		if v, ok := sc["agentic_max_messages"].(float64); ok && v > 0 {
			cfg.MaxMessages = int(v)
		}
		if v, ok := sc["agentic_history_token_limit"].(float64); ok && v > 0 {
			cfg.HistoryTokenLimit = int(v)
		}
	}

	// 未显式配置时按 max_tools 联动推导合理默认值
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = defaultAgenticMaxMessages(cfg.MaxTools)
	}
	if cfg.HistoryTokenLimit <= 0 {
		cfg.HistoryTokenLimit = defaultAgenticHistoryTokenLimit
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

// getAgenticMaxMessages 从 StageContext 获取消息条数软上限，缺省按 max_tools 推导
func getAgenticMaxMessages(ctx StageContext) int {
	if cfg := getAgenticConfigFromCtx(ctx); cfg != nil && cfg.MaxMessages > 0 {
		return cfg.MaxMessages
	}
	return defaultAgenticMaxMessages(getAgenticMaxTools(ctx))
}

// getAgenticHistoryTokenLimit 从 StageContext 获取历史消息 token 截断阈值
func getAgenticHistoryTokenLimit(ctx StageContext) int {
	if cfg := getAgenticConfigFromCtx(ctx); cfg != nil && cfg.HistoryTokenLimit > 0 {
		return cfg.HistoryTokenLimit
	}
	return defaultAgenticHistoryTokenLimit
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
