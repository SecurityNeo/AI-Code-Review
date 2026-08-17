//go:build cgo

package parser

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

// TreeSitterPythonExtractor Python语言AST提取器（基于Tree-sitter）
type TreeSitterPythonExtractor struct{}

// NewTreeSitterPythonExtractor 创建Tree-sitter Python提取器
func NewTreeSitterPythonExtractor() *TreeSitterPythonExtractor {
	return &TreeSitterPythonExtractor{}
}

// Language 返回语言
func (e *TreeSitterPythonExtractor) Language() string { return "python" }

// FileExts 返回文件扩展名
func (e *TreeSitterPythonExtractor) FileExts() []string { return []string{".py"} }

// ParseFile 解析Python文件
func (e *TreeSitterPythonExtractor) ParseFile(filePath string, src []byte) (*UnifiedAST, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(python.GetLanguage())
	tree := parser.Parse(nil, src)
	if tree == nil {
		return nil, fmt.Errorf("tree-sitter parse failed for %s", filePath)
	}
	root := tree.RootNode()

	result := &UnifiedAST{
		Language: "python",
		FilePath: filePath,
	}

	// Extract imports
	for _, imp := range findChildren(root, "import_statement") {
		result.Imports = append(result.Imports, e.parseImport(imp, src))
	}
	for _, imp := range findChildren(root, "import_from_statement") {
		result.Imports = append(result.Imports, e.parseImportFrom(imp, src))
	}

	// Extract functions
	for _, fn := range findChildren(root, "function_definition") {
		result.Functions = append(result.Functions, e.parseFunc(fn, filePath, src))
	}

	// Extract classes
	for _, cls := range findChildren(root, "class_definition") {
		t := e.parseClass(cls, filePath, src)
		if t != nil {
			result.Types = append(result.Types, *t)
		}
	}

	// Extract call sites
	result.CallSites = e.extractCallSites(root, filePath, src)

	return result, nil
}

func (e *TreeSitterPythonExtractor) parseImport(n *sitter.Node, src []byte) UnifiedImport {
	imp := UnifiedImport{}
	dotted := findFirstChild(n, "dotted_name")
	if dotted != nil {
		imp.Path = tsText(dotted, src)
	}
	return imp
}

func (e *TreeSitterPythonExtractor) parseImportFrom(n *sitter.Node, src []byte) UnifiedImport {
	imp := UnifiedImport{}
	dotted := findFirstChild(n, "dotted_name")
	if dotted != nil {
		imp.Path = tsText(dotted, src)
	}
	return imp
}

func (e *TreeSitterPythonExtractor) parseFunc(n *sitter.Node, filePath string, src []byte) UnifiedFunction {
	fn := UnifiedFunction{
		Location: SourceLocation{File: filePath, LineStart: tsLine(n)},
	}

	nameNode := findFirstChild(n, "identifier")
	if nameNode != nil {
		fn.Name = tsText(nameNode, src)
	}

	// Check for async keyword
	for i := 0; i < int(n.ChildCount()); i++ {
		if n.Child(i).Type() == "async" {
			fn.IsAsync = true
			break
		}
	}

	fn.IsExported = !strings.HasPrefix(fn.Name, "_")

	// 提取装饰器（如 @app.get("/path")）
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "decorator" {
			annot := strings.TrimSpace(tsText(c, src))
			if annot != "" {
				fn.Annotations = append(fn.Annotations, annot)
			}
		}
	}

	// parameters
	paramsNode := findFirstChild(n, "parameters")
	if paramsNode != nil {
		fn.Params = e.parseParams(paramsNode, src)
	}

	// body snippet
	bodyNode := findFirstChild(n, "block")
	if bodyNode != nil {
		fn.BodySnippet = truncateBody(tsText(bodyNode, src))
	}

	return fn
}

func (e *TreeSitterPythonExtractor) parseParams(n *sitter.Node, src []byte) []UnifiedParam {
	var params []UnifiedParam
	if n == nil {
		return params
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() != "identifier" && c.Type() != "typed_parameter" && c.Type() != "default_parameter" {
			continue
		}
		var name, typ string
		var annotations []string
		switch c.Type() {
		case "identifier":
			name = tsText(c, src)
		case "typed_parameter":
			nameNode := findFirstChild(c, "identifier")
			if nameNode != nil {
				name = tsText(nameNode, src)
			}
			for j := 0; j < int(c.ChildCount()); j++ {
				child := c.Child(j)
				if child.Type() == "type" {
					typ = tsText(child, src)
				}
			}
		case "default_parameter":
			nameNode := findFirstChild(c, "identifier")
			if nameNode != nil {
				name = tsText(nameNode, src)
			}
			// 提取类型注解和默认值中的注解（如 FastAPI Query/Body/Header）
			for j := 0; j < int(c.ChildCount()); j++ {
				child := c.Child(j)
				switch child.Type() {
				case "type":
					typ = tsText(child, src)
				case "call":
					// 默认值是函数调用，如 Query(...)、Body(...)
					funcNode := child.Child(0)
					if funcNode != nil {
						annotations = append(annotations, tsText(funcNode, src))
					}
				case "identifier":
					// 默认值是标识符，可能是别名导入的 Query。
					// 使用 tree-sitter Node.ID() 比较节点身份：跳过参数名自身，只收集其他标识符。
					if nameNode != nil && child.ID() != nameNode.ID() {
						annotations = append(annotations, tsText(child, src))
					}
				}
			}
		}
		if name == "self" || name == "cls" {
			continue
		}
		params = append(params, UnifiedParam{Name: name, Type: typ, Annotations: annotations})
	}
	return params
}

func (e *TreeSitterPythonExtractor) parseClass(n *sitter.Node, filePath string, src []byte) *UnifiedType {
	nameNode := findFirstChild(n, "identifier")
	if nameNode == nil {
		return nil
	}
	t := &UnifiedType{
		Name:       tsText(nameNode, src),
		Kind:       "class",
		IsExported: !strings.HasPrefix(tsText(nameNode, src), "_"),
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
	}

	// bases
	argsNode := findFirstChild(n, "argument_list")
	if argsNode != nil {
		for i := 0; i < int(argsNode.ChildCount()); i++ {
			c := argsNode.Child(i)
			if c.Type() == "identifier" || c.Type() == "attribute" {
				base := tsText(c, src)
				if base != "object" {
					t.Extends = append(t.Extends, base)
				}
			}
		}
	}

	// methods & fields from body
	bodyNode := findFirstChild(n, "block")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			c := bodyNode.Child(i)
			if c.Type() == "function_definition" {
				fn := e.parseFunc(c, filePath, src)
				ms := UnifiedMethodSignature{
					Name:       fn.Name,
					IsExported: fn.IsExported,
					Params:     fn.Params,
					Location:   fn.Location,
				}
				t.Methods = append(t.Methods, ms)
			}
			if c.Type() == "expression_statement" {
				// simple field detection: self.x = ...
				child := c.Child(0)
				if child != nil && child.Type() == "assignment" {
					left := child.Child(0)
					if left != nil && left.Type() == "attribute" {
						attrName := findFirstChild(left, "identifier")
						if attrName != nil {
							t.Fields = append(t.Fields, UnifiedField{
								Name: tsText(attrName, src),
							})
						}
					}
				}
			}
		}
	}

	return t
}

func (e *TreeSitterPythonExtractor) extractCallSites(root *sitter.Node, filePath string, src []byte) []UnifiedCallSite {
	var sites []UnifiedCallSite
	for _, call := range findChildren(root, "call") {
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
		case "attribute":
			pkgNode := findFirstChild(funcNode, "identifier")
			if pkgNode != nil {
				// first identifier is the object, last is the method
				// simplified: just take the text
				parts := strings.Split(tsText(funcNode, src), ".")
				if len(parts) >= 2 {
					site.TargetPkg = parts[0]
					site.TargetFunc = parts[len(parts)-1]
				}
			}
		}
		sites = append(sites, site)
	}
	return sites
}
