package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// MCPRequest JSON-RPC 请求
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse JSON-RPC 响应
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

// MCPError MCP 错误
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ToolAnnotations MCP 1.0 工具标注（可选，旧客户端忽略）
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`           // 人类可读标题
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`    // 只读操作
	DestructiveHint bool   `json:"destructiveHint,omitempty"` // 破坏性操作
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`  // 幂等操作
	OpenWorldHint   bool   `json:"openWorldHint,omitempty"`   // 可能影响外部系统
}

// Tool 定义
type Tool struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema json.RawMessage  `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"` // MCP 1.0 新增（旧客户端忽略）
}

// ToolHandler Tool 处理函数签名
type ToolHandler func(ctx context.Context, authCtx *AuthContext, args map[string]interface{}) (interface{}, error)

// MCP 标准 Error Code 常量（MCP 1.0 规范）
const (
	ErrParseError     = -32700 // Parse error
	ErrInvalidRequest = -32600 // Invalid Request
	ErrMethodNotFound = -32601 // Method not found
	ErrInvalidParams  = -32602 // Invalid params
	ErrInternalError  = -32603 // Internal error
	// 自定义 Error Code（范围 -32000 ~ -32099，不冲突）
	ErrServerNotInitialized = -32002
	ErrUnauthorized         = -32001
	ErrRateLimit            = -32000
)

// mcpSession MCP Session（StreamableHTTP 传输模式）
type mcpSession struct {
	authCtx    *AuthContext
	createdAt  time.Time
	lastUsedAt time.Time
}

// Server MCP Server
type Server struct {
	tools     map[string]Tool
	handlers  map[string]ToolHandler
	limiter   *RateLimiter
	logChan   chan *model.MCPCallLog
	sessions  map[string]*mcpSession
	sessionMu sync.RWMutex
	sseHub    *mcpSSEHub // SSE 广播中心
}

// NewServer 创建 MCP Server
func NewServer() *Server {
	s := &Server{
		tools:    make(map[string]Tool),
		handlers: make(map[string]ToolHandler),
		limiter:  NewRateLimiter(100, time.Minute),
		logChan:  make(chan *model.MCPCallLog, 100),
		sessions: make(map[string]*mcpSession),
		sseHub:   newMcpSSEHub(),
	}
	go s.logWorker()
	return s
}

// RegisterTool 注册 Tool（支持可选 annotations）
// 注册完成后通知所有 SSE 客户端工具列表已变更
func (s *Server) RegisterTool(name, description string, schema json.RawMessage, handler ToolHandler, annotations ...*ToolAnnotations) {
	tool := Tool{Name: name, Description: description, InputSchema: schema}
	if len(annotations) > 0 && annotations[0] != nil {
		tool.Annotations = annotations[0]
	}
	s.tools[name] = tool
	s.handlers[name] = handler
	// 通知所有已连接的 SSE 客户端工具列表已变更
	s.sseHub.NotifyToolsListChanged()
}

// HandleMCP 处理 MCP POST 请求（JSON-RPC）
func (s *Server) HandleMCP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	// 解析请求
	var req MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondMCPError(w, nil, ErrParseError, "Parse error")
		return
	}

	// 获取认证上下文
	authCtx := GetAuthContext(r.Context())
	if authCtx == nil {
		respondMCPError(w, req.ID, ErrUnauthorized, "Unauthorized")
		return
	}

	// 限流检查（按 API Key）
	if !s.limiter.Allow(fmt.Sprintf("key_%d", authCtx.APIKey.ID)) {
		respondMCPError(w, req.ID, ErrRateLimit, "Rate limit exceeded")
		return
	}

	// 处理不同 Method
	var result interface{}
	var err error
	isNotification := false

	switch req.Method {
	case "initialize":
		// 解析 initialize 参数获取客户端请求的 protocolVersion
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &initParams)

		// 协商 protocolVersion
		// 如果客户端请求 2025-03-26，返回 1.0 版本
		// 否则返回 2024-11-05 兼容旧客户端
		protocolVersion := "2024-11-05"
		if initParams.ProtocolVersion == "2025-03-26" {
			protocolVersion = "2025-03-26"
		} else if initParams.ProtocolVersion != "" {
			// 未知版本降级到 2025-03-26
			protocolVersion = "2025-03-26"
		}

		result = map[string]interface{}{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]interface{}{
				// 旧版 capabilities（兼容旧客户端）
				"tools": map[string]interface{}{
					"listChanged": true,
				},
				// MCP 1.0 新增 capabilities（旧客户端忽略）
				// 注：logging 暂不提供（无 SSE 端点推送日志）
				"prompts": map[string]interface{}{
					"listChanged": false,
				},
				"resources": map[string]interface{}{
					"subscribe":   false,
					"listChanged": false,
				},
			},
			"serverInfo": map[string]string{
				"name":    "codeguard-mcp",
				"version": "1.0.0",
			},
		}

		// 创建 session 并在响应头中返回 session ID（StreamableHTTP 要求）
		sessionID := s.createSession(authCtx)
		w.Header().Set("Mcp-Session-Id", sessionID)

	case "tools/list":
		result = s.listTools()
	case "tools/call":
		// 从 Header 读取 IM 认证信息
		imProvider := r.Header.Get("X-IM-Provider")
		imUserID := r.Header.Get("X-IM-User-ID")
		var fullAuthCtx *AuthContext
		var toolName string
		result, toolName, fullAuthCtx, err = s.callTool(r.Context(), authCtx, req.Params, imProvider, imUserID)
		if fullAuthCtx != nil {
			authCtx = fullAuthCtx
		}
		if req.Method != "ping" && !strings.HasPrefix(req.Method, "notifications/") {
			duration := time.Since(start).Milliseconds()
			go s.recordLog(authCtx, req.Method, toolName, req.Params, err, int(duration))
		}
	case "ping":
		// MCP keepalive ping，返回空对象
		result = map[string]interface{}{}
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		// MCP 通知方法，无需处理，静默成功
		isNotification = true

	// MCP 1.0 新增方法：resources / prompts（当前降级返回空列表或不支持）
	case "resources/list":
		result = map[string]interface{}{"resources": []interface{}{}}
	case "resources/read":
		err = fmt.Errorf("resource not found")
	case "resources/templates/list":
		result = map[string]interface{}{"resourceTemplates": []interface{}{}}
	case "prompts/list":
		result = map[string]interface{}{"prompts": []interface{}{}}
	case "prompts/get":
		err = fmt.Errorf("prompt not found")
	case "completion/complete":
		result = map[string]interface{}{"completion": map[string]interface{}{"values": []string{}, "total": 0, "hasMore": false}}
	case "sampling/createMessage":
		// 客户端代理采样：CodeGuard 当前不支持
		err = fmt.Errorf("sampling is not supported by this server")

	default:
		err = fmt.Errorf("unknown method: %s", req.Method)
	}

	if err != nil {
		code := ErrInvalidParams
		if strings.HasPrefix(err.Error(), "unknown method") {
			code = ErrMethodNotFound
		} else if strings.HasPrefix(err.Error(), "sampling is not supported") || strings.HasPrefix(err.Error(), "prompt not found") {
			code = ErrInvalidRequest
		}
		respondMCPError(w, req.ID, code, err.Error())
		return
	}

	// 通知方法不需要返回响应体
	if isNotification {
		w.WriteHeader(http.StatusOK)
		return
	}

	respondJSON(w, http.StatusOK, MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

// HandleSSE 处理 MCP SSE 长连接（MCP 1.0 StreamableHTTP + SSE）
func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// 从 Header 或 Query 获取 Session ID
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = r.URL.Query().Get("session_id")
	}
	if sessionID == "" {
		sessionID = generateSessionID()
	}

	ch := s.sseHub.register(sessionID)
	defer s.sseHub.unregister(sessionID)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	for {
		select {
		case msg := <-ch:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ":keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// listTools 列出所有可用 Tools（tools/list 不根据 Scope 过滤，让智能体发现全部能力）
func (s *Server) listTools() map[string]interface{} {
	var names []string
	for name := range s.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var available []Tool
	for _, name := range names {
		available = append(available, s.tools[name])
	}
	return map[string]interface{}{
		"tools": available,
	}
}

// ToolCount 返回已注册的 Tool 总数（诊断工具使用）
func (s *Server) ToolCount() int {
	return len(s.tools)
}

// ToolNames 返回已注册的 Tool 名称列表（诊断工具使用）
func (s *Server) ToolNames() []string {
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	return names
}

// callTool 调用 Tool
// Phase 1 双轨运行：
//   - 绑定模式（authCtx.User != nil）：直接使用 Key 绑定的用户，忽略 IM Header
//   - 共享模式（authCtx.User == nil）：从 X-IM-Provider + X-IM-User-ID Header 读取用户身份（旧 Key 兼容）
func (s *Server) callTool(ctx context.Context, baseAuthCtx *AuthContext, params json.RawMessage, imProvider, imUserID string) (interface{}, string, *AuthContext, error) {
	if baseAuthCtx == nil || baseAuthCtx.APIKey == nil {
		return nil, "", nil, fmt.Errorf("invalid authentication context")
	}

	var call struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, "", nil, fmt.Errorf("invalid tool call params: %w", err)
	}

	var fullAuthCtx *AuthContext

	if baseAuthCtx.User != nil {
		// -- 绑定模式：AuthMiddleware 已加载绑定用户，直接使用 --
		fullAuthCtx = baseAuthCtx
	} else {
		// -- 共享模式（旧 Key 兼容）：从 Header 中补充身份认证信息 --
		if imProvider == "" || imUserID == "" {
			return nil, call.Name, nil, fmt.Errorf("缺少认证 Header 'X-IM-Provider' 和 'X-IM-User-ID'，请在请求头中提供 IM 供应商和用户ID")
		}
		if imProvider != IMPlatformWeCom {
			return nil, call.Name, nil, fmt.Errorf("不支持的 IM 供应商 '%s'，当前仅支持 '%s'", imProvider, IMPlatformWeCom)
		}

		var user model.User
		if err := model.DB.Where("im_platform = ? AND im_user_id = ? AND enabled = ?",
			imProvider, imUserID, true).First(&user).Error; err != nil {
			return nil, call.Name, nil, fmt.Errorf("用户未绑定，请联系管理员在用户管理中绑定（平台: %s, 用户: %s）", imProvider, imUserID)
		}

		fullAuthCtx = &AuthContext{
			APIKey:     baseAuthCtx.APIKey,
			User:       &user,
			UserID:     user.ID,
			IMUserID:   imUserID,
			IMProvider: imProvider,
			Scopes:     baseAuthCtx.Scopes,
			IsAdmin:    user.Role == "admin",
			ClientIP:   baseAuthCtx.ClientIP,
		}
	}

	handler, ok := s.handlers[call.Name]
	if !ok {
		return nil, call.Name, fullAuthCtx, fmt.Errorf("tool not found: %s", call.Name)
	}

	// 权限检查
	if !s.hasToolPermission(fullAuthCtx, call.Name) {
		return nil, call.Name, fullAuthCtx, fmt.Errorf("permission denied for tool: %s", call.Name)
	}

	// 直接使用 arguments，无需再剥离身份字段（已从 Header 或绑定模式获取）
	result, err := handler(ctx, fullAuthCtx, call.Arguments)
	if err != nil {
		return nil, call.Name, fullAuthCtx, err
	}
	return map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": toJSONString(result),
			},
		},
	}, call.Name, fullAuthCtx, nil
}

// hasToolPermission 检查用户是否有权限使用 Tool
func (s *Server) hasToolPermission(authCtx *AuthContext, toolName string) bool {
	// admin:* 为超级管理员权限，可调用所有 Tool
	if authCtx.HasScope("admin:*") {
		return true
	}

	// 根据 Tool 名称映射到 Scope
	scopeMap := map[string]string{
		"list_tasks":               "tasks:read",
		"get_task":                 "tasks:read",
		"get_task_pipeline":        "tasks:read",
		"get_mr_review":            "tasks:read",
		"get_issue_detail":         "issues:read",
		"list_pending_issues":      "issues:read",
		"list_historical_issues":   "issues:read",
		"resolve_issue":            "issues:write",
		"reject_issue":             "issues:write",
		"ignore_issue":             "issues:write",
		"retry_task":               "tasks:write",
		"stop_task":                "tasks:write",
		"list_merge_requests":      "merge_requests:read",
		"get_merge_request_detail": "merge_requests:read",
		"get_workbench":            "workbench:read",
		"get_dashboard_stats":      "dashboard:read",
		"get_token_usage":          "dashboard:read",
		"list_projects":            "projects:read",
		"list_notifications":       "notifications:read",
		"mark_all_read":            "notifications:write",
	}

	requiredScope, ok := scopeMap[toolName]
	if !ok {
		return false
	}

	return authCtx.HasScope(requiredScope)
}

// recordLog 异步记录调用日志
func (s *Server) recordLog(authCtx *AuthContext, method, toolName string, params json.RawMessage, err error, durationMs int) {
	if authCtx == nil || authCtx.APIKey == nil {
		return
	}
	status := "success"
	statusCode := 200
	errMsg := ""
	if err != nil {
		status = "error"
		statusCode = 500
		errMsg = err.Error()
	}
	log := &model.MCPCallLog{
		APIKeyID:     authCtx.APIKey.ID,
		APIKeyName:   authCtx.APIKey.Name,
		IMUserID:     authCtx.IMUserID,
		IMProvider:   authCtx.IMProvider,
		UserID:       authCtx.UserID,
		Method:       method,
		ToolName:     toolName,
		Params:       string(params),
		Status:       status,
		StatusCode:   statusCode,
		ErrorMsg:     errMsg,
		DurationMs:   int64(durationMs),
	}
	select {
	case s.logChan <- log:
	default:
		zap.L().Warn("MCP call log channel full, dropping log")
	}
}

// logWorker 异步批量写入日志
func (s *Server) logWorker() {
	batch := make([]*model.MCPCallLog, 0, 50)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := model.DB.CreateInBatches(batch, 50).Error; err != nil {
			zap.L().Error("failed to write MCP call logs batch", zap.Error(err), zap.Int("batch_size", len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case log := <-s.logChan:
			batch = append(batch, log)
			if len(batch) >= 50 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// ---------------------- Session 管理 ----------------------

// createSession 创建 MCP Session
func (s *Server) createSession(authCtx *AuthContext) string {
	id := generateSessionID()
	s.sessionMu.Lock()
	s.sessions[id] = &mcpSession{
		authCtx:    authCtx,
		createdAt:  time.Now(),
		lastUsedAt: time.Now(),
	}
	s.sessionMu.Unlock()
	return id
}

// getSession 获取并验证 Session（自动更新最后访问时间）
func (s *Server) getSession(id string) *mcpSession {
	s.sessionMu.RLock()
	sess, ok := s.sessions[id]
	s.sessionMu.RUnlock()

	if !ok {
		return nil
	}

	// Session 有效期 24 小时（基于最后访问时间的滑动窗口）
	if time.Since(sess.lastUsedAt) > 24*time.Hour {
		s.deleteSession(id)
		return nil
	}

	// 更新最后访问时间
	s.sessionMu.Lock()
	sess.lastUsedAt = time.Now()
	s.sessionMu.Unlock()

	return sess
}

// deleteSession 删除 Session
func (s *Server) deleteSession(id string) {
	s.sessionMu.Lock()
	delete(s.sessions, id)
	s.sessionMu.Unlock()
}

// ---------------------- SSE Hub ----------------------

// mcpSSEHub SSE 广播中心
type mcpSSEHub struct {
		mu      sync.RWMutex
		clients map[string]chan string // sessionID -> SSE write channel
	}

func newMcpSSEHub() *mcpSSEHub {
	return &mcpSSEHub{
		clients: make(map[string]chan string),
	}
}

func (h *mcpSSEHub) register(sessionID string) chan string {
	ch := make(chan string, 10)
	h.mu.Lock()
	h.clients[sessionID] = ch
	h.mu.Unlock()
	return ch
}

func (h *mcpSSEHub) unregister(sessionID string) {
	h.mu.Lock()
	delete(h.clients, sessionID)
	h.mu.Unlock()
}

// broadcast 广播消息给所有 SSE 客户端
func (h *mcpSSEHub) broadcast(msg string) {
	h.mu.RLock()
	clients := make([]chan string, 0, len(h.clients))
	for _, ch := range h.clients {
		clients = append(clients, ch)
	}
	h.mu.RUnlock()

	for _, ch := range clients {
		select {
		case ch <- msg:
		default: // channel full, drop silently
		}
	}
}

// NotifyToolsListChanged 通知所有客户端工具列表已变更
func (h *mcpSSEHub) NotifyToolsListChanged() {
	h.broadcast(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
}

// generateSessionId 生成随机 Session ID
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 降级：使用时间戳+随机数
		return fmt.Sprintf("sess-%d-%d", time.Now().UnixNano(), time.Now().Unix())
	}
	return hex.EncodeToString(b)
}

// AuthMiddleware MCP 双层认证中间件（支持 Session 认证）
func (s *Server) AuthMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 0. 首先尝试 Session 认证（StreamableHTTP 模式）
			sessionID := r.Header.Get("Mcp-Session-Id")
			if sessionID != "" {
				if sess := s.getSession(sessionID); sess != nil {
					// Session 有效，直接使用 session 中的认证上下文
					r = r.WithContext(WithAuthContext(r.Context(), sess.authCtx))
					next.ServeHTTP(w, r)
					return
				}
				// Session 无效或过期，继续尝试 API Key 认证（降级兼容）
			}

			// 1. API Key 认证（通道身份）
			apiKey := r.Header.Get(HeaderAPIKey)
			if apiKey == "" {
				respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少 API Key")
				return
			}

			// 提取 key 前缀用于缩小查询范围（格式：sk-cg-xxxx-xxxxxxxx...）
			keyPrefix := extractKeyPrefix(apiKey)

			// 先按前缀查询活跃 key，减少 bcrypt 比较次数
			var keys []model.MCPAPIKey
			query := model.DB.Where("status = ?", "active")
			if keyPrefix != "" {
				query = query.Where("prefix = ?", keyPrefix)
			}
			if err := query.Find(&keys).Error; err != nil {
				respondError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "查询密钥失败")
				return
			}

			// 验证 Key（bcrypt 比较）
			var matchedKey *model.MCPAPIKey
			for i := range keys {
				if checkAPIKey(apiKey, keys[i].KeyHash) {
					matchedKey = &keys[i]
					break
				}
			}

			if matchedKey == nil {
				respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API Key 无效或已禁用")
				return
			}

			// 检查过期
			if matchedKey.ExpiresAt != nil && matchedKey.ExpiresAt.Before(time.Now()) {
				respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API Key 已过期")
				return
			}

			// 检查 IP 白名单
			if matchedKey.IPWhitelist != "" {
				clientIP := getClientIP(r)
				if !isIPAllowed(clientIP, matchedKey.IPWhitelist) {
					respondError(w, http.StatusForbidden, "FORBIDDEN", "当前 IP 不在白名单内")
					return
				}
			}

			// 解析 Scopes
			var scopes []string
			if matchedKey.Scopes != "" {
				_ = json.Unmarshal([]byte(matchedKey.Scopes), &scopes)
			}

			// 更新最后使用时间
			now := time.Now()
			matchedKey.LastUsedAt = &now
			model.DB.Save(matchedKey)

			clientIP := getClientIP(r)
			var authCtx *AuthContext

			if matchedKey.UserID > 0 {
				// -- 绑定模式（新 Key）-- 直接加载绑定用户，无需 IM Header
				var user model.User
				if err := model.DB.Where("id = ? AND enabled = ?", matchedKey.UserID, true).First(&user).Error; err == nil {
					authCtx = &AuthContext{
						APIKey:     matchedKey,
						User:       &user,
						UserID:     user.ID,
						IMUserID:   user.IMUserID,
						IMProvider: user.IMPlatform,
						Scopes:     scopes,
						IsAdmin:    user.Role == "admin",
						ClientIP:   clientIP,
					}
				} else {
					// 绑定用户已删除或被禁用：降级为共享模式，留待 tools/call 时从 Header 补充身份
					authCtx = &AuthContext{
						APIKey:   matchedKey,
						Scopes:   scopes,
						ClientIP: clientIP,
					}
					zap.L().Warn("MCP API Key bound user not found or disabled, fallback to shared mode",
						zap.Uint("key_id", matchedKey.ID),
						zap.Uint("user_id", matchedKey.UserID))
				}
			} else {
				// -- 共享模式（旧 Key 兼容）-- 仅通道认证，用户身份在 tools/call 时从 Header 获取
				authCtx = &AuthContext{
					APIKey:   matchedKey,
					Scopes:   scopes,
					ClientIP: clientIP,
				}
			}

			// 将认证上下文放入 request context
			r = r.WithContext(WithAuthContext(r.Context(), authCtx))
			next.ServeHTTP(w, r)
		})
	}
}

// toJSONString 将对象转为 JSON 字符串
func toJSONString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// respondJSON 返回 JSON 响应
func respondJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

// respondMCPError 返回 MCP JSON-RPC 错误
func respondMCPError(w http.ResponseWriter, id interface{}, code int, message string) {
	respondJSON(w, http.StatusOK, MCPResponse{ // MCP 错误仍返回 HTTP 200
		JSONRPC: "2.0",
		ID:      id,
		Error: &MCPError{
			Code:    code,
			Message: message,
		},
	})
}
