package graphscan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/framework"
	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/parser"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ScanService 全量扫描服务
type ScanService struct {
	db            *gorm.DB
	workspace     string
	storage       graph.GraphStorage
	cache         *graph.GraphCache
	logger        *zap.Logger
	isRunning     map[uint64]bool
	mu            sync.Mutex
	repoMgr       *pipeline.RepoManager
	overviewCache map[uint64]overviewCacheEntry
}

type overviewCacheEntry struct {
	data      map[string]interface{}
	expiresAt time.Time
}

// NewScanService 创建扫描服务
func NewScanService(db *gorm.DB, workspace string) *ScanService {
	ttl := 30 * time.Minute
	if v := os.Getenv("CODEGUARD_GRAPH_CACHE_TTL_MINUTES"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil && minutes > 0 {
			ttl = time.Duration(minutes) * time.Minute
		}
	}
	return &ScanService{
		db:            db,
		workspace:     workspace,
		storage:       graph.NewSQLiteGraphStorage(workspace),
		cache:         graph.NewGraphCache(ttl),
		logger:        zap.L(),
		isRunning:     make(map[uint64]bool),
		repoMgr:       pipeline.GetRepoManager(),
		overviewCache: make(map[uint64]overviewCacheEntry),
	}
}

// RefreshScan 重新全量扫描（同build）
func (s *ScanService) RefreshScan(projectID uint64, branch string, opts ...ScanOption) (*model.GraphScanTask, error) {
	return s.TriggerScan(projectID, branch, opts...)
}

// ScanOption 扫描配置选项
type ScanOption func(*model.GraphScanTask)

// WithTriggerType 设置触发类型
func WithTriggerType(t string) ScanOption {
	return func(task *model.GraphScanTask) {
		task.TriggerType = t
	}
}

// WithMRInfo 设置MR信息
func WithMRInfo(iid int, title string) ScanOption {
	return func(task *model.GraphScanTask) {
		task.MRIID = iid
		task.MRTitle = title
	}
}

// TriggerScan 触发项目全量扫描（后台异步）
func (s *ScanService) TriggerScan(projectID uint64, branch string, opts ...ScanOption) (*model.GraphScanTask, error) {
	var runningCount int64
	s.db.Model(&model.GraphScanTask{}).Where("project_id = ? AND status = ?", projectID, "running").Count(&runningCount)
	if runningCount > 0 {
		return nil, fmt.Errorf("project %d already has a running scan task", projectID)
	}

	task := &model.GraphScanTask{
		ProjectID: projectID,
		Status:    "pending",
		Branch:    branch,
		ScanType:  "full",
	}
	for _, opt := range opts {
		opt(task)
	}
	if err := s.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create scan task failed: %w", err)
	}

	go s.runScan(task.ID, projectID, branch)

	return task, nil
}

// runScan 执行扫描（后台goroutine）
func (s *ScanService) runScan(taskID uint64, projectID uint64, branch string) {
	s.mu.Lock()
	s.isRunning[projectID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.isRunning, projectID)
		s.mu.Unlock()
	}()

	now := time.Now()
	s.db.Model(&model.GraphScanTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":     "running",
		"started_at": now,
	})
	s.db.Model(&model.Project{}).Where("id = ?", projectID).Update("graph_scan_status", "running")

	var project model.Project
	if err := s.db.First(&project, projectID).Error; err != nil {
		s.logger.Error("scan failed: project not found", zap.Uint64("project_id", projectID), zap.Error(err))
		s.failTask(taskID, projectID, "project not found")
		return
	}

	// 确保仓库存在且为最新代码（clone 或 fetch + checkout）
	repoDir, err := s.repoMgr.EnsureRepo(project.ProjectPath, project.AccessToken, branch, uint(projectID))
	if err != nil {
		s.logger.Error("scan failed: repo ensure failed", zap.Uint64("project_id", projectID), zap.String("branch", branch), zap.Error(err))
		s.failTask(taskID, projectID, "repo clone/update failed: "+err.Error())
		return
	}

	var allASTs []*parser.UnifiedAST
	fileCount := 0
	processedCount := 0
	startTime := time.Now()

	err = filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, "node_modules/") ||
			strings.Contains(path, ".git/") {
			return nil
		}

		ext := filepath.Ext(path)
		extractor, ok := parser.GlobalRegistry.GetExtractorByExt(ext)
		if !ok {
			return nil
		}

		fileCount++
		relativePath, _ := filepath.Rel(repoDir, path)

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		ast, err := extractor.ParseFile(relativePath, content)
		if err != nil {
			s.logger.Warn("parse file failed during scan", zap.String("path", relativePath), zap.Error(err))
			return nil
		}

		allASTs = append(allASTs, ast)
		processedCount++

		if processedCount%100 == 0 {
			s.db.Model(&model.GraphScanTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
				"file_count": processedCount,
			})
		}

		return nil
	})

	if err != nil {
		s.logger.Error("walk repo failed", zap.Error(err))
	}

	durationMs := int(time.Since(startTime).Milliseconds())

	sg := graph.NewSymbolGraph(s.storage, s.cache, s.logger, s.workspace)
	var activeAdapters []graph.ASTAdapter
	for _, ast := range allASTs {
		for _, adapter := range framework.GlobalAdapterRegistry.GetAdaptersForAST(ast) {
			found := false
			for _, existing := range activeAdapters {
				if existing.Name() == adapter.Name() {
					found = true
					break
				}
			}
			if !found {
				activeAdapters = append(activeAdapters, adapter)
			}
		}
	}

	graphResult, err := sg.BuildFromASTs(projectID, allASTs, activeAdapters)
	if err != nil {
		s.logger.Error("build graph failed", zap.Error(err))
		s.failTask(taskID, projectID, err.Error())
		return
	}

	if err := sg.SaveBaseline(projectID, graphResult.Graph); err != nil {
		s.logger.Error("save baseline failed", zap.Error(err))
		s.failTask(taskID, projectID, err.Error())
		return
	}

	// P0-A: 重复函数检测 + API 健康度评估（常驻化存储）
	duplicatedGroups := graph.DetectDuplicatedFunctions(graphResult.Graph)
	apiHealthItems := graph.EvaluateAPIHealth(graphResult.Graph)
	if len(duplicatedGroups) > 0 {
		graphResult.Graph.SetMetadata("duplicated_functions", duplicatedGroups)
	}
	if len(apiHealthItems) > 0 {
		graphResult.Graph.SetMetadata("api_health_items", apiHealthItems)
		// 计算平均健康度
		totalScore := 0
		for _, item := range apiHealthItems {
			totalScore += item.Score
		}
		avgScore := 0
		if len(apiHealthItems) > 0 {
			avgScore = totalScore / len(apiHealthItems)
		}
		graphResult.Graph.SetMetadata("api_health_avg_score", avgScore)
	}
	// 重新保存，包含分析结果 metadata
	if err := sg.SaveBaseline(projectID, graphResult.Graph); err != nil {
		s.logger.Error("save baseline with metadata failed", zap.Error(err))
		// 非致命错误，不重试
	}

	// P0-B: 简化污点分析（Source→Sink BFS 可达性）
	taintPaths, sinkInfos := graph.DetectTaintPaths(graphResult.Graph)
	if len(taintPaths) > 0 {
		graphResult.Graph.SetMetadata("taint_paths", taintPaths)
	}
	if len(sinkInfos) > 0 {
		graphResult.Graph.SetMetadata("sink_infos", sinkInfos)
	}
	graphResult.Graph.SetMetadata("taint_path_count", len(taintPaths))
	// 重新保存
	if err := sg.SaveBaseline(projectID, graphResult.Graph); err != nil {
		s.logger.Error("save baseline with taint analysis failed", zap.Error(err))
	}

	// 清除 overview 缓存（新数据已写入）
	s.mu.Lock()
	delete(s.overviewCache, projectID)
	s.mu.Unlock()

	// 更新项目元数据
	completedAt := time.Now()
	frameworksJSON := ""
	if len(graphResult.Frameworks) > 0 {
		b, _ := json.Marshal(graphResult.Frameworks)
		frameworksJSON = string(b)
	}
	s.db.Model(&model.Project{}).Where("id = ?", projectID).Updates(map[string]interface{}{
		"graph_scan_status":    "completed",
		"graph_built_at":       completedAt,
		"graph_node_count":     graphResult.NodeCount,
		"graph_rel_count":      graphResult.RelCount,
		"graph_endpoint_count": len(graphResult.Endpoints),
		"graph_frameworks":     frameworksJSON,
		"graph_last_build_at":  completedAt,
		"graph_scan_error":     nil,
	})

	// 完成任务
	s.db.Model(&model.GraphScanTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":         "completed",
		"node_count":     graphResult.NodeCount,
		"relation_count": graphResult.RelCount,
		"file_count":     fileCount,
		"duration_ms":    durationMs,
		"completed_at":   completedAt,
	})
}

func (s *ScanService) failTask(taskID uint64, projectID uint64, message string) {
	now := time.Now()
	s.db.Model(&model.GraphScanTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":        "failed",
		"error_message": message,
		"completed_at":  now,
	})
	s.db.Model(&model.Project{}).Where("id = ?", projectID).Updates(map[string]interface{}{
		"graph_scan_status": "failed",
		"graph_scan_error":  message,
	})
}

// GetScanStatus 获取扫描状态
func (s *ScanService) GetScanStatus(projectID uint64) (*model.GraphScanTask, error) {
	var task model.GraphScanTask
	if err := s.db.Where("project_id = ?", projectID).Order("created_at DESC").First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// GetGraphOverview 获取图谱概览（带 5 秒 TTL 缓存，避免 SQLite 并发读取不稳定）
func (s *ScanService) GetGraphOverview(projectID uint64) (map[string]interface{}, error) {
	if !s.storage.Exists(projectID) {
		return map[string]interface{}{
			"status":     "none",
			"node_count": 0,
			"rel_count":  0,
			"endpoints":  []interface{}{},
		}, nil
	}

	// 检查缓存
	s.mu.Lock()
	if entry, ok := s.overviewCache[projectID]; ok && time.Now().Before(entry.expiresAt) {
		s.mu.Unlock()
		return entry.data, nil
	}
	s.mu.Unlock()

	g, err := s.storage.Load(projectID)
	if err != nil {
		return nil, err
	}

	var endpoints []map[string]interface{}
	var functions []map[string]interface{}
	var types []map[string]interface{}
	var todos []map[string]interface{}
	frameworkSet := make(map[string]bool)
	
	// 认证覆盖率统计
	var authTotal, authProtected, authRequired int
	var unprotectedHighRisk []map[string]interface{}
	
	for _, node := range g.AllNodes() {
		switch node.Type {
		case graph.NodeEndpoint:
			method := getStringProp(node.Properties, "method")
			isAuthRequired := getBoolProp(node.Properties, "auth")
			endpoint := map[string]interface{}{
				"id":                    node.ID,
				"path":                  node.Name,
				"method":                method,
				"framework":             getStringProp(node.Properties, "framework"),
				"file":                  node.File,
				"line":                  node.Location.LineStart,
				"auth_required":         isAuthRequired,
				"rate_limit":            getStringProp(node.Properties, "rate_limit"),
				"authz_policy":          getStringProp(node.Properties, "authz_policy"),
				"produces_content_type": getStringProp(node.Properties, "produces_content_type"),
				"consumes_content_type": getStringProp(node.Properties, "consumes_content_type"),
				"handler":               getStringProp(node.Properties, "handler"),
			}
			endpoints = append(endpoints, endpoint)
			if fw := getStringProp(node.Properties, "framework"); fw != "" {
				frameworkSet[fw] = true
			}
			
			// 认证统计
			authTotal++
			if isAuthRequired {
				authProtected++
			}
			// 非 GET 方法一般视为需要认证
			if method != "GET" && method != "" {
				authRequired++
			}
			// 未认证的高风险端点（POST/DELETE/PATCH + 无认证）
			if !isAuthRequired && (method == "POST" || method == "DELETE" || method == "PUT" || method == "PATCH") {
				unprotectedHighRisk = append(unprotectedHighRisk, map[string]interface{}{
					"path":    node.Name,
					"method":  method,
					"file":    node.File,
					"line":    node.Location.LineStart,
					"framework": getStringProp(node.Properties, "framework"),
				})
			}
		case graph.NodeFunc:
			funcEntry := map[string]interface{}{
				"id":            node.ID,
				"name":          node.Name,
				"signature":     node.Signature,
				"file":          node.File,
				"line":          node.Location.LineStart,
				"language":      node.Language,
				"is_exported":   node.IsExported,
				"security_role": getStringProp(node.Properties, "security_role"),
				"complexity":    getIntProp(node.Properties, "complexity"),
				"incoming_calls": len(g.GetIncomingRelations(node.ID, graph.RelCalls)),
			}
			if node.Package != "" {
				funcEntry["package"] = node.Package
			}
			if recv := getStringProp(node.Properties, "receiver"); recv != "" {
				funcEntry["receiver"] = recv
			}
			functions = append(functions, funcEntry)
		case graph.NodeType:
			var fields []map[string]interface{}
			for _, rel := range g.GetRelations(node.ID, graph.RelContains) {
				if fnode, ok := g.GetNode(rel.To); ok && fnode.Type == graph.NodeField {
					fEntry := map[string]interface{}{
						"name":        fnode.Name,
						"type":        getStringProp(fnode.Properties, "type"),
						"tag":         getStringProp(fnode.Properties, "tag"),
						"is_exported": fnode.IsExported,
					}
					fields = append(fields, fEntry)
				}
			}
			tpEntry := map[string]interface{}{
				"id":          node.ID,
				"name":        node.Name,
				"file":        node.File,
				"line":        node.Location.LineStart,
				"kind":        getStringProp(node.Properties, "kind"),
				"fields":      fields,
				"is_exported": node.IsExported,
			}
			if methods := getStringSliceProp(node.Properties, "methods"); len(methods) > 0 {
				tpEntry["methods"] = methods
			}
			if impls := getStringSliceProp(node.Properties, "implements"); len(impls) > 0 {
				tpEntry["implements"] = impls
			}
			types = append(types, tpEntry)
		case graph.NodeTODO:
			todos = append(todos, map[string]interface{}{
				"text":         node.Name,
				"comment_type": getStringProp(node.Properties, "comment_type"),
				"function":     getStringProp(node.Properties, "function"),
				"file":         node.File,
				"line":         node.Location.LineStart,
			})
		}
	}
	
	// 从 RelImplements 关系填充 implements（Tree-sitter extractor 不直接解析 implements）
	for _, rel := range g.GetAllRelations() {
		if rel.Type != graph.RelImplements {
			continue
		}
		fromNode, ok1 := g.GetNode(rel.From)
		toNode, ok2 := g.GetNode(rel.To)
		if !ok1 || !ok2 {
			continue
		}
		for _, tp := range types {
			if tp["name"] == fromNode.Name && tp["file"] == fromNode.File {
				var impls []string
				if existing, ok := tp["implements"].([]string); ok {
					impls = existing
				}
				impls = append(impls, toNode.Name)
				tp["implements"] = impls
				break
			}
		}
	}

	// 统计每个 interface 被实现的次数
	// 收集所有 struct 的 implements 列表
	implementedByCount := make(map[string]int)
	for _, tp := range types {
		if tpImpls, ok := tp["implements"].([]string); ok {
			for _, iface := range tpImpls {
				implementedByCount[iface]++
			}
		}
	}
	// 将次数填充到 interface 类型记录中
	for _, tp := range types {
		if tp["kind"] == "interface" {
			if count, ok := implementedByCount[tp["name"].(string)]; ok {
				tp["implemented_by_count"] = count
			} else {
				tp["implemented_by_count"] = 0
			}
		}
	}

	frameworks := make([]string, 0, len(frameworkSet))
	for fw := range frameworkSet {
		frameworks = append(frameworks, fw)
	}

	// 从 metadata 读取分析数据（使用 JSON 转换兼容内存类型和反序列化后的类型）
	var duplicatedFuncs []interface{}
	var apiHealth []interface{}
	var apiHealthAvgScore int
	if meta := g.GetMetadata("duplicated_functions"); meta != nil {
		for _, item := range metadataToSlice(meta) {
			duplicatedFuncs = append(duplicatedFuncs, item)
		}
	}
	if meta := g.GetMetadata("api_health_items"); meta != nil {
		for _, item := range metadataToSlice(meta) {
			apiHealth = append(apiHealth, item)
		}
	}
	if meta := g.GetMetadata("api_health_avg_score"); meta != nil {
		apiHealthAvgScore = metadataToInt(meta)
	}

	// 从 metadata 读取污点分析数据
	var taintPaths []interface{}
	var sinkInfos []interface{}
	var taintPathCount int
	if meta := g.GetMetadata("taint_paths"); meta != nil {
		for _, item := range metadataToSlice(meta) {
			taintPaths = append(taintPaths, item)
		}
	}
	if meta := g.GetMetadata("sink_infos"); meta != nil {
		for _, item := range metadataToSlice(meta) {
			sinkInfos = append(sinkInfos, item)
		}
	}
	if meta := g.GetMetadata("taint_path_count"); meta != nil {
		taintPathCount = metadataToInt(meta)
	}

	// 对 function 按 incoming_calls 降序排序，保证截断后的一致性
	sort.Slice(functions, func(i, j int) bool {
		ci, _ := functions[i]["incoming_calls"].(int)
		cj, _ := functions[j]["incoming_calls"].(int)
		return ci > cj
	})

	// 计算所有 function 的角色分布（不受截断影响）
	roleDistribution := make(map[string]int)
	for _, f := range functions {
		role, _ := f["security_role"].(string)
		if role == "" {
			role = "other"
		}
		roleDistribution[role]++
	}

	result := map[string]interface{}{
		"status":               "completed",
		"node_count":           len(g.AllNodes()),
		"relation_count":       g.RelationCount(),
		"rel_count":            g.RelationCount(),
		"endpoint_count":       len(endpoints),
		"function_count":       len(functions),
		"type_count":           len(types),
		"todo_count":           len(todos),
		"taint_path_count":     taintPathCount,
		"frameworks":           frameworks,
		"endpoints":            endpoints,
		"functions":            functions,
		"role_distribution":    roleDistribution,
		"types":                types,
		"todos":                todos,
		"duplicated_functions": duplicatedFuncs,
		"api_health_items":     apiHealth,
		"api_health_avg_score": apiHealthAvgScore,
		"taint_paths":          taintPaths,
		"sink_infos":           sinkInfos,
		"auth_coverage": map[string]interface{}{
			"total":                authTotal,
			"protected":            authProtected,
			"required":             authRequired,
			"coverage_rate":        fmt.Sprintf("%.0f%%", safeDiv(authProtected, authRequired)*100),
			"unprotected_high_risk": unprotectedHighRisk,
		},
	}

	// 写入缓存（5 秒 TTL）
	s.mu.Lock()
	s.overviewCache[projectID] = overviewCacheEntry{
		data:      result,
		expiresAt: time.Now().Add(5 * time.Second),
	}
	s.mu.Unlock()

	return result, nil
}

func safeDiv(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func getBoolProp(props map[string]interface{}, key string) bool {
	if props == nil {
		return false
	}
	if v, ok := props[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getIntProp(props map[string]interface{}, key string) int {
	if props == nil {
		return 0
	}
	if v, ok := props[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}

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

// metadataToSlice 将 metadata 值（任意类型）统一转换为 []map[string]interface{}
// 兼容直接存储的 Go 结构体切片和 JSON/SQLite 反序列化后的 []interface{}
func metadataToSlice(meta interface{}) []map[string]interface{} {
	if meta == nil {
		return nil
	}
	bytes, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	var result []map[string]interface{}
	if err := json.Unmarshal(bytes, &result); err != nil {
		return nil
	}
	return result
}

// metadataToInt 将 metadata 数值统一转换为 int
// 兼容 int/int64/float64/json.Number
func metadataToInt(meta interface{}) int {
	if meta == nil {
		return 0
	}
	switch v := meta.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	// 泛化回退：JSON 序列化后再反序列化
	bytes, _ := json.Marshal(meta)
	var n int
	if err := json.Unmarshal(bytes, &n); err != nil {
		return 0
	}
	return n
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}