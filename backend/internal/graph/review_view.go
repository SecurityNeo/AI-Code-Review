package graph

import (
	"fmt"

	"github.com/ai-optimizer/backend/internal/parser"
)

// ReviewView 评审视图：基线只读 + 内存叠加
type ReviewView struct {
	baseGraph *MemorySymbolGraph
	overlay   *MemorySymbolGraph
	projectID uint64
}

// BuildReviewView 从基线+分支变更构建评审视图
func BuildReviewView(storage GraphStorage, projectID uint64, changedFiles map[string][]byte) (*ReviewView, error) {
	base, err := storage.Load(projectID)
	if err != nil {
		return nil, fmt.Errorf("load baseline graph failed: %w", err)
	}

	// 克隆基线到内存
	view := &ReviewView{
		baseGraph: base.Clone(),
		overlay:   NewMemorySymbolGraph(projectID),
		projectID: projectID,
	}

	// 叠加变更文件
	for filePath, content := range changedFiles {
		if ext := filepathExt(filePath); ext != "" {
			extractor, ok := parser.GlobalRegistry.GetExtractorByExt(ext)
			if !ok {
				continue
			}
			if err := view.baseGraph.OverlayFileChanges(filePath, content, extractor); err != nil {
				// 不影响整体流程，记录即可
				continue
			}
		}
	}

	return view, nil
}

// FindCallers 查找某函数的调用者（跨文件）
func (v *ReviewView) FindCallers(funcName string) []MemorySymbolNode {
	var callers []MemorySymbolNode
	for _, node := range v.baseGraph.AllNodes() {
		if node.Type != NodeFunc {
			continue
		}
		for _, rel := range v.baseGraph.GetRelations(node.ID, RelCalls) {
			if rel.To == funcName || rel.To == fmt.Sprintf("func:%s", funcName) {
				callers = append(callers, *node)
			}
		}
	}
	return callers
}

// FindSinks 查找从某函数可达的危险Sink
func (v *ReviewView) FindSinks(funcName string) []MemorySymbolNode {
	var sinks []MemorySymbolNode
	visited := make(map[string]bool)
	// 简单的BFS
	var queue []string
	for _, n := range v.baseGraph.GetNodeByName(funcName) {
		queue = append(queue, n.ID)
	}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if visited[curr] {
			continue
		}
		visited[curr] = true

		node, ok := v.baseGraph.GetNode(curr)
		if !ok {
			continue
		}
		if node.Type == NodeSink {
			sinks = append(sinks, *node)
		}
		for _, rel := range v.baseGraph.GetRelations(curr, RelCalls) {
			if !visited[rel.To] {
				queue = append(queue, rel.To)
			}
		}
	}
	return sinks
}

// FindEndpoints 查找项目的所有HTTP端点
func (v *ReviewView) FindEndpoints() []MemorySymbolNode {
	var endpoints []MemorySymbolNode
	for _, node := range v.baseGraph.AllNodes() {
		if node.Type == NodeEndpoint {
			endpoints = append(endpoints, *node)
		}
	}
	return endpoints
}

// FindAuthChecks 查找项目的所有认证检查
func (v *ReviewView) FindAuthChecks() []MemorySymbolNode {
	var checks []MemorySymbolNode
	for _, node := range v.baseGraph.AllNodes() {
		if node.Type == NodeFunc {
			if props, ok := node.Properties["is_auth_check"]; ok && props == true {
				checks = append(checks, *node)
			}
		}
	}
	return checks
}

// GetGraph 获取底层图（只读）
func (v *ReviewView) GetGraph() *MemorySymbolGraph {
	return v.baseGraph
}

func filepathExt(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/'; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}
