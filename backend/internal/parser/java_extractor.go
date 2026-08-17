package parser

import (
	"regexp"
	"strings"
)

// JavaExtractor Java语言提取器（简化版，基于正则+行级解析）
type JavaExtractor struct{}

// NewJavaExtractor 创建Java提取器
func NewJavaExtractor() *JavaExtractor { return &JavaExtractor{} }

// Language 返回语言
func (e *JavaExtractor) Language() string { return "java" }

// FileExts 返回文件扩展名
func (e *JavaExtractor) FileExts() []string { return []string{".java"} }

// ParseFile 解析Java文件
func (e *JavaExtractor) ParseFile(filePath string, content []byte) (*UnifiedAST, error) {
	src := string(content)
	result := &UnifiedAST{
		Language: "java",
		FilePath: filePath,
	}

	// 类/接口/枚举匹配
	classRe := regexp.MustCompile(`(?m)^\s*(?:public\s+|private\s+|protected\s+)?(?:abstract\s+)?(?:static\s+)?(class|interface|enum)\s+(\w+)`)
	classMatches := classRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range classMatches {
		kind := src[m[2]:m[3]]
		name := src[m[4]:m[5]]
		t := UnifiedType{
			Name:       name,
			Kind:       kind,
			IsExported: true,
			Location:   calcLocation(src, m[0]),
		}
		// 尝试提取泛型
		if idx := strings.Index(src[m[1]:], "<"); idx >= 0 && idx < 50 {
			end := strings.Index(src[m[1]+idx:], ">")
			if end >= 0 && end < 50 {
				t.Generics = append(t.Generics, src[m[1]+idx+1:m[1]+idx+end+1])
			}
		}
		result.Types = append(result.Types, t)
	}

	// 方法匹配（简化）
	methodRe := regexp.MustCompile(`(?m)^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:final\s+)?(?:abstract\s+)?(?:[\w<>\[\],\s]+\s+)?(\w+)\s*\(([^)]*)\)`)
	methodMatches := methodRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range methodMatches {
		name := src[m[2]:m[3]]
		if isKeyword(name) {
			continue
		}
		fn := UnifiedFunction{
			Name:       name,
			IsExported: true,
			IsStatic:   strings.Contains(src[m[0]:m[2]], "static"),
			Location:   calcLocation(src, m[0]),
		}
		// 解析参数
		paramsStr := src[m[4]:m[5]]
		fn.Params = e.parseJavaParams(paramsStr)
		result.Functions = append(result.Functions, fn)
	}

	// Import匹配
	importRe := regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([\w.]+);`)
	importMatches := importRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range importMatches {
		path := src[m[2]:m[3]]
		result.Imports = append(result.Imports, UnifiedImport{Path: path, IsStdLib: !strings.Contains(path, ".")})
	}

	return result, nil
}

func (e *JavaExtractor) parseJavaParams(paramsStr string) []UnifiedParam {
	if strings.TrimSpace(paramsStr) == "" {
		return nil
	}
	var params []UnifiedParam
	// 简单按逗号分割
	for _, part := range strings.Split(paramsStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 去除泛型
		cleaned := removeGenerics(part)
		fields := strings.Fields(cleaned)
		if len(fields) >= 2 {
			params = append(params, UnifiedParam{
				Name: fields[len(fields)-1],
				Type: strings.Join(fields[:len(fields)-1], " "),
			})
		}
	}
	return params
}

func isKeyword(s string) bool {
	keywords := map[string]bool{
		"if": true, "for": true, "while": true, "switch": true,
		"catch": true, "try": true, "return": true, "throw": true,
	}
	return keywords[s]
}

func removeGenerics(s string) string {
	var result strings.Builder
	depth := 0
	for _, ch := range s {
		switch ch {
		case '<':
			depth++
		case '>':
			depth--
		default:
			if depth == 0 {
				result.WriteRune(ch)
			}
		}
	}
	return result.String()
}

func calcLocation(src string, offset int) SourceLocation {
	line := strings.Count(src[:offset], "\n") + 1
	lineStart := strings.LastIndex(src[:offset], "\n")
	if lineStart < 0 {
		lineStart = 0
	}
	col := offset - lineStart
	return SourceLocation{LineStart: line, ColumnStart: col}
}

// JavaExtractorInit registers the fallback Java extractor
func JavaExtractorInit() {
	GlobalRegistry.Register(NewJavaExtractor())
}
