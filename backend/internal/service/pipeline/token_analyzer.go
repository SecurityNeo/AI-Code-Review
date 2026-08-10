package pipeline

import (
	"strings"
	"unicode"
)

// ===================== 通用 Tokenizer =====================

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokKeyword
	tokNumber
	tokString
	tokComment
	tokSymbol
	tokNewline
	tokWhitespace
)

type syntaxToken struct {
	Kind   tokenKind
	Text   string
	Line   int
	Column int
}

// tokenizer 词法分析器（通用，正确处理注释、字符串、转义）
type tokenizer struct {
	input string
	pos   int
	line  int
	col   int
}

func newTokenizer(input string) *tokenizer {
	return &tokenizer{input: input, pos: 0, line: 1, col: 1}
}

func (t *tokenizer) nextToken(lang string) syntaxToken {
	if t.pos >= len(t.input) {
		return syntaxToken{Kind: tokEOF}
	}
	ch := t.input[t.pos]

	// 换行
	if ch == '\n' || ch == '\r' {
		return t.consumeNewline()
	}

	// 空白字符
	if ch == ' ' || ch == '\t' {
		return t.consumeWhitespace()
	}

	// 注释（行注释）
	if ch == '/' && t.peek(1) == '/' {
		return t.consumeLineComment()
	}
	if ch == '#' {
		return t.consumeLineComment()
	}

	// 块注释 /* */
	if ch == '/' && t.peek(1) == '*' {
		return t.consumeBlockComment()
	}

	// Python/docstring 风格的多行注释 """ """
	if (ch == '"' && t.peek(1) == '"' && t.peek(2) == '"') ||
		(ch == '\'' && t.peek(1) == '\'' && t.peek(2) == '\'') {
		return t.consumeDocstring()
	}

	// 字符串字面量
	if ch == '"' || ch == '\'' || ch == '`' {
		return t.consumeString(ch)
	}

	// 数字
	if unicode.IsDigit(rune(ch)) || (ch == '.' && unicode.IsDigit(rune(t.peek(1)))) {
		return t.consumeNumber()
	}

	// 标识符 / 关键字
	if unicode.IsLetter(rune(ch)) || ch == '_' || ch == '$' {
		return t.consumeIdent(lang)
	}

	// 运算符 / 标点符号
	return t.consumeSymbol()
}

func (t *tokenizer) consumeNewline() syntaxToken {
	startLine := t.line
	if t.input[t.pos] == '\r' {
		t.pos++
		t.col++
	}
	if t.pos < len(t.input) && t.input[t.pos] == '\n' {
		t.pos++
	}
	t.line++
	t.col = 1
	return syntaxToken{Kind: tokNewline, Text: "\n", Line: startLine}
}

func (t *tokenizer) consumeWhitespace() syntaxToken {
	start := t.pos
	startLine := t.line
	startCol := t.col
	for t.pos < len(t.input) && (t.input[t.pos] == ' ' || t.input[t.pos] == '\t') {
		t.pos++
		t.col++
	}
	return syntaxToken{Kind: tokWhitespace, Text: t.input[start:t.pos], Line: startLine, Column: startCol}
}

func (t *tokenizer) consumeLineComment() syntaxToken {
	start := t.pos
	startLine := t.line
	startCol := t.col
	for t.pos < len(t.input) && t.input[t.pos] != '\n' && t.input[t.pos] != '\r' {
		t.pos++
		t.col++
	}
	return syntaxToken{Kind: tokComment, Text: t.input[start:t.pos], Line: startLine, Column: startCol}
}

func (t *tokenizer) consumeBlockComment() syntaxToken {
	start := t.pos
	startLine := t.line
	startCol := t.col
	depth := 0
	for t.pos < len(t.input)-1 {
		if t.input[t.pos] == '/' && t.input[t.pos+1] == '*' {
			depth++
			t.pos += 2
			t.col += 2
			continue
		}
		if t.input[t.pos] == '*' && t.input[t.pos+1] == '/' {
			depth--
			t.pos += 2
			t.col += 2
			if depth <= 0 {
				break
			}
			continue
		}
		if t.input[t.pos] == '\n' {
			t.line++
			t.col = 1
		} else {
			t.col++
		}
		t.pos++
	}
	return syntaxToken{Kind: tokComment, Text: t.input[start:t.pos], Line: startLine, Column: startCol}
}

func (t *tokenizer) consumeDocstring() syntaxToken {
	quote := t.input[t.pos]
	start := t.pos
	startLine := t.line
	startCol := t.col
	t.pos += 3
	t.col += 3
	for t.pos < len(t.input)-2 {
		if t.input[t.pos] == quote && t.input[t.pos+1] == quote && t.input[t.pos+2] == quote {
			t.pos += 3
			t.col += 3
			break
		}
		if t.input[t.pos] == '\n' {
			t.line++
			t.col = 1
		} else {
			t.col++
		}
		t.pos++
	}
	return syntaxToken{Kind: tokString, Text: t.input[start:t.pos], Line: startLine, Column: startCol}
}

func (t *tokenizer) consumeString(quote byte) syntaxToken {
	start := t.pos
	startLine := t.line
	startCol := t.col
	t.pos++
	t.col++
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if ch == '\\' {
			t.pos++
			t.col++
			if t.pos < len(t.input) {
				if t.input[t.pos] == '\n' {
					t.line++
					t.col = 1
				} else {
					t.col++
				}
				t.pos++
			}
			continue
		}
		if ch == quote {
			t.pos++
			t.col++
			break
		}
		if ch == '\n' {
			t.line++
			t.col = 1
		} else {
			t.col++
		}
		t.pos++
	}
	return syntaxToken{Kind: tokString, Text: t.input[start:t.pos], Line: startLine, Column: startCol}
}

func (t *tokenizer) consumeNumber() syntaxToken {
	start := t.pos
	startLine := t.line
	startCol := t.col
	for t.pos < len(t.input) && (unicode.IsDigit(rune(t.input[t.pos])) || t.input[t.pos] == '.' || t.input[t.pos] == '_' || t.input[t.pos] == 'e' || t.input[t.pos] == 'E' || t.input[t.pos] == '+' || t.input[t.pos] == '-') {
		t.pos++
		t.col++
	}
	return syntaxToken{Kind: tokNumber, Text: t.input[start:t.pos], Line: startLine, Column: startCol}
}

func (t *tokenizer) consumeIdent(lang string) syntaxToken {
	start := t.pos
	startLine := t.line
	startCol := t.col
	for t.pos < len(t.input) && (unicode.IsLetter(rune(t.input[t.pos])) || unicode.IsDigit(rune(t.input[t.pos])) || t.input[t.pos] == '_' || t.input[t.pos] == '$') {
		t.pos++
		t.col++
	}
	text := t.input[start:t.pos]
	kind := tokIdent
	if isKeyword(lang, text) {
		kind = tokKeyword
	}
	return syntaxToken{Kind: kind, Text: text, Line: startLine, Column: startCol}
}

func (t *tokenizer) consumeSymbol() syntaxToken {
	start := t.pos
	startLine := t.line
	startCol := t.col
	// 多字符符号优先
	twoChar := ""
	if t.pos+1 < len(t.input) {
		twoChar = t.input[t.pos : t.pos+2]
	}
	if twoChar == "->" || twoChar == "=>" || twoChar == "::" || twoChar == ".." || twoChar == "**" || twoChar == "==" || twoChar == "!=" || twoChar == ">=" || twoChar == "<=" || twoChar == "&&" || twoChar == "||" || twoChar == "++" || twoChar == "--" || twoChar == "<<" || twoChar == ">>" || twoChar == "+=" || twoChar == "-=" || twoChar == "*=" || twoChar == "/=" || twoChar == "%=" || twoChar == "&=" || twoChar == "|=" || twoChar == "^=" {
		t.pos += 2
		t.col += 2
		return syntaxToken{Kind: tokSymbol, Text: twoChar, Line: startLine, Column: startCol}
	}
	t.pos++
	t.col++
	return syntaxToken{Kind: tokSymbol, Text: t.input[start:t.pos], Line: startLine, Column: startCol}
}

func (t *tokenizer) peek(offset int) byte {
	if t.pos+offset < len(t.input) {
		return t.input[t.pos+offset]
	}
	return 0
}

// tokenizeAll 将整个文件 token 化，返回非空白/注释/换行的 token（但保留行号信息用于搜索）
// keepComments: 是否保留注释 token（用于搜索时跳过）
func tokenizeAll(input, lang string, keepComments bool) []syntaxToken {
	t := newTokenizer(input)
	var tokens []syntaxToken
	for {
		tok := t.nextToken(lang)
		if tok.Kind == tokEOF {
			break
		}
		if tok.Kind == tokWhitespace || tok.Kind == tokNewline {
			continue
		}
		if tok.Kind == tokComment && !keepComments {
			continue
		}
		tokens = append(tokens, tok)
	}
	return tokens
}

func isKeyword(lang, word string) bool {
	keywords := map[string]map[string]bool{
		"golang": {
			"break": true, "case": true, "chan": true, "const": true, "continue": true,
			"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
			"func": true, "go": true, "goto": true, "if": true, "import": true,
			"interface": true, "map": true, "package": true, "range": true, "return": true,
			"select": true, "struct": true, "switch": true, "type": true, "var": true,
		},
		"java": {
			"abstract": true, "assert": true, "boolean": true, "break": true, "byte": true,
			"case": true, "catch": true, "char": true, "class": true, "const": true,
			"continue": true, "default": true, "do": true, "double": true, "else": true,
			"enum": true, "extends": true, "final": true, "finally": true, "float": true,
			"for": true, "if": true, "goto": true, "implements": true, "import": true,
			"instanceof": true, "int": true, "interface": true, "long": true, "native": true,
			"new": true, "package": true, "private": true, "protected": true, "public": true,
			"return": true, "short": true, "static": true, "strictfp": true, "super": true,
			"switch": true, "synchronized": true, "this": true, "throw": true, "throws": true,
			"transient": true, "try": true, "void": true, "volatile": true, "while": true,
		},
		"python": {
			"and": true, "as": true, "assert": true, "break": true, "class": true,
			"continue": true, "def": true, "del": true, "elif": true, "else": true,
			"except": true, "False": true, "finally": true, "for": true, "from": true,
			"global": true, "if": true, "import": true, "in": true, "is": true,
			"lambda": true, "None": true, "nonlocal": true, "not": true, "or": true,
			"pass": true, "raise": true, "return": true, "True": true, "try": true,
			"while": true, "with": true, "yield": true, "async": true, "await": true,
		},
		"javascript": {
			"break": true, "case": true, "catch": true, "class": true, "const": true,
			"continue": true, "debugger": true, "default": true, "delete": true, "do": true,
			"else": true, "export": true, "extends": true, "false": true, "finally": true,
			"for": true, "function": true, "if": true, "import": true, "in": true,
			"instanceof": true, "new": true, "null": true, "return": true, "super": true,
			"switch": true, "this": true, "throw": true, "true": true, "try": true,
			"typeof": true, "var": true, "void": true, "while": true, "with": true,
			"let": true, "static": true, "yield": true, "await": true, "async": true,
		},
		"typescript": {
			"break": true, "case": true, "catch": true, "class": true, "const": true,
			"continue": true, "debugger": true, "default": true, "delete": true, "do": true,
			"else": true, "export": true, "extends": true, "false": true, "finally": true,
			"for": true, "function": true, "if": true, "import": true, "in": true,
			"instanceof": true, "new": true, "null": true, "return": true, "super": true,
			"switch": true, "this": true, "throw": true, "true": true, "try": true,
			"typeof": true, "var": true, "void": true, "while": true, "with": true,
			"let": true, "static": true, "yield": true, "await": true, "async": true,
			"interface": true, "type": true, "namespace": true, "declare": true, "readonly": true,
			"abstract": true, "implements": true, "enum": true, "module": true, "require": true,
		},
	}
	if set, ok := keywords[lang]; ok {
		return set[word]
	}
	return false
}

// ===================== 接口定义 =====================

// tokenAnalyzer 语言分析器接口（精度远高于正则）
type tokenAnalyzer interface {
	// Tokenize 词法分析
	Tokenize(content string) []syntaxToken
	// ParseSymbols 从 token 流提取符号定义
	ParseSymbols(tokens []syntaxToken, filePath string) []SymbolInfo
	// FindReferences 在所有文件中精确查找符号引用
	FindReferences(symbol string, allFiles map[string][]syntaxToken) []CallerInfo
	// FileExts 返回该语言匹配的文件扩展名
	FileExts() []string
}

// langToAnalyzer 根据语言创建对应分析器（Go 返回 nil，因其使用标准库 AST）
func langToAnalyzer(lang string) tokenAnalyzer {
	switch lang {
	case "java":
		return newJavaTokenAnalyzer()
	case "python":
		return newPythonTokenAnalyzer()
	case "javascript", "frontend":
		return newJSTokenAnalyzer()
	case "typescript":
		return newTSTokenAnalyzer()
	default:
		return newGenericTokenAnalyzer()
	}
}

// ===================== 通用 Token 分析器 =====================

type genericTokenAnalyzer struct{}

func newGenericTokenAnalyzer() *genericTokenAnalyzer { return &genericTokenAnalyzer{} }
func (a *genericTokenAnalyzer) FileExts() []string   { return []string{} } // 接受所有扩展名
func (a *genericTokenAnalyzer) Tokenize(content string) []syntaxToken {
	return tokenizeAll(content, "generic", false)
}

// ParseSymbols 从通用 token 流提取 func/function/def/class/struct/interface 定义
func (a *genericTokenAnalyzer) ParseSymbols(tokens []syntaxToken, filePath string) []SymbolInfo {
	var symbols []SymbolInfo
	defKeywords := map[string]string{
		"func":      "function",
		"function":  "function",
		"def":       "function",
		"class":     "class",
		"struct":    "struct",
		"interface": "interface",
	}

	for i := 0; i < len(tokens); i++ {
		if typ, ok := defKeywords[tokens[i].Text]; ok {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name := tokens[i+1].Text
				// 排除常见误匹配（如 func main）
				if name == "main" && tokens[i].Text == "func" {
					continue
				}
				symbols = append(symbols, SymbolInfo{
					Name:     name,
					Type:     typ,
					File:     filePath,
					Exported: isExportedGeneric(name),
				})
			}
		}
	}
	return symbols
}

func (a *genericTokenAnalyzer) FindReferences(symbol string, allFiles map[string][]syntaxToken) []CallerInfo {
	var callers []CallerInfo
	for filePath, tokens := range allFiles {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Text == symbol {
				// 排除定义位置（前一个是 func/function/def/class/struct/interface）
				if i > 0 {
					prev := tokens[i-1].Text
					if prev == "func" || prev == "function" || prev == "def" ||
						prev == "class" || prev == "struct" || prev == "interface" {
						continue
					}
				}
				// 调用模式: symbol( 或 .symbol(
				if i+1 < len(tokens) && tokens[i+1].Text == "(" {
					callers = append(callers, CallerInfo{
						File:     filePath,
						Line:     tokens[i].Line,
						CallExpr: a.getExpr(tokens, i),
					})
				}
				// 属性访问或类型使用: .symbol
				if i > 0 && tokens[i-1].Text == "." {
					callers = append(callers, CallerInfo{
						File:     filePath,
						Line:     tokens[i].Line,
						CallExpr: a.getExpr(tokens, i),
					})
				}
			}
		}
	}
	return callers
}

func (a *genericTokenAnalyzer) getExpr(tokens []syntaxToken, idx int) string {
	start := max(0, idx-3)
	end := min(len(tokens), idx+4)
	var parts []string
	for i := start; i < end; i++ {
		parts = append(parts, tokens[i].Text)
	}
	return strings.Join(parts, " ")
}

func isExportedGeneric(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	return first >= 'A' && first <= 'Z'
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
