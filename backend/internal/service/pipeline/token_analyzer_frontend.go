package pipeline

import "strings"

// ===================== JavaScript/TypeScript Token Analyzer =====================

type jsTokenAnalyzer struct{}

func newJSTokenAnalyzer() *jsTokenAnalyzer    { return &jsTokenAnalyzer{} }
func (a *jsTokenAnalyzer) FileExts() []string { return []string{".js", ".jsx"} }
func (a *jsTokenAnalyzer) Tokenize(content string) []syntaxToken {
	return tokenizeAll(content, "javascript", false)
}

func (a *jsTokenAnalyzer) ParseSymbols(tokens []syntaxToken, filePath string) []SymbolInfo {
	var symbols []SymbolInfo
	i := 0
	for i < len(tokens) {
		// function name(params) { ... }
		if tokens[i].Text == "function" || tokens[i].Text == "async" {
			offset := 1
			if tokens[i].Text == "async" {
				if i+1 < len(tokens) && tokens[i+1].Text == "function" {
					offset = 2
				} else {
					// async 箭头函数: async (params) => { ... } 或 async x => { ... }
					i++
					continue
				}
			}
			if i+offset < len(tokens) && tokens[i+offset].Kind == tokIdent {
				name := tokens[i+offset].Text
				symbols = append(symbols, SymbolInfo{
					Name:     name,
					Type:     "function",
					File:     filePath,
					Exported: a.isExported(tokens, i),
				})
				i = a.skipFuncBody(tokens, i+offset)
				continue
			}
			i++
			continue
		}

		// export [default] function/class/const/let/var
		if tokens[i].Text == "export" {
			i = a.parseExport(tokens, i, filePath, &symbols)
			continue
		}

		// class Name { method() { ... } }
		if tokens[i].Text == "class" {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name := tokens[i+1].Text
				symbols = append(symbols, SymbolInfo{
					Name:     name,
					Type:     "class",
					File:     filePath,
					Exported: a.isExported(tokens, i),
				})
				i = a.skipClassBody(tokens, i)
				continue
			}
		}

		// const/let/var name = function(...) { ... } 或 name = (params) => { ... }
		if tokens[i].Text == "const" || tokens[i].Text == "let" || tokens[i].Text == "var" {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name := tokens[i+1].Text
				// 检查是否是函数赋值
				if i+2 < len(tokens) && tokens[i+2].Text == "=" {
					if a.isFuncAssignment(tokens, i+2) {
						symbols = append(symbols, SymbolInfo{
							Name:     name,
							Type:     "function",
							File:     filePath,
							Exported: a.isExported(tokens, i),
						})
					}
				}
			}
		}

		// method shorthand: name(params) { ... } （在 class / object literal 中）
		// 需要区分对象字面量和函数定义
		if i > 0 && tokens[i].Kind == tokIdent && tokens[i+1].Text == "(" {
			// 检查前面是否是 . 或 [ 或 ( → 不是定义
			if tokens[i-1].Text == "." || tokens[i-1].Text == "[" || tokens[i-1].Text == "(" {
				i++
				continue
			}
			// 检查是否是 getter/setter: get name() / set name(val)
			if tokens[i-1].Text == "get" || tokens[i-1].Text == "set" {
				isGetter := tokens[i-1].Text == "get"
				prefix := "set"
				if isGetter {
					prefix = "get"
				}
				symbols = append(symbols, SymbolInfo{
					Name:     prefix + " " + tokens[i].Text,
					Type:     "property",
					File:     filePath,
					Exported: false,
				})
			}
		}

		i++
	}
	return symbols
}

func (a *jsTokenAnalyzer) parseExport(tokens []syntaxToken, start int, filePath string, symbols *[]SymbolInfo) int {
	i := start + 1
	if i >= len(tokens) {
		return i
	}

	// export default function/class
	if tokens[i].Text == "default" {
		i++
		if i < len(tokens) && (tokens[i].Text == "function" || tokens[i].Text == "class" || tokens[i].Text == "async") {
			symbolType := ""
			var name string
			if tokens[i].Text == "function" || (tokens[i].Text == "async" && i+1 < len(tokens) && tokens[i+1].Text == "function") {
				symbolType = "function"
				offset := 1
				if tokens[i].Text == "async" {
					offset = 2
				}
				if i+offset < len(tokens) && tokens[i+offset].Kind == tokIdent {
					name = tokens[i+offset].Text
				}
			} else if tokens[i].Text == "class" && i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				symbolType = "class"
				name = tokens[i+1].Text
			}
			if symbolType != "" && name != "" {
				*symbols = append(*symbols, SymbolInfo{
					Name:     name,
					Type:     symbolType,
					File:     filePath,
					Exported: true,
				})
			}
		}
		return i
	}

	// export function/class/const/let/var
	if tokens[i].Text == "function" || tokens[i].Text == "class" || tokens[i].Text == "async" || tokens[i].Text == "const" || tokens[i].Text == "let" || tokens[i].Text == "var" {
		symbolType := ""
		var name string
		if tokens[i].Text == "function" || (tokens[i].Text == "async" && i+1 < len(tokens) && tokens[i+1].Text == "function") {
			symbolType = "function"
			offset := 1
			if tokens[i].Text == "async" {
				offset = 2
			}
			if i+offset < len(tokens) && tokens[i+offset].Kind == tokIdent {
				name = tokens[i+offset].Text
			}
		} else if tokens[i].Text == "class" && i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
			symbolType = "class"
			name = tokens[i+1].Text
		} else if tokens[i].Text == "const" || tokens[i].Text == "let" || tokens[i].Text == "var" {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name = tokens[i+1].Text
				if a.isFuncAssignment(tokens, i+1) {
					symbolType = "function"
				} else {
					symbolType = "variable"
				}
			}
		}
		if symbolType != "" && name != "" {
			*symbols = append(*symbols, SymbolInfo{
				Name:     name,
				Type:     symbolType,
				File:     filePath,
				Exported: true,
			})
		}
		return i
	}

	// export { name1, name2 } — 跳过
	if tokens[i].Text == "{" {
		return a.skipBalanced(tokens, i, "{", "}") + 1
	}

	return i
}

func (a *jsTokenAnalyzer) isFuncAssignment(tokens []syntaxToken, eqIdx int) bool {
	// = 后面紧跟 function 或 ( ... ) => 或 async ( ... ) =>
	if eqIdx+1 >= len(tokens) {
		return false
	}
	j := eqIdx + 1
	if tokens[j].Text == "function" || tokens[j].Text == "async" {
		return true
	}
	// 箭头函数: ( ... ) => 或 async ( ... ) =>
	if tokens[j].Text == "async" && j+1 < len(tokens) && tokens[j+1].Text == "(" {
		return true
	}
	if tokens[j].Text == "(" {
		return true
	}
	return false
}

func (a *jsTokenAnalyzer) isExported(tokens []syntaxToken, idx int) bool {
	// 向前查找 export 关键字（最多向前10个 token）
	for j := max(0, idx-10); j < idx; j++ {
		if tokens[j].Text == "export" {
			return true
		}
		if tokens[j].Text == ";" || tokens[j].Text == "}" {
			break
		}
	}
	return false
}

func (a *jsTokenAnalyzer) skipFuncBody(tokens []syntaxToken, nameIdx int) int {
	// 找到 ( ... ) 后的 { ... }
	parenIdx := -1
	for i := nameIdx + 1; i < len(tokens) && i < nameIdx+20; i++ {
		if tokens[i].Text == "(" {
			parenIdx = i
			break
		}
	}
	if parenIdx < 0 {
		return nameIdx + 1
	}
	closeParen := a.skipBalanced(tokens, parenIdx, "(", ")")
	if closeParen+1 < len(tokens) && tokens[closeParen+1].Text == "{" {
		return a.skipBalanced(tokens, closeParen+1, "{", "}") + 1
	}
	// 箭头函数可能没有 {}（单行）
	if closeParen+1 < len(tokens) && tokens[closeParen+1].Text == "=>" {
		if closeParen+2 < len(tokens) && tokens[closeParen+2].Text == "{" {
			return a.skipBalanced(tokens, closeParen+2, "{", "}") + 1
		}
		// 跳到下一个语句结束（; 或换行）——简化处理，跳到 ; 或最多20个 token
		for j := closeParen + 2; j < len(tokens) && j < closeParen+22; j++ {
			if tokens[j].Text == ";" {
				return j + 1
			}
		}
		return closeParen + 2
	}
	return closeParen + 1
}

func (a *jsTokenAnalyzer) skipClassBody(tokens []syntaxToken, classIdx int) int {
	// 找到 class Name [extends Base] {
	for i := classIdx + 1; i < len(tokens); i++ {
		if tokens[i].Text == "{" {
			return a.skipBalanced(tokens, i, "{", "}") + 1
		}
	}
	return classIdx + 1
}

func (a *jsTokenAnalyzer) skipBalanced(tokens []syntaxToken, start int, open, close string) int {
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

func (a *jsTokenAnalyzer) FindReferences(symbol string, allFiles map[string][]syntaxToken) []CallerInfo {
	var callers []CallerInfo
	for filePath, tokens := range allFiles {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Text == symbol {
				// 排除定义
				if a.isDefinition(tokens, i) {
					continue
				}
				// 函数调用: symbol( 或 .symbol(
				if i+1 < len(tokens) && tokens[i+1].Text == "(" {
					callers = append(callers, CallerInfo{
						File:     filePath,
						Line:     tokens[i].Line,
						CallExpr: a.getExpr(tokens, i),
					})
				}
				// 属性访问: obj.symbol — 不直接是调用，但记录访问
				if i+1 < len(tokens) && tokens[i+1].Text == "." {
					// symbol is being accessed as property, not called
					// skip or optionally record
				}
				// new Symbol()
				if i > 0 && tokens[i-1].Text == "new" {
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

func (a *jsTokenAnalyzer) isDefinition(tokens []syntaxToken, idx int) bool {
	// function symbol(
	if idx > 0 && tokens[idx-1].Text == "function" {
		return true
	}
	// class symbol
	if idx > 0 && tokens[idx-1].Text == "class" {
		return true
	}
	// const/let/var symbol =
	if idx > 0 && (tokens[idx-1].Text == "const" || tokens[idx-1].Text == "let" || tokens[idx-1].Text == "var") {
		return true
	}
	// export [default] function/class/const/let/var symbol
	if idx > 1 && tokens[idx-2].Text == "export" && (tokens[idx-1].Text == "function" || tokens[idx-1].Text == "class" || tokens[idx-1].Text == "const" || tokens[idx-1].Text == "let" || tokens[idx-1].Text == "var") {
		return true
	}
	if idx > 2 && tokens[idx-3].Text == "export" && tokens[idx-2].Text == "default" && (tokens[idx-1].Text == "function" || tokens[idx-1].Text == "class") {
		return true
	}
	// method: obj = { symbol() { ... } }
	if idx > 0 && tokens[idx-1].Text == ":" {
		return true
	}
	return false
}

func (a *jsTokenAnalyzer) getExpr(tokens []syntaxToken, idx int) string {
	start := max(0, idx-3)
	end := min(len(tokens), idx+4)
	var parts []string
	for i := start; i < end; i++ {
		parts = append(parts, tokens[i].Text)
	}
	return strings.Join(parts, " ")
}

// TypeScript 分析器继承自 JS，增加 interface/type 识别
type tsTokenAnalyzer struct {
	jsTokenAnalyzer
}

func newTSTokenAnalyzer() *tsTokenAnalyzer { return &tsTokenAnalyzer{} }
func (a *tsTokenAnalyzer) FileExts() []string {
	return []string{".ts", ".tsx"}
}
func (a *tsTokenAnalyzer) Tokenize(content string) []syntaxToken {
	return tokenizeAll(content, "typescript", false)
}

func (a *tsTokenAnalyzer) ParseSymbols(tokens []syntaxToken, filePath string) []SymbolInfo {
	// 先调用 JS 解析器提取 function/class/const/let/var
	symbols := a.jsTokenAnalyzer.ParseSymbols(tokens, filePath)

	// 额外解析 TypeScript 特有：interface, type, enum, namespace
	i := 0
	for i < len(tokens) {
		if tokens[i].Text == "export" {
			i++
			continue
		}

		// interface Name { ... }
		if tokens[i].Text == "interface" {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name := tokens[i+1].Text
				symbols = append(symbols, SymbolInfo{
					Name:     name,
					Type:     "interface",
					File:     filePath,
					Exported: a.jsTokenAnalyzer.isExported(tokens, i),
				})
				// 跳过 body
				if i+2 < len(tokens) && tokens[i+2].Text == "{" {
					i = a.jsTokenAnalyzer.skipBalanced(tokens, i+2, "{", "}") + 1
					continue
				}
			}
		}

		// type Name = ...
		if tokens[i].Text == "type" {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name := tokens[i+1].Text
				// 检查不是 typeof
				if name != "of" {
					symbols = append(symbols, SymbolInfo{
						Name:     name,
						Type:     "type",
						File:     filePath,
						Exported: a.jsTokenAnalyzer.isExported(tokens, i),
					})
				}
				// 跳过到 ;
				for j := i + 1; j < len(tokens); j++ {
					if tokens[j].Text == ";" {
						i = j + 1
						break
					}
					if j == len(tokens)-1 {
						i = len(tokens)
					}
				}
				continue
			}
		}

		// enum Name { ... }
		if tokens[i].Text == "enum" {
			if i+1 < len(tokens) && tokens[i+1].Kind == tokIdent {
				name := tokens[i+1].Text
				symbols = append(symbols, SymbolInfo{
					Name:     name,
					Type:     "enum",
					File:     filePath,
					Exported: a.jsTokenAnalyzer.isExported(tokens, i),
				})
				if i+2 < len(tokens) && tokens[i+2].Text == "{" {
					i = a.jsTokenAnalyzer.skipBalanced(tokens, i+2, "{", "}") + 1
					continue
				}
			}
		}

		i++
	}

	return symbols
}

func (a *tsTokenAnalyzer) isDefinition(tokens []syntaxToken, idx int) bool {
	if a.jsTokenAnalyzer.isDefinition(tokens, idx) {
		return true
	}
	if idx > 0 && tokens[idx-1].Text == "interface" {
		return true
	}
	if idx > 0 && tokens[idx-1].Text == "type" {
		return true
	}
	if idx > 0 && tokens[idx-1].Text == "enum" {
		return true
	}
	return false
}

func (a *tsTokenAnalyzer) FindReferences(symbol string, allFiles map[string][]syntaxToken) []CallerInfo {
	// TS 引用查找与 JS 类似，但还要考虑类型注解中的使用
	var callers []CallerInfo
	for filePath, tokens := range allFiles {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Text == symbol {
				if a.isDefinition(tokens, i) {
					continue
				}
				// 函数/构造函数调用
				if i+1 < len(tokens) && tokens[i+1].Text == "(" {
					callers = append(callers, CallerInfo{
						File:     filePath,
						Line:     tokens[i].Line,
						CallExpr: a.jsTokenAnalyzer.getExpr(tokens, i),
					})
				}
				// new Symbol()
				if i > 0 && tokens[i-1].Text == "new" {
					callers = append(callers, CallerInfo{
						File:     filePath,
						Line:     tokens[i].Line,
						CallExpr: a.jsTokenAnalyzer.getExpr(tokens, i),
					})
				}
				// 类型注解: : Symbol 或 as Symbol
				if i > 0 && (tokens[i-1].Text == ":" || tokens[i-1].Text == "as") {
					callers = append(callers, CallerInfo{
						File:     filePath,
						Line:     tokens[i].Line,
						CallExpr: a.jsTokenAnalyzer.getExpr(tokens, i),
					})
				}
			}
		}
	}
	return callers
}
