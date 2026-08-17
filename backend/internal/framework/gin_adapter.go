package framework

import (
	"fmt"
	"regexp"
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
	// 1. 优先从结构化 CallSites 提取（不受 BodySnippet 截断影响）
	found := a.enrichFromCallSites(ast, g)
	// 2. fallback：BodySnippet 正则兜底
	if !found {
		a.enrichFromBodySnippet(ast, g)
	}

	// 3. 识别认证中间件使用
	a.enrichAuthMiddleware(ast, g)
	// 4. 危险Sink
	a.enrichSinks(ast, g)
}

// enrichFromCallSites 从结构化调用点提取端点
func (a *GinAdapter) enrichFromCallSites(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) bool {
	methodSet := make(map[string]bool)
	for _, m := range a.routeMethods {
		methodSet[m] = true
	}
	found := false
	for _, cs := range ast.CallSites {
		if !methodSet[cs.TargetFunc] {
			continue
		}
		if len(cs.Arguments) == 0 {
			continue
		}
		path := extractStringLiteral(cs.Arguments[0])
		if path == "" {
			continue
		}
		var handler string
		if len(cs.Arguments) >= 2 {
			handler = strings.TrimSpace(cs.Arguments[1])
		}
		node := &graph.MemorySymbolNode{
			ID:       fmt.Sprintf("endpoint:%s:%s:%s", ast.FilePath, cs.TargetFunc, path),
			Type:     graph.NodeEndpoint,
			Name:     path,
			Language: ast.Language,
			File:     ast.FilePath,
			Location: cs.Location,
			Properties: map[string]interface{}{
				"method":    cs.TargetFunc,
				"framework": "gin",
				"handler":   handler,
			},
		}
		g.AddNode(node)
		found = true
	}
	return found
}

// enrichFromBodySnippet 基于 BodySnippet 正则提取（兜底）
func (a *GinAdapter) enrichFromBodySnippet(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	for _, fn := range ast.Functions {
		snippet := fn.BodySnippet
		if snippet == "" {
			continue
		}
		for _, method := range a.routeMethods {
			re := regexp.MustCompile(fmt.Sprintf(`[^\w]%s\s*\(`, method))
			if re.MatchString(snippet) {
				pathRe := regexp.MustCompile(fmt.Sprintf(`%s\s*\(\s*["']([^"']+)["']`, method))
				matches := pathRe.FindAllStringSubmatch(snippet, -1)
				for _, m := range matches {
					path := m[1]
					node := &graph.MemorySymbolNode{
						ID:       fmt.Sprintf("endpoint:%s:%s:%s", ast.FilePath, method, path),
						Type:     graph.NodeEndpoint,
						Name:     path,
						Language: ast.Language,
						File:     ast.FilePath,
						Location: fn.Location,
						Properties: map[string]interface{}{
							"method":    method,
							"framework": "gin",
						},
					}
					if h := extractHandlerName(snippet, method); h != "" {
						node.Properties["handler"] = h
					}
					g.AddNode(node)
				}
			}
		}
	}
}

// enrichAuthMiddleware 识别认证中间件
func (a *GinAdapter) enrichAuthMiddleware(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	for _, fn := range ast.Functions {
		if strings.Contains(fn.BodySnippet, "Use(middleware.Auth") ||
			strings.Contains(fn.BodySnippet, "Use(middleware.JWT") {
			if n, ok := g.GetNode(fmt.Sprintf("func:%s:%d", ast.FilePath, fn.Location.LineStart)); ok {
				n.Properties["auth_middleware"] = true
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

func extractHandlerName(snippet, method string) string {
	re := regexp.MustCompile(fmt.Sprintf(`%s\s*\([^)]*\,\s*(\w+)\s*\)`, method))
	m := re.FindStringSubmatch(snippet)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func extractStringLiteral(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		return s[1 : len(s)-1]
	}
	if strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") {
		return s[1 : len(s)-1]
	}
	return ""
}
