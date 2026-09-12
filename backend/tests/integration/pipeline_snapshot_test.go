package integration

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"github.com/stretchr/testify/assert"
)

// TestPipelineSnapshotConsistency 验证 Pipeline 配置快照一致性
// T8.4: Stage1 后修改 review_agent_configs，Stage11 仍用旧配置
func TestPipelineSnapshotConsistency(t *testing.T) {
	// 创建项目和任务
	org := model.Organization{Name: "Snapshot Org"}
	model.DB.Create(&org)

	project := model.Project{Name: "Snapshot Project", ProjectPath: "snap/proj", OrgID: org.ID}
	model.DB.Create(&project)

	task := model.Task{ProjectID: project.ID, OrgID: org.ID, SourceBranch: "feat/snapshot"}
	model.DB.Create(&task)

	// 创建初始 ReviewAgentConfig
	initialConfig := model.ReviewAgentConfig{
		OrgID:             org.ID,
		EnabledStages:     `["trigger_check","git_clone","code_understanding","dependency_scan","batch_review_frame","review_arbitration","post_process"]`,
		ShowAgentStatus:   true,
		SecretScanEnabled: false,
	}
	model.DB.Create(&initialConfig)

	// 1. 加载配置快照
	snapshot, err := pipeline.LoadOrgConfigSnapshot(model.DB, org.ID)
	assert.NoError(t, err)
	assert.NotNil(t, snapshot)
	assert.Equal(t, initialConfig.ID, snapshot.AgentConfig.ID)

	// 2. 验证快照中的 secret_scan 是关闭的
	assert.False(t, snapshot.AgentConfig.SecretScanEnabled)

	// 3. 模拟 Stage1 后修改配置（开启 secret_scan）
	updatedConfig := model.ReviewAgentConfig{ID: initialConfig.ID, OrgID: org.ID}
	model.DB.Model(&updatedConfig).Update("secret_scan_enabled", true)

	// 4. 确认快照仍然是旧值（不受实时修改影响）
	assert.False(t, snapshot.AgentConfig.SecretScanEnabled, "Snapshot should NOT reflect real-time config change")

	// 5. 但新加载的快照应反映最新配置
	newSnapshot, err := pipeline.LoadOrgConfigSnapshot(model.DB, org.ID)
	assert.NoError(t, err)
	assert.True(t, newSnapshot.AgentConfig.SecretScanEnabled, "New snapshot should reflect updated config")
}
