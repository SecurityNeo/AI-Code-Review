//go:build cgo

package parser

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"
)

// TreeSitterJavaExtractor Java语言AST提取器（基于Tree-sitter）
type TreeSitterJavaExtractor struct{}

// NewTreeSitterJavaExtractor 创建Tree-sitter Java提取器
func NewTreeSitterJavaExtractor() *TreeSitterJavaExtractor {
	return &TreeSitterJavaExtractor{}
}

// Language 返回语言
func (e *TreeSitterJavaExtractor) Language() string { return "java" }

// FileExts 返回文件扩展名
func (e *TreeSitterJavaExtractor) FileExts() []string { return []string{".java"} }

// ParseFile 解析Java文件
func (e *TreeSitterJavaExtractor) ParseFile(filePath string, src []byte) (*UnifiedAST, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(java.GetLanguage())
	tree := parser.Parse(nil, src)
	if tree == nil {
		return nil, fmt.Errorf("tree-sitter parse failed for %s", filePath)
	}
	root := tree.RootNode()

	result := &UnifiedAST{
		Language: "java",
		FilePath: filePath,
	}

	// Extract imports
	for _, imp := range findChildren(root, "import_declaration") {
		result.Imports = append(result.Imports, e.parseImport(imp, src))
	}

	// Extract classes/interfaces/enums
	for _, decl := range []string{"class_declaration", "interface_declaration", "enum_declaration"} {
		for _, node := range findChildren(root, decl) {
			t := e.parseType(node, filePath, src, decl)
			if t != nil {
				result.Types = append(result.Types, *t)
			}
		}
	}

	// Extract methods
	for _, node := range findChildren(root, "method_declaration") {
		fn := e.parseMethod(node, filePath, src)
		if fn != nil {
			result.Functions = append(result.Functions, *fn)
		}
	}
	// Also constructor_declaration
	for _, node := range findChildren(root, "constructor_declaration") {
		fn := e.parseConstructor(node, filePath, src)
		if fn != nil {
			result.Functions = append(result.Functions, *fn)
		}
	}

	// Extract call sites
	result.CallSites = e.extractCallSites(root, filePath, src)

	return result, nil
}

func (e *TreeSitterJavaExtractor) parseImport(n *sitter.Node, src []byte) UnifiedImport {
	imp := UnifiedImport{}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "scoped_identifier" || c.Type() == "identifier" {
			imp.Path = tsText(c, src)
		}
	}
	if !strings.Contains(imp.Path, ".") {
		imp.IsStdLib = true
	}
	return imp
}

func (e *TreeSitterJavaExtractor) parseType(n *sitter.Node, filePath string, src []byte, declType string) *UnifiedType {
	nameNode := findFirstChild(n, "identifier")
	if nameNode == nil {
		return nil
	}
	t := &UnifiedType{
		Name:       tsText(nameNode, src),
		IsExported: isExported(tsText(nameNode, src)),
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
	}
	switch declType {
	case "class_declaration":
		t.Kind = "class"
	case "interface_declaration":
		t.Kind = "interface"
	case "enum_declaration":
		t.Kind = "enum"
	}

	// generics
	typeParams := findFirstChild(n, "type_parameters")
	if typeParams != nil {
		for i := 0; i < int(typeParams.ChildCount()); i++ {
			c := typeParams.Child(i)
			if c.Type() == "type_parameter" {
				t.Generics = append(t.Generics, tsText(c, src))
			}
		}
	}

	// extends
	extends := findFirstChild(n, "superclass")
	if extends != nil {
		for i := 0; i < int(extends.ChildCount()); i++ {
			c := extends.Child(i)
			if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" {
				t.Extends = append(t.Extends, tsText(c, src))
			}
		}
	}

	// implements
	implements := findFirstChild(n, "super_interfaces")
	if implements != nil {
		for i := 0; i < int(implements.ChildCount()); i++ {
			c := implements.Child(i)
			if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" {
				t.Implements = append(t.Implements, tsText(c, src))
			}
		}
	}

	// fields & methods from body
	body := findFirstChild(n, "class_body")
	if body == nil {
		body = findFirstChild(n, "interface_body")
	}
	if body != nil {
		for i := 0; i < int(body.ChildCount()); i++ {
			c := body.Child(i)
			if c.Type() == "field_declaration" {
				field := e.parseField(c, src)
				if field != nil {
					t.Fields = append(t.Fields, *field)
				}
			}
			if c.Type() == "method_declaration" {
				fn := e.parseMethod(c, filePath, src)
				if fn != nil {
					ms := UnifiedMethodSignature{
						Name:       fn.Name,
						IsExported: fn.IsExported,
						Params:     fn.Params,
						Location:   fn.Location,
					}
					t.Methods = append(t.Methods, ms)
				}
			}
		}
	}

	return t
}

func (e *TreeSitterJavaExtractor) parseField(n *sitter.Node, src []byte) *UnifiedField {
	var typ string
	var names []string
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		switch c.Type() {
		case "type_identifier", "scoped_type_identifier", "integral_type", "floating_point_type", "boolean_type":
			typ = tsText(c, src)
		case "variable_declarator":
			idNode := findFirstChild(c, "identifier")
			if idNode != nil {
				names = append(names, tsText(idNode, src))
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &UnifiedField{
		Name:       names[0],
		Type:       typ,
		IsExported: isExported(names[0]),
	}
}

func (e *TreeSitterJavaExtractor) parseMethod(n *sitter.Node, filePath string, src []byte) *UnifiedFunction {
	fn := &UnifiedFunction{
		Location: SourceLocation{File: filePath, LineStart: tsLine(n)},
	}

	nameNode := findFirstChild(n, "identifier")
	if nameNode != nil {
		fn.Name = tsText(nameNode, src)
	}

	fn.IsExported = isExported(fn.Name)

	// modifiers & annotations
	modifiersNode := findFirstChild(n, "modifiers")
	if modifiersNode != nil {
		modText := tsText(modifiersNode, src)
		fn.IsStatic = strings.Contains(modText, "static")
		for i := 0; i < int(modifiersNode.ChildCount()); i++ {
			c := modifiersNode.Child(i)
			if c.Type() == "marker_annotation" || c.Type() == "annotation" {
				annot := strings.TrimSpace(tsText(c, src))
				if annot != "" {
					fn.Annotations = append(fn.Annotations, annot)
				}
			}
		}
	}

	// params
	paramList := findFirstChild(n, "formal_parameters")
	if paramList != nil {
		fn.Params = e.parseParams(paramList, src)
	}

	// return type
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" ||
			c.Type() == "integral_type" || c.Type() == "floating_point_type" ||
			c.Type() == "boolean_type" || c.Type() == "void_type" {
			fn.Returns = append(fn.Returns, tsText(c, src))
		}
	}

	// body snippet + metrics
	body := findFirstChild(n, "block")
	if body != nil {
		bodyText := tsText(body, src)
		fn.BodySnippet = truncateBody(bodyText)
		fn.Complexity = EstimateComplexityFromSnippet(bodyText)
		fn.LOC = strings.Count(bodyText, "\n")
		fn.NestedDepth = estimateNestedDepthJava(body)
	}

	fn.SecurityRole = InferSecurityRoleJava(fn.Name, "")

	return fn
}

func (e *TreeSitterJavaExtractor) parseConstructor(n *sitter.Node, filePath string, src []byte) *UnifiedFunction {
	fn := &UnifiedFunction{
		Name:       "<init>",
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
		IsExported: true,
	}
	// modifiers & annotations
	modifiersNode := findFirstChild(n, "modifiers")
	if modifiersNode != nil {
		for i := 0; i < int(modifiersNode.ChildCount()); i++ {
			c := modifiersNode.Child(i)
			if c.Type() == "marker_annotation" || c.Type() == "annotation" {
				annot := strings.TrimSpace(tsText(c, src))
				if annot != "" {
					fn.Annotations = append(fn.Annotations, annot)
				}
			}
		}
	}
	paramList := findFirstChild(n, "formal_parameters")
	if paramList != nil {
		fn.Params = e.parseParams(paramList, src)
	}
	body := findFirstChild(n, "block")
	if body != nil {
		bodyText := tsText(body, src)
		fn.BodySnippet = truncateBody(bodyText)
		fn.Complexity = EstimateComplexityFromSnippet(bodyText)
		fn.LOC = strings.Count(bodyText, "\n")
		fn.NestedDepth = estimateNestedDepthJava(body)
	}
	return fn
}

func (e *TreeSitterJavaExtractor) parseParams(n *sitter.Node, src []byte) []UnifiedParam {
	var params []UnifiedParam
	if n == nil {
		return params
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() != "formal_parameter" {
			continue
		}
		var name, typ string
		var annotations []string
		for j := 0; j < int(c.ChildCount()); j++ {
			child := c.Child(j)
			switch child.Type() {
			case "identifier":
				name = tsText(child, src)
			case "type_identifier", "scoped_type_identifier", "integral_type", "floating_point_type", "boolean_type":
				typ = tsText(child, src)
			case "array_type":
				typ = tsText(child, src)
			case "annotation":
				annotations = append(annotations, tsText(child, src))
			case "modifiers":
				for k := 0; k < int(child.ChildCount()); k++ {
					modChild := child.Child(k)
					if modChild.Type() == "annotation" {
						annotations = append(annotations, tsText(modChild, src))
					}
				}
			}
		}
		params = append(params, UnifiedParam{Name: name, Type: typ, Annotations: annotations})
	}
	return params
}

func (e *TreeSitterJavaExtractor) extractCallSites(root *sitter.Node, filePath string, src []byte) []UnifiedCallSite {
	var sites []UnifiedCallSite
	for _, call := range findChildren(root, "method_invocation") {
		site := UnifiedCallSite{
			Location:   SourceLocation{File: filePath, LineStart: tsLine(call)},
			CallerFunc: findCallerFuncNameJava(call, src),
		}
		for i := 0; i < int(call.ChildCount()); i++ {
			c := call.Child(i)
			switch c.Type() {
			case "identifier":
				site.TargetFunc = tsText(c, src)
			case "field_access":
				pkgNode := findFirstChild(c, "identifier")
				if pkgNode != nil {
					site.TargetPkg = tsText(pkgNode, src)
				}
			case "argument_list":
				// arguments not collected in simplified version
			}
		}
		// also check for scoped method calls
		funcNode := call.Child(0)
		if funcNode != nil && funcNode.Type() == "field_access" {
			for j := 0; j < int(funcNode.ChildCount()); j++ {
				child := funcNode.Child(j)
				if child.Type() == "identifier" {
					if site.TargetPkg == "" {
						site.TargetPkg = tsText(child, src)
					} else {
						site.TargetFunc = tsText(child, src)
					}
				}
			}
		}
		sites = append(sites, site)
	}
	return sites
}

// findCallerFuncNameJava 从 method_invocation 向上遍历父节点，找到最近的函数/方法名
func findCallerFuncNameJava(call *sitter.Node, src []byte) string {
	for parent := call.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type() {
		case "method_declaration":
			nameNode := findFirstChild(parent, "identifier")
			if nameNode != nil {
				return tsText(nameNode, src)
			}
		case "constructor_declaration":
			return "<init>"
		}
	}
	return ""
}

// estimateNestedDepthJava 估算 Java 方法体的最大嵌套深度
func estimateNestedDepthJava(body *sitter.Node) int {
	if body == nil {
		return 0
	}
	maxDepth := 0
	var walk func(n *sitter.Node, depth int)
	walk = func(n *sitter.Node, depth int) {
		if n == nil {
			return
		}
		if depth > maxDepth {
			maxDepth = depth
		}
		switch n.Type() {
		case "if_statement", "switch_statement", "for_statement", "while_statement",
			"do_statement", "try_statement", "synchronized_statement":
			depth++
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i), depth)
		}
	}
	walk(body, 0)
	return maxDepth
}

// InferSecurityRoleJava 推断 Java 函数的安全角色
func InferSecurityRoleJava(name, receiver string) string {
	lower := strings.ToLower(name)
	recvLower := strings.ToLower(receiver)

	if strings.Contains(lower, "auth") || strings.Contains(lower, "login") || strings.Contains(lower, "logout") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "session") || strings.Contains(lower, "permission") ||
		strings.Contains(lower, "credential") || strings.Contains(lower, "jwt") || strings.Contains(lower, "oauth") {
		return "auth"
	}
	if strings.Contains(lower, "encrypt") || strings.Contains(lower, "decrypt") || strings.Contains(lower, "hash") ||
		strings.Contains(lower, "sign") || strings.Contains(lower, "verify") || strings.Contains(lower, "cipher") {
		return "encryption"
	}
	if strings.Contains(lower, "sanitiz") || strings.Contains(lower, "escape") || strings.Contains(lower, "clean") ||
		strings.Contains(lower, "filter") || strings.Contains(lower, "validat") || strings.Contains(lower, "check") {
		return "sanitizer"
	}
	if strings.Contains(lower, "parse") || strings.Contains(lower, "bind") || strings.Contains(lower, "decode") ||
		strings.Contains(lower, "deserialize") || strings.Contains(lower, "unmarshal") || strings.Contains(lower, "convert") {
		return "validator"
	}
	if strings.Contains(lower, "get") || strings.Contains(lower, "list") || strings.Contains(lower, "find") ||
		strings.Contains(lower, "fetch") || strings.Contains(lower, "query") || strings.Contains(lower, "search") ||
		strings.Contains(lower, "save") || strings.Contains(lower, "create") || strings.Contains(lower, "insert") ||
		strings.Contains(lower, "update") || strings.Contains(lower, "modify") || strings.Contains(lower, "delete") ||
		strings.Contains(lower, "remove") || strings.Contains(lower, "destroy") {
		return "data_access"
	}
	if strings.HasPrefix(lower, "handle") || strings.HasPrefix(lower, "serve") ||
		strings.Contains(recvLower, "handler") || strings.Contains(recvLower, "controller") ||
		strings.Contains(recvLower, "router") || strings.Contains(recvLower, "resource") ||
		strings.Contains(recvLower, "servlet") || strings.Contains(recvLower, "endpoint") {
		return "handler"
	}
	if strings.Contains(recvLower, "service") || strings.Contains(recvLower, "manager") ||
		strings.Contains(recvLower, "facade") || strings.Contains(recvLower, "biz") {
		return "service"
	}
	if strings.Contains(recvLower, "repo") || strings.Contains(recvLower, "dao") ||
		strings.Contains(recvLower, "store") || strings.Contains(recvLower, "database") {
		return "data_access"
	}
	return "other"
}
