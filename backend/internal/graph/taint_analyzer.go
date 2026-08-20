package graph

import "strings"

// TaintPath 污点传播路径（简化版 - 只记录调用链）
type TaintPath struct {
	SourceNodeID   string   `json:"source_node_id"`
	SourceName     string   `json:"source_name"`
	SourceFile     string   `json:"source_file"`
	SinkNodeID     string   `json:"sink_node_id"`
	SinkName       string   `json:"sink_name"`
	SinkFile       string   `json:"sink_file"`
	PathDepth      int      `json:"path_depth"`       // 调用链深度
	PathNodeIDs    []string `json:"path_node_ids"`    // 中间节点 ID 列表
	PathNodeNames  []string `json:"path_node_names"`  // 中间节点名称（可读）
	HasSanitizer   bool     `json:"has_sanitizer"`    // 路径中是否有 sanitizer 函数
	RiskLevel      string   `json:"risk_level"`       // critical | high | medium | low
	Category       string   `json:"category"`         // sql-injection | command-injection | path-traversal | xss | others
}

// SinkInfo 危险函数信息
type SinkInfo struct {
	NodeID   string `json:"node_id"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Category string `json:"category"` // exec | sql | file | reflect | unsafe
}

// 内置 Source 函数/模式库（简化版 - 按语言 / 框架）
var defaultSourcePatterns = map[string][]string{
	"golang": {
		"Query", "QueryParam", "Param", "PostForm", "GetString",
		"Context.Query", "Context.PostForm", "Context.Param",
		"Request.FormValue", "Request.URL.Query",
		"os.Args", "os.Getenv",
		"flag.String", "flag.Int",
	},
	"javascript": {
		"req.query", "req.body", "req.params", "req.headers",
		"process.argv", "process.env",
	},
}

// 内置 Sink 函数库
var defaultSinkFunctions = map[string]map[string]string{
	"golang": {
		"os/exec.Command":        "exec",
		"os/exec.CommandContext": "exec",
		"syscall.Exec":           "exec",
		"database/sql.Query":     "sql",
		"database/sql.Exec":      "sql",
		"db.Query":               "sql",
		"db.Exec":                "sql",
		"os.WriteFile":           "file",
		"os.OpenFile":            "file",
		"ioutil.WriteFile":       "file",
		"fmt.Fprintf":            "xss",
		"reflect.ValueOf":        "reflect",
		"unsafe.Pointer":         "unsafe",
	},
	"javascript": {
		"exec":        "exec",
		"eval":        "exec",
		"query":       "sql",
		"writeFile":   "file",
		"innerHTML":   "xss",
	},
}

// 内置 Sanitizer 函数库
var defaultSanitizerPatterns = []string{
	"Sanitize", "Validate", "Escape", "Clean", "Check",
	"QuoteMeta", "HTMLEscape", "JSEscape", "URLEscape",
}

// DetectTaintPaths 基于 Call Graph BFS 的简化污点分析
func DetectTaintPaths(g *MemorySymbolGraph) ([]TaintPath, []SinkInfo) {
	// 1. 收集所有 Sink 节点
	sinkNodes := findSinkNodes(g)
	
	// 2. 收集所有 Source 节点
	sourceNodes := findSourceNodes(g)
	
	// 3. 收集所有 Sanitizer 节点
	sanitizerSet := findSanitizerNodes(g)
	
	// 4. 对每对 Source→Sink，做 BFS 可达性分析
	var taintPaths []TaintPath
	for _, source := range sourceNodes {
		for _, sink := range sinkNodes {
			if source.ID == sink.ID {
				continue
			}
			path := bfsPath(g, source.ID, sink.ID, sanitizerSet)
			if path != nil {
				taintPaths = append(taintPaths, *path)
			}
		}
	}
	
	// 5. 构建 SinkInfo 列表
	var sinkInfos []SinkInfo
	for _, sink := range sinkNodes {
		category := getStringProp(sink.Properties, "sink_category")
		if category == "" {
			category = "others"
		}
		sinkInfos = append(sinkInfos, SinkInfo{
			NodeID:   sink.ID,
			Name:     sink.Name,
			File:     sink.File,
			Line:     sink.Location.LineStart,
			Category: category,
		})
	}
	
	return taintPaths, sinkInfos
}

// --- 内部函数 ---

func findSinkNodes(g *MemorySymbolGraph) []*MemorySymbolNode {
	var sinks []*MemorySymbolNode
	for _, node := range g.AllNodes() {
		if node.Type != NodeFunc {
			continue
		}
		
		// 从 Properties 中检查是否已标记为 sink
		if getStringProp(node.Properties, "sink_category") != "" {
			sinks = append(sinks, node)
			continue
		}
		
		// 基于函数名匹配内置 Sink 库
		category := matchSinkFunction(node.Name, node.Package)
		if category != "" {
			if node.Properties == nil {
				node.Properties = make(map[string]interface{})
			}
			node.Properties["sink_category"] = category
			sinks = append(sinks, node)
		}
	}
	return sinks
}

func findSourceNodes(g *MemorySymbolGraph) []*MemorySymbolNode {
	var sources []*MemorySymbolNode
	for _, node := range g.AllNodes() {
		if node.Type != NodeFunc && node.Type != NodeEndpoint {
			continue
		}
		
		// 端点本身就是 Source
		if node.Type == NodeEndpoint {
			if node.Properties == nil {
				node.Properties = make(map[string]interface{})
			}
			node.Properties["source_category"] = "http_endpoint"
			sources = append(sources, node)
			continue
		}
		
		// 基于函数名匹配 Source 模式
		if matchSourceFunction(node.Name) {
			if node.Properties == nil {
				node.Properties = make(map[string]interface{})
			}
			node.Properties["source_category"] = "input_handler"
			sources = append(sources, node)
		}
	}
	return sources
}

func findSanitizerNodes(g *MemorySymbolGraph) map[string]bool {
	set := make(map[string]bool)
	for _, node := range g.AllNodes() {
		if node.Type != NodeFunc {
			continue
		}
		for _, pattern := range defaultSanitizerPatterns {
			if containsSubstring(node.Name, pattern) {
				set[node.ID] = true
				if node.Properties == nil {
					node.Properties = make(map[string]interface{})
				}
				node.Properties["is_sanitizer"] = true
				break
			}
		}
	}
	return set
}

func bfsPath(g *MemorySymbolGraph, sourceID, sinkID string, sanitizerSet map[string]bool) *TaintPath {
	// BFS 查找从 source 到 sink 的最短调用路径
	visited := make(map[string]string) // nodeID -> prevNodeID
	queue := []string{sourceID}
	visited[sourceID] = ""
	
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		
		if curr == sinkID {
			// 找到路径，回溯构建
			pathIDs := []string{}
			pathNames := []string{}
			node := sinkID
			hasSanitizer := false
			for node != "" {
				pathIDs = append([]string{node}, pathIDs...)
				if n, ok := g.GetNode(node); ok {
					pathNames = append([]string{n.Name}, pathNames...)
					if sanitizerSet[node] {
						hasSanitizer = true
					}
				}
				node = visited[node]
			}
			
			sourceNode, _ := g.GetNode(sourceID)
			sinkNode, _ := g.GetNode(sinkID)
			
			riskLevel := calculateRiskLevel(len(pathIDs), hasSanitizer, getStringProp(sinkNode.Properties, "sink_category"))
			
			return &TaintPath{
				SourceNodeID:  sourceID,
				SourceName:    sourceNode.Name,
				SourceFile:    sourceNode.File,
				SinkNodeID:    sinkID,
				SinkName:      sinkNode.Name,
				SinkFile:      sinkNode.File,
				PathDepth:     len(pathIDs) - 1,
				PathNodeIDs:   pathIDs,
				PathNodeNames: pathNames,
				HasSanitizer:  hasSanitizer,
				RiskLevel:     riskLevel,
				Category:      getStringProp(sinkNode.Properties, "sink_category"),
			}
		}
		
		// 遍历当前节点的所有出边（调用关系）
		for _, rel := range g.GetRelations(curr, RelCalls) {
			if _, ok := visited[rel.To]; !ok {
				visited[rel.To] = curr
				queue = append(queue, rel.To)
			}
		}
	}
	
	return nil
}

func matchSinkFunction(funcName, pkg string) string {
	// 首先尝试精确匹配：funcName
	for fullName, category := range defaultSinkFunctions["golang"] {
		parts := splitLast(fullName, ".")
		if len(parts) >= 2 {
			// 如果是带包名的全限定路径（如 os/exec.Command），需要匹配函数名
			if funcName == parts[1] {
				// 如果有包名信息，进一步匹配包的一部分
				if pkg != "" {
					pkgParts := splitLast(fullName, "/")
					if len(pkgParts) >= 2 {
						lastPkgPart := splitLast(pkgParts[0], ".")
						if len(lastPkgPart) >= 1 && strings.Contains(pkg, lastPkgPart[len(lastPkgPart)-1]) {
							return category
						}
					}
				} else {
					// 没有包名信息时，任何匹配函数名的都认为是 sink（放宽匹配）
					return category
				}
			}
		}
	}
	return ""
}

func matchSourceFunction(funcName string) bool {
	for _, pattern := range defaultSourcePatterns["golang"] {
		if containsSubstring(funcName, pattern) {
			return true
		}
	}
	return false
}

func calculateRiskLevel(depth int, hasSanitizer bool, sinkCategory string) string {
	// 基础风险
	baseRisk := 0
	switch sinkCategory {
	case "exec":
		baseRisk = 4
	case "sql":
		baseRisk = 3
	case "file":
		baseRisk = 2
	case "xss":
		baseRisk = 2
	default:
		baseRisk = 1
	}
	
	// 深度越深风险越低（净化机会更多）
	if depth <= 2 {
		baseRisk += 2
	} else if depth <= 4 {
		baseRisk += 1
	}
	
	// 有 sanitizer 降级
	if hasSanitizer {
		baseRisk -= 2
	}
	
	// 映射到等级
	switch {
	case baseRisk >= 5:
		return "critical"
	case baseRisk >= 3:
		return "high"
	case baseRisk >= 2:
		return "medium"
	default:
		return "low"
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSub(s, substr))
}

func containsSub(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func splitLast(s, sep string) []string {
	idx := -1
	for i := len(s) - 1; i >= 0; i-- {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			idx = i
			break
		}
	}
	if idx == -1 {
		return []string{s}
	}
	return []string{s[:idx], s[idx+len(sep):]}
}
