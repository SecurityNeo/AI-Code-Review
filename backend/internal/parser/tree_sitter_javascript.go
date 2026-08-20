//go:build cgo

package parser

import (
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
)

// TreeSitterJavaScriptExtractor JavaScript/TypeScript/Vue语言AST提取器（基于Tree-sitter）
type TreeSitterJavaScriptExtractor struct {
	language string
	exts     []string
}

// NewTreeSitterJavaScriptExtractor 创建Tree-sitter JS提取器
func NewTreeSitterJavaScriptExtractor() *TreeSitterJavaScriptExtractor {
	return &TreeSitterJavaScriptExtractor{language: "javascript", exts: []string{".js", ".jsx", ".vue"}}
}

// NewTreeSitterTypeScriptExtractor 创建Tree-sitter TS提取器
func NewTreeSitterTypeScriptExtractor() *TreeSitterJavaScriptExtractor {
	return &TreeSitterJavaScriptExtractor{language: "typescript", exts: []string{".ts", ".tsx", ".vue"}}
}

// Language 返回语言
func (e *TreeSitterJavaScriptExtractor) Language() string { return e.language }

// FileExts 返回文件扩展名
func (e *TreeSitterJavaScriptExtractor) FileExts() []string { return e.exts }

// ParseFile 解析JS/TS/Vue文件
func (e *TreeSitterJavaScriptExtractor) ParseFile(filePath string, src []byte) (*UnifiedAST, error) {
	// Vue SFC: 提取 <script> 标签内的内容
	body := src
	if strings.HasSuffix(filePath, ".vue") {
		scriptContent := extractVueScript(src)
		if scriptContent == nil {
			// 没有 <script> 标签，返回空 AST（template-only 组件）
			return &UnifiedAST{
				Language: e.language,
				FilePath: filePath,
			}, nil
		}
		body = scriptContent
	}

	parser := sitter.NewParser()
	parser.SetLanguage(javascript.GetLanguage())
	tree := parser.Parse(nil, body)
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

	// Extract functions (多种声明方式)
	// 1. function_declaration
	for _, fn := range findChildren(root, "function_declaration") {
		result.Functions = append(result.Functions, e.parseFunc(fn, filePath, src))
	}
	// 2. 变量声明中的函数/箭头函数: const foo = function() {...} / const foo = () => {...}
	for _, decl := range findChildren(root, "lexical_declaration") {
		for i := 0; i < int(decl.ChildCount()); i++ {
			c := decl.Child(i)
			if c.Type() == "variable_declarator" {
				if fn := e.parseVariableDeclaratorFunc(c, filePath, src); fn != nil {
					result.Functions = append(result.Functions, *fn)
				}
			}
		}
	}
	// 3. export default 中的函数对象
	for _, exp := range findChildren(root, "export_statement") {
		for i := 0; i < int(exp.ChildCount()); i++ {
			c := exp.Child(i)
			if c.Type() == "function_declaration" {
				result.Functions = append(result.Functions, e.parseFunc(c, filePath, src))
			}
		}
	}
	// 4. object 中的方法（常用于 Vue options API: methods: { foo() {} }）
	for _, obj := range findChildren(root, "object") {
		for i := 0; i < int(obj.ChildCount()); i++ {
			c := obj.Child(i)
			if c.Type() == "method_definition" || c.Type() == "pair" {
				if fn := e.parseObjectMethod(c, filePath, src); fn != nil {
					result.Functions = append(result.Functions, *fn)
				}
			}
		}
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

// extractVueScript 从 Vue 单文件组件中提取 <script> 标签内的内容
func extractVueScript(src []byte) []byte {
	re := regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)
	matches := re.FindSubmatch(src)
	if len(matches) >= 2 {
		return matches[1]
	}
	return nil
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
		body := tsText(bodyNode, src)
		fn.BodySnippet = truncateBody(body)
		fn.Complexity = EstimateComplexityFromSnippet(body)
		fn.LOC = strings.Count(body, "\n")
		fn.NestedDepth = estimateNestedDepthJS(bodyNode)
	}

	fn.SecurityRole = InferSecurityRoleJS(fn.Name, "")

	return fn
}

// parseVariableDeclaratorFunc 解析 const foo = function() {} 或 const foo = () => {}
func (e *TreeSitterJavaScriptExtractor) parseVariableDeclaratorFunc(n *sitter.Node, filePath string, src []byte) *UnifiedFunction {
	var nameNode *sitter.Node
	var valueNode *sitter.Node
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		switch c.Type() {
		case "identifier":
			nameNode = c
		case "function", "arrow_function", "function_declaration":
			valueNode = c
		}
	}
	if nameNode == nil || valueNode == nil {
		return nil
	}

	fn := UnifiedFunction{
		Name:       tsText(nameNode, src),
		Location:   SourceLocation{File: filePath, LineStart: tsLine(n)},
		IsExported: e.isExported(n.Parent(), src),
	}

	if valueNode.Type() == "arrow_function" {
		// async check on declarator itself
		for i := 0; i < int(n.ChildCount()); i++ {
			if n.Child(i).Type() == "async" {
				fn.IsAsync = true
				break
			}
		}
	}

	paramsNode := findFirstChild(valueNode, "formal_parameters")
	if paramsNode != nil {
		fn.Params = e.parseParams(paramsNode, src)
	}

	bodyNode := findFirstChild(valueNode, "statement_block")
	if bodyNode != nil {
		body := tsText(bodyNode, src)
		fn.BodySnippet = truncateBody(body)
		fn.Complexity = EstimateComplexityFromSnippet(body)
		fn.LOC = strings.Count(body, "\n")
		fn.NestedDepth = estimateNestedDepthJS(bodyNode)
	}

	fn.SecurityRole = InferSecurityRoleJS(fn.Name, "")
	return &fn
}

// parseObjectMethod 解析对象字面量中的方法: { foo() {}, bar: function() {} }
func (e *TreeSitterJavaScriptExtractor) parseObjectMethod(n *sitter.Node, filePath string, src []byte) *UnifiedFunction {
	var name string
	var valueNode *sitter.Node

	switch n.Type() {
	case "method_definition":
		nameNode := findFirstChild(n, "property_identifier")
		if nameNode == nil {
			nameNode = findFirstChild(n, "identifier")
		}
		if nameNode != nil {
			name = tsText(nameNode, src)
		}
		valueNode = n
	case "pair":
		keyNode := findFirstChild(n, "property_identifier")
		if keyNode == nil {
			keyNode = findFirstChild(n, "identifier")
		}
		if keyNode == nil {
			keyNode = findFirstChild(n, "string")
		}
		if keyNode != nil {
			name = strings.Trim(tsText(keyNode, src), `"'`)
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c.Type() == "function" || c.Type() == "arrow_function" {
				valueNode = c
				break
			}
		}
	}

	if name == "" || valueNode == nil {
		return nil
	}

	fn := UnifiedFunction{
		Name:     name,
		Location: SourceLocation{File: filePath, LineStart: tsLine(n)},
	}

	paramsNode := findFirstChild(valueNode, "formal_parameters")
	if paramsNode != nil {
		fn.Params = e.parseParams(paramsNode, src)
	}

	bodyNode := findFirstChild(valueNode, "statement_block")
	if bodyNode != nil {
		body := tsText(bodyNode, src)
		fn.BodySnippet = truncateBody(body)
		fn.Complexity = EstimateComplexityFromSnippet(body)
		fn.LOC = strings.Count(body, "\n")
		fn.NestedDepth = estimateNestedDepthJS(bodyNode)
	}

	fn.SecurityRole = InferSecurityRoleJS(fn.Name, "")
	return &fn
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
		body := tsText(bodyNode, src)
		fn.BodySnippet = truncateBody(body)
		fn.Complexity = EstimateComplexityFromSnippet(body)
		fn.LOC = strings.Count(body, "\n")
		fn.NestedDepth = estimateNestedDepthJS(bodyNode)
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
		body := tsText(bodyNode, src)
		fn.BodySnippet = truncateBody(body)
		fn.Complexity = EstimateComplexityFromSnippet(body)
		fn.LOC = strings.Count(body, "\n")
		fn.NestedDepth = estimateNestedDepthJS(bodyNode)
	}
	fn.SecurityRole = InferSecurityRoleJS(fn.Name, "")
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
			CallerFunc: findCallerFuncNameJS(call, src),
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

// findCallerFuncNameJS 从 call expression 向上遍历父节点，找到最近的函数/方法名
func findCallerFuncNameJS(call *sitter.Node, src []byte) string {
	for parent := call.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type() {
		case "function_declaration":
			nameNode := findFirstChild(parent, "identifier")
			if nameNode != nil {
				return tsText(nameNode, src)
			}
		case "method_definition":
			nameNode := findFirstChild(parent, "property_identifier")
			if nameNode == nil {
				nameNode = findFirstChild(parent, "identifier")
			}
			if nameNode != nil {
				return tsText(nameNode, src)
			}
		case "arrow_function":
			// 尝试从变量声明器获取名字
			gp := parent.Parent()
			if gp != nil && gp.Type() == "variable_declarator" {
				nameNode := findFirstChild(gp, "identifier")
				if nameNode != nil {
					return tsText(nameNode, src)
				}
			}
			return "<arrow>"
		case "function":
			// 匿名函数：尝试从变量声明器或对象属性获取名字
			gp := parent.Parent()
			if gp != nil {
				switch gp.Type() {
				case "variable_declarator":
					nameNode := findFirstChild(gp, "identifier")
					if nameNode != nil {
						return tsText(nameNode, src)
					}
				case "pair":
					nameNode := findFirstChild(gp, "property_identifier")
					if nameNode == nil {
						nameNode = findFirstChild(gp, "identifier")
					}
					if nameNode != nil {
						return strings.Trim(tsText(nameNode, src), `"'`)
					}
				}
			}
			return "<anonymous>"
		}
	}
	return ""
}

// estimateNestedDepthJS 估算 JS 函数体的最大嵌套深度
func estimateNestedDepthJS(body *sitter.Node) int {
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
			"do_statement", "try_statement", "with_statement":
			depth++
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i), depth)
		}
	}
	walk(body, 0)
	return maxDepth
}

// InferSecurityRoleJS 推断 JS/TS 函数的安全角色
func InferSecurityRoleJS(name, receiver string) string {
	lower := strings.ToLower(name)
	recvLower := strings.ToLower(receiver)

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
		strings.Contains(recvLower, "handler") || strings.Contains(recvLower, "controller") ||
		strings.Contains(recvLower, "router") || strings.Contains(recvLower, "component") ||
		strings.Contains(recvLower, "view") || strings.Contains(recvLower, "page") {
		return "handler"
	}
	if strings.Contains(recvLower, "service") || strings.Contains(recvLower, "manager") ||
		strings.Contains(recvLower, "store") || strings.Contains(recvLower, "model") {
		return "service"
	}
	return "other"
}
