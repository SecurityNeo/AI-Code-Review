package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// GraphScanTask 全量扫描任务
type GraphScanTask struct {
	ID            uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	OrgID         uint           `gorm:"column:org_id;not null;default:1;index:idx_org_id" json:"org_id"`
	ProjectID     uint64         `gorm:"column:project_id;not null;index:idx_project_status"`
	Branch        string         `gorm:"column:branch;not null;default:'main'"`
	ScanType      string         `gorm:"column:scan_type;size:32;not null;default:'full'"` // full | incremental
	Status        string         `gorm:"column:status;size:32;not null;default:'pending'"` // pending | running | completed | failed
	TriggerType   string         `gorm:"column:trigger_type;size:20;not null;default:'manual'"` // manual | mr_merge
	MRIID         int            `gorm:"column:mr_iid;not null;default:0"`                    // MR合入时填充
	MRTitle       string         `gorm:"column:mr_title;size:512"`
	NodeCount     int            `gorm:"column:node_count;not null;default:0"`
	RelationCount int            `gorm:"column:relation_count;not null;default:0"`
	FileCount     int            `gorm:"column:file_count;not null;default:0"`
	DurationMs    int            `gorm:"column:duration_ms;not null;default:0"`
	ErrorMessage  string         `gorm:"column:error_message;type:text"`
	CreatedAt     time.Time      `gorm:"column:created_at;autoCreateTime"`
	StartedAt     *time.Time     `gorm:"column:started_at"`
	CompletedAt   *time.Time     `gorm:"column:completed_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"` // 自动软删除
}

// TableName 指定表名
func (GraphScanTask) TableName() string {
	return "graph_scan_tasks"
}

// GraphVersion 基线版本快照
type GraphVersion struct {
	ID             uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	OrgID          uint       `gorm:"column:org_id;not null;default:1;index:idx_org_id" json:"org_id"`
	ProjectID      uint64     `gorm:"column:project_id;not null;index"`
	Branch         string     `gorm:"column:branch;not null"`
	CommitHash     string     `gorm:"column:commit_hash;size:64;not null"`
	NodeCount      int        `gorm:"column:node_count;not null;default:0"`
	RelationCount  int        `gorm:"column:relation_count;not null;default:0"`
	ScanType       string     `gorm:"column:scan_type;size:32;not null"`
	LastFullScanAt *time.Time `gorm:"column:last_full_scan_at"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// BeforeUpdate GORM hook: 防止 org_id 被修改
func (gv *GraphVersion) BeforeUpdate(tx *gorm.DB) error {
	if tx.Statement == nil {
		return nil
	}
	switch dest := tx.Statement.Dest.(type) {
	case map[string]interface{}:
		if _, exists := dest["org_id"]; exists {
			return errors.New("org_id cannot be modified on GraphVersion")
		}
	case *GraphVersion:
		if dest.OrgID != 0 && dest.OrgID != gv.OrgID {
			return errors.New("org_id cannot be modified on GraphVersion")
		}
	}
	return nil
}

// TableName 指定表名
func (GraphVersion) TableName() string {
	return "graph_versions"
}
