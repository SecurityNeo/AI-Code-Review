package model

import "time"

// ========== 依赖审查（供应链安全）相关模型 ==========
// 说明：本功能与 AI 评审完全独立，不写 review_issues、不发通知。
// 约束：不修改 VulnerabilityRecord（只读复用）。

// PackageLicense 许可证数据（全局共享，跨组织）
type PackageLicense struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Ecosystem   string     `gorm:"size:32;not null;uniqueIndex:uk_pkg_license,priority:1" json:"ecosystem"` // Go/npm/pypi/maven
	PackageName string     `gorm:"size:255;not null;uniqueIndex:uk_pkg_license,priority:2" json:"package_name"`
	Version     string     `gorm:"size:128;not null;uniqueIndex:uk_pkg_license,priority:3" json:"version"`
	LicenseSPDX string     `gorm:"type:text;not null" json:"license_spdx"`
	LicenseRaw  string     `gorm:"type:text" json:"license_raw"`
	DataSource  string     `gorm:"size:16;not null;default:'sync'" json:"data_source"` // sync/import/manual
	FetchedAt   *time.Time `json:"fetched_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// PackageImportName 制品导入名映射（全局共享，精确可达性用）
type PackageImportName struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Ecosystem   string     `gorm:"size:32;not null;uniqueIndex:uk_pkg_import,priority:1" json:"ecosystem"` // pypi/maven
	PackageName string     `gorm:"size:255;not null;uniqueIndex:uk_pkg_import,priority:2" json:"package_name"`
	Version     string     `gorm:"size:128;not null;uniqueIndex:uk_pkg_import,priority:3" json:"version"`
	ImportPaths string     `gorm:"type:mediumtext;not null" json:"import_paths"` // JSON array
	Precision   string     `gorm:"size:16;not null;default:'exact'" json:"precision"`
	DataSource  string     `gorm:"size:16;not null;default:'sync'" json:"data_source"`
	ArtifactRef string     `gorm:"size:512" json:"artifact_ref"`
	FetchedAt   *time.Time `json:"fetched_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// DependencyAuditConfig 组织级配置
type DependencyAuditConfig struct {
	ID                     uint      `gorm:"primaryKey" json:"id"`
	OrgID                  uint      `gorm:"column:org_id;not null;uniqueIndex:uk_depaudit_org" json:"org_id"`
	LockfilePrecise        bool      `gorm:"not null;default:true" json:"lockfile_precise"`
	IncludeTransitive      bool      `gorm:"not null;default:false" json:"include_transitive"`
	ReachabilityEnabled    bool      `gorm:"not null;default:false" json:"reachability_enabled"`
	ScheduleEnabled        bool      `gorm:"not null;default:true" json:"schedule_enabled"`
	LicenseComplianceMode  string    `gorm:"size:16;not null;default:'report_only'" json:"license_compliance_mode"`
	AllowedLicenses        string    `gorm:"type:text" json:"allowed_licenses"`
	ForbiddenLicenses      string    `gorm:"type:text" json:"forbidden_licenses"`
	ReviewRequiredLicenses string    `gorm:"type:text" json:"review_required_licenses"`
	DefaultLicenseAction   string    `gorm:"size:16;not null;default:'review'" json:"default_license_action"`
	RunsKeep               int       `gorm:"not null;default:30" json:"runs_keep"`
	TaskKeepDays           int       `gorm:"not null;default:30" json:"task_keep_days"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// DependencyAuditTask 任务（一次触发产生的项目级 run 的容器）
type DependencyAuditTask struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	OrgID               uint       `gorm:"column:org_id;not null;index:idx_depaudit_task_org" json:"org_id"`
	TriggerType         string     `gorm:"size:16;not null" json:"trigger_type"` // manual/scheduled
	Status              string     `gorm:"size:16;not null;index" json:"status"` // queued/running/success/partial/failed/cancelled
	ProjectTotal        int        `gorm:"not null;default:0" json:"project_total"`
	RunRunning          int        `gorm:"not null;default:0" json:"run_running"`
	RunSuccess          int        `gorm:"not null;default:0" json:"run_success"`
	RunFailed           int        `gorm:"not null;default:0" json:"run_failed"`
	RunSkipped          int        `gorm:"not null;default:0" json:"run_skipped"`
	RunCancelled        int        `gorm:"not null;default:0" json:"run_cancelled"`
	DepTotal            int        `gorm:"not null;default:0" json:"dep_total"`
	VulnTotal           int        `gorm:"not null;default:0" json:"vuln_total"`
	VulnCritical        int        `gorm:"not null;default:0" json:"vuln_critical"`
	VulnHigh            int        `gorm:"not null;default:0" json:"vuln_high"`
	VulnMedium          int        `gorm:"not null;default:0" json:"vuln_medium"`
	VulnLow             int        `gorm:"not null;default:0" json:"vuln_low"`
	VulnAllowlisted     int        `gorm:"not null;default:0" json:"vuln_allowlisted"`
	LicenseCompliant    int        `gorm:"not null;default:0" json:"license_compliant"`
	LicenseNoncompliant int        `gorm:"not null;default:0" json:"license_noncompliant"`
	LicenseReview       int        `gorm:"not null;default:0" json:"license_review"`
	LicenseNA           int        `gorm:"not null;default:0" json:"license_na"`
	LicenseUnknown      int        `gorm:"not null;default:0" json:"license_unknown"`
	SnapshotMissing     int        `gorm:"not null;default:0" json:"snapshot_missing"`
	CollectFailed       int        `gorm:"not null;default:0" json:"collect_failed"`
	SBOMKey             string     `gorm:"size:512" json:"sbom_key"`
	ErrorMessage        string     `gorm:"column:error_msg;type:text" json:"error_msg"`
	StartedAt           *time.Time `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at"`
	CreatedBy           uint       `json:"created_by"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// DependencyAuditProjectDep 依赖坐标快照（每个项目当前全部依赖坐标）
type DependencyAuditProjectDep struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	OrgID           uint      `gorm:"column:org_id;not null;index" json:"org_id"`
	ProjectID       uint      `gorm:"not null;uniqueIndex:uk_depaudit_projdep,priority:1;index" json:"project_id"`
	Ecosystem       string    `gorm:"size:32;not null;uniqueIndex:uk_depaudit_projdep,priority:2;index:idx_projdep_pkg,priority:1" json:"ecosystem"`
	PackageName     string    `gorm:"size:255;not null;uniqueIndex:uk_depaudit_projdep,priority:3;index:idx_projdep_pkg,priority:2" json:"package_name"`
	PackageVersion  string    `gorm:"size:128;not null;uniqueIndex:uk_depaudit_projdep,priority:4;index:idx_projdep_pkg,priority:3" json:"package_version"`
	VersionResolved bool      `gorm:"not null;default:true" json:"version_resolved"`
	Direct          bool      `gorm:"not null;default:false" json:"direct"`
	Scope           string    `gorm:"size:32" json:"scope"`
	SourceFiles     string    `gorm:"type:text;not null" json:"source_files"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// DependencyAuditCollectState 项目坐标收集状态
type DependencyAuditCollectState struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	OrgID           uint       `gorm:"column:org_id;not null;index" json:"org_id"`
	ProjectID       uint       `gorm:"not null;uniqueIndex:uk_depaudit_collect" json:"project_id"`
	Branch          string     `gorm:"size:255" json:"branch"`
	LastCommit      string     `gorm:"size:64" json:"last_commit"`
	LastCollectedAt *time.Time `json:"last_collected_at"`
	DepCount        int        `gorm:"not null;default:0" json:"dep_count"`
	Status          string     `gorm:"size:16;not null;default:'success'" json:"status"` // success/failed/skipped
	Error           string     `gorm:"column:error;size:512" json:"error"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// DependencyAuditSyncQueue 许可证/导入名待同步队列
type DependencyAuditSyncQueue struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Ecosystem   string     `gorm:"size:32;not null;uniqueIndex:uk_depaudit_queue,priority:1" json:"ecosystem"`
	Name        string     `gorm:"size:255;not null;uniqueIndex:uk_depaudit_queue,priority:2" json:"name"`
	Version     string     `gorm:"size:128;not null;uniqueIndex:uk_depaudit_queue,priority:3" json:"version"`
	Kind        string     `gorm:"size:16;not null;uniqueIndex:uk_depaudit_queue,priority:4;index:idx_queue_status,priority:1" json:"kind"` // license/import_name
	Status      string     `gorm:"size:16;not null;default:'pending';index:idx_queue_status,priority:2" json:"status"`
	Attempts    int        `gorm:"not null;default:0" json:"attempts"`
	LastError   string     `gorm:"size:512" json:"last_error"`
	NextRetryAt *time.Time `json:"next_retry_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// DependencyAuditJobLog 收集/同步作业日志（可观测）
type DependencyAuditJobLog struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	OrgID      uint       `gorm:"column:org_id;not null;index:idx_joblog_org_kind,priority:1" json:"org_id"`
	Kind       string     `gorm:"size:32;not null;index:idx_joblog_org_kind,priority:2" json:"kind"` // coordinate_collect/license_sync/import_name_sync
	Status     string     `gorm:"size:16;not null" json:"status"`                                    // running/success/failed
	Total      int        `gorm:"not null;default:0" json:"total"`
	Done       int        `gorm:"not null;default:0" json:"done"`
	Failed     int        `gorm:"not null;default:0" json:"failed"`
	Message    string     `gorm:"type:text" json:"message"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// DependencyAuditRun 单项目执行
type DependencyAuditRun struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	TaskID              *uint      `gorm:"index:idx_depaudit_run_task" json:"task_id"`
	Attempt             int        `gorm:"not null;default:1" json:"attempt"`
	OrgID               uint       `gorm:"column:org_id;not null;index:idx_depaudit_run_org" json:"org_id"`
	ProjectID           uint       `gorm:"not null;index:idx_depaudit_run_proj" json:"project_id"`
	Branch              string     `gorm:"size:255;not null" json:"branch"`
	CommitSHA           string     `gorm:"size:64" json:"commit_sha"`
	TriggerType         string     `gorm:"size:16;not null" json:"trigger_type"` // manual/scheduled
	Status              string     `gorm:"size:16;not null;index" json:"status"` // queued/running/success/failed/skipped/cancelled
	SkipReason          string     `gorm:"size:255" json:"skip_reason"`
	DepTotal            int        `gorm:"not null;default:0" json:"dep_total"`
	DepDirect           int        `gorm:"not null;default:0" json:"dep_direct"`
	DepIndirect         int        `gorm:"not null;default:0" json:"dep_indirect"`
	VulnTotal           int        `gorm:"not null;default:0" json:"vuln_total"`
	VulnCritical        int        `gorm:"not null;default:0" json:"vuln_critical"`
	VulnHigh            int        `gorm:"not null;default:0" json:"vuln_high"`
	VulnMedium          int        `gorm:"not null;default:0" json:"vuln_medium"`
	VulnLow             int        `gorm:"not null;default:0" json:"vuln_low"`
	VulnAllowlisted     int        `gorm:"not null;default:0" json:"vuln_allowlisted"`
	LicenseCompliant    int        `gorm:"not null;default:0" json:"license_compliant"`
	LicenseNoncompliant int        `gorm:"not null;default:0" json:"license_noncompliant"`
	LicenseReview       int        `gorm:"not null;default:0" json:"license_review"`
	LicenseNA           int        `gorm:"not null;default:0" json:"license_na"`
	LicenseUnknown      int        `gorm:"not null;default:0" json:"license_unknown"`
	SnapshotCommit      string     `gorm:"size:64" json:"snapshot_commit"`
	SnapshotAt          *time.Time `json:"snapshot_at"`
	CollectError        string     `gorm:"size:512" json:"collect_error"`
	SBOMKey             string     `gorm:"size:512" json:"sbom_key"`
	VulnDBSnapshotAt    *time.Time `json:"vuln_db_snapshot_at"`
	ErrorMessage        string     `gorm:"column:error_msg;type:text" json:"error_msg"`
	StartedAt           *time.Time `json:"started_at"`
	CompletedAt         *time.Time `json:"completed_at"`
	DurationMs          int64      `json:"duration_ms"`
	CreatedBy           uint       `json:"created_by"`
	CreatedAt           time.Time  `json:"created_at"`
}

// DependencyAuditItem 依赖明细（按去重坐标，一坐标一行）
type DependencyAuditItem struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	RunID           uint      `gorm:"not null;uniqueIndex:uk_depaudit_item,priority:1;index" json:"run_id"`
	OrgID           uint      `gorm:"column:org_id;not null;index" json:"org_id"`
	ProjectID       uint      `gorm:"not null;index" json:"project_id"`
	Ecosystem       string    `gorm:"size:32;not null;uniqueIndex:uk_depaudit_item,priority:2" json:"ecosystem"`
	PackageName     string    `gorm:"size:255;not null;uniqueIndex:uk_depaudit_item,priority:3" json:"package_name"`
	PackageVersion  string    `gorm:"size:128;not null;uniqueIndex:uk_depaudit_item,priority:4" json:"package_version"`
	DependencyType  string    `gorm:"size:16;not null" json:"dependency_type"` // direct/indirect
	VersionResolved bool      `gorm:"not null;default:true" json:"version_resolved"`
	SourceFiles     string    `gorm:"type:text;not null" json:"source_files"` // JSON array
	Scope           string    `gorm:"size:32" json:"scope"`
	PURL            string    `gorm:"column:purl;size:512" json:"purl"`
	LicenseExpr     string    `gorm:"type:text" json:"license_expr"`
	LicenseStatus   string    `gorm:"size:16;not null;default:'unknown'" json:"license_status"` // compliant/non_compliant/review/unknown/na
	LicenseMatchMsg string    `gorm:"size:512" json:"license_match_msg"`
	LicenseSource   string    `gorm:"size:16" json:"license_source"`
	Reachable       string    `gorm:"size:16" json:"reachable"` // true/false/unknown
	CreatedAt       time.Time `json:"created_at"`
}

// DependencyAuditVuln 漏洞明细（按 run+坐标+漏洞 去重）
type DependencyAuditVuln struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	RunID          uint      `gorm:"not null;uniqueIndex:uk_depaudit_vuln_item,priority:1;index" json:"run_id"`
	ItemID         uint      `gorm:"not null;uniqueIndex:uk_depaudit_vuln_item,priority:2;index" json:"item_id"`
	OrgID          uint      `gorm:"column:org_id;not null;index" json:"org_id"`
	ProjectID      uint      `gorm:"not null;index" json:"project_id"`
	PackageName    string    `gorm:"size:255;not null" json:"package_name"`
	PackageVersion string    `gorm:"size:128;not null" json:"package_version"`
	VulnID         string    `gorm:"size:64;not null;uniqueIndex:uk_depaudit_vuln_item,priority:3" json:"vuln_id"`
	Aliases        string    `gorm:"size:512" json:"aliases"`
	Severity       string    `gorm:"size:16" json:"severity"`
	FixedVersions  string    `gorm:"type:text" json:"fixed_versions"`
	Summary        string    `gorm:"type:text" json:"summary"`
	Allowlisted    bool      `gorm:"not null;default:false" json:"allowlisted"`
	Reachable      string    `gorm:"size:16" json:"reachable"`
	CreatedAt      time.Time `json:"created_at"`
}

// TableName 显式指定表名（避免命名歧义）
func (DependencyAuditConfig) TableName() string       { return "dependency_audit_configs" }
func (DependencyAuditTask) TableName() string         { return "dependency_audit_tasks" }
func (DependencyAuditRun) TableName() string          { return "dependency_audit_runs" }
func (DependencyAuditItem) TableName() string         { return "dependency_audit_items" }
func (DependencyAuditVuln) TableName() string         { return "dependency_audit_vulns" }
func (DependencyAuditProjectDep) TableName() string   { return "dependency_audit_project_deps" }
func (DependencyAuditCollectState) TableName() string { return "dependency_audit_collect_states" }
func (DependencyAuditSyncQueue) TableName() string    { return "dependency_audit_sync_queue" }
func (DependencyAuditJobLog) TableName() string       { return "dependency_audit_job_logs" }
func (PackageLicense) TableName() string              { return "package_licenses" }
func (PackageImportName) TableName() string           { return "package_import_names" }
