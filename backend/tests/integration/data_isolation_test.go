package integration

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/stretchr/testify/assert"
)

// TestDataIsolation 验证跨 org 数据不可见
// T8.2: org_A 用户查询 org_B 的 project/task/issue 返回空
func TestDataIsolation(t *testing.T) {
	// 准备：创建两个组织
	orgA := model.Organization{Name: "Org A"}
	orgB := model.Organization{Name: "Org B"}
	model.DB.Create(&orgA)
	model.DB.Create(&orgB)

	// 创建用户并绑定组织
	userA := model.User{Username: "user_a", GitlabEmail: "a@test.com", DefaultOrgID: orgA.ID}
	userB := model.User{Username: "user_b", GitlabEmail: "b@test.com", DefaultOrgID: orgB.ID}
	model.DB.Create(&userA)
	model.DB.Create(&userB)
	model.DB.Create(&model.OrgUser{OrgID: orgA.ID, UserID: userA.ID, Role: "org_admin", Status: "active"})
	model.DB.Create(&model.OrgUser{OrgID: orgB.ID, UserID: userB.ID, Role: "org_admin", Status: "active"})

	// 创建项目（分别归属不同 org）
	projectA := model.Project{Name: "Project A", ProjectPath: "org-a/project-a", OrgID: orgA.ID}
	projectB := model.Project{Name: "Project B", ProjectPath: "org-b/project-b", OrgID: orgB.ID}
	model.DB.Create(&projectA)
	model.DB.Create(&projectB)

	// 创建任务
	taskA := model.Task{ProjectID: projectA.ID, OrgID: orgA.ID, SourceBranch: "feat/a"}
	taskB := model.Task{ProjectID: projectB.ID, OrgID: orgB.ID, SourceBranch: "feat/b"}
	model.DB.Create(&taskA)
	model.DB.Create(&taskB)

	// 创建 ReviewIssue
	issueA := model.ReviewIssue{TaskID: taskA.ID, OrgID: orgA.ID, RuleCode: "test-rule", Message: "Issue A"}
	issueB := model.ReviewIssue{TaskID: taskB.ID, OrgID: orgB.ID, RuleCode: "test-rule", Message: "Issue B"}
	model.DB.Create(&issueA)
	model.DB.Create(&issueB)

	// === 验证：userA 只能看到 orgA 的数据 ===
	scopeA := &model.UserAuthScope{
		UserID:         userA.ID,
		CurrentOrgID:   orgA.ID,
		VisibleOrgIDs:  []uint{orgA.ID},
		CurrentOrgRole: "org_admin",
	}

	var projects []model.Project
	model.DB.Scopes(model.OrgScope(scopeA)).Find(&projects)
	assert.Len(t, projects, 1, "userA should only see 1 project")
	assert.Equal(t, projectA.ID, projects[0].ID)

	var tasks []model.Task
	model.DB.Scopes(model.OrgScope(scopeA)).Find(&tasks)
	assert.Len(t, tasks, 1, "userA should only see 1 task")
	assert.Equal(t, taskA.ID, tasks[0].ID)

	var issues []model.ReviewIssue
	model.DB.Scopes(model.OrgScope(scopeA)).Find(&issues)
	assert.Len(t, issues, 1, "userA should only see 1 issue")
	assert.Equal(t, issueA.ID, issues[0].ID)

	// === 验证：userB 只能看到 orgB 的数据 ===
	scopeB := &model.UserAuthScope{
		UserID:         userB.ID,
		CurrentOrgID:   orgB.ID,
		VisibleOrgIDs:  []uint{orgB.ID},
		CurrentOrgRole: "org_admin",
	}

	var projectsB []model.Project
	model.DB.Scopes(model.OrgScope(scopeB)).Find(&projectsB)
	assert.Len(t, projectsB, 1, "userB should only see 1 project")
	assert.Equal(t, projectB.ID, projectsB[0].ID)

	// === 验证：super_admin 可以看到全部 ===
	scopeSuper := &model.UserAuthScope{
		UserID:        userA.ID,
		IsSuperAdmin:  true,
		VisibleOrgIDs: []uint{},
	}
	var allProjects []model.Project
	model.DB.Scopes(model.OrgScope(scopeSuper)).Find(&allProjects)
	assert.GreaterOrEqual(t, len(allProjects), 2, "super_admin should see all projects")
}

// TestOrgScopeNilReturnsEmpty 验证 authScope=nil 时返回空结果
func TestOrgScopeNilReturnsEmpty(t *testing.T) {
	var projects []model.Project
	model.DB.Scopes(model.OrgScope(nil)).Find(&projects)
	assert.Len(t, projects, 0, "nil scope should return empty result")
}

// TestOrgScopeEmptyVisibleOrgsReturnsEmpty 验证 visibleOrgs=[] 时返回空结果
func TestOrgScopeEmptyVisibleOrgsReturnsEmpty(t *testing.T) {
	scope := &model.UserAuthScope{UserID: 1, VisibleOrgIDs: []uint{}}
	var projects []model.Project
	model.DB.Scopes(model.OrgScope(scope)).Find(&projects)
	assert.Len(t, projects, 0, "empty visibleOrgs should return empty result")
}

// TestProjectOrgUniqueIndex 验证 (org_id, project_path) 复合唯一索引
func TestProjectOrgUniqueIndex(t *testing.T) {
	org := model.Organization{Name: "Test Org"}
	model.DB.Create(&org)

	p1 := model.Project{Name: "P1", ProjectPath: "test/repo", OrgID: org.ID}
	assert.NoError(t, model.DB.Create(&p1).Error)

	// 同一组织下相同 path 应该冲突
	p2 := model.Project{Name: "P2", ProjectPath: "test/repo", OrgID: org.ID}
	err := model.DB.Create(&p2).Error
	assert.Error(t, err, "duplicate project_path within same org should fail")

	// 不同组织下相同 path 应该成功
	org2 := model.Organization{Name: "Test Org 2"}
	model.DB.Create(&org2)
	p3 := model.Project{Name: "P3", ProjectPath: "test/repo", OrgID: org2.ID}
	assert.NoError(t, model.DB.Create(&p3).Error)
}

// TestTaskInheritsOrgIDFromProject 验证 Task 从 Project 继承 org_id
func TestTaskInheritsOrgIDFromProject(t *testing.T) {
	org := model.Organization{Name: "Task Org"}
	model.DB.Create(&org)

	project := model.Project{Name: "Task Project", ProjectPath: "task/proj", OrgID: org.ID}
	model.DB.Create(&project)

	task := model.Task{ProjectID: project.ID, SourceBranch: "feat/test"}
	model.DB.Create(&task)

	assert.Equal(t, org.ID, task.OrgID, "Task should inherit org_id from Project")
}
