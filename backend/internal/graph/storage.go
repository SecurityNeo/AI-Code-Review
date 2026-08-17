package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// JSONFileStorage 基于JSON文件的图谱存储（无CGO依赖）
type JSONFileStorage struct {
	workspace string
	mu        sync.Mutex
}

// graphData JSON序列化结构
type graphData struct {
	Nodes     []MemorySymbolNode `json:"nodes"`
	Relations []MemoryRelation   `json:"relations"`
	Version   string             `json:"version"`
}

// NewJSONFileStorage 创建JSON文件存储
func NewJSONFileStorage(workspace string) GraphStorage {
	return &JSONFileStorage{workspace: workspace}
}

func (s *JSONFileStorage) getDBPath(projectID uint64) string {
	newPath := filepath.Join(s.workspace, "graph", fmt.Sprintf("project_%d_baseline.json", projectID))
	oldPath := filepath.Join(s.workspace, fmt.Sprintf("project_%d_baseline.json", projectID))

	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	if _, err := os.Stat(oldPath); err == nil {
		return oldPath
	}
	return newPath
}

// Save 保存图到JSON文件
func (s *JSONFileStorage) Save(projectID uint64, graph *MemorySymbolGraph) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dbPath := s.getDBPath(projectID)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create dir failed: %w", err)
	}

	data := graphData{
		Version: "1.0",
		Nodes:   make([]MemorySymbolNode, 0, len(graph.AllNodes())),
	}
	for _, node := range graph.AllNodes() {
		data.Nodes = append(data.Nodes, *node)
	}
	for _, rel := range graph.relations {
		data.Relations = append(data.Relations, rel)
	}

	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal graph failed: %w", err)
	}

	if err := os.WriteFile(dbPath, bytes, 0644); err != nil {
		return fmt.Errorf("write graph file failed: %w", err)
	}
	return nil
}

// Load 从JSON文件加载图
func (s *JSONFileStorage) Load(projectID uint64) (*MemorySymbolGraph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dbPath := s.getDBPath(projectID)
	bytes, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("read graph file failed: %w", err)
	}

	var data graphData
	if err := json.Unmarshal(bytes, &data); err != nil {
		return nil, fmt.Errorf("unmarshal graph failed: %w", err)
	}

	graph := NewMemorySymbolGraph(projectID)
	for i := range data.Nodes {
		node := &data.Nodes[i]
		graph.AddNode(node)
	}
	for _, rel := range data.Relations {
		graph.AddRelation(rel)
	}
	return graph, nil
}

// Delete 删除项目图谱
func (s *JSONFileStorage) Delete(projectID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dbPath := s.getDBPath(projectID)
	return os.Remove(dbPath)
}

// Exists 检查项目图谱是否存在
func (s *JSONFileStorage) Exists(projectID uint64) bool {
	dbPath := s.getDBPath(projectID)
	_, err := os.Stat(dbPath)
	return err == nil
}
