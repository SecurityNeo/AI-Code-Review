package pipeline

import (
	"strings"
)

// ===================== Java Token Analyzer =====================

type javaTokenAnalyzer struct{}

func newJavaTokenAnalyzer() *javaTokenAnalyzer  { return &javaTokenAnalyzer{} }
func (a *javaTokenAnalyzer) FileExts() []string { return []string{".java"} }
func (a *javaTokenAnalyzer) Tokenize(content string) []syntaxToken {
	return tokenizeAll(content, "java", false)
}

// ParseSymbols 从 Java token 流提取类/接口/枚举和方法定义
func (a *javaTokenAnalyzer) ParseSymbols(tokens []syntaxToken, filePath string) []SymbolInfo {
	var symbols []SymbolInfo
	i := 0
	for i < len(tokens) {
		// 跳过注解
		for i < len(tokens) && tokens[i].Text == "@" {
			i++
			if i < len(tokens) && tokens[i].Kind == tokIdent {
				i++
				// 跳过注解参数 ( ... )
				if i < len(tokens) && tokens[i].Text == "(" {
					i = a.skipBalanced(tokens, i, "(", ")") + 1
				}
			}
		}

		if i >= len(tokens) {
			break
		}

		// 识别 class / interface / enum
		if tokens[i].Text == "class" || tokens[i].Text == "interface" || tokens[i].Text == "enum" {
			kind := "class"
			if tokens[i].Text == "interface" {
				kind = "interface"
			}
			if tokens[i].Text == "enum" {
				kind = "enum"
			}
			i++
			if i < len(tokens) && tokens[i].Kind == tokIdent {
				name := tokens[i].Text
				symbols = append(symbols, SymbolInfo{
					Name:     name,
					Type:     kind,
					File:     filePath,
					Exported: true,
				})
				i++
				// 跳到 { ... }
				i = a.skipToBlockEnd(tokens, i)
			}
			continue
		}

		// 识别方法：需要判断是方法声明还是字段声明
		// 方法模式: [类型修饰符*] 返回类型 名称 ( 参数 ) [throws ...] { ... }
		// 字段模式: [类型修饰符*] 类型 名称 [= val] ;
		start := i
		// 向前走找 "(" — 如果找到且后面是 ")" 和 "{" 或 "throws" → 方法
		parenIdx := -1
		for j := i; j < len(tokens) && tokens[j].Text != ";" && tokens[j].Text != "{"; j++ {
			if tokens[j].Text == "(" {
				parenIdx = j
				break
			}
		}
		if parenIdx > start && parenIdx-start >= 2 {
			// 检查是否是方法：名前是类型，后面是 ) 跟着 { 或 throws
			nameIdx := parenIdx - 1
			if tokens[nameIdx].Kind == tokIdent || tokens[nameIdx].Kind == tokKeyword {
				// 确认后面跟着 ) 和 { 或 throws
				closeParen := a.findMatchingParen(tokens, parenIdx, "(", ")")
				if closeParen > parenIdx && closeParen+1 < len(tokens) {
					next := tokens[closeParen+1]
					if next.Text == "{" || next.Text == "throws" {
						name := tokens[nameIdx].Text
						// 判断是否是构造函数（与类名相同）
						isConstructor := false
						for _, s := range symbols {
							if s.Type == "class" && s.Name == name {
								isConstructor = true
								break
							}
						}
						if !isConstructor {
							// 签名从 start 到 closeParen
							sig := a.buildSignature(tokens[start:parenIdx]) + "(" + a.buildSignature(tokens[parenIdx+1:closeParen]) + ")"
							symbols = append(symbols, SymbolInfo{
								Name:      name,
								Type:      "method",
								File:      filePath,
								Exported:  true,
								Signature: sig,
							})
						}
						i = a.skipToBlockEnd(tokens, closeParen+1)
						continue
					}
				}
			}
		}
		i++
	}
	return symbols
}

func (a *javaTokenAnalyzer) skipBalanced(tokens []syntaxToken, start int, open, close string) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i].Text == open {
			depth++
		} else if tokens[i].Text == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(tokens) - 1
}

func (a *javaTokenAnalyzer) findMatchingParen(tokens []syntaxToken, openIdx int, open, close string) int {
	return a.skipBalanced(tokens, openIdx, open, close)
}

func (a *javaTokenAnalyzer) skipToBlockEnd(tokens []syntaxToken, start int) int {
	// 跳过到下一个 { ... }
	for i := start; i < len(tokens); i++ {
		if tokens[i].Text == "{" {
			return a.skipBalanced(tokens, i, "{", "}") + 1
		}
	}
	return start
}

func (a *javaTokenAnalyzer) buildSignature(tokens []syntaxToken) string {
	var parts []string
	for _, t := range tokens {
		if t.Kind != tokComment && t.Kind != tokString {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}

// FindReferences 在 Java token 流中查找符号引用
func (a *javaTokenAnalyzer) FindReferences(symbol string, allFiles map[string][]syntaxToken) []CallerInfo {
	var callers []CallerInfo
	// 搜索模式: symbol( 或 .symbol(
	for filePath, tokens := range allFiles {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Text == symbol {
				// 检查下一个是 "(" 或 "."
				if i+1 < len(tokens) && (tokens[i+1].Text == "(" || tokens[i+1].Text == ".") {
					// 排除定义位置（前面是返回类型或 class/interface 等）
					if !a.isDefinitionPosition(tokens, i) {
						// 获取上下文行号
						line := tokens[i].Line
						callers = append(callers, CallerInfo{
							File:     filePath,
							Line:     line,
							CallExpr: a.getCallExpr(tokens, i),
						})
					}
				}
			}
		}
	}
	return callers
}

func (a *javaTokenAnalyzer) isDefinitionPosition(tokens []syntaxToken, idx int) bool {
	// 方法定义的典型特征：后面是 ( ... ) { 或 throws
	if idx+1 < len(tokens) && tokens[idx+1].Text == "(" {
		// 找匹配的 )
		closeParen := a.findMatchingParen(tokens, idx+1, "(", ")")
		if closeParen > idx+1 && closeParen+1 < len(tokens) {
			next := tokens[closeParen+1]
			if next.Text == "{" || next.Text == "throws" {
				return true
			}
		}
	}
	// class 定义
	if idx > 0 && tokens[idx-1].Text == "class" {
		return true
	}
	// interface 定义
	if idx > 0 && tokens[idx-1].Text == "interface" {
		return true
	}
	return false
}

func (a *javaTokenAnalyzer) getCallExpr(tokens []syntaxToken, idx int) string {
	// 提取调用表达式（前后几个 token）
	start := max(0, idx-3)
	end := min(len(tokens), idx+4)
	var parts []string
	for i := start; i < end; i++ {
		parts = append(parts, tokens[i].Text)
	}
	return strings.Join(parts, " ")
}

// ===================== Python Token Analyzer =====================

type pythonTokenAnalyzer struct{}

func newPythonTokenAnalyzer() *pythonTokenAnalyzer { return &pythonTokenAnalyzer{} }
func (a *pythonTokenAnalyzer) FileExts() []string  { return []string{".py"} }
func (a *pythonTokenAnalyzer) Tokenize(content string) []syntaxToken {
	return tokenizeAll(content, "python", false)
}

func (a *pythonTokenAnalyzer) ParseSymbols(tokens []syntaxToken, filePath string) []SymbolInfo {
	var symbols []SymbolInfo
	i := 0
	for i < len(tokens) {
		// @decorator
		if tokens[i].Text == "@" {
			i++
			if i < len(tokens) && tokens[i].Kind == tokIdent {
				i++
				if i < len(tokens) && tokens[i].Text == "(" {
					i = a.skipBalanced(tokens, i, "(", ")") + 1
				}
			}
			continue
		}

		// def name(params):
		if tokens[i].Text == "def" || tokens[i].Text == "async" {
			offset := 1
			if tokens[i].Text == "async" {
				if i+1 < len(tokens) && tokens[i+1].Text == "def" {
					offset = 2
				} else {
					i++
					continue
				}
			}
			if i+offset < len(tokens) && tokens[i+offset].Kind == tokIdent {
				name := tokens[i+offset].Text
				sig := a.buildFuncSig(tokens, i+offset)
				symbols = append(symbols, SymbolInfo{
					Name:      name,
					Type:      "function",
					File:      filePath,
					Exported:  !strings.HasPrefix(name, "_"),
					Signature: sig,
				})
				i += offset + 1
				// 跳过参数列表
				if i < len(tokens) && tokens[i].Text == "(" {
					i = a.skipBalanced(tokens, i, "(", ")") + 1
				}
				continue
			}
		}

		// class Name[(Base)]:
		if tokens[i].Text == "class" {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name := tokens[i+1].Text
				symbols = append(symbols, SymbolInfo{
					Name:     name,
					Type:     "class",
					File:     filePath,
					Exported: !strings.HasPrefix(name, "_"),
				})
				i += 2
				// 跳过继承列表
				if i < len(tokens) && tokens[i].Text == "(" {
					i = a.skipBalanced(tokens, i, "(", ")") + 1
				}
				continue
			}
		}

		i++
	}
	return symbols
}

func (a *pythonTokenAnalyzer) skipBalanced(tokens []syntaxToken, start int, open, close string) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i].Text == open {
			depth++
		} else if tokens[i].Text == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(tokens) - 1
}

func (a *pythonTokenAnalyzer) buildFuncSig(tokens []syntaxToken, nameIdx int) string {
	// name(params)
	if nameIdx+1 < len(tokens) && tokens[nameIdx+1].Text == "(" {
		closeParen := a.skipBalanced(tokens, nameIdx+1, "(", ")")
		var parts []string
		for i := nameIdx; i <= closeParen; i++ {
			parts = append(parts, tokens[i].Text)
		}
		return strings.Join(parts, " ")
	}
	return tokens[nameIdx].Text + "()"
}

func (a *pythonTokenAnalyzer) FindReferences(symbol string, allFiles map[string][]syntaxToken) []CallerInfo {
	var callers []CallerInfo
	for filePath, tokens := range allFiles {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Text == symbol {
				if i+1 < len(tokens) && tokens[i+1].Text == "(" {
					// 排除定义
					if i > 0 && (tokens[i-1].Text == "def" || (i > 1 && tokens[i-2].Text == "def" && tokens[i-1].Text == "async")) {
						continue
					}
					callers = append(callers, CallerInfo{
						File:     filePath,
						Line:     tokens[i].Line,
						CallExpr: a.getExpr(tokens, i),
					})
				}
				// Python 方法调用: obj.symbol(...)
				if i > 0 && tokens[i-1].Text == "." && i+1 < len(tokens) && tokens[i+1].Text == "(" {
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

func (a *pythonTokenAnalyzer) getExpr(tokens []syntaxToken, idx int) string {
	start := max(0, idx-4)
	end := min(len(tokens), idx+5)
	var parts []string
	for i := start; i < end; i++ {
		parts = append(parts, tokens[i].Text)
	}
	return strings.Join(parts, " ")
}
