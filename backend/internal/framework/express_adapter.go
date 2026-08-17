package framework

import (
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// ExpressAdapter Express/Koa适配器
type ExpressAdapter struct{}

// NewExpressAdapter 创建Express适配器
func NewExpressAdapter() *ExpressAdapter { return &ExpressAdapter{} }

// Name 返回框架名
func (a *ExpressAdapter) Name() string { return "express" }

// Language 返回语言
func (a *ExpressAdapter) Language() string { return "javascript" }

// Detect 检测是否使用Express
func (a *ExpressAdapter) Detect(ast *parser.UnifiedAST) bool {
	for _, imp := range ast.Imports {
		if imp.Path == "express" || strings.Contains(imp.Path, "express") {
			return true
		}
	}
	return false
}

// Enrich 提取Express端点
func (a *ExpressAdapter) Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	methods := []string{"get", "post", "put", "delete", "patch"}
	methodSet := make(map[string]bool)
	for _, m := range methods {
		methodSet[m] = true
	}

	// 优先从结构化 CallSites 提取
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
			ID:       fmt.Sprintf("endpoint:%s:%s", ast.FilePath, path),
			Type:     graph.NodeEndpoint,
			Name:     path,
			Language: ast.Language,
			File:     ast.FilePath,
			Location: cs.Location,
			Properties: map[string]interface{}{
				"method":    strings.ToUpper(cs.TargetFunc),
				"framework": "express",
				"handler":   handler,
			},
		}
		g.AddNode(node)
		found = true
	}
	if found {
		return
	}

	// fallback：BodySnippet 兜底
	for _, fn := range ast.Functions {
		snippet := fn.BodySnippet
		if snippet == "" {
			snippet = fn.Name
		}
		for _, method := range methods {
			if strings.Contains(snippet, fmt.Sprintf("app.%s(", method)) ||
				strings.Contains(snippet, fmt.Sprintf("router.%s(", method)) {
				node := &graph.MemorySymbolNode{
					ID:       fmt.Sprintf("endpoint:%s:%s", ast.FilePath, fn.Name),
					Type:     graph.NodeEndpoint,
					Name:     fmt.Sprintf("/%s", fn.Name),
					Language: ast.Language,
					File:     ast.FilePath,
					Location: fn.Location,
					Properties: map[string]interface{}{
						"method":    strings.ToUpper(method),
						"framework": "express",
					},
				}
				g.AddNode(node)
				break
			}
		}
	}
}
