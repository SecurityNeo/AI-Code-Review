package dataflow

import (
	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

// SourceKind 数据源类型
const (
	SourceHTTPParam  = "http_param"
	SourceHeader     = "header"
	SourceCookie     = "cookie"
	SourceBody       = "body"
	SourceQueryParam = "query_param"
	SourceDB         = "db"
	SourceFile       = "file"
	SourceEnv        = "env"
)

// SinkKind 危险Sink类型
const (
	SinkSQLInjection     = "sql_injection"
	SinkCommandInjection = "command_injection"
	SinkXSS              = "xss"
	SinkPathTraversal    = "path_traversal"
	SinkSSRF             = "ssrf"
	SinkDeserialization  = "deserialization"
	SinkFileWrite        = "file_write"
)

// DataFlowResult 数据流分析结果
type DataFlowResult struct {
	Flows []TaintFlow `json:"flows"`
}

// TaintFlow 污点流路径
type TaintFlow struct {
	Source      SourceInfo `json:"source"`
	Sink        SinkInfo   `json:"sink"`
	Path        []PathNode `json:"path"`
	RiskLevel   string     `json:"risk_level"` // high | medium | low
	Category    string     `json:"category"`
	IsSanitized bool       `json:"is_sanitized"`
	Sanitizers  []PathNode `json:"sanitizers,omitempty"`
}

// SourceInfo 污点源信息
type SourceInfo struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	NodeID   string `json:"node_id,omitempty"`
}

// SinkInfo Sink信息
type SinkInfo struct {
	Kind     string `json:"kind"`
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	NodeID   string `json:"node_id,omitempty"`
}

// PathNode 路径节点（污点传播链中的节点）
type PathNode struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	VarName  string `json:"var_name,omitempty"` // 传播到该节点的变量名
}

// Engine v2 污点分析引擎入口
// 使用方式:
//
//	engine := dataflow.NewEngineV2(lang, asts, graph)
//	result := engine.Analyze()
type Engine struct {
	lang  string
	asts  []*parser.UnifiedAST
	graph *graph.MemorySymbolGraph
}

// NewEngineV2 创建新的污点分析引擎
func NewEngineV2(lang string, asts []*parser.UnifiedAST, graph *graph.MemorySymbolGraph) *Engine {
	return &Engine{
		lang:  normalizeLang(lang),
		asts:  asts,
		graph: graph,
	}
}

// Analyze 执行污点分析（v2，基于 AST）
func (e *Engine) Analyze() *DataFlowResult {
	analyzer := NewTaintAnalyzer(e.lang, e.asts, e.graph)
	return analyzer.Analyze()
}

// ReviewViewIface 评审视图接口（兼容旧代码，保留用于 symbol_graph 等 consumers）
type ReviewViewIface interface {
	GetGraph() *graph.MemorySymbolGraph
	FindEndpoints() []graph.MemorySymbolNode
}
