//go:build cgo

package parser

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// TreeSitterGoExtractor Go语言AST提取器（基于Tree-sitter）
type TreeSitterGoExtractor struct{}

// NewTreeSitterGoExtractor 创建Tree-sitter Go提取器
func NewTreeSitterGoExtractor() *TreeSitterGoExtractor {
	return &TreeSitterGoExtractor{}
}

// Language 返回语言
func (e *TreeSitterGoExtractor) Language() string { return "golang" }

// FileExts 返回文件扩展名
func (e *TreeSitterGoExtractor) FileExts() []string { return []string{".go"} }

// ParseFile 解析Go文件
func (e *TreeSitterGoExtractor) ParseFile(filePath string, src []byte) (*UnifiedAST, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(golang.GetLanguage())
	tree := parser.Parse(nil, src)
	if tree == nil {
		return nil, fmt.Errorf("tree-sitter parse failed for %s", filePath)
	}
	root := tree.RootNode()

	result := &UnifiedAST{
		Language: "golang",
		FilePath: filePath,
	}

	// Extract imports
	for _, imp := range findChildren(root, "import_declaration") {
		result.Imports = append(result.Imports, e.parseImport(imp, src))
	}
	for _, imp := range findChildren(root, "import_spec") {
		// if they were already captured by import_declaration, skip duplicates
		if parent := imp.Parent(); parent != nil && parent.Type() == "import_declaration" {
			continue
		}
		result.Imports = append(result.Imports, e.parseImportSpec(imp, src))
	}

	// Extract functions
	for _, fn := range findChildren(root, "function_declaration") {
		result.Functions = append(result.Functions, e.parseFunc(fn, filePath, src, false))
	}
	for _, fn := range findChildren(root, "method_declaration") {
		result.Functions = append(result.Functions, e.parseFunc(fn, filePath, src, true))
	}

	// Extract types
	for _, td := range findChildren(root, "type_declaration") {
		ts := findFirstChild(td, "type_spec")
		if ts == nil {
			continue
		}
		t := e.parseTypeSpec(ts, filePath, src)
		if t != nil {
			result.Types = append(result.Types, *t)
		}
	}

	// Extract variables
	for _, vd := range findChildren(root, "var_declaration") {
		result.Variables = append(result.Variables, e.parseVarSpecs(vd, src)...)
	}

	// Extract call sites
	result.CallSites = e.extractCallSites(root, filePath, src)
	// Extract variable bindings (for route group prefix derivation etc.)
	result.VarBindings = e.extractVarBindings(root, filePath, src)

	return result, nil
}

func (e *TreeSitterGoExtractor) parseImport(n *sitter.Node, src []byte) UnifiedImport {
	spec := findFirstChild(n, "import_spec")
	if spec == nil {
		pathNode := findFirstChild(n, "interpreted_string_literal")
		if pathNode == nil {
			return UnifiedImport{}
		}
		return UnifiedImport{Path: strings.Trim(tsText(pathNode, src), "\"")}
	}
	return e.parseImportSpec(spec, src)
}

func (e *TreeSitterGoExtractor) parseImportSpec(n *sitter.Node, src []byte) UnifiedImport {
	imp := UnifiedImport{}
	pathNode := findFirstChild(n, "interpreted_string_literal")
	if pathNode == nil {
		pathNode = findFirstChild(n, "raw_string_literal")
	}
	if pathNode != nil {
		imp.Path = strings.Trim(tsText(pathNode, src), "\"'")
	}
	nameNode := findFirstChild(n, "package_identifier")
	if nameNode == nil {
		nameNode = findFirstChild(n, "identifier")
	}
	if nameNode != nil {
		imp.Alias = tsText(nameNode, src)
	}
	if !strings.Contains(imp.Path, ".") {
		imp.IsStdLib = true
	}
	return imp
}

func (e *TreeSitterGoExtractor) parseFunc(n *sitter.Node, filePath string, src []byte, isMethod bool) UnifiedFunction {
	fn := UnifiedFunction{
		Location: SourceLocation{
			File:      filePath,
			LineStart: tsLine(n),
		},
	}
	if isMethod {
		// method_declaration: func, parameter_list(receiver), field_identifier(name), parameter_list(params), [type_identifier(return)], block
		recvList := findFirstChild(n, "parameter_list")
		if recvList != nil {
			// check if this parameter list is the receiver (first after func)
			fn.Receiver = tsText(recvList, src)
			// clean up receiver formatting
			fn.Receiver = strings.TrimSpace(strings.Trim(fn.Receiver, "()"))
		}
		nameNode := findFirstChild(n, "field_identifier")
		if nameNode != nil {
			fn.Name = tsText(nameNode, src)
		}
		// find second parameter_list for params
		foundFirst := false
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c.Type() == "parameter_list" {
				if !foundFirst {
					foundFirst = true
					continue
				}
				fn.Params = e.parseParams(c, src)
				break
			}
		}
		// find return type
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c.Type() == "type_identifier" || c.Type() == "qualified_type" || c.Type() == "pointer_type" {
				fn.Returns = append(fn.Returns, tsText(c, src))
			}
		}
	} else {
		// function_declaration: func, identifier(name), parameter_list(params), [type_identifier(return)], block
		nameNode := findFirstChild(n, "identifier")
		if nameNode != nil {
			fn.Name = tsText(nameNode, src)
		}
		paramList := findFirstChild(n, "parameter_list")
		if paramList != nil {
			fn.Params = e.parseParams(paramList, src)
		}
		// find return type
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c.Type() == "type_identifier" || c.Type() == "qualified_type" || c.Type() == "pointer_type" {
				fn.Returns = append(fn.Returns, tsText(c, src))
			}
		}
	}

	fn.IsExported = isExported(fn.Name)

	// body snippet
	block := findFirstChild(n, "block")
	if block != nil {
		body := tsText(block, src)
		fn.BodySnippet = truncateBody(body)
	}

	return fn
}

func (e *TreeSitterGoExtractor) parseParams(n *sitter.Node, src []byte) []UnifiedParam {
	var params []UnifiedParam
	if n == nil {
		return params
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() != "parameter_declaration" {
			continue
		}
		var names []string
		var typ string
		for j := 0; j < int(c.ChildCount()); j++ {
			child := c.Child(j)
			switch child.Type() {
			case "identifier":
				names = append(names, tsText(child, src))
			case "type_identifier", "qualified_type", "pointer_type", "slice_type", "array_type", "map_type", "function_type":
				typ = tsText(child, src)
			case "variadic_parameter_declaration":
				typ = tsText(child, src)
			}
		}
		if len(names) == 0 && typ != "" {
			params = append(params, UnifiedParam{Type: typ})
		}
		for _, name := range names {
			params = append(params, UnifiedParam{Name: name, Type: typ})
		}
	}
	return params
}

func (e *TreeSitterGoExtractor) parseTypeSpec(n *sitter.Node, filePath string, src []byte) *UnifiedType {
	nameNode := findFirstChild(n, "type_identifier")
	if nameNode == nil {
		return nil
	}
	t := &UnifiedType{
		Name:       tsText(nameNode, src),
		IsExported: isExported(tsText(nameNode, src)),
		Location: SourceLocation{
			File:      filePath,
			LineStart: tsLine(n),
		},
	}

	structType := findFirstChild(n, "struct_type")
	if structType != nil {
		t.Kind = "struct"
		fieldList := findFirstChild(structType, "field_declaration_list")
		if fieldList != nil {
			for i := 0; i < int(fieldList.ChildCount()); i++ {
				f := fieldList.Child(i)
				if f.Type() != "field_declaration" {
					continue
				}
				field := e.parseField(f, filePath, src)
				if field != nil {
					t.Fields = append(t.Fields, *field)
				}
			}
		}
	}

	interfaceType := findFirstChild(n, "interface_type")
	if interfaceType != nil {
		t.Kind = "interface"
		methodList := findFirstChild(interfaceType, "method_spec_list")
		if methodList != nil {
			for i := 0; i < int(methodList.ChildCount()); i++ {
				m := methodList.Child(i)
				if m.Type() != "method_spec" {
					continue
				}
				nameNode := findFirstChild(m, "field_identifier")
				if nameNode == nil {
					continue
				}
				ms := UnifiedMethodSignature{
					Name:       tsText(nameNode, src),
					IsExported: isExported(tsText(nameNode, src)),
					Location:   SourceLocation{File: filePath, LineStart: tsLine(m)},
				}
				paramList := findFirstChild(m, "parameter_list")
				if paramList != nil {
					ms.Params = e.parseParams(paramList, src)
				}
				// Find return type in method spec
				for j := 0; j < int(m.ChildCount()); j++ {
					child := m.Child(j)
					if child.Type() == "type_identifier" || child.Type() == "qualified_type" {
						ms.Returns = append(ms.Returns, tsText(child, src))
					}
				}
				t.Methods = append(t.Methods, ms)
			}
		}
	}

	return t
}

func (e *TreeSitterGoExtractor) parseField(n *sitter.Node, filePath string, src []byte) *UnifiedField {
	// 直接从 field_declaration 原始文本解析，不再依赖子节点类型匹配
	// tree-sitter-go 不同版本中子节点命名差异极大（如 field/identifier/primitive_type/tag 等节点有无/命名不定）
	text := strings.TrimSpace(tsText(n, src))
	if text == "" {
		return nil
	}

	// 提取 tag（反引号包裹的部分）
	var tag string
	if backIdx := strings.LastIndex(text, "`"); backIdx > 0 {
		if startIdx := strings.LastIndex(text[:backIdx], "`"); startIdx >= 0 {
			tag = text[startIdx+1 : backIdx]
			text = strings.TrimSpace(text[:startIdx])
		}
	}

	// 逗号分隔的多字段声明：Name1, Name2 Type
	if commaIdx := strings.Index(text, ","); commaIdx > 0 && !strings.Contains(text[:commaIdx], " ") {
		beforeComma := strings.TrimSpace(text[:commaIdx])
		afterComma := strings.TrimSpace(text[commaIdx+1:])
		typeStart := strings.IndexFunc(afterComma, func(r rune) bool { return r == ' ' || r == '\t' })
		if typeStart >= 0 {
			typ := strings.TrimSpace(afterComma[typeStart:])
			return &UnifiedField{
				Name:       beforeComma,
				Type:       typ,
				Tag:        tag,
				IsExported: isExported(beforeComma),
				Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
			}
		}
		return &UnifiedField{
			Name:       beforeComma,
			Type:       afterComma,
			Tag:        tag,
			IsExported: isExported(beforeComma),
			Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
		}
	}

	// 找到第一个空白符，前面是名字，后面是类型
	firstSpace := strings.IndexFunc(text, func(r rune) bool { return r == ' ' || r == '\t' })
	if firstSpace < 0 {
		// 单 token：匿名嵌入字段（如 jwt.StandardClaims）
		typ := text
		name := text
		if idx := strings.LastIndex(text, "."); idx >= 0 {
			name = text[idx+1:]
		}
		return &UnifiedField{
			Name:       name,
			Type:       typ,
			Tag:        tag,
			IsExported: isExported(name),
			Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
		}
	}

	name := strings.TrimSpace(text[:firstSpace])
	typ := strings.TrimSpace(text[firstSpace:])
	return &UnifiedField{
		Name:       name,
		Type:       typ,
		Tag:        tag,
		IsExported: isExported(name),
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
	}
}

func (e *TreeSitterGoExtractor) parseVarSpecs(n *sitter.Node, src []byte) []UnifiedVariable {
	var vars []UnifiedVariable
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() != "var_spec" && c.Type() != "const_spec" {
			continue
		}
		nameNode := findFirstChild(c, "identifier")
		if nameNode == nil {
			continue
		}
		var typ string
		for j := 0; j < int(c.ChildCount()); j++ {
			child := c.Child(j)
			if child.Type() == "type_identifier" || child.Type() == "qualified_type" {
				typ = tsText(child, src)
			}
		}
		vars = append(vars, UnifiedVariable{
			Name:       tsText(nameNode, src),
			Type:       typ,
			IsExported: isExported(tsText(nameNode, src)),
		})
	}
	return vars
}

func (e *TreeSitterGoExtractor) extractCallSites(root *sitter.Node, filePath string, src []byte) []UnifiedCallSite {
	var sites []UnifiedCallSite
	for _, call := range findChildren(root, "call_expression") {
		funcNode := call.Child(0)
		if funcNode == nil {
			continue
		}
		site := UnifiedCallSite{
			Location: SourceLocation{
				File:      filePath,
				LineStart: tsLine(call),
			},
		}
		switch funcNode.Type() {
		case "identifier":
			site.TargetFunc = tsText(funcNode, src)
		case "selector_expression":
			pkgNode := findFirstChild(funcNode, "identifier")
			if pkgNode != nil {
				site.TargetPkg = tsText(pkgNode, src)
				site.ReceiverVar = site.TargetPkg
			}
			selNode := findFirstChild(funcNode, "field_identifier")
			if selNode != nil {
				site.TargetFunc = tsText(selNode, src)
			}
		case "field_identifier":
			site.TargetFunc = tsText(funcNode, src)
		}

		// 提取参数
		argList := findFirstChild(call, "argument_list")
		if argList != nil {
			for i := 0; i < int(argList.ChildCount()); i++ {
				arg := argList.Child(i)
				if arg == nil || arg.Type() == "," || arg.Type() == "(" || arg.Type() == ")" {
					continue
				}
				site.Arguments = append(site.Arguments, tsText(arg, src))
			}
		}

		sites = append(sites, site)
	}
	return sites
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	return strings.ToUpper(name[:1]) == name[:1]
}

func truncateBody(body string) string {
	if len(body) > 5000 {
		return body[:5000] + "..."
	}
	return body
}

func (e *TreeSitterGoExtractor) extractVarBindings(root *sitter.Node, filePath string, src []byte) []UnifiedVarBinding {
	var bindings []UnifiedVarBinding
	for _, stmt := range findChildren(root, "short_var_declaration") {
		bindings = append(bindings, e.parseVarBinding(stmt, filePath, src)...)
	}
	for _, decl := range findChildren(root, "var_declaration") {
		for _, spec := range findChildren(decl, "var_spec") {
			bindings = append(bindings, e.parseVarSpecBinding(spec, filePath, src)...)
		}
	}
	for _, stmt := range findChildren(root, "assignment_statement") {
		bindings = append(bindings, e.parseAssignmentBinding(stmt, filePath, src)...)
	}
	return bindings
}

func (e *TreeSitterGoExtractor) parseVarBinding(n *sitter.Node, filePath string, src []byte) []UnifiedVarBinding {
	var leftExpr, rightExpr *sitter.Node
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "expression_list" {
			if leftExpr == nil {
				leftExpr = c
			} else {
				rightExpr = c
			}
		}
	}
	if leftExpr == nil || rightExpr == nil {
		return nil
	}
	return e.bindingsFromExprLists(leftExpr, rightExpr, filePath, src)
}

func (e *TreeSitterGoExtractor) parseAssignmentBinding(n *sitter.Node, filePath string, src []byte) []UnifiedVarBinding {
	var leftExpr, rightExpr *sitter.Node
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "expression_list" {
			if leftExpr == nil {
				leftExpr = c
			} else {
				rightExpr = c
			}
		}
	}
	if leftExpr == nil || rightExpr == nil {
		return nil
	}
	return e.bindingsFromExprLists(leftExpr, rightExpr, filePath, src)
}

func (e *TreeSitterGoExtractor) parseVarSpecBinding(n *sitter.Node, filePath string, src []byte) []UnifiedVarBinding {
	var name string
	var rightNode *sitter.Node
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "identifier" && name == "" {
			name = tsText(c, src)
		}
		if c.Type() == "call_expression" || c.Type() == "expression_list" {
			rightNode = c
		}
	}
	if name == "" || rightNode == nil {
		return nil
	}
	b := UnifiedVarBinding{VarName: name}
	if rightNode.Type() == "call_expression" {
		b.CallSite = e.callSiteFromNode(rightNode, filePath, src)
	} else {
		b.Literal = tsText(rightNode, src)
	}
	return []UnifiedVarBinding{b}
}

func (e *TreeSitterGoExtractor) bindingsFromExprLists(left, right *sitter.Node, filePath string, src []byte) []UnifiedVarBinding {
	var bindings []UnifiedVarBinding
	li, ri := 0, 0
	for li < int(left.ChildCount()) && ri < int(right.ChildCount()) {
		lc := left.Child(li)
		rc := right.Child(ri)
		if lc == nil || lc.Type() == "," {
			li++
			continue
		}
		if rc == nil || rc.Type() == "," {
			ri++
			continue
		}
		if lc.Type() == "identifier" {
			name := tsText(lc, src)
			b := UnifiedVarBinding{VarName: name}
			if rc.Type() == "call_expression" {
				b.CallSite = e.callSiteFromNode(rc, filePath, src)
			} else {
				b.Literal = tsText(rc, src)
			}
			bindings = append(bindings, b)
		}
		li++
		ri++
	}
	return bindings
}

func (e *TreeSitterGoExtractor) callSiteFromNode(call *sitter.Node, filePath string, src []byte) *UnifiedCallSite {
	funcNode := call.Child(0)
	if funcNode == nil {
		return nil
	}
	site := &UnifiedCallSite{
		Location: SourceLocation{File: filePath, LineStart: tsLine(call)},
	}
	switch funcNode.Type() {
	case "identifier":
		site.TargetFunc = tsText(funcNode, src)
	case "selector_expression":
		pkgNode := findFirstChild(funcNode, "identifier")
		if pkgNode != nil {
			site.TargetPkg = tsText(pkgNode, src)
			site.ReceiverVar = site.TargetPkg
		}
		selNode := findFirstChild(funcNode, "field_identifier")
		if selNode != nil {
			site.TargetFunc = tsText(selNode, src)
		}
	case "field_identifier":
		site.TargetFunc = tsText(funcNode, src)
	}
	argList := findFirstChild(call, "argument_list")
	if argList != nil {
		for i := 0; i < int(argList.ChildCount()); i++ {
			arg := argList.Child(i)
			if arg == nil || arg.Type() == "," || arg.Type() == "(" || arg.Type() == ")" {
				continue
			}
			site.Arguments = append(site.Arguments, tsText(arg, src))
		}
	}
	return site
}
