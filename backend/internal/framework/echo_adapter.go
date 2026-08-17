package framework

import (
	"fmt"
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
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}
	methodSet := make(map[string]bool)
	for _, m := range methods {
		methodSet[m] = true
	}

	// 优先从结构化 CallSites 提取（不受 BodySnippet 截断影响）
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
				"framework": "echo",
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
