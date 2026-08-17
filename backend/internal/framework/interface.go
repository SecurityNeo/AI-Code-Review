package framework

import (
	"regexp"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// FrameworkAdapter 框架适配器接口
type FrameworkAdapter interface {
	Name() string                                              // 框架名称，如 "gin"
	Language() string                                          // 语言，如 "golang"
	Detect(ast *parser.UnifiedAST) bool                        // 检测是否使用该框架
	Enrich(ast *parser.UnifiedAST, g *graph.MemorySymbolGraph) // 提取Endpoint、Sink、Auth等
}

// AdapterRegistry 框架适配器注册中心
type AdapterRegistry struct {
	adapters []FrameworkAdapter
}

// NewAdapterRegistry 创建注册中心
func NewAdapterRegistry() *AdapterRegistry {
	reg := &AdapterRegistry{}
	// 注册默认适配器
	reg.Register(NewGinAdapter())
	reg.Register(NewEchoAdapter())
	reg.Register(NewSpringAdapter())
	reg.Register(NewFlaskAdapter())
	reg.Register(NewFastAPIAdapter())
	reg.Register(NewExpressAdapter())
	return reg
}

// Register 注册适配器
func (r *AdapterRegistry) Register(adapter FrameworkAdapter) {
	r.adapters = append(r.adapters, adapter)
}

// GetAdaptersForAST 获取适用于该AST的所有适配器
func (r *AdapterRegistry) GetAdaptersForAST(ast *parser.UnifiedAST) []FrameworkAdapter {
	var result []FrameworkAdapter
	for _, a := range r.adapters {
		if a.Language() == ast.Language && a.Detect(ast) {
			result = append(result, a)
		}
	}
	return result
}

// GlobalAdapterRegistry 全局注册中心
var GlobalAdapterRegistry = NewAdapterRegistry()

// hasImport 检查是否导入某包
func hasImport(ast *parser.UnifiedAST, pkg string) bool {
	for _, imp := range ast.Imports {
		if imp.Path == pkg || strings.HasSuffix(imp.Path, "/"+pkg) {
			return true
		}
	}
	return false
}

// matchRegex 检查是否有函数匹配正则
func matchRegex(ast *parser.UnifiedAST, re *regexp.Regexp) bool {
	for _, fn := range ast.Functions {
		if re.MatchString(fn.BodySnippet) {
			return true
		}
	}
	return false
}

// getReceiverType 获取receiver的类型名（去除指针）
func getReceiverType(receiver string) string {
	receiver = strings.TrimPrefix(receiver, "*")
	return receiver
}
