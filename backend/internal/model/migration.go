package model

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// migrateOrgColumns 安全地为现有表添加 org_id 列
// 使用 HasColumn 检测防止重复执行，支持 MySQL/PostgreSQL/SQLite 三方言
func migrateOrgColumns() error {
	// 定义需要添加 org_id 的表及其索引配置
	type orgColumnDef struct {
		model     interface{}
		column    string
		sqlType   string
		indexes   []string // 需要创建的索引SQL
		fks       []string // 需要添加的外键SQL（如果有）
	}

	// Note: 使用 GORM 的 HasColumn 检测列是否存在
	// 对于不同方言，使用 GORM 抽象的 Migrator

	// users 表特殊处理：添加 default_org_id 而非 org_id
	if err := addColumnIfNotExists(&User{}, "default_org_id"); err != nil {
		return fmt.Errorf("add default_org_id to users failed: %w", err)
	}
	if err := addIndexIfNotExists("users", "idx_default_org_id", "default_org_id"); err != nil {
		return fmt.Errorf("add idx_default_org_id failed: %w", err)
	}

	// projects 表：添加 org_id 和 gitlab_instance_id
	if err := addColumnIfNotExists(&Project{}, "org_id"); err != nil {
		return fmt.Errorf("add org_id to projects failed: %w", err)
	}
	if err := addColumnIfNotExists(&Project{}, "gitlab_instance_id"); err != nil {
		return fmt.Errorf("add gitlab_instance_id to projects failed: %w", err)
	}
	if err := addIndexIfNotExists("projects", "idx_org_id", "org_id"); err != nil {
		return fmt.Errorf("add idx org_id to projects failed: %w", err)
	}
	if err := addIndexIfNotExists("projects", "idx_org_project_path", "org_id, project_path"); err != nil {
		return fmt.Errorf("add idx org_project_path failed: %w", err)
	}
	if err := addIndexIfNotExists("projects", "idx_gitlab_instance", "gitlab_instance_id"); err != nil {
		return fmt.Errorf("add idx gitlab_instance failed: %w", err)
	}

	// 核心任务表
	coreModels := []struct {
		model  interface{}
		table  string
	}{
		{&Task{}, "tasks"},
		{&ReviewIssue{}, "review_issues"},
		{&ResourcePool{}, "resource_pools"},
		{&LLMModel{}, "llm_models"},
		{&ReviewRule{}, "review_rules"},
		{&ProjectTemplate{}, "project_templates"},
		{&ProjectReviewConfig{}, "project_review_configs"},
		{&ReviewAgentConfig{}, "review_agent_configs"},
		{&ProjectResponsibility{}, "project_responsibilities"},
		{&TaskReviewComment{}, "task_review_comments"},
		{&TaskReviewRule{}, "task_review_rules"},
		{&TaskPipelineExecution{}, "task_pipeline_executions"},
		{&ReviewIssueRuleMatch{}, "review_issue_rule_matches"},
		{&RuleIncubation{}, "rule_incubations"},
		{&Notification{}, "notifications"},
		{&NotificationRule{}, "notification_rules"},
		{&WeComNotifier{}, "wecom_notifiers"},
		{&ReportConfig{}, "report_configs"},
		{&ReportRecipient{}, "report_recipients"},
		{&MCPAPIKey{}, "mcp_api_keys"},
		{&OperationLog{}, "operation_logs"},
		{&MergeRequestReviewLog{}, "merge_request_review_logs"},
		{&MRDiffMeta{}, "mr_diff_meta"},
		{&GraphScanTask{}, "graph_scan_tasks"},
		{&GraphVersion{}, "graph_versions"},
		{&ReportLog{}, "report_logs"},
		{&ObjectStorageConfig{}, "object_storage_configs"},
		{&VulnerabilityAllowlist{}, "vulnerability_allowlists"},
		{&ReviewIssueVector{}, "review_issue_vectors"},
		{&ReviewRuleVector{}, "review_rule_vectors"},
		{&SecretScanFinding{}, "secret_scan_findings"},
		{&SecurityAuditFinding{}, "security_audit_findings"},
		{&IncubatorConfig{}, "incubator_configs"},
		{&RuleIncubationJob{}, "rule_incubation_jobs"},
		{&NotificationDeliveryLog{}, "notification_delivery_logs"},
		// NOTE: MCPCallLog 未设计 org_id 隔离（同 llm_call_logs），不在此处添加
		{&Token{}, "tokens"},
	}

	for _, m := range coreModels {
		if err := addColumnIfNotExists(m.model, "org_id"); err != nil {
			return fmt.Errorf("add org_id to %s failed: %w", m.table, err)
		}
		if err := addIndexIfNotExists(m.table, "idx_org_id", "org_id"); err != nil {
			return fmt.Errorf("add idx_org_id to %s failed: %w", m.table, err)
		}
	}

	// vulnerability_allowlists 需要额外的 unique index（使用 vuln_id，不是 cve_id）
	if err := addUniqueIndexIfNotExists("vulnerability_allowlists", "idx_org_vuln", "org_id, vuln_id"); err != nil {
		return fmt.Errorf("add idx_org_vuln failed: %w", err)
	}

	// resource_pools 和 llm_models 需要添加 is_shared 字段
	if err := addColumnIfNotExists(&ResourcePool{}, "is_shared"); err != nil {
		return fmt.Errorf("add is_shared to resource_pools failed: %w", err)
	}
	if err := addColumnIfNotExists(&LLMModel{}, "is_shared"); err != nil {
		return fmt.Errorf("add is_shared to llm_models failed: %w", err)
	}

	// review_rules 需要添加 scope 字段
	if err := addColumnIfNotExists(&ReviewRule{}, "scope"); err != nil {
		return fmt.Errorf("add scope to review_rules failed: %w", err)
	}
	if err := addIndexIfNotExists("review_rules", "idx_org_scope", "org_id, scope"); err != nil {
		return fmt.Errorf("add idx_org_scope failed: %w", err)
	}

	// wecom_notifiers 需要添加 webhook_key_enc
	if err := addColumnIfNotExists(&WeComNotifier{}, "webhook_key_enc"); err != nil {
		return fmt.Errorf("add webhook_key_enc to wecom_notifiers failed: %w", err)
	}

	// tokens 需要添加 revoked_at
	if err := addColumnIfNotExists(&Token{}, "revoked_at"); err != nil {
		return fmt.Errorf("add revoked_at to tokens failed: %w", err)
	}

	return nil
}

// addColumnIfNotExists 使用 GORM Migrator 安全添加列（跨方言兼容）
// 如果表不存在，自动跳过（某些 model 的表可能尚未创建）
func addColumnIfNotExists(model interface{}, columnName string) error {
	if !DB.Migrator().HasTable(model) {
		zap.L().Warn("addColumnIfNotExists: table not found, skip", zap.String("column", columnName))
		return nil
	}
	if DB.Migrator().HasColumn(model, columnName) {
		return nil
	}
	return DB.Migrator().AddColumn(model, columnName)
}

// addIndexIfNotExists 安全创建普通索引（如果不存在）
// 如果表不存在，自动跳过
func addIndexIfNotExists(tableName, indexName, columns string) error {
	var tableExists int64
	DB.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", tableName).Scan(&tableExists)
	if tableExists == 0 {
		zap.L().Warn("addIndexIfNotExists: table not found, skip", zap.String("table", tableName))
		return nil
	}
	if DB.Migrator().HasIndex(tableName, indexName) {
		return nil
	}
	return DB.Exec(fmt.Sprintf("CREATE INDEX %s ON %s (%s)", indexName, tableName, columns)).Error
}

// addUniqueIndexIfNotExists 安全创建唯一索引（如果不存在）
// 如果表或列不存在，自动跳过
func addUniqueIndexIfNotExists(tableName, indexName, columns string) error {
	var tableExists int64
	DB.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", tableName).Scan(&tableExists)
	if tableExists == 0 {
		zap.L().Warn("addUniqueIndexIfNotExists: table not found, skip", zap.String("table", tableName))
		return nil
	}
	// 检查所有列是否存在
	colList := strings.Split(columns, ",")
	for _, col := range colList {
		colTrimmed := strings.TrimSpace(col)
		var colExists int64
		DB.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", tableName, colTrimmed).Scan(&colExists)
		if colExists == 0 {
			zap.L().Warn("addUniqueIndexIfNotExists: column not found, skip", zap.String("table", tableName), zap.String("column", colTrimmed))
			return nil
		}
	}
	if DB.Migrator().HasIndex(tableName, indexName) {
		return nil
	}
	return DB.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s)", indexName, tableName, columns)).Error
}

// getTableName 通过 GORM Statement 解析获取表名
func getTableName(model interface{}) string {
	stmt := &gorm.Statement{DB: DB}
	_ = stmt.Parse(model)
	return stmt.Table
}

// getTypeName 已废弃，使用 getTableName 替代
func getTypeName(model interface{}) string {
	return getTableName(model)
}

// MigrateAll 执行所有迁移（包括新表创建和存量表改造）
func MigrateAll() error {
	// 1. 创建新表（GORM AutoMigrate 会自动创建不存在的表）
	newModels := []interface{}{
		&Organization{},
		&Department{},
		&Team{},
		&GitLabInstance{},
		&GitLabUserBinding{},
		&OrgUser{},
		&OrgUserInvite{},
		&TeamProject{},
		&ResourceQuota{},
	}

	for _, m := range newModels {
		if err := DB.AutoMigrate(m); err != nil {
			return fmt.Errorf("auto migrate new model failed: %w", err)
		}
	}

	// 2. 存量表添加 org_id 列
	if err := migrateOrgColumns(); err != nil {
		return fmt.Errorf("migrate org columns failed: %w", err)
	}

	// 2.5. 将旧的 system_configs 中的 GitLab 配置迁移到独立的 gitlab_instances 表
	if err := migrateGitLabFromSystemConfig(); err != nil {
		return fmt.Errorf("migrate gitlab from system config failed: %w", err)
	}

	// 3. 更新 GORM 模型缓存
	if err := DB.AutoMigrate(
		&Project{}, &Task{}, &ReviewIssue{}, &ResourcePool{}, &LLMModel{},
		&ReviewRule{}, &ProjectTemplate{}, &ProjectReviewConfig{}, &ReviewAgentConfig{},
		&ProjectResponsibility{}, &TaskReviewComment{}, &TaskReviewRule{},
		&TaskPipelineExecution{}, &ReviewIssueRuleMatch{}, &RuleIncubation{},
		&Notification{}, &NotificationRule{}, &WeComNotifier{},
		&ReportConfig{}, &ReportRecipient{}, &MCPAPIKey{}, &OperationLog{},
		&MergeRequestReviewLog{}, &MRDiffMeta{}, &GraphScanTask{}, &GraphVersion{},
		&ReportLog{}, &ObjectStorageConfig{}, &VulnerabilityAllowlist{},
		&ReviewIssueVector{}, &ReviewRuleVector{}, &SecretScanFinding{},
		&SecurityAuditFinding{}, &IncubatorConfig{}, &RuleIncubationJob{},
		&NotificationDeliveryLog{}, &MCPCallLog{}, &Token{}, &User{},
	); err != nil {
		return fmt.Errorf("auto migrate existing models failed: %w", err)
	}

	// 4. 初始化根组织（如果不存在）
	if err := initRootOrganization(); err != nil {
		return fmt.Errorf("init root organization failed: %w", err)
	}

	return nil
}

// initRootOrganization 初始化根组织（org_id = 1）
// 多租户改造：自动创建根组织及其默认资源配额
func initRootOrganization() error {
	var count int64
	DB.Model(&Organization{}).Where("id = 1").Count(&count)
	if count > 0 {
		// 根组织已存在，确保默认配额存在
		ensureDefaultQuotas(1)
		return nil
	}

	rootOrg := Organization{
		ID:     1,
		Code:   "root",
		Name:   "根组织",
		Type:   "internal",
		Status: "active",
	}

	if err := DB.Create(&rootOrg).Error; err != nil {
		return err
	}

	// 为根组织创建默认资源配额（0=无限制）
	ensureDefaultQuotas(1)

	return nil
}

// ensureDefaultQuotas 为组织创建默认资源配额（幂等）
func ensureDefaultQuotas(orgID uint) {
	quotaTypes := []string{"project", "user", "parallel_task", "llm_tokens_daily"}
	for _, qt := range quotaTypes {
		var q ResourceQuota
		if err := DB.Where("org_id = ? AND resource_type = ?", orgID, qt).First(&q).Error; err != nil {
			// 不存在则创建（limit=0表示无限制）
			q = ResourceQuota{
				OrgID:        orgID,
				ResourceType: qt,
				LimitValue:   0,
				UsedValue:    0,
				PeriodType:   "monthly",
			}
			if err := DB.Create(&q).Error; err != nil {
				zap.L().Warn("create default quota failed", zap.Uint("org_id", orgID), zap.String("type", qt), zap.Error(err))
			}
		}
	}
}

// InitRootOrgIDForExistingData 为现有数据设置 org_id = 1
func InitRootOrgIDForExistingData() error {
	// 此函数在 migrateOrgColumns 之后执行，确保 DEFAULT 1 已生效
	// 可选：显式更新所有 org_id = 0 或 NULL 的记录
	tables := []struct {
		model interface{}
		name  string
	}{
		{&Project{}, "projects"},
		{&Task{}, "tasks"},
		{&ReviewIssue{}, "review_issues"},
	}

	for _, t := range tables {
		result := DB.Model(t.model).Where("org_id = 0 OR org_id IS NULL").Update("org_id", 1)
		if result.Error != nil {
			zap.L().Warn("init root org_id failed", zap.String("table", t.name), zap.Error(result.Error))
		} else if result.RowsAffected > 0 {
			zap.L().Info("init root org_id", zap.String("table", t.name), zap.Int64("rows", result.RowsAffected))
		}
	}

	return nil
}

// migrateGitLabFromSystemConfig 将旧的 system_configs 中的 GitLab 配置迁移到独立的 gitlab_instances 表
// 仅执行一次：当 gitlab_instances 为空且 system_configs 中有 GitLab 配置时
func migrateGitLabFromSystemConfig() error {
	// 检查 gitlab_instances 是否已有数据
	var instanceCount int64
	if err := DB.Model(&GitLabInstance{}).Count(&instanceCount).Error; err != nil {
		return fmt.Errorf("count gitlab_instances failed: %w", err)
	}
	if instanceCount > 0 {
		return nil // 已有数据，不重复迁移
	}

	// 读取 system_configs 中的 GitLab 旧配置
	var cfg SystemConfig
	if err := DB.First(&cfg).Error; err != nil {
		return nil // system_configs 表尚无数据，无配置可迁移
	}

	// 如果 base_url 为空，说明 GitLab 从未配置过，无需迁移
	if cfg.GitlabBaseURL == "" {
		return nil
	}

	// 提取 client_secret（GORM 使用 gitlab_oauth_client_secret 列名）
	var clientSecret string
	DB.Raw("SELECT gitlab_oauth_client_secret FROM system_configs LIMIT 1").Scan(&clientSecret)

	// 创建第一条 gitlab_instances 记录
	inst := GitLabInstance{
		OrgID:               1, // 根组织
		Name:                "默认实例",
		BaseURL:             cfg.GitlabBaseURL,
		WebhookSecret:       "",
		IsDefault:           true,
		Status:              "active",
		OAuthEnabled:        cfg.GitlabOAuthEnabled,
		OAuthClientID:       cfg.GitlabOAuthClientID,
		OAuthClientSecret:   clientSecret,
		OAuthRedirectURI:    cfg.GitlabOAuthRedirectURI,
		OAuthAutoCreateUser: cfg.GitlabOAuthAutoCreateUser,
		OAuthSkipVerify:     cfg.GitlabOAuthSkipVerify,
	}
	if err := DB.Create(&inst).Error; err != nil {
		zap.L().Error("migrate gitlab from system config failed", zap.Error(err))
		return fmt.Errorf("create default gitlab instance failed: %w", err)
	}

	// 清空 system_configs 中的 GitLab 字段（避免后续从旧表读取）
	if err := DB.Model(&cfg).Updates(map[string]interface{}{
		"gitlab_oauth_enabled":        false,
		"gitlab_base_url":             "",
		"gitlab_oauth_client_id":      "",
		"gitlab_oauth_client_secret":  "",
		"gitlab_oauth_redirect_uri":   "",
		"gitlab_oauth_auto_create_user": true,
		"gitlab_oauth_skip_verify":    false,
	}).Error; err != nil {
		zap.L().Warn("clear gitlab fields from system_config failed", zap.Error(err))
		// 不返回错误，因为实例已创建成功
	}

	zap.L().Info("migrated gitlab config from system_config to gitlab_instances", zap.Uint("instance_id", inst.ID))
	return nil
}

// SoftDeleteByOrg 按组织软删除（保留数据但设 deleted_at）
func SoftDeleteByOrg(model interface{}, orgID uint) error {
	return DB.Where("org_id = ?", orgID).Delete(model).Error
}

// parseIntEnv 解析环境变量为 int
func parseIntEnv(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return n
}
