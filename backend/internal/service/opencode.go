package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/encrypt"
	"go.uber.org/zap"
)

type OpencodeClient struct {
	Endpoint      string
	Username      string
	Password      string
	APIKey        string
	ModelProvider string
	ModelID       string
	Client        *http.Client
}

func NewOpencodeClient(endpoint, username, password string) *OpencodeClient {
	client := &http.Client{}
	c := &OpencodeClient{
		Endpoint: endpoint,
		Username: username,
		Password: password,
		Client:   client,
	}
	c.loadConfig()
	return c
}

func NewOpencodeClientWithLongTimeout(endpoint, username, password string) *OpencodeClient {
	client := &http.Client{
		Timeout: 120 * time.Second,
	}
	c := &OpencodeClient{
		Endpoint: endpoint,
		Username: username,
		Password: password,
		Client:   client,
	}
	c.loadConfig()
	return c
}

func NewOpencodeClientWithTaskTimeout(endpoint, username, password string, timeoutMin int) *OpencodeClient {
	timeout := time.Duration(timeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 120 * time.Minute // 默认2小时，支持大token量
	}
	client := &http.Client{
		Timeout: timeout,
	}
	c := &OpencodeClient{
		Endpoint: endpoint,
		Username: username,
		Password: password,
		Client:   client,
	}
	c.loadConfig()
	return c
}

func NewOpencodeClientWithAPIKeyAndTimeout(endpoint, apiKey string, timeoutMin int) *OpencodeClient {
	timeout := time.Duration(timeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 120 * time.Minute // 默认2小时
	}
	client := &http.Client{
		Timeout: timeout,
	}
	c := &OpencodeClient{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Client:   client,
	}
	c.loadConfig()
	return c
}

func NewOpencodeClientWithAPIKey(endpoint, apiKey string) *OpencodeClient {
	client := &http.Client{
		Timeout: 120 * time.Second,
	}
	c := &OpencodeClient{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Client:   client,
	}
	c.loadConfig()
	return c
}

type OpencodeConfig struct {
	Model    string `json:"model"`
	Provider map[string]struct {
		Name   string              `json:"name"`
		Models map[string]struct{} `json:"models"`
	} `json:"provider"`
}

func (c *OpencodeClient) loadConfig() {
	url := fmt.Sprintf("%s/config", c.Endpoint)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		zap.L().Warn("load config request failed", zap.Error(err))
		return
	}

	hasAuth := false
	if c.APIKey != "" {
		hasAuth = true
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	} else if c.Username != "" && c.Password != "" {
		hasAuth = true
		auth := c.Username + ":" + c.Password
		encoded := base64.StdEncoding.EncodeToString([]byte(auth))
		req.Header.Set("Authorization", "Basic "+encoded)
	}
	zap.L().Debug("loadConfig auth check", zap.String("endpoint", c.Endpoint), zap.Bool("has_auth", hasAuth), zap.String("username", c.Username), zap.String("password_len", fmt.Sprintf("%d", len(c.Password))))

	resp, err := c.Client.Do(req)
	if err != nil {
		zap.L().Warn("load config request failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		zap.L().Warn("load config failed", zap.Int("status", resp.StatusCode))
		return
	}

	var config OpencodeConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		zap.L().Warn("parse config failed", zap.Error(err))
		return
	}

	c.ModelID = config.Model
	for providerID := range config.Provider {
		c.ModelProvider = providerID
		break
	}

	zap.L().Debug("opencode config loaded", zap.String("model", c.ModelID), zap.String("provider", c.ModelProvider))
}

func (c *OpencodeClient) Login() error {
	if c.APIKey == "" && c.Username == "" && c.Password == "" {
		return nil
	}

	testURL := fmt.Sprintf("%s/global/health", c.Endpoint)
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("auth failed: invalid credentials")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("auth failed with status %d", resp.StatusCode)
	}
	return nil
}

func (c *OpencodeClient) setAuth(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	} else if c.Username != "" && c.Password != "" {
		auth := c.Username + ":" + c.Password
		encoded := base64.StdEncoding.EncodeToString([]byte(auth))
		req.Header.Set("Authorization", "Basic "+encoded)
	}
}

type SessionResponse struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type MessageResponse struct {
	Info struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"info"`
	Parts []Part `json:"parts"`
}

// OpenCodeMessage 代表 /session/{id}/message 返回的单条消息
// 时间字段为 Unix 毫秒时间戳
// Role: user | assistant
// Parts 类型: text | reasoning | tool | step-start | step-finish
//
// 示例：
//
//	{
//	  "info": {
//	    "id": "msg_xxx",
//	    "sessionID": "ses_xxx",
//	    "role": "user",
//	    "time": { "created": 1779701930192 },
//	    "agent": "general",
//	    "model": { "providerID": "Uni-Self-Hosted-Models", "modelID": "Kimi-K2.6" }
//	  },
//	  "parts": [{ "type": "text", "text": "用户输入..." }]
//	}
type OpenCodeMessage struct {
	Info  MessageInfo    `json:"info"`
	Parts []OpenCodePart `json:"parts"`
}

type MessageInfo struct {
	ID         string                 `json:"id"`
	SessionID  string                 `json:"sessionID"`
	Role       string                 `json:"role"`
	Time       MessageTime            `json:"time"`
	ParentID   string                 `json:"parentID,omitempty"`
	ModelID    string                 `json:"modelID,omitempty"`
	ProviderID string                 `json:"providerID,omitempty"`
	Mode       string                 `json:"mode,omitempty"`
	Agent      string                 `json:"agent,omitempty"`
	Path       map[string]interface{} `json:"path,omitempty"`
	Cost       int                    `json:"cost,omitempty"`
	Tokens     MessageTokens          `json:"tokens,omitempty"`
	Finish     string                 `json:"finish,omitempty"`
}

type MessageTime struct {
	Created   int64  `json:"created"`
	Completed *int64 `json:"completed,omitempty"`
}

type MessageTokens struct {
	Total     int `json:"total"`
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	Cache     struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

type OpenCodePart struct {
	ID        string                 `json:"id,omitempty"`
	SessionID string                 `json:"sessionID,omitempty"`
	MessageID string                 `json:"messageID,omitempty"`
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	CallID    string                 `json:"callID,omitempty"`
	Tool      string                 `json:"tool,omitempty"`
	State     map[string]interface{} `json:"state,omitempty"`
	Time      *PartTime              `json:"time,omitempty"`
}

type PartTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Part 通用 Part 类型（保持向后兼容）
type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type PromptRequest struct {
	NoReply bool   `json:"noReply,omitempty"`
	Parts   []Part `json:"parts"`
}

func (c *OpencodeClient) CreateSession(title string) (*SessionResponse, error) {
	url := fmt.Sprintf("%s/session", c.Endpoint)
	body := map[string]string{"title": title}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("create session failed: %d", resp.StatusCode)
	}

	var result SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *OpencodeClient) CreateSessionWithDirectory(title, directory string) (*SessionResponse, error) {
	url := fmt.Sprintf("%s/session", c.Endpoint)
	body := map[string]string{"title": title}
	if directory != "" {
		// 如果需要，可以添加 directory 到 body
		// body["directory"] = directory
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Opencode-Directory", directory)
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("create session with directory failed: %d", resp.StatusCode)
	}

	var result SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *OpencodeClient) SendPrompt(sessionID, prompt string) (*MessageResponse, error) {
	return c.SendPromptWithDirectory(sessionID, prompt, "")
}

func (c *OpencodeClient) SendPromptWithDirectory(sessionID, prompt, directory string) (*MessageResponse, error) {
	url := fmt.Sprintf("%s/session/%s/message", c.Endpoint, sessionID)
	reqBody := map[string]interface{}{
		"parts": []map[string]string{{"type": "text", "text": prompt}},
		"agent": "general",
		"model": map[string]string{
			"providerID": c.ModelProvider,
			"modelID":    c.ModelID,
		},
	}
	jsonBody, _ := json.Marshal(reqBody)

	zap.L().Debug("send prompt", zap.String("session_id", sessionID), zap.String("prompt", prompt), zap.String("directory", directory), zap.String("body", string(jsonBody)))

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if directory != "" {
		req.Header.Set("X-OpenCode-Directory", directory)
	}
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		zap.L().Error("send prompt failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("send prompt failed: %d", resp.StatusCode)
	}

	var result MessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *OpencodeClient) AbortSession(sessionID string) (bool, error) {
	url := fmt.Sprintf("%s/session/%s/abort", c.Endpoint, sessionID)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return false, err
	}
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

// SendPromptAsync 异步发送消息，使用 /session/{id}/prompt_async 接口
func (c *OpencodeClient) SendPromptAsync(sessionID, prompt, directory string) error {
	url := fmt.Sprintf("%s/session/%s/prompt_async", c.Endpoint, sessionID)
	reqBody := map[string]interface{}{
		"parts": []map[string]string{{"type": "text", "text": prompt}},
		"agent": "general",
		"model": map[string]string{
			"providerID": c.ModelProvider,
			"modelID":    c.ModelID,
		},
	}
	jsonBody, _ := json.Marshal(reqBody)

	zap.L().Debug("send prompt_async", zap.String("session_id", sessionID), zap.String("directory", directory))

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if directory != "" {
		req.Header.Set("X-OpenCode-Directory", directory)
	}
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		zap.L().Error("send prompt_async failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return fmt.Errorf("send prompt_async failed: %d", resp.StatusCode)
	}

	zap.L().Info("prompt sent asynchronously", zap.String("session_id", sessionID), zap.Int("status", resp.StatusCode))
	return nil
}

// ==============================================================================
// SSE 事件流处理
// ==============================================================================

// SSEEvent 代表转发给前端的 SSE 事件
type SSEEvent struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

// PartInfo OpenCode Event 中的 Part 数据结构（通用）
type PartInfo struct {
	ID        string                 `json:"id"`
	MessageID string                 `json:"messageID"`
	SessionID string                 `json:"sessionID"`
	Type      string                 `json:"type"` // reasoning | text | tool | step-start | step-finish
	Text      string                 `json:"text"`
	Tool      string                 `json:"tool,omitempty"`
	CallID    string                 `json:"callID,omitempty"`
	State     map[string]interface{} `json:"state,omitempty"`
	Time      *struct {
		Start int64 `json:"start"`
		End   int64 `json:"end,omitempty"`
	} `json:"time,omitempty"`
}

// OpenCodeProperties payload.properties 结构（支持多种事件类型）
type OpenCodeProperties struct {
	SessionID string    `json:"sessionID"`
	MessageID string    `json:"messageID"`
	PartID    string    `json:"partID"`         // 用于 message.part.delta
	Field     string    `json:"field"`          // 用于 message.part.delta
	Delta     string    `json:"delta"`          // 用于 message.part.delta
	Text      string    `json:"text"`           // 用于 sync / message.part.updated
	Part      *PartInfo `json:"part,omitempty"` // 用于 message.part.updated（⭐关键！）
	Time      int64     `json:"time"`           // Unix 毫秒时间戳
}

// OpenCodeGlobalEvent 代表 /global/event 返回的 NDJSON 事件
type OpenCodeGlobalEvent struct {
	Directory string `json:"directory"`
	Project   string `json:"project"`
	Payload   struct {
		ID         string             `json:"id"`
		Type       string             `json:"type"` // message.part.delta | message.part.updated | message.completed | ...
		Properties OpenCodeProperties `json:"properties"`
	} `json:"payload"`
}

// PartMeta 缓存在内存中的 part 元数据
type PartMeta struct {
	Type      string                 `json:"type"`
	Tool      string                 `json:"tool,omitempty"`
	State     map[string]interface{} `json:"state,omitempty"`
	MessageID string                 `json:"messageID,omitempty"`
	Time      *struct {
		Start int64 `json:"start"`
		End   int64 `json:"end,omitempty"`
	} `json:"time,omitempty"`
}

// SSEDeltaData delta 事件的数据结构（JSON）
type SSEDeltaData struct {
	Delta        string                 `json:"delta"`
	PartID       string                 `json:"partID"`
	InferredType string                 `json:"inferredType"`
	Tool         string                 `json:"tool,omitempty"`
	State        map[string]interface{} `json:"state,omitempty"`
}

// SSEPartUpdatedData part_updated 事件的数据结构
type SSEPartUpdatedData struct {
	PartID string                 `json:"partID"`
	Type   string                 `json:"type"`
	Tool   string                 `json:"tool,omitempty"`
	State  map[string]interface{} `json:"state,omitempty"`
	Text   string                 `json:"text,omitempty"`
	Time   *struct {
		Start int64 `json:"start"`
		End   int64 `json:"end,omitempty"`
	} `json:"time,omitempty"`
}

// SubscribeGlobalEvents 订阅 OpenCode 全局事件流
// OpenCode /global/event 可能是 NDJSON（每行一个 JSON）或 SSE（data: {...}）格式
func (c *OpencodeClient) SubscribeGlobalEvents(ctx context.Context, targetSessionID string) (chan SSEEvent, error) {
	url := fmt.Sprintf("%s/global/event", c.Endpoint)

	// 使用一个长的超时或没有超时的客户端
	var httpClient *http.Client
	if c.Client != nil {
		httpClient = c.Client
	} else {
		httpClient = &http.Client{}
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("subscribe event failed: %d, body: %s", resp.StatusCode, string(body))
	}

	zap.L().Info("sse subscription started",
		zap.String("url", url),
		zap.String("target_session_id", targetSessionID),
		zap.String("content_type", resp.Header.Get("Content-Type")),
		zap.String("transfer_encoding", resp.Header.Get("Transfer-Encoding")),
	)

	// 增大 buffer 防止事件丢失
	eventChan := make(chan SSEEvent, 1000)
	// partID -> PartMeta 映射缓存（内存中）
	partTypeCache := make(map[string]*PartMeta)

	go func() {
		defer close(eventChan)
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		lineCount := 0
		matchCount := 0
		skipCount := 0
		parseErrCount := 0
		var dataBuffer strings.Builder

		for {
			select {
			case <-ctx.Done():
				zap.L().Info("sse context cancelled",
					zap.String("session", targetSessionID),
					zap.Int("lines_read", lineCount),
					zap.Int("matched", matchCount),
					zap.Int("skipped", skipCount),
					zap.Int("parse_errors", parseErrCount),
					zap.Int("part_type_cache_size", len(partTypeCache)))
				return
			default:
				line, err := reader.ReadString('\n')
				if err != nil {
					if err != io.EOF {
						zap.L().Error("sse read error", zap.Error(err), zap.String("session", targetSessionID))
					} else {
						zap.L().Info("sse EOF",
							zap.String("session", targetSessionID),
							zap.Int("lines_read", lineCount),
							zap.Int("matched", matchCount),
							zap.Int("skipped", skipCount),
							zap.Int("parse_errors", parseErrCount),
							zap.Int("part_type_cache_size", len(partTypeCache)))
					}
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					if dataBuffer.Len() > 0 {
						processNDJSONLine(dataBuffer.String(), targetSessionID, eventChan, &matchCount, &skipCount, &parseErrCount, partTypeCache)
						dataBuffer.Reset()
					}
					continue
				}
				lineCount++

				if strings.HasPrefix(line, "data:") {
					dataBuffer.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				} else {
					processNDJSONLine(line, targetSessionID, eventChan, &matchCount, &skipCount, &parseErrCount, partTypeCache)
				}
			}
		}
	}()

	return eventChan, nil
}

func processNDJSONLine(line, targetSessionID string, eventChan chan SSEEvent, matchCount, skipCount, parseErrCount *int, partTypeCache map[string]*PartMeta) {
	if line == "" {
		return
	}

	var evt OpenCodeGlobalEvent
	if err := json.Unmarshal([]byte(line), &evt); err != nil {
		var rawMap map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(line), &rawMap); jsonErr == nil {
			zap.L().Warn("ndjson line parsed but payload structure mismatch",
				zap.Any("keys", getTopLevelKeys(rawMap)),
				zap.Error(err))
		} else {
			truncated := line
			if len(truncated) > 200 {
				truncated = truncated[:200] + "..."
			}
			zap.L().Warn("invalid ndjson line", zap.String("line", truncated), zap.Error(err))
		}
		*parseErrCount++
		return
	}

	// ---- 提取事件类型 ----
	evtType := evt.Payload.Type

	// ---- 提取 sessionID（优先 properties.sessionID，其次 properties.part.sessionID） ----
	var sessID string
	sessID = strings.TrimSpace(evt.Payload.Properties.SessionID)
	if sessID == "" && evt.Payload.Properties.Part != nil {
		sessID = strings.TrimSpace(evt.Payload.Properties.Part.SessionID)
	}
	targetID := strings.TrimSpace(targetSessionID)

	if *matchCount+*skipCount < 10 || (*matchCount+*skipCount)%100 == 0 {
		zap.L().Debug("sse event received",
			zap.String("type", evtType),
			zap.String("event_session_id", sessID),
			zap.String("target_session_id", targetID),
			zap.Bool("match", sessID == targetID),
		)
	}

	// ---- 处理 message.completed 特殊事件（可能无 sessionID） ----
	isCompletionEvent := evtType == "message.completed" || strings.HasPrefix(evtType, "message.completed") ||
		evtType == "done" || evtType == "finished" || evtType == "message.finished" ||
		strings.HasPrefix(evtType, "message.done")
	if isCompletionEvent && sessID == "" {
		zap.L().Info("completion event with empty sessionID, skipping session filter", zap.String("type", evtType), zap.String("session", targetID))
		select {
		case eventChan <- SSEEvent{Event: "finish", Data: "done"}:
		case <-time.After(5 * time.Second):
			zap.L().Warn("sse channel blocked, dropping finish event", zap.String("session", targetID))
		}
		return
	}

	// 只处理目标 session 的事件
	if sessID != targetID {
		if *skipCount < 5 {
			zap.L().Debug("sse event skipped (session mismatch)",
				zap.String("expected", targetID),
				zap.String("got", sessID),
				zap.String("type", evtType))
		}
		*skipCount++
		return
	}
	*matchCount++

	zap.L().Debug("sse event matched",
		zap.String("type", evtType),
		zap.String("session", sessID),
		zap.Int("match_count", *matchCount),
		zap.Int("cache_size", len(partTypeCache)))

	// ---- 处理 message.part.updated：提取完整的 part 元数据（tool 类型核心来源） ----
	if evtType == "message.part.updated" && evt.Payload.Properties.Part != nil {
		part := evt.Payload.Properties.Part
		if part.ID != "" && part.Type != "" {
			meta := &PartMeta{
				Type:      part.Type,
				Tool:      part.Tool,
				State:     part.State,
				MessageID: part.MessageID,
				Time:      part.Time,
			}
			partTypeCache[part.ID] = meta
			zap.L().Debug("part type cached",
				zap.String("partID", part.ID),
				zap.String("inferredType", part.Type),
				zap.String("tool", part.Tool),
				zap.String("messageID", part.MessageID))

			// 向前端发送 part_updated 事件，让前端可以立即渲染工具卡片
			data := SSEPartUpdatedData{
				PartID: part.ID,
				Type:   part.Type,
				Tool:   part.Tool,
				State:  part.State,
				Text:   part.Text,
				Time:   part.Time,
			}
			jsonData, _ := json.Marshal(data)
			select {
			case eventChan <- SSEEvent{Event: "part_updated", Data: string(jsonData)}:
			case <-time.After(5 * time.Second):
				zap.L().Warn("sse channel blocked, dropping part_updated event", zap.String("session", sessID))
			}
		}
		return
	}

	// ---- 处理 delta 事件：查询 PartMeta 并发送 JSON ----
	if evtType == "message.part.delta" {
		partID := evt.Payload.Properties.PartID
		delta := evt.Payload.Properties.Delta
		inferredType := "unknown"
		var toolName string
		var state map[string]interface{}
		if meta, ok := partTypeCache[partID]; ok && meta != nil {
			inferredType = meta.Type
			toolName = meta.Tool
			state = meta.State
		}
		if len(delta) > 50 {
			zap.L().Debug("sse delta",
				zap.String("session", sessID),
				zap.String("partID", partID),
				zap.String("inferredType", inferredType),
				zap.String("delta", delta[:50]+"..."))
		} else {
			zap.L().Debug("sse delta",
				zap.String("session", sessID),
				zap.String("partID", partID),
				zap.String("inferredType", inferredType),
				zap.String("delta", delta))
		}
		data := SSEDeltaData{
			Delta:        delta,
			PartID:       partID,
			InferredType: inferredType,
			Tool:         toolName,
			State:        state,
		}
		jsonData, _ := json.Marshal(data)
		select {
		case eventChan <- SSEEvent{Event: "delta", Data: string(jsonData)}:
		case <-time.After(5 * time.Second):
			zap.L().Warn("sse channel blocked, dropping delta event", zap.String("session", sessID))
		}
		return
	}

	// ---- 处理消息完成事件 ----
	if isCompletionEvent {
		zap.L().Info("sse completion event", zap.String("type", evtType), zap.String("session", sessID))
		select {
		case eventChan <- SSEEvent{Event: "finish", Data: "done"}:
		case <-time.After(5 * time.Second):
			zap.L().Warn("sse channel blocked, dropping finish event", zap.String("session", sessID))
		}
		return
	}

	// ---- tool.start / tool.completed ----
	if evtType == "tool.start" || evtType == "tool.completed" {
		select {
		case eventChan <- SSEEvent{Event: "tool_start", Data: evtType}:
		case <-time.After(5 * time.Second):
			zap.L().Warn("sse channel blocked, dropping tool event", zap.String("session", sessID))
		}
		return
	}

	zap.L().Debug("sse other event", zap.String("type", evtType), zap.String("session", sessID))
}

func getTopLevelKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// GetSessionMessages 调用 OpenCode /session/{id}/message 接口获取对话记录
// 当 task 状态为 running 时，用于实时查看 AI 处理过程中的对话内容
func (c *OpencodeClient) GetSessionMessages(sessionID string) ([]OpenCodeMessage, error) {
	url := fmt.Sprintf("%s/session/%s/message", c.Endpoint, sessionID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		zap.L().Error("get session messages failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("get session messages failed: %d", resp.StatusCode)
	}

	var result []OpenCodeMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode session messages failed: %w", err)
	}

	zap.L().Info("session messages fetched", zap.String("session_id", sessionID), zap.Int("count", len(result)))
	return result, nil
}

func (c *OpencodeClient) DeleteSession(sessionID string) (bool, error) {
	url := fmt.Sprintf("%s/session/%s", c.Endpoint, sessionID)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return false, err
	}
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		zap.L().Error("delete session failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return false, fmt.Errorf("delete session failed: %d", resp.StatusCode)
	}
	return true, nil
}

// ShellWithSession 在指定session中执行shell命令
func (c *OpencodeClient) ShellWithSession(sessionID string, command string) (string, error) {
	url := fmt.Sprintf("%s/session/%s/shell", c.Endpoint, sessionID)
	body := map[string]string{
		"command": command,
		"agent":   "general",
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	result, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("shell failed: %d, %s", resp.StatusCode, string(result))
	}
	return string(result), nil
}

// Shell 与 ShellWithSession 相同
func (c *OpencodeClient) Shell(command string) (string, error) {
	url := fmt.Sprintf("%s/shell", c.Endpoint)
	body := map[string]string{
		"command": command,
		"agent":   "general",
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	result, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("shell failed: %d, %s", resp.StatusCode, string(result))
	}
	return string(result), nil
}

// GetSessionStatus 获取session状态
func (c *OpencodeClient) GetSessionStatus(sessionID string) (string, error) {
	url := fmt.Sprintf("%s/session/%s", c.Endpoint, sessionID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	c.setAuth(req)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get session status failed: %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	status, ok := result["status"].(string)
	if !ok {
		return "", fmt.Errorf("invalid session status response")
	}
	return status, nil
}

// OpencodeService 业务包装层
type OpencodeService struct{}

func NewOpencodeService() *OpencodeService {
	return &OpencodeService{}
}

func (s *OpencodeService) ExecuteTask(poolID uint, projectPath, prompt string) (string, error) {
	var pool model.ResourcePool
	if err := model.DB.First(&pool, poolID).Error; err != nil {
		return "", err
	}

	password, _ := encrypt.Decrypt(pool.OpencodePassword)

	client := NewOpencodeClientWithLongTimeout(pool.OpencodeEndpoint, pool.OpencodeUsername, password)

	session, err := client.CreateSession("AI Code Review")
	if err != nil {
		zap.L().Error("create opencode session failed", zap.Error(err))
		return "", err
	}

	zap.L().Info("opencode session created", zap.String("session_id", session.ID))

	_, err = client.SendPrompt(session.ID, prompt)
	if err != nil {
		zap.L().Error("send prompt to opencode failed", zap.Error(err))
		client.AbortSession(session.ID)
		return "", err
	}

	return session.ID, nil
}

func (s *OpencodeService) AbortTask(poolID uint, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	var pool model.ResourcePool
	if err := model.DB.First(&pool, poolID).Error; err != nil {
		return err
	}

	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = NewOpencodeClientWithLongTimeout(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}
	_, err := client.AbortSession(sessionID)
	return err
}

func (s *OpencodeService) DeleteSession(poolID uint, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	var pool model.ResourcePool
	if err := model.DB.First(&pool, poolID).Error; err != nil {
		return err
	}

	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}
	_, err := client.DeleteSession(sessionID)
	return err
}

func (s *OpencodeService) GetSessionStatus(poolID uint, sessionID string) (string, error) {
	var pool model.ResourcePool
	if err := model.DB.First(&pool, poolID).Error; err != nil {
		return "", err
	}

	password, _ := encrypt.Decrypt(pool.OpencodePassword)

	client := NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	return client.GetSessionStatus(sessionID)
}

func (s *OpencodeService) ExecuteTaskWithSession(taskID, poolID uint, projectPath, projectName, prompt, baseDir, accessToken string) (string, string, error) {
	var pool model.ResourcePool
	if err := model.DB.First(&pool, poolID).Error; err != nil {
		return "", "", err
	}

	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	zap.L().Info("pool loaded",
		zap.String("endpoint", pool.OpencodeEndpoint),
		zap.String("username", pool.OpencodeUsername),
		zap.String("password_len", fmt.Sprintf("%d", len(pool.OpencodePassword))),
		zap.String("decrypted_len", fmt.Sprintf("%d", len(password))),
		zap.String("apikey_len", fmt.Sprintf("%d", len(pool.OpencodeAPIKey))))

	// 获取系统配置的超时时间
	var sysConfig model.SystemConfig
	timeoutMin := 30
	if err := model.DB.First(&sysConfig).Error; err == nil && sysConfig.TaskTimeoutMin > 0 {
		timeoutMin = sysConfig.TaskTimeoutMin
	}
	zap.L().Info("using task timeout", zap.Int("timeout_min", timeoutMin))

	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKeyAndTimeout(pool.OpencodeEndpoint, pool.OpencodeAPIKey, timeoutMin)
	} else {
		client = NewOpencodeClientWithTaskTimeout(pool.OpencodeEndpoint, pool.OpencodeUsername, password, timeoutMin)
	}

	if err := client.Login(); err != nil {
		zap.L().Error("opencode login failed", zap.Error(err))
		return "", "", fmt.Errorf("opencode login failed: %w", err)
	}

	zap.L().Info("step 1: creating initial opencode session")
	session, err := client.CreateSession("AI Code Review: " + projectName)
	if err != nil {
		zap.L().Error("create initial opencode session failed", zap.Error(err))
		return "", "", err
	}
	zap.L().Info("initial session created", zap.String("session_id", session.ID))

	zap.L().Info("step 2: creating project directory")
	projectDir := baseDir + projectName
	shellCmd := fmt.Sprintf("mkdir -p %s", projectDir)
	zap.L().Info("executing shell", zap.String("session_id", session.ID), zap.String("command", shellCmd))
	_, err = client.ShellWithSession(session.ID, shellCmd)
	if err != nil {
		zap.L().Error("create project directory failed", zap.Error(err))
		client.AbortSession(session.ID)
		return "", "", fmt.Errorf("create project directory failed: %w", err)
	}
	zap.L().Info("project directory created", zap.String("project_dir", projectDir))

	zap.L().Info("step 3: deleting initial session")
	if _, err := client.DeleteSession(session.ID); err != nil {
		zap.L().Error("delete initial session failed", zap.Error(err))
		client.AbortSession(session.ID)
		return "", "", err
	}
	zap.L().Info("initial session deleted")

	zap.L().Info("step 4: creating new session with X-Opencode-Directory header")
	newSession, err := client.CreateSessionWithDirectory("AI Code Review: "+projectName, projectDir)
	if err != nil {
		zap.L().Error("create session with directory failed", zap.Error(err))
		return "", "", err
	}
	zap.L().Info("new session created with directory", zap.String("session_id", newSession.ID), zap.String("directory", projectDir))

	now := time.Now()
	model.DB.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"started_at":          now,
		"opencode_session_id": newSession.ID,
	})
	zap.L().Info("task started_at and session_id updated", zap.Uint("task_id", taskID), zap.Time("started_at", now), zap.String("session_id", newSession.ID))

	// 获取 task 信息
	var task model.Task
	model.DB.Preload("Project").Preload("Project.Template").First(&task, taskID)

	zap.L().Info("system config loaded for branch generation")

	// 根据 task_type 和 trigger_source 选择模板
	var template string
	if task.TaskType == "bugfix" && task.Project.TemplateID > 0 && task.Project.Template.ID > 0 {
		template = task.Project.Template.Prompt
		zap.L().Info("using project template for bugfix", zap.Uint("template_id", task.Project.TemplateID), zap.String("template_name", task.Project.Template.Name))
	} else if task.TriggerSource == "score_threshold" {
		// 评分阈值触发使用代码审查模版
		template = sysConfig.ReviewTemplate
		zap.L().Info("using review template for score threshold", zap.String("trigger_source", task.TriggerSource))
		if template == "" {
			// 若代码审查模版未配置，回退到 AI 对话模版
			template = sysConfig.AILogTemplate
			zap.L().Warn("review template empty, fallback to ai_log_template")
		}
		if template == "" {
			template = "请先执行以下命令拉取代码：\ngit clone {{CLONE_URL}}\n\n{{USER_INPUT}}\n\n请审查以上代码变更，给出审查意见。"
		}
	} else {
		template = sysConfig.AILogTemplate
		if template == "" {
			template = "请先执行以下命令拉取代码：\ngit clone {{CLONE_URL}}\n\n{{USER_INPUT}}\n\n请审查以上代码变更，给出审查意见。"
		}
	}

	// 准备变量值
	cloneURL := projectPath
	if accessToken != "" {
		if !strings.Contains(projectPath, "://") {
			cloneURL = "https://" + projectPath
		}
		if !strings.Contains(cloneURL, "@") {
			parts := strings.SplitN(cloneURL, "://", 2)
			cloneURL = parts[0] + "://oauth2:" + accessToken + "@" + parts[1]
		}
	}

	// 替换变量
	fullPrompt := strings.ReplaceAll(template, "{{CLONE_URL}}", cloneURL)
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{USER_INPUT}}", prompt)

	// 生成 AI 分支名
	aiBranch := "AI-" + generateRandomString(4)

	zap.L().Debug("generated ai branch name", zap.String("ai_branch", aiBranch))

	// 从 GitLab API 获取 MR diff
	mrDiff := ""
	if task.MRMergeID > 0 && task.Project.ProjectPath != "" {
		mrDiff = fetchMRDiff(task.Project.ProjectPath, accessToken, task.MRMergeID)
	}
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{MR_DIFF}}", mrDiff)
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{MR_AUTHOR}}", task.MRAuthor)
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{SRC_BRANCH}}", task.SourceBranch)
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{DEST_BRANCH}}", task.TargetBranch)
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{AI_BRANCH}}", aiBranch)

	zap.L().Info("step 5: sending prompt to execute task", zap.String("session_id", newSession.ID), zap.String("prompt_len", fmt.Sprintf("%d", len(fullPrompt))))
	resp, err := client.SendPromptWithDirectory(newSession.ID, fullPrompt, projectDir)
	if err != nil {
		zap.L().Error("send prompt to opencode failed", zap.Error(err))
		client.AbortSession(newSession.ID)
		return "", "", err
	}

	aiResponse := extractTextFromResponse(resp)
	aiResponse = cleanThoughtTags(aiResponse)

	// 任务完成后不再删除 session，保留用于查看交互记录
	// zap.L().Info("step 6: deleting task session", zap.String("session_id", newSession.ID))
	// if _, err := client.DeleteSession(newSession.ID); err != nil {
	//  zap.L().Error("delete task session failed", zap.Error(err))
	// } else {
	//  zap.L().Info("task session deleted")
	// }

	zap.L().Info("task completed", zap.String("session_id", newSession.ID), zap.String("response_len", fmt.Sprintf("%d", len(aiResponse))))

	return newSession.ID, aiResponse, nil
}

func extractTextFromResponse(resp *MessageResponse) string {
	// 若倒数第二个 part 是 reasoning, 直接取该 part 的 text
	if len(resp.Parts) >= 2 {
		if penultimate := resp.Parts[len(resp.Parts)-2]; penultimate.Type == "reasoning" {
			return penultimate.Text
		}
	}
	// 其他所有情况保持原有逻辑: 拼接所有 text part
	var sb strings.Builder
	for _, part := range resp.Parts {
		if part.Type == "text" && part.Text != "" {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

func cleanThoughtTags(text string) string {
	re := regexp.MustCompile(`(?s)<think>.*?</think>`)
	text = re.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func extractProtocolAndHost(projectPath string) (string, string) {
	protocol := "https"
	if strings.HasPrefix(projectPath, "http://") {
		protocol = "http"
		projectPath = strings.TrimPrefix(projectPath, "http://")
	} else if strings.HasPrefix(projectPath, "https://") {
		projectPath = strings.TrimPrefix(projectPath, "https://")
	}
	return protocol, projectPath
}

func fetchMRDiff(projectPath, gitlabToken string, mrIID int) string {
	protocol, path := extractProtocolAndHost(projectPath)
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		zap.L().Warn("invalid project path for MR diff", zap.String("project_path", projectPath))
		return ""
	}
	namespaceProject := parts[1]
	namespaceProject = strings.TrimSuffix(namespaceProject, ".git")

	url := fmt.Sprintf("%s://%s/api/v4/projects/%s/merge_requests/%d/changes?access_raw_diffs=true",
		protocol, parts[0], strings.ReplaceAll(namespaceProject, "/", "%2F"), mrIID)

	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: tr}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		zap.L().Warn("create MR diff request failed", zap.Error(err))
		return ""
	}
	req.Header.Set("PRIVATE-TOKEN", gitlabToken)

	resp, err := client.Do(req)
	if err != nil {
		zap.L().Warn("fetch MR diff failed", zap.Error(err))
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		zap.L().Warn("fetch MR diff status not 200", zap.Int("status", resp.StatusCode))
		return ""
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		zap.L().Warn("decode MR diff failed", zap.Error(err))
		return ""
	}

	changes, ok := data["changes"].([]interface{})
	if !ok || len(changes) == 0 {
		zap.L().Debug("no changes in MR")
		return ""
	}

	var diffBuilder strings.Builder
	for _, change := range changes {
		c, ok := change.(map[string]interface{})
		if !ok {
			continue
		}
		if diff, ok := c["diff"].(string); ok && diff != "" {
			diffBuilder.WriteString(diff)
			diffBuilder.WriteString("\n")
		}
	}

	result := diffBuilder.String()
	zap.L().Info("MR diff fetched", zap.Int("mr_iid", mrIID), zap.String("diff_len", fmt.Sprintf("%d", len(result))))
	return result
}

func generateRandomString(length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// CheckConnectivity 检查 OpenCode 服务连通性，返回 (连通状态, 错误信息, 版本号, go错误)
func (s *OpencodeService) CheckConnectivity(id uint) (bool, string, string, error) {
	var pool model.ResourcePool
	if err := model.DB.First(&pool, id).Error; err != nil {
		return false, err.Error(), "", err
	}

	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}

	// 直接请求 /global/health
	testURL := fmt.Sprintf("%s/global/health", client.Endpoint)
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return false, err.Error(), "", nil
	}
	client.setAuth(req)

	// 禁用 Keep-Alive + 设置超时，防止高频健康检查导致端口耗尽
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}
	httpClient := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err.Error(), "", nil
	}
	// 必须读完 Body 再关闭，否则连接无法回收
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 401 {
		return false, "auth failed: invalid credentials", "", nil
	}
	if resp.StatusCode != 200 {
		return false, fmt.Sprintf("health check failed with status %d", resp.StatusCode), "", nil
	}

	var healthResp struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(bodyBytes, &healthResp); err == nil {
		return true, "", healthResp.Version, nil
	}
	// 解析失败也认为是连通，只是版本号拿不到
	return true, "", "", nil
}

// TestConnectivityByConfig 使用配置直接测试连通性（不查数据库）
func (s *OpencodeService) TestConnectivityByConfig(endpoint, username, password string) (bool, string, error) {
	client := NewOpencodeClient(endpoint, username, password)
	if err := client.Login(); err != nil {
		return false, err.Error(), nil
	}
	return true, "", nil
}

// Skill 代表 OpenCode 技能
// swagger:model Skill
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Content     string `json:"content"`
}

// GetSkills 获取指定资源池的 OpenCode 技能列表
func (s *OpencodeService) GetSkills(poolID uint) ([]Skill, error) {
	var pool model.ResourcePool
	if err := model.DB.First(&pool, poolID).Error; err != nil {
		return nil, err
	}

	password, _ := encrypt.Decrypt(pool.OpencodePassword)
	if password == "" && pool.OpencodePassword != "" {
		password = pool.OpencodePassword
	}

	var client *OpencodeClient
	if pool.OpencodeAPIKey != "" {
		client = NewOpencodeClientWithAPIKey(pool.OpencodeEndpoint, pool.OpencodeAPIKey)
	} else {
		client = NewOpencodeClient(pool.OpencodeEndpoint, pool.OpencodeUsername, password)
	}

	url := fmt.Sprintf("%s/skill", pool.OpencodeEndpoint)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	client.setAuth(req)

	resp, err := client.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("opencode returned %d: %s", resp.StatusCode, string(body))
	}

	var result []Skill
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}
