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
	for _, fn := range ast.Functions {
		// 识别路由注册
		for _, method := range a.routeMethods {
			re := regexp.MustCompile(fmt.Sprintf(`[^\w]%s\s*\(`, method))
			if re.MatchString(fn.BodySnippet) {
				// 提取路径 - 简化处理
				pathRe := regexp.MustCompile(fmt.Sprintf(`%s\s*\(\s*["']([^"']+)["']`, method))
				matches := pathRe.FindAllStringSubmatch(fn.BodySnippet, -1)
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
					// 尝试提取handler
					if h := extractHandlerName(fn.BodySnippet, method); h != "" {
						node.Properties["handler"] = h
					}
					g.AddNode(node)
				}
			}
		}

		// 识别认证中间件使用
		if strings.Contains(fn.BodySnippet, "Use(middleware.Auth") ||
			strings.Contains(fn.BodySnippet, "Use(middleware.JWT") {
			if n, ok := g.GetNode(fmt.Sprintf("func:%s:%d", ast.FilePath, fn.Location.LineStart)); ok {
				n.Properties["auth_middleware"] = true
			}
		}
	}

	// 识别危险Sink：exec.Command, sql注入等
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
	// 简化：尝试从路由注册中提取handler函数名
	// 如 r.GET("/path", handlerFunc)
	re := regexp.MustCompile(fmt.Sprintf(`%s\s*\([^)]*\,\s*(\w+)\s*\)`, method))
	m := re.FindStringSubmatch(snippet)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}
