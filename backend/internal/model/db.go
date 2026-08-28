package model

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ai-optimizer/backend/config"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// FilterByUser 按用户角色过滤数据查询
// admin 不过滤，user 按 authorColumn = gitlab_username 过滤
func FilterByUser(db *gorm.DB, user User, authorColumn string) *gorm.DB {
	if user.Role == RoleAdmin || user.GitlabUsername == "" {
		return db
	}
	return db.Where(authorColumn+" = ?", user.GitlabUsername)
}

func InitDB(cfg *config.Config) error {
	var dialector gorm.Dialector
	dsn := cfg.GetDSN()

	switch cfg.Database {
	case "postgres":
		dialector = postgres.Open(dsn)
	default:
		dialector = mysql.Open(dsn)
	}

	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             0, // 不打印慢查询（避免原型阶段噪音）
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true, // 关闭 record not found 日志
			Colorful:                  false,
		},
	)

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:                                   newLogger,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return fmt.Errorf("connect database failed: %w", err)
	}

	DB = db
	return autoMigrate()
}

// migrateOAuthColumns 兼容已有表：添加 SystemConfig 新增的 OAuth 列
func migrateSystemConfigColumns() {
	// GORM 将 GitlabOAuthClientID 翻译为 gitlab_o_auth_client_id（多出一个下划线）
	// 而 json tag / frontend 使用的是 gitlab_oauth_client_id
	// 这导致数据库中可能同时存在两套列名，数据读写分离
	// 修复：显式指定 column tag 后，统一使用正确命名的列，删除错误列

	correctColumns := []struct {
		name string
		def  string
	}{
		{"gitlab_oauth_enabled", "TINYINT(1) NOT NULL DEFAULT 0"},
		{"gitlab_base_url", "VARCHAR(512)"},
		{"gitlab_oauth_client_id", "VARCHAR(255)"},
		{"gitlab_oauth_client_secret", "VARCHAR(255)"},
		{"gitlab_oauth_redirect_uri", "VARCHAR(512)"},
		{"gitlab_oauth_auto_create_user", "TINYINT(1) NOT NULL DEFAULT 1"},
		{"gitlab_oauth_skip_verify", "TINYINT(1) NOT NULL DEFAULT 0"},
	}

	wrongSuffixes := []string{
		"_o_auth_", // GORM 错误生成的 gitlab_o_auth_client_id 等
	}

	// 1. 删除错误命名的列（如果存在）
	// 注意：只对 gitlab_oauth_xxx 字段检测 GORM 错误命名（如 gitlab_o_auth_client_id）
	// gitlab_base_url 不属于 gitlab_oauth_ 前缀，不需要检测错误命名
	for _, col := range correctColumns {
		if !strings.HasPrefix(col.name, "gitlab_oauth_") {
			continue // 跳过非 gitlab_oauth_ 前缀的字段
		}
		wrongName := strings.ReplaceAll(col.name, "gitlab_oauth_", "gitlab_o_auth_")
		if DB.Migrator().HasColumn(&SystemConfig{}, wrongName) {
			if err := DB.Exec("ALTER TABLE system_configs DROP COLUMN " + wrongName).Error; err != nil {
				zap.L().Warn("drop wrong column failed, may have data",
					zap.String("column", wrongName), zap.Error(err))
			} else {
				zap.L().Info("dropped wrong column", zap.String("column", wrongName))
			}
		}
		_ = wrongSuffixes // 标记使用
	}

	// 2. 创建正确命名的列（如果不存在）
	for _, col := range correctColumns {
		if !DB.Migrator().HasColumn(&SystemConfig{}, col.name) {
			sql := fmt.Sprintf("ALTER TABLE system_configs ADD COLUMN %s %s", col.name, col.def)
			if err := DB.Exec(sql).Error; err != nil {
				zap.L().Warn("add column failed", zap.String("column", col.name), zap.Error(err))
			} else {
				zap.L().Info("added column", zap.String("column", col.name))
			}
		}
	}
}

func autoMigrate() error {
	// 按依赖顺序创建表，避免外键约束问题
	if err := DB.AutoMigrate(
		&SystemConfig{},
		&OperationLog{},
	); err != nil {
		return err
	}

	// 兼容已有表：补充新增列（AutoMigrate 在某些场景下可能遗漏）
	migrateSystemConfigColumns()

	if err := DB.AutoMigrate(
		&User{},
		&Token{},
	); err != nil {
		return err
	}

	// 兼容：现有用户标记 login_type = local，role 保持现有值（主要是 admin）
	// 只要 role 为空字符串的记录才设为 'user'
	DB.Exec("UPDATE users SET login_type = 'local' WHERE login_type = '' OR login_type IS NULL")
	DB.Exec("UPDATE users SET role = 'admin' WHERE role = '' OR role IS NULL")

	if err := DB.AutoMigrate(
		&ResourcePool{},
		&LLMModel{},
		&WeComNotifier{},
		&TeamMember{}, // 人员管理主表
	); err != nil {
		return err
	}

	// 新增：AI 结构化评审相关表
	if err := DB.AutoMigrate(
		&ReviewRule{},
		&ReviewCategory{},
	); err != nil {
		return err
	}

	if err := DB.AutoMigrate(
		&ProjectTemplate{},
	); err != nil {
		return err
	}

	// Issue 治理与通知系统相关表
	if err := DB.AutoMigrate(
		&Notification{},
		&NotificationDeliveryLog{},
		&ProjectResponsibility{}, // 项目职责分配
		&Holiday{},
		&NotificationRule{},
		&NotificationGlobalSetting{}, // 通知规则生效起点
	); err != nil {
		return err
	}

	if err := DB.AutoMigrate(
		&Project{},
	); err != nil {
		return err
	}

	if err := DB.AutoMigrate(
		&Task{},
		&TaskReviewComment{},
		&ProjectReviewConfig{},
		&ReviewIssue{},
		&TaskReviewRule{},
		&MRDiffMeta{}, // Diff line-map 索引表
	); err != nil {
		return err
	}

	// 清理 Task.used_model_id 空字符串脏数据（从 string 改为 uint 前的兼容处理）
	DB.Exec("UPDATE tasks SET model_id = 0 WHERE model_id = '' OR model_id IS NULL")

	// 删除已废弃的 user_review_comment 列（数据已迁移到 TaskReviewComment 表）
	if DB.Migrator().HasColumn(&Task{}, "user_review_comment") {
		if err := DB.Migrator().DropColumn(&Task{}, "user_review_comment"); err != nil {
			zap.L().Warn("drop column user_review_comment failed", zap.Error(err))
		} else {
			zap.L().Info("dropped deprecated column user_review_comment")
		}
	}

	// 初始化内置评审规则
	initBuiltInReviewRules()

	// 初始化内置评审维度
	initBuiltInReviewCategories()

	// 为现有项目生成默认规则配置
	initDefaultProjectReviewConfigs()

	// 初始化系统配置（确保任务超时等配置有默认值）
	initSystemConfig()

	if err := DB.AutoMigrate(
		&MergeRequestReviewLog{},
		&SMTPConfig{},
		&ReportConfig{},
		&ReportRecipient{},
		&ReportLog{},
	); err != nil {
		return err
	}

	// 让 review_count 字段可空（已存在的记录可能没有该字段的数据，设为 0）
	DB.Exec("UPDATE merge_request_review_logs SET review_count = 0 WHERE review_count IS NULL")

	// 清理已废弃的字段：webhook_key（已从 Model 移除，旧表残留会导致插入报错）
	if DB.Migrator().HasColumn(&WeComNotifier{}, "webhook_key") {
		DB.Migrator().DropColumn(&WeComNotifier{}, "webhook_key")
	}

	// Token 用量监控表
	if err := DB.AutoMigrate(
		&LLMCallLog{},
	); err != nil {
		return err
	}

	// 兼容已有表：为 llm_models 添加 model_type 列和索引
	migrateLLMModelColumns()

	// Rule Incubator 相关表
	if err := DB.AutoMigrate(
		&IncubatorConfig{},
		&RuleIncubation{},
		&RuleIncubationJob{},
		&ReviewIssueRuleMatch{},
		&ReviewIssueVector{},
		&ReviewRuleVector{},
		&RuleIncubationVector{},
	); err != nil {
		return err
	}

	// 初始化 IncubatorConfig 单例（确保 id=1 存在）
	initIncubatorConfig()

	// 初始化 NotificationGlobalSetting 单例（确保 id=1 存在）
	initNotificationGlobalSetting()

	// 兼容：为已有 rule_incubations 补充 similar_passed 默认值（根据 similar_rules 推断）
	DB.Exec("UPDATE rule_incubations SET similar_passed = true WHERE similar_rules IN ('[]','{}','null') AND similar_passed = false")

	// ========== Pipeline 链路可视化 + 对象存储 + 知识图谱（新增表）==========
	if err := DB.AutoMigrate(
		&ObjectStorageConfig{},
		&ReviewPipelineStage{},
		&TaskPipelineExecution{},
		&GraphScanTask{},
		&GraphVersion{},
	); err != nil {
		return err
	}

	// ========== 漏洞数据库（新增表）==========
	if err := DB.AutoMigrate(&VulnerabilityRecord{}); err != nil {
		return err
	}
	if err := DB.AutoMigrate(&VulnerabilityAllowlist{}); err != nil {
		return err
	}
	if err := DB.AutoMigrate(&VulnerabilitySyncSource{}); err != nil {
		return err
	}
	// 初始化内置漏洞库同步源
	initBuiltinVulnSyncSources()

	// ========== AI 评审智能体全局配置（新增表）==========
	if err := DB.AutoMigrate(&ReviewAgentConfig{}); err != nil {
		return err
	}
	initReviewAgentConfig()

	// ========== Phase C：扩展智能体规则库 ==========
	if err := DB.AutoMigrate(&SecretScanRule{}); err != nil {
		return err
	}
	if err := DB.AutoMigrate(&SecretScanFinding{}); err != nil {
		return err
	}
	initSecretScanRules()

	if err := DB.AutoMigrate(&SecurityAuditRule{}); err != nil {
		return err
	}
	if err := DB.AutoMigrate(&SecurityAuditFinding{}); err != nil {
		return err
	}
	initSecurityAuditRules()

	// Token 开销校准记录表
	if err := DB.AutoMigrate(&OverheadCalibration{}); err != nil {
		return err
	}

	// 兼容：移除 object_storage_configs.name 的旧 unique 索引（已改为普通字段允许多个同名配置）
	if DB.Migrator().HasIndex(&ObjectStorageConfig{}, "idx_object_storage_configs_name") {
		if err := DB.Migrator().DropIndex(&ObjectStorageConfig{}, "idx_object_storage_configs_name"); err != nil {
			zap.L().Warn("drop old unique index on object_storage_configs.name failed", zap.Error(err))
		} else {
			zap.L().Info("dropped old unique index on object_storage_configs.name")
		}
	}

	// MCP 集成相关表
	if err := DB.AutoMigrate(&MCPAPIKey{}, &MCPCallLog{}, &MCPTool{}); err != nil {
		return err
	}

	// 初始化 Pipeline 阶段定义
	initPipelineStages()

	// 初始化 MCP 工具元数据
	initMCPTools()

	return nil
}

// OrderColumn helper for sorting
func OrderColumn(key string) string {
	o := map[string]string{
		"created_at": "created_at",
		"updated_at": "updated_at",
		"status":     "status",
		"name":       "name",
	}
	if v, ok := o[key]; ok {
		return v
	}
	return "created_at DESC"
}

// SilentFirst 执行 First 查询但忽略 record not found 日志（初始化场景使用）
func SilentFirst(db *gorm.DB, dest interface{}, conds ...interface{}) error {
	return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).First(dest, conds...).Error
}
func Paginate(page, pageSize int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if page <= 0 {
			page = 1
		}
		if pageSize <= 0 {
			pageSize = 20
		}
		if pageSize > 100 {
			pageSize = 100
		}
		offset := (page - 1) * pageSize
		return db.Offset(offset).Limit(pageSize)
	}
}

// RecordOpLog records an operation log
func RecordOpLog(opType, opObject string, objectID, userID uint, result, errorMsg, requestIP string) {
	log := &OperationLog{
		OpType:     opType,
		OpObject:   opObject,
		OpObjectID: objectID,
		OpUserID:   userID,
		OpResult:   result,
		ErrorMsg:   errorMsg,
		RequestIP:  requestIP,
	}
	if err := DB.Create(log).Error; err != nil {
		zap.L().Error("record operation log failed", zap.Error(err))
	}
}

// initBuiltInReviewRules 初始化内置评审规则（INSERT IGNORE）
func initBuiltInReviewRules() {
	rules := []ReviewRule{
		// --- common ---
		{Code: "common-sql-injection", Name: "SQL注入", Category: "security", Severity: "critical", Language: "common", IsEnabled: true, Description: "检查拼接SQL、未参数化查询", Prompt: "检查是否存在SQL注入漏洞。重点关注：\n1. 字符串拼接SQL查询\n2. 使用fmt.Sprintf构建SQL\n3. 未使用预编译语句的参数化查询\n4. ORM的Raw方法传入变量", SortOrder: 1},
		{Code: "common-hardcoded-secret", Name: "硬编码密钥", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "检查硬编码API密钥、密码、Token", Prompt: "检查是否存在硬编码的敏感信息：\n1. 字符串字面量中包含 'api_key', 'secret', 'password', 'token', 'private_key' 等关键词\n2. JWT签名密钥、数据库密码、云服务凭据\n3. 配置文件中明文存储的密钥\n4. 注释中泄露的敏感信息", SortOrder: 2},
		{Code: "common-xss-vulnerability", Name: "XSS漏洞", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "检查未转义的用户输入输出", Prompt: "检查是否存在XSS漏洞：\n1. 用户输入未转义直接输出到HTML\n2. 使用innerHTML插入不可信内容\n3. URL参数直接反射到页面\n4. 前端模板中未使用安全插值", SortOrder: 3},
		{Code: "common-unsafe-deserialization", Name: "不安全的反序列化", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "检查不安全的反序列化操作", Prompt: "检查是否存在不安全的反序列化：\n1. 反序列化不可信来源的数据\n2. 使用存在已知漏洞的序列化库\n3. 未对反序列化结果进行类型校验\n4. 使用pickle/反序列化执行不可信数据", SortOrder: 4},
		{Code: "common-resource-leak", Name: "资源泄露", Category: "performance", Severity: "high", Language: "common", IsEnabled: true, Description: "检查文件、连接未关闭", Prompt: "检查是否存在资源泄露：\n1. 文件打开后未关闭\n2. 数据库连接未释放\n3. 网络连接未关闭\n4. 锁未释放\n5. 内存未回收（循环引用等）", SortOrder: 5},
		{Code: "common-n-plus-one-query", Name: "N+1查询", Category: "performance", Severity: "medium", Language: "common", IsEnabled: true, Description: "检查循环体内数据库查询", Prompt: "检查是否存在N+1查询问题：\n1. 循环体内调用数据库查询\n2. ORM懒加载导致的隐式查询\n3. 批量操作未完成的地方\n4. 应该使用JOIN或IN查询的地方使用了多次查询", SortOrder: 6},
		{Code: "common-inefficient-loop", Name: "低效循环", Category: "performance", Severity: "medium", Language: "common", IsEnabled: true, Description: "检查O(n²)、重复计算", Prompt: "检查是否存在低效循环：\n1. 循环嵌套导致O(n²)或更高复杂度\n2. 循环体内重复计算不变值\n3. 不必要的循环（可用集合操作替代）\n4. 递归未优化或可能导致栈溢出", SortOrder: 7},
		{Code: "common-magic-number", Name: "魔法数字", Category: "readability", Severity: "low", Language: "common", IsEnabled: true, Description: "检查未命名的常量", Prompt: "检查是否存在魔法数字：\n1. 代码中直接使用的未命名数字常量\n2. 应该定义为具名常量的值\n3. 魔法字符串（重复出现的相同字符串字面量）", SortOrder: 8},
		{Code: "common-deep-nesting", Name: "嵌套过深", Category: "maintainability", Severity: "medium", Language: "common", IsEnabled: true, Description: "检查if/for嵌套>4层", Prompt: "检查是否存在嵌套过深的问题：\n1. if/for/while嵌套超过4层\n2. 回调地狱（多层嵌套回调）\n3. 应该抽取为函数的复杂嵌套逻辑\n4. 使用早期返回减少嵌套", SortOrder: 9},
		{Code: "common-too-long-function", Name: "函数过长", Category: "maintainability", Severity: "medium", Language: "common", IsEnabled: true, Description: "检查函数行数>100", Prompt: "检查是否存在函数过长的问题：\n1. 函数行数超过100行\n2. 函数职责不单一（应拆分为多个函数）\n3. 参数过多（超过5个）\n4. 圈复杂度过高", SortOrder: 10},

		// --- golang ---
		{Code: "go-error-handling", Name: "错误处理不当", Category: "maintainability", Severity: "medium", Language: "golang", IsEnabled: true, Description: "检查未wrap错误或裸返回", Prompt: "检查Go代码的错误处理是否符合最佳实践：\n1. 错误返回时是否使用了fmt.Errorf(\"...: %w\", err)进行wrap\n2. 是否避免了只写 `if err != nil { return err }` 而未添加上下文\n3. 是否在错误路径上记录了足够的信息\n4. 是否避免了panic/recover的错误处理模式", SortOrder: 11},
		{Code: "go-context-propagation", Name: "Context未传递", Category: "maintainability", Severity: "medium", Language: "golang", IsEnabled: true, Description: "检查context是否正确传递", Prompt: "检查Go代码的Context传递：\n1. 函数是否接收并传递了context.Context参数\n2. HTTP handlers是否正确使用request context\n3. 数据库操作是否传入了context\n4. 跨goroutine时context是否正确传播", SortOrder: 12},
		{Code: "go-goroutine-leak", Name: "Goroutine泄露", Category: "performance", Severity: "high", Language: "golang", IsEnabled: true, Description: "检查goroutine未正确退出", Prompt: "检查是否存在Goroutine泄露：\n1. 启动的goroutine是否都有退出条件\n2. 是否使用了sync.WaitGroup正确等待\n3. channel是否可能阻塞导致goroutine无法退出\n4. 是否使用了context取消信号", SortOrder: 13},
		{Code: "go-interface-compliance", Name: "接口实现未显式校验", Category: "readability", Severity: "low", Language: "golang", IsEnabled: true, Description: "检查接口实现是否显式声明", Prompt: "检查Go代码的接口实现：\n1. 是否使用了 `var _ Interface = (*Type)(nil)` 显式声明接口实现\n2. 接口定义是否清晰\n3. 接口方法数量是否合理（接口隔离原则）", SortOrder: 14},
		{Code: "go-concurrency-race", Name: "共享状态未保护", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "检查并发访问共享状态", Prompt: "检查Go代码的并发安全：\n1. 共享变量在多个goroutine中访问是否使用了sync.Mutex/RWMutex\n2. 是否使用了原子操作\n3. map在并发环境中是否安全\n4. 是否存在数据竞争", SortOrder: 15},
		{Code: "go-panic-recovery", Name: "不当使用panic", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "检查panic/recovery使用", Prompt: "检查Go代码中panic的使用：\n1. 是否在生产代码中不当使用panic\n2. 是否有必要的recover机制\n3. panic信息是否泄露了敏感信息\n4. 是否应该返回error而非panic", SortOrder: 16},
		{Code: "go-prepared-statement", Name: "未使用预编译", Category: "security", Severity: "medium", Language: "golang", IsEnabled: true, Description: "检查数据库预编译语句", Prompt: "检查Go代码的数据库操作：\n1. SQL查询是否使用了参数化查询/预编译语句\n2. 是否使用fmt.Sprintf拼接SQL\n3. ORM查询是否安全", SortOrder: 17},
		{Code: "go-struct-tag", Name: "JSON tag格式错误", Category: "readability", Severity: "low", Language: "golang", IsEnabled: true, Description: "检查struct tag格式", Prompt: "检查Go代码的struct tag：\n1. json tag是否使用正确的驼峰命名\n2. tag格式是否正确（无多余空格）\n3. omitempty使用是否恰当\n4. 是否遗漏了必要的tag", SortOrder: 18},
		{Code: "go-channel-close", Name: "Channel未正确关闭", Category: "performance", Severity: "medium", Language: "golang", IsEnabled: true, Description: "检查channel关闭逻辑", Prompt: "检查Go代码的channel使用：\n1. channel是否由发送方正确关闭\n2. 是否向已关闭的channel发送数据\n3. 是否重复关闭channel\n4. select语句是否处理了所有case", SortOrder: 19},
		{Code: "go-nil-pointer", Name: "潜在空指针访问", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "检查nil指针解引用", Prompt: "检查Go代码中的nil指针风险：\n1. 接口值为nil但底层类型非nil的情况\n2. 函数返回值未检查直接使用\n3. map查找结果直接访问\n4. 类型断言未检查ok值", SortOrder: 20},
		{Code: "go-string-concat-loop", Name: "循环内字符串拼接", Category: "performance", Severity: "low", Language: "golang", IsEnabled: true, Description: "检查循环中使用+拼接字符串", Prompt: "检查Go代码中的字符串拼接：\n1. 循环内是否使用了+拼接字符串（应使用strings.Builder）\n2. 大量字符串拼接是否使用了bytes.Buffer\n3. 格式化字符串是否使用了fmt.Sprintf（性能敏感场景）", SortOrder: 21},
		{Code: "go-defer-in-loop", Name: "循环内使用defer", Category: "performance", Severity: "medium", Language: "golang", IsEnabled: true, Description: "检查循环内defer资源泄漏", Prompt: "检查Go代码中defer的使用：\n1. 循环内使用defer可能导致资源延迟释放\n2. defer是否包裹了不应该defer的操作\n3. defer的执行顺序是否正确", SortOrder: 22},

		// --- python ---
		{Code: "py-bare-except", Name: "裸except", Category: "maintainability", Severity: "medium", Language: "python", IsEnabled: true, Description: "检查except未指定异常类型", Prompt: "检查Python代码的异常处理：\n1. 是否使用了裸except（应指定异常类型）\n2. except Exception是否过度宽泛\n3. 是否捕获了异常但未处理\n4. finally块是否正确使用", SortOrder: 23},
		{Code: "py-mutable-default-arg", Name: "可变默认参数", Category: "security", Severity: "high", Language: "python", IsEnabled: true, Description: "检查可变的默认参数值", Prompt: "检查Python函数默认参数：\n1. 是否使用了可变对象（list/dict）作为默认参数\n2. 默认参数是否在多次调用间共享状态\n3. 应使用None作为默认值并在函数内初始化", SortOrder: 24},
		{Code: "py-type-hint-missing", Name: "缺少类型注解", Category: "readability", Severity: "low", Language: "python", IsEnabled: true, Description: "检查函数缺少类型注解", Prompt: "检查Python代码的类型注解：\n1. 函数参数是否缺少类型注解\n2. 返回值类型是否标注\n3. 复杂类型是否使用了typing模块\n4. 类型注解是否准确", SortOrder: 25},
		{Code: "py-sql-string-format", Name: "SQL字符串格式化", Category: "security", Severity: "critical", Language: "python", IsEnabled: true, Description: "检查字符串格式化SQL", Prompt: "检查Python代码的SQL拼接：\n1. 是否使用字符串格式化（%, f-string, .format）拼接SQL\n2. 是否使用了参数化查询\n3. ORM查询是否安全\n4. 存储过程调用是否参数化", SortOrder: 26},
		{Code: "py-global-mutable", Name: "全局可变对象滥用", Category: "maintainability", Severity: "medium", Language: "python", IsEnabled: true, Description: "检查全局可变状态", Prompt: "检查Python代码的全局状态：\n1. 全局可变对象是否被多个函数修改\n2. 单例模式实现是否线程安全\n3. 模块级变量是否被意外修改\n4. 应使用函数参数传递而非全局状态", SortOrder: 27},
		{Code: "py-eval-exec", Name: "使用eval/exec", Category: "security", Severity: "critical", Language: "python", IsEnabled: true, Description: "检查危险的内置函数使用", Prompt: "检查Python代码中的危险函数：\n1. 是否使用了eval()或exec()处理用户输入\n2. 是否使用了compile() + exec\n3. subprocess或os.system是否拼接了用户输入\n4. 模板引擎中是否存在SSTI（服务器端模板注入）", SortOrder: 28},

		// --- frontend ---
		{Code: "frontend-xss-innerHTML", Name: "直接插入innerHTML", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "检查危险的DOM操作", Prompt: "检查前端代码的DOM操作：\n1. 是否直接使用innerHTML插入不可信内容\n2. 是否使用了document.write\n3. 是否对URL参数未过滤直接渲染\n4. React中是否使用了dangerouslySetInnerHTML", SortOrder: 29},
		{Code: "frontend-memory-leak", Name: "未清理事件监听", Category: "performance", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "检查组件卸载未清理", Prompt: "检查前端代码的内存管理：\n1. 组件卸载时是否清理了事件监听\n2. 定时器/setInterval是否在unmount时清除\n3. 订阅是否在销毁时取消\n4. 闭包是否持有过期引用", SortOrder: 30},
		{Code: "frontend-callback-hell", Name: "回调地狱", Category: "readability", Severity: "low", Language: "frontend", IsEnabled: true, Description: "检查嵌套回调", Prompt: "检查前端代码的异步处理：\n1. 是否存在多层嵌套回调（回调地狱）\n2. 是否使用了Promise/async-await替代\n3. Promise链是否过长\n4. 错误处理是否完善", SortOrder: 31},
		{Code: "react-missing-key", Name: "列表缺少key", Category: "performance", Severity: "low", Language: "frontend", IsEnabled: true, Description: "检查React列表渲染", Prompt: "检查React代码的列表渲染：\n1. map遍历是否提供了key属性\n2. key是否使用了稳定的唯一标识\n3. 是否使用了index作为key（不推荐）\n4. 列表项重排时key是否正确", SortOrder: 32},
		{Code: "vue-mutate-prop", Name: "直接修改props", Category: "maintainability", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "检查Vue props修改", Prompt: "检查Vue代码的props使用：\n1. 是否直接修改了props值\n2. 是否通过emit通知父组件更新\n3. 是否使用了computed处理派生状态\n4. 是否使用了v-model错误地修改prop", SortOrder: 33},
		{Code: "frontend-cors-misconfig", Name: "CORS配置过于宽松", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "检查CORS配置", Prompt: "检查前端/后端CORS配置：\n1. 是否允许了所有来源（*）\n2. 是否允许了危险的方法（PUT/DELETE）\n3. 是否暴露了敏感Header\n4. credentials配置是否正确", SortOrder: 34},
		{Code: "frontend-hardcoded-api-key", Name: "前端硬编码API Key", Category: "security", Severity: "critical", Language: "frontend", IsEnabled: true, Description: "检查前端泄露密钥", Prompt: "检查前端代码的密钥管理：\n1. 是否在前端代码中硬编码了API Key\n2. 是否将密钥提交到版本控制\n3. 环境变量是否正确使用\n4. 构建配置中是否泄露了敏感信息", SortOrder: 35},

		// --- java ---
		{Code: "java-null-pointer", Name: "NPE潜在风险", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "检查空指针风险", Prompt: "检查Java代码的NPE风险：\n1. Optional使用是否恰当\n2. 链式调用是否检查了中间null值\n3. 方法参数是否进行了null校验\n4. 集合操作是否检查了空值", SortOrder: 36},
		{Code: "java-resource-leak", Name: "未用try-with-resources", Category: "performance", Severity: "medium", Language: "java", IsEnabled: true, Description: "检查资源关闭", Prompt: "检查Java代码的资源管理：\n1. 是否使用了try-with-resources\n2. Closeable资源是否在finally中关闭\n3. 数据库连接是否及时归还\n4. 文件流是否正确关闭", SortOrder: 37},
		{Code: "java-concurrent-modification", Name: "并发修改异常", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "检查集合并发修改", Prompt: "检查Java代码的并发安全：\n1. 是否在迭代时修改了集合\n2. 并发环境是否使用了线程安全的集合\n3. 是否使用了CopyOnWriteArrayList等并发集合\n4. synchronized使用是否正确", SortOrder: 38},
		{Code: "java-string-concat-loop", Name: "循环内String拼接", Category: "performance", Severity: "medium", Language: "java", IsEnabled: true, Description: "检查低效字符串操作", Prompt: "检查Java代码的字符串操作：\n1. 循环内是否使用了+拼接String\n2. 是否使用了StringBuilder/StringBuffer\n3. 大量字符串拼接的场景优化", SortOrder: 39},
		{Code: "java-raw-type", Name: "使用泛型原始类型", Category: "maintainability", Severity: "low", Language: "java", IsEnabled: true, Description: "检查泛型使用", Prompt: "检查Java代码的泛型使用：\n1. 是否使用了原始类型（raw type）\n2. 泛型参数是否完整声明\n3. @SuppressWarnings是否必要\n4. 类型转换是否安全", SortOrder: 40},
		{Code: "java-transactional-misuse", Name: "事务注解使用不当", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "检查@Transactional使用", Prompt: "检查Java代码的事务管理：\n1. @Transactional是否在public方法上\n2. 事务传播行为是否恰当\n3. 事务边界是否合理\n4. 异常回滚配置是否正确", SortOrder: 41},
		{Code: "java-magic-number", Name: "魔法数字", Category: "readability", Severity: "low", Language: "java", IsEnabled: true, Description: "检查未命名常量", Prompt: "检查Java代码的常量使用：\n1. 是否存在魔法数字\n2. 是否使用了static final常量\n3. 枚举类型使用是否恰当\n4. 配置值是否提取到配置文件", SortOrder: 42},
		{Code: "java-singleton-race", Name: "单例模式并发问题", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "检查单例实现线程安全", Prompt: "检查Java代码的单例模式：\n1. 懒汉式单例是否线程安全\n2. 双重检查锁定是否正确\n3. 枚举单例是否被使用\n4. 单例状态是否被共享修改", SortOrder: 43},

		// ========== test_coverage（测试覆盖）全语言补充 ==========
		{Code: "common-missing-unit-test", Name: "缺少单元测试", Category: "test_coverage", Severity: "high", Language: "common", IsEnabled: true, Description: "新增业务逻辑未配套单元测试", Prompt: "检查本次变更是否缺少对应的单元测试：\n1. 新增的业务逻辑函数是否有测试覆盖\n2. Bug修复是否添加了回归测试\n3. 边界条件是否被测试\n4. 异常路径是否被测试\n5. 测试文件命名是否规范（*_test.go、test_*.py、*Test.java等）", SortOrder: 44},
		{Code: "common-low-test-assertion", Name: "测试断言不足", Category: "test_coverage", Severity: "medium", Language: "common", IsEnabled: true, Description: "测试仅验证 happy path 缺少断言", Prompt: "检查测试代码的断言质量：\n1. 是否只验证了正常路径而未检查错误路径\n2. 断言是否具体（避免过于宽泛的true/false判断）\n3. 是否验证了返回值的具体内容而非仅非空判断\n4. 副作用是否被验证（数据库状态、日志输出等）", SortOrder: 45},

		{Code: "go-test-table-driven", Name: "未使用表驱动测试", Category: "test_coverage", Severity: "low", Language: "golang", IsEnabled: true, Description: "Go测试应使用表驱动结构减少冗余", Prompt: "检查Go测试代码：\n1. 多个相似测试用例是否重复编写了测试函数\n2. 是否使用了表驱动测试（[]struct{name;input;want}）\n3. 子测试（t.Run）是否被使用\n4. 测试表格是否覆盖了边界值", SortOrder: 46},
		{Code: "go-test-race-unsafe", Name: "测试存在竞态风险", Category: "test_coverage", Severity: "medium", Language: "golang", IsEnabled: true, Description: "并发测试未使用-race或同步机制", Prompt: "检查Go并发相关测试：\n1. 并发测试是否使用了go test -race\n2. 并发原语（Mutex/channel）在测试中是否正确同步\n3. 共享变量在测试中是否被安全访问\n4. 是否使用了testing/quick或fuzzing测试并发场景", SortOrder: 47},

		{Code: "py-test-assert-count", Name: "测试用例过少", Category: "test_coverage", Severity: "medium", Language: "python", IsEnabled: true, Description: "核心业务缺少充分测试覆盖", Prompt: "检查Python测试代码：\n1. 核心业务函数是否有对应的测试\n2. 测试覆盖率是否充分（分支覆盖、条件覆盖）\n3. 是否使用了pytest的parametrize减少冗余\n4. fixture使用是否合理", SortOrder: 48},
		{Code: "py-test-mock-usage", Name: "Mock使用不当", Category: "test_coverage", Severity: "low", Language: "python", IsEnabled: true, Description: "Mock范围过宽或未验证调用", Prompt: "检查Python测试中Mock的使用：\n1. mock.patch是否过度宽泛（patch了整个模块而非具体方法）\n2. 是否验证了mock被正确调用（assert_called_with）\n3. mock的对象在测试后是否正确恢复\n4. 是否mock了不应被mock的内部实现", SortOrder: 49},

		{Code: "java-test-assert-all", Name: "未使用assertAll", Category: "test_coverage", Severity: "low", Language: "java", IsEnabled: true, Description: "JUnit5未使用assertAll批量断言", Prompt: "检查Java测试代码：\n1. JUnit5测试中多个独立断言是否使用了assertAll\n2. 断言失败时是否能得到完整结果而非第一个失败即终止\n3. 是否使用了适当的断言方法（assertThrows、assertIterableEquals等）\n4. @ParameterizedTest是否被使用", SortOrder: 50},
		{Code: "java-test-missing-timeout", Name: "测试缺少超时", Category: "test_coverage", Severity: "medium", Language: "java", IsEnabled: true, Description: "可能阻塞的测试未设置超时", Prompt: "检查Java测试代码：\n1. IO操作、异步调用的测试是否设置了超时（@Timeout或assertTimeout）\n2. 死锁风险的并发测试是否有超时保护\n3. 外部依赖调用的测试是否有超时控制\n4. 测试执行时间是否过长", SortOrder: 51},

		{Code: "frontend-test-missing", Name: "组件缺少测试", Category: "test_coverage", Severity: "high", Language: "frontend", IsEnabled: true, Description: "前端组件/Hook缺少单元测试", Prompt: "检查前端代码的测试覆盖：\n1. React/Vue组件是否有渲染测试\n2. 自定义Hook是否有行为测试\n3. 工具函数是否有单元测试\n4. 交互事件（点击、表单提交）是否被测试\n5. 是否使用了Testing Library而非Enzyme", SortOrder: 52},
		{Code: "frontend-test-async-await", Name: "异步测试处理不当", Category: "test_coverage", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "异步测试未等待或断言时机错误", Prompt: "检查前端异步测试：\n1. await waitFor/findBy等是否正确等待异步更新\n2. 断言是否在异步操作完成后执行\n3. async/await在测试中的使用是否正确\n4. 定时器测试是否使用了jest fake timers", SortOrder: 53},

		// ========== api_design（API设计）全语言补充 ==========
		{Code: "common-api-versioning", Name: "API缺少版本控制", Category: "maintainability", Severity: "medium", Language: "common", IsEnabled: true, Description: "公共API未做版本管理", Prompt: "检查API接口设计：\n1. 公共API是否包含版本标识（v1/v2路径或Header）\n2. 版本变更是否有兼容策略\n3. 废弃的API是否有明确的弃用计划\n4. 客户端是否被通知版本变更", SortOrder: 54},
		{Code: "common-api-break-compat", Name: "破坏性变更未标记", Category: "maintainability", Severity: "high", Language: "common", IsEnabled: true, Description: "修改了public接口未声明破坏性变更", Prompt: "检查本次变更是否为破坏性变更：\n1. 是否删除了public字段/方法/参数\n2. 是否修改了返回值的类型或结构\n3. 是否修改了错误码或异常类型\n4. 是否未提前通知下游团队", SortOrder: 55},

		{Code: "go-api-pagination", Name: "列表接口缺少分页", Category: "performance", Severity: "medium", Language: "golang", IsEnabled: true, Description: "未分页的列表查询可能OOM", Prompt: "检查Go的列表/查询接口：\n1. 返回数组的接口是否提供了分页参数\n2. 默认分页大小是否合理（防止过大）\n3. 是否有最大分页限制\n4. 游标分页vs偏移分页的选择是否恰当", SortOrder: 56},
		{Code: "go-api-idempotent", Name: "修改接口缺少幂等性", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "POST/PUT未实现幂等导致重复提交", Prompt: "检查Go的非安全HTTP方法：\n1. 创建/更新操作是否支持幂等键（Idempotency-Key）\n2. 重复请求是否会创建重复数据\n3. 支付/下单等关键操作是否幂等\n4. 是否使用了乐观锁或唯一索引防重", SortOrder: 57},

		{Code: "java-api-validation", Name: "入参缺少校验", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "Controller/Service入参未校验", Prompt: "检查Java的API入参校验：\n1. 是否使用了@Valid/@Validated进行参数校验\n2. 字符串长度、数值范围是否有约束\n3. DTO中是否缺少@NotBlank/@NotNull等注解\n4. 自定义校验规则是否覆盖业务约束\n5. 校验失败时错误响应是否友好", SortOrder: 58},
		{Code: "java-api-restful", Name: "不合规RESTful设计", Category: "readability", Severity: "low", Language: "java", IsEnabled: true, Description: "HTTP方法与资源路径不匹配", Prompt: "检查Java RESTful API设计：\n1. 是否使用了正确的HTTP方法（GET/POST/PUT/DELETE）\n2. URL路径是否使用名词复数（/users而非/getUsers）\n3. 状态码使用是否准确（201/204/400/401/403/404/409/422）\n4. 是否有合理的HATEOAS或API文档", SortOrder: 59},

		{Code: "py-api-validation", Name: "入参缺少校验", Category: "security", Severity: "high", Language: "python", IsEnabled: true, Description: "Flask/FastAPI/Django入参未严格校验", Prompt: "检查Python Web框架的入参校验：\n1. FastAPI是否使用了Pydantic模型约束字段\n2. Flask是否使用了 marshmallow/schema 校验\n3. 路径参数/查询参数是否有类型和范围限制\n4. 文件上传是否有大小和类型限制", SortOrder: 60},
		{Code: "py-api-rate-limit", Name: "接口缺少限流", Category: "security", Severity: "medium", Language: "python", IsEnabled: true, Description: "未配置速率限制导致暴力攻击", Prompt: "检查Python API的限流配置：\n1. 是否配置了全局或接口级速率限制\n2. 登录/注册等敏感接口是否有更严格的限制\n3. 限流响应是否正确（429状态码+Retry-After）\n4. 是否区分了认证用户和匿名用户", SortOrder: 61},

		// ========== logging（日志）全语言补充 ==========
		{Code: "common-sensitive-in-log", Name: "日志泄露敏感信息", Category: "security", Severity: "critical", Language: "common", IsEnabled: true, Description: "密码/Token/PII记录到日志", Prompt: "检查日志输出内容：\n1. 是否记录了密码、Token、信用卡号等PII\n2. 用户手机号、身份证号是否被记录\n3. 完整的请求体/响应体是否包含敏感字段\n4. 日志级别下调试信息是否在生产环境泄露\n5. 是否有日志脱敏或字段过滤机制", SortOrder: 62},
		{Code: "common-log-level-mismatch", Name: "日志级别与内容不符", Category: "readability", Severity: "low", Language: "common", IsEnabled: true, Description: "错误用info、调试用error", Prompt: "检查日志级别使用是否恰当：\n1. 预期的错误路径是否使用了error级别\n2. 调试信息是否混在info级别中\n3. 高频低价值日志是否使用了debug\n4. 致命错误是否使用了fatal/panic（避免滥用）", SortOrder: 63},
		{Code: "common-log-no-trace", Name: "缺少链路追踪ID", Category: "maintainability", Severity: "medium", Language: "common", IsEnabled: true, Description: "日志缺少request_id/trace_id", Prompt: "检查日志是否包含链路追踪信息：\n1. 每条日志是否包含request_id或trace_id\n2. 跨服务调用时trace_id是否正确传递\n3. 异步任务中trace_id是否保持一致\n4. 是否使用了结构化日志（JSON）便于聚合", SortOrder: 64},

		{Code: "go-log-sprintf-cost", Name: "日志使用Sprintf造成性能损失", Category: "performance", Severity: "low", Language: "golang", IsEnabled: true, Description: "log.Printf而非结构化logger", Prompt: "检查Go日志性能：\n1. 是否使用logrus/zap/slog等结构化logger\n2. 日志参数是否使用fmt.Sprintf预格式化（应避免）\n3. 高频日志是否使用了WithField/WithContext减少重复字段\n4. 日志级别检查是否在格式化之前", SortOrder: 65},

		{Code: "java-log-string-concat", Name: "日志参数拼接", Category: "performance", Severity: "medium", Language: "java", IsEnabled: true, Description: "log.info(\"a=\"+a)造成不必要的字符串构建", Prompt: "检查Java日志使用：\n1. 是否使用了SLF4J/Log4j2的占位符{}（避免字符串拼接）\n2. 是否在debug级别前进行了isDebugEnabled检查\n3. 异常堆栈是否被正确记录\n4. 日志配置是否与代码级别匹配", SortOrder: 66},

		{Code: "py-log-fstring-cost", Name: "日志使用f-string", Category: "performance", Severity: "low", Language: "python", IsEnabled: true, Description: "f-string在日志中无条件求值", Prompt: "检查Python日志性能：\n1. 是否使用了log.msg('format', arg)而非f-string/%\n2. 是否在debug日志前检查了logger.isEnabledFor\n3. 是否配置了structlog/json日志格式\n4. 异常信息是否被正确记录", SortOrder: 67},

		// ========== error_handling（错误处理）全语言补充 ==========
		{Code: "common-silent-error", Name: "错误被静默吞没", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "catch后未处理、未返回、未记录", Prompt: "检查代码中错误被静默处理的情况：\n1. catch/except后是否直接pass或空块\n2. 错误返回后调用方是否忽略\n3. defer中的错误是否被检查\n4. 异步操作的错误是否被await/catch\n5. 资源清理失败是否被忽略", SortOrder: 68},
		{Code: "common-error-info-leak", Name: "错误信息泄露内部细节", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "堆栈/SQL/路径暴露给用户", Prompt: "检查错误响应是否泄露敏感信息：\n1. 500错误响应是否包含堆栈跟踪\n2. SQL错误是否直接返回给客户端\n3. 文件路径、内部IP等是否暴露\n4. 是否对外统一了错误格式（对外隐藏细节）", SortOrder: 69},

		{Code: "py-except-pass", Name: "except后直接pass", Category: "maintainability", Severity: "medium", Language: "python", IsEnabled: true, Description: "异常被吞没无日志无告警", Prompt: "检查Python异常处理：\n1. except块中是否只有pass\n2. 是否捕获了异常但未记录日志\n3. 是否应该使用contextlib.suppress替代\n4. 异常信息是否被转换为无意义的返回值", SortOrder: 70},

		{Code: "java-swallow-exception", Name: "吞没异常堆栈", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "catch后只打印e.getMessage()", Prompt: "检查Java异常处理：\n1. catch块中是否只记录了e.getMessage()（丢失了堆栈）\n2. 是否使用了throw new RuntimeException(msg)而未传入cause\n3. 是否将异常转换为null/空值返回\n4. 是否在日志中完整包含了异常对象", SortOrder: 71},
		{Code: "java-generic-exception", Name: "抛出过度宽泛异常", Category: "maintainability", Severity: "medium", Language: "java", IsEnabled: true, Description: "throw new Exception()而非具体子类", Prompt: "检查Java异常类型：\n1. 是否抛出了过于宽泛的Exception/RuntimeException\n2. 是否定义了业务异常类（BusinessException）\n3. 异常层级是否清晰（checked vs unchecked）\n4. 是否应该使用标准异常（IllegalArgumentException等）", SortOrder: 72},

		{Code: "frontend-promise-unhandled", Name: "未处理Promise reject", Category: "security", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "异步错误未catch导致全局崩溃", Prompt: "检查前端异步错误处理：\n1. Promise链中是否有未处理的.catch\n2. async/await是否包裹了try-catch\n3. fetch/axios请求失败是否被处理\n4. window.onunhandledrejection是否有兜底", SortOrder: 73},
		{Code: "frontend-error-boundary-missing", Name: "缺少错误边界", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "React未包裹ErrorBoundary", Prompt: "检查前端错误边界：\n1. 关键组件树是否被ErrorBoundary包裹\n2. 错误边界中是否正确记录和上报错误\n3. 是否展示了用户友好的fallback UI\n4. 路由级别是否有错误边界保护", SortOrder: 74},

		// ========== documentation（文档）全语言补充 ==========
		{Code: "common-missing-doc", Name: "公共函数缺少文档", Category: "readability", Severity: "low", Language: "common", IsEnabled: true, Description: "导出/公共API缺少注释说明", Prompt: "检查公共API的文档完整性：\n1. 公共函数/方法是否有注释说明用途、参数、返回值\n2. 复杂业务逻辑是否有内联注释\n3. 接口变更是否同步更新了文档\n4. TODO注释是否有对应的issue或责任人", SortOrder: 75},
		{Code: "common-outdated-comment", Name: "注释与代码不符", Category: "maintainability", Severity: "medium", Language: "common", IsEnabled: true, Description: "修改代码后未同步更新注释", Prompt: "检查注释的准确性：\n1. 注释描述的行为是否与代码实际行为一致\n2. 参数类型或数量变更后注释是否更新\n3. 是否包含已废弃的代码注释\n4. 注释中提到的变量名是否已更名", SortOrder: 76},
		{Code: "common-todo-unresolved", Name: "遗留TODO未处理", Category: "maintainability", Severity: "low", Language: "common", IsEnabled: true, Description: "TODO/FIXME长期未解决", Prompt: "检查TODO/FIXME注释：\n1. 新增代码中的TODO是否有明确的完成计划\n2. 长期存在的TODO是否应该转为issue\n3. 临时解决方案是否有明确的修正时间\n4. HACK/WORKAROUND注释是否被记录", SortOrder: 77},

		{Code: "go-exported-doc", Name: "导出项缺少godoc", Category: "readability", Severity: "low", Language: "golang", IsEnabled: true, Description: "exported函数/类型无文档注释", Prompt: "检查Go代码的文档：\n1. 导出函数/类型/变量是否有以名称开头的注释\n2. 包级别是否有package注释\n3. 复杂函数是否说明了前置条件和副作用\n4. 示例代码是否被编写（ExampleTest）", SortOrder: 78},

		{Code: "java-javadoc-missing", Name: "公共API缺少Javadoc", Category: "readability", Severity: "low", Language: "java", IsEnabled: true, Description: "public方法缺少@param @return", Prompt: "检查Java代码的Javadoc：\n1. public/protected方法是否有Javadoc\n2. @param和@return是否完整\n3. @throws是否说明了可能抛出的异常\n4. 类级别是否有作者和职责说明", SortOrder: 79},

		{Code: "py-docstring-missing", Name: "函数缺少docstring", Category: "readability", Severity: "low", Language: "python", IsEnabled: true, Description: "公共函数无Google/NumPy风格docstring", Prompt: "检查Python代码的文档：\n1. 公共模块/函数/类是否有docstring\n2. 是否遵循了Google/NumPy/PEP257风格\n3. 类型信息是否在docstring中与类型注解一致\n4. __init__.py是否有模块说明", SortOrder: 80},

		// ========== security（安全）补充 ==========
		{Code: "common-ssrf-risk", Name: "SSRF风险", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "服务端请求伪造", Prompt: "检查是否存在SSRF漏洞：\n1. 是否允许用户输入控制URL并进行请求\n2. 是否访问了内部服务（localhost/169.254/内网IP）\n3. 是否做了URL白名单校验\n4. 重定向响应是否被追踪到内部地址", SortOrder: 81},
		{Code: "common-authz-bypass", Name: "权限绕过", Category: "security", Severity: "critical", Language: "common", IsEnabled: true, Description: "缺少鉴权或鉴权逻辑可被绕过", Prompt: "检查权限控制实现：\n1. 敏感接口是否进行了身份验证和授权\n2. 鉴权中间件是否在路由之前执行\n3. 是否可能存在IDOR（不安全的直接对象引用）\n4. 管理员接口是否有额外的权限校验\n5. JWT/Session验证是否被绕过", SortOrder: 82},
		{Code: "common-path-traversal", Name: "路径遍历", Category: "security", Severity: "critical", Language: "common", IsEnabled: true, Description: "用户输入拼接文件路径", Prompt: "检查文件路径处理：\n1. 是否拼接了用户输入到文件路径\n2. 是否过滤了../等路径穿越字符\n3. 是否限制在特定目录范围内访问\n4. 上传文件名是否被重命名（避免覆盖）", SortOrder: 83},
		{Code: "common-log-injection", Name: "日志注入", Category: "security", Severity: "medium", Language: "common", IsEnabled: true, Description: "用户输入写入日志导致伪造/污染", Prompt: "检查日志中的用户输入：\n1. 是否直接将用户输入写入日志（可能伪造日志条目）\n2. 换行符/n是否被过滤（防止多行日志注入）\n3. 日志解析器是否能区分注入内容\n4. 是否使用了结构化日志降低注入风险", SortOrder: 84},
		{Code: "common-insecure-hash", Name: "使用不安全的哈希算法", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "MD5/SHA1用于密码或签名", Prompt: "检查密码和敏感数据的哈希算法：\n1. 是否使用了MD5/SHA1处理密码（应使用bcrypt/argon2/scrypt）\n2. 数字签名是否使用了SHA-256或更高\n3. HMAC密钥是否足够长且随机\n4. 是否使用了salt（盐值）", SortOrder: 85},

		{Code: "go-crypto-insecure-rand", Name: "不安全的随机数生成", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "math/rand用于密码学场景", Prompt: "检查Go的随机数使用：\n1. 安全相关场景（Token/密钥/CSRF）是否使用了crypto/rand\n2. math/rand是否被用于安全目的\n3. 随机种子是否使用固定值（time.Now().Unix()）\n4. UUID生成是否使用了足够熵的库", SortOrder: 86},

		{Code: "py-insecure-crypto", Name: "使用弱加密算法", Category: "security", Severity: "high", Language: "python", IsEnabled: true, Description: "PyCryptodome/AES-ECB使用不当", Prompt: "检查Python加密使用：\n1. 是否使用了ECB模式（应使用CBC/GCM）\n2. 密钥是否硬编码或传输\n3. 是否使用了足够强度的算法（AES-256/ChaCha20）\n4. 哈希比较是否使用了hmac.compare_digest防时序攻击", SortOrder: 87},
		{Code: "py-ssrf-requests", Name: "requests访问不可信URL", Category: "security", Severity: "high", Language: "python", IsEnabled: true, Description: "requests.get(url)参数未校验", Prompt: "检查Python requests/urllib的URL：\n1. URL是否由用户输入拼接\n2. 是否允许访问内网地址\n3. 超时配置是否合理\n4. 重定向次数是否有限制", SortOrder: 88},

		{Code: "java-insecure-random", Name: "使用Random做安全用途", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "java.util.Random用于Token/密码", Prompt: "检查Java随机数使用：\n1. 安全场景是否使用了SecureRandom\n2. java.util.Random是否用于Token/CSRF/密码\n3. SecureRandom是否正确初始化\n4. 是否使用了UUID.randomUUID()用于安全标识", SortOrder: 89},
		{Code: "java-xxe-risk", Name: "XXE漏洞", Category: "security", Severity: "critical", Language: "java", IsEnabled: true, Description: "XML解析器未禁用外部实体", Prompt: "检查Java XML处理：\n1. DocumentBuilderFactory是否禁用了外部实体\n2. SAXParser是否配置了FEATURE_SECURE_PROCESSING\n3. Transformer是否禁用了DOCTYPE\n4. 是否使用了已知安全的XML库", SortOrder: 90},

		{Code: "frontend-csrf-missing", Name: "缺少CSRF保护", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "状态修改请求缺少CSRF Token", Prompt: "检查前端安全：\n1. 表单提交是否包含CSRF Token\n2. fetch/XHR的POST/PUT/DELETE是否携带Token\n3. SameSite Cookie属性是否正确设置\n4. 跨域写操作是否有额外保护", SortOrder: 91},
		{Code: "frontend-open-redirect", Name: "开放式重定向", Category: "security", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "返回URL参数未校验直接跳转", Prompt: "检查前端跳转逻辑：\n1. 登录后回调地址是否被校验\n2. window.location.href是否被不可信URL赋值\n3. 重定向目标是否在白名单内\n4. router.push是否被恶意构造的URL调用", SortOrder: 92},

		// ========== performance（性能）补充 ==========
		{Code: "common-synchronous-io-block", Name: "同步IO阻塞请求", Category: "performance", Severity: "medium", Language: "common", IsEnabled: true, Description: "请求线程等待外部IO未异步化", Prompt: "检查同步阻塞操作：\n1. 外部API调用是否阻塞了请求线程\n2. 文件IO/数据库查询是否可异步化\n3. 批量操作是否有异步队列处理\n4. 是否使用了连接池（HTTP/DB）", SortOrder: 93},
		{Code: "common-missing-cache", Name: "可缓存数据未做缓存", Category: "performance", Severity: "medium", Language: "common", IsEnabled: true, Description: "重复计算/查询未使用缓存", Prompt: "检查缓存策略：\n1. 频繁访问且变化少的数据是否被缓存\n2. 数据库查询结果是否有缓存层\n3. 缓存过期策略是否合理\n4. 缓存穿透/击穿/雪崩是否有防护", SortOrder: 94},
		{Code: "common-memory-hotspot", Name: "内存热点对象", Category: "performance", Severity: "medium", Language: "common", IsEnabled: true, Description: "大对象频繁创建导致GC压力", Prompt: "检查内存使用：\n1. 是否频繁创建大对象（大字符串/大数组）\n2. 对象池/复用是否被考虑\n3. 循环内是否创建了不必要的对象\n4. 内存泄漏模式（事件监听未清理等）", SortOrder: 95},

		{Code: "go-heap-escape", Name: "变量逃逸到堆", Category: "performance", Severity: "low", Language: "golang", IsEnabled: true, Description: "指针返回导致GC压力", Prompt: "检查Go内存分配模式：\n1. 小对象是否通过指针返回导致堆分配\n2. 值接收者vs指针接收者选择是否恰当\n3. 局部变量是否被闭包捕获导致逃逸\n4. 大切片是否预分配了容量", SortOrder: 96},
		{Code: "go-sync-map-inefficient", Name: "sync.Map使用不当", Category: "performance", Severity: "low", Language: "golang", IsEnabled: true, Description: "读写频繁场景应使用map+Mutex", Prompt: "检查Go sync.Map使用：\n1. 读写比是否适合sync.Map（大量读少量写才适合）\n2. 类型化的map是否被interface{}替代（类型断言开销）\n3. 是否清楚sync.Map的Load/Store语义\n4. 高频计数场景是否使用了原子操作", SortOrder: 97},

		{Code: "py-list-comprehension", Name: "循环可替换为推导式", Category: "readability", Severity: "low", Language: "python", IsEnabled: true, Description: "for+append可简化为list comprehension", Prompt: "检查Python代码简洁性：\n1. 循环+append是否可用list comprehension替代\n2. filter+map是否可用generator expression\n3. dict comprehension是否被使用\n4. 复杂逻辑是否应保留显式循环（可读性优先）", SortOrder: 98},
		{Code: "py-generator-lazy", Name: "大数据集未使用生成器", Category: "performance", Severity: "medium", Language: "python", IsEnabled: true, Description: "全部加载到内存应改为yield", Prompt: "检查Python大数据处理：\n1. 大文件/数据集是否使用了yield/generator\n2. 列表是否可替换为生成器表达式\n3. itertools是否被使用优化内存\n4. pandas处理大文件是否使用了chunksize", SortOrder: 99},

		{Code: "java-stream-inefficient", Name: "Stream API低效使用", Category: "performance", Severity: "low", Language: "java", IsEnabled: true, Description: "多次stream()可合并", Prompt: "检查Java Stream使用：\n1. 多次stream过滤是否可合并为一次\n2. 是否在高频循环内创建stream\n3. parallelStream是否被滥用（线程安全问题）\n4. Optional.isPresent()+get()是否被正确使用", SortOrder: 100},
		{Code: "java-boxing-unboxing", Name: "装箱拆箱性能损耗", Category: "performance", Severity: "low", Language: "java", IsEnabled: true, Description: "基本类型与包装类混用", Prompt: "检查Java基本类型使用：\n1. 数值运算是否频繁装箱拆箱\n2. Map<Integer, Integer>是否可用Int2IntMap替代\n3. ArrayList<Integer> vs int[]性能差异\n4. Stream<Integer>是否可替换为IntStream", SortOrder: 101},

		{Code: "frontend-unnecessary-rerender", Name: "不必要的重渲染", Category: "performance", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "state/props变更导致全树渲染", Prompt: "检查前端渲染性能：\n1. React中是否使用了React.memo/useMemo/useCallback避免不必要重渲染\n2. Vue中是否过度使用了深度响应式\n3. 大列表是否使用了虚拟滚动\n4. 第三方库组件是否被不必要地重新创建", SortOrder: 102},
		{Code: "frontend-bundle-size", Name: "引入过大依赖", Category: "performance", Severity: "low", Language: "frontend", IsEnabled: true, Description: "引入moment/full lodash等大包", Prompt: "检查前端包大小：\n1. 是否引入了完整的lodash而非lodash/debounce\n2. moment.js是否可替换为date-fns/dayjs\n3. 是否引入了未使用的UI组件\n4. 图标库是否按需引入", SortOrder: 103},

		// ========== readability（可读性）补充 ==========
		{Code: "common-ambiguous-name", Name: "命名语义不清", Category: "readability", Severity: "low", Language: "common", IsEnabled: true, Description: "变量/函数名无法表达意图（a1/tmp/do）", Prompt: "检查命名质量：\n1. 变量名是否能表达其含义（避免a/b/x/i/j无意义命名）\n2. 函数名是否说明了做什么（动词+名词）\n3. 布尔变量是否使用了疑问句式（is/has/should/can）\n4. 缩写是否过于晦涩", SortOrder: 104},
		{Code: "common-dead-code", Name: "死代码/未使用变量", Category: "readability", Severity: "low", Language: "common", IsEnabled: true, Description: "未引用的变量/函数/导入", Prompt: "检查代码是否存在死代码：\n1. 未使用的变量、参数、导入\n2. 未调用的私有函数\n3. 注释掉的大段代码\n4. 不可达的分支（永远false的条件）\n5. 重复的模板代码", SortOrder: 105},
		{Code: "common-inconsistent-style", Name: "代码风格不一致", Category: "readability", Severity: "info", Language: "common", IsEnabled: true, Description: "缩进/引号/命名规范与同项目冲突", Prompt: "检查代码风格一致性：\n1. 缩进（空格/Tab）是否与项目一致\n2. 引号风格（单/双）是否一致\n3. 命名规范（camel/snake/Pascal）是否一致\n4. 括号风格是否与项目统一\n5. import/order是否与项目lint规则一致", SortOrder: 106},

		{Code: "go-vet-shadow", Name: "变量遮蔽", Category: "readability", Severity: "low", Language: "golang", IsEnabled: true, Description: "内层变量与外层同名导致歧义", Prompt: "检查Go变量遮蔽：\n1. if简短语句中声明的变量是否与外部同名\n2. for循环变量是否在goroutine中被错误捕获\n3. 同名变量赋值是否导致意外行为\n4. 使用go vet -shadow检查结果", SortOrder: 107},
		{Code: "go-naked-return", Name: "裸返回可读性差", Category: "readability", Severity: "low", Language: "golang", IsEnabled: true, Description: "return未带变量名降低可读性", Prompt: "检查Go裸返回：\n1. 命名返回值是否被裸返回（不带变量名）\n2. 函数较长时裸返回是否导致理解困难\n3. return语句是否明确表达了返回意图\n4. 错误路径返回是否清晰", SortOrder: 108},

		{Code: "py-pep8-violation", Name: "PEP8风格违规", Category: "readability", Severity: "info", Language: "python", IsEnabled: true, Description: "行宽/空行/导入排序等PEP8规范", Prompt: "检查Python代码风格：\n1. 行宽是否超过79/100/120字符（根据项目约定）\n2. 导入排序是否符合isort规范\n3. 空行使用是否恰当（函数间两空行）\n4. 命名是否遵循PEP8（snake_case for function）\n5. 字符串引号风格是否一致", SortOrder: 109},

		{Code: "java-naming-convention", Name: "命名规范违规", Category: "readability", Severity: "low", Language: "java", IsEnabled: true, Description: "类名/方法名/常量未按Java规范", Prompt: "检查Java命名规范：\n1. 类名是否使用UpperCamelCase\n2. 方法/变量是否使用lowerCamelCase\n3. 常量是否使用UPPER_SNAKE_CASE\n4. 包名是否使用小写\n5. 布尔方法是否使用is/has/can前缀", SortOrder: 110},

		{Code: "frontend-props-drilling", Name: "过度Props透传", Category: "readability", Severity: "low", Language: "frontend", IsEnabled: true, Description: "超过3层props传递应使用Context", Prompt: "检查前端组件通信：\n1. props是否被透传超过3层\n2. 是否适合使用Context/Redux/Zustand替代\n3. 中间组件是否接收了不用的props只为传递\n4. 组合模式（composition）是否可替代props传递", SortOrder: 111},
		{Code: "frontend-mixed-import-style", Name: "导入风格混杂", Category: "readability", Severity: "info", Language: "frontend", IsEnabled: true, Description: "绝对/相对路径import混用", Prompt: "检查前端import风格：\n1. 绝对路径和相对路径是否混用（应统一）\n2. 第三方库import是否在自定义import之前\n3. 未使用的import是否被清理\n4. 是否使用了路径别名（@/components）", SortOrder: 112},

		// ========== Phase 1 新增：通用规则 (12条) ==========
		{Code: "common-command-injection", Name: "命令注入", Category: "security", Severity: "critical", Language: "common", IsEnabled: true, Description: "用户输入拼接到系统命令执行", Prompt: "检查是否存在命令注入漏洞：\n1. 是否将用户输入拼接到系统命令执行（exec/system/subprocess）\n2. 是否对用户输入中的shell元字符未过滤（; && | ` $）\n3. 是否使用了参数列表而非字符串拼接传递命令\n4. 是否对命令参数做了白名单校验", SortOrder: 113},
		{Code: "common-ldap-injection", Name: "LDAP注入", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "用户输入拼接到LDAP查询", Prompt: "检查是否存在LDAP注入漏洞：\n1. 是否将用户输入拼接到LDAP过滤器或DN\n2. 是否对特殊字符（* ( ) \\）做了转义\n3. 是否使用了参数化的LDAP查询库\n4. 是否对查询结果集的size进行了限制", SortOrder: 114},
		{Code: "common-nosql-injection", Name: "NoSQL注入", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "MongoDB等NoSQL查询注入", Prompt: "检查是否存在NoSQL注入漏洞：\n1. 是否将用户输入拼接到NoSQL查询条件（MongoDB、Redis等）\n2. 是否对用户输入中的操作符（$gt, $ne, $where）做了过滤\n3. 查询条件是否使用了参数化/绑定变量\n4. 聚合管道是否拼接了用户输入", SortOrder: 115},
		{Code: "common-http-response-split", Name: "HTTP响应拆分", Category: "security", Severity: "medium", Language: "common", IsEnabled: true, Description: "用户输入污染HTTP响应头", Prompt: "检查是否存在HTTP响应拆分漏洞：\n1. 是否将用户输入拼接到HTTP响应头（Location、Set-Cookie等）\n2. 是否对换行符（\\r\\n）做了过滤\n3. 重定向或设置Cookie时是否校验了参数\n4. 是否使用了框架提供的安全API设置头", SortOrder: 116},
		{Code: "common-clickjacking", Name: "点击劫持风险", Category: "security", Severity: "medium", Language: "common", IsEnabled: true, Description: "未配置X-Frame-Options/CSP", Prompt: "检查是否有点击劫持防护：\n1. HTTP响应头是否包含X-Frame-Options（DENY/SAMEORIGIN）\n2. CSP帧祖先指令（frame-ancestors）是否正确配置\n3. 敏感页面是否允许被嵌套在iframe中\n4. 框架/模板是否默认添加了防护头", SortOrder: 117},
		{Code: "common-insecure-cookie", Name: "Cookie安全标志缺失", Category: "security", Severity: "medium", Language: "common", IsEnabled: true, Description: "未设置HttpOnly/Secure/SameSite", Prompt: "检查Cookie安全属性配置：\n1. 敏感Cookie是否设置了HttpOnly（禁止JS访问）\n2. 生产环境Cookie是否设置了Secure（仅HTTPS传输）\n3. SameSite属性是否配置为Strict或Lax\n4. 会话Cookie的生命周期是否合理", SortOrder: 118},
		{Code: "common-integer-overflow", Name: "整数溢出风险", Category: "security", Severity: "medium", Language: "common", IsEnabled: true, Description: "数值计算可能导致溢出", Prompt: "检查是否存在整数溢出风险：\n1. 数值计算是否可能导致超出类型最大值/最小值\n2. 用户输入的数值是否在运算前做了范围校验\n3. 语言是否提供了安全数学运算库\n4. 类型转换（如int转short）是否可能截断", SortOrder: 119},
		{Code: "common-redos", Name: "ReDoS正则DOS", Category: "security", Severity: "medium", Language: "common", IsEnabled: true, Description: "正则表达式存在灾难性回溯", Prompt: "检查是否存在ReDoS（正则表达式拒绝服务）风险：\n1. 正则中是否存在（a+）+、（a|a）*等嵌套量词\n2. 是否使用用户输入构造正则\n3. 正则匹配是否有超时机制\n4. 是否可用字符串操作替代复杂正则", SortOrder: 120},
		{Code: "common-sensitive-data-unencrypted", Name: "敏感数据未加密存储", Category: "security", Severity: "critical", Language: "common", IsEnabled: true, Description: "密码/身份证/手机号明文存储", Prompt: "检查敏感数据的存储安全：\n1. 密码是否使用bcrypt/argon2/scrypt哈希（非MD5/SHA1）\n2. 身份证号/手机号/银行卡号是否加密存储非明文\n3. 密钥/加密配置是否单独管理\n4. 数据库备份中敏感字段是否同样加密", SortOrder: 121},
		{Code: "common-batch-operation-missing", Name: "缺少批量操作", Category: "performance", Severity: "medium", Language: "common", IsEnabled: true, Description: "循环内逐条操作数据库/网络", Prompt: "检查是否存在大量单条操作可优化为批量：\n1. 循环内是否每次迭代都发起数据库查询/写入\n2. 网络调用是否可合并为批量接口\n3. 是否使用了ORM的bulk_create/bulk_update\n4. 批量大小的选择是否合理（避免过大导致超时）", SortOrder: 122},
		{Code: "common-race-condition", Name: "竞态条件", Category: "security", Severity: "high", Language: "common", IsEnabled: true, Description: "非原子性的检查-执行操作", Prompt: "检查是否存在竞态条件（TOCTOU）：\n1. 是否存在先检查后执行的非原子操作（如if exists then delete）\n2. 并发环境下对共享状态的读写是否同步\n3. 文件操作中是否存在检查时和打开时状态不一致\n4. 是否可使用原子操作/事务/锁消除竞态", SortOrder: 123},
		{Code: "common-tight-coupling", Name: "模块耦合度过高", Category: "maintainability", Severity: "low", Language: "common", IsEnabled: true, Description: "模块间直接依赖导致难以修改", Prompt: "检查模块间耦合程度：\n1. 模块是否直接依赖了其他模块的具体实现（非接口）\n2. 是否可能出现修改A模块导致B/C/D模块需要跟着改\n3. 是否使用了依赖注入/接口解耦\n4. 导入关系是否成环（循环依赖）", SortOrder: 124},

		// ========== Phase 1 新增：Go专属 (20条) ==========
		{Code: "go-unsafe-pointer", Name: "不当使用unsafe.Pointer", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "绕过类型安全检查", Prompt: "检查Go代码中unsafe.Pointer的使用：\n1. unsafe.Pointer是否用于性能优化而非必要的内存操作\n2. 是否清楚unsafe.Pointer不是类型安全的（指针类型可互转）\n3. 转换后是否可能导致GC误回收或内存踩踏\n4. 是否有同等的类型安全写法可以替代", SortOrder: 125},
		{Code: "go-temp-file-race", Name: "临时文件竞争条件", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "ioutil.TempFile使用不当", Prompt: "检查Go临时文件安全性：\n1. 临时文件名是否包含不可预测的后缀（io/ioutil生成的随机名）\n2. 创建临时文件后是否及时设置了合适的文件权限（os.Chmod）\n3. 临时文件目录是否可被其他用户写入（安全检查）\n4. 临时文件是否在使用后及时清理（defer删除）", SortOrder: 126},
		{Code: "go-http-server-timeout", Name: "HTTP服务器缺少超时", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "http.Server未配置读写超时", Prompt: "检查Go HTTP服务器的超时配置：\n1. http.Server是否设置了ReadTimeout和WriteTimeout\n2. 是否配置了IdleTimeout防止闲置连接占用资源\n3. 处理大文件上传的接口是否有合理的MaxHeaderBytes\n4. 是否存在慢读攻击或慢写攻击的风险", SortOrder: 127},
		{Code: "go-sql-null-scan", Name: "数据库NULL值未处理", Category: "security", Severity: "medium", Language: "golang", IsEnabled: true, Description: "扫描NULL到基本类型导致panic", Prompt: "检查Go的数据库NULL值处理：\n1. 数据库字段是否可能为NULL\n2. Scan到基本类型（string/int/float64）时是否可能panic\n3. 是否应使用sql.NullString/sql.NullInt64等包装类型\n4. 从ORM查询后是否对可能为空的字段做了校验", SortOrder: 128},
		{Code: "go-map-concurrent-write", Name: "并发写Map", Category: "security", Severity: "critical", Language: "golang", IsEnabled: true, Description: "多个goroutine同时写map", Prompt: "检查Go map的并发安全：\n1. 多个goroutine是否可能同时读写同一个map\n2. 是否使用了sync.RWMutex保护map读写\n3. 读多写少场景是否使用了sync.Map\n4. 并发迭代map时是否遵循了语言规范要求", SortOrder: 129},
		{Code: "go-http-body-close", Name: "HTTP响应体未关闭", Category: "performance", Severity: "medium", Language: "golang", IsEnabled: true, Description: "resp.Body未关闭导致连接泄露", Prompt: "检查Go HTTP响应体的关闭：\n1. http.Response的Body是否被正确关闭（defer resp.Body.Close()）\n2. 是否只在err != nil后才处理Body\n3. 是否读取了Body内容后再关闭\n4. Body.Close()返回的错误是否被忽略", SortOrder: 130},
		{Code: "go-json-unmarshal-interface", Name: "JSON反序列化到interface{}", Category: "security", Severity: "medium", Language: "golang", IsEnabled: true, Description: "可能解析为float64丢失精度", Prompt: "检查Go的JSON反序列化安全性：\n1. json.Unmarshal到interface{}时数字是否被解析为float64\n2. 大整数是否因float64导致精度丢失\n3. 是否应使用json.Number或明确定义structs接收\n4. 不受信的JSON数据是否做了严格的结构校验", SortOrder: 131},
		{Code: "go-buffer-pool-reuse", Name: "bytes.Buffer未重置复用", Category: "performance", Severity: "low", Language: "golang", IsEnabled: true, Description: "sync.Pool取出Buffer未Reset", Prompt: "检查Go sync.Pool中Buffer的重置：\n1. 从pool取出bytes.Buffer后是否调用了Reset()\n2. Buffer是否可能残留上一次使用的内容\n3. 放回pool前是否已清空\n4. 是否清楚sync.Pool中对象可能被GC随时回收", SortOrder: 132},
		{Code: "go-ctx-cancel-check", Name: "未检查Context取消", Category: "performance", Severity: "medium", Language: "golang", IsEnabled: true, Description: "长时间运行未检查ctx.Done()", Prompt: "检查Go的Context取消检查：\n1. 长时间运行的循环/操作是否检查了ctx.Done()\n2. IO操作（数据库/HTTP）是否传入了context.Context\n3. 是否忽略了context.DeadlineExceeded或context.Canceled\n4. 级联调用是否正确传递了ctx", SortOrder: 133},
		{Code: "go-range-pointer-capture", Name: "range循环变量指针捕获", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "goroutine中引用range变量", Prompt: "检查Go range循环的闭包捕获：\n1. goroutine或defer中是否引用了range变量指针\n2. range中的变量是否被所有迭代共享（地址不变）\n3. 是否将range值显式复制到局部变量后再使用\n4. 方法值（method value）是否在range中按预期绑定", SortOrder: 134},
		{Code: "go-os-exec-injection", Name: "命令执行注入", Category: "security", Severity: "critical", Language: "golang", IsEnabled: true, Description: "exec.Command参数拼接用户输入", Prompt: "检查Go命令执行安全：\n1. exec.Command的参数是否包含用户输入\n2. 是否对输入中的shell元字符做了过滤\n3. 是否使用了参数列表而非字符串拼接\n4. 是否限制了可执行的命令白名单", SortOrder: 135},
		{Code: "go-httputil-reverseproxy-misuse", Name: "反向代理配置风险", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "X-Forwarded-*未校验", Prompt: "检查Go反向代理配置：\n1. httputil.ReverseProxy是否正确配置Transport\n2. X-Forwarded-For/X-Real-IP是否伪造风险\n3. 是否暴露了内部服务端口或路径\n4. 代理超时和错误处理是否完善", SortOrder: 136},
		{Code: "go-slice-alias-bug", Name: "切片别名导致的修改泄露", Category: "maintainability", Severity: "medium", Language: "golang", IsEnabled: true, Description: "append后底层数组共享", Prompt: "检查Go的切片别名问题：\n1. append操作是否可能导致原始切片被意外修改\n2. 切片的子切片修改是否污染了父切片\n3. 函数返回内部切片的子切片是否暴露了内部状态\n4. 需要独立拷贝的场景是否使用了copy或append到nil切片", SortOrder: 137},
		{Code: "go-time-parse-layout", Name: "时间格式解析错误", Category: "maintainability", Severity: "low", Language: "golang", IsEnabled: true, Description: "使用错误的时间布局字符串", Prompt: "检查Go时间格式解析：\n1. time.Parse/time.Format是否使用了Go特有的布局字符串（2006-01-02 15:04:05）\n2. 布局字符串是否与输入格式匹配\n3. 时区处理是否正确（time.UTC/time.Local）\n4. 是否误用了其他语言的日期格式占位符", SortOrder: 138},
		{Code: "go-init-order-risk", Name: "初始化顺序依赖", Category: "maintainability", Severity: "low", Language: "golang", IsEnabled: true, Description: "init函数间存在隐式依赖", Prompt: "检查Go的init函数依赖：\n1. 多个init函数之间是否存在隐式依赖\n2. init中是否调用了可能失败的外部操作\n3. 包初始化顺序是否清晰可控\n4. init中panic是否会导致程序启动失败", SortOrder: 139},
		{Code: "go-recover-misuse", Name: "recover位置不当", Category: "security", Severity: "high", Language: "golang", IsEnabled: true, Description: "recover未放在defer函数中", Prompt: "检查Go recover的使用：\n1. recover是否只在defer函数中调用\n2. recover是否捕获了不该捕获的panic\n3. recover后是否记录了足够的上下文信息\n4. 是否有在goroutine外recover但goroutine内panic未处理", SortOrder: 140},
		{Code: "go-select-default-busy-loop", Name: "select default导致忙等待", Category: "performance", Severity: "medium", Language: "golang", IsEnabled: true, Description: "无阻塞case+default形成CPU空转", Prompt: "检查Go select语句的使用：\n1. select中是否所有case都被default覆盖且default无阻塞操作\n2. 是否在循环内使用select+default形成忙等待\n3. 是否应在default中加入time.Sleep或Ticker\n4. 是否有比select+default更合适的同步原语", SortOrder: 141},
		{Code: "go-reflect-performance", Name: "反射性能损耗", Category: "performance", Severity: "low", Language: "golang", IsEnabled: true, Description: "高频路径使用reflect慢10-100倍", Prompt: "检查Go反射的性能影响：\n1. 高频路径（每次请求/循环迭代）是否使用了reflect\n2. 是否可以用codegen/模板/类型参数替代reflect\n3. json序列化/反序列化是否在热路径中\n4. 反射获取的Method是否被缓存复用", SortOrder: 142},
		{Code: "go-duplicate-error-check", Name: "重复错误检查", Category: "readability", Severity: "info", Language: "golang", IsEnabled: true, Description: "连续多次检查同一错误", Prompt: "检查Go的冗余错误处理：\n1. 是否对同一err连续多次if err != nil检查\n2. 是否可用 early return 减少嵌套\n3. 错误处理逻辑是否被重复提取而未复用\n4. 多步骤操作是否使用了 errs = append(errs, err) 模式", SortOrder: 143},
		{Code: "go-inefficient-map-lookup", Name: "多次Map查找未优化", Category: "performance", Severity: "low", Language: "golang", IsEnabled: true, Description: "先查询存在再取值应合并", Prompt: "检查Go map操作的效率：\n1. 是否先if _, ok := m[k]; ok再v := m[k]\n2. 是否可合并为单次查找 if v, ok := m[k]; ok\n3. 高频查找的map是否预分配了足够容量\n4. 键类型选择是否恰当（string vs struct）", SortOrder: 144},

		// ========== Phase 1 新增：Python专属 (17条) ==========
		{Code: "py-circular-import", Name: "循环导入", Category: "maintainability", Severity: "medium", Language: "python", IsEnabled: true, Description: "模块间循环import", Prompt: "检查Python的导入关系：\n1. 模块A导入B、B又导入A的循环导入\n2. 是否在模块顶层（非函数内）做了导入\n3. 是否可通过依赖注入或接口解耦\n4. 是否延迟导入（函数内import）解决循环", SortOrder: 145},
		{Code: "py-sqlalchemy-n-plus-one", Name: "ORM懒加载N+1", Category: "performance", Severity: "medium", Language: "python", IsEnabled: true, Description: "SQLAlchemy关系未预取", Prompt: "检查SQLAlchemy的N+1查询：\n1. 遍历relationship时是否触发了额外的select\n2. 是否使用了joinedload/selectinload/raiseload预取\n3. 是否配置了lazy='raise'主动发现N+1\n4. 分页查询后遍历关系数据的场景", SortOrder: 146},
		{Code: "py-path-traversal-ospath", Name: "路径遍历风险", Category: "security", Severity: "high", Language: "python", IsEnabled: true, Description: "os.path.join用户输入未校验", Prompt: "检查Python的文件路径安全：\n1. 是否将用户输入拼接到os.path.join/pathlib.Path\n2. 是否对路径进行了规范化处理（os.path.normpath）\n3. 是否限制访问目录在白名单内\n4. 是否过滤了..和绝对路径前缀", SortOrder: 147},
		{Code: "py-subprocess-shell-true", Name: "subprocess使用shell=True", Category: "security", Severity: "critical", Language: "python", IsEnabled: true, Description: "shell=True参数导致命令注入", Prompt: "检查Python subprocess安全：\n1. subprocess调用是否使用了shell=True\n2. shell=True时命令字符串是否拼接了用户输入\n3. 是否可使用参数列表（shell=False）替代\n4. 是否做了命令白名单校验", SortOrder: 148},
		{Code: "py-pickle-untrusted", Name: "反序列化不可信pickle数据", Category: "security", Severity: "critical", Language: "python", IsEnabled: true, Description: "pickle.load处理不可信数据可RCE", Prompt: "检查Python pickle使用安全：\n1. pickle.load/pickle.loads是否处理来自网络/用户的数据\n2. 是否使用了更安全的序列化格式（json/msgpack）\n3. 反序列化来源是否经过签名/加密校验\n4. 是否有防止任意代码执行的沙箱机制", SortOrder: 149},
		{Code: "py-requests-no-timeout", Name: "HTTP请求缺少超时", Category: "performance", Severity: "medium", Language: "python", IsEnabled: true, Description: "requests/get/post未设置timeout", Prompt: "检查Python requests的超时配置：\n1. requests.get/post是否设置了timeout参数\n2. 超时值是否合理（连接超时和读取超时）\n3. 是否重试机制配置了指数退避\n4. 慢响应接口是否导致线程/协程堆积", SortOrder: 150},
		{Code: "py-django-raw-sql", Name: "Django原始SQL注入", Category: "security", Severity: "critical", Language: "python", IsEnabled: true, Description: "raw/extra使用字符串格式化", Prompt: "检查Django原始SQL安全：\n1. Manager.raw/extra是否使用了字符串格式化（%、f-string、.format）\n2. 是否使用了参数化查询传递参数\n3. 动态排序/表名是否做了白名单校验\n4. ORM查询是否优先使用ORM API（filter/exclude）", SortOrder: 151},
		{Code: "py-flask-debug-mode", Name: "Flask调试模式生产环境未关闭", Category: "security", Severity: "high", Language: "python", IsEnabled: true, Description: "DEBUG=True暴露Werkzeug调试器", Prompt: "检查Flask生产配置：\n1. DEBUG配置是否从环境变量读取（非硬编码True）\n2. 生产环境是否设置了DEBUG=False\n3. 是否暴露了SECRET_KEY等敏感配置\n4. Werkzeug调试器是否在生产环境启用", SortOrder: 152},
		{Code: "py-password-hardcoded", Name: "硬编码数据库密码", Category: "security", Severity: "high", Language: "python", IsEnabled: true, Description: "数据库连接字符串包含明文密码", Prompt: "检查Python中的硬编码凭据：\n1. 数据库连接字符串是否包含明文密码\n2. API密钥/Token是否硬编码在源码中\n3. 是否使用了环境变量或密钥管理服务\n4. 测试代码中是否有弱密码/默认密码", SortOrder: 153},
		{Code: "py-mutable-class-variable", Name: "类级别可变变量", Category: "security", Severity: "medium", Language: "python", IsEnabled: true, Description: "类属性list/dict被所有实例共享", Prompt: "检查Python类变量的可变默认值：\n1. 类属性是否使用了[]、{}等可变对象作为默认值\n2. 实例修改类属性是否影响了其他实例\n3. 是否应在__init__中初始化实例属性\n4. 是否使用了frozenset/tuple等不可变类型", SortOrder: 154},
		{Code: "py-decorator-misuse", Name: "装饰器使用不当", Category: "maintainability", Severity: "low", Language: "python", IsEnabled: true, Description: "@装饰器未正确保留函数元信息", Prompt: "检查Python装饰器：\n1. 自定义装饰器是否使用了@functools.wraps保留元信息\n2. 装饰器是否丢失了函数的docstring和签名\n3. 装饰器参数（带参数装饰器）实现是否正确\n4. 类装饰器与函数装饰器的区分是否恰当", SortOrder: 155},
		{Code: "py-async-blocking-call", Name: "异步函数中调用阻塞操作", Category: "performance", Severity: "high", Language: "python", IsEnabled: true, Description: "async中调用time.sleep/requests", Prompt: "检查Python异步代码中的阻塞调用：\n1. async def中是否调用了阻塞API（time.sleep、requests、open）\n2. 是否使用了对应的异步库（aiohttp、aiofiles）\n3. 是否在await中混用了线程池（asyncio.to_thread）\n4. 事件循环是否被阻塞导致并发度下降", SortOrder: 156},
		{Code: "py-type-cast-risk", Name: "不安全的类型转换", Category: "security", Severity: "medium", Language: "python", IsEnabled: true, Description: "int()/float()转换未处理异常", Prompt: "检查Python类型转换安全：\n1. int()/float()/str()转换是否可能抛出ValueError/TypeError\n2. 用户输入转换成数字时是否做了try-except\n3. 列表索引强制转换int是否可能失败\n4. 是否区分了可为None和必须转换的场景", SortOrder: 157},
		{Code: "py-staticmethod-where-classmethod", Name: "应使用classmethod", Category: "readability", Severity: "low", Language: "python", IsEnabled: true, Description: "staticmethod需要类信息时应改", Prompt: "检查Python方法装饰器选择：\n1. staticmethod中是否引用了类名（应改为classmethod）\n2. 是否需要继承链中的cls引用\n3. 是否清楚staticmethod、classmethod、普通方法的区别\n4. 工厂模式创建实例是否使用了classmethod", SortOrder: 158},
		{Code: "py-iter-mutate-list", Name: "迭代时修改列表", Category: "security", Severity: "medium", Language: "python", IsEnabled: true, Description: "for循环中删除/添加列表元素", Prompt: "检查Python迭代时修改序列：\n1. for循环中是否修改了正在迭代的列表（添加/删除元素）\n2. 是否使用了切片复制（for x in items[:]）或列表推导\n3. 是否使用了filter/del配合索引\n4. 是否会因索引漂移导致跳过元素或越界", SortOrder: 159},
		{Code: "py-enum-comparison", Name: "枚举值错误比较", Category: "maintainability", Severity: "low", Language: "python", IsEnabled: true, Description: "使用枚举名称字符串而非成员", Prompt: "检查Python枚举的使用：\n1. 是否使用字符串比较（role == 'admin'）而非枚举成员\n2. 枚举成员比较是否使用了is或==\n3. 枚举自动值与手动指定值是否一致\n4. 是否使用了StrEnum/IntEnum混淆了类型", SortOrder: 160},
		{Code: "py-context-manager-missing", Name: "未使用上下文管理器", Category: "maintainability", Severity: "low", Language: "python", IsEnabled: true, Description: "文件/锁/连接未用with语句", Prompt: "检查Python资源管理：\n1. 文件操作是否使用了with open(...)\n2. 锁（threading.Lock）是否使用了with lock:\n3. 数据库连接/事务是否使用了with语句\n4. 自定义__enter__/__exit__的资源是否正确管理", SortOrder: 161},

		// ========== Phase 1 新增：Java专属 (18条) ==========
		{Code: "java-deprecated-api", Name: "使用已废弃API", Category: "maintainability", Severity: "low", Language: "java", IsEnabled: true, Description: "使用了@Deprecated的类或方法", Prompt: "检查Java代码中的废弃API：\n1. 是否调用了标记为@Deprecated的方法/类\n2. 废弃API是否有推荐的替代方案\n3. 第三方库的版本是否过时导致API废弃\n4. Java升级计划是否考虑了废弃API的迁移", SortOrder: 162},
		{Code: "java-sql-injection-jdbc", Name: "JDBC SQL注入", Category: "security", Severity: "critical", Language: "java", IsEnabled: true, Description: "Statement拼接SQL字符串", Prompt: "检查Java JDBC的SQL注入风险：\n1. java.sql.Statement.execute是否拼接了SQL字符串\n2. 是否使用了PreparedStatement绑定参数\n3. 动态ORDER BY/LIMIT是否做了白名单校验\n4. LIKE中用户输入是否被转义", SortOrder: 163},
		{Code: "java-ldap-injection", Name: "LDAP查询注入", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "用户输入拼接到LDAP过滤器", Prompt: "检查Java LDAP注入风险：\n1. DirContext.search/LdapTemplate的filter是否拼接了用户输入\n2. 是否对LDAP特殊字符做了转义\n3. 是否使用了参数化LDAP查询\n4. 搜索范围是否限制了base DN", SortOrder: 164},
		{Code: "java-deserialization-untrusted", Name: "反序列化不可信数据", Category: "security", Severity: "critical", Language: "java", IsEnabled: true, Description: "ObjectInputStream读取不可信来源", Prompt: "检查Java反序列化安全：\n1. ObjectInputStream.readObject是否读取不可信数据\n2. 是否使用了ObjectInputFilter（JEP 290）限制反序列化类\n3. 是否使用了JSON/xml替代Java原生序列化\n4. RMI/JNDI中是否配置了只允许反序列化白名单类", SortOrder: 165},
		{Code: "java-weak-crypto", Name: "使用弱加密算法", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "DES/RC4/MD5用于加密场景", Prompt: "检查Java加密算法强度：\n1. 是否使用了DES/RC4/MD5等弱算法\n2. 密码哈希是否使用了bcrypt/PBKDF2/argon2\n3. AES密钥长度是否足够（至少128位，推荐256位）\n4. 是否使用了不安全的模式（ECB而非CBC/GCM）", SortOrder: 166},
		{Code: "java-http-client-timeout", Name: "HTTP客户端缺少超时", Category: "performance", Severity: "medium", Language: "java", IsEnabled: true, Description: "HttpURLConnection未设置超时", Prompt: "检查Java HTTP客户端超时配置：\n1. HttpURLConnection/HttpClient是否设置了connectTimeout和readTimeout\n2. RestTemplate/WebClient是否配置了超时\n3. 外部服务调用是否有熔断/降级机制\n4. 慢速响应是否会导致线程池耗尽", SortOrder: 167},
		{Code: "java-thread-pool-unbounded", Name: "线程池无界队列", Category: "performance", Severity: "high", Language: "java", IsEnabled: true, Description: "LinkedBlockingQueue导致OOM", Prompt: "检查Java线程池配置：\n1. ThreadPoolExecutor是否使用了无界队列（LinkedBlockingQueue无参构造）\n2. 最大线程数和队列大小是否匹配业务负载\n3. 拒绝策略是否合理（Abort/CallerRuns/Discard）\n4. 线程池是否被多个组件共享（隔离风险）", SortOrder: 168},
		{Code: "java-spring-validation-missing", Name: "Spring入参缺少校验", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "Controller参数未标记@Valid", Prompt: "检查Spring MVC的入参校验：\n1. Controller方法参数是否标记了@Valid/@Validated\n2. DTO字段是否添加了约束注解（@NotBlank/@Size/@Pattern）\n3. 错误校验结果是否被统一处理（MethodArgumentNotValidException）\n4. 自定义校验器是否覆盖了业务规则", SortOrder: 169},
		{Code: "java-Equals-hashCode-contract", Name: "违反Equals/HashCode契约", Category: "maintainability", Severity: "medium", Language: "java", IsEnabled: true, Description: "重写了equals未重写hashCode", Prompt: "检查Java equals和hashCode契约：\n1. 重写了equals()是否同时重写了hashCode()\n2. 两个对象equals相等时hashCode是否相等\n3. 是否在可变对象上使用了hashCode（HashMap/HashSet键）\n4. 是否使用了Objects.equals/Objects.hash", SortOrder: 170},
		{Code: "java-StringBuilder-no-init-cap", Name: "StringBuilder未预分配容量", Category: "performance", Severity: "low", Language: "java", IsEnabled: true, Description: "拼接大量字符串时动态扩容", Prompt: "检查Java StringBuilder使用：\n1. 已知拼接长度的场景是否预分配了容量\n2. 循环内频繁append是否导致了多次数组扩容\n3. 是否清楚无参构造的初始容量（16字符）\n4. 字符串拼接是否在单线程下用StringBuilder而非StringBuffer", SortOrder: 171},
		{Code: "java-Locale-dependent-operation", Name: "依赖Locale的操作", Category: "maintainability", Severity: "low", Language: "java", IsEnabled: true, Description: "toUpperCase未指定Locale", Prompt: "检查Java的Locale敏感操作：\n1. String.toUpperCase()/toLowerCase()是否指定了Locale.ROOT\n2. 土耳其Locale（ï）等特殊字母是否导致意外行为\n3. SimpleDateFormat是否明确了Locale和时区\n4. 大小写转换是否用于业务逻辑判断\n（如验证码比对、协议标识）", SortOrder: 172},
		{Code: "java-null-check-after-deref", Name: "空检查滞后", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "先使用后检查null", Prompt: "检查Java空指针检查的时序：\n1. 是否在引用对象之前先调用了方法再检查null\n2. Optional使用是否正确（isPresent + get前的检查）\n3. 链式调用（a.b.c.d）是否对中间节点做了null检查\n4. 是否合理使用@NonNull/@Nullable注解", SortOrder: 173},
		{Code: "java-infinite-recursion", Name: "无限递归风险", Category: "security", Severity: "medium", Language: "java", IsEnabled: true, Description: "缺少终止条件或基准情况", Prompt: "检查Java递归实现：\n1. 递归函数是否有明确的终止条件（base case）\n2. 递归深度是否可控制（防止StackOverflowError）\n3. 递归与迭代的性能/内存对比\n4. 尾递归是否可优化为循环", SortOrder: 174},
		{Code: "java-system-exit", Name: "使用System.exit", Category: "maintainability", Severity: "medium", Language: "java", IsEnabled: true, Description: "System.exit终止整个JVM", Prompt: "检查Java System.exit使用：\n1. 库/工具代码中是否错误地使用了System.exit\n2. 是否应抛出异常而非直接退出JVM\n3. System.exit是否破坏了框架生命周期管理\n4. Web应用中System.exit是否会导致整个服务宕机", SortOrder: 175},
		{Code: "java-catch-and-throw-new", Name: "捕获后抛出无cause新异常", Category: "maintainability", Severity: "medium", Language: "java", IsEnabled: true, Description: "丢失了原始堆栈信息", Prompt: "检查Java异常链完整性：\n1. catch后是否使用了throw new Exception(msg)而未传入cause\n2. 是否丢失了底层异常的关键堆栈信息\n3. 自定义异常构造器是否接受cause参数\n4. 日志中是否记录了完整异常链", SortOrder: 176},
		{Code: "java-implicit-narrowing", Name: "隐式窄化转换", Category: "security", Severity: "medium", Language: "java", IsEnabled: true, Description: "long/int转int/short可能溢出", Prompt: "检查Java隐式类型转换：\n1. long/float/double赋值给int/short/byte是否丢失精度\n2. 数值计算中是否存在隐式溢出\n3. Math.toIntExact是否被使用来检测溢出\n4. 窄化转换前是否做了范围校验", SortOrder: 177},
		{Code: "java-sql-resource-leak-try", Name: "数据库资源未用try-with-resources", Category: "performance", Severity: "medium", Language: "java", IsEnabled: true, Description: "Connection/Statement/ResultSet未关闭", Prompt: "检查Java数据库资源管理：\n1. Connection/Statement/ResultSet是否使用了try-with-resources\n2. 是否在finally中关闭资源\n3. 连接池中的连接是否被正确归还\n4. 是否使用了Spring的@Transactional管理连接生命周期", SortOrder: 178},
		{Code: "java-ConcurrentHashMap-atomic", Name: "ConcurrentHashMap非原子复合操作", Category: "security", Severity: "high", Language: "java", IsEnabled: true, Description: "put-if-absent非原子", Prompt: "检查Java ConcurrentHashMap的原子性：\n1. containsKey + putIfAbsent是否未使用computeIfAbsent\n2. get + put是否可替换为merge或compute\n3. 复合操作（if absent then put）是否原子\n4. 计数器是否使用了AtomicLong/LongAdder", SortOrder: 179},

		// ========== Phase 1 新增：Frontend专属 (17条) ==========
		{Code: "frontend-dom-xss", Name: "DOM型XSS", Category: "security", Severity: "critical", Language: "frontend", IsEnabled: true, Description: "用户输入通过DOM操作直接插入", Prompt: "检查前端DOM型XSS漏洞：\n1. 是否使用了document.createElement后insertBefore/appendChild插入用户输入\n2. textContent是否替代了innerHTML处理不可信内容\n3. URL.hash/location.href中的参数是否直接操作DOM\n4. postMessage接收的消息是否校验了origin", SortOrder: 180},
		{Code: "frontend-csp-missing", Name: "缺少内容安全策略", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "未配置Content-Security-Policy", Prompt: "检查前端CSP配置：\n1. HTTP响应头是否包含Content-Security-Policy\n2. 是否限制了script-src为self或nonce-hash\n3. 是否禁止了unsafe-inline和unsafe-eval\n4. 是否配置了report-uri/report-to收集违规报告", SortOrder: 181},
		{Code: "frontend-localstorage-sensitive", Name: "LocalStorage存储敏感信息", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "Token/密码存储在localStorage", Prompt: "检查前端本地存储安全：\n1. JWT Token/密码/密钥是否存储在localStorage（XSS可窃取）\n2. 敏感信息是否存储在httpOnly cookie中\n3. localStorage中的数据是否做了过期清理\n4. 是否考虑过使用sessionStorage替代减少持久化风险", SortOrder: 182},
		{Code: "frontend-js-url-validation", Name: "URL跳转未校验", Category: "security", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "window.open使用用户输入", Prompt: "检查前端URL跳转安全：\n1. window.open/location.href赋值是否使用了用户输入\n2. 跳转目标是否在白名单内\n3. 是否允许了javascript:协议（导致XSS）\n4. 是否在服务端校验了回调地址（OAuth等场景）", SortOrder: 183},
		{Code: "frontend-eval-usage", Name: "使用eval/Function", Category: "security", Severity: "critical", Language: "frontend", IsEnabled: true, Description: "动态执行字符串代码", Prompt: "检查前端动态代码执行：\n1. 是否使用了eval()执行字符串\n2. 是否使用了new Function()动态创建函数\n3. setTimeout/setInterval是否传入了字符串参数\n4. 是否有更安全的替代方案（JSON.parse、模板解析）", SortOrder: 184},
		{Code: "frontend-https-mixed-content", Name: "HTTPS页面混入HTTP资源", Category: "security", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "img/script/css使用http://", Prompt: "检查前端的混合内容问题：\n1. HTTPS页面中是否加载了HTTP资源（img/script/css/iframe）\n2. 内部API调用是否全量升级为HTTPS\n3. 相对协议URL（//example.com）是否被正确使用\n4. CSP upgrade-insecure-requests是否被配置", SortOrder: 185},
		{Code: "frontend-dom-insertAdjacentHTML", Name: "insertAdjacentHTML未过滤", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "用户输入通过insertAdjacentHTML插入", Prompt: "检查前端insertAdjacentHTML的使用：\n1. 用户输入是否直接通过insertAdjacentHTML插入DOM\n2. 是否先进行了HTML转义\n3. DOMPurify等库是否被使用\n4. 是否可用textContent/innerText替代", SortOrder: 186},
		{Code: "frontend-react-hooks-dependency", Name: "React Hooks依赖缺失", Category: "maintainability", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "useEffect/useCallback缺少依赖项", Prompt: "检查React Hooks依赖完整性：\n1. useEffect/useCallback/useMemo的dependency array是否完整\n2. 是否遗漏了state/prop/callback依赖\n3. 依赖数组是否包含不稳定引用导致无限循环\n4. ESLint react-hooks/exhaustive-deps规则是否开启", SortOrder: 187},
		{Code: "frontend-vue-watch-deep", Name: "Vue deep watch性能问题", Category: "performance", Severity: "low", Language: "frontend", IsEnabled: true, Description: "深度监听大对象导致性能下降", Prompt: "检查Vue的watch性能：\n1. watch是否使用了deep: true监听大对象\n2. 深层嵌套对象的变更是否被准确追踪\n3. 是否可以用computed替代watch\n4. 监听数组时是否清楚Vue的数组变更检测限制", SortOrder: 188},
		{Code: "frontend-console-log-production", Name: "生产环境保留console.log", Category: "maintainability", Severity: "info", Language: "frontend", IsEnabled: true, Description: "调试日志未在生产构建中移除", Prompt: "检查前端生产构建产物：\n1. 生产构建的JS/CSS中是否残留了console.log\n2. debugger语句是否被移除\n3. 开发工具（React/Vue devtools）是否在生产环境暴露\n4. source map是否被上传到公开环境", SortOrder: 189},
		{Code: "frontend-accessibility-missing-alt", Name: "图片缺少alt文本", Category: "maintainability", Severity: "low", Language: "frontend", IsEnabled: true, Description: "img标签缺少alt属性", Prompt: "检查前端无障碍可访问性：\n1. img标签是否提供了有意义的alt属性\n2. 装饰性图片是否使用了alt=\"\"\n3. 表单控件是否关联了label\n4. 按钮/链接是否可通过键盘访问（focus/tabindex）", SortOrder: 190},
		{Code: "frontend-api-error-unhandled", Name: "API错误未统一处理", Category: "maintainability", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "fetch/axios错误未全局捕获", Prompt: "检查前端API错误处理：\n1. fetch/axios请求失败是否被统一处理\n2. 401/403/500错误是否有用户友好的提示\n3. 网络断开/超时是否被处理\n4. 是否配置了全局的请求/响应拦截器", SortOrder: 191},
		{Code: "frontend-event-listener-memory", Name: "事件监听内存泄露", Category: "performance", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "addEventListener未remove", Prompt: "检查前端事件监听清理：\n1. addEventListener后是否在组件卸载时removeEventListener\n2. 全局事件（window/scroll/resize）是否在合适时机清理\n3. 自定义事件总线的订阅是否被取消\n4. 是否注意使用useEffect的cleanup函数", SortOrder: 192},
		{Code: "frontend-css-important-misuse", Name: "滥用!important", Category: "readability", Severity: "info", Language: "frontend", IsEnabled: true, Description: "过多使用!important导致样式难维护", Prompt: "检查CSS !important使用：\n1. 是否过多使用了!important覆盖样式\n2. 样式问题是否应通过提高选择器权重解决\n3. 第三方库样式覆盖是否合理\n4. CSS-in-JS/tailwind中!important的使用是否顺手", SortOrder: 193},
		{Code: "frontend-jsonp-usage", Name: "使用JSONP", Category: "security", Severity: "high", Language: "frontend", IsEnabled: true, Description: "JSONP存在XSS和CSRF风险", Prompt: "检查前端JSONP使用：\n1. 是否仍使用了JSONP跨域（现代应用应使用CORS）\n2. JSONP回调函数是否可被利用做XSS\n3. 是否验证了JSONP响应来源\n4. 是否可使用fetch + CORS替代JSONP", SortOrder: 194},
		{Code: "frontend-credential-plaintext", Name: "前端硬编码API凭据", Category: "security", Severity: "critical", Language: "frontend", IsEnabled: true, Description: "源码中包含API Key/Secret", Prompt: "检查前端代码中的硬编码凭据：\n1. 源码中是否包含API Key/Secret/AppID\n2. build产物中是否可通过字符串搜索找到密钥\n3. 环境变量是否正确使用（import.meta.env）\n4. 客户端凭证是否通过服务端代理调用API", SortOrder: 195},
		{Code: "frontend-deep-link-unvalidated", Name: "深度链接未校验", Category: "security", Severity: "medium", Language: "frontend", IsEnabled: true, Description: "URL Scheme被恶意利用", Prompt: "检查前端深度链接处理：\n1. 是否通过URL scheme调用了本地应用\n2. scheme目标是否在白名单内\n3. 参数是否被正确编码和校验\n4. 是否防范了scheme注入攻击", SortOrder: 196},
	}

	for _, rule := range rules {
		var existing ReviewRule
		if err := SilentFirst(DB.Where("code = ?", rule.Code), &existing); err != nil {
			// 不存在则插入
			if err := DB.Create(&rule).Error; err != nil {
				zap.L().Warn("init built-in rule failed", zap.String("code", rule.Code), zap.Error(err))
			} else {
				zap.L().Info("init built-in rule", zap.String("code", rule.Code))
			}
		} else {
			// 已存在，更新 IsEnabled 和 SortOrder（允许运行时调整）
			DB.Model(&existing).Updates(map[string]interface{}{
				"is_enabled": rule.IsEnabled,
				"sort_order": rule.SortOrder,
			})
		}
	}
	zap.L().Info("built-in review rules initialized", zap.Int("total", len(rules)))
}

// initDefaultProjectReviewConfigs 为所有现有项目生成默认规则配置
func initDefaultProjectReviewConfigs() {
	var projects []Project
	if err := DB.Find(&projects).Error; err != nil {
		zap.L().Warn("find projects for review config init failed", zap.Error(err))
		return
	}

	var count int
	for _, p := range projects {
		// 检查是否已有配置
		var cfgCount int64
		DB.Model(&ProjectReviewConfig{}).Where("project_id = ?", p.ID).Count(&cfgCount)
		if cfgCount > 0 {
			continue // 已有配置，跳过
		}

		// 查询该项目语言对应的通用规则 + 特定语言规则
		var rules []ReviewRule
		DB.Where("is_enabled = ? AND (language = 'common' OR language = ?)", true, p.Language).Find(&rules)

		for _, rule := range rules {
			config := ProjectReviewConfig{
				ProjectID: p.ID,
				RuleID:    rule.ID,
				IsEnabled: true,
				Severity:  "", // 使用规则默认级别
			}
			if err := DB.Create(&config).Error; err != nil {
				zap.L().Warn("init project review config failed",
					zap.Uint("project_id", p.ID),
					zap.Uint("rule_id", rule.ID),
					zap.Error(err))
			}
		}
		count++
	}
	zap.L().Info("default project review configs initialized", zap.Int("projects", count))
}

// initBuiltInReviewCategories 初始化内置评审维度
func initBuiltInReviewCategories() {
	categories := []ReviewCategory{
		{Code: "security", Name: "安全性", IsBuiltIn: true, SortOrder: 1},
		{Code: "performance", Name: "性能", IsBuiltIn: true, SortOrder: 2},
		{Code: "readability", Name: "可读性", IsBuiltIn: true, SortOrder: 3},
		{Code: "maintainability", Name: "可维护性", IsBuiltIn: true, SortOrder: 4},
		{Code: "test_coverage", Name: "测试覆盖", IsBuiltIn: true, SortOrder: 5},
	}
	for _, cat := range categories {
		var existing ReviewCategory
		if err := SilentFirst(DB.Where("code = ?", cat.Code), &existing); err != nil {
			if err := DB.Create(&cat).Error; err != nil {
				zap.L().Warn("init built-in category failed", zap.String("code", cat.Code), zap.Error(err))
			} else {
				zap.L().Info("init built-in category", zap.String("code", cat.Code))
			}
		} else {
			// 已存在则更新排序和名称（允许运行时微调显示名称）
			DB.Model(&existing).Updates(map[string]interface{}{
				"name":        cat.Name,
				"sort_order":  cat.SortOrder,
				"is_built_in": true,
			})
		}
	}
	zap.L().Info("built-in review categories initialized", zap.Int("total", len(categories)))
}

// initSystemConfig 初始化系统配置默认记录，确保超时等配置有默认值
func initSystemConfig() {
	var cfg SystemConfig
	if err := SilentFirst(DB, &cfg); err != nil {
		cfg = SystemConfig{
			TaskTimeoutMin:             120,
			MaxParallelTask:            20,
			LogRetentionDay:            90,
			DiffTruncationThreshold:    5000,
			LLMRetryMaxAttempts:        3,
			LLMRetryInitialDelayMs:     1000,
			LLMRetryBackoffMultiplier:  2.0,
			LLMRetryMaxDelayMs:         30000,
			AlertDurationSec:           300,
			AlertCooldownSec:           3600,
			AlertNotifierID:            0,
			AlertMentionUserIDs:        "",
			JSONRetryMaxAttempts:       3,
			JSONRetryInitialDelaySec:   2,
			JSONRetryBackoffMultiplier: 2.0,
			JSONRetryMaxDelaySec:       30,
			JSONRetryFallbackStrategy:  "regex",
			DefaultDimensionWeights:    `{"security":30,"code_quality":25,"readability":20,"maintainability":15,"test_coverage":10}`,
			AILogTemplate:              "请先执行以下命令拉取代码：\ngit clone {{CLONE_URL}}\n\n变更摘要：\n{{MR_DIFF}}\n\n{{USER_INPUT}}\n\n请审查以上代码变更，给出审查意见。",
		}
		if err := DB.Create(&cfg).Error; err != nil {
			zap.L().Error("init system config failed", zap.Error(err))
		} else {
			zap.L().Info("system config initialized with defaults")
		}
	} else {
		// 兼容旧记录：新加列默认 0 会导致业务逻辑异常（"不重试"），这里回填默认值
		if cfg.LLMRetryMaxAttempts <= 0 || cfg.LLMRetryInitialDelayMs <= 0 ||
			cfg.LLMRetryBackoffMultiplier <= 0 || cfg.LLMRetryMaxDelayMs <= 0 {
			update := map[string]interface{}{}
			if cfg.LLMRetryMaxAttempts <= 0 {
				update["llm_retry_max_attempts"] = 3
			}
			if cfg.LLMRetryInitialDelayMs <= 0 {
				update["llm_retry_initial_delay_ms"] = 1000
			}
			if cfg.LLMRetryBackoffMultiplier <= 0 {
				update["llm_retry_backoff_multiplier"] = 2.0
			}
			if cfg.LLMRetryMaxDelayMs <= 0 {
				update["llm_retry_max_delay_ms"] = 30000
			}
			if err := DB.Model(&cfg).Updates(update).Error; err != nil {
				zap.L().Error("backfill system config defaults failed", zap.Error(err))
			} else {
				zap.L().Info("system config backfilled defaults", zap.Any("updates", update))
			}
		}
		zap.L().Info("system config already exists",
			zap.Int("task_timeout_min", cfg.TaskTimeoutMin),
			zap.Int("max_parallel_task", cfg.MaxParallelTask),
			zap.Int("llm_retry_max_attempts", cfg.LLMRetryMaxAttempts))
	}
}

func migrateLLMModelColumns() {
	if !DB.Migrator().HasColumn(&LLMModel{}, "model_type") {
		if err := DB.Exec("ALTER TABLE llm_models ADD COLUMN model_type VARCHAR(20) NOT NULL DEFAULT 'llm'").Error; err != nil {
			zap.L().Warn("add column model_type failed", zap.Error(err))
		} else {
			zap.L().Info("added column model_type to llm_models")
		}
	}
	if !DB.Migrator().HasIndex(&LLMModel{}, "idx_type_status") {
		if err := DB.Exec("CREATE INDEX idx_type_status ON llm_models(model_type, status)").Error; err != nil {
			zap.L().Warn("create index idx_type_status failed", zap.Error(err))
		} else {
			zap.L().Info("created index idx_type_status on llm_models")
		}
	}
}

func initIncubatorConfig() {
	var cfg IncubatorConfig
	if err := SilentFirst(DB.Where("id = ?", 1), &cfg); err != nil {
		cfg = IncubatorConfig{ID: 1, VectorStoreConfig: "{}"}
		if err := DB.Create(&cfg).Error; err != nil {
			zap.L().Error("init incubator config failed", zap.Error(err))
		} else {
			zap.L().Info("incubator config initialized with defaults")
		}
	} else {
		zap.L().Info("incubator config already exists", zap.Uint("id", cfg.ID))
	}
}

func initNotificationGlobalSetting() {
	var setting NotificationGlobalSetting
	if err := SilentFirst(DB.Where("id = ?", 1), &setting); err != nil {
		setting = NotificationGlobalSetting{ID: 1}
		if err := DB.Create(&setting).Error; err != nil {
			zap.L().Error("init notification global setting failed", zap.Error(err))
		} else {
			zap.L().Info("notification global setting initialized")
		}
	} else {
		zap.L().Info("notification global setting already exists", zap.Uint("id", setting.ID))
	}
}

// initPipelineStages 初始化 Pipeline 阶段定义
func initPipelineStages() {
	// AI 评审总超时时间使用系统配置的任务超时（分钟→秒），但上限 600 秒（10 分钟）
	var sysCfg SystemConfig
	var reviewFrameTimeoutSec int
	if err := DB.First(&sysCfg).Error; err == nil && sysCfg.TaskTimeoutMin > 0 {
		reviewFrameTimeoutSec = sysCfg.TaskTimeoutMin * 60
		if reviewFrameTimeoutSec > 600 {
			reviewFrameTimeoutSec = 600
		}
	} else {
		reviewFrameTimeoutSec = 600 // fallback: 10 分钟
	}

	stages := []ReviewPipelineStage{
		{Code: "trigger_check", Name: "触发条件评估", Description: "检查 MR 是否符合自动审查条件", Icon: "fas fa-filter", SortOrder: 10, TimeoutSec: 5},
		{Code: "git_clone", Name: "代码获取", Description: "从 Git 仓库获取变更 diff", Icon: "fas fa-code-branch", SortOrder: 20, TimeoutSec: 60},
		{Code: "code_understanding", Name: "代码理解器", Description: "AST + 知识图谱 + 数据流分析", Icon: "fas fa-brain", SortOrder: 31, TimeoutSec: 60},
		{Code: "dependency_scan", Name: "依赖漏洞扫描", Description: "解析依赖文件并匹配本地漏洞库", Icon: "fas fa-shield-alt", SortOrder: 35, TimeoutSec: 30},
		{Code: "secret_scan", Name: "密钥泄露扫描", Description: "扫描 diff 中泄露的密钥 / Token / 密码", Icon: "fas fa-key", SortOrder: 36, TimeoutSec: 30},
		{Code: "security_audit", Name: "安全审计", Description: "AST + Regex 双模式安全敏感操作检测", Icon: "fas fa-user-secret", SortOrder: 37, TimeoutSec: 30},
		{Code: "test_suggestion", Name: "测试建议", Description: "基于代码分支结构生成测试场景建议", Icon: "fas fa-vial", SortOrder: 45, TimeoutSec: 30},
		{Code: "impact_analysis", Name: "影响分析", Description: "识别 Breaking Change 和跨模块影响范围", Icon: "fas fa-project-diagram", SortOrder: 46, TimeoutSec: 30},
		{Code: "batch_review_frame", Name: "AI 评审", Description: "结构化 AI 代码审查（含分批）", Icon: "fas fa-robot", IsGroup: true, SortOrder: 50, TimeoutSec: reviewFrameTimeoutSec},
		{Code: "batch_plan", Name: "分批决策", Description: "根据 diff 大小决定分批策略", Icon: "fas fa-list-ol", SortOrder: 51, TimeoutSec: 5},
		{Code: "batch_review", Name: "批次评审", Description: "逐批次 LLM 代码审查", Icon: "fas fa-layer-group", IsAsync: true, SortOrder: 52, TimeoutSec: 120},
		{Code: "batch_summary", Name: "汇总评审", Description: "汇总各批次结果生成综合报告", Icon: "fas fa-file-signature", SortOrder: 53, TimeoutSec: 120},
		{Code: "review_arbitration", Name: "评审裁决", Description: "Agent-LLM 协同去重、汇总裁决、独立分类", Icon: "fas fa-balance-scale", SortOrder: 55, TimeoutSec: 60, IsMandatory: true, AllowedToSkip: true},
		{Code: "post_process", Name: "后处理", Description: "发布评论、发送通知、阈值检查", Icon: "fas fa-paper-plane", SortOrder: 60, TimeoutSec: 30},
	}

	// 设置 review_arbitration 的依赖阶段
	for i := range stages {
		if stages[i].Code == "review_arbitration" {
			stages[i].SetRequiredStages([]string{"batch_review_frame"})
			break
		}
	}

	for _, stage := range stages {
		var existing ReviewPipelineStage
		if err := SilentFirst(DB.Where("code = ?", stage.Code), &existing); err != nil {
			// 不存在则插入
			if err := DB.Create(&stage).Error; err != nil {
				zap.L().Warn("init pipeline stage failed", zap.String("code", stage.Code), zap.Error(err))
			} else {
				zap.L().Info("init pipeline stage", zap.String("code", stage.Code), zap.String("name", stage.Name))
			}
		} else {
			// 已存在则更新（允许运行时调整名称、图标、超时）
			DB.Model(&existing).Updates(map[string]interface{}{
				"name":            stage.Name,
				"description":     stage.Description,
				"icon":            stage.Icon,
				"is_group":        stage.IsGroup,
				"is_async":        stage.IsAsync,
				"timeout_sec":     stage.TimeoutSec,
				"sort_order":      stage.SortOrder,
				"is_mandatory":    stage.IsMandatory,
				"allowed_to_skip": stage.AllowedToSkip,
				"required_stages": stage.RequiredStages,
			})
		}
	}
	zap.L().Info("pipeline stages initialized", zap.Int("total", len(stages)))
}

// initReviewAgentConfig 初始化默认智能体全局配置（全局唯一记录 id=1）
func initReviewAgentConfig() {
	var cfg ReviewAgentConfig
	if err := DB.First(&cfg, 1).Error; err == nil {
		// 记录已存在，直接返回
		return
	}
	// 首次初始化：全部启用
	cfg = ReviewAgentConfig{
		ID:              1,
		ShowAgentStatus: true,
	}
	cfg.SetEnabledStageCodes([]string{
		"trigger_check", "git_clone", "code_understanding",
		"dependency_scan", "batch_review_frame", "post_process",
	})
	cfg.SetStageConfig("code_understanding", map[string]interface{}{
		"depth":                     1,
		"context_extract_timeout":   30,
		"symbol_graph_timeout":      60,
		"report_merge_timeout":      10,
	})
	cfg.SetTriggerEventCodes([]string{
		"merge_request_open", "merge_request_update", "merge_request_reopen",
	})
	if err := DB.Create(&cfg).Error; err != nil {
		// 多实例并发启动时可能出现唯一键冲突，属于正常情况，忽略即可
		if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
			return
		}
		zap.L().Warn("init review agent config failed", zap.Error(err))
	} else {
		zap.L().Info("init review agent config with default stages")
	}
}

// initBuiltinVulnSyncSources 初始化内置漏洞库同步源
func initBuiltinVulnSyncSources() {
	builtins := []VulnerabilitySyncSource{
		{
			Name:       "GitHub Advisory Database",
			SourceType: "git",
			URL:        "https://github.com/github/advisory-database.git",
			Branch:     "main",
			Ecosystem:  "Go,maven,pypi,npm",
			IsBuiltin:  true,
			Enabled:    true,
		},
	}

	// 删除已被移除的旧内置源
	var oldBuiltins []VulnerabilitySyncSource
	if err := DB.Where("is_builtin = ?", true).Find(&oldBuiltins).Error; err == nil {
		for _, old := range oldBuiltins {
			found := false
			for _, b := range builtins {
				if b.URL == old.URL {
					found = true
					break
				}
			}
			if !found {
				DB.Where("id = ?", old.ID).Delete(&VulnerabilitySyncSource{})
				zap.L().Info("removed old builtin vuln sync source", zap.String("name", old.Name))
			}
		}
	}

	for _, src := range builtins {
		var existing VulnerabilitySyncSource
		if err := SilentFirst(DB.Where("url = ? AND is_builtin = ?", src.URL, true), &existing); err != nil {
			// 不存在则插入
			if err := DB.Create(&src).Error; err != nil {
				zap.L().Warn("init builtin vuln sync source failed", zap.String("url", src.URL), zap.Error(err))
			} else {
				zap.L().Info("init builtin vuln sync source", zap.String("name", src.Name), zap.String("url", src.URL))
			}
		} else {
			// 已存在则更新名称和配置（允许运行时调整）
			if err := DB.Model(&existing).Updates(map[string]interface{}{
				"name":      src.Name,
				"branch":    src.Branch,
				"ecosystem": src.Ecosystem,
				"enabled":   src.Enabled,
			}).Error; err != nil {
				zap.L().Warn("update builtin vuln sync source failed", zap.String("url", src.URL), zap.Error(err))
			}
		}
	}
}

// initMCPTools 初始化 MCP 能力中心工具元数据（INSERT IGNORE + UPDATE）
func initMCPTools() {
	tools := []MCPTool{
		{ToolName: "list_tasks", DisplayName: "任务列表查询", Description: "查询代码评审任务列表，支持按状态和项目过滤。获取 task_id 的入口。", Category: "query", Icon: "fa-list", Color: "#3b82f6", Tags: `["只读","分页","需认证"]`, Params: `[{"n":"status","t":"string","r":false,"d":"running|pending|failed|success|stopped"},{"n":"project_id","t":"integer","r":false},{"n":"mine","t":"boolean","r":false,"d":"true"},{"n":"limit","t":"integer","r":false,"d":"5"}]`, Phrases: `["看看我最近的评审任务","列出所有失败的任务","项目 12 最近有哪些 MR 在评审"]`, ResponseExample: `{"data":[{"id":2084,"project_id":12,"mr_merge_id":42,"mr_title":"feat: add auth middleware","mr_author":"zhangsan","status":"success","score_value":82,"created_at":"2026-08-28T10:00:00Z"}],"total":156}`, SortOrder: 1},
		{ToolName: "get_task", DisplayName: "任务详情查询", Description: "获取单个评审任务的完整详情（含 MR 信息、评审得分、Issue 数量、使用模型）。", Category: "query", Icon: "fa-file-alt", Color: "#3b82f6", Tags: `["只读","需认证"]`, Params: `[{"n":"task_id","t":"integer","r":true}]`, Phrases: `["任务 2084 怎么样了","这个评审的得分是多少"]`, Requires: `["task_id"]`, ResponseExample: `{"data":{"id":2084,"project_id":12,"mr_merge_id":42,"mr_title":"feat: add auth middleware","mr_author":"zhangsan","status":"success","score_value":82,"pending_issues_count":3,"total_issues_count":7,"created_at":"2026-08-28T10:00:00Z","completed_at":"2026-08-28T10:05:00Z"}}`, SortOrder: 2},
		{ToolName: "get_task_pipeline", DisplayName: "Pipeline 详情查询", Description: "查询任务 Pipeline 各阶段执行详情，用于定位「卡在哪一步」或「为什么失败」。", Category: "query", Icon: "fa-project-diagram", Color: "#3b82f6", Tags: `["只读","需认证"]`, Params: `[{"n":"task_id","t":"integer","r":true}]`, Phrases: `["任务 2084 卡在哪一步","Pipeline 执行到哪了"]`, Requires: `["task_id"]`, ResponseExample: `{"data":{"task_id":2084,"stages":[{"stage_code":"trigger_check","status":"success","duration_sec":2},{"stage_code":"git_clone","status":"success","duration_sec":15},{"stage_code":"batch_review_frame","status":"running","duration_sec":120}]}}`, SortOrder: 3},
		{ToolName: "get_mr_review", DisplayName: "MR 评审结果查询", Description: "获取指定任务的 AI 代码评审结果（Issue 列表）。支持按 severity 和 category 过滤。", Category: "query", Icon: "fa-code", Color: "#3b82f6", Tags: `["只读","需认证","可过滤"]`, Params: `[{"n":"task_id","t":"integer","r":true},{"n":"severity","t":"string","r":false,"d":"critical|high|medium|low"},{"n":"category","t":"string","r":false,"d":"security|performance|style|..."}]`, Phrases: `["任务 2084 有什么安全问题","找出所有 critical 的 issue"]`, Requires: `["task_id"]`, ResponseExample: `{"data":{"task_id":2084,"total_score":82,"dimensions":{"security":{"score":90,"weight":30},"code_quality":{"score":78,"weight":25}},"issues":[{"id":301,"severity":"high","category":"security","file":"auth.go","line_start":45,"message":"硬编码密钥存在泄露风险","suggestion":"建议从环境变量读取"}],"recommendations":["建议增加单元测试覆盖"]}}`, SortOrder: 4},
		{ToolName: "get_issue_detail", DisplayName: "Issue 详情查询", Description: "获取单个 Issue 的完整详情（含代码上下文、详细描述、修复方案）。", Category: "query", Icon: "fa-search-plus", Color: "#3b82f6", Tags: `["只读","需认证"]`, Params: `[{"n":"issue_id","t":"integer","r":true}]`, Phrases: `["第 3 个 issue 具体是什么","issue 42 的修复建议"]`, Requires: `["issue_id"]`, ResponseExample: `{"data":{"id":301,"task_id":2084,"severity":"high","category":"security","file":"auth.go","line_start":45,"line_end":48,"code_snippet":"const API_KEY = \"sk-123456\"","message":"硬编码密钥存在泄露风险","suggestion":"建议改为从环境变量读取，例如 os.Getenv(\"API_KEY\")","owner_name":"zhangsan","status":"pending"}}`, SortOrder: 5},
		{ToolName: "list_pending_issues", DisplayName: "待处理 Issue 列表查询", Description: "查询当前用户的待处理 Issue 列表（pending / pending_inherited），用于对话式 issue 治理。返回结果含 index 编号便于对话引用。", Category: "query", Icon: "fa-clipboard-list", Color: "#3b82f6", Tags: `["只读","需认证","可过滤"]`, Params: `[{"n":"limit","t":"integer","r":false,"d":"20"}]`, Phrases: `["我有什么待处理的问题","我的待办","还有哪些 issue 没处理"]`, ResponseExample: `{"data":{"total":5,"items":[{"index":1,"id":301,"severity":"high","category":"security","file":"auth.go","message":"硬编码密钥","age_hours":24},{"index":2,"id":302,"severity":"medium","category":"code_quality","file":"utils.go","message":"函数过长","age_hours":48}]}}`, SortOrder: 6},
		{ToolName: "list_historical_issues", DisplayName: "历史 Issue 列表查询", Description: "查询当前用户已处理的历史 Issue（resolved / false_positive / ignored / auto_archived）。支持按状态过滤。", Category: "query", Icon: "fa-history", Color: "#3b82f6", Tags: `["只读","需认证","可过滤"]`, Params: `[{"n":"status","t":"string","r":false,"d":"resolved|false_positive|ignored|auto_archived"},{"n":"limit","t":"integer","r":false,"d":"20"}]`, Phrases: `["我之前处理过哪些问题","看看误报记录","自动归档了哪些 issue"]`, ResponseExample: `{"data":{"total":12,"items":[{"id":201,"severity":"low","category":"style","file":"main.go","message":"缺少文档注释","resolved_at":"2026-08-20T10:00:00Z","status":"resolved"}]}}`, SortOrder: 7},
		{ToolName: "list_merge_requests", DisplayName: "MR 列表查询", Description: "查询已触发过 AI 评审的 MR 列表。支持按状态和项目过滤。", Category: "query", Icon: "fa-code-branch", Color: "#3b82f6", Tags: `["只读","分页","需认证"]`, Params: `[{"n":"project_id","t":"integer","r":false},{"n":"status","t":"string","r":false}]`, Phrases: `["看看最近的 MR","有哪些 MR 在评审"]`, ResponseExample: `{"data":[{"mr_iid":42,"project_id":12,"title":"feat: add auth middleware","author":"zhangsan","status":"success","latest_task_id":2084}],"total":23}`, SortOrder: 8},
		{ToolName: "get_merge_request_detail", DisplayName: "MR 详情查询", Description: "获取指定 MR 的评审详情和对应任务信息。", Category: "query", Icon: "fa-info-circle", Color: "#3b82f6", Tags: `["只读","需认证"]`, Params: `[{"n":"project_id","t":"integer","r":true},{"n":"mr_iid","t":"integer","r":true}]`, Phrases: `["MR !42 评审结果如何","这个 MR 有什么问题"]`, ResponseExample: `{"data":{"mr_iid":42,"project_id":12,"title":"feat: add auth middleware","author":"zhangsan","tasks":[{"id":2084,"status":"success","score_value":82,"created_at":"2026-08-28T10:00:00Z"}],"open_issues":3,"total_issues":7}}`, SortOrder: 9},
		{ToolName: "list_notifications", DisplayName: "通知列表查询", Description: "查看站内通知列表（评审完成、升级提醒等）。支持 unread/all 过滤。", Category: "query", Icon: "fa-bell", Color: "#3b82f6", Tags: `["只读","分页","需认证"]`, Params: `[{"n":"filter","t":"string","r":false,"d":"unread|all"}]`, Phrases: `["有没有新通知","未读通知有哪些"]`, ResponseExample: `{"data":[{"id":1001,"type":"task_completed","title":"AI评审完成","content":"项目 test-app 的 MR !42 已完成评审，得分 82","is_read":false,"created_at":"2026-08-28T10:05:00Z"}],"unread_count":3}`, SortOrder: 10},
		{ToolName: "list_projects", DisplayName: "项目列表查询", Description: "查询已接入 AI 代码评审的项目列表。默认只返回我有任务的项目。", Category: "query", Icon: "fa-folder-open", Color: "#3b82f6", Tags: `["只读","分页","需认证"]`, Params: `[{"n":"limit","t":"integer","r":false,"d":"20"}]`, Phrases: `["有哪些项目在用评审","看看所有项目"]`, ResponseExample: `{"data":[{"id":12,"name":"test-app","language":"Go","pending_issues":5}],"total":8}`, SortOrder: 11},
		{ToolName: "retry_task", DisplayName: "任务重试", Description: "重试 AI 评审任务，支持对已完成、失败、停止、超时、待处理状态的任务重新触发评审。review 任务可附加补充复核意见，AI 将参考意见进行针对性重审。", Category: "write", Icon: "fa-redo", Color: "#f59e0b", Tags: `["可写","需认证","需确认"]`, Params: `[{"n":"task_id","t":"integer","r":true},{"n":"user_review_comment","t":"string","r":false,"d":"补充复核意见，最多5000字符"},{"n":"selected_comment_ids","t":"array","r":false,"d":"历史复核意见ID列表"}]`, Phrases: `["重试任务 2084","重新评审一下","再跑一次","这个评审结果不对，重新审一下"]`, Dangerous: true, Requires: `["task_id"]`, ResponseExample: `{"success":true,"task_id":2084,"message":"已触发重试"}`, SortOrder: 12},
		{ToolName: "stop_task", DisplayName: "任务停止", Description: "停止正在运行或排队中的评审任务（当前阶段完成后终止）。", Category: "write", Icon: "fa-stop-circle", Color: "#f59e0b", Tags: `["可写","需认证","需确认"]`, Params: `[{"n":"task_id","t":"integer","r":true}]`, Phrases: `["停掉任务 2084","取消评审","停止正在跑的任务"]`, Dangerous: true, Requires: `["task_id"]`, ResponseExample: `{"success":true,"task_id":2084,"message":"已发送停止信号，任务将在当前 stage 完成后终止"}`, SortOrder: 13},
		{ToolName: "mark_all_read", DisplayName: "标记全部已读", Description: "一键将当前用户所有未读通知标记为已读。", Category: "write", Icon: "fa-check-double", Color: "#f59e0b", Tags: `["可写","需认证"]`, Params: `[]`, Phrases: `["全部标记为已读","清除所有通知"]`, Dangerous: true, ResponseExample: `{"success":true,"marked_count":7,"message":"已将 7 条通知标记为已读"}`, SortOrder: 14},
		{ToolName: "resolve_issue", DisplayName: "Issue 标记已处理", Description: "将单个待处理 Issue 标记为已处理（resolved）。当用户在对话中说「这个我修好了」时使用。", Category: "write", Icon: "fa-check-circle", Color: "#f59e0b", Tags: `["可写","需认证","需确认"]`, Params: `[{"n":"issue_id","t":"integer","r":true},{"n":"comment","t":"string","r":false}]`, Phrases: `["第 1 个已处理","这个问题已经修复了","标记为已处理"]`, Dangerous: true, Requires: `["issue_id"]`, ResponseExample: `{"success":true,"issue_id":301,"status":"resolved","message":"Issue 已标记为已处理"}`, SortOrder: 15},
		{ToolName: "reject_issue", DisplayName: "Issue 标记误报", Description: "将单个待处理 Issue 标记为误报（false_positive）。当用户在对话中说「这是误报」时使用。必须填写 reason 说明原因。", Category: "write", Icon: "fa-times-circle", Color: "#f59e0b", Tags: `["可写","需认证","需确认"]`, Params: `[{"n":"issue_id","t":"integer","r":true},{"n":"reason","t":"string","r":true}]`, Phrases: `["第 2 个是误报","这是误报，已经做了校验","标记为误报"]`, Dangerous: true, Requires: `["issue_id"]`, ResponseExample: `{"success":true,"issue_id":301,"status":"false_positive","message":"Issue 已标记为误报"}`, SortOrder: 16},
		{ToolName: "ignore_issue", DisplayName: "Issue 标记忽略", Description: "将单个待处理 Issue 标记为忽略（ignored）。当用户在对话中说「先忽略这个」时使用。必须填写 reason 说明原因。", Category: "write", Icon: "fa-eye-slash", Color: "#f59e0b", Tags: `["可写","需认证","需确认"]`, Params: `[{"n":"issue_id","t":"integer","r":true},{"n":"reason","t":"string","r":true}]`, Phrases: `["先忽略这个","暂时不处理","标记为忽略"]`, Dangerous: true, Requires: `["issue_id"]`, ResponseExample: `{"success":true,"issue_id":301,"status":"ignored","message":"Issue 已标记为忽略"}`, SortOrder: 17},
		{ToolName: "get_workbench", DisplayName: "工作台数据查询", Description: "获取个人工作台数据，包含待处理 Issue 统计（按严重程度分档）、超时预警。", Category: "stats", Icon: "fa-desktop", Color: "#10b981", Tags: `["只读","需认证"]`, Params: `[{"n":"admin_view","t":"boolean","r":false,"d":"false"}]`, Phrases: `["我的待办","有哪些问题需要处理","超时预警"]`, ResponseExample: `{"data":{"pending_by_severity":{"critical":2,"high":5,"medium":8,"low":12},"overdue_warnings":[{"issue_id":301,"file":"auth.go","message":"硬编码密钥","overdue_hours":48}],"total_pending":27}}`, SortOrder: 18},
		{ToolName: "get_dashboard_stats", DisplayName: "大盘统计查询", Description: "获取代码评审大盘统计（任务量、成功率、失败率、Top 项目）。支持 today/week/month。", Category: "stats", Icon: "fa-chart-bar", Color: "#10b981", Tags: `["只读","需认证"]`, Params: `[{"n":"period","t":"string","r":false,"d":"today|week|month"}]`, Phrases: `["这周评审了多少 MR","成功率多少","团队评审负载如何"]`, ResponseExample: `{"data":{"period":"week","total_tasks":48,"success_rate":0.89,"failure_rate":0.06,"top_projects":[{"project_id":12,"name":"test-app","task_count":15}]}}`, SortOrder: 19},
		{ToolName: "get_token_usage", DisplayName: "Token 用量查询", Description: "查看 AI 评审 Token 消耗和成本（按天汇总）。支持 day/project/model 分组。", Category: "stats", Icon: "fa-coins", Color: "#10b981", Tags: `["只读","需认证"]`, Params: `[{"n":"group_by","t":"string","r":false,"d":"day|project|model"}]`, Phrases: `["Token 花了多少","AI 调用成本","模型 usage"]`, ResponseExample: `{"data":[{"date":"2026-08-28","input_tokens":1250000,"output_tokens":320000,"cost_usd":2.15}],"total_cost_usd":15.6}`, SortOrder: 20},
	}

	for _, tool := range tools {
		var existing MCPTool
		if err := SilentFirst(DB.Where("tool_name = ?", tool.ToolName), &existing); err != nil {
			if err := DB.Create(&tool).Error; err != nil {
				zap.L().Warn("init mcp tool failed", zap.String("tool_name", tool.ToolName), zap.Error(err))
			} else {
				zap.L().Info("init mcp tool", zap.String("tool_name", tool.ToolName))
			}
		} else {
			if err := DB.Model(&existing).Updates(map[string]interface{}{
				"display_name":     tool.DisplayName,
				"description":      tool.Description,
				"category":         tool.Category,
				"icon":             tool.Icon,
				"color":            tool.Color,
				"tags":             tool.Tags,
				"params":           tool.Params,
				"phrases":          tool.Phrases,
				"requires":         tool.Requires,
				"response_example": tool.ResponseExample,
				"dangerous":        tool.Dangerous,
				"sort_order":       tool.SortOrder,
			}).Error; err != nil {
				zap.L().Warn("update mcp tool failed", zap.String("tool_name", tool.ToolName), zap.Error(err))
			}
		}
	}
}
