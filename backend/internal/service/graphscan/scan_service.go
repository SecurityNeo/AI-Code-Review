package graphscan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	db        *gorm.DB
	workspace string
	storage   graph.GraphStorage
	cache     *graph.GraphCache
	logger    *zap.Logger
	isRunning map[uint64]bool
	mu        sync.Mutex
	repoMgr   *pipeline.RepoManager
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
		db:        db,
		workspace: workspace,
		storage:   graph.NewSQLiteGraphStorage(workspace),
		cache:     graph.NewGraphCache(ttl),
		logger:    zap.L(),
		isRunning: make(map[uint64]bool),
		repoMgr:   pipeline.GetRepoManager(),
	}
}

// RefreshScan 重新全量扫描（同build）
func (s *ScanService) RefreshScan(projectID uint64, branch string) (*model.GraphScanTask, error) {
	return s.TriggerScan(projectID, branch)
}

// TriggerScan 触发项目全量扫描（后台异步）
func (s *ScanService) TriggerScan(projectID uint64, branch string) (*model.GraphScanTask, error) {
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

// GetGraphOverview 获取图谱概览
func (s *ScanService) GetGraphOverview(projectID uint64) (map[string]interface{}, error) {
	if !s.storage.Exists(projectID) {
		return map[string]interface{}{
			"status":     "none",
			"node_count": 0,
			"rel_count":  0,
			"endpoints":  []interface{}{},
		}, nil
	}

	g, err := s.storage.Load(projectID)
	if err != nil {
		return nil, err
	}

	var endpoints []map[string]interface{}
	var functions []map[string]interface{}
	var types []map[string]interface{}
	frameworkSet := make(map[string]bool)
	for _, node := range g.AllNodes() {
		switch node.Type {
		case graph.NodeEndpoint:
			endpoints = append(endpoints, map[string]interface{}{
				"path":      node.Name,
				"method":    getStringProp(node.Properties, "method"),
				"framework": getStringProp(node.Properties, "framework"),
				"file":      node.File,
				"line":      node.Location.LineStart,
			})
			if fw := getStringProp(node.Properties, "framework"); fw != "" {
				frameworkSet[fw] = true
			}
		case graph.NodeFunc:
			funcEntry := map[string]interface{}{
				"name":      node.Name,
				"signature": node.Signature,
				"file":      node.File,
				"line":      node.Location.LineStart,
				"language":  node.Language,
			}
			if node.Package != "" {
				funcEntry["package"] = node.Package
			}
			functions = append(functions, funcEntry)
		case graph.NodeType:
			var fields []map[string]interface{}
			for _, rel := range g.GetRelations(node.ID, graph.RelContains) {
				if fnode, ok := g.GetNode(rel.To); ok && fnode.Type == graph.NodeField {
					fEntry := map[string]interface{}{
						"name": fnode.Name,
						"type": getStringProp(fnode.Properties, "type"),
						"tag":  getStringProp(fnode.Properties, "tag"),
					}
					fields = append(fields, fEntry)
				}
			}
			types = append(types, map[string]interface{}{
				"name":   node.Name,
				"file":   node.File,
				"line":   node.Location.LineStart,
				"kind":   getStringProp(node.Properties, "kind"),
				"fields": fields,
			})
		}
	}

	frameworks := make([]string, 0, len(frameworkSet))
	for fw := range frameworkSet {
		frameworks = append(frameworks, fw)
	}

	return map[string]interface{}{
		"status":         "completed",
		"node_count":     len(g.AllNodes()),
		"relation_count": g.RelationCount(),
		"rel_count":      g.RelationCount(),
		"endpoint_count": len(endpoints),
		"function_count": len(functions),
		"type_count":     len(types),
		"frameworks":     frameworks,
		"endpoints":      endpoints,
		"functions":      functions[:min(50, len(functions))],
		"types":          types[:min(50, len(types))],
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
