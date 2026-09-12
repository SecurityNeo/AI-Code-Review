package model

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// UserAuthScope 用户多租户认证范围
// 所有权限判定以此为唯一来源，废弃 users.role
type UserAuthScope struct {
	UserID         uint
	IsSuperAdmin   bool
	VisibleOrgIDs  []uint
	CurrentOrgID   uint
	CurrentOrgRole string // developer / org_admin / super_admin
}

// HasOrgRole 检查当前组织角色是否匹配任意给定角色
func (s *UserAuthScope) HasOrgRole(roles ...string) bool {
	for _, r := range roles {
		if s.CurrentOrgRole == r {
			return true
		}
	}
	return false
}

// userAuthJoinResult 单次 JOIN 查询的临时结果结构
type userAuthJoinResult struct {
	User
	OrgID   uint   `gorm:"column:org_id"`
	OrgRole string `gorm:"column:org_role"`
}

// BuildAuthScope 通过 JOIN 查询 + 递归 CTE 构建用户认证范围
// 废弃 users.role，完全以 org_users.role 为权限来源
func BuildAuthScope(userID uint) (*UserAuthScope, error) {
	if DB == nil {
		return nil, errors.New("database not initialized")
	}

	var rows []userAuthJoinResult
	err := DB.Raw(
		`SELECT users.*, org_users.org_id, org_users.role as org_role 
		 FROM users 
		 LEFT JOIN org_users ON users.id = org_users.user_id AND org_users.status = 'active'
		 WHERE users.id = ?
		 ORDER BY org_users.is_default DESC, org_users.id ASC`,
		userID,
	).Scan(&rows).Error

	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("user not found")
	}

	first := rows[0]
	scope := &UserAuthScope{
		UserID:       first.ID,
		CurrentOrgID: first.DefaultOrgID,
	}

	// 收集绑定关系：org_id -> role
	orgRoleMap := make(map[uint]string)
	for _, r := range rows {
		if r.OrgID > 0 {
			orgRoleMap[r.OrgID] = r.OrgRole
			if r.OrgRole == "super_admin" {
				scope.IsSuperAdmin = true
			}
		}
	}

	// 设置 CurrentOrgRole（基于 CurrentOrgID）
	if role, ok := orgRoleMap[scope.CurrentOrgID]; ok {
		scope.CurrentOrgRole = role
	} else {
		// default_org_id 不在 active 绑定中（可能已删除/禁用），fallback 到第一个有效绑定
		scope.CurrentOrgID = 0
		scope.CurrentOrgRole = ""
		for _, r := range rows {
			if r.OrgID > 0 {
				scope.CurrentOrgID = r.OrgID
				scope.CurrentOrgRole = r.OrgRole
				break
			}
		}
	}

	// super_admin：不过滤（VisibleOrgIDs = nil 表示全部可见）
	if scope.IsSuperAdmin {
		return scope, nil
	}

	// 计算 VisibleOrgIDs
	// developer：仅自己绑定的 org_id
	// org_admin：自己绑定的 org_id + 所有后代组织
	visibleSet := make(map[uint]struct{})
	var adminOrgIDs []uint
	for orgID, role := range orgRoleMap {
		visibleSet[orgID] = struct{}{}
		if role == "org_admin" {
			adminOrgIDs = append(adminOrgIDs, orgID)
		}
	}

	// 递归扩展 org_admin 的后代组织（MySQL 8.0 CTE）
	if len(adminOrgIDs) > 0 {
		var descendantIDs []uint
		if err := DB.Raw(`
			WITH RECURSIVE org_tree AS (
				SELECT id FROM organizations WHERE id IN ?
				UNION ALL
				SELECT o.id FROM organizations o JOIN org_tree ot ON o.parent_id = ot.id
			) SELECT id FROM org_tree
		`, adminOrgIDs).Scan(&descendantIDs).Error; err != nil {
			return nil, fmt.Errorf("query org descendants failed: %w", err)
		}
		for _, id := range descendantIDs {
			visibleSet[id] = struct{}{}
		}
	}

	for id := range visibleSet {
		scope.VisibleOrgIDs = append(scope.VisibleOrgIDs, id)
	}

	return scope, nil
}

// OrgScope 返回 GORM Scope 函数，用于按组织过滤查询
// 注意：super_admin 在前置判断中直接返回裸 DB，不会落入 len==0 的分支
func OrgScope(authScope *UserAuthScope) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if authScope == nil {
			return db.Where("1 = 0")
		}
		if authScope.IsSuperAdmin {
			return db
		}
		if len(authScope.VisibleOrgIDs) == 0 {
			return db.Where("1 = 0")
		}
		return db.Where("org_id IN ?", authScope.VisibleOrgIDs)
	}
}
