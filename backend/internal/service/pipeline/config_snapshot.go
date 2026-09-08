package pipeline

import (
	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// OrgConfigSnapshot 组织配置快照
// 多租户改造核心：在 Pipeline 开始时一次性加载并冻结配置，
// 确保同一任务的多个 Stage 始终使用同一套配置，避免 Stage 间配置被修改导致不一致。
type OrgConfigSnapshot struct {
	AgentConfig  model.ReviewAgentConfig // review_agent_configs（org 特定配置）
	PrimaryModel *model.LLMModel         // llm_models（org 的主模型）
	Rules        []model.ReviewRule      // review_rules（org 的规则，含全局规则兼容）
}

// LoadOrgConfigSnapshot 按 org_id 加载组织配置快照
// 多租户改造：替代硬编码 model.DB.First(&agentCfg, 1)，按 org_id 查询配置
func LoadOrgConfigSnapshot(db *gorm.DB, orgID uint) (*OrgConfigSnapshot, error) {
	var snapshot OrgConfigSnapshot

	// 1. 加载 ReviewAgentConfig（优先按 org_id，fallback 到根组织 org_id=1）
	var agentCfg model.ReviewAgentConfig
	if err := db.Where("org_id = ?", orgID).First(&agentCfg).Error; err != nil {
		// fallback：如果找不到当前 org 的配置，尝试根组织配置
		if err := db.Where("org_id = 1").First(&agentCfg).Error; err != nil {
			zap.L().Warn("加载 ReviewAgentConfig 失败，使用零值",
				zap.Uint("org_id", orgID), zap.Error(err))
		} else {
			agentCfg.MigrateContextExtractConfig()
			snapshot.AgentConfig = agentCfg
		}
	} else {
		agentCfg.MigrateContextExtractConfig()
		snapshot.AgentConfig = agentCfg
	}

	// 2. 加载主模型（org 的 IsPrimary=true 的模型，fallback 到 IsDefault）
	var primaryModel model.LLMModel
	if err := db.Where("org_id = ? AND is_primary = ?", orgID, true).First(&primaryModel).Error; err != nil {
		// fallback：尝试 org 的默认模型
		if err := db.Where("org_id = ? AND is_default = ?", orgID, true).First(&primaryModel).Error; err != nil {
			// fallback：根组织的主模型
			if err := db.Where("org_id = 1 AND is_primary = ?", true).First(&primaryModel).Error; err != nil {
				zap.L().Warn("加载主模型失败",
					zap.Uint("org_id", orgID), zap.Error(err))
			} else {
				snapshot.PrimaryModel = &primaryModel
			}
		} else {
			snapshot.PrimaryModel = &primaryModel
		}
	} else {
		snapshot.PrimaryModel = &primaryModel
	}

	// 3. 加载评审规则：当前 org 规则 + 全局规则（org_id=1）
	// 多租户改造：替代无过滤的全表查询 model.DB.Find(&allRules)
	if err := db.Where("org_id = ? OR org_id = 1", orgID).
		Order("sort_order ASC").Find(&snapshot.Rules).Error; err != nil {
		zap.L().Warn("加载评审规则失败",
			zap.Uint("org_id", orgID), zap.Error(err))
	}

	return &snapshot, nil
}

// GetOrgConfigSnapshotFromCtx 从 StageContext 中提取 OrgConfigSnapshot
// 多租户改造：所有 Stage 统一通过此辅助函数获取快照，不再直接查库
func GetOrgConfigSnapshotFromCtx(ctx StageContext) *OrgConfigSnapshot {
	if v, ok := ctx.GetInput("_org_config_snapshot").(*OrgConfigSnapshot); ok && v != nil {
		return v
	}
	// 向后兼容：如果快照未注入（旧调用路径），返回 nil，调用方应自行 fallback 到旧逻辑
	return nil
}
