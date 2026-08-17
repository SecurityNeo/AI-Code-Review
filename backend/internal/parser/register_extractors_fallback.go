//go:build !cgo

package parser

func init() {
	// 非 CGO 环境回退到正则/标准库提取器
	GoExtractorInit()
	JavaExtractorInit()
	PythonExtractorInit()
	JavaScriptExtractorInit()
}
