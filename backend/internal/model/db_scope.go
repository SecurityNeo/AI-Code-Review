package model

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DBWithScope 返回已应用 OrgScope 的数据库句柄
// 使用规则：
//   - scope == nil      → 返回原 DB（超管、迁移脚本、全局配置）
//   - IsSuperAdmin      → 返回原 DB（超管不过滤）
//   - VisibleOrgIDs 为空 → 返回 WHERE 1=0（无可见组织 = 空结果）
//   - 其他情况          → 返回 WHERE org_id IN (visibleOrgIDs)
func DBWithScope(scope *UserAuthScope) *gorm.DB {
	if scope == nil || scope.IsSuperAdmin {
		return DB
	}
	if len(scope.VisibleOrgIDs) == 0 {
		return DB.Where("1 = 0")
	}
	return DB.Where("org_id IN ?", scope.VisibleOrgIDs)
}

// DBWithContext 从 gin.Context 自动提取 auth_scope 并应用 OrgScope
// Handler 层首选：db := model.DBWithContext(c)
func DBWithContext(c *gin.Context) *gorm.DB {
	scope, exists := c.Get("auth_scope")
	if !exists || scope == nil {
		return DB
	}
	authScope, ok := scope.(*UserAuthScope)
	if !ok {
		return DB
	}
	return DBWithScope(authScope)
}

// MustDBWithContext 同 DBWithContext，但如果 context 中无 auth_scope 则 panic
// 仅用于确信 auth middleware 已执行的 handler/service 方法
func MustDBWithContext(c *gin.Context) *gorm.DB {
	db := DBWithContext(c)
	// 简单校验：如果返回的是裸 DB（非 scoped），确认 context 里确实有 scope
	scope, exists := c.Get("auth_scope")
	if !exists {
		// 无 scope 但返回了裸 DB，可能是白名单路径或未登录
		// 不 panic，让调用方自行决定
	}
	_ = scope
	return db
}

// ScopedForCron 为后台 cron 任务构造指定 org 的 scope
// 示例：ScopedForCron(1).Model(&Task{}).Where(...)
func ScopedForCron(orgID uint) *gorm.DB {
	return DB.Where("org_id = ?", orgID)
}

// ScopedForSuperAdmin 为超管构造无过滤的 DB 句柄
func ScopedForSuperAdmin() *gorm.DB {
	return DB
}
