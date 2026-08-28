package model

import (
	"fmt"
	"time"
)

// MRDiffMetaQuery 封装 MRDiffMeta 的常用数据库操作
type MRDiffMetaQuery struct{}

// NewMRDiffMetaQuery 创建查询器
func NewMRDiffMetaQuery() *MRDiffMetaQuery {
	return &MRDiffMetaQuery{}
}

// Create 创建 MRDiffMeta 记录
func (q *MRDiffMetaQuery) Create(meta *MRDiffMeta) error {
	if meta == nil {
		return fmt.Errorf("meta is nil")
	}
	if DB == nil {
		return fmt.Errorf("DB is nil")
	}
	return DB.Create(meta).Error
}

// GetByTaskID 按 task_id 查询
func (q *MRDiffMetaQuery) GetByTaskID(taskID uint) (*MRDiffMeta, error) {
	if DB == nil {
		return nil, fmt.Errorf("DB is nil")
	}
	var meta MRDiffMeta
	if err := DB.Where("task_id = ?", taskID).First(&meta).Error; err != nil {
		return nil, fmt.Errorf("mr_diff_meta not found for task %d: %w", taskID, err)
	}
	return &meta, nil
}

// GetByProjectAndMR 按 project_id + mr_iid 查询（可能返回多条，因为一个 MR 可能有多个 task）
func (q *MRDiffMetaQuery) GetByProjectAndMR(projectID uint, mrIID int) ([]MRDiffMeta, error) {
	if DB == nil {
		return nil, fmt.Errorf("DB is nil")
	}
	var metas []MRDiffMeta
	if err := DB.Where("project_id = ? AND mr_iid = ?", projectID, mrIID).Order("created_at DESC").Find(&metas).Error; err != nil {
		return nil, fmt.Errorf("query mr_diff_meta failed: %w", err)
	}
	return metas, nil
}

// GetByObjectKey 按 object_key 查询
func (q *MRDiffMetaQuery) GetByObjectKey(objectKey string) (*MRDiffMeta, error) {
	if DB == nil {
		return nil, fmt.Errorf("DB is nil")
	}
	var meta MRDiffMeta
	if err := DB.Where("object_key = ?", objectKey).First(&meta).Error; err != nil {
		return nil, fmt.Errorf("mr_diff_meta not found for key %s: %w", objectKey, err)
	}
	return &meta, nil
}

// UpdateDiffRefs 更新 diff_refs 字段
func (q *MRDiffMetaQuery) UpdateDiffRefs(taskID uint, baseSha, headSha, startSha string) error {
	if DB == nil {
		return fmt.Errorf("DB is nil")
	}
	updates := map[string]interface{}{
		"base_sha":  baseSha,
		"head_sha":  headSha,
		"start_sha": startSha,
	}
	result := DB.Model(&MRDiffMeta{}).Where("task_id = ?", taskID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update diff_refs failed for task %d: %w", taskID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("no mr_diff_meta record found for task %d", taskID)
	}
	return nil
}

// DeleteByTaskID 幂等删除（记录不存在视为成功）
func (q *MRDiffMetaQuery) DeleteByTaskID(taskID uint) error {
	if DB == nil {
		return fmt.Errorf("DB is nil")
	}
	result := DB.Where("task_id = ?", taskID).Delete(&MRDiffMeta{})
	if result.Error != nil {
		return fmt.Errorf("delete mr_diff_meta failed for task %d: %w", taskID, result.Error)
	}
	return nil
}

// ListByProject 分页查询某项目的 MRDiffMeta
func (q *MRDiffMetaQuery) ListByProject(projectID uint, limit, offset int) ([]MRDiffMeta, int64, error) {
	if DB == nil {
		return nil, 0, fmt.Errorf("DB is nil")
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := DB.Model(&MRDiffMeta{}).Where("project_id = ?", projectID).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count mr_diff_meta failed: %w", err)
	}

	var metas []MRDiffMeta
	if err := DB.Where("project_id = ?", projectID).Order("created_at DESC").Limit(limit).Offset(offset).Find(&metas).Error; err != nil {
		return nil, 0, fmt.Errorf("list mr_diff_meta failed: %w", err)
	}
	return metas, total, nil
}

// ListStale 查询已过期（超过 retentionDays 天）的记录，用于定期清理
func (q *MRDiffMetaQuery) ListStale(retentionDays int, limit int) ([]MRDiffMeta, error) {
	if DB == nil {
		return nil, fmt.Errorf("DB is nil")
	}
	if retentionDays <= 0 {
		retentionDays = 90 // 默认保留 90 天
	}
	if limit <= 0 {
		limit = 100
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	var metas []MRDiffMeta
	if err := DB.Where("created_at < ?", cutoff).Order("created_at ASC").Limit(limit).Find(&metas).Error; err != nil {
		return nil, fmt.Errorf("list stale mr_diff_meta failed: %w", err)
	}
	return metas, nil
}

// Exists 检查 task_id 是否已有记录
func (q *MRDiffMetaQuery) Exists(taskID uint) (bool, error) {
	if DB == nil {
		return false, fmt.Errorf("DB is nil")
	}
	var count int64
	if err := DB.Model(&MRDiffMeta{}).Where("task_id = ?", taskID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
