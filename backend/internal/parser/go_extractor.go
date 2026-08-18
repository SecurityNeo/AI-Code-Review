package parser

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// GoExtractor Go语言AST提取器（使用go/parser标准库）
type GoExtractor struct{}

// NewGoExtractor 创建Go提取器
func NewGoExtractor() *GoExtractor {
	return &GoExtractor{}
}

// Language 返回语言
func (e *GoExtractor) Language() string { return "golang" }

// FileExts 返回文件扩展名
func (e *GoExtractor) FileExts() []string { return []string{".go"} }

// ParseFile 解析Go文件
func (e *GoExtractor) ParseFile(filePath string, src []byte) (*UnifiedAST, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, src, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parse go file failed: %w", err)
	}

	result := &UnifiedAST{
		Language: "golang",
		FilePath: filePath,
	}

	functions := make([]UnifiedFunction, 0)
	types := make([]UnifiedType, 0)
	imports := make([]UnifiedImport, 0)
	variables := make([]UnifiedVariable, 0)

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			fn := convertFuncDecl(fset, filePath, src, x)
			functions = append(functions, fn)
		case *ast.TypeSpec:
			t := convertTypeSpec(fset, filePath, x)
			if t != nil {
				types = append(types, *t)
			}
		case *ast.ImportSpec:
			imports = append(imports, convertImportSpec(x))
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if name.Obj == nil {
					continue
				}
				typ := ""
				if x.Type != nil {
					typ = exprToString(x.Type)
				}
				var defaultVal string
				if x.Values != nil && i < len(x.Values) {
					defaultVal = exprToString(x.Values[i])
				}
				if name.Obj.Kind == ast.Con {
					// 常量 - 暂时不收集到统一变量中，需要时扩展
					_ = defaultVal
				} else {
					variables = append(variables, UnifiedVariable{
						Name:         name.Name,
						Type:         typ,
						DefaultValue: defaultVal,
						IsExported:   ast.IsExported(name.Name),
					})
				}
			}
		}
		return true
	})

	result.Functions = functions
	result.Types = types
	result.Imports = imports
	result.Variables = variables
	result.CallSites = extractCallSites(fset, filePath, f)
	result.VarBindings = extractVarBindings(fset, filePath, f)
	result.TODOComments = extractTODOComments(fset, f, functions)

	return result, nil
}

func convertFuncDecl(fset *token.FileSet, filePath string, src []byte, decl *ast.FuncDecl) UnifiedFunction {
	fn := UnifiedFunction{
		Name:       decl.Name.Name,
		IsExported: ast.IsExported(decl.Name.Name),
		Location: SourceLocation{
			File:      filePath,
			LineStart: fset.Position(decl.Pos()).Line,
			LineEnd:   fset.Position(decl.End()).Line,
		},
	}
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		fn.Receiver = exprToString(decl.Recv.List[0].Type)
	}
	if decl.Doc != nil {
		fn.DocComment = decl.Doc.Text()
	}
	if decl.Type.Params != nil {
		for _, param := range decl.Type.Params.List {
			typ := exprToString(param.Type)
			for _, name := range param.Names {
				fn.Params = append(fn.Params, UnifiedParam{
					Name:       name.Name,
					Type:       typ,
					IsVariadic: isVariadicAst(param.Type),
				})
			}
			if len(param.Names) == 0 {
				fn.Params = append(fn.Params, UnifiedParam{Type: typ})
			}
		}
	}
	if decl.Type.Results != nil {
		for _, ret := range decl.Type.Results.List {
			fn.Returns = append(fn.Returns, exprToString(ret.Type))
		}
	}
	if decl.Body != nil {
		start := fset.Position(decl.Body.Pos()).Offset
		end := fset.Position(decl.Body.End()).Offset
		if end > start && start >= 0 && end <= len(src) {
			body := string(src[start:end])
			if len(body) > 5000 {
				body = body[:5000] + "..."
			}
			fn.BodySnippet = body
		}
		fn.Complexity = estimateComplexity(decl.Body)
	}
	fn.SecurityRole = inferSecurityRole(fn.Name, fn.Receiver)
	return fn
}

func convertTypeSpec(fset *token.FileSet, filePath string, spec *ast.TypeSpec) *UnifiedType {
	t := &UnifiedType{
		Name:       spec.Name.Name,
		IsExported: ast.IsExported(spec.Name.Name),
		Location: SourceLocation{
			File:      filePath,
			LineStart: fset.Position(spec.Pos()).Line,
			LineEnd:   fset.Position(spec.End()).Line,
		},
	}
	if spec.Doc != nil {
		t.DocComment = spec.Doc.Text()
	}
	switch x := spec.Type.(type) {
	case *ast.StructType:
		t.Kind = "struct"
		if x.Fields != nil {
			for _, field := range x.Fields.List {
				ft := UnifiedField{
					Type:       exprToString(field.Type),
					IsExported: ast.IsExported(nameOrAnonymous(field.Names)),
					Location:   SourceLocation{File: filePath, LineStart: fset.Position(field.Pos()).Line},
				}
				if len(field.Names) > 0 {
					ft.Name = field.Names[0].Name
				} else {
					ft.Name = ft.Type // 匿名字段，类型名即字段名
				}
				if field.Tag != nil {
					ft.Tag = strings.Trim(field.Tag.Value, "`")
				}
				t.Fields = append(t.Fields, ft)
			}
		}
	case *ast.InterfaceType:
		t.Kind = "interface"
		if x.Methods != nil {
			for _, method := range x.Methods.List {
				if len(method.Names) > 0 {
					m := UnifiedMethodSignature{
						Name:       method.Names[0].Name,
						IsExported: ast.IsExported(method.Names[0].Name),
					}
					if fnType, ok := method.Type.(*ast.FuncType); ok {
						m.Params = extractFieldList(fnType.Params)
						m.Returns = extractReturns(fnType.Results)
					}
					t.Methods = append(t.Methods, m)
				}
			}
		}
	}
	return t
}

func convertImportSpec(spec *ast.ImportSpec) UnifiedImport {
	imp := UnifiedImport{Path: strings.Trim(spec.Path.Value, `"`)}
	if spec.Name != nil {
		imp.Alias = spec.Name.Name
	}
	if !strings.Contains(imp.Path, ".") {
		imp.IsStdLib = true
	}
	return imp
}

func extractCallSites(fset *token.FileSet, filePath string, f *ast.File) []UnifiedCallSite {
	var sites []UnifiedCallSite
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sites = append(sites, callSiteFromExpr(fset, filePath, call))
		return true
	})
	return sites
}

func callSiteFromExpr(fset *token.FileSet, filePath string, call *ast.CallExpr) UnifiedCallSite {
	site := UnifiedCallSite{
		Location: SourceLocation{
			File:      filePath,
			LineStart: fset.Position(call.Pos()).Line,
			LineEnd:   fset.Position(call.End()).Line,
		},
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		site.TargetFunc = fn.Name
	case *ast.SelectorExpr:
		site.TargetFunc = fn.Sel.Name
		if ident, ok := fn.X.(*ast.Ident); ok {
			site.TargetPkg = ident.Name
			site.ReceiverVar = ident.Name
		}
	}
	for _, arg := range call.Args {
		site.Arguments = append(site.Arguments, exprToString(arg))
	}
	return site
}

func extractVarBindings(fset *token.FileSet, filePath string, f *ast.File) []UnifiedVarBinding {
	var bindings []UnifiedVarBinding
	ast.Inspect(f, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			if len(stmt.Lhs) == 0 || len(stmt.Rhs) == 0 {
				return true
			}
			if ident, ok := stmt.Lhs[0].(*ast.Ident); ok {
				binding := UnifiedVarBinding{VarName: ident.Name}
				if call, ok := stmt.Rhs[0].(*ast.CallExpr); ok {
					cs := callSiteFromExpr(fset, filePath, call)
					binding.CallSite = &cs
				} else {
					binding.Literal = exprToString(stmt.Rhs[0])
				}
				bindings = append(bindings, binding)
			}
		case *ast.ValueSpec:
			if len(stmt.Names) == 0 || len(stmt.Values) == 0 {
				return true
			}
			binding := UnifiedVarBinding{VarName: stmt.Names[0].Name}
			if call, ok := stmt.Values[0].(*ast.CallExpr); ok {
				cs := callSiteFromExpr(fset, filePath, call)
				binding.CallSite = &cs
			} else {
				binding.Literal = exprToString(stmt.Values[0])
			}
			bindings = append(bindings, binding)
		}
		return true
	})
	return bindings
}

func extractFieldList(list *ast.FieldList) []UnifiedParam {
	if list == nil {
		return nil
	}
	var params []UnifiedParam
	for _, field := range list.List {
		typ := exprToString(field.Type)
		for _, name := range field.Names {
			params = append(params, UnifiedParam{
				Name:       name.Name,
				Type:       typ,
				IsVariadic: isVariadicAst(field.Type),
			})
		}
		if len(field.Names) == 0 {
			params = append(params, UnifiedParam{Type: typ})
		}
	}
	return params
}

func extractReturns(list *ast.FieldList) []string {
	if list == nil {
		return nil
	}
	var outs []string
	for _, field := range list.List {
		outs = append(outs, exprToString(field.Type))
	}
	return outs
}

// GoExtractorInit registers the fallback Go extractor (called from fallback registration)
func GoExtractorInit() {
	GlobalRegistry.Register(NewGoExtractor())
}

func exprToString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + exprToString(x.X)
	case *ast.ArrayType:
		if x.Len == nil {
			return "[]" + exprToString(x.Elt)
		}
		return "[...]" + exprToString(x.Elt)
	case *ast.SelectorExpr:
		return exprToString(x.X) + "." + x.Sel.Name
	case *ast.FuncType:
		return "func(...)"
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.MapType:
		return "map[" + exprToString(x.Key) + "]" + exprToString(x.Value)
	case *ast.ChanType:
		return "chan " + exprToString(x.Value)
	case *ast.Ellipsis:
		return "..." + exprToString(x.Elt)
	case *ast.StructType:
		return "struct{}"
	case *ast.BasicLit:
		return x.Value
	case *ast.CompositeLit:
		return exprToString(x.Type)
	case *ast.CallExpr:
		return exprToString(x.Fun)
	case *ast.IndexExpr:
		return exprToString(x.X) + "[" + exprToString(x.Index) + "]"
	case *ast.IndexListExpr:
		return exprToString(x.X) + "[...]"
	default:
		return "unknown"
	}
}

func isVariadicAst(expr ast.Expr) bool {
	_, ok := expr.(*ast.Ellipsis)
	return ok
}

func nameOrAnonymous(names []*ast.Ident) string {
	if len(names) == 0 {
		return "anonymous"
	}
	return names[0].Name
}

// inferSecurityRole 根据函数名和接收者推断安全角色 (P2-1)
func inferSecurityRole(name, receiver string) string {
	lower := strings.ToLower(name)
	recvLower := strings.ToLower(receiver)
	// 认证/授权
	if strings.Contains(lower, "auth") || strings.Contains(lower, "login") || strings.Contains(lower, "logout") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "session") || strings.Contains(lower, "permission") ||
		strings.Contains(lower, "role") || strings.Contains(lower, "credential") {
		return "auth"
	}
	// 加密/解密
	if strings.Contains(lower, "encrypt") || strings.Contains(lower, "decrypt") || strings.Contains(lower, "cipher") ||
		strings.Contains(lower, "hash") || strings.Contains(lower, "sign") || strings.Contains(lower, "verify") {
		return "encryption"
	}
	// 净化/过滤
	if strings.Contains(lower, "sanitize") || strings.Contains(lower, "escape") || strings.Contains(lower, "clean") ||
		strings.Contains(lower, "filter") || strings.Contains(lower, "validate") || strings.Contains(lower, "check") {
		return "sanitizer"
	}
	// 输入验证
	if strings.Contains(lower, "parse") || strings.Contains(lower, "bind") || strings.Contains(lower, "unmarshal") ||
		strings.Contains(lower, "decode") || strings.Contains(lower, "deserialize") {
		return "validator"
	}
	// HTTP Handler / Controller
	if strings.HasPrefix(lower, "handle") || strings.HasPrefix(lower, "get") || strings.HasPrefix(lower, "post") ||
		strings.HasPrefix(lower, "put") || strings.HasPrefix(lower, "delete") || strings.HasPrefix(lower, "patch") ||
		strings.Contains(recvLower, "handler") || strings.Contains(recvLower, "controller") || strings.Contains(recvLower, "router") {
		return "handler"
	}
	// Repository / DAO / Data Access
	if strings.Contains(recvLower, "repo") || strings.Contains(recvLower, "dao") || strings.Contains(recvLower, "store") ||
		strings.Contains(recvLower, "db") || strings.Contains(recvLower, "database") || strings.Contains(lower, "query") ||
		strings.Contains(lower, "fetch") || strings.Contains(lower, "find") || strings.Contains(lower, "getby") {
		return "data_access"
	}
	// Service / Business Logic
	if strings.Contains(recvLower, "service") || strings.Contains(recvLower, "usecase") || strings.Contains(recvLower, "manager") ||
		strings.Contains(recvLower, "biz") {
		return "service"
	}
	return "other"
}

// estimateComplexity 基于 AST 简单估算圈复杂度 (P2-5)
// 规则：每个 if/switch/for/range/select/case/&&/|| 增加 1
func estimateComplexity(body *ast.BlockStmt) int {
	if body == nil {
		return 1
	}
	complexity := 1
	ast.Inspect(body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.IfStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SelectStmt:
			complexity++
		case *ast.CaseClause:
			// case 语句只增加非 default 的分支
			if len(n.(*ast.CaseClause).List) > 0 {
				complexity++
			}
		case *ast.BinaryExpr:
			be := n.(*ast.BinaryExpr)
			if be.Op == token.LAND || be.Op == token.LOR {
				complexity++
			}
		}
		return true
	})
	return complexity
}

// extractTODOComments 扫描文件中的所有 TODO/FIXME/HACK/XXX 注释 (P2-3)
func extractTODOComments(fset *token.FileSet, f *ast.File, functions []UnifiedFunction) []TODOComment {
	var results []TODOComment
	if f.Comments == nil {
		return results
	}
	// 构建函数行号范围映射，用于推断注释属于哪个函数
	funcRanges := make([]struct {
		name      string
		lineStart int
		lineEnd   int
	}, 0, len(functions))
	for _, fn := range functions {
		funcRanges = append(funcRanges, struct {
			name      string
			lineStart int
			lineEnd   int
		}{name: fn.Name, lineStart: fn.Location.LineStart, lineEnd: fn.Location.LineEnd})
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			text := strings.TrimSpace(c.Text)
			upper := strings.ToUpper(text)
			var typ string
			if strings.Contains(upper, "TODO") {
				typ = "todo"
			} else if strings.Contains(upper, "FIXME") {
				typ = "fixme"
			} else if strings.Contains(upper, "HACK") {
				typ = "hack"
			} else if strings.Contains(upper, "XXX") {
				typ = "xxx"
			} else if strings.Contains(upper, "BUG") {
				typ = "bug"
			}
			if typ == "" {
				continue
			}
			line := fset.Position(c.Pos()).Line
			// 推断所属函数
			var funcName string
			for _, r := range funcRanges {
				if line >= r.lineStart && line <= r.lineEnd {
					funcName = r.name
					break
				}
			}
			results = append(results, TODOComment{
				Text:     text,
				Type:     typ,
				Line:     line,
				Function: funcName,
			})
		}
	}
	return results
}
