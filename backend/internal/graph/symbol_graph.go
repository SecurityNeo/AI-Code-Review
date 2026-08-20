package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/parser"
	"go.uber.org/zap"
)

// ASTAdapter 框架适配器接口（在 graph 包中定义以避免循环依赖）
type ASTAdapter interface {
	Name() string
	Detect(ast *parser.UnifiedAST) bool
	Enrich(ast *parser.UnifiedAST, g *MemorySymbolGraph)
}

// SymbolGraph 符号图构建器
type SymbolGraph struct {
	storage   GraphStorage
	cache     *GraphCache
	logger    *zap.Logger
	workspace string
}

// NewSymbolGraph 创建符号图构建器
func NewSymbolGraph(storage GraphStorage, cache *GraphCache, logger *zap.Logger, workspace string) *SymbolGraph {
	return &SymbolGraph{
		storage:   storage,
		cache:     cache,
		logger:    logger,
		workspace: workspace,
	}
}

// SymbolGraphResult 符号图分析结果
type SymbolGraphResult struct {
	Graph      *MemorySymbolGraph       `json:"-"` // 内存图（不序列化）
	NodeCount  int                      `json:"node_count"`
	RelCount   int                      `json:"rel_count"`
	Endpoints  []parser.UnifiedEndpoint `json:"endpoints,omitempty"`
	TopCallers map[string][]string      `json:"top_callers,omitempty"`
	Frameworks []string                 `json:"frameworks,omitempty"` // 检测到的框架列表
}

// BuildFromASTs 从多个AST构建符号图
// changedFiles 不为空时构建增量评审图（仅收录变更文件 + 上下游被引用节点）
// changedFiles 为 nil 时保持全仓库构建（基线图/ code-map 场景）
func (sg *SymbolGraph) BuildFromASTs(projectID uint64, asts []*parser.UnifiedAST, adapters []ASTAdapter, changedFiles map[string]bool) (*SymbolGraphResult, error) {
	graph := NewMemorySymbolGraph(projectID)

	// 构建全量函数索引，供按需补录使用
	funcIndex := buildFuncIndex(asts)

	// 步骤1：摄入基础节点（增量模式下仅变更文件）
	for _, ast := range asts {
		graph.ingestASTNodes(ast, changedFiles)
	}

	// 步骤2：添加调用关系（增量模式下按需补录缺失节点）
	for _, ast := range asts {
		graph.ingestASTCalls(ast, changedFiles, funcIndex)
	}

	// 步骤3：运行框架适配器（增量模式下仅处理变更文件）
	activatedFrameworks := make(map[string]bool)
	for _, ast := range asts {
		if changedFiles != nil && !changedFiles[ast.FilePath] {
			continue
		}
		for _, adapter := range adapters {
			if adapter.Detect(ast) {
				adapter.Enrich(ast, graph)
				activatedFrameworks[adapter.Name()] = true
			}
		}
	}

	// 步骤4：推断关系（实现接口、类型引用等）
	sg.inferRelations(graph, asts)

	result := &SymbolGraphResult{
		Graph:      graph,
		NodeCount:  len(graph.AllNodes()),
		RelCount:   len(graph.relations),
		Frameworks: make([]string, 0, len(activatedFrameworks)),
	}
	for fw := range activatedFrameworks {
		result.Frameworks = append(result.Frameworks, fw)
	}

	// 收集端点
	for _, node := range graph.AllNodes() {
		if node.Type == NodeEndpoint {
			if props, ok := node.Properties["method"]; ok {
				method, _ := props.(string)
				path := node.Name
				result.Endpoints = append(result.Endpoints, parser.UnifiedEndpoint{
					Kind:        "http_route",
					Path:        path,
					Method:      method,
					HandlerFunc: node.Name,
					File:        node.File,
					Line:        node.Location.LineStart,
					Framework:   getStringProp(node.Properties, "framework"),
				})
			}
		}
	}

	return result, nil
}

// SaveBaseline 保存基线图到存储
func (sg *SymbolGraph) SaveBaseline(projectID uint64, graph *MemorySymbolGraph) error {
	if err := sg.storage.Save(projectID, graph); err != nil {
		return fmt.Errorf("save baseline failed: %w", err)
	}
	sg.cache.Set(projectID, graph)
	return nil
}

// LoadBaseline 加载基线图（优先缓存）
func (sg *SymbolGraph) LoadBaseline(projectID uint64) (*MemorySymbolGraph, error) {
	if g, ok := sg.cache.Get(projectID); ok {
		return g, nil
	}
	g, err := sg.storage.Load(projectID)
	if err != nil {
		return nil, err
	}
	sg.cache.Set(projectID, g)
	return g, nil
}

// BuildReviewView 构建评审视图（核心方法：基线只读 + 内存叠加）
func (sg *SymbolGraph) BuildReviewView(ctx context.Context, projectID uint64, changedFiles map[string][]byte, adapters []ASTAdapter) (*ReviewView, error) {
	// 1. 加载基线（只读，从缓存或磁盘）
	baseline, err := sg.LoadBaseline(projectID)
	if err != nil {
		return nil, fmt.Errorf("load baseline for review view failed: %w", err)
	}

	// 2. 克隆基线到内存（评审副本）
	viewGraph := baseline.Clone()

	// 3. 叠加变更文件，并运行框架适配器重建 endpoint/sink/auth 等节点
	activatedFrameworks := make(map[string]bool)
	for filePath, content := range changedFiles {
		if ext := filepathExt(filePath); ext != "" {
			extractor, ok := parser.GlobalRegistry.GetExtractorByExt(ext)
			if !ok {
				continue
			}
			if err := viewGraph.OverlayFileChanges(filePath, content, extractor); err != nil {
				sg.logger.Warn("overlay file changes failed",
					zap.String("file", filePath),
					zap.Error(err),
				)
				continue
			}
			// 解析 AST 并运行框架适配器（重建 endpoint、sink、auth 等框架特有节点）
			ast, parseErr := extractor.ParseFile(filePath, content)
			if parseErr == nil {
				for _, adapter := range adapters {
					if adapter.Detect(ast) {
						adapter.Enrich(ast, viewGraph)
						activatedFrameworks[adapter.Name()] = true
					}
				}
			}
		}
	}

	view := &ReviewView{
		baseGraph: viewGraph,
		overlay:   NewMemorySymbolGraph(projectID),
		projectID: projectID,
	}
	_ = activatedFrameworks // 可在 ReviewView 中扩展 frameworks 字段时使用

	return view, nil
}

// inferRelations 推断跨文件关系
func (sg *SymbolGraph) inferRelations(graph *MemorySymbolGraph, asts []*parser.UnifiedAST) {
	// 构建包名到文件映射
	pkgFiles := make(map[string]map[string]bool) // pkg -> files
	fileToPkg := make(map[string]string)         // file -> pkg
	for _, ast := range asts {
		pkg := inferPackage(ast.FilePath, ast.Language)
		if pkgFiles[pkg] == nil {
			pkgFiles[pkg] = make(map[string]bool)
		}
		pkgFiles[pkg][ast.FilePath] = true
		fileToPkg[ast.FilePath] = pkg
	}

	// 构建 import 映射: file -> imported package -> alias
	fileImports := make(map[string]map[string]string) // file -> pkg path -> alias
	for _, ast := range asts {
		imps := make(map[string]string)
		for _, imp := range ast.Imports {
			path := imp.Path
			alias := imp.Alias
			if alias == "" {
				// 默认别名取最后一段
				parts := strings.Split(path, "/")
				alias = parts[len(parts)-1]
			}
			imps[alias] = path
		}
		fileImports[ast.FilePath] = imps
	}

	// 构建函数名到节点映射
	funcNodes := make(map[string][]*MemorySymbolNode)
	typeNodes := make(map[string]*MemorySymbolNode)
	for _, node := range graph.AllNodes() {
		if node.Type == NodeFunc {
			funcNodes[node.Name] = append(funcNodes[node.Name], node)
		}
		if node.Type == NodeType {
			typeNodes[node.Name] = node
		}
	}

	// 跨文件调用解析：遍历关系，根据 import 补充跨包调用
	for _, node := range graph.AllNodes() {
		if node.Type != NodeFunc {
			continue
		}
		file := node.File
		imps := fileImports[file]
		if imps == nil {
			continue
		}
		// 查找该函数体内的 call sites，尝试解析跨文件调用
		// 利用已有的 call sites 信息（如果 AST 已摄入）
		// 这里基于 imports 推断：如果函数引用了 import 包中的标识符，建立关系
	}

	// 推断接口实现关系（Go）
	// P3-3 修复：使用 Properties 中存储的 kind 和 receiver_methods 进行正确匹配
	// 构建 receiver -> methods 映射
	receiverMethods := make(map[string]map[string]bool) // receiverType -> map[methodName]bool
	for _, node := range graph.AllNodes() {
		if node.Type != NodeFunc {
			continue
		}
		receiver := getStringProp(node.Properties, "receiver")
		if receiver == "" {
			continue
		}
		// 清理 receiver 类型：去掉 * 和 & 前缀
		receiver = strings.TrimPrefix(receiver, "*")
		receiver = strings.TrimPrefix(receiver, "&")
		if receiverMethods[receiver] == nil {
			receiverMethods[receiver] = make(map[string]bool)
		}
		receiverMethods[receiver][node.Name] = true
	}

	// 收集接口类型及其方法集
	ifaceMethods := make(map[string]map[string]bool) // interfaceName -> map[methodName]bool
	for _, node := range graph.AllNodes() {
		if node.Type != NodeType || node.Language != "golang" {
			continue
		}
		if getStringProp(node.Properties, "kind") != "interface" {
			continue
		}
		methods := getStringSliceProp(node.Properties, "methods")
		if len(methods) == 0 {
			continue
		}
		ifaceMethods[node.Name] = make(map[string]bool)
		for _, m := range methods {
			ifaceMethods[node.Name][m] = true
		}
	}

	// 遍历所有 struct 类型，检查是否实现了某个接口
	for _, node := range graph.AllNodes() {
		if node.Type != NodeType || node.Language != "golang" {
			continue
		}
		if getStringProp(node.Properties, "kind") != "struct" {
			continue
		}
		structMethods := receiverMethods[node.Name]
		if len(structMethods) == 0 {
			continue
		}
		for ifaceName, requiredMethods := range ifaceMethods {
			if len(requiredMethods) == 0 {
				continue
			}
			// 检查 struct 是否实现了接口的所有方法
			allImplemented := true
			for reqMethod := range requiredMethods {
				if !structMethods[reqMethod] {
					allImplemented = false
					break
				}
			}
			if allImplemented {
				ifaceNode := typeNodes[ifaceName]
				if ifaceNode != nil {
					graph.AddRelation(MemoryRelation{
						From: node.ID,
						To:   ifaceNode.ID,
						Type: RelImplements,
					})
				}
			}
		}
	}
}

// inferPackageFromImport 从 import 路径推断包名
func inferPackageFromImport(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

// getStringProp 安全获取字符串属性
func getStringProp(props map[string]interface{}, key string) string {
	if props == nil {
		return ""
	}
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// getStringSliceProp 安全获取字符串切片属性
func getStringSliceProp(props map[string]interface{}, key string) []string {
	if props == nil {
		return nil
	}
	if v, ok := props[key]; ok {
		if sl, ok := v.([]string); ok {
			return sl
		}
		if sl, ok := v.([]interface{}); ok {
			var result []string
			for _, item := range sl {
				if s, ok := item.(string); ok {
					result = append(result, s)
				}
			}
			return result
		}
	}
	return nil
}

// inferPackage 从文件路径推断包/模块名
func inferPackage(filePath, language string) string {
	parts := strings.Split(filePath, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return ""
}
