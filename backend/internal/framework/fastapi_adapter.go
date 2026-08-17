package framework

import (
	"fmt"
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
		// 优先从结构化 Annotations 提取（不再依赖 BodySnippet）
		method, path := a.extractRouteInfo(fn)
		if method == "" {
			continue
		}

		node := &graph.MemorySymbolNode{
			ID:       fmt.Sprintf("endpoint:%s:%s:%s", ast.FilePath, method, path),
			Type:     graph.NodeEndpoint,
			Name:     path,
			Language: ast.Language,
			File:     ast.FilePath,
			Location: fn.Location,
			Properties: map[string]interface{}{
				"method":    strings.ToUpper(method),
				"framework": "fastapi",
				"handler":   fn.Name,
			},
		}
		g.AddNode(node)
	}
}

// extractRouteInfo 从 Annotations 中提取 HTTP 方法和路由路径
func (a *FastAPIAdapter) extractRouteInfo(fn parser.UnifiedFunction) (method, path string) {
	for _, annot := range fn.Annotations {
		// FastAPI 常见路由装饰器：@app.get("/path")、@router.post("/path")
		for _, m := range []string{"get", "post", "put", "delete", "patch", "head", "options"} {
			if strings.Contains(annot, "."+m+"(") {
				path = extractFirstStringArg(annot)
				if path != "" {
					return m, path
				}
			}
		}
		// FastAPI 通用 api_route
		if strings.Contains(annot, ".api_route(") {
			path = extractFirstStringArg(annot)
			if path != "" {
				return "any", path
			}
		}
	}
	return "", ""
}

// extractFirstStringArg 从装饰器文本中提取第一个字符串参数的值
func extractFirstStringArg(s string) string {
	start := strings.Index(s, "(")
	if start == -1 {
		return ""
	}
	rest := s[start+1:]
	rest = strings.TrimLeft(rest, " \t\n")
	// 找到第一个双引号或单引号
	q := ""
	if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, `'`) {
		q = rest[:1]
	}
	if q == "" {
		return ""
	}
	end := strings.Index(rest[1:], q)
	if end == -1 {
		return ""
	}
	return rest[1 : end+1]
}
