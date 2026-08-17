package framework

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// SpringAdapter Spring Boot适配器
type SpringAdapter struct{}

// NewSpringAdapter 创建Spring适配器
func NewSpringAdapter() *SpringAdapter { return &SpringAdapter{} }

// Name 返回框架名
func (a *SpringAdapter) Name() string { return "spring" }

// Language 返回语言
func (a *SpringAdapter) Language() string { return "java" }

// Detect 检测是否使用Spring
func (a *SpringAdapter) Detect(ast *parser.UnifiedAST) bool {
	for _, imp := range ast.Imports {
		if strings.Contains(imp.Path, "org.springframework") {
			return true
		}
	}
	return false
}

// Enrich 提取Spring端点、认证等
func (a *SpringAdapter) Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) {
	for _, fn := range ast.Functions {
		// 识别Controller方法
		if hasAnnotation(fn.Annotations, "@RequestMapping", "@GetMapping", "@PostMapping", "@PutMapping", "@DeleteMapping") {
			method := "GET"
			path := "/"
			for _, annot := range fn.Annotations {
				if strings.HasPrefix(annot, "@GetMapping") {
					method = "GET"
					path = extractMappingPath(annot)
				} else if strings.HasPrefix(annot, "@PostMapping") {
					method = "POST"
					path = extractMappingPath(annot)
				} else if strings.HasPrefix(annot, "@PutMapping") {
					method = "PUT"
					path = extractMappingPath(annot)
				} else if strings.HasPrefix(annot, "@DeleteMapping") {
					method = "DELETE"
					path = extractMappingPath(annot)
				} else if strings.HasPrefix(annot, "@RequestMapping") {
					method = extractRequestMethod(annot)
					path = extractMappingPath(annot)
				}
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
					"handler":   fn.Name,
					"framework": "spring",
					"auth":      hasAnnotation(fn.Annotations, "@PreAuthorize", "@Secured"),
				},
			}
			g.AddNode(node)
		}
	}
}

func hasAnnotation(annotations []string, prefixes ...string) bool {
	for _, a := range annotations {
		for _, p := range prefixes {
			if strings.HasPrefix(a, p) {
				return true
			}
		}
	}
	return false
}

func extractMappingPath(annot string) string {
	re := regexp.MustCompile(`\(\s*["']([^"']+)["']`)
	m := re.FindStringSubmatch(annot)
	if len(m) > 1 {
		return m[1]
	}
	return "/"
}

func extractRequestMethod(annot string) string {
	if strings.Contains(annot, `method = RequestMethod.POST`) {
		return "POST"
	}
	if strings.Contains(annot, `method = RequestMethod.PUT`) {
		return "PUT"
	}
	if strings.Contains(annot, `method = RequestMethod.DELETE`) {
		return "DELETE"
	}
	return "GET"
}
