package dataflow

import (
	"sort"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// TaintAnalyzer 基于 AST 的污点分析器
// 核心改进 (v2):
// - 基于 UnifiedAST.CallSites / VarBindings，不再依赖 body_snippet 字符串匹配
// - 支持多语言/多框架 Source/Sink 配置
// - 支持单文件内变量级污点传播
// - 可选跨函数传播（基于 graph）
type TaintAnalyzer struct {
	config *TaintConfig
	asts   []*parser.UnifiedAST
	graph  *graph.MemorySymbolGraph
	lang   string // normalized language
}

// NewTaintAnalyzer 创建分析器
func NewTaintAnalyzer(lang string, asts []*parser.UnifiedAST, graph *graph.MemorySymbolGraph) *TaintAnalyzer {
	return &TaintAnalyzer{
		config: NewDefaultTaintConfig(),
		asts:   asts,
		graph:  graph,
		lang:   normalizeLang(lang),
	}
}

// Analyze 执行污点分析
func (a *TaintAnalyzer) Analyze() *DataFlowResult {
	result := &DataFlowResult{Flows: make([]TaintFlow, 0)}

	// Phase 1: 单文件内分析（基于 AST）
	for _, ast := range a.asts {
		flows := a.analyzeFile(ast)
		result.Flows = append(result.Flows, flows...)
	}

	// Phase 2: 跨函数传播（基于 graph，可选）
	if a.graph != nil {
		crossFlows := a.analyzeCrossFunction(result.Flows)
		result.Flows = append(result.Flows, crossFlows...)
	}

	return result
}

// -------------------- Phase 1: 单文件内分析 --------------------

// fileState 单文件内的污点状态
type fileState struct {
	tainted     map[string]*SourceInfo // varName -> SourceInfo (被污染的变量)
	sanitized   map[string]bool        // varName -> true (已净化)
	propagation map[string][]string    // varName -> []propagatedFrom (传播链：谁污染了此变量)
}

func newFileState() *fileState {
	return &fileState{
		tainted:     make(map[string]*SourceInfo),
		sanitized:   make(map[string]bool),
		propagation: make(map[string][]string),
	}
}

func (a *TaintAnalyzer) analyzeFile(ast *parser.UnifiedAST) []TaintFlow {
	cfg := a.config.GetLanguageConfig(a.lang)
	if cfg == nil {
		return nil
	}

	state := newFileState()

	// Step 1: 从 VarBindings 识别 Source（最精确的污点源）
	for _, vb := range ast.VarBindings {
		if vb.CallSite != nil {
			if sourceInfo := a.matchSource(vb.CallSite, cfg, ast.FilePath); sourceInfo != nil {
				state.tainted[vb.VarName] = sourceInfo
			}
		}
	}

	// Step 1b: Java/Python 注解/装饰器参数识别
	state.tainted = a.annotateSources(ast, cfg, state.tainted)

	// Step 2: 污点传播分析（通过 VarBindings 的 Literal 分析）
	// 检测形如: sql := "SELECT..." + user （user 已污染 → sql 也被污染）
	// 同时检测 sanitizer 调用: intId := strconv.Atoi(userId) → intId 已净化
	state.propagateTaint(ast, cfg)

	// Step 3: 按行号排序 CallSites，模拟执行顺序
	sorted := make([]*parser.UnifiedCallSite, 0, len(ast.CallSites))
	for i := range ast.CallSites {
		sorted = append(sorted, &ast.CallSites[i])
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Location.LineStart < sorted[j].Location.LineStart
	})

	var flows []TaintFlow

	for _, cs := range sorted {
		// 3a. 检查是否 Sanitizer（净化后停止传播）
		if a.matchSanitizer(cs, cfg) {
			for _, arg := range cs.Arguments {
				argVars := extractVarNames(arg)
				for _, v := range argVars {
					if _, ok := state.tainted[v]; ok {
						state.sanitized[v] = true
						// 如果净化后的变量被赋值给新变量，新变量也被视为净化
						state.sanitizeDownstream(v)
					}
				}
			}
			continue
		}

		// 3b. 检查是否 Sink
		if sinkInfo := a.matchSink(cs, cfg, ast.FilePath); sinkInfo != nil {
			// 首先检查整个调用是否为参数化查询/预编译语句
			isParam := a.isParameterizedQuery(cs.Arguments, sinkInfo.Kind)
			for _, arg := range cs.Arguments {
				argVars := extractVarNames(arg)
				for _, v := range argVars {
					if src, ok := state.tainted[v]; ok && !state.sanitized[v] {
						// 参数化查询 → 降低风险并标记为已安全
						if isParam {
							flow := TaintFlow{
								Source:      *src,
								Sink:        *sinkInfo,
								Path:        a.buildPath(state, v, cs.CallerFunc, ast.FilePath, cs.Location.LineStart),
								RiskLevel:   a.calcRiskLevelWithParam(sinkInfo.Kind),
								Category:    sinkInfo.Kind,
								IsSanitized: true,
							}
							if !a.hasFlow(flows, flow) {
								flows = append(flows, flow)
							}
							continue
						}
						// 检查此前是否有类型转换 Sanitizer
						if a.isTypeSanitized(state, v, sinkInfo.Kind) {
							continue
						}
						flow := TaintFlow{
							Source:    *src,
							Sink:      *sinkInfo,
							Path:      a.buildPath(state, v, cs.CallerFunc, ast.FilePath, cs.Location.LineStart),
							RiskLevel: a.calcRiskLevel(sinkInfo.Kind),
							Category:  sinkInfo.Kind,
						}
						if !a.hasFlow(flows, flow) {
							flows = append(flows, flow)
						}
					}
				}
			}
		}
	}

	return flows
}

// propagateTaint 通过 VarBindings 传播污点
// 当赋值右侧表达式包含已污染变量时，左侧变量也被污染。
// 当右侧表达式包含 sanitizer（如 strconv.Atoi）时，左侧变量被标记为已净化。
func (s *fileState) propagateTaint(ast *parser.UnifiedAST, cfg *LanguageConfig) {
	for _, vb := range ast.VarBindings {
		if vb.CallSite != nil {
			continue // 已由 Step 1 处理为 Source
		}
		if vb.Literal == "" {
			continue
		}
		// 先检查右侧是否包含 sanitizer 调用（如 strconv.Atoi(userId)）
		sanitized := false
		if cfg != nil {
			for _, san := range cfg.Sanitizers {
				if strings.Contains(vb.Literal, san.Pattern) {
					s.sanitized[vb.VarName] = true
					sanitized = true
					break
				}
			}
		}
		if sanitized {
			continue
		}
		// 检查字面量中是否包含已污染变量
		deps := extractVarNames(vb.Literal)
		var pollutedDeps []string
		for _, dep := range deps {
			if _, ok := s.tainted[dep]; ok && !s.sanitized[dep] {
				pollutedDeps = append(pollutedDeps, dep)
			}
		}
		if len(pollutedDeps) > 0 {
			// 左侧变量继承第一个污染源的 SourceInfo
			s.tainted[vb.VarName] = s.tainted[pollutedDeps[0]]
			s.propagation[vb.VarName] = pollutedDeps
		}
	}
}

// sanitizeDownstream 净化下游传播链
func (s *fileState) sanitizeDownstream(varName string) {
	for childVar, parents := range s.propagation {
		for _, p := range parents {
			if p == varName {
				s.sanitized[childVar] = true
				s.sanitizeDownstream(childVar)
			}
		}
	}
}

// buildPath 构建污点传播路径：从 Source 到 Sink 的完整变量流转链
func (a *TaintAnalyzer) buildPath(state *fileState, sinkVar, callerFunc, file string, line int) []PathNode {
	var path []PathNode
	visited := make(map[string]bool)
	var dfs func(v string)
	dfs = func(v string) {
		if visited[v] {
			return
		}
		visited[v] = true
		if parents, ok := state.propagation[v]; ok {
			for _, p := range parents {
				dfs(p)
			}
		}
		path = append(path, PathNode{Function: callerFunc, File: file, Line: line, VarName: v})
	}
	dfs(sinkVar)
	// 路径反转：从 Source 到 Sink
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// matchSource 检查 CallSite 是否匹配 Source pattern
func (a *TaintAnalyzer) matchSource(cs *parser.UnifiedCallSite, cfg *LanguageConfig, filePath string) *SourceInfo {
	for _, sp := range cfg.Sources {
		if a.matchesPattern(cs, sp.Type, sp.Pattern) {
			return &SourceInfo{
				Kind:     sp.Kind,
				Name:     cs.TargetFunc,
				Function: cs.CallerFunc,
				File:     filePath,
				Line:     cs.Location.LineStart,
			}
		}
	}
	return nil
}

// matchSink 检查 CallSite 是否匹配 Sink pattern
func (a *TaintAnalyzer) matchSink(cs *parser.UnifiedCallSite, cfg *LanguageConfig, filePath string) *SinkInfo {
	for _, sp := range cfg.Sinks {
		if a.matchesPattern(cs, sp.Type, sp.Pattern) {
			return &SinkInfo{
				Kind:     sp.Kind,
				Function: cs.TargetFunc,
				File:     filePath,
				Line:     cs.Location.LineStart,
			}
		}
	}
	return nil
}

// matchSanitizer 检查 CallSite 是否匹配 Sanitizer pattern
func (a *TaintAnalyzer) matchSanitizer(cs *parser.UnifiedCallSite, cfg *LanguageConfig) bool {
	for _, sp := range cfg.Sanitizers {
		if a.matchesPattern(cs, MatchMethodCall, sp.Pattern) || a.matchesPattern(cs, MatchFunctionCall, sp.Pattern) {
			return true
		}
	}
	return false
}

// matchesPattern 匹配 CallSite 与模式
func (a *TaintAnalyzer) matchesPattern(cs *parser.UnifiedCallSite, mt MatchType, pattern string) bool {
	target := cs.TargetFunc
	receiver := cs.ReceiverVar
	targetPkg := cs.TargetPkg

	switch mt {
	case MatchMethodCall:
		// Pattern: "Query" 匹配任何 TargetFunc == "Query"
		// Pattern: "*.Query" 匹配 ReceiverVar 非空且 TargetFunc == "Query"
		if strings.HasPrefix(pattern, "*.") {
			fn := strings.TrimPrefix(pattern, "*.")
			return receiver != "" && target == fn
		}
		return target == pattern

	case MatchFunctionCall:
		// Pattern: "exec.Command" 匹配 ReceiverVar == "exec" && TargetFunc == "Command"
		if strings.Contains(pattern, ".") {
			parts := strings.SplitN(pattern, ".", 2)
			return receiver == parts[0] && target == parts[1]
		}
		return target == pattern

	case MatchFieldAccess:
		// Pattern: "request.args" → 匹配 ReceiverVar == "request" && TargetFunc == "args"
		//           或 TargetPkg == "request" && TargetFunc == "args" (Python/Java 风格)
		// Pattern: "request.args.get" → 匹配 ReceiverVar == "request.args" && TargetFunc == "get"
		//           或 TargetPkg == "request" && TargetFunc == "get"
		if strings.Contains(pattern, ".") {
			idx := strings.LastIndex(pattern, ".")
			prefix := pattern[:idx]
			suffix := pattern[idx+1:]
			// 方式1: ReceiverVar 匹配完整前缀 (Go 风格)
			if receiver == prefix && target == suffix {
				return true
			}
			// 方式2: TargetPkg 匹配首级前缀 (Python/Java/JS 风格)
			if targetPkg != "" {
				firstDot := strings.Index(pattern, ".")
				if firstDot > 0 && targetPkg == pattern[:firstDot] && target == suffix {
					return true
				}
			}
		}
		return target == pattern

	case MatchAnnotation:
		// 通过 Annotations 匹配，需要框架适配器已经提取到 Annotations
		// 这里不做 CallSite 匹配，而是检查函数的 Annotations
		return false
	}

	return false
}

// calcRiskLevel 根据 Sink 类型计算风险等级
func (a *TaintAnalyzer) calcRiskLevel(sinkKind string) string {
	switch sinkKind {
	case SinkSQLInjection, SinkCommandInjection:
		return "high"
	case SinkXSS, SinkPathTraversal, SinkSSRF:
		return "medium"
	case SinkDeserialization, SinkFileWrite:
		return "low"
	default:
		return "medium"
	}
}

// calcRiskLevelWithParam 参数化查询的风险等级（已安全，降级）
func (a *TaintAnalyzer) calcRiskLevelWithParam(sinkKind string) string {
	switch sinkKind {
	case SinkSQLInjection:
		return "low" // 参数化查询：SQL 注入风险极低
	case SinkCommandInjection:
		return "low" // 参数化/数组方式：命令注入风险极低
	default:
		return a.calcRiskLevel(sinkKind)
	}
}

// isParameterizedQuery 检测参数化查询（预编译语句）
// 特征：SQL 字符串参数中包含 ?, $1, :name 等占位符
func (a *TaintAnalyzer) isParameterizedQuery(args []string, sinkKind string) bool {
	if sinkKind != SinkSQLInjection {
		return false
	}
	for _, arg := range args {
		for _, ph := range sqlPlaceholders {
			if strings.Contains(arg, ph) {
				return true
			}
		}
	}
	return false
}

// sqlPlaceholders 预编译 SQL 占位符（包级变量，避免重复分配）
var sqlPlaceholders = []string{"?", "$1", "$2", "$3", "$4", "$5", ":name", ":id", ":value"}

// isTypeSanitized 检查变量是否已被类型转换 Sanitizer 处理。
// propagateTaint 已经将所有 sanitizer 调用（如 strconv.Atoi）的左侧变量标记为 sanitized，
// 这里只需检查变量自身或其传播链祖先是否已被净化。
func (a *TaintAnalyzer) isTypeSanitized(state *fileState, varName string, sinkKind string) bool {
	if sinkKind != SinkSQLInjection {
		return false
	}
	// 变量自身是否已被净化（如 intId := strconv.Atoi(userId) 中 intId 被标记）
	if state.sanitized[varName] {
		return true
	}
	// 传播链中的祖先是否已被净化
	if parents, ok := state.propagation[varName]; ok {
		for _, p := range parents {
			if state.sanitized[p] {
				return true
			}
		}
	}
	return false
}

// hasFlow 去重：避免同一 Source + Sink 组合被重复报告
func (a *TaintAnalyzer) hasFlow(flows []TaintFlow, f TaintFlow) bool {
	for _, existing := range flows {
		if existing.Source.File == f.Source.File &&
			existing.Source.Line == f.Source.Line &&
			existing.Sink.File == f.Sink.File &&
			existing.Sink.Line == f.Sink.Line &&
			existing.Category == f.Category {
			return true
		}
	}
	return false
}

// annotateSources 从函数注解/装饰器/参数注解中识别污点源
// 优先级：参数级注解 > 函数级注解
// 支持：Java Spring, Python FastAPI, JavaScript Express 等
func (a *TaintAnalyzer) annotateSources(ast *parser.UnifiedAST, cfg *LanguageConfig, tainted map[string]*SourceInfo) map[string]*SourceInfo {
	for _, fn := range ast.Functions {
		// Step 1: 从参数级注解识别（最精确）
		for _, param := range fn.Params {
			if len(param.Annotations) == 0 {
				continue
			}
			// 排除框架内部类型参数
			if isFrameworkInternalType(param.Type) {
				continue
			}
			matchedSource := a.matchParamAnnotation(param.Annotations, cfg)
			if matchedSource != nil {
				if _, ok := tainted[param.Name]; !ok {
					tainted[param.Name] = &SourceInfo{
						Kind:     matchedSource.Kind,
						Name:     param.Name,
						Function: fn.Name,
						File:     ast.FilePath,
						Line:     fn.Location.LineStart,
					}
				}
			}
		}

		// Step 2: Fallback：函数级注解（当参数没有注解时，函数级注解标记所有参数）
		for _, ann := range fn.Annotations {
			for _, sp := range cfg.Sources {
				if sp.Type != MatchAnnotation {
					continue
				}
				if ann == sp.Pattern || strings.Contains(ann, sp.Pattern) {
					for _, param := range fn.Params {
						if isFrameworkInternalType(param.Type) {
							continue
						}
						// 优先使用参数级注解的结果，避免覆盖
						if _, ok := tainted[param.Name]; !ok {
							tainted[param.Name] = &SourceInfo{
								Kind:     sp.Kind,
								Name:     param.Name,
								Function: fn.Name,
								File:     ast.FilePath,
								Line:     fn.Location.LineStart,
							}
						}
					}
				}
			}
		}
	}
	return tainted
}

// matchParamAnnotation 匹配参数级注解与 SourcePattern
// 支持: Java @RequestParam, Python Query/Body, 等
func (a *TaintAnalyzer) matchParamAnnotation(anns []string, cfg *LanguageConfig) *SourcePattern {
	for _, ann := range anns {
		// 去掉前导 @（Java 风格）
		cleanAnn := strings.TrimPrefix(ann, "@")
		for _, sp := range cfg.Sources {
			if sp.Type != MatchAnnotation {
				continue
			}
			patternClean := strings.TrimPrefix(sp.Pattern, "@")
			if cleanAnn == patternClean || strings.Contains(strings.ToLower(ann), strings.ToLower(sp.Pattern)) {
				return &sp
			}
		}
		// Python FastAPI: 如 "Query(...)", "Body(...)" → 提取函数名
		if idx := strings.Index(ann, "("); idx > 0 {
			funcName := ann[:idx]
			for _, sp := range cfg.Sources {
				if sp.Type != MatchAnnotation {
					continue
				}
				if strings.EqualFold(funcName, sp.Pattern) {
					return &sp
				}
			}
		}
	}
	return nil
}

func isFrameworkInternalType(typ string) bool {
	internals := []string{
		"HttpServletRequest", "HttpServletResponse", "ServletRequest", "ServletResponse",
		"Model", "ModelMap", "RedirectAttributes", "BindingResult",
		"Locale", "TimeZone", "ZoneId", "OutputStream", "Writer", "PrintWriter",
		"HttpSession", "Principal", "Authentication",
	}
	for _, t := range internals {
		if strings.Contains(typ, t) {
			return true
		}
	}
	return false
}

// -------------------- Phase 2: 跨函数传播 (基于 graph) --------------------

func (a *TaintAnalyzer) analyzeCrossFunction(existingFlows []TaintFlow) []TaintFlow {
	if a.graph == nil {
		return nil
	}

	cfg := a.config.GetLanguageConfig(a.lang)
	if cfg == nil {
		return nil
	}

	var crossFlows []TaintFlow
	visited := make(map[string]bool)

	// 收集变更文件列表
	changedFiles := make(map[string]bool)
	for _, ast := range a.asts {
		changedFiles[ast.FilePath] = true
	}

	// 预构建 graph 函数名索引（避免 O(N*N) 遍历）
	funcIndex := make(map[string][]*graph.MemorySymbolNode)
	for _, node := range a.graph.AllNodes() {
		if node.Type == graph.NodeFunc {
			funcIndex[node.Name] = append(funcIndex[node.Name], node)
		}
	}

	// Step 1: 收集变更文件中的 Source 函数（graph 节点 ID）
	sourceNodes := make(map[string]bool)
	for _, ast := range a.asts {
		// 从 VarBindings 中的 Source 推导函数（只标记包含 Source 的函数）
		sourceFuncs := make(map[string]bool)
		for _, vb := range ast.VarBindings {
			if vb.CallSite != nil && a.matchSource(vb.CallSite, cfg, ast.FilePath) != nil {
				if vb.CallSite.CallerFunc != "" {
					sourceFuncs[vb.CallSite.CallerFunc] = true
				}
			}
		}
		// 加上注解参数的 Source（按函数精确标记）
		for _, fn := range ast.Functions {
			for _, param := range fn.Params {
				if matched := a.matchParamAnnotation(param.Annotations, cfg); matched != nil {
					sourceFuncs[fn.Name] = true
				}
			}
		}
		// 使用索引快速查找 graph 节点
		for funcName := range sourceFuncs {
			for _, node := range funcIndex[funcName] {
				if node.Type == graph.NodeFunc && node.File == ast.FilePath {
					sourceNodes[node.ID] = true
				}
			}
		}
	}

	// Step 2: 遍历变更文件函数，查找它们调用的非变更文件函数是否有 Sink
	for sourceID := range sourceNodes {
		for _, rel := range a.graph.GetRelations(sourceID, graph.RelCalls) {
			callee, ok := a.graph.GetNode(rel.To)
			if !ok || callee.Type != graph.NodeFunc {
				continue
			}
			// 只处理非变更文件的被调用者
			if changedFiles[callee.File] {
				continue
			}
			snippet, _ := callee.Properties["body_snippet"].(string)
			if snippet == "" {
				continue
			}
			for _, sp := range cfg.Sinks {
				if strings.Contains(snippet, sp.Pattern) {
					key := sourceID + "->" + callee.ID + "->" + sp.Kind
					if visited[key] {
						continue
					}
					visited[key] = true

					callerNode, _ := a.graph.GetNode(sourceID)
					if callerNode == nil {
						continue
					}

					crossFlows = append(crossFlows, TaintFlow{
						Source: SourceInfo{
							Kind:     "cross_file_propagation",
							Name:     callerNode.Name,
							Function: callerNode.Name,
							File:     callerNode.File,
							Line:     callerNode.Location.LineStart,
						},
						Sink: SinkInfo{
							Kind:     sp.Kind,
							Function: sp.Pattern,
							File:     callee.File,
							Line:     callee.Location.LineStart,
						},
						Path: []PathNode{
							{Function: callerNode.Name, File: callerNode.File, Line: callerNode.Location.LineStart},
							{Function: callee.Name, File: callee.File, Line: callee.Location.LineStart},
						},
						RiskLevel: sp.Severity,
						Category:  sp.Kind,
					})
				}
			}
		}
	}

	return crossFlows
}

// -------------------- 工具函数 --------------------

// extractVarNames 从参数表达式中提取可能的变量名
// 策略：按运算符分割，提取简单的标识符
func extractVarNames(expr string) []string {
	var names []string
	// 按常见运算符分割
	delimiters := []string{"+", "-", "*", "/", "%", "==", "!=", "<", ">", "<=", ">=", "&&", "||", "&", "|", "^", "<<", ">>"}
	parts := []string{expr}
	for _, d := range delimiters {
		var newParts []string
		for _, p := range parts {
			for _, s := range strings.Split(p, d) {
				newParts = append(newParts, s)
			}
		}
		parts = newParts
	}
	// 再按括号和逗号分割
	finalParts := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "_" {
			continue
		}
		// 去掉字符串字面量
		if strings.HasPrefix(p, `"`) || strings.HasPrefix(p, `'`) || strings.HasPrefix(p, "`") {
			continue
		}
		// 去掉数字字面量
		isNum := true
		for _, r := range p {
			if r < '0' || r > '9' {
				isNum = false
				break
			}
		}
		if isNum {
			continue
		}
		// 分割嵌套调用 (如 toString(obj)) → 提取 obj
		if idx := strings.Index(p, "("); idx >= 0 && strings.HasSuffix(p, ")") {
			inner := p[idx+1 : len(p)-1]
			inner = strings.TrimSpace(inner)
			if inner != "" {
				finalParts = append(finalParts, inner)
			}
			continue
		}
		finalParts = append(finalParts, p)
	}

	// 去重
	seen := make(map[string]bool)
	for _, p := range finalParts {
		if isSimpleIdentifier(p) && !seen[p] {
			seen[p] = true
			names = append(names, p)
		}
	}
	return names
}

func isSimpleIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r > 127 {
				continue
			}
			return false
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r > 127 {
			continue
		}
		return false
	}
	return true
}
