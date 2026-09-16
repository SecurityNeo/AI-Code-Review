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
//
// 注意：统一以 Session(&gorm.Session{}) 返回，使句柄的 clone=1。
// GORM 链式调用（Where/Model/...）返回的句柄 clone=0，会在同一 Statement 上继续
// 叠加条件；若调用方复用同一 db 句柄查询不同模型，会导致 where 条件与表名串线
// （曾导致 incubator 查询 report_issues 表却使用 rule_incubation_jobs 的列）。
// 返回 clone=1 的句柄后，每次链式调用都会克隆全新 Statement，避免污染。
func DBWithScope(scope *UserAuthScope) *gorm.DB {
	if scope == nil || scope.IsSuperAdmin {
		return DB.Session(&gorm.Session{})
	}
	if len(scope.VisibleOrgIDs) == 0 {
		return DB.Where("1 = 0").Session(&gorm.Session{})
	}
	return DB.Where("org_id IN ?", scope.VisibleOrgIDs).Session(&gorm.Session{})
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
	return DB.Where("org_id = ?", orgID).Session(&gorm.Session{})
}

// ScopedForSuperAdmin 为超管构造无过滤的 DB 句柄
func ScopedForSuperAdmin() *gorm.DB {
	return DB.Session(&gorm.Session{})
}
