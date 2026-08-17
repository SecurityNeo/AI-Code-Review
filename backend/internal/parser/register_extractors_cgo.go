//go:build cgo

package parser

func init() {
	// Tree-sitter 版本作为主要提取器（CGO 环境）
	GlobalRegistry.Register(NewTreeSitterGoExtractor())
	GlobalRegistry.Register(NewTreeSitterJavaExtractor())
	GlobalRegistry.Register(NewTreeSitterPythonExtractor())
	GlobalRegistry.Register(NewTreeSitterJavaScriptExtractor())
	GlobalRegistry.Register(NewTreeSitterTypeScriptExtractor())
}
