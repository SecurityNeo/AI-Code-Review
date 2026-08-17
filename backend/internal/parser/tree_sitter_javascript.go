//go:build cgo

package parser

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
)

// TreeSitterJavaScriptExtractor JavaScript/TypeScript语言AST提取器（基于Tree-sitter）
type TreeSitterJavaScriptExtractor struct {
	language string
	exts     []string
}

// NewTreeSitterJavaScriptExtractor 创建Tree-sitter JS提取器
func NewTreeSitterJavaScriptExtractor() *TreeSitterJavaScriptExtractor {
	return &TreeSitterJavaScriptExtractor{language: "javascript", exts: []string{".js", ".jsx"}}
}

// NewTreeSitterTypeScriptExtractor 创建Tree-sitter TS提取器
func NewTreeSitterTypeScriptExtractor() *TreeSitterJavaScriptExtractor {
	return &TreeSitterJavaScriptExtractor{language: "typescript", exts: []string{".ts", ".tsx"}}
}

// Language 返回语言
func (e *TreeSitterJavaScriptExtractor) Language() string { return e.language }

// FileExts 返回文件扩展名
func (e *TreeSitterJavaScriptExtractor) FileExts() []string { return e.exts }

// ParseFile 解析JS/TS文件
func (e *TreeSitterJavaScriptExtractor) ParseFile(filePath string, src []byte) (*UnifiedAST, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(javascript.GetLanguage())
	tree := parser.Parse(nil, src)
	if tree == nil {
		return nil, fmt.Errorf("tree-sitter parse failed for %s", filePath)
	}
	root := tree.RootNode()

	result := &UnifiedAST{
		Language: e.language,
		FilePath: filePath,
	}

	// Extract imports
	for _, imp := range findChildren(root, "import_statement") {
		result.Imports = append(result.Imports, e.parseImport(imp, src))
	}

	// Extract functions
	for _, fn := range findChildren(root, "function_declaration") {
		result.Functions = append(result.Functions, e.parseFunc(fn, filePath, src))
	}
	for _, fn := range findChildren(root, "arrow_function") {
		// arrow functions usually assigned to variables - simplified
		result.Functions = append(result.Functions, e.parseArrowFunc(fn, filePath, src))
	}

	// Extract classes
	for _, cls := range findChildren(root, "class_declaration") {
		t := e.parseClass(cls, filePath, src)
		if t != nil {
			result.Types = append(result.Types, *t)
		}
	}

	// Extract interfaces (TS only)
	if e.language == "typescript" {
		for _, iface := range findChildren(root, "interface_declaration") {
			t := e.parseInterface(iface, filePath, src)
			if t != nil {
				result.Types = append(result.Types, *t)
			}
		}
	}

	// Extract call sites
	result.CallSites = e.extractCallSites(root, filePath, src)

	return result, nil
}

func (e *TreeSitterJavaScriptExtractor) parseImport(n *sitter.Node, src []byte) UnifiedImport {
	imp := UnifiedImport{}
	source := findFirstChild(n, "string")
	if source == nil {
		source = findFirstChild(n, "string_fragment")
	}
	if source != nil {
		imp.Path = strings.Trim(tsText(source, src), `"'`)
	}
	return imp
}

func (e *TreeSitterJavaScriptExtractor) parseFunc(n *sitter.Node, filePath string, src []byte) UnifiedFunction {
	fn := UnifiedFunction{
		Location: SourceLocation{File: filePath, LineStart: tsLine(n)},
	}

	nameNode := findFirstChild(n, "identifier")
	if nameNode != nil {
		fn.Name = tsText(nameNode, src)
	}

	// async check
	for i := 0; i < int(n.ChildCount()); i++ {
		if n.Child(i).Type() == "async" {
			fn.IsAsync = true
			break
		}
	}

	fn.IsExported = e.isExported(n, src)

	paramsNode := findFirstChild(n, "formal_parameters")
	if paramsNode != nil {
		fn.Params = e.parseParams(paramsNode, src)
	}

	bodyNode := findFirstChild(n, "statement_block")
	if bodyNode != nil {
		fn.BodySnippet = truncateBody(tsText(bodyNode, src))
	}

	return fn
}

func (e *TreeSitterJavaScriptExtractor) parseArrowFunc(n *sitter.Node, filePath string, src []byte) UnifiedFunction {
	fn := UnifiedFunction{
		Name:       "<arrow>",
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
		IsExported: false,
	}
	paramsNode := findFirstChild(n, "formal_parameters")
	if paramsNode != nil {
		fn.Params = e.parseParams(paramsNode, src)
	}
	bodyNode := findFirstChild(n, "statement_block")
	if bodyNode != nil {
		fn.BodySnippet = truncateBody(tsText(bodyNode, src))
	}
	return fn
}

func (e *TreeSitterJavaScriptExtractor) parseParams(n *sitter.Node, src []byte) []UnifiedParam {
	var params []UnifiedParam
	if n == nil {
		return params
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() != "identifier" && c.Type() != "formal_parameter" && c.Type() != "required_parameter" && c.Type() != "optional_parameter" {
			continue
		}
		var name, typ string
		switch c.Type() {
		case "identifier":
			name = tsText(c, src)
		default:
			idNode := findFirstChild(c, "identifier")
			if idNode != nil {
				name = tsText(idNode, src)
			}
			typeAnnotation := findFirstChild(c, "type_annotation")
			if typeAnnotation != nil {
				typ = strings.TrimPrefix(tsText(typeAnnotation, src), ": ")
			}
		}
		params = append(params, UnifiedParam{Name: name, Type: typ})
	}
	return params
}

func (e *TreeSitterJavaScriptExtractor) parseClass(n *sitter.Node, filePath string, src []byte) *UnifiedType {
	nameNode := findFirstChild(n, "identifier")
	if nameNode == nil {
		return nil
	}
	t := &UnifiedType{
		Name:       tsText(nameNode, src),
		Kind:       "class",
		IsExported: e.isExported(n, src),
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
	}

	// extends
	extendsNode := findFirstChild(n, "class_heritage")
	if extendsNode != nil {
		for i := 0; i < int(extendsNode.ChildCount()); i++ {
			c := extendsNode.Child(i)
			if c.Type() == "identifier" {
				t.Extends = append(t.Extends, tsText(c, src))
			}
		}
	}

	// body
	bodyNode := findFirstChild(n, "class_body")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			c := bodyNode.Child(i)
			if c.Type() == "method_definition" {
				fn := e.parseMethodDef(c, filePath, src)
				ms := UnifiedMethodSignature{
					Name:       fn.Name,
					IsExported: fn.IsExported,
					Params:     fn.Params,
					Location:   fn.Location,
				}
				t.Methods = append(t.Methods, ms)
			}
			if c.Type() == "field_definition" {
				field := e.parseFieldDef(c, src)
				if field != nil {
					t.Fields = append(t.Fields, *field)
				}
			}
		}
	}

	return t
}

func (e *TreeSitterJavaScriptExtractor) parseInterface(n *sitter.Node, filePath string, src []byte) *UnifiedType {
	nameNode := findFirstChild(n, "type_identifier")
	if nameNode == nil {
		nameNode = findFirstChild(n, "identifier")
	}
	if nameNode == nil {
		return nil
	}
	t := &UnifiedType{
		Name:       tsText(nameNode, src),
		Kind:       "interface",
		IsExported: e.isExported(n, src),
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
	}

	// extends
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "interface_heritage" {
			for j := 0; j < int(c.ChildCount()); j++ {
				child := c.Child(j)
				if child.Type() == "type_identifier" || child.Type() == "identifier" {
					t.Extends = append(t.Extends, tsText(child, src))
				}
			}
		}
	}

	// body
	bodyNode := findFirstChild(n, "interface_body")
	if bodyNode == nil {
		bodyNode = findFirstChild(n, "object_type")
	}
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			c := bodyNode.Child(i)
			if c.Type() == "method_signature" || c.Type() == "method_definition" {
				nameNode := findFirstChild(c, "property_identifier")
				if nameNode == nil {
					nameNode = findFirstChild(c, "identifier")
				}
				if nameNode != nil {
					ms := UnifiedMethodSignature{
						Name:       tsText(nameNode, src),
						IsExported: isExported(tsText(nameNode, src)),
						Location:   SourceLocation{File: filePath, LineStart: tsLine(c)},
					}
					paramsNode := findFirstChild(c, "formal_parameters")
					if paramsNode != nil {
						ms.Params = e.parseParams(paramsNode, src)
					}
					t.Methods = append(t.Methods, ms)
				}
			}
			if c.Type() == "property_signature" {
				field := e.parseFieldDef(c, src)
				if field != nil {
					t.Fields = append(t.Fields, *field)
				}
			}
		}
	}

	return t
}

func (e *TreeSitterJavaScriptExtractor) parseMethodDef(n *sitter.Node, filePath string, src []byte) UnifiedFunction {
	fn := UnifiedFunction{
		Location: SourceLocation{File: filePath, LineStart: tsLine(n)},
	}
	nameNode := findFirstChild(n, "property_identifier")
	if nameNode == nil {
		nameNode = findFirstChild(n, "identifier")
	}
	if nameNode != nil {
		fn.Name = tsText(nameNode, src)
	}
	fn.IsExported = isExported(fn.Name)
	paramsNode := findFirstChild(n, "formal_parameters")
	if paramsNode != nil {
		fn.Params = e.parseParams(paramsNode, src)
	}
	bodyNode := findFirstChild(n, "statement_block")
	if bodyNode != nil {
		fn.BodySnippet = truncateBody(tsText(bodyNode, src))
	}
	return fn
}

func (e *TreeSitterJavaScriptExtractor) parseFieldDef(n *sitter.Node, src []byte) *UnifiedField {
	nameNode := findFirstChild(n, "property_identifier")
	if nameNode == nil {
		nameNode = findFirstChild(n, "identifier")
	}
	if nameNode == nil {
		return nil
	}
	return &UnifiedField{
		Name:       tsText(nameNode, src),
		IsExported: isExported(tsText(nameNode, src)),
	}
}

func (e *TreeSitterJavaScriptExtractor) extractCallSites(root *sitter.Node, filePath string, src []byte) []UnifiedCallSite {
	var sites []UnifiedCallSite
	for _, call := range findChildren(root, "call_expression") {
		funcNode := call.Child(0)
		if funcNode == nil {
			continue
		}
		site := UnifiedCallSite{
			Location: SourceLocation{File: filePath, LineStart: tsLine(call)},
		}
		switch funcNode.Type() {
		case "identifier":
			site.TargetFunc = tsText(funcNode, src)
		case "member_expression":
			objNode := findFirstChild(funcNode, "identifier")
			if objNode != nil {
				site.TargetPkg = tsText(objNode, src)
			}
			propNode := findFirstChild(funcNode, "property_identifier")
			if propNode != nil {
				site.TargetFunc = tsText(propNode, src)
			}
		}
		sites = append(sites, site)
	}
	return sites
}

func (e *TreeSitterJavaScriptExtractor) isExported(n *sitter.Node, src []byte) bool {
	// Check parent for export keyword
	parent := n.Parent()
	if parent != nil && parent.Type() == "export_statement" {
		return true
	}
	// Check preceding modifiers
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "export" {
			return true
		}
	}
	return false
}
