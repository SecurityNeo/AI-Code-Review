package dataflow

// defuse.go - 保留类型定义用于向后兼容
// 核心逻辑已迁移到 analyzer.go

// DefUseChain 单函数内的 def-use 链
type DefUseChain struct {
	Defs      []DefPoint
	Uses      []UsePoint
	ParamFlow []ParamFlowEdge
}

// DefPoint 变量定义点
type DefPoint struct {
	VarName  string
	Line     int
	Kind     string // assign | param | decl
	FuncName string
}

// UsePoint 变量使用点
type UsePoint struct {
	VarName  string
	Line     int
	Kind     string // call_arg | condition | return
	FuncName string
}

// ParamFlowEdge 参数流向边
type ParamFlowEdge struct {
	FromVar string
	ToVar   string
	Line    int
}

// DefUseAnalyzer def-use 分析器（Deprecated：请使用 TaintAnalyzer）
type DefUseAnalyzer struct{}

// NewDefUseAnalyzer 创建分析器
func NewDefUseAnalyzer() *DefUseAnalyzer {
	return &DefUseAnalyzer{}
}

// AnalyzeFunction 分析函数体（已废弃，返回空结果）
func (a *DefUseAnalyzer) AnalyzeFunction(funcName, bodySnippet string) *DefUseChain {
	return &DefUseChain{}
}
