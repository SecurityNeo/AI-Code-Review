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

	// body snippet + metrics
	bodyNode := findFirstChild(n, "block")
	if bodyNode != nil {
		body := tsText(bodyNode, src)
		fn.BodySnippet = truncateBody(body)
		fn.Complexity = EstimateComplexityFromSnippet(body)
		fn.LOC = strings.Count(body, "\n")
		fn.NestedDepth = estimateNestedDepthPython(bodyNode)
	}

	fn.SecurityRole = InferSecurityRolePython(fn.Name)

	return fn
}

func (e *TreeSitterPythonExtractor) parseParams(n *sitter.Node, src []byte) []UnifiedParam {
	var params []UnifiedParam
	if n == nil {
		return params
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() != "identifier" && c.Type() != "typed_parameter" && c.Type() != "default_parameter" &&
			c.Type() != "list_splat_pattern" && c.Type() != "dictionary_splat_pattern" {
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
			if nameNode == nil {
				nameNode = findFirstChild(c, "typed_parameter")
				if nameNode != nil {
					innerName := findFirstChild(nameNode, "identifier")
					if innerName != nil {
						name = tsText(innerName, src)
					}
					for k := 0; k < int(nameNode.ChildCount()); k++ {
						child := nameNode.Child(k)
						if child.Type() == "type" {
							typ = tsText(child, src)
						}
					}
				}
			} else {
				name = tsText(nameNode, src)
			}
			// 提取类型注解和默认值中的注解（如 FastAPI Query/Body/Header）
			for j := 0; j < int(c.ChildCount()); j++ {
				child := c.Child(j)
				switch child.Type() {
				case "type":
					typ = tsText(child, src)
				case "call":
					funcNode := child.Child(0)
					if funcNode != nil {
						annotations = append(annotations, tsText(funcNode, src))
					}
				case "identifier":
					if nameNode != nil && child.ID() != nameNode.ID() {
						annotations = append(annotations, tsText(child, src))
					}
				}
			}
		case "list_splat_pattern", "dictionary_splat_pattern":
			name = tsText(c, src)
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
			Location:   SourceLocation{File: filePath, LineStart: tsLine(call)},
			CallerFunc: findCallerFuncNamePython(call, src),
		}
		switch funcNode.Type() {
		case "identifier":
			site.TargetFunc = tsText(funcNode, src)
		case "attribute":
			parts := strings.Split(tsText(funcNode, src), ".")
			if len(parts) >= 2 {
				site.TargetPkg = parts[0]
				site.TargetFunc = parts[len(parts)-1]
			}
		}
		sites = append(sites, site)
	}
	return sites
}

// findCallerFuncNamePython 从 call 向上遍历父节点，找到最近的函数/方法名
func findCallerFuncNamePython(call *sitter.Node, src []byte) string {
	for parent := call.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type() {
		case "function_definition":
			nameNode := findFirstChild(parent, "identifier")
			if nameNode != nil {
				return tsText(nameNode, src)
			}
		}
	}
	return ""
}

// estimateNestedDepthPython 估算 Python 函数体的最大嵌套深度
func estimateNestedDepthPython(body *sitter.Node) int {
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
		case "if_statement", "for_statement", "while_statement", "with_statement",
			"try_statement", "match_statement":
			depth++
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i), depth)
		}
	}
	walk(body, 0)
	return maxDepth
}

// InferSecurityRolePython 推断 Python 函数的安全角色
func InferSecurityRolePython(name string) string {
	lower := strings.ToLower(name)

	if strings.Contains(lower, "auth") || strings.Contains(lower, "login") || strings.Contains(lower, "logout") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "session") || strings.Contains(lower, "permission") {
		return "auth"
	}
	if strings.Contains(lower, "encrypt") || strings.Contains(lower, "decrypt") || strings.Contains(lower, "hash") ||
		strings.Contains(lower, "sign") || strings.Contains(lower, "verify") {
		return "encryption"
	}
	if strings.Contains(lower, "sanitiz") || strings.Contains(lower, "escape") || strings.Contains(lower, "clean") ||
		strings.Contains(lower, "filter") || strings.Contains(lower, "validat") || strings.Contains(lower, "check") {
		return "sanitizer"
	}
	if strings.Contains(lower, "parse") || strings.Contains(lower, "bind") || strings.Contains(lower, "decode") ||
		strings.Contains(lower, "deserialize") || strings.Contains(lower, "format") {
		return "validator"
	}
	if strings.Contains(lower, "get") || strings.Contains(lower, "list") || strings.Contains(lower, "find") ||
		strings.Contains(lower, "fetch") || strings.Contains(lower, "query") || strings.Contains(lower, "search") ||
		strings.Contains(lower, "save") || strings.Contains(lower, "create") || strings.Contains(lower, "insert") ||
		strings.Contains(lower, "update") || strings.Contains(lower, "modify") || strings.Contains(lower, "delete") ||
		strings.Contains(lower, "remove") || strings.Contains(lower, "destroy") {
		return "data_access"
	}
	if strings.HasPrefix(lower, "handle") || strings.HasPrefix(lower, "on") ||
		strings.Contains(lower, "endpoint") || strings.Contains(lower, "route") {
		return "handler"
	}
	if strings.Contains(lower, "service") || strings.Contains(lower, "manager") ||
		strings.Contains(lower, "business") || strings.Contains(lower, "usecase") {
		return "service"
	}
	return "other"
}
