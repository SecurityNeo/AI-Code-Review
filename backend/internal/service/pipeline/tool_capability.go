package pipeline

import "github.com/ai-optimizer/backend/pkg/llm"

// ToolCapability 模型 Tool Calling 能力等级
type ToolCapability int

const (
	ToolCapNone       ToolCapability = iota // 不支持
	ToolCapPseudoTag                          // <tool_call> 伪标签
	ToolCapNative                              // 原生支持
)

// DetectToolCapability 基于 Provider 推断能力等级
// 不硬编码白名单，按 Provider 类型默认推断，调用方可运行时验证
func DetectToolCapability(provider llm.Provider) ToolCapability {
	switch provider {
	case llm.ProviderOpenAI, llm.ProviderAnthropic, llm.ProviderAzure:
		return ToolCapNative
	case llm.ProviderDeepSeek, llm.ProviderVLLM:
		// DeepSeek/VLLM 实际支持原生 tool calling，但为安全起见先用伪标签
		return ToolCapPseudoTag
	default:
		return ToolCapPseudoTag
	}
}
