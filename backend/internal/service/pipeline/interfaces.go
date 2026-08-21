package pipeline

import (
	"context"

	"github.com/ai-optimizer/backend/pkg/llm"
)

// StageExecutor 阶段执行器接口
type StageExecutor interface {
	Code() string
	Execute(ctx StageContext) error
}

// ChatResult LLM 调用结果（pipeline 私有定义，避免跨包类型不匹配）
type ChatResult struct {
	Content      string
	ModelName    string
	ModelID      uint
	InputTokens  int // prompt_tokens
	OutputTokens int // completion_tokens
}

// StructuredChatResult 结构化输出结果
type StructuredChatResult struct {
	Content      string
	ModelName    string
	ModelID      uint
	InputTokens  int
	OutputTokens int
	Response     *llm.ChatResponse // 完整响应（含 Refusal）
}

// LLMService LLM 调用接口（由上层注入，避免 import cycle）
type LLMService interface {
	ChatCompletion(ctx context.Context, taskID *uint, modelID uint, caller, customInstruction, userPrompt string) (*ChatResult, error)
	ChatCompletionStructured(ctx context.Context, taskID *uint, modelID uint, caller, systemPrompt, userPrompt string, responseFormat *llm.ResponseFormat) (*StructuredChatResult, error)
}
