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
func (sg *SymbolGraph) BuildFromASTs(projectID uint64, asts []*parser.UnifiedAST, adapters []ASTAdapter) (*SymbolGraphResult, error) {
	graph := NewMemorySymbolGraph(projectID)

	// 步骤1：摄入基础AST
	for _, ast := range asts {
		graph.ingestAST(ast)
	}

	// 步骤2：运行框架适配器（识别Endpoint、Sink、认证等）
	activatedFrameworks := make(map[string]bool)
	for _, ast := range asts {
		for _, adapter := range adapters {
			if adapter.Detect(ast) {
				adapter.Enrich(ast, graph)
				activatedFrameworks[adapter.Name()] = true
			}
		}
	}

	// 步骤3：推断关系（实现接口、类型引用等）
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
func (sg *SymbolGraph) BuildReviewView(ctx context.Context, projectID uint64, changedFiles map[string][]byte) (*ReviewView, error) {
	// 1. 加载基线（只读，从缓存或磁盘）
	baseline, err := sg.LoadBaseline(projectID)
	if err != nil {
		return nil, fmt.Errorf("load baseline for review view failed: %w", err)
	}

	// 2. 克隆基线到内存（评审副本）
	viewGraph := baseline.Clone()

	// 3. 叠加变更文件
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
		}
	}

	// 4. 运行框架适配器（新文件可能引入新路由）
	// 在实际实现中，需要对变更的文件解析后再运行适配器

	view := &ReviewView{
		baseGraph: viewGraph,
		overlay:   NewMemorySymbolGraph(projectID),
		projectID: projectID,
	}

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
	// TODO: 当前逻辑基于不准确的假设（funcNodes map 的 key 是方法名而非 "Type.Method"），
	// 实际上 ingestAST 中 generateFuncID 会将方法名存储为 func:file:Receiver.Method 或 func:file:Method，
	// 而 funcNodes 的 key 仅为 Name 字段（即方法名，不含接收者前缀）。
	// 这导致 strings.Contains(fn.Name, node.Name+".") 几乎永远为 false，接口推断关系从未真正建立。
	// 正确做法应在 ingestAST 时就为 receiver 方法统一记录 "Struct.Method" 格式的 name，
	// 或在构建 funcNodes 时额外维护一个 receiver→methods 的映射。
	for _, node := range graph.AllNodes() {
		if node.Type != NodeType || node.Language != "golang" {
			continue
		}
		// 查找同名接口
		ifaceNode, isIface := typeNodes[node.Name]
		if !isIface || ifaceNode == node {
			continue
		}
		// 检查结构体是否实现了接口的所有方法
		if ifaceNode.Language != "golang" {
			continue
		}
		structMethods := make(map[string]bool)
		for _, fnNode := range funcNodes {
			for _, fn := range fnNode {
				// BUG: fn.Name 此处为纯方法名，node.Name 为类型名，
				// strings.Contains("GetUser", "UserService.") → false
				// 正确的匹配需要 fn.Name 为 "UserService.GetUser" 或基于 ID 模式匹配
				if fn.Package == node.Package && strings.Contains(fn.Name, node.Name+".") {
					parts := strings.Split(fn.Name, ".")
					if len(parts) >= 2 {
						structMethods[parts[len(parts)-1]] = true
					}
				}
			}
		}
		// 简化的接口实现推断：如果结构体有方法，认为可能实现接口
		if len(structMethods) > 0 {
			graph.AddRelation(MemoryRelation{
				From: node.ID,
				To:   ifaceNode.ID,
				Type: RelImplements,
			})
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

// inferPackage 从文件路径推断包/模块名
func inferPackage(filePath, language string) string {
	parts := strings.Split(filePath, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return ""
}
