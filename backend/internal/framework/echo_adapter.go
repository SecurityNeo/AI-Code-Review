package framework

import (
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// EchoAdapter Echo框架适配器
type EchoAdapter struct{}

// NewEchoAdapter 创建Echo适配器
func NewEchoAdapter() *EchoAdapter { return &EchoAdapter{} }

// Name 返回框架名
func (a *EchoAdapter) Name() string { return "echo" }

// Language 返回语言
func (a *EchoAdapter) Language() string { return "golang" }

// Detect 检测是否使用Echo
func (a *EchoAdapter) Detect(ast *parser.UnifiedAST) bool {
	return hasImport(ast, "github.com/labstack/echo")
}

// Enrich 提取Echo端点
func (a *EchoAdapter) Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	for _, fn := range ast.Functions {
		snippet := fn.BodySnippet
		if snippet == "" {
			snippet = fn.Name
		}
		// Echo路由检测 e.GET("/path", handler)
		for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
			if strings.Contains(snippet, "e."+method+"(") || strings.Contains(snippet, "group."+method+"(") {
				node := &graph.MemorySymbolNode{
					ID:       "endpoint:" + ast.FilePath + ":" + method + ":" + fn.Name,
					Type:     graph.NodeEndpoint,
					Name:     "/" + fn.Name,
					Language: ast.Language,
					File:     ast.FilePath,
					Location: fn.Location,
					Properties: map[string]interface{}{
						"method":    method,
						"framework": "echo",
					},
				}
				g.AddNode(node)
				break
			}
		}
	}
}
