package llm

// ToolDefinition 工具定义（传给模型）
type ToolDefinition struct {
	Type     string       `json:"type"`      // 固定 "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction 工具函数元数据
type ToolFunction struct {
	Name        string                 `json:"name"`        // 工具名
	Description string                 `json:"description"` // 工具功能描述
	Parameters  map[string]interface{} `json:"parameters"`  // JSON Schema 参数定义
}

// ToolCall 模型输出的工具调用请求
type ToolCall struct {
	ID       string           `json:"id"`       // 工具调用唯一 ID（OpenAI 格式）
	Type     string           `json:"type"`     // 固定 "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 工具调用参数
type ToolCallFunction struct {
	Name      string `json:"name"`      // 工具名
	Arguments string `json:"arguments"` // JSON 字符串
}

// ToolResult 工具执行结果（回传给模型）
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"` // 对应 ToolCall.ID
	Content    string `json:"content"`      // 工具执行结果文本
}

// ToolChatRequest 工具调用对话请求
type ToolChatRequest struct {
	TaskID         *uint
	ModelID        uint
	Caller         string
	Messages       []Message        // 完整对话历史
	Tools          []ToolDefinition // 可用工具定义
	ResponseFormat *ResponseFormat  // 最终输出的 JSON Schema
	MaxTokens      int
	Temperature    float64
}

// ToolChatResponse 工具调用对话响应
type ToolChatResponse struct {
	Content      string        // 最终轮次的文本内容
	ToolCalls    []ToolCall    // 模型请求的工具调用列表
	FinishReason string        // "stop" | "tool_calls" | "length" | "content_filter" | ""
	InputTokens  int           // 本轮 prompt tokens
	OutputTokens int           // 本轮 completion tokens
	ModelName    string
	ModelID      uint
	RawResponse  *ChatResponse // 原始响应（调试用）
}
