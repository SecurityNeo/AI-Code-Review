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
	for _, fn := range ast.Functions {
		snippet := fn.BodySnippet
		if snippet == "" {
			snippet = fn.Name
		}
		// Express路由检测
		for _, method := range []string{"get", "post", "put", "delete", "patch"} {
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
