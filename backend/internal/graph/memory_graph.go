package graph

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ai-optimizer/backend/internal/parser"
)

// MemorySymbolNode 内存中的符号节点
type MemorySymbolNode struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"` // function | type | field | endpoint | sink
	Name       string                 `json:"name"`
	Language   string                 `json:"language"`
	File       string                 `json:"file"`
	Package    string                 `json:"package,omitempty"`
	Signature  string                 `json:"signature,omitempty"`
	IsExported bool                   `json:"is_exported"`
	Location   parser.SourceLocation  `json:"location"`
	Properties map[string]interface{} `json:"properties,omitempty"` // 框架适配器扩展字段
}

// Key 返回唯一键
func (n *MemorySymbolNode) Key() string {
	if n.Package != "" {
		return fmt.Sprintf("%s.%s", n.Package, n.Name)
	}
	return n.Name
}

// MemoryRelation 内存中的关系
type MemoryRelation struct {
	From   string            `json:"from"`
	To     string            `json:"to"`
	Type   string            `json:"type"`
	Weight float64           `json:"weight,omitempty"` // 调用频率或置信度
	Extra  map[string]string `json:"extra,omitempty"`
}

// MemorySymbolGraph 内存符号图
type MemorySymbolGraph struct {
	nodes     map[string]*MemorySymbolNode
	relations []MemoryRelation
	mu        sync.RWMutex
	projectID uint64
}

// NewMemorySymbolGraph 创建内存符号图
func NewMemorySymbolGraph(projectID uint64) *MemorySymbolGraph {
	return &MemorySymbolGraph{
		nodes:     make(map[string]*MemorySymbolNode),
		relations: make([]MemoryRelation, 0),
		projectID: projectID,
	}
}

// AddNode 添加节点
func (g *MemorySymbolGraph) AddNode(node *MemorySymbolNode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if node.ID == "" {
		node.ID = g.generateID(node)
	}
	g.nodes[node.ID] = node
}

// GetNode 获取节点
func (g *MemorySymbolGraph) GetNode(id string) (*MemorySymbolNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n, ok := g.nodes[id]
	return n, ok
}

// GetNodeByName 按名称获取节点（模糊匹配）
func (g *MemorySymbolGraph) GetNodeByName(name string) []*MemorySymbolNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var result []*MemorySymbolNode
	for _, n := range g.nodes {
		if n.Name == name || strings.HasSuffix(n.Name, "."+name) {
			result = append(result, n)
		}
	}
	return result
}

// AddRelation 添加关系
func (g *MemorySymbolGraph) AddRelation(rel MemoryRelation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.relations = append(g.relations, rel)
}

// GetRelations 获取从某节点出发的关系
func (g *MemorySymbolGraph) GetRelations(fromID string, relType string) []MemoryRelation {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var result []MemoryRelation
	for _, r := range g.relations {
		if r.From == fromID && (relType == "" || r.Type == relType) {
			result = append(result, r)
		}
	}
	return result
}

// GetIncomingRelations 获取到达某节点的关系
func (g *MemorySymbolGraph) GetIncomingRelations(toID string, relType string) []MemoryRelation {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var result []MemoryRelation
	for _, r := range g.relations {
		if r.To == toID && (relType == "" || r.Type == relType) {
			result = append(result, r)
		}
	}
	return result
}

// RelationCount 返回关系数量
func (g *MemorySymbolGraph) RelationCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.relations)
}

// GetAllRelations 返回所有关系（副本）
func (g *MemorySymbolGraph) GetAllRelations() []MemoryRelation {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]MemoryRelation, len(g.relations))
	copy(result, g.relations)
	return result
}

// AllNodes 返回所有节点（副本）
func (g *MemorySymbolGraph) AllNodes() map[string]*MemorySymbolNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string]*MemorySymbolNode, len(g.nodes))
	for k, v := range g.nodes {
		// 浅拷贝
		cp := *v
		result[k] = &cp
	}
	return result
}

// Clone 深拷贝图（用于评审视图）
func (g *MemorySymbolGraph) Clone() *MemorySymbolGraph {
	g.mu.RLock()
	defer g.mu.RUnlock()
	clone := NewMemorySymbolGraph(g.projectID)
	for k, v := range g.nodes {
		cp := *v
		if v.Properties != nil {
			cp.Properties = make(map[string]interface{}, len(v.Properties))
			for pk, pv := range v.Properties {
				cp.Properties[pk] = pv
			}
		}
		clone.nodes[k] = &cp
	}
	clone.relations = make([]MemoryRelation, len(g.relations))
	copy(clone.relations, g.relations)
	return clone
}

// OverlayFileChanges 在内存图上叠加文件变更（评审视图专用）
func (g *MemorySymbolGraph) OverlayFileChanges(filePath string, fileData []byte, extractor parser.TreeSitterExtractor) error {
	ast, err := extractor.ParseFile(filePath, fileData)
	if err != nil {
		return fmt.Errorf("parse file %s failed: %w", filePath, err)
	}
	// 移除旧节点（同一文件）
	g.mu.Lock()
	for k, v := range g.nodes {
		if v.File == filePath {
			delete(g.nodes, k)
		}
	}
	// 移除旧关系
	var newRels []MemoryRelation
	for _, r := range g.relations {
		fromNode, ok1 := g.nodes[r.From]
		toNode, ok2 := g.nodes[r.To]
		if ok1 && fromNode.File == filePath {
			continue
		}
		if ok2 && toNode.File == filePath {
			continue
		}
		newRels = append(newRels, r)
	}
	g.relations = newRels
	g.mu.Unlock()

	// 添加新节点
	g.ingestAST(ast)
	return nil
}

// ingestAST 将AST摄入图
func (g *MemorySymbolGraph) ingestAST(ast *parser.UnifiedAST) {
	pkg := ""
	// 尝试从文件路径推断包名
	if parts := strings.Split(ast.FilePath, "/"); len(parts) >= 2 {
		pkg = parts[len(parts)-2]
	}

	for _, fn := range ast.Functions {
		node := &MemorySymbolNode{
			ID:         g.generateFuncID(ast.FilePath, pkg, fn.Receiver, fn.Name),
			Type:       NodeFunc,
			Name:       fn.Name,
			Language:   ast.Language,
			File:       ast.FilePath,
			Package:    pkg,
			Signature:  buildSignature(fn),
			IsExported: fn.IsExported,
			Location:   fn.Location,
		}
		if node.ID == "" {
			node.ID = fmt.Sprintf("func:%s:%d", ast.FilePath, fn.Location.LineStart)
		}
		g.AddNode(node)
	}

	for _, tp := range ast.Types {
		node := &MemorySymbolNode{
			ID:         fmt.Sprintf("type:%s:%s", ast.FilePath, tp.Name),
			Type:       NodeType,
			Name:       tp.Name,
			Language:   ast.Language,
			File:       ast.FilePath,
			Package:    pkg,
			IsExported: tp.IsExported,
			Location:   tp.Location,
		}
		g.AddNode(node)

		// 字段关系
		for _, f := range tp.Fields {
			fieldNode := &MemorySymbolNode{
				ID:       fmt.Sprintf("field:%s:%s:%s", ast.FilePath, tp.Name, f.Name),
				Type:     NodeField,
				Name:     f.Name,
				Language: ast.Language,
				File:     ast.FilePath,
				Package:  pkg,
				Location: f.Location,
				Properties: map[string]interface{}{
					"type": f.Type,
					"tag":  f.Tag,
				},
			}
			g.AddNode(fieldNode)
			g.AddRelation(MemoryRelation{
				From: node.ID,
				To:   fieldNode.ID,
				Type: RelContains,
			})
		}
	}

	for _, ep := range ast.Endpoints {
		node := &MemorySymbolNode{
			ID:       fmt.Sprintf("endpoint:%s:%s", ast.FilePath, ep.HandlerFunc),
			Type:     NodeEndpoint,
			Name:     ep.Path,
			Language: ast.Language,
			File:     ast.FilePath,
			Package:  pkg,
			Location: parser.SourceLocation{File: ep.File, LineStart: ep.Line},
			Properties: map[string]interface{}{
				"method":    ep.Method,
				"auth":      ep.AuthRequired,
				"framework": ep.Framework,
			},
		}
		g.AddNode(node)
	}

	// 调用关系
	for _, cs := range ast.CallSites {
		if cs.TargetFunc == "" {
			continue
		}
		g.AddRelation(MemoryRelation{
			From: fmt.Sprintf("func:%s:%s", ast.FilePath, cs.CallerFunc),
			To:   fmt.Sprintf("func:%s", cs.TargetFunc),
			Type: RelCalls,
		})
	}
}

// generateID 生成节点ID
func (g *MemorySymbolGraph) generateID(node *MemorySymbolNode) string {
	if node.Type == NodeFunc {
		return fmt.Sprintf("func:%s:%d", node.File, node.Location.LineStart)
	}
	return fmt.Sprintf("%s:%s:%s", node.Type, node.File, node.Name)
}

// generateFuncID 生成函数ID
func (g *MemorySymbolGraph) generateFuncID(file, pkg, receiver, name string) string {
	if receiver != "" {
		return fmt.Sprintf("func:%s:%s.%s", file, receiver, name)
	}
	if pkg != "" {
		return fmt.Sprintf("func:%s:%s", pkg, name)
	}
	return fmt.Sprintf("func:%s:%s", file, name)
}

// buildSignature 构建函数签名
func buildSignature(fn parser.UnifiedFunction) string {
	var params []string
	for _, p := range fn.Params {
		if p.Name != "" {
			params = append(params, fmt.Sprintf("%s %s", p.Name, p.Type))
		} else {
			params = append(params, p.Type)
		}
	}
	return fmt.Sprintf("%s(%s)", fn.Name, strings.Join(params, ", "))
}
