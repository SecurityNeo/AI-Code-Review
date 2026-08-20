package pipeline

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ===================== 核心数据结构 =====================

// CrossFileContext 跨文件调用链分析完整结果
type CrossFileContext struct {
	// 变更文件 -> 该文件的调用链上下文
	FileContexts map[string]*FileCallContext `json:"file_contexts"`

	// 项目级统计
	TotalFiles     int `json:"total_files"`
	TotalSymbols   int `json:"total_symbols"`
	TotalCallEdges int `json:"total_call_edges"`
}

// FileCallContext 单个文件的调用链上下文
type FileCallContext struct {
	FilePath string `json:"file_path"`

	// 本文件 export 的符号被哪些外部文件调用了
	UpstreamCallers []CallerInfo `json:"upstream_callers"`

	// 本文件调用了哪些外部符号
	DownstreamDeps []DependencyInfo `json:"downstream_deps"`

	// 同包（同目录）其他文件的相关符号
	SamePackageSymbols []SymbolInfo `json:"same_package_symbols"`
}

// CallerInfo 调用者信息
type CallerInfo struct {
	File        string `json:"file"`         // 调用者文件路径
	Function    string `json:"function"`     // 调用者函数名
	Line        int    `json:"line"`         // 调用行号
	CodeSnippet string `json:"code_snippet"` // 调用点代码（前后3行）
	CallExpr    string `json:"call_expr"`    // 调用表达式文本
	Distance    int    `json:"distance"`     // 调用链距离（0=direct）
}

// DependencyInfo 本文件依赖的外部符号
type DependencyInfo struct {
	File       string `json:"file"`        // 被依赖文件路径
	Symbol     string `json:"symbol"`      // 符号名
	Package    string `json:"package"`     // 包/模块名
	SymbolType string `json:"symbol_type"` // "func", "type", "var"
	IsInternal bool   `json:"is_internal"` // 是否是项目内部依赖
}

// SymbolInfo 符号定义信息
type SymbolInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // "func", "struct", "interface", "class"
	Package   string `json:"package"`
	File      string `json:"file"`
	Exported  bool   `json:"exported"`
	Signature string `json:"signature"` // 函数签名/类定义等
}

// ===================== CrossFileAnalyzer =====================

// CrossFileAnalyzer 跨文件调用链分析器入口
type CrossFileAnalyzer struct {
	repoDir      string
	lang         string
	excludedDirs []string
}

// NewCrossFileAnalyzer 创建分析器
func NewCrossFileAnalyzer(repoDir, lang string) *CrossFileAnalyzer {
	return &CrossFileAnalyzer{
		repoDir:      repoDir,
		lang:         lang,
		excludedDirs: []string{"vendor", "node_modules", ".git", "dist", "build", "target"},
	}
}

// Analyze 分析指定文件的跨文件调用链
// changedFiles: 变更文件的相对路径（相对于 repoDir）
func (a *CrossFileAnalyzer) Analyze(changedFiles []string) (*CrossFileContext, error) {
	ctx := &CrossFileContext{
		FileContexts: make(map[string]*FileCallContext),
	}

	switch a.lang {
	case "golang":
		return a.analyzeGo(changedFiles, ctx)
	default:
		return a.analyzeTokens(changedFiles, ctx)
	}
}

// ===================== Token 级语法感知分析（Java/Python/JS/TS）=====================

func (a *CrossFileAnalyzer) analyzeTokens(changedFiles []string, ctx *CrossFileContext) (*CrossFileContext, error) {
	analyzer := langToAnalyzer(a.lang)
	if analyzer == nil {
		return ctx, fmt.Errorf("不支持的编程语言: %s", a.lang)
	}

	exts := analyzer.FileExts()
	if len(exts) == 0 {
		// 通用分析器，接受所有文件
		exts = []string{""}
	}

	// Step 1: 全库扫描，构建 token 表和符号表
	type fileTokenSymbols struct {
		Path    string
		Tokens  []syntaxToken
		Symbols []SymbolInfo
	}

	allFiles := make(map[string]*fileTokenSymbols)

	_ = filepath.Walk(a.repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if a.isExcludedDir(path) {
			return filepath.SkipDir
		}
		extOk := len(exts) == 0
		for _, ext := range exts {
			if ext == "" || strings.HasSuffix(path, ext) {
				extOk = true
				break
			}
		}
		if !extOk {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		tokens := analyzer.Tokenize(string(content))
		symbols := analyzer.ParseSymbols(tokens, path)

		allFiles[path] = &fileTokenSymbols{
			Path:    path,
			Tokens:  tokens,
			Symbols: symbols,
		}
		ctx.TotalSymbols += len(symbols)
		return nil
	})

	ctx.TotalFiles = len(allFiles)

	// Step 2: 为每个变更文件查找引用
	for _, cf := range changedFiles {
		absPath := filepath.Join(a.repoDir, cf)
		fileCtx := &FileCallContext{FilePath: cf}
		fs, ok := allFiles[absPath]
		if !ok {
			continue
		}

		// 收集所有需要查找的 exported 符号
		// 转换为纯 token 表供 FindReferences 使用
		tokenMap := make(map[string][]syntaxToken)
		for path, fts := range allFiles {
			tokenMap[path] = fts.Tokens
		}

		for _, sym := range fs.Symbols {
			if !sym.Exported {
				continue
			}
			callers := analyzer.FindReferences(sym.Name, tokenMap)
			// 排除自身文件的引用
			for _, c := range callers {
				if c.File == cf || c.File == absPath {
					continue
				}
				fileCtx.UpstreamCallers = append(fileCtx.UpstreamCallers, c)
			}
		}

		fileCtx.UpstreamCallers = dedupCallers(fileCtx.UpstreamCallers)
		ctx.FileContexts[cf] = fileCtx
		ctx.TotalCallEdges += len(fileCtx.UpstreamCallers)
	}

	return ctx, nil
}

// ===================== Go 全库分析（最爱完善）=====================

func (a *CrossFileAnalyzer) analyzeGo(changedFiles []string, ctx *CrossFileContext) (*CrossFileContext, error) {
	// Step 1: 全库符号表（并发）
	symTable := &goSymbolTable{
		Symbols: make(map[string][]*SymbolInfo), // package.Func -> [defs]
		Files:   make(map[string]*goFileSymbols),
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	_ = filepath.Walk(a.repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if a.isExcludedDir(path) {
			return filepath.SkipDir
		}

		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, nil, parser.AllErrors)
			if err != nil {
				return
			}
			pkgName := f.Name.Name
			fileSyms := &goFileSymbols{
				Path:    p,
				Package: pkgName,
				Imports: make(map[string]string),
			}

			// imports
			for _, imp := range f.Imports {
				pathStr := strings.Trim(imp.Path.Value, `"`)
				alias := filepath.Base(pathStr)
				if imp.Name != nil && imp.Name.Name != "_" {
					alias = imp.Name.Name
				}
				fileSyms.Imports[alias] = pathStr
			}

			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.FuncDecl:
					name := x.Name.Name
					exported := ast.IsExported(name)
					recv := ""
					if x.Recv != nil && len(x.Recv.List) > 0 {
						recv = goTypeToString(x.Recv.List[0].Type)
					}
					sig := buildGoFuncSignature(name, recv, x.Type)
					sym := &SymbolInfo{
						Name:      name,
						Type:      "func",
						Package:   pkgName,
						File:      p,
						Exported:  exported,
						Signature: sig,
					}
					mu.Lock()
					fileSyms.Functions = append(fileSyms.Functions, sym)
					key := pkgName + "." + name
					if recv != "" {
						key = pkgName + "." + recv + "." + name
					}
					symTable.Symbols[key] = append(symTable.Symbols[key], sym)
					ctx.TotalSymbols++
					mu.Unlock()

				case *ast.TypeSpec:
					if _, ok := x.Type.(*ast.StructType); ok {
						sym := &SymbolInfo{
							Name:     x.Name.Name,
							Type:     "struct",
							Package:  pkgName,
							File:     p,
							Exported: ast.IsExported(x.Name.Name),
						}
						mu.Lock()
						fileSyms.Types = append(fileSyms.Types, sym)
						key := pkgName + "." + x.Name.Name
						symTable.Symbols[key] = append(symTable.Symbols[key], sym)
						ctx.TotalSymbols++
						mu.Unlock()
					}
					if _, ok := x.Type.(*ast.InterfaceType); ok {
						sym := &SymbolInfo{
							Name:     x.Name.Name,
							Type:     "interface",
							Package:  pkgName,
							File:     p,
							Exported: ast.IsExported(x.Name.Name),
						}
						mu.Lock()
						fileSyms.Types = append(fileSyms.Types, sym)
						key := pkgName + "." + x.Name.Name
						symTable.Symbols[key] = append(symTable.Symbols[key], sym)
						ctx.TotalSymbols++
						mu.Unlock()
					}
				}
				return true
			})

			mu.Lock()
			symTable.Files[p] = fileSyms
			mu.Unlock()
		}(path)

		return nil
	})

	wg.Wait()
	ctx.TotalFiles = len(symTable.Files)

	// Step 2: 调用者分析 - 对每个变更文件的 exported 符号，在全库搜索调用点
	for _, cf := range changedFiles {
		absPath := filepath.Join(a.repoDir, cf)
		fileCtx := &FileCallContext{FilePath: cf}
		fileSyms, ok := symTable.Files[absPath]
		if !ok {
			continue
		}

		// 找出本文件的 exported 符号
		for _, fn := range fileSyms.Functions {
			if !fn.Exported {
				continue
			}
			// 搜索全库调用点
			callers := a.findGoCallers(fn, symTable)
			fileCtx.UpstreamCallers = append(fileCtx.UpstreamCallers, callers...)
		}
		for _, t := range fileSyms.Types {
			if !t.Exported {
				continue
			}
			// 搜索类型使用点
			callers := a.findGoTypeUsers(t, symTable)
			fileCtx.UpstreamCallers = append(fileCtx.UpstreamCallers, callers...)
		}

		// Step 3: 同包中与变更文件相关的符号（被变更文件调用或引用）
		for path, fs := range symTable.Files {
			if fs.Package == fileSyms.Package && path != absPath {
				// 只收集被当前变更文件引用的同包符号
				for _, fn := range fs.Functions {
					if a.isReferencedByFile(fileSyms, fn) {
						fileCtx.SamePackageSymbols = append(fileCtx.SamePackageSymbols, *fn)
					}
				}
				for _, t := range fs.Types {
					if a.isReferencedByFile(fileSyms, t) {
						fileCtx.SamePackageSymbols = append(fileCtx.SamePackageSymbols, *t)
					}
				}
			}
		}
		// 限制数量，避免无关符号过多
		if len(fileCtx.SamePackageSymbols) > 10 {
			fileCtx.SamePackageSymbols = fileCtx.SamePackageSymbols[:10]
		}

		// Step 4: 依赖分析（imports 中项目内的包）
		for impAlias, impPath := range fileSyms.Imports {
			_ = impAlias
			// 判断是否是项目内 import（根据路径前缀判断）
			// 这里简化：如果 import 路径对应的包在符号表中有定义 → 项目内
			for key, syms := range symTable.Symbols {
				if strings.HasPrefix(key, impPath+".") && len(syms) > 0 {
					for _, sym := range syms {
						fileCtx.DownstreamDeps = append(fileCtx.DownstreamDeps, DependencyInfo{
							File:       sym.File,
							Symbol:     sym.Name,
							Package:    sym.Package,
							SymbolType: sym.Type,
							IsInternal: true,
						})
					}
				}
			}
		}

		// 去重
		fileCtx.UpstreamCallers = dedupCallers(fileCtx.UpstreamCallers)
		fileCtx.DownstreamDeps = dedupDeps(fileCtx.DownstreamDeps)

		ctx.FileContexts[cf] = fileCtx
		ctx.TotalCallEdges += len(fileCtx.UpstreamCallers) + len(fileCtx.DownstreamDeps)
	}

	return ctx, nil
}

// goSymbolTable Go 语言符号表
type goSymbolTable struct {
	Symbols map[string][]*SymbolInfo // 全限定名 -> 定义列表
	Files   map[string]*goFileSymbols
}

type goFileSymbols struct {
	Path      string
	Package   string
	Imports   map[string]string // alias -> full path
	Functions []*SymbolInfo
	Types     []*SymbolInfo
}

func (a *CrossFileAnalyzer) findGoCallers(target *SymbolInfo, table *goSymbolTable) []CallerInfo {
	var callers []CallerInfo

	// 搜索模式：函数名( 或 接收者.函数名(
	searchPatterns := []string{target.Name + "("}
	if target.Type == "func" {
		// 尝试获取接收者（从 signature 解析）
		// 简化：搜索 "FuncName(" 在所有 .go 文件中
	}

	for path, _ := range table.Files {
		if path == target.File {
			continue // 跳过自身
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for i, line := range lines {
			for _, pattern := range searchPatterns {
				if strings.Contains(line, pattern) {
					// 排除注释行
					trimmed := strings.TrimSpace(line)
					if strings.HasPrefix(trimmed, "//") {
						continue
					}
					// 获取上下文（前后3行）
					snippetStart := max(0, i-2)
					snippetEnd := min(len(lines), i+3)
					snippet := strings.Join(lines[snippetStart:snippetEnd], "\n")

					// 查找所在函数名（简化：搜索上方最近的 func 声明）
					funcName := a.findEnclosingFunc(lines, i)

					callers = append(callers, CallerInfo{
						File:        a.relPath(path),
						Function:    funcName,
						Line:        i + 1,
						CodeSnippet: snippet,
						CallExpr:    strings.TrimSpace(line),
						Distance:    0,
					})
				}
			}
		}
	}
	return callers
}

func (a *CrossFileAnalyzer) findGoTypeUsers(target *SymbolInfo, table *goSymbolTable) []CallerInfo {
	// 搜索类型的使用：type FieldName *TargetType, func (t *TargetType), var x TargetType
	var callers []CallerInfo
	searchPatterns := []string{
		"*" + target.Name,
		" " + target.Name + " ",
		" " + target.Name + "{",
		" " + target.Name + "\n",
		"(" + target.Name + ")",
	}

	for path := range table.Files {
		if path == target.File {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for i, line := range lines {
			for _, pattern := range searchPatterns {
				if strings.Contains(line, pattern) {
					trimmed := strings.TrimSpace(line)
					if strings.HasPrefix(trimmed, "//") {
						continue
					}
					snippetStart := max(0, i-2)
					snippetEnd := min(len(lines), i+3)
					snippet := strings.Join(lines[snippetStart:snippetEnd], "\n")
					funcName := a.findEnclosingFunc(lines, i)
					callers = append(callers, CallerInfo{
						File:        a.relPath(path),
						Function:    funcName,
						Line:        i + 1,
						CodeSnippet: snippet,
						CallExpr:    strings.TrimSpace(line),
						Distance:    0,
					})
				}
			}
		}
	}
	return callers
}

func (a *CrossFileAnalyzer) findEnclosingFunc(lines []string, idx int) string {
	for i := idx; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "func ") {
			// 提取函数名: func [可选接收者] 函数名(
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				namePart := parts[1]
				// 跳过接收者部分 (recvType)
				if strings.HasPrefix(namePart, "(") {
					if len(parts) >= 3 {
						namePart = parts[2]
					}
				}
				// 取函数名（去掉参数列表）
				parenIdx := strings.Index(namePart, "(")
				if parenIdx > 0 {
					return namePart[:parenIdx]
				}
				return namePart
			}
			return "[anonymous]"
		}
	}
	return ""
}

func (a *CrossFileAnalyzer) relPath(absPath string) string {
	rel, _ := filepath.Rel(a.repoDir, absPath)
	return rel
}

func (a *CrossFileAnalyzer) isExcludedDir(path string) bool {
	for _, ex := range a.excludedDirs {
		if strings.Contains(path, "/"+ex+"/") || strings.HasSuffix(path, "/"+ex) {
			return true
		}
	}
	return false
}

// isReferencedByFile 判断目标符号是否被变更文件引用（调用或使用）
func (a *CrossFileAnalyzer) isReferencedByFile(fileSyms *goFileSymbols, target *SymbolInfo) bool {
	// 读取变更文件内容
	content, err := os.ReadFile(fileSyms.Path)
	if err != nil {
		return false
	}
	contentStr := string(content)
	// 搜索符号名称的使用（排除注释和定义）
	lines := strings.Split(contentStr, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 跳过注释行
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		// 跳过自身定义行（如 "func TargetName(" 或 "type TargetName struct"）
		if strings.HasPrefix(trimmed, "func "+target.Name+"(") ||
			strings.HasPrefix(trimmed, "type "+target.Name+" ") {
			continue
		}
		// 检查是否包含符号名（后面跟括号表示调用，或作为类型使用）
		if strings.Contains(line, target.Name) {
			return true
		}
	}
	return false
}

// ===================== 辅助函数 =====================

func dedupCallers(items []CallerInfo) []CallerInfo {
	seen := make(map[string]bool)
	var out []CallerInfo
	for _, c := range items {
		key := c.File + ":" + c.Function + ":" + fmt.Sprintf("%d", c.Line)
		if !seen[key] {
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

func dedupDeps(items []DependencyInfo) []DependencyInfo {
	seen := make(map[string]bool)
	var out []DependencyInfo
	for _, d := range items {
		key := d.File + ":" + d.Symbol
		if !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}
	return out
}

func buildGoFuncSignature(name, receiver string, ft *ast.FuncType) string {
	var sb strings.Builder
	if receiver != "" {
		sb.WriteString("func (" + receiver + ") ")
	} else {
		sb.WriteString("func ")
	}
	sb.WriteString(name + "(")
	if ft.Params != nil {
		for i, p := range ft.Params.List {
			if i > 0 {
				sb.WriteString(", ")
			}
			for j, n := range p.Names {
				if j > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(n.Name + " ")
				sb.WriteString(goTypeToString(p.Type))
			}
		}
	}
	sb.WriteString(")")
	if ft.Results != nil && len(ft.Results.List) > 0 {
		sb.WriteString(" ")
		if len(ft.Results.List) > 1 {
			sb.WriteString("(")
		}
		for i, r := range ft.Results.List {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(goTypeToString(r.Type))
		}
		if len(ft.Results.List) > 1 {
			sb.WriteString(")")
		}
	}
	return sb.String()
}
