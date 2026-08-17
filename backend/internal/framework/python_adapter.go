package framework

import (
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// FlaskAdapter Flask/FastAPI适配器
type FlaskAdapter struct{}

// NewFlaskAdapter 创建Flask适配器
func NewFlaskAdapter() *FlaskAdapter { return &FlaskAdapter{} }

// Name 返回框架名
func (a *FlaskAdapter) Name() string { return "flask" }

// Language 返回语言
func (a *FlaskAdapter) Language() string { return "python" }

// Detect 检测是否使用Flask/FastAPI
func (a *FlaskAdapter) Detect(ast *parser.UnifiedAST) bool {
	for _, imp := range ast.Imports {
		if imp.Path == "flask" || imp.Path == "fastapi" {
			return true
		}
	}
	return false
}

// Enrich 提取Flask/FastAPI端点
func (a *FlaskAdapter) Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	for _, fn := range ast.Functions {
		snippet := fn.BodySnippet
		if snippet == "" {
			snippet = fn.Name
		}
		// Flask路由装饰器检测
		for range []string{"route", "get", "post", "put", "delete", "patch"} {
			decorator := "@route('path')"
			_ = decorator // 简化处理
			if strings.Contains(snippet, "@app.") || strings.Contains(snippet, "@router.") {
				node := &graph.MemorySymbolNode{
					ID:       fmt.Sprintf("endpoint:%s:%s", ast.FilePath, fn.Name),
					Type:     graph.NodeEndpoint,
					Name:     fmt.Sprintf("/%s", fn.Name),
					Language: ast.Language,
					File:     ast.FilePath,
					Location: fn.Location,
					Properties: map[string]interface{}{
						"method":    "ANY",
						"framework": "flask",
					},
				}
				g.AddNode(node)
				break
			}
		}
	}
}
