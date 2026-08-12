package pipeline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

// ASTContext AST 上下文信息（注入 Prompt 的摘要）
type ASTContext struct {
	Functions  []FunctionSignature `json:"functions"`   // 变更涉及的函数签名
	Structs    []TypeSignature     `json:"structs"`     // 变更涉及的结构体
	Interfaces []TypeSignature     `json:"interfaces"`  // 变更涉及的接口
	Imports    []string            `json:"imports"`     // 新增/修改的 import
	Callers    []string            `json:"callers"`     // 调用链（正则提取）
	TotalChars int                 `json:"total_chars"` // AST 文本总字符数
	Language   string              `json:"language"`    // 检测到的语言
}

// FunctionSignature 函数签名
type FunctionSignature struct {
	Name       string   `json:"name"`
	Receiver   string   `json:"receiver,omitempty"` // 接收者类型，如 "*UserService"
	Params     []string `json:"params"`             // 参数类型列表，如 ["context.Context", "int"]
	Returns    []string `json:"returns"`            // 返回值类型列表
	IsExported bool     `json:"is_exported"`
	FilePath   string   `json:"file_path,omitempty"`  // 所在文件路径
	LineStart  int      `json:"line_start,omitempty"` // 函数起始行号
	Body       string   `json:"body,omitempty"`       // 函数体文本摘要
}

// TypeSignature 类型签名（struct/interface）
type TypeSignature struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // "struct" | "interface"
	IsExported bool   `json:"is_exported"`
	FilePath   string `json:"file_path,omitempty"` // 所在文件路径
}

// ASTExtractor AST 提取器
type ASTExtractor struct {
	language string // golang | java | python | javascript | unknown
}

// NewASTExtractor 创建提取器
func NewASTExtractor(lang string) *ASTExtractor {
	if lang == "" {
		lang = "golang"
	}
	return &ASTExtractor{language: lang}
}

// Extract 从 diff 文件列表中提取 AST 上下文
// files: []map[string]{"path": string, "diff": string}
func (e *ASTExtractor) Extract(files []map[string]interface{}) *ASTContext {
	ctx := &ASTContext{
		Language:   e.language,
		Functions:  make([]FunctionSignature, 0),
		Structs:    make([]TypeSignature, 0),
		Interfaces: make([]TypeSignature, 0),
		Imports:    make([]string, 0),
		Callers:    make([]string, 0),
	}

	for _, f := range files {
		path, _ := f["path"].(string)
		diff, _ := f["diff"].(string)
		if path == "" || diff == "" {
			continue
		}

		switch e.language {
		case "golang":
			e.extractGo(path, diff, ctx)
		case "java":
			e.extractJava(path, diff, ctx)
		case "python":
			e.extractPython(path, diff, ctx)
		case "frontend", "javascript", "typescript":
			e.extractJavaScript(path, diff, ctx)
		default:
			e.extractGeneric(path, diff, ctx)
		}
	}

	// 计算总字符数（用于 Token 估算）
	var sb strings.Builder
	for _, fn := range ctx.Functions {
		sb.WriteString(fn.Name)
		for _, p := range fn.Params {
			sb.WriteString(p)
		}
	}
	// 去重：多个文件可能包含相同的 import/caller
	ctx.Imports = uniqueStrings(ctx.Imports)
	ctx.Callers = uniqueStrings(ctx.Callers)
	for _, im := range ctx.Imports {
		sb.WriteString(im)
	}
	ctx.TotalChars = sb.Len()

	return ctx
}

// ========== Go 语言：使用 go/parser + go/ast 标准库 ==========

func (e *ASTExtractor) extractGo(path, diff string, ctx *ASTContext) {
	// 1. 尝试从 diff 中恢复可解析的代码片段
	codeBlocks := extractCodeBlocksFromDiff(diff)

	for _, code := range codeBlocks {
		if code == "" {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, code, parser.SkipObjectResolution|parser.AllErrors)
		if err != nil {
			// 解析失败，回退到正则
			e.extractGoByRegex(code, path, ctx)
			continue
		}

		// 遍历 AST
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				sig := FunctionSignature{
					Name:       x.Name.Name,
					IsExported: ast.IsExported(x.Name.Name),
					FilePath:   path,
					LineStart:  fset.Position(x.Pos()).Line,
				}
				// 提取函数体（从 code 字符串中按 FileSet 偏移量截取）
				if x.Body != nil {
					if body := extractFuncBodyFromSource(fset, x.Body, code, 500); body != "" {
						sig.Body = body
					}
				}
				// Receiver
				if x.Recv != nil && len(x.Recv.List) > 0 {
					sig.Receiver = goTypeToString(x.Recv.List[0].Type)
				}
				// Params
				if x.Type.Params != nil {
					for _, param := range x.Type.Params.List {
						for range param.Names {
							sig.Params = append(sig.Params, goTypeToString(param.Type))
						}
					}
				}
				// Returns
				if x.Type.Results != nil {
					for _, ret := range x.Type.Results.List {
						sig.Returns = append(sig.Returns, goTypeToString(ret.Type))
					}
				}
				ctx.Functions = append(ctx.Functions, sig)

			case *ast.TypeSpec:
				if _, ok := x.Type.(*ast.StructType); ok {
					ctx.Structs = append(ctx.Structs, TypeSignature{
						Name:       x.Name.Name,
						Kind:       "struct",
						IsExported: ast.IsExported(x.Name.Name),
						FilePath:   path,
					})
				}
				if _, ok := x.Type.(*ast.InterfaceType); ok {
					ctx.Interfaces = append(ctx.Interfaces, TypeSignature{
						Name:       x.Name.Name,
						Kind:       "interface",
						IsExported: ast.IsExported(x.Name.Name),
						FilePath:   path,
					})
				}
			}
			return true
		})
	}

	// 2. 从 diff 中提取 import 变更
	ctx.Imports = append(ctx.Imports, extractGoImports(diff)...)
	// 3. 调用链分析（简单正则）
	ctx.Callers = append(ctx.Callers, extractGoCallers(diff)...)
}

func (e *ASTExtractor) extractGoByRegex(code, path string, ctx *ASTContext) {
	// 函数签名正则
	funcRe := regexp.MustCompile(`(?m)^\s*func\s+(?:\(([^)]+)\)\s+)?(\w+)\s*\(([^)]*)\)\s*(?:\(([^)]*)\)|([\w\[\]*.]+))?\s*\{`)
	matches := funcRe.FindAllStringSubmatchIndex(code, -1)
	for _, m := range matches {
		if len(m) < 6 {
			continue
		}
		name := code[m[4]:m[5]]
		sig := FunctionSignature{
			Name:       name,
			IsExported: isExported(name),
			FilePath:   path,
			LineStart:  lineStartFromIndex(code, m[0]),
		}
		// Receiver (m[2]:m[3])
		if m[2] >= 0 && m[3] >= 0 {
			sig.Receiver = strings.TrimSpace(code[m[2]:m[3]])
		}
		// Params (m[6]:m[7])
		if len(m) > 6 && m[6] >= 0 && m[7] >= 0 {
			for _, p := range strings.Split(code[m[6]:m[7]], ",") {
				parts := strings.Fields(strings.TrimSpace(p))
				if len(parts) >= 1 {
					sig.Params = append(sig.Params, parts[len(parts)-1])
				}
			}
		}
		// Returns (m[8]:m[9] 或 m[10]:m[11])
		if len(m) > 8 && m[8] >= 0 && m[9] >= 0 {
			for _, r := range strings.Split(code[m[8]:m[9]], ",") {
				sig.Returns = append(sig.Returns, strings.TrimSpace(r))
			}
		}
		if len(m) > 10 && m[10] >= 0 && m[11] >= 0 {
			sig.Returns = append(sig.Returns, strings.TrimSpace(code[m[10]:m[11]]))
		}
		// 尝试提取函数体
		if body := extractGoBodyByBraces(code, m[0], 500); body != "" {
			sig.Body = body
		}
		ctx.Functions = append(ctx.Functions, sig)
	}
}

func extractGoImports(diff string) []string {
	var imports []string
	re := regexp.MustCompile(`(?m)^[+-]\s*"([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(diff, -1) {
		imp := m[1]
		if !strings.Contains(imp, `"`) && len(imp) > 1 {
			imports = append(imports, imp)
		}
	}
	return uniqueStrings(imports)
}

func extractGoCallers(diff string) []string {
	// 简单调用链：识别 funcName(...) 模式
	var callers []string
	re := regexp.MustCompile(`(?m)\b([A-Z]\w*)\.([A-Z]\w*)\s*\(`)
	seen := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(diff, -1) {
		call := m[1] + "." + m[2]
		if !seen[call] {
			seen[call] = true
			callers = append(callers, call)
		}
	}
	return callers
}

func goTypeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + goTypeToString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + goTypeToString(t.Elt)
		}
		return "[...]" + goTypeToString(t.Elt)
	case *ast.SelectorExpr:
		return goTypeToString(t.X) + "." + t.Sel.Name
	case *ast.FuncType:
		return "func(...)"
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.MapType:
		return "map[" + goTypeToString(t.Key) + "]" + goTypeToString(t.Value)
	case *ast.ChanType:
		return "chan " + goTypeToString(t.Value)
	default:
		return "unknown"
	}
}

// ========== Java / Python / JS：正则提取 ==========

func (e *ASTExtractor) extractJava(path, diff string, ctx *ASTContext) {
	// 类方法正则
	re := regexp.MustCompile(`(?m)^\s*[+-]?\s*(?:public|private|protected|static|\s)*\s*([\w<>,\s\[\]]+)\s+(\w+)\s*\(([^)]*)\)\s*(?:throws\s+\w+)?\s*\{`)
	for _, m := range re.FindAllStringSubmatch(diff, -1) {
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:       m[2],
			Returns:    []string{strings.TrimSpace(m[1])},
			IsExported: isExported(m[2]),
			FilePath:   path,
		})
	}
	// 类定义
	classRe := regexp.MustCompile(`(?m)^\s*[+-]?\s*(?:public\s+)?(?:class|interface|enum)\s+(\w+)`)
	for _, m := range classRe.FindAllStringSubmatch(diff, -1) {
		ctx.Structs = append(ctx.Structs, TypeSignature{
			Name:       m[1],
			Kind:       "class",
			IsExported: isExported(m[1]),
			FilePath:   path,
		})
	}
}

func (e *ASTExtractor) extractPython(path, diff string, ctx *ASTContext) {
	// 函数 def
	re := regexp.MustCompile(`(?m)^\s*[+-]?\s*def\s+(\w+)\s*\(([^)]*)\)`)
	for _, m := range re.FindAllStringSubmatch(diff, -1) {
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:       m[1],
			IsExported: !strings.HasPrefix(m[1], "_"),
			FilePath:   path,
		})
	}
	// 类定义
	classRe := regexp.MustCompile(`(?m)^\s*[+-]?\s*class\s+(\w+)`)
	for _, m := range classRe.FindAllStringSubmatch(diff, -1) {
		ctx.Structs = append(ctx.Structs, TypeSignature{
			Name:     m[1],
			Kind:     "class",
			FilePath: path,
		})
	}
}

func (e *ASTExtractor) extractJavaScript(path, diff string, ctx *ASTContext) {
	// 函数声明
	re := regexp.MustCompile(`(?m)^\s*[+-]?\s*(?:export\s+(?:default\s+)?)?(?:async\s+)?function\s+(\w+)\s*\(([^)]*)\)`)
	for _, m := range re.FindAllStringSubmatch(diff, -1) {
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:       m[1],
			IsExported: true,
			FilePath:   path,
		})
	}
	// 箭头函数 / 方法
	methodRe := regexp.MustCompile(`(?m)(\w+)\s*\(([^)]*)\)\s*\{`)
	for _, m := range methodRe.FindAllStringSubmatch(diff, -1) {
		if m[1] == "if" || m[1] == "while" || m[1] == "for" || m[1] == "switch" {
			continue
		}
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:     m[1],
			FilePath: path,
		})
	}
}

func (e *ASTExtractor) extractGeneric(path, diff string, ctx *ASTContext) {
	// 通用：尝试识别函数定义模式
	re := regexp.MustCompile(`(?m)^\s*[+-]?\s*(?:func|function|def|fn)\s+(\w+)`)
	for _, m := range re.FindAllStringSubmatch(diff, -1) {
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:       m[1],
			IsExported: isExported(m[1]),
			FilePath:   path,
		})
	}
	// 类定义
	classRe := regexp.MustCompile(`(?m)^\s*[+-]?\s*(?:class|struct|interface)\s+(\w+)`)
	for _, m := range classRe.FindAllStringSubmatch(diff, -1) {
		ctx.Structs = append(ctx.Structs, TypeSignature{
			Name:       m[1],
			Kind:       "type",
			IsExported: isExported(m[1]),
			FilePath:   path,
		})
	}
}

// ========== 工具函数 ==========

// extractCodeBlocksFromDiff 从 diff 中提取可解析的代码块
// 策略：收集 diff 中以 + 开头的行，还原为一段可能的代码
func extractCodeBlocksFromDiff(diff string) []string {
	var blocks []string
	var current strings.Builder
	lines := strings.Split(diff, "\n")

	for _, line := range lines {
		// diff 中以 + 开头的是新增代码
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			codeLine := strings.TrimPrefix(line, "+")
			current.WriteString(codeLine)
			current.WriteString("\n")
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			// 删除的代码也用于识别上下文（但不作为新代码）
			// 这里我们只收集新增代码块
			if current.Len() > 0 {
				blocks = append(blocks, current.String())
				current.Reset()
			}
		} else if strings.TrimSpace(line) == "" && current.Len() > 0 {
			// 空行作为块分隔
			blocks = append(blocks, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		blocks = append(blocks, current.String())
	}

	// 过滤太短的块
	var valid []string
	for _, b := range blocks {
		if len(strings.TrimSpace(b)) > 10 {
			valid = append(valid, b)
		}
	}
	return valid
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	return first >= 'A' && first <= 'Z'
}

func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Format 将 ASTContext 格式化为可注入 Prompt 的文本摘要
func (ctx *ASTContext) Format() string {
	if ctx == nil || (len(ctx.Functions) == 0 && len(ctx.Structs) == 0 && len(ctx.Interfaces) == 0 && len(ctx.Imports) == 0 && len(ctx.Callers) == 0) {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 代码结构上下文\n\n")
	if len(ctx.Functions) > 0 {
		sb.WriteString("### 涉及的函数签名\n")
		for _, fn := range ctx.Functions {
			var sig strings.Builder
			if fn.Receiver != "" {
				sig.WriteString("func (" + fn.Receiver + ") ")
			} else {
				sig.WriteString("func ")
			}
			sig.WriteString(fn.Name + "(")
			for i, p := range fn.Params {
				if i > 0 {
					sig.WriteString(", ")
				}
				sig.WriteString(p)
			}
			sig.WriteString(")")
			if len(fn.Returns) > 0 {
				sig.WriteString(" ")
				if len(fn.Returns) > 1 {
					sig.WriteString("(")
				}
				for i, r := range fn.Returns {
					if i > 0 {
						sig.WriteString(", ")
					}
					sig.WriteString(r)
				}
				if len(fn.Returns) > 1 {
					sig.WriteString(")")
				}
			}
			sb.WriteString("- " + sig.String() + "\n")
		}
		sb.WriteString("\n")
	}
	if len(ctx.Structs) > 0 {
		sb.WriteString("### 涉及的结构体/类\n")
		for _, s := range ctx.Structs {
			sb.WriteString("- type " + s.Name + " " + s.Kind + "\n")
		}
		sb.WriteString("\n")
	}
	if len(ctx.Interfaces) > 0 {
		sb.WriteString("### 涉及的接口\n")
		for _, iface := range ctx.Interfaces {
			sb.WriteString("- type " + iface.Name + " interface\n")
		}
		sb.WriteString("\n")
	}
	if len(ctx.Imports) > 0 {
		sb.WriteString("### 新增/修改的依赖\n")
		for _, imp := range ctx.Imports {
			sb.WriteString("- " + imp + "\n")
		}
		sb.WriteString("\n")
	}
	if len(ctx.Callers) > 0 {
		sb.WriteString("### 调用链\n")
		for _, c := range ctx.Callers {
			sb.WriteString("- " + c + "\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ========== 基于完整文件内容的 AST 提取（更完整准确） ==========

// ExtractFull 基于完整文件内容提取 AST 上下文
// fileContents: map[file_path]file_content
func (e *ASTExtractor) ExtractFull(fileContents map[string]string) *ASTContext {
	ctx := &ASTContext{
		Language:   e.language,
		Functions:  make([]FunctionSignature, 0),
		Structs:    make([]TypeSignature, 0),
		Interfaces: make([]TypeSignature, 0),
		Imports:    make([]string, 0),
		Callers:    make([]string, 0),
	}

	for path, content := range fileContents {
		if path == "" || content == "" {
			continue
		}
		switch e.language {
		case "golang":
			e.extractGoFull(path, content, ctx)
		case "java":
			e.extractJavaFull(path, content, ctx)
		case "python":
			e.extractPythonFull(path, content, ctx)
		case "frontend", "javascript", "typescript":
			e.extractJavaScriptFull(path, content, ctx)
		default:
			e.extractGenericFull(path, content, ctx)
		}
	}

	ctx.Imports = uniqueStrings(ctx.Imports)
	ctx.Callers = uniqueStrings(ctx.Callers)
	ctx.TotalChars = len(ctx.Format())
	return ctx
}

// ExtractSingle 对单个完整文件提取 AST（返回该文件独立的 ASTContext）
func (e *ASTExtractor) ExtractSingle(path, content string) *ASTContext {
	single := make(map[string]string)
	single[path] = content
	return e.ExtractFull(single)
}

// ========== Go 语言：完整文件 AST 提取 ==========

func (e *ASTExtractor) extractGoFull(path, content string, ctx *ASTContext) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.AllErrors)
	if err != nil {
		// 完整文件解析失败（理论上不应发生），回退到正则
		e.extractGoByRegex(content, path, ctx)
		return
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			sig := FunctionSignature{
				Name:       x.Name.Name,
				IsExported: ast.IsExported(x.Name.Name),
				FilePath:   path,
				LineStart:  fset.Position(x.Pos()).Line,
			}
			if x.Recv != nil && len(x.Recv.List) > 0 {
				sig.Receiver = goTypeToString(x.Recv.List[0].Type)
			}
			if x.Type.Params != nil {
				for _, param := range x.Type.Params.List {
					for range param.Names {
						sig.Params = append(sig.Params, goTypeToString(param.Type))
					}
				}
			}
			if x.Type.Results != nil {
				for _, ret := range x.Type.Results.List {
					sig.Returns = append(sig.Returns, goTypeToString(ret.Type))
				}
			}
			// 提取函数体
			if x.Body != nil {
				if body := extractFuncBodyFromSource(fset, x.Body, content, 500); body != "" {
					sig.Body = body
				}
			}
			ctx.Functions = append(ctx.Functions, sig)

		case *ast.TypeSpec:
			if _, ok := x.Type.(*ast.StructType); ok {
				ctx.Structs = append(ctx.Structs, TypeSignature{
					Name:       x.Name.Name,
					Kind:       "struct",
					IsExported: ast.IsExported(x.Name.Name),
					FilePath:   path,
				})
			}
			if _, ok := x.Type.(*ast.InterfaceType); ok {
				ctx.Interfaces = append(ctx.Interfaces, TypeSignature{
					Name:       x.Name.Name,
					Kind:       "interface",
					IsExported: ast.IsExported(x.Name.Name),
					FilePath:   path,
				})
			}

		case *ast.ImportSpec:
			imp := strings.Trim(x.Path.Value, `"`)
			if imp != "" {
				ctx.Imports = append(ctx.Imports, imp)
			}
		}
		return true
	})

	// 调用链分析
	ctx.Callers = append(ctx.Callers, extractGoCallers(content)...)
}

// ========== Java 完整文件提取 ==========

func (e *ASTExtractor) extractJavaFull(path, content string, ctx *ASTContext) {
	// 类名
	classRe := regexp.MustCompile(`(?m)^\s*public\s+(?:class|interface|enum)\s+(\w+)`)
	for _, m := range classRe.FindAllStringSubmatch(content, -1) {
		ctx.Structs = append(ctx.Structs, TypeSignature{
			Name:       m[1],
			Kind:       "class",
			IsExported: true,
			FilePath:   path,
		})
	}
	// 方法签名
	methodRe := regexp.MustCompile(`(?m)^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:[\w<>\[\]]+\s+)+(\w+)\s*\(([^)]*)\)`)
	for _, m := range methodRe.FindAllStringSubmatch(content, -1) {
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:       m[1],
			IsExported: true,
			FilePath:   path,
		})
	}
	// import
	importRe := regexp.MustCompile(`(?m)^\s*import\s+([\w.]+);`)
	for _, m := range importRe.FindAllStringSubmatch(content, -1) {
		ctx.Imports = append(ctx.Imports, m[1])
	}
}

// ========== Python 完整文件提取 ==========

func (e *ASTExtractor) extractPythonFull(path, content string, ctx *ASTContext) {
	// 函数定义
	funcRe := regexp.MustCompile(`(?m)^\s*def\s+(\w+)\s*\(([^)]*)\)`)
	for _, m := range funcRe.FindAllStringSubmatch(content, -1) {
		params := []string{}
		if m[2] != "" {
			for _, p := range strings.Split(m[2], ",") {
				p = strings.TrimSpace(p)
				if p != "" && !strings.HasPrefix(p, "self") && !strings.HasPrefix(p, "cls") {
					parts := strings.Split(p, ":")
					if len(parts) > 1 {
						params = append(params, strings.TrimSpace(parts[1]))
					}
				}
			}
		}
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:       m[1],
			IsExported: !strings.HasPrefix(m[1], "_"),
			Params:     params,
			FilePath:   path,
		})
	}
	// 类定义
	classRe := regexp.MustCompile(`(?m)^\s*class\s+(\w+)`)
	for _, m := range classRe.FindAllStringSubmatch(content, -1) {
		ctx.Structs = append(ctx.Structs, TypeSignature{
			Name:       m[1],
			Kind:       "class",
			IsExported: !strings.HasPrefix(m[1], "_"),
			FilePath:   path,
		})
	}
	// import
	importRe := regexp.MustCompile(`(?m)^\s*(?:import\s+([\w.]+)|from\s+([\w.]+)\s+import)`)
	for _, m := range importRe.FindAllStringSubmatch(content, -1) {
		if m[1] != "" {
			ctx.Imports = append(ctx.Imports, m[1])
		} else if m[2] != "" {
			ctx.Imports = append(ctx.Imports, m[2])
		}
	}
}

// ========== JS/TS 完整文件提取 ==========

func (e *ASTExtractor) extractJavaScriptFull(path, content string, ctx *ASTContext) {
	// 函数定义（多种方式）
	funcPatterns := []string{
		`(?m)^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\(([^)]*)\)`,
		`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:function|\([^)]*\)\s*=>)`,
		`(?m)^\s*(?:export\s+)?class\s+(\w+)`,
	}
	for _, pat := range funcPatterns {
		re := regexp.MustCompile(pat)
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			if strings.Contains(m[0], "class") {
				ctx.Structs = append(ctx.Structs, TypeSignature{
					Name:       m[1],
					Kind:       "class",
					IsExported: strings.Contains(m[0], "export"),
					FilePath:   path,
				})
			} else {
				params := []string{}
				if len(m) > 2 && m[2] != "" {
					for _, p := range strings.Split(m[2], ",") {
						p = strings.TrimSpace(p)
						if p != "" {
							params = append(params, p)
						}
					}
				}
				ctx.Functions = append(ctx.Functions, FunctionSignature{
					Name:       m[1],
					IsExported: strings.Contains(m[0], "export"),
					Params:     params,
					FilePath:   path,
				})
			}
		}
	}
	// import
	importRe := regexp.MustCompile(`(?m)^\s*import\s+.*?\s+from\s+['"]([^'"]+)['"]`)
	for _, m := range importRe.FindAllStringSubmatch(content, -1) {
		ctx.Imports = append(ctx.Imports, m[1])
	}
}

// ========== 通用完整文件提取 ==========

func (e *ASTExtractor) extractGenericFull(path, content string, ctx *ASTContext) {
	// 简单提取：函数定义和类定义
	funcRe := regexp.MustCompile(`(?m)^\s*(?:func|function|def|method)\s+(\w+)`)
	for _, m := range funcRe.FindAllStringSubmatch(content, -1) {
		ctx.Functions = append(ctx.Functions, FunctionSignature{
			Name:       m[1],
			IsExported: isExported(m[1]),
			FilePath:   path,
		})
	}
	classRe := regexp.MustCompile(`(?m)^\s*(?:class|struct|interface)\s+(\w+)`)
	for _, m := range classRe.FindAllStringSubmatch(content, -1) {
		ctx.Structs = append(ctx.Structs, TypeSignature{
			Name:       m[1],
			Kind:       "type",
			IsExported: isExported(m[1]),
			FilePath:   path,
		})
	}
}

// extractFuncBodyFromSource 基于 go/token.FileSet 从原始源码字符串中提取函数体，
// 并截断到 maxLen 长度。用于 extractGo / extractGoFull。
func extractFuncBodyFromSource(fset *token.FileSet, body *ast.BlockStmt, source string, maxLen int) string {
	if body == nil {
		return ""
	}
	file := fset.File(body.Pos())
	if file == nil {
		return ""
	}
	start := file.Offset(body.Pos())
	end := file.Offset(body.End())
	if end <= start || start < 0 || end > len(source) {
		return ""
	}
	bodyText := source[start:end]
	if len(bodyText) > maxLen {
		bodyText = bodyText[:maxLen] + "..."
	}
	return bodyText
}

// extractGoBodyByBraces 从代码字符串中基于大括号匹配提取函数体。
// 用于 extractGoByRegex 回退路径，仅做近似提取（不处理字符串字面量中的大括号）。
func extractGoBodyByBraces(code string, funcStartIdx int, maxLen int) string {
	braceIdx := strings.Index(code[funcStartIdx:], "{")
	if braceIdx < 0 {
		return ""
	}
	braceIdx += funcStartIdx
	depth := 1
	i := braceIdx + 1
	for i < len(code) && depth > 0 {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		i++
	}
	if depth != 0 {
		return "" // 未找到匹配的大括号
	}
	body := code[braceIdx:i]
	if len(body) > maxLen {
		body = body[:maxLen] + "..."
	}
	return body
}

// lineStartFromIndex 返回 source 中 idx 位置对应的行号（1-based）
func lineStartFromIndex(source string, idx int) int {
	if idx < 0 || idx > len(source) {
		return 0
	}
	return strings.Count(source[:idx], "\n") + 1
}
