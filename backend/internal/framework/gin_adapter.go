package framework

import (
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// GinAdapter Gin框架适配器
type GinAdapter struct {
	routeMethods []string
}

// NewGinAdapter 创建Gin适配器
func NewGinAdapter() *GinAdapter {
	return &GinAdapter{
		routeMethods: []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"},
	}
}

// Name 返回框架名
func (a *GinAdapter) Name() string { return "gin" }

// Language 返回语言
func (a *GinAdapter) Language() string { return "golang" }

// Detect 检测是否使用Gin
func (a *GinAdapter) Detect(ast *parser.UnifiedAST) bool {
	if ast.Language != a.Language() {
		return false
	}
	return hasImport(ast, "github.com/gin-gonic/gin")
}

// Enrich 提取Gin特有的端点、认证、Sink等
func (a *GinAdapter) Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	// 1. 从结构化 CallSites + VarBindings 推导完整路由（彻底去 BodySnippet 化）
	a.enrichFromCallSites(ast, g)
	// 2. 识别认证中间件使用
	a.enrichAuthMiddleware(ast, g)
	// 3. 危险Sink
	a.enrichSinks(ast, g)
}

// enrichFromCallSites 从结构化调用点 + 变量绑定推导端点
func (a *GinAdapter) enrichFromCallSites(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	groupMap := buildGroupMap(ast.VarBindings)
	methodSet := make(map[string]bool)
	for _, m := range a.routeMethods {
		methodSet[m] = true
	}

	for _, cs := range ast.CallSites {
		if !methodSet[cs.TargetFunc] {
			continue
		}
		if len(cs.Arguments) == 0 {
			continue
		}
		path := extractStringLiteral(cs.Arguments[0])
		prefix := ""
		if cs.ReceiverVar != "" {
			prefix = resolveVarPrefix(groupMap, cs.ReceiverVar)
		}
		fullPath := prefix + path
		if fullPath == "" {
			continue
		}

		node := &graph.MemorySymbolNode{
			ID:       fmt.Sprintf("endpoint:%s:%s:%s", ast.FilePath, cs.TargetFunc, fullPath),
			Type:     graph.NodeEndpoint,
			Name:     fullPath,
			Language: ast.Language,
			File:     ast.FilePath,
			Location: cs.Location,
			Properties: map[string]interface{}{
				"method":    cs.TargetFunc,
				"framework": "gin",
			},
		}
		if len(cs.Arguments) >= 2 {
			handler := strings.TrimSpace(cs.Arguments[1])
			if handler != "" {
				node.Properties["handler"] = handler
			}
		}
		g.AddNode(node)
	}
}

// buildGroupMap 从变量绑定中构建 Group 前缀映射，支持链式分组
func buildGroupMap(bindings []parser.UnifiedVarBinding) map[string]string {
	groupBindings := make(map[string]*parser.UnifiedVarBinding)
	for i := range bindings {
		b := &bindings[i]
		if b.CallSite != nil && b.CallSite.TargetFunc == "Group" && len(b.CallSite.Arguments) > 0 {
			groupBindings[b.VarName] = b
		}
	}

	cache := make(map[string]string)
	var resolve func(string) string
	resolve = func(varName string) string {
		if varName == "" || varName == "r" || varName == "router" || varName == "engine" {
			return ""
		}
		if prefix, ok := cache[varName]; ok {
			return prefix
		}
		b, ok := groupBindings[varName]
		if !ok {
			cache[varName] = ""
			return ""
		}
		ownPrefix := extractStringLiteral(b.CallSite.Arguments[0])
		parentPrefix := resolve(b.CallSite.ReceiverVar)
		full := parentPrefix + ownPrefix
		cache[varName] = full
		return full
	}

	for name := range groupBindings {
		_ = resolve(name)
	}
	return cache
}

// resolveVarPrefix 解析变量的路由前缀
func resolveVarPrefix(groupMap map[string]string, varName string) string {
	if varName == "" || varName == "r" || varName == "router" || varName == "engine" {
		return ""
	}
	if prefix, ok := groupMap[varName]; ok {
		return prefix
	}
	return ""
}

// enrichAuthMiddleware 识别认证中间件与限流配置
func (a *GinAdapter) enrichAuthMiddleware(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	for _, fn := range ast.Functions {
		funcID := fmt.Sprintf("func:%s:%d", ast.FilePath, fn.Location.LineStart)
		authRequired := false
		// 检测认证中间件
		authPatterns := []string{
			"Use(middleware.Auth", "Use(middleware.JWT", "Use(middleware.OAuth",
			"Use(jwt", "Use(auth", "jwt.Parse", "jwt.Validate",
		}
		for _, pat := range authPatterns {
			if strings.Contains(fn.BodySnippet, pat) {
				authRequired = true
				break
			}
		}
		// 在函数节点上标记
		if n, ok := g.GetNode(funcID); ok {
			n.Properties["auth_middleware"] = authRequired
		}

		// 在 endpoint 节点上标记（endpoint ID 基于 handler_func，需要匹配）
		// 简化策略：遍历此文件的所有 endpoint 节点，若 endpoint 的 handler 在当前函数体内被注册，标记认证
		for _, node := range g.AllNodes() {
			if node.Type == graph.NodeEndpoint && node.File == ast.FilePath {
				handler, _ := node.Properties["handler"].(string)
				if handler != "" && strings.Contains(fn.BodySnippet, handler) {
					node.Properties["auth_required"] = authRequired
					if authRequired {
						g.AddNode(node) // 更新节点
					}
				}
			}
		}
	}

	// 检测限流配置（基于全局变量或中间件注册）
	rateLimitPatterns := []string{
		"Use(middleware.RateLimit", "Use(ratelimit", "RateLimit(",
		"throttle", "limiter",
	}
	for _, fn := range ast.Functions {
		for _, pat := range rateLimitPatterns {
			if strings.Contains(fn.BodySnippet, pat) {
				// 找到此文件的所有 endpoint 标记限流
				for _, node := range g.AllNodes() {
					if node.Type == graph.NodeEndpoint && node.File == ast.FilePath {
						node.Properties["rate_limit"] = "enabled"
						g.AddNode(node)
					}
				}
				break
			}
		}
	}
}

// enrichSinks 识别危险Sink
func (a *GinAdapter) enrichSinks(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	sinkPatterns := []struct {
		funcName string
		kind     string
	}{
		{"exec.Command", "command_injection"},
		{"Query", "sql_injection"},
		{"Exec", "sql_injection"},
		{"db.Query", "sql_injection"},
		{"db.Exec", "sql_injection"},
		{"fmt.Sprintf", "format_string"},
		{"os.WriteFile", "path_traversal"},
	}
	for _, fn := range ast.Functions {
		for _, p := range sinkPatterns {
			if strings.Contains(fn.BodySnippet, p.funcName) {
				node := &graph.MemorySymbolNode{
					ID:       fmt.Sprintf("sink:%s:%s:%d", ast.FilePath, p.funcName, fn.Location.LineStart),
					Type:     graph.NodeSink,
					Name:     p.funcName,
					Language: ast.Language,
					File:     ast.FilePath,
					Location: fn.Location,
					Properties: map[string]interface{}{
						"sink_kind": p.kind,
					},
				}
				g.AddNode(node)
			}
		}
	}
}
