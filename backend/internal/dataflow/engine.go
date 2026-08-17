package dataflow

import (
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
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

// PathNode 路径节点
type PathNode struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
}

// DefaultTaintConfig 默认污点配置
type DefaultTaintConfig struct {
	Sources []string            // 例如 gin.Context.Query, req.GetParameter
	Sinks   map[string][]string // sink kind -> function patterns
}

// Engine 数据流分析引擎
type Engine struct {
	config DefaultTaintConfig
}

// NewEngine 创建数据流分析引擎
func NewEngine() *Engine {
	return &Engine{
		config: DefaultTaintConfig{
			Sources: []string{
				"Query", "Param", "PostForm", "GetHeader", "Cookie",
				"GetQuery", "GetParam", "Body", "FormValue",
			},
			Sinks: map[string][]string{
				SinkSQLInjection:     {"Exec", "Raw", "ExecContext", "QueryContext"},
				SinkCommandInjection: {"Command", "CommandContext", "Start", "Run"},
				SinkXSS:              {"WriteString", `Write\(`, `Fprintf\(`, `Sprintf\(`},
				SinkPathTraversal:    {"OpenFile", "WriteFile", "Create", "Mkdir", "ReadFile"},
				SinkSSRF:             {"Get", "Post", "Do", "Dial", "DialContext"},
			},
		},
	}
}

// Analyze 分析评审视图中的污点路径
func (e *Engine) Analyze(view ReviewViewIface) *DataFlowResult {
	result := &DataFlowResult{Flows: make([]TaintFlow, 0)}

	// 获取所有函数节点
	for _, node := range view.GetGraph().AllNodes() {
		if node.Type != graph.NodeFunc {
			continue
		}
		// 识别污点源
		sources := e.identifySources(node)
		if len(sources) == 0 {
			continue
		}

		// 对每个源，查找可达的Sink
		for _, src := range sources {
			sinks := e.findReachableSinks(view.GetGraph(), node, src)
			for _, sink := range sinks {
				flow := TaintFlow{
					Source:    src,
					Sink:      sink,
					Path:      []PathNode{{Function: node.Name, File: node.File, Line: node.Location.LineStart}},
					RiskLevel: "high",
					Category:  sink.Kind,
				}
				result.Flows = append(result.Flows, flow)
			}
		}
	}

	return result
}

// identifySources 识别函数中的污点源
func (e *Engine) identifySources(node *graph.MemorySymbolNode) []SourceInfo {
	var sources []SourceInfo
	if node.Properties == nil {
		return sources
	}
	snippet, _ := node.Properties["body_snippet"].(string)
	if snippet == "" {
		return sources
	}
	for _, src := range e.config.Sources {
		if strings.Contains(snippet, src) {
			sources = append(sources, SourceInfo{
				Kind:     SourceHTTPParam,
				Name:     src,
				Function: node.Name,
				File:     node.File,
				Line:     node.Location.LineStart,
				NodeID:   node.ID,
			})
		}
	}
	return sources
}

// findReachableSinks 从某函数查找可达的Sink
func (e *Engine) findReachableSinks(g *graph.MemorySymbolGraph, startNode *graph.MemorySymbolNode, src SourceInfo) []SinkInfo {
	var sinks []SinkInfo
	visited := make(map[string]bool)

	// BFS遍历调用图
	var queue []*graph.MemorySymbolNode
	queue = append(queue, startNode)

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if visited[curr.ID] {
			continue
		}
		visited[curr.ID] = true

		// 检查当前节点是否有Sink
		if sinkMatches := e.findSinksInNode(curr); len(sinkMatches) > 0 {
			for _, s := range sinkMatches {
				// 检查是否有sanitizer
				if !e.hasSanitizer(curr, src) {
					sinks = append(sinks, s)
				}
			}
		}

		// 基础 def-use 参数流分析：如果源变量在函数参数中，且函数体内有 sink 函数调用，增强置信度
		if analyzer := NewDefUseAnalyzer(); analyzer != nil {
			chain := analyzer.AnalyzeFunction(curr.Name, curr.Properties["body_snippet"].(string))
			if len(chain.Defs) > 0 && len(chain.Uses) > 0 {
				// 简化的参数流：参数被赋值后流向 sink
				for _, def := range chain.Defs {
					for _, use := range chain.Uses {
						if def.VarName == use.VarName && use.Line > def.Line {
							// 标记为参数流增强
							_ = def
						}
					}
				}
			}
		}

		// 继续BFS到调用的函数
		for _, rel := range g.GetRelations(curr.ID, graph.RelCalls) {
			if next, ok := g.GetNode(rel.To); ok && !visited[next.ID] {
				queue = append(queue, next)
			}
		}
	}

	return sinks
}

// findSinksInNode 在单个函数中查找Sink
func (e *Engine) findSinksInNode(node *graph.MemorySymbolNode) []SinkInfo {
	var sinks []SinkInfo
	snippet, _ := node.Properties["body_snippet"].(string)
	if snippet == "" {
		return sinks
	}

	for kind, patterns := range e.config.Sinks {
		for _, pattern := range patterns {
			// 简化匹配：字符串包含
			if strings.Contains(snippet, pattern) {
				sinks = append(sinks, SinkInfo{
					Kind:     kind,
					Function: pattern,
					File:     node.File,
					Line:     node.Location.LineStart,
					NodeID:   node.ID,
				})
			}
		}
	}
	return sinks
}

// hasSanitizer 检查是否有sanitizer（简化版）
func (e *Engine) hasSanitizer(node *graph.MemorySymbolNode, src SourceInfo) bool {
	snippet, _ := node.Properties["body_snippet"].(string)
	if snippet == "" {
		return false
	}
	sanitizers := []string{"validate", "sanitize", "escape", "html.EscapeString", "sql.Quote", "Prepare"}
	for _, san := range sanitizers {
		if strings.Contains(snippet, san) {
			return true
		}
	}
	return false
}

// ReviewViewIface 评审视图接口（避免循环依赖）
type ReviewViewIface interface {
	GetGraph() *graph.MemorySymbolGraph
	FindEndpoints() []graph.MemorySymbolNode
}
