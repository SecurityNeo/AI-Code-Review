package parser

import (
	"regexp"
	"strings"
)

// PythonExtractor Python语言提取器（简化版）
type PythonExtractor struct{}

// NewPythonExtractor 创建Python提取器
func NewPythonExtractor() *PythonExtractor { return &PythonExtractor{} }

// Language 返回语言
func (e *PythonExtractor) Language() string { return "python" }

// FileExts 返回文件扩展名
func (e *PythonExtractor) FileExts() []string { return []string{".py"} }

// ParseFile 解析Python文件
func (e *PythonExtractor) ParseFile(filePath string, content []byte) (*UnifiedAST, error) {
	src := string(content)
	result := &UnifiedAST{
		Language: "python",
		FilePath: filePath,
	}

	// 函数/方法匹配
	funcRe := regexp.MustCompile(`(?m)^\s*(?:async\s+)?def\s+(\w+)\s*\(([^)]*)\)`)
	funcMatches := funcRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range funcMatches {
		name := src[m[2]:m[3]]
		if name == "__init__" || name == "__main__" {
			continue
		}
		fn := UnifiedFunction{
			Name:     name,
			IsAsync:  strings.Contains(src[m[0]:m[2]], "async"),
			Location: calcLocation(src, m[0]),
		}
		fn.IsExported = !strings.HasPrefix(name, "_")
		// 解析参数
		paramsStr := src[m[4]:m[5]]
		fn.Params = e.parsePythonParams(paramsStr)
		result.Functions = append(result.Functions, fn)
	}

	// 类匹配
	classRe := regexp.MustCompile(`(?m)^\s*class\s+(\w+)(?:\(([^)]*)\))?:`)
	classMatches := classRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range classMatches {
		name := src[m[2]:m[3]]
		t := UnifiedType{
			Name:       name,
			Kind:       "class",
			IsExported: !strings.HasPrefix(name, "_"),
			Location:   calcLocation(src, m[0]),
		}
		// 提取继承
		if len(m) > 5 && m[4] > 0 && m[5] > 0 {
			bases := strings.Split(src[m[4]:m[5]], ",")
			for _, b := range bases {
				b = strings.TrimSpace(b)
				if b != "" && b != "object" {
					t.Extends = append(t.Extends, b)
				}
			}
		}
		result.Types = append(result.Types, t)
	}

	// Import匹配
	importRe := regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
	importMatches := importRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range importMatches {
		var path string
		if m[2] >= 0 && m[3] >= 0 {
			path = src[m[2]:m[3]]
		} else if m[4] >= 0 && m[5] >= 0 {
			path = src[m[4]:m[5]]
		}
		if path != "" {
			result.Imports = append(result.Imports, UnifiedImport{Path: path})
		}
	}

	return result, nil
}

func (e *PythonExtractor) parsePythonParams(paramsStr string) []UnifiedParam {
	if strings.TrimSpace(paramsStr) == "" {
		return nil
	}
	var params []UnifiedParam
	for _, part := range strings.Split(paramsStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "self" || part == "cls" {
			continue
		}
		// 去除默认值
		if idx := strings.Index(part, "="); idx >= 0 {
			part = part[:idx]
		}
		// 去除类型注解
		var name, typ string
		if idx := strings.Index(part, ":"); idx >= 0 {
			name = strings.TrimSpace(part[:idx])
			typ = strings.TrimSpace(part[idx+1:])
		} else {
			name = part
		}
		params = append(params, UnifiedParam{Name: name, Type: typ})
	}
	return params
}

// PythonExtractorInit registers the fallback Python extractor
func PythonExtractorInit() {
	GlobalRegistry.Register(NewPythonExtractor())
}
