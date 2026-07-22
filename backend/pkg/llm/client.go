package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderAzure     Provider = "azure"
	ProviderDeepSeek  Provider = "deepseek"
	ProviderVLLM      Provider = "vllm"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Refusal string `json:"refusal,omitempty"` // 新增：拒绝回答标记
}

type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"` // 新增：结构化输出格式
}

type ResponseFormat struct {
	Type       string      `json:"type,omitempty"` // "json_schema" | "json_object"
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

type JSONSchema struct {
	Name   string      `json:"name"`
	Strict bool        `json:"strict"`
	Schema interface{} `json:"schema"`
}

// ChatResponse 扩展 Refusal 字段
type ChatResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"` // 命中缓存的 prompt tokens（部分 provider 返回）
}

type ErrorResponse struct {
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Client LLM 客户端接口
type Client interface {
	Chat(request *ChatRequest) (*ChatResponse, error)
	Close() error
}

// Config LLM 配置
type Config struct {
	Provider Provider
	ModelID  string
	BaseURL  string
	APIKey   string
	Timeout  time.Duration
}

// NewClient 创建 LLM 客户端
func NewClient(cfg Config) (Client, error) {
	switch cfg.Provider {
	case ProviderOpenAI, ProviderDeepSeek, ProviderVLLM:
		return newOpenAICompatibleClient(cfg), nil
	case ProviderAnthropic:
		return newAnthropicClient(cfg), nil
	case ProviderAzure:
		return newAzureClient(cfg), nil
	default:
		return newOpenAICompatibleClient(cfg), nil
	}
}

// --- OpenAI Compatible Client ---

type openAIClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	timeout    time.Duration
}

func newOpenAICompatibleClient(cfg Config) *openAIClient {
	return &openAIClient{
		baseURL: cfg.BaseURL,
		apiKey:  cfg.APIKey,
		timeout: cfg.Timeout,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (c *openAIClient) Chat(request *ChatRequest) (*ChatResponse, error) {
	url := c.baseURL + "/chat/completions"

	body, _ := json.Marshal(request)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		json.Unmarshal(respBody, &errResp)
		if errResp.Error != nil {
			return nil, fmt.Errorf("API error: %s", errResp.Error.Message)
		}
		return nil, fmt.Errorf("API error: status=%d", resp.StatusCode)
	}

	var result ChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	return &result, nil
}

func (c *openAIClient) Close() error { return nil }

// --- Anthropic Client ---

type anthropicClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	timeout    time.Duration
}

type AnthropicRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type AnthropicResponse struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func newAnthropicClient(cfg Config) *anthropicClient {
	return &anthropicClient{
		baseURL: cfg.BaseURL,
		apiKey:  cfg.APIKey,
		timeout: cfg.Timeout,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (c *anthropicClient) Chat(request *ChatRequest) (*ChatResponse, error) {
	url := c.baseURL + "/v1/messages"

	// Convert OpenAI format to Anthropic format
	anthropicReq := AnthropicRequest{
		Model:       request.Model,
		Messages:    request.Messages,
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
	}
	if anthropicReq.MaxTokens == 0 {
		anthropicReq.MaxTokens = 1024
	}

	body, _ := json.Marshal(anthropicReq)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		json.Unmarshal(respBody, &errResp)
		if errResp.Error != nil {
			return nil, fmt.Errorf("API error: %s", errResp.Error.Message)
		}
		return nil, fmt.Errorf("API error: status=%d", resp.StatusCode)
	}

	var anthropicResp AnthropicResponse
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	// Convert back to OpenAI format
	result := &ChatResponse{
		ID: anthropicResp.ID,
		Choices: []Choice{
			{
				Message: Message{
					Role:    "assistant",
					Content: anthropicResp.Content,
				},
				FinishReason: anthropicResp.StopReason,
			},
		},
		Usage: Usage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		},
	}

	return result, nil
}

func (c *anthropicClient) Close() error { return nil }

// --- Azure Client ---

type azureClient struct {
	openAIClient
	apiVersion string
}

func newAzureClient(cfg Config) *azureClient {
	return &azureClient{
		openAIClient: openAIClient{
			baseURL:    cfg.BaseURL,
			apiKey:     cfg.APIKey,
			timeout:    cfg.Timeout,
			httpClient: &http.Client{Timeout: cfg.Timeout},
		},
		apiVersion: "2024-02-01",
	}
}

func (c *azureClient) Chat(request *ChatRequest) (*ChatResponse, error) {
	url := c.baseURL + "/openai/deployments/" + request.Model + "/chat/completions?api-version=" + c.apiVersion

	body, _ := json.Marshal(request)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		json.Unmarshal(respBody, &errResp)
		if errResp.Error != nil {
			return nil, fmt.Errorf("API error: %s", errResp.Error.Message)
		}
		return nil, fmt.Errorf("API error: status=%d", resp.StatusCode)
	}

	var result ChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	return &result, nil
}

func (c *azureClient) Close() error { return nil }

// CheckConnectivity 测试 API 连通性
func CheckConnectivity(provider Provider, baseURL, apiKey string, timeout time.Duration, modelID ...string) error {
	cfg := Config{
		Provider: provider,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Timeout:  timeout,
	}

	client, err := NewClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	// Use provided model_id or default
	model := "gpt-4o-mini"
	if len(modelID) > 0 && modelID[0] != "" {
		model = modelID[0]
	}

	// Send a minimal request to test connectivity
	testReq := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "user", Content: "Hi"},
		},
		MaxTokens: 5,
	}

	_, err = client.Chat(testReq)
	return err
}

// CheckEmbeddingConnectivity tests embedding endpoint connectivity by sending
// a minimal embedding request.
func CheckEmbeddingConnectivity(baseURL, apiKey, modelID string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}

	reqBody := map[string]any{
		"model": modelID,
		"input": "test",
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
