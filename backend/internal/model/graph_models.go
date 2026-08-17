package model

import "time"

// GraphScanTask 全量扫描任务
type GraphScanTask struct {
	ID            uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID     uint64     `gorm:"column:project_id;not null;index:idx_project_status"`
	Branch        string     `gorm:"column:branch;not null;default:'main'"`
	ScanType      string     `gorm:"column:scan_type;size:32;not null;default:'full'"` // full | incremental
	Status        string     `gorm:"column:status;size:32;not null;default:'pending'"` // pending | running | completed | failed
	NodeCount     int        `gorm:"column:node_count;not null;default:0"`
	RelationCount int        `gorm:"column:relation_count;not null;default:0"`
	FileCount     int        `gorm:"column:file_count;not null;default:0"`
	DurationMs    int        `gorm:"column:duration_ms;not null;default:0"`
	ErrorMessage  string     `gorm:"column:error_message;type:text"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	StartedAt     *time.Time `gorm:"column:started_at"`
	CompletedAt   *time.Time `gorm:"column:completed_at"`
}

// TableName 指定表名
func (GraphScanTask) TableName() string {
	return "graph_scan_tasks"
}

// GraphVersion 基线版本快照
type GraphVersion struct {
	ID             uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID      uint64     `gorm:"column:project_id;not null;index"`
	Branch         string     `gorm:"column:branch;not null"`
	CommitHash     string     `gorm:"column:commit_hash;size:64;not null"`
	NodeCount      int        `gorm:"column:node_count;not null;default:0"`
	RelationCount  int        `gorm:"column:relation_count;not null;default:0"`
	ScanType       string     `gorm:"column:scan_type;size:32;not null"`
	LastFullScanAt *time.Time `gorm:"column:last_full_scan_at"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 指定表名
func (GraphVersion) TableName() string {
	return "graph_versions"
}
