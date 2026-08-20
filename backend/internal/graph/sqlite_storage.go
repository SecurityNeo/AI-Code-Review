package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ai-optimizer/backend/internal/parser"
	_ "modernc.org/sqlite"
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS graph_nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_type TEXT NOT NULL,
    labels TEXT NOT NULL DEFAULT '[]',
    props TEXT NOT NULL DEFAULT '{}',
    file_path TEXT,
    line_start INTEGER,
    line_end INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_nodes_type ON graph_nodes(node_type);
CREATE INDEX IF NOT EXISTS idx_nodes_file ON graph_nodes(file_path);

CREATE TABLE IF NOT EXISTS graph_relations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    rel_type TEXT NOT NULL,
    from_node_id INTEGER NOT NULL,
    to_node_id INTEGER,
    props TEXT DEFAULT '{}',
    file_path TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (from_node_id) REFERENCES graph_nodes(id) ON DELETE CASCADE,
    FOREIGN KEY (to_node_id) REFERENCES graph_nodes(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_rel_type ON graph_relations(rel_type);
CREATE INDEX IF NOT EXISTS idx_rel_from ON graph_relations(from_node_id);
CREATE INDEX IF NOT EXISTS idx_rel_to ON graph_relations(to_node_id);
CREATE INDEX IF NOT EXISTS idx_rel_file ON graph_relations(file_path);

CREATE TABLE IF NOT EXISTS graph_file_index (
    file_path TEXT PRIMARY KEY,
    node_ids TEXT NOT NULL DEFAULT '[]',
    relation_ids TEXT NOT NULL DEFAULT '[]',
    last_modified DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS graph_metadata (
    key TEXT PRIMARY KEY,
    value TEXT,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT OR IGNORE INTO graph_metadata (key, value) VALUES ('schema_version', '1.0');
`

// SQLiteGraphStorage SQLite 实现（零 CGO，纯 Go）
type SQLiteGraphStorage struct {
	workspace string
	dbCache   map[uint64]*sql.DB
	mu        sync.Mutex
}

// NewSQLiteGraphStorage 创建 SQLite 存储
func NewSQLiteGraphStorage(workspace string) GraphStorage {
	return &SQLiteGraphStorage{
		workspace: workspace,
		dbCache:   make(map[uint64]*sql.DB),
	}
}

func (s *SQLiteGraphStorage) getDBPath(projectID uint64) string {
	newPath := filepath.Join(s.workspace, "graph", fmt.Sprintf("project_%d_baseline.db", projectID))
	oldPath := filepath.Join(s.workspace, fmt.Sprintf("project_%d_baseline.db", projectID))

	// 兼容旧格式：如果旧路径存在而新路径不存在，继续使用旧路径
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	if _, err := os.Stat(oldPath); err == nil {
		return oldPath
	}
	// 默认使用新路径
	return newPath
}

func (s *SQLiteGraphStorage) getDB(projectID uint64) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if db, ok := s.dbCache[projectID]; ok {
		return db, nil
	}

	dbPath := s.getDBPath(projectID)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create dir for project %d failed: %w", projectID, err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=DELETE&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite for project %d failed: %w", projectID, err)
	}

	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema for project %d failed: %w", projectID, err)
	}

	// 切换 WAL 模式到 DELETE（如果之前是 WAL）
	if _, err := db.Exec("PRAGMA journal_mode=DELETE;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("switch journal mode failed: %w", err)
	}

	// 删除可能残留的 WAL 文件（确保干净状态）
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	s.dbCache[projectID] = db
	return db, nil
}

// Save 保存图到 SQLite
func (s *SQLiteGraphStorage) Save(projectID uint64, graph *MemorySymbolGraph) error {
	db, err := s.getDB(projectID)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM graph_nodes"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM graph_relations"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM graph_file_index"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM graph_metadata WHERE key != 'schema_version'"); err != nil {
		return err
	}

	nodeStmt, err := tx.Prepare(`
		INSERT INTO graph_nodes (node_type, labels, props, file_path, line_start, line_end)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer nodeStmt.Close()

	nodeIDMap := make(map[string]int64)
	fileNodeIDs := make(map[string][]int64)

	for _, node := range graph.AllNodes() {
		labels, _ := json.Marshal([]string{node.Type})
		props, _ := json.Marshal(nodeToProps(node))

		res, err := nodeStmt.Exec(
			node.Type, string(labels), string(props),
			node.File, node.Location.LineStart, node.Location.LineEnd,
		)
		if err != nil {
			return fmt.Errorf("insert node %s failed: %w", node.ID, err)
		}
		id, _ := res.LastInsertId()
		nodeIDMap[node.ID] = id
		fileNodeIDs[node.File] = append(fileNodeIDs[node.File], id)
	}

	relStmt, err := tx.Prepare(`
		INSERT INTO graph_relations (rel_type, from_node_id, to_node_id, props, file_path)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer relStmt.Close()

	fileRelIDs := make(map[string][]int64)
	for _, rel := range graph.GetAllRelations() {
		fromID := nodeIDMap[rel.From]
		if fromID == 0 {
			continue
		}
		toID := nodeIDMap[rel.To]
		if toID == 0 {
			// 外部依赖：目标节点不在图中，存储为 -1（ sentinel 值）
			toID = -1
		}
		props, _ := json.Marshal(rel.Extra)
		if toID == -1 {
			// 外部依赖：在 props 中存储目标 ID
			var extra map[string]interface{}
			if rel.Extra != nil {
				extra = make(map[string]interface{}, len(rel.Extra))
				for k, v := range rel.Extra {
					extra[k] = v
				}
			}
			if extra == nil {
				extra = make(map[string]interface{})
			}
			extra["target_id"] = rel.To
			props, _ = json.Marshal(extra)
		}
		res, err := relStmt.Exec(rel.Type, fromID, toID, string(props), "")
		if err != nil {
			return fmt.Errorf("insert relation failed: %w", err)
		}
		id, _ := res.LastInsertId()
		fileRelIDs[""] = append(fileRelIDs[""], id)
	}

	fileStmt, err := tx.Prepare(`
		INSERT INTO graph_file_index (file_path, node_ids, relation_ids, last_modified)
		VALUES (?, ?, ?, datetime('now'))
	`)
	if err != nil {
		return err
	}
	defer fileStmt.Close()
	for file, nids := range fileNodeIDs {
		nidsJSON, _ := json.Marshal(nids)
		ridsJSON, _ := json.Marshal(fileRelIDs[file])
		_, err := fileStmt.Exec(file, string(nidsJSON), string(ridsJSON))
		if err != nil {
			return err
		}
	}

	metaStmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO graph_metadata (key, value, updated_at)
		VALUES (?, ?, datetime('now'))
	`)
	if err != nil {
		return err
	}
	defer metaStmt.Close()
	for k, v := range graph.AllMetadata() {
		metaJSON, _ := json.Marshal(v)
		_, err := metaStmt.Exec(k, string(metaJSON))
		if err != nil {
			return fmt.Errorf("insert metadata %s failed: %w", k, err)
		}
	}

	return tx.Commit()
}

// Load 从 SQLite 加载图
func (s *SQLiteGraphStorage) Load(projectID uint64) (*MemorySymbolGraph, error) {
	db, err := s.getDB(projectID)
	if err != nil {
		return nil, err
	}

	graph := NewMemorySymbolGraph(projectID)

	rows, err := db.Query(`
		SELECT id, node_type, props, file_path, line_start, line_end
		FROM graph_nodes
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodeIDMap := make(map[int64]string)
	for rows.Next() {
		var id int64
		var nodeType, propsJSON, filePath string
		var lineStart, lineEnd int
		if err := rows.Scan(&id, &nodeType, &propsJSON, &filePath, &lineStart, &lineEnd); err != nil {
			return nil, err
		}
		node := &MemorySymbolNode{
			Type:     nodeType,
			File:     filePath,
			Location: parser.SourceLocation{File: filePath, LineStart: lineStart, LineEnd: lineEnd},
		}
		if propsJSON != "" {
			var props map[string]interface{}
			_ = json.Unmarshal([]byte(propsJSON), &props)
			node.Name = getStringPropFromMap(props, "name")
			node.Language = getStringPropFromMap(props, "language")
			node.Package = getStringPropFromMap(props, "package")
			node.Signature = getStringPropFromMap(props, "signature")
			if v, ok := props["is_exported"]; ok {
				node.IsExported = v == true
			}
			node.Properties = props
			// 优先恢复原始 ID，避免基于 lineStart 重新生成导致冲突
			if origID := getStringPropFromMap(props, "__original_id"); origID != "" {
				node.ID = origID
			} else {
				node.ID = fmt.Sprintf("%s:%s:%d", nodeType, filePath, lineStart)
			}
		} else {
			node.ID = fmt.Sprintf("%s:%s:%d", nodeType, filePath, lineStart)
		}
		graph.AddNode(node)
		nodeIDMap[id] = node.ID
	}

	relRows, err := db.Query(`SELECT rel_type, from_node_id, to_node_id, props FROM graph_relations`)
	if err != nil {
		return nil, err
	}
	defer relRows.Close()

	for relRows.Next() {
		var relType string
		var fromID, toID int64
		var propsJSON string
		if err := relRows.Scan(&relType, &fromID, &toID, &propsJSON); err != nil {
			return nil, err
		}
		fromNodeID := nodeIDMap[fromID]
		if fromNodeID == "" {
			continue
		}
		// 支持外部依赖：to_node_id=-1 表示目标不在图中
		var toNodeID string
		if toID == -1 {
			// 尝试从 relation props 或 Extra 中恢复目标 ID
			var extra map[string]string
			if propsJSON != "" {
				_ = json.Unmarshal([]byte(propsJSON), &extra)
			}
			if targetID, ok := extra["target_id"]; ok && targetID != "" {
				toNodeID = targetID
			}
			if toNodeID == "" {
				continue
			}
		} else {
			toNodeID = nodeIDMap[toID]
		}
		rel := MemoryRelation{From: fromNodeID, To: toNodeID, Type: relType}
		if propsJSON != "" {
			_ = json.Unmarshal([]byte(propsJSON), &rel.Extra)
		}
		graph.AddRelation(rel)
	}

	// 加载 metadata
	metaRows, err := db.Query(`SELECT key, value FROM graph_metadata WHERE key != 'schema_version'`)
	if err == nil {
		defer metaRows.Close()
		for metaRows.Next() {
			var k, v string
			if err := metaRows.Scan(&k, &v); err == nil && v != "" {
				var val interface{}
				if err := json.Unmarshal([]byte(v), &val); err == nil {
					graph.SetMetadata(k, val)
				}
			}
		}
	}

	return graph, nil
}

// Delete 删除项目图谱
func (s *SQLiteGraphStorage) Delete(projectID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if db, ok := s.dbCache[projectID]; ok {
		db.Close()
		delete(s.dbCache, projectID)
	}

	dbPath := s.getDBPath(projectID)
	return os.Remove(dbPath)
}

// Exists 检查项目图谱是否存在
func (s *SQLiteGraphStorage) Exists(projectID uint64) bool {
	dbPath := s.getDBPath(projectID)
	_, err := os.Stat(dbPath)
	return err == nil
}

// nodeToProps 将节点转换为属性 map
func nodeToProps(node *MemorySymbolNode) map[string]interface{} {
	props := map[string]interface{}{
		"name":          node.Name,
		"language":      node.Language,
		"package":       node.Package,
		"signature":     node.Signature,
		"is_exported":   node.IsExported,
		"__original_id": node.ID,
	}
	if node.Properties != nil {
		for k, v := range node.Properties {
			if _, ok := props[k]; !ok {
				props[k] = v
			}
		}
	}
	return props
}

func getStringPropFromMap(props map[string]interface{}, key string) string {
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
