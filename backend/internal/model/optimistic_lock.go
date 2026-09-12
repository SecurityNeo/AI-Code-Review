package model

import (
	"fmt"

	"gorm.io/gorm"
)

// OptimisticUpdateError 乐观锁冲突错误
type OptimisticUpdateError struct {
	Entity  string
	ID      uint
	Version int
}

func (e *OptimisticUpdateError) Error() string {
	return fmt.Sprintf("optimistic lock conflict: %s id=%d version=%d", e.Entity, e.ID, e.Version)
}

// OptimisticUpdate 执行带乐观锁的检查更新。
// db 为已设置 WHERE 条件的 *gorm.DB（必须包含主键和版本条件）
// dest 为要更新的结构体指针指针
// 返回 error: 若 RowsAffected==0 则返回 OptimisticUpdateError
func OptimisticUpdate(db *gorm.DB, entity string, dest interface{}) error {
	result := db.Updates(dest)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return &OptimisticUpdateError{Entity: entity, ID: 0, Version: 0}
	}
	return nil
}
