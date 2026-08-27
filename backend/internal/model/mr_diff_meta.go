package model

import "gorm.io/gorm"

// MRDiffMeta 存储 MR diff 的轻量索引，原始 diff 保存在对象存储中
type MRDiffMeta struct {
	gorm.Model
	TaskID      uint   `gorm:"index;not null"` // 关联的任务 ID
	ProjectID   uint   `gorm:"index;not null"` // 项目 ID
	MRIID       int    `gorm:"not null"`       // GitLab MR IID
	ObjectKey   string `gorm:"size:512"`       // 对象存储中的 key（如 diffs/project_42/mr_100/task_1234.diff）
	BaseSha     string `gorm:"size:64"`        // diff_refs.base_sha
	HeadSha     string `gorm:"size:64"`        // diff_refs.head_sha
	StartSha    string `gorm:"size:64"`        // diff_refs.start_sha
	LineMapJSON string `gorm:"type:text"`      // DiffLineInfo 数组的 JSON（n/o/t 格式）
}

// TableName 自定义表名
func (MRDiffMeta) TableName() string {
	return "mr_diff_meta"
}
