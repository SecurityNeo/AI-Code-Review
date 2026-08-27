package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

// Tool 定义
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolHandler Tool 处理函数签名
type ToolHandler func(ctx context.Context, authCtx *AuthContext, args map[string]interface{}) (interface{}, error)

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
}

// NewServer 创建 MCP Server
func NewServer() *Server {
	s := &Server{
		tools:    make(map[string]Tool),
		handlers: make(map[string]ToolHandler),
		limiter:  NewRateLimiter(100, time.Minute),
		logChan:  make(chan *model.MCPCallLog, 100),
		sessions: make(map[string]*mcpSession),
	}
	// 调用日志已禁用（logWorker 不再启动）
	// go s.logWorker()
	return s
}

// RegisterTool 注册 Tool
func (s *Server) RegisterTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	s.tools[name] = Tool{Name: name, Description: description, InputSchema: schema}
	s.handlers[name] = handler
}

// HandleMCP 处理 MCP POST 请求（JSON-RPC）
func (s *Server) HandleMCP(w http.ResponseWriter, r *http.Request) {
	// 解析请求
	var req MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondMCPError(w, nil, -32700, "Parse error")
		return
	}

	// 获取认证上下文
	authCtx := GetAuthContext(r.Context())
	if authCtx == nil {
		respondMCPError(w, req.ID, -32001, "Unauthorized")
		return
	}

	// 限流检查（按 API Key）
	if !s.limiter.Allow(fmt.Sprintf("key_%d", authCtx.APIKey.ID)) {
		respondMCPError(w, req.ID, -32002, "Rate limit exceeded")
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

		// 协商 protocolVersion：如果客户端请求的是我们支持的版本，返回客户端版本；否则返回我们的默认版本
		protocolVersion := initParams.ProtocolVersion
		if protocolVersion == "" {
			protocolVersion = "2024-11-05"
		}

		result = map[string]interface{}{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{
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
		var fullAuthCtx *AuthContext
		result, _, fullAuthCtx, err = s.callTool(r.Context(), authCtx, req.Params)
		if fullAuthCtx != nil {
			authCtx = fullAuthCtx
		}
	case "ping":
		// MCP keepalive ping，返回空对象
		result = map[string]interface{}{}
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		// MCP 通知方法，无需处理，静默成功
		isNotification = true
	default:
		err = fmt.Errorf("unknown method: %s", req.Method)
	}

	// 调用日志已禁用
	/*
	if req.Method != "ping" && !strings.HasPrefix(req.Method, "notifications/") {
		duration := time.Since(start).Milliseconds()
		go s.recordLog(authCtx, req.Method, toolName, req.Params, err, int(duration))
	}
	*/

	if err != nil {
		respondMCPError(w, req.ID, -32602, err.Error())
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
func (s *Server) callTool(ctx context.Context, baseAuthCtx *AuthContext, params json.RawMessage) (interface{}, string, *AuthContext, error) {
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

	// 1. 从 arguments 中提取身份认证信息
	imProvider, ok1 := call.Arguments["x_im_provider"].(string)
	imUserID, ok2 := call.Arguments["x_im_user_id"].(string)
	if !ok1 || imProvider == "" || !ok2 || imUserID == "" {
		return nil, call.Name, nil, fmt.Errorf("缺少必填参数 'x_im_provider' 和 'x_im_user_id'，请在 arguments 中提供 IM 供应商和用户ID")
	}
	if imProvider != IMPlatformWeCom {
		return nil, call.Name, nil, fmt.Errorf("不支持的 IM 供应商 '%s'，当前仅支持 '%s'", imProvider, IMPlatformWeCom)
	}

	// 2. 查询用户（统一身份源）
	var user model.User
	if err := model.DB.Where("im_platform = ? AND im_user_id = ? AND enabled = ?",
		imProvider, imUserID, true).First(&user).Error; err != nil {
		return nil, call.Name, nil, fmt.Errorf("用户未绑定，请联系管理员在用户管理中绑定（平台: %s, 用户: %s）", imProvider, imUserID)
	}

	// 3. 构造完整 AuthContext
	fullAuthCtx := &AuthContext{
		APIKey:     baseAuthCtx.APIKey,
		User:       &user,
		UserID:     user.ID,
		IMUserID:   imUserID,
		IMProvider: imProvider,
		Scopes:     baseAuthCtx.Scopes,
		IsAdmin:    user.Role == "admin",
		ClientIP:   baseAuthCtx.ClientIP,
	}

	handler, ok := s.handlers[call.Name]
	if !ok {
		return nil, call.Name, fullAuthCtx, fmt.Errorf("tool not found: %s", call.Name)
	}

	// 4. 权限检查
	if !s.hasToolPermission(fullAuthCtx, call.Name) {
		return nil, call.Name, fullAuthCtx, fmt.Errorf("permission denied for tool: %s", call.Name)
	}

	// 5. 从 arguments 中剥离身份字段，避免传递给业务 handler
	cleanArgs := make(map[string]interface{})
	for k, v := range call.Arguments {
		if k != "x_im_provider" && k != "x_im_user_id" {
			cleanArgs[k] = v
		}
	}

	result, err := handler(ctx, fullAuthCtx, cleanArgs)
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
	// 根据 Tool 名称映射到 Scope
	scopeMap := map[string]string{
		"list_tasks":                "tasks:read",
		"get_task":                  "tasks:read",
		"get_task_pipeline":         "tasks:read",
		"get_mr_review":             "tasks:read",
		"get_issue_detail":          "issues:read",
		"list_pending_issues":       "issues:read",
		"list_historical_issues":    "issues:read",
		"resolve_issue":             "issues:write",
		"reject_issue":              "issues:write",
		"ignore_issue":              "issues:write",
		"retry_task":                "tasks:write",
		"stop_task":                 "tasks:write",
		"list_merge_requests":       "merge_requests:read",
		"get_merge_request_detail":  "merge_requests:read",
		"get_workbench":             "workbench:read",
		"get_dashboard_stats":       "dashboard:read",
		"get_token_usage":           "dashboard:read",
		"list_projects":             "projects:read",
		"list_notifications":        "notifications:read",
		"mark_all_read":             "notifications:write",
	}

	requiredScope, ok := scopeMap[toolName]
	if !ok {
		return false
	}

	return authCtx.HasScope(requiredScope)
}

// recordLog 异步记录调用日志（已禁用）
func (s *Server) recordLog(authCtx *AuthContext, method, toolName string, params json.RawMessage, err error, durationMs int) {
	// 调用日志已禁用
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

	// Session 有效期 24 小时
	if time.Since(sess.createdAt) > 24*time.Hour {
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

			// 构造通道级认证上下文（不含用户身份，用户身份在 tools/call 时从 arguments 提取）
			authCtx := &AuthContext{
				APIKey:   matchedKey,
				Scopes:   scopes,
				ClientIP: getClientIP(r),
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
