package framework

import (
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// FlaskAdapter Flask适配器
type FlaskAdapter struct{}

// NewFlaskAdapter 创建Flask适配器
func NewFlaskAdapter() *FlaskAdapter { return &FlaskAdapter{} }

// Name 返回框架名
func (a *FlaskAdapter) Name() string { return "flask" }

// Language 返回语言
func (a *FlaskAdapter) Language() string { return "python" }

// Detect 检测是否使用Flask
func (a *FlaskAdapter) Detect(ast *parser.UnifiedAST) bool {
	for _, imp := range ast.Imports {
		if imp.Path == "flask" {
			return true
		}
	}
	return false
}

// Enrich 提取Flask端点
func (a *FlaskAdapter) Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
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
				"method":    method,
				"framework": "flask",
				"handler":   fn.Name,
			},
		}
		g.AddNode(node)
	}
}

// extractRouteInfo 从 Annotations 中提取 HTTP 方法和路由路径
func (a *FlaskAdapter) extractRouteInfo(fn parser.UnifiedFunction) (method, path string) {
	for _, annot := range fn.Annotations {
		// Flask 通用 route：@app.route('/path', methods=['GET'])
		if strings.Contains(annot, ".route(") {
			path = extractFirstStringArg(annot)
			if path == "" {
				continue
			}
			// 尝试提取 methods 参数
			method = a.extractMethodsParam(annot)
			if method == "" {
				method = "ANY"
			}
			return method, path
		}
		// Flask 2.0+ 快捷装饰器：@app.get('/path') 等
		for _, m := range []string{"get", "post", "put", "delete", "patch", "head", "options"} {
			if strings.Contains(annot, "."+m+"(") {
				path = extractFirstStringArg(annot)
				if path != "" {
					return strings.ToUpper(m), path
				}
			}
		}
	}
	return "", ""
}

// extractMethodsParam 从 @app.route(..., methods=[...]) 中提取 HTTP 方法
func (a *FlaskAdapter) extractMethodsParam(annot string) string {
	idx := strings.Index(annot, "methods=")
	if idx == -1 {
		idx = strings.Index(annot, "methods =")
		if idx == -1 {
			return ""
		}
		idx += len("methods =")
	} else {
		idx += len("methods=")
	}
	rest := annot[idx:]
	// 找到方括号 [ ... ]
	lb := strings.Index(rest, "[")
	rb := strings.Index(rest, "]")
	if lb == -1 || rb == -1 || lb >= rb {
		return ""
	}
	list := rest[lb+1 : rb]
	var methods []string
	for _, token := range strings.Split(list, ",") {
		token = strings.TrimSpace(token)
		token = extractStringLiteral(token)
		upper := strings.ToUpper(token)
		switch upper {
		case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
			methods = append(methods, upper)
		}
	}
	return strings.Join(methods, ",")
}
