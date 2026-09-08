package model

import (
	"testing"
)

// TestOrgScopeIsolation org_id 隔离性集成测试
// 验证不同组织的用户不能访问/修改对方的数据
func TestOrgScopeIsolation(t *testing.T) {
	// 注意：此测试需要真实数据库连接，使用 t.Skip() 在 CI 中跳过
	if DB == nil {
		t.Skip("数据库未初始化，跳过集成测试")
	}

	// 清理测试数据
	defer cleanupTestOrgs()

	// 1. 创建两个测试组织
	orgA := Organization{Code: "test-org-a", Name: "测试组织A", Type: "internal", Status: "active"}
	orgB := Organization{Code: "test-org-b", Name: "测试组织B", Type: "internal", Status: "active"}
	if err := DB.Create(&orgA).Error; err != nil {
		t.Fatalf("create orgA failed: %v", err)
	}
	if err := DB.Create(&orgB).Error; err != nil {
		t.Fatalf("create orgB failed: %v", err)
	}

	// 2. 创建两个用户并分别绑定到不同组织
	userA := User{Username: "test-user-a", DefaultOrgID: orgA.ID, Role: RoleUser, LoginType: "local"}
	userB := User{Username: "test-user-b", DefaultOrgID: orgB.ID, Role: RoleUser, LoginType: "local"}
	if err := DB.Create(&userA).Error; err != nil {
		t.Fatalf("create userA failed: %v", err)
	}
	if err := DB.Create(&userB).Error; err != nil {
		t.Fatalf("create userB failed: %v", err)
	}

	dbOrgUserA := OrgUser{OrgID: orgA.ID, UserID: userA.ID, Role: "developer", Status: "active"}
	dbOrgUserB := OrgUser{OrgID: orgB.ID, UserID: userB.ID, Role: "developer", Status: "active"}
	if err := DB.Create(&dbOrgUserA).Error; err != nil {
		t.Fatalf("create org_user A failed: %v", err)
	}
	if err := DB.Create(&dbOrgUserB).Error; err != nil {
		t.Fatalf("create org_user B failed: %v", err)
	}

	// 3. 为每个组织创建一个项目
	projectA := Project{OrgID: orgA.ID, Name: "Project-A", ProjectPath: "group/project-a", Language: "golang"}
	projectB := Project{OrgID: orgB.ID, Name: "Project-B", ProjectPath: "group/project-b", Language: "golang"}
	if err := DB.Create(&projectA).Error; err != nil {
		t.Fatalf("create project A failed: %v", err)
	}
	if err := DB.Create(&projectB).Error; err != nil {
		t.Fatalf("create project B failed: %v", err)
	}

	// 4. 构建用户认证范围
	scopeA, err := BuildAuthScope(userA.ID)
	if err != nil {
		t.Fatalf("build auth scope for userA failed: %v", err)
	}
	scopeB, err := BuildAuthScope(userB.ID)
	if err != nil {
		t.Fatalf("build auth scope for userB failed: %v", err)
	}

	// 5. 验证 OrgScope: userA 只能看到 orgA 的项目
	t.Run("ScopeA filters to orgA only", func(t *testing.T) {
		var projects []Project
		if err := DB.Scopes(OrgScope(scopeA)).Find(&projects).Error; err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(projects) != 1 {
			t.Errorf("userA should see exactly 1 project, got %d", len(projects))
		}
		if len(projects) > 0 && projects[0].ID != projectA.ID {
			t.Errorf("userA should see projectA, got project %d", projects[0].ID)
		}
	})

	// 6. 验证 OrgScope: userB 只能看到 orgB 的项目
	t.Run("ScopeB filters to orgB only", func(t *testing.T) {
		var projects []Project
		if err := DB.Scopes(OrgScope(scopeB)).Find(&projects).Error; err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(projects) != 1 {
			t.Errorf("userB should see exactly 1 project, got %d", len(projects))
		}
		if len(projects) > 0 && projects[0].ID != projectB.ID {
			t.Errorf("userB should see projectB, got project %d", projects[0].ID)
		}
	})

	// 7. userA 尝试通过 Where("id = ?") 直接访问 projectB 应该失败（因为叠加了 Scope）
	t.Run("Cross-org access denied via Scope", func(t *testing.T) {
		var p Project
		err := DB.Where("id = ?", projectB.ID).Scopes(OrgScope(scopeA)).First(&p).Error
		if err == nil {
			t.Error("userA should NOT be able to access projectB via scope filtering")
		}
	})

	// 8. super_admin 应该能看到所有项目
	t.Run("SuperAdmin sees all", func(t *testing.T) {
		adminScope := &UserAuthScope{
			IsSuperAdmin:  true,
			VisibleOrgIDs: []uint{},
		}
		var projects []Project
		if err := DB.Scopes(OrgScope(adminScope)).Find(&projects).Error; err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(projects) != 2 {
			t.Errorf("super_admin should see exactly 2 projects, got %d", len(projects))
		}
	})
}

// TestProjectPathUniqueIndex 验证 ProjectPath 的联合唯一索引
// 不同组织下允许同名项目路径，同一组织下不允许重复
func TestProjectPathUniqueIndex(t *testing.T) {
	if DB == nil {
		t.Skip("数据库未初始化，跳过集成测试")
	}

	defer cleanupTestProjects()

	orgA := Organization{Code: "test-uniq-a", Name: "测试组织A", Type: "internal", Status: "active"}
	orgB := Organization{Code: "test-uniq-b", Name: "测试组织B", Type: "internal", Status: "active"}
	DB.Create(&orgA)
	DB.Create(&orgB)

	// 同一 org 下不能重复
	projectA1 := Project{OrgID: orgA.ID, Name: "P1", ProjectPath: "group/project-same", Language: "golang"}
	projectA2 := Project{OrgID: orgA.ID, Name: "P2", ProjectPath: "group/project-same", Language: "golang"}
	if err := DB.Create(&projectA1).Error; err != nil {
		t.Fatalf("create first project failed: %v", err)
	}
	if err := DB.Create(&projectA2).Error; err == nil {
		t.Error("expected unique constraint violation for same org same path, but succeeded")
	}

	// 不同 org 下允许重复
	projectB := Project{OrgID: orgB.ID, Name: "PB", ProjectPath: "group/project-same", Language: "golang"}
	if err := DB.Create(&projectB).Error; err != nil {
		t.Errorf("different org same path should be allowed, but got: %v", err)
	}
}

// TestBuildAuthScopeInactiveUser 验证 inactive 的用户不可见
func TestBuildAuthScopeInactiveUser(t *testing.T) {
	if DB == nil {
		t.Skip("数据库未初始化，跳过集成测试")
	}

	defer cleanupTestOrgs()

	org := Organization{Code: "test-inact-org", Name: "Inactive Test", Type: "internal", Status: "active"}
	DB.Create(&org)

	user := User{Username: "test-inact-user", DefaultOrgID: org.ID, Role: RoleUser, LoginType: "local"}
	DB.Create(&user)

	// active 状态
	DB.Create(&OrgUser{OrgID: org.ID, UserID: user.ID, Role: "developer", Status: "active"})
	scope, err := BuildAuthScope(user.ID)
	if err != nil {
		t.Fatalf("build scope failed: %v", err)
	}
	if len(scope.VisibleOrgIDs) != 1 {
		t.Errorf("active user should have 1 org, got %d", len(scope.VisibleOrgIDs))
	}

	// 将 org_user 改为 inactive
	DB.Model(&OrgUser{}).Where("user_id = ? AND org_id = ?", user.ID, org.ID).Update("status", "inactive")

	scope2, err := BuildAuthScope(user.ID)
	if err != nil {
		t.Fatalf("build scope after inactive failed: %v", err)
	}
	if len(scope2.VisibleOrgIDs) != 0 {
		t.Errorf("inactive user should have 0 orgs, got %d", len(scope2.VisibleOrgIDs))
	}
}

// cleanupTestOrgs 清理测试产生的组织和关联数据
func cleanupTestOrgs() {
	if DB == nil {
		return
	}
	_ = DB.Where("code LIKE ?", "test-%").Delete(&Organization{})
	_ = DB.Where("username LIKE ?", "test-%").Delete(&User{})
}

// cleanupTestProjects 清理测试产生的项目
func cleanupTestProjects() {
	if DB == nil {
		return
	}
	_ = DB.Where("name LIKE ?", "P%").Delete(&Project{})
}
