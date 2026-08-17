package framework

import (
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// FastAPIAdapter FastAPI框架适配器
type FastAPIAdapter struct{}

// NewFastAPIAdapter 创建FastAPI适配器
func NewFastAPIAdapter() *FastAPIAdapter { return &FastAPIAdapter{} }

// Name 返回框架名
func (a *FastAPIAdapter) Name() string { return "fastapi" }

// Language 返回语言
func (a *FastAPIAdapter) Language() string { return "python" }

// Detect 检测是否使用FastAPI
func (a *FastAPIAdapter) Detect(ast *parser.UnifiedAST) bool {
	for _, imp := range ast.Imports {
		if imp.Path == "fastapi" || strings.HasPrefix(imp.Path, "fastapi.") {
			return true
		}
	}
	return false
}

// Enrich 提取FastAPI端点
func (a *FastAPIAdapter) Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	for _, fn := range ast.Functions {
		snippet := fn.BodySnippet
		if snippet == "" {
			snippet = fn.Name
		}
		// FastAPI路由装饰器 @app.get("/path")
		for _, method := range []string{"get", "post", "put", "delete", "patch"} {
			if strings.Contains(snippet, "@app."+method+"(") || strings.Contains(snippet, "@router."+method+"(") {
				node := &graph.MemorySymbolNode{
					ID:       "endpoint:" + ast.FilePath + ":" + method + ":" + fn.Name,
					Type:     graph.NodeEndpoint,
					Name:     "/" + fn.Name,
					Language: ast.Language,
					File:     ast.FilePath,
					Location: fn.Location,
					Properties: map[string]interface{}{
						"method":    strings.ToUpper(method),
						"framework": "fastapi",
					},
				}
				g.AddNode(node)
				break
			}
		}
	}
}
