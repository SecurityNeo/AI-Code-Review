package parser

import (
	"regexp"
	"strings"
)

// JavaScriptExtractor JavaScript/TypeScript提取器
type JavaScriptExtractor struct {
	language string
	exts     []string
}

// NewJavaScriptExtractor 创建JS提取器
func NewJavaScriptExtractor() *JavaScriptExtractor {
	return &JavaScriptExtractor{language: "javascript", exts: []string{".js", ".jsx"}}
}

// NewTypeScriptExtractor 创建TS提取器
func NewTypeScriptExtractor() *JavaScriptExtractor {
	return &JavaScriptExtractor{language: "typescript", exts: []string{".ts", ".tsx"}}
}

// Language 返回语言
func (e *JavaScriptExtractor) Language() string { return e.language }

// FileExts 返回文件扩展名
func (e *JavaScriptExtractor) FileExts() []string { return e.exts }

// ParseFile 解析JS/TS文件
func (e *JavaScriptExtractor) ParseFile(filePath string, content []byte) (*UnifiedAST, error) {
	src := string(content)
	result := &UnifiedAST{
		Language: e.language,
		FilePath: filePath,
	}

	// 函数匹配（多种声明方式）
	patterns := []string{
		`(?m)^\s*(?:export\s+(?:default\s+)?)?(?:async\s+)?function\s+(\w+)\s*\(([^)]*)\)`,
		`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:function|\([^)]*\)\s*=>)`,
	}
	for _, pat := range patterns {
		re := regexp.MustCompile(pat)
		matches := re.FindAllStringSubmatchIndex(src, -1)
		for _, m := range matches {
			name := src[m[2]:m[3]]
			fn := UnifiedFunction{
				Name:       name,
				IsExported: strings.Contains(src[m[0]:m[2]], "export"),
				IsAsync:    strings.Contains(src[m[0]:m[2]], "async"),
				Location:   calcLocation(src, m[0]),
			}
			if len(m) > 5 {
				paramsStr := src[m[4]:m[5]]
				fn.Params = e.parseJSParams(paramsStr)
			}
			result.Functions = append(result.Functions, fn)
		}
	}

	// 类匹配
	classRe := regexp.MustCompile(`(?m)^\s*(?:export\s+)?class\s+(\w+)(?:\s+extends\s+(\w+))?`)
	classMatches := classRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range classMatches {
		name := src[m[2]:m[3]]
		t := UnifiedType{
			Name:       name,
			Kind:       "class",
			IsExported: strings.Contains(src[m[0]:m[2]], "export"),
			Location:   calcLocation(src, m[0]),
		}
		if len(m) > 5 && m[4] >= 0 && m[5] >= 0 {
			t.Extends = append(t.Extends, src[m[4]:m[5]])
		}
		result.Types = append(result.Types, t)
	}

	// 接口匹配（TS）
	if e.language == "typescript" {
		ifaceRe := regexp.MustCompile(`(?m)^\s*(?:export\s+)?interface\s+(\w+)(?:\s+extends\s+([\w,\s]+))?`)
		ifaceMatches := ifaceRe.FindAllStringSubmatchIndex(src, -1)
		for _, m := range ifaceMatches {
			name := src[m[2]:m[3]]
			t := UnifiedType{
				Name:       name,
				Kind:       "interface",
				IsExported: strings.Contains(src[m[0]:m[2]], "export"),
				Location:   calcLocation(src, m[0]),
			}
			if len(m) > 5 && m[4] >= 0 && m[5] >= 0 {
				for _, ext := range strings.Split(src[m[4]:m[5]], ",") {
					ext = strings.TrimSpace(ext)
					if ext != "" {
						t.Extends = append(t.Extends, ext)
					}
				}
			}
			result.Types = append(result.Types, t)
		}
	}

	// Import匹配
	importRe := regexp.MustCompile(`(?m)^\s*import\s+.*?\s+from\s+['"]([^'"]+)['"]`)
	importMatches := importRe.FindAllStringSubmatchIndex(src, -1)
	for _, m := range importMatches {
		path := src[m[2]:m[3]]
		result.Imports = append(result.Imports, UnifiedImport{Path: path})
	}

	return result, nil
}

func (e *JavaScriptExtractor) parseJSParams(paramsStr string) []UnifiedParam {
	if strings.TrimSpace(paramsStr) == "" {
		return nil
	}
	var params []UnifiedParam
	for _, part := range strings.Split(paramsStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 去除默认值
		if idx := strings.Index(part, "="); idx >= 0 {
			part = part[:idx]
		}
		// 去除TS类型注解（简单处理）
		var name, typ string
		if idx := strings.Index(part, ":"); idx >= 0 {
			name = strings.TrimSpace(part[:idx])
			typ = strings.TrimSpace(part[idx+1:])
		} else {
			name = part
		}
		// 去除解构
		if strings.HasPrefix(name, "{") || strings.HasPrefix(name, "[") {
			continue
		}
		params = append(params, UnifiedParam{Name: name, Type: typ})
	}
	return params
}

// JavaScriptExtractorInit registers the fallback JS/TS extractors
func JavaScriptExtractorInit() {
	GlobalRegistry.Register(NewJavaScriptExtractor())
	GlobalRegistry.Register(NewTypeScriptExtractor())
}
