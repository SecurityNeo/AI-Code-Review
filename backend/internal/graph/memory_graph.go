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
	metadata  map[string]interface{}
	mu        sync.RWMutex
	projectID uint64
}

// NewMemorySymbolGraph 创建内存符号图
func NewMemorySymbolGraph(projectID uint64) *MemorySymbolGraph {
	return &MemorySymbolGraph{
		nodes:     make(map[string]*MemorySymbolNode),
		relations: make([]MemoryRelation, 0),
		metadata:  make(map[string]interface{}),
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
	if g.metadata != nil {
		clone.metadata = make(map[string]interface{}, len(g.metadata))
		for mk, mv := range g.metadata {
			clone.metadata[mk] = mv
		}
	}
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

// ingestAST 将AST摄入图（单文件完整解析，用于 OverlayFileChanges 等场景）
func (g *MemorySymbolGraph) ingestAST(ast *parser.UnifiedAST) {
	g.ingestASTNodes(ast)
	g.ingestASTCalls(ast)
}

// ingestASTNodes 摄入所有节点（第一阶段：先添加所有节点，不添加跨文件关系）
func (g *MemorySymbolGraph) ingestASTNodes(ast *parser.UnifiedAST) {
	pkg := ""
	if parts := strings.Split(ast.FilePath, "/"); len(parts) >= 2 {
		pkg = parts[len(parts)-2]
	}

	for _, fn := range ast.Functions {
		id := g.generateFuncID(ast.FilePath, pkg, fn.Receiver, fn.Name)
		node := &MemorySymbolNode{
			ID:         id,
			Type:       NodeFunc,
			Name:       fn.Name,
			Language:   ast.Language,
			File:       ast.FilePath,
			Package:    pkg,
			Signature:  buildSignature(fn),
			IsExported: fn.IsExported,
			Location:   fn.Location,
			Properties: map[string]interface{}{
				"receiver":      fn.Receiver,
				"security_role": fn.SecurityRole,
				"complexity":    fn.Complexity,
			},
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
			Properties: map[string]interface{}{
				"kind":       tp.Kind,
				"methods":    getMethodNames(tp.Methods),
				"fields":     getFieldNames(tp.Fields),
				"implements": tp.Implements,
			},
		}
		g.AddNode(node)

		// 字段节点和 contains 关系（同文件内，可立即添加）
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
				"method":                ep.Method,
				"auth":                  ep.AuthRequired,
				"authz_policy":          ep.AuthzPolicy,
				"rate_limit":            ep.RateLimit,
				"produces_content_type": ep.ProducesContentType,
				"consumes_content_type": ep.ConsumesContentType,
				"framework":             ep.Framework,
			},
		}
		g.AddNode(node)
	}

	for _, td := range ast.TODOComments {
		todoNode := &MemorySymbolNode{
			ID:   fmt.Sprintf("todo:%s:%d", ast.FilePath, td.Line),
			Type: NodeTODO,
			Name: td.Text,
			File: ast.FilePath,
			Location: parser.SourceLocation{
				File:      ast.FilePath,
				LineStart: td.Line,
				LineEnd:   td.Line,
			},
			Properties: map[string]interface{}{
				"comment_type": td.Type,
				"function":     td.Function,
			},
		}
		g.AddNode(todoNode)
	}
}

// ingestASTCalls 摄入调用关系（第二阶段：所有节点添加完毕后，再解析跨文件引用）
func (g *MemorySymbolGraph) ingestASTCalls(ast *parser.UnifiedAST) {
	pkg := ""
	if parts := strings.Split(ast.FilePath, "/"); len(parts) >= 2 {
		pkg = parts[len(parts)-2]
	}

	// 为本文件函数建立 name -> ID 映射（用于确定 caller）
	funcNameToID := make(map[string]string)
	for _, fn := range ast.Functions {
		id := g.generateFuncID(ast.FilePath, pkg, fn.Receiver, fn.Name)
		funcNameToID[fn.Name] = id
	}

	for _, cs := range ast.CallSites {
		if cs.TargetFunc == "" {
			continue
		}
		fromID := funcNameToID[cs.CallerFunc]
		if fromID == "" {
			continue
		}

		// 在所有已加载节点中搜索目标函数（跨文件引用此时已存在）
		var toID string
		targetPkg := cs.TargetPkg
		if targetPkg == "" {
			targetPkg = pkg
		}

		// 1. 尝试精确匹配 func:pkg:name（适配器生成的节点格式）
		tryID := fmt.Sprintf("func:%s:%s", targetPkg, cs.TargetFunc)
		if _, ok := g.GetNode(tryID); ok {
			toID = tryID
		} else {
			// 2. 按函数名全局查找
			matches := g.GetNodeByName(cs.TargetFunc)
			if len(matches) > 0 {
				// 优先匹配同包的
				for _, n := range matches {
					if n.Package == targetPkg {
						toID = n.ID
						break
					}
				}
				if toID == "" {
					toID = matches[0].ID
				}
			}
		}

		if toID == "" {
			// 3. fallback：使用 fallback 格式，后续可能通过 inferRelations 补充
			toID = fmt.Sprintf("func:%s:%s", targetPkg, cs.TargetFunc)
		}

		g.AddRelation(MemoryRelation{
			From: fromID,
			To:   toID,
			Type: RelCalls,
			Extra: map[string]string{
				"caller_file": ast.FilePath,
			},
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

// getMethodNames 提取方法名列表
func getMethodNames(methods []parser.UnifiedMethodSignature) []string {
	var names []string
	for _, m := range methods {
		names = append(names, m.Name)
	}
	return names
}

// getFieldNames 提取字段名列表
func getFieldNames(fields []parser.UnifiedField) []string {
	var names []string
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return names
}

// SetMetadata 设置图谱元数据
func (g *MemorySymbolGraph) SetMetadata(key string, value interface{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.metadata == nil {
		g.metadata = make(map[string]interface{})
	}
	g.metadata[key] = value
}

// GetMetadata 获取图谱元数据
func (g *MemorySymbolGraph) GetMetadata(key string) interface{} {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.metadata == nil {
		return nil
	}
	return g.metadata[key]
}

// AllMetadata 获取所有元数据
func (g *MemorySymbolGraph) AllMetadata() map[string]interface{} {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.metadata == nil {
		return nil
	}
	result := make(map[string]interface{}, len(g.metadata))
	for k, v := range g.metadata {
		result[k] = v
	}
	return result
}
