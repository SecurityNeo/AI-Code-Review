package depaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	defaultConcurrency = 3
	projectTimeout     = 10 * time.Minute
)

// scanSem 全局并发信号量：限制进程内同时执行的依赖审查项目数
var scanSem = make(chan struct{}, defaultConcurrency)

// runCancels 运行中 run 的取消函数（跨实例共享）
var runCancels sync.Map // map[uint]context.CancelFunc

// projectLocks 按项目串行化克隆/编目，避免并发竞态
var projectLocks sync.Map // map[uint]*sync.Mutex

func lockProject(projectID uint) *sync.Mutex {
	m, _ := projectLocks.LoadOrStore(projectID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// ScanService 依赖审查扫描服务
type ScanService struct {
	db        *gorm.DB
	workspace string
}

func NewScanService(db *gorm.DB, workspace string) *ScanService {
	return &ScanService{db: db, workspace: workspace}
}

// TriggerProjects 创建任务并为指定项目创建 run（幂等，跳过已有活动 run 的项目）
func (s *ScanService) TriggerProjects(orgID uint, projectIDs []uint, triggerType string, createdBy uint) (uint, error) {
	if len(projectIDs) == 0 {
		return 0, fmt.Errorf("no projects")
	}
	var projects []model.Project
	if err := s.db.Where("id IN ? AND org_id = ?", projectIDs, orgID).Find(&projects).Error; err != nil {
		return 0, err
	}
	return s.createTaskWithRuns(orgID, projects, triggerType, createdBy), nil
}

// TriggerOrg 触发组织内所有项目的扫描任务
func (s *ScanService) TriggerOrg(orgID uint, triggerType string, createdBy uint) (uint, error) {
	var projects []model.Project
	if err := s.db.Where("org_id = ?", orgID).Find(&projects).Error; err != nil {
		return 0, err
	}
	return s.createTaskWithRuns(orgID, projects, triggerType, createdBy), nil
}

func (s *ScanService) createTaskWithRuns(orgID uint, projects []model.Project, triggerType string, createdBy uint) uint {
	task := &model.DependencyAuditTask{
		OrgID: orgID, TriggerType: triggerType, Status: "queued",
		ProjectTotal: len(projects), CreatedBy: createdBy,
	}
	now := time.Now()
	task.StartedAt = &now
	if err := s.db.Create(task).Error; err != nil {
		zap.L().Error("create depaudit task failed", zap.Error(err))
		return 0
	}

	var created []uint
	for i := range projects {
		p := projects[i]
		// 项目级锁：串行化"检查活动 run + 创建"，避免并发触发产生重复 run
		lk := lockProject(p.ID)
		lk.Lock()
		branch := p.GraphBaseBranch
		if branch == "" {
			branch = "main"
		}
		if s.hasActiveRun(p.ID) {
			// 已有进行中的扫描：记录一条 skipped run，保证任务项目数与 run 数一致
			skip := &model.DependencyAuditRun{
				TaskID: &task.ID, Attempt: 1, OrgID: orgID, ProjectID: p.ID,
				Branch: branch, TriggerType: triggerType, Status: "skipped",
				SkipReason: "已有进行中的扫描，本次跳过", CreatedBy: createdBy,
			}
			s.db.Create(skip)
			lk.Unlock()
			continue
		}
		run := &model.DependencyAuditRun{
			TaskID: &task.ID, Attempt: 1, OrgID: orgID, ProjectID: p.ID,
			Branch: branch, TriggerType: triggerType, Status: "queued", CreatedBy: createdBy,
		}
		err := s.db.Create(run).Error
		lk.Unlock()
		if err != nil {
			zap.L().Warn("create depaudit run failed", zap.Uint("project_id", p.ID), zap.Error(err))
			continue
		}
		created = append(created, run.ID)
	}
	if len(created) == 0 {
		now := time.Now()
		s.db.Model(&model.DependencyAuditTask{}).Where("id = ?", task.ID).
			Updates(map[string]interface{}{"status": "failed", "error_msg": "没有可执行的项目（可能均在进行中）", "finished_at": now})
		return task.ID
	}
	s.schedule(created)
	return task.ID
}

func (s *ScanService) hasActiveRun(projectID uint) bool {
	var n int64
	s.db.Model(&model.DependencyAuditRun{}).
		Where("project_id = ? AND status IN ?", projectID, []string{"queued", "running"}).
		Count(&n)
	return n > 0
}

func (s *ScanService) schedule(runIDs []uint) {
	for _, id := range runIDs {
		go func(runID uint) {
			scanSem <- struct{}{}
			defer func() { <-scanSem }()
			s.execute(runID)
		}(id)
	}
}

// execute 执行单个 run
func (s *ScanService) execute(runID uint) {
	ctx, cancel := context.WithTimeout(context.Background(), projectTimeout)
	defer cancel()
	runCancels.Store(runID, cancel)
	defer runCancels.Delete(runID)
	defer func() {
		if r := recover(); r != nil {
			zap.L().Error("dependency-audit run panic", zap.Uint("run_id", runID), zap.Any("panic", r))
			s.finishRun(runID, "failed", fmt.Sprintf("panic: %v", r))
			s.recomputeTaskByRun(runID)
		}
	}()

	var run model.DependencyAuditRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return
	}
	if run.Status != "queued" {
		return
	}
	start := time.Now()
	res := s.db.Model(&model.DependencyAuditRun{}).
		Where("id = ? AND status = ?", runID, "queued").
		Updates(map[string]interface{}{"status": "running", "started_at": start})
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}

	var project model.Project
	if err := s.db.First(&project, run.ProjectID).Error; err != nil {
		s.finishRun(runID, "failed", "project not found")
		s.recomputeTask(run.TaskID)
		return
	}
	cfg, err := GetConfig(s.db, run.OrgID)
	if err != nil {
		cfg = DefaultConfig(run.OrgID)
	}
	manual := run.TriggerType == "manual"

	collectSvc := NewCollectService(s.db)
	collectErrMsg := ""
	repoDir := ""
	effectiveBranch := run.Branch
	// 手动扫描先收集；定时扫描在缺快照时收集
	if manual || !s.snapshotExists(project.ID) {
		_, _, usedBranch, cerr := collectSvc.CollectProject(&project, cfg, manual)
		if cerr != nil {
			collectErrMsg = sanitizeMessage(cerr.Error())
			zap.L().Warn("pre-scan collect failed", zap.Uint("project_id", project.ID), zap.Error(cerr))
		} else if usedBranch != "" {
			effectiveBranch = usedBranch
			if usedBranch != run.Branch {
				s.db.Model(&model.DependencyAuditRun{}).Where("id = ?", runID).Update("branch", usedBranch)
			}
		}
		repoDir = pipeline.GetRepoManager().GetRepoDir(project.ID, effectiveBranch)
	}

	var state model.DependencyAuditCollectState
	s.db.Where("project_id = ?", project.ID).First(&state)

	var deps []model.DependencyAuditProjectDep
	s.db.Where("project_id = ?", project.ID).Find(&deps)
	if len(deps) == 0 {
		reason := "no dependency snapshot/manifest found"
		switch {
		case state.Status == "skipped" && state.Error != "":
			reason = state.Error
		case collectErrMsg != "":
			reason = "坐标收集失败：" + collectErrMsg
		case state.Status == "failed" && state.Error != "":
			reason = "坐标收集失败：" + state.Error
		}
		s.finishRunSkipped(runID, reason)
		s.recomputeTask(run.TaskID)
		return
	}

	items := make([]*model.DependencyAuditItem, 0, len(deps))
	for _, d := range deps {
		dtype := "indirect"
		if d.Direct {
			dtype = "direct"
		}
		items = append(items, &model.DependencyAuditItem{
			RunID: runID, OrgID: run.OrgID, ProjectID: run.ProjectID,
			Ecosystem: d.Ecosystem, PackageName: d.PackageName, PackageVersion: d.PackageVersion,
			DependencyType: dtype, VersionResolved: d.VersionResolved, SourceFiles: d.SourceFiles,
			Scope: d.Scope, PURL: BuildPURL(d.Ecosystem, d.PackageName, d.PackageVersion),
			LicenseStatus: LicenseUnknown,
		})
	}

	// 本地许可证库匹配（不联网）
	lib := LoadLocalLicenses(s.db, items)
	ApplyLocalLicenses(items, lib)
	EnqueueMissingItems(s.db, items, lib)

	for _, it := range items {
		status, msg := EvaluateLicense(it.LicenseExpr, cfg)
		it.LicenseStatus = status
		it.LicenseMatchMsg = msg
	}

	if err := s.db.Create(&items).Error; err != nil {
		s.finishRun(runID, "failed", "persist items failed: "+err.Error())
		s.recomputeTask(run.TaskID)
		return
	}

	match := MatchVulnerabilities(s.db, run.OrgID, items)

	targets := map[uint]bool{}
	for _, v := range match.Vulns {
		if !v.Allowlisted && (v.Severity == "critical" || v.Severity == "high") {
			targets[v.ItemID] = true
		}
	}
	if cfg.ReachabilityEnabled && len(targets) > 0 {
		if repoDir == "" {
			if dir, _, e := ensureRepoWithFallback(project, effectiveBranch); e == nil {
				repoDir = dir
			}
		}
		if repoDir != "" {
			AnalyzeReachability(ctx, s.db, repoDir, items, targets)
			for _, it := range items {
				if targets[it.ID] {
					s.db.Model(&model.DependencyAuditItem{}).Where("id = ?", it.ID).Update("reachable", it.Reachable)
				}
			}
		}
	}
	for _, v := range match.Vulns {
		v.Reachable = itemReachable(items, v.ItemID)
	}
	if len(match.Vulns) > 0 {
		if err := s.db.Create(&match.Vulns).Error; err != nil {
			zap.L().Warn("persist vulns failed", zap.Error(err))
		}
	}

	direct, indirect := 0, 0
	var licC, licN, licR, licNA, licU int
	for _, it := range items {
		if it.DependencyType == "direct" {
			direct++
		} else {
			indirect++
		}
		switch it.LicenseStatus {
		case LicenseCompliant:
			licC++
		case LicenseNoncompliant:
			licN++
		case LicenseReview:
			licR++
		case LicenseNA:
			licNA++
		default:
			licU++
		}
	}

	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			s.finishRun(runID, "failed", "timeout")
		}
		s.recomputeTask(run.TaskID)
		return
	}

	snapAt := state.LastCollectedAt
	updates := map[string]interface{}{
		"status": "success", "completed_at": time.Now(), "duration_ms": time.Since(start).Milliseconds(),
		"dep_total": len(items), "dep_direct": direct, "dep_indirect": indirect,
		"vuln_total": match.Total, "vuln_critical": match.Critical, "vuln_high": match.High,
		"vuln_medium": match.Medium, "vuln_low": match.Low, "vuln_allowlisted": match.Allowlisted,
		"license_compliant": licC, "license_noncompliant": licN, "license_review": licR,
		"license_na": licNA, "license_unknown": licU,
		"vuln_db_snapshot_at": match.SnapshotAt, "snapshot_commit": state.LastCommit, "snapshot_at": snapAt,
		"collect_error": collectErrMsg, "error_msg": "",
	}
	if err := s.db.Model(&model.DependencyAuditRun{}).Where("id = ?", runID).Updates(updates).Error; err != nil {
		zap.L().Error("finalize depaudit run failed", zap.Uint("run_id", runID), zap.Error(err))
		s.finishRun(runID, "failed", "finalize failed: "+err.Error())
		s.recomputeTask(run.TaskID)
		return
	}

	s.recomputeTask(run.TaskID)
}

// recomputeTaskByRun 依据 run 反查任务并重算（用于异常兜底）
func (s *ScanService) recomputeTaskByRun(runID uint) {
	var run model.DependencyAuditRun
	if err := s.db.Select("task_id").First(&run, runID).Error; err != nil {
		return
	}
	s.recomputeTask(run.TaskID)
}

func (s *ScanService) snapshotExists(projectID uint) bool {
	var n int64
	s.db.Model(&model.DependencyAuditProjectDep{}).Where("project_id = ?", projectID).Count(&n)
	return n > 0
}

// RecomputeTask 依据各项目最新 attempt 的 run 重算任务聚合状态
func (s *ScanService) recomputeTask(taskID *uint) {
	if taskID == nil {
		return
	}
	var task model.DependencyAuditTask
	if err := s.db.First(&task, *taskID).Error; err != nil {
		return
	}
	var runs []model.DependencyAuditRun
	s.db.Where("task_id = ?", *taskID).Order("attempt asc").Find(&runs)

	latest := map[uint]model.DependencyAuditRun{}
	for _, r := range runs {
		if cur, ok := latest[r.ProjectID]; !ok || r.Attempt > cur.Attempt {
			latest[r.ProjectID] = r
		}
	}

	var running, success, failed, skipped, cancelled, snapshotMissing, collectFailed int
	var depTotal, vulnTotal, vc, vh, vm, vl, va, lc, ln, lr, lna, lu int
	terminal := true
	for _, r := range latest {
		switch r.Status {
		case "success":
			success++
		case "failed":
			failed++
		case "skipped":
			skipped++
		case "cancelled":
			cancelled++
		default:
			running++
			terminal = false
		}
		if r.CollectError != "" {
			collectFailed++
		}
		if r.Status == "skipped" && strings.Contains(r.SkipReason, "snapshot") {
			snapshotMissing++
		}
		depTotal += r.DepTotal
		vulnTotal += r.VulnTotal
		vc += r.VulnCritical
		vh += r.VulnHigh
		vm += r.VulnMedium
		vl += r.VulnLow
		va += r.VulnAllowlisted
		lc += r.LicenseCompliant
		ln += r.LicenseNoncompliant
		lr += r.LicenseReview
		lna += r.LicenseNA
		lu += r.LicenseUnknown
	}

	status := task.Status
	if task.Status == "cancelled" {
		// 已被显式取消：不再被后续 run 完成事件覆盖
		status = "cancelled"
	} else if terminal {
		switch {
		case success == len(latest) && len(latest) > 0:
			status = "success"
		case success > 0:
			status = "partial"
		case cancelled == len(latest) && len(latest) > 0:
			status = "cancelled"
		default:
			status = "failed"
		}
	} else {
		status = "running"
	}

	updates := map[string]interface{}{
		"status": status, "run_running": running, "run_success": success, "run_failed": failed,
		"run_skipped": skipped, "run_cancelled": cancelled,
		"dep_total": depTotal, "vuln_total": vulnTotal,
		"vuln_critical": vc, "vuln_high": vh, "vuln_medium": vm, "vuln_low": vl, "vuln_allowlisted": va,
		"license_compliant": lc, "license_noncompliant": ln, "license_review": lr,
		"license_na": lna, "license_unknown": lu,
		"snapshot_missing": snapshotMissing, "collect_failed": collectFailed,
	}
	if terminal {
		now := time.Now()
		updates["finished_at"] = now
	}
	s.db.Model(&model.DependencyAuditTask{}).Where("id = ?", *taskID).Updates(updates)

	if terminal && task.FinishedAt == nil {
		s.finalizeTask(*taskID)
	}
}

// finalizeTask 生成任务级 SBOM 并落库
func (s *ScanService) finalizeTask(taskID uint) {
	var task model.DependencyAuditTask
	if err := s.db.First(&task, taskID).Error; err != nil {
		return
	}
	data, err := GenerateTaskSBOM(s.db, &task)
	if err != nil {
		zap.L().Warn("generate task sbom failed", zap.Uint("task_id", taskID), zap.Error(err))
		return
	}
	provider, err := service.GetObjectStorageProviderForOrg(task.OrgID)
	if err != nil || provider == nil {
		return
	}
	key := fmt.Sprintf("depaudit/org_%d/task_%d/sbom.zip", task.OrgID, task.ID)
	if _, err := provider.Put(context.Background(), key, data); err != nil {
		zap.L().Warn("upload task sbom failed", zap.Uint("task_id", taskID), zap.Error(err))
		return
	}
	s.db.Model(&model.DependencyAuditTask{}).Where("id = ?", taskID).Update("sbom_key", key)
}

// CancelTask 取消任务下所有进行中的 run
func (s *ScanService) CancelTask(taskID, orgID uint) error {
	var task model.DependencyAuditTask
	if err := s.db.Where("id = ? AND org_id = ?", taskID, orgID).First(&task).Error; err != nil {
		return err
	}
	if task.Status == "success" || task.Status == "failed" || task.Status == "cancelled" {
		return fmt.Errorf("task is not cancellable")
	}
	var runs []model.DependencyAuditRun
	s.db.Where("task_id = ? AND status IN ?", taskID, []string{"queued", "running"}).Find(&runs)
	for _, r := range runs {
		if c, ok := runCancels.Load(r.ID); ok {
			if cancel, ok := c.(context.CancelFunc); ok {
				cancel()
			}
		}
		s.db.Model(&model.DependencyAuditRun{}).
			Where("id = ? AND status IN ?", r.ID, []string{"queued", "running"}).
			Updates(map[string]interface{}{"status": "cancelled", "completed_at": time.Now()})
	}
	now := time.Now()
	s.db.Model(&model.DependencyAuditTask{}).Where("id = ?", taskID).
		Updates(map[string]interface{}{"status": "cancelled", "finished_at": now})
	return nil
}

// DeleteTask 删除任务及其 run/items/vulns 与 SBOM 对象
func (s *ScanService) DeleteTask(taskID, orgID uint) error {
	var task model.DependencyAuditTask
	if err := s.db.Where("id = ? AND org_id = ?", taskID, orgID).First(&task).Error; err != nil {
		return err
	}
	if task.SBOMKey != "" {
		if provider, err := service.GetObjectStorageProviderForOrg(task.OrgID); err == nil && provider != nil {
			if err := provider.Delete(context.Background(), task.SBOMKey); err != nil {
				zap.L().Warn("delete task sbom failed", zap.String("key", task.SBOMKey), zap.Error(err))
			}
		}
	}
	var runs []model.DependencyAuditRun
	s.db.Where("task_id = ?", taskID).Find(&runs)
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, r := range runs {
			if err := tx.Where("run_id = ?", r.ID).Delete(&model.DependencyAuditItem{}).Error; err != nil {
				return err
			}
			if err := tx.Where("run_id = ?", r.ID).Delete(&model.DependencyAuditVuln{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("task_id = ?", taskID).Delete(&model.DependencyAuditRun{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.DependencyAuditTask{}, taskID).Error
	})
}

// RetryTask 对失败/跳过/取消的项目在同一任务内追加 run
func (s *ScanService) RetryTask(taskID, orgID uint, projectIDs []uint) (int, error) {
	var task model.DependencyAuditTask
	if err := s.db.Where("id = ? AND org_id = ?", taskID, orgID).First(&task).Error; err != nil {
		return 0, err
	}
	var runs []model.DependencyAuditRun
	q := s.db.Where("task_id = ?", taskID)
	if len(projectIDs) > 0 {
		q = q.Where("project_id IN ?", projectIDs)
	}
	q.Find(&runs)

	latest := map[uint]model.DependencyAuditRun{}
	for _, r := range runs {
		if cur, ok := latest[r.ProjectID]; !ok || r.Attempt > cur.Attempt {
			latest[r.ProjectID] = r
		}
	}

	created := 0
	var ids []uint
	for _, r := range latest {
		if r.Status != "failed" && r.Status != "skipped" && r.Status != "cancelled" {
			continue
		}
		var project model.Project
		if err := s.db.First(&project, r.ProjectID).Error; err != nil {
			continue
		}
		lk := lockProject(r.ProjectID)
		lk.Lock()
		if s.hasActiveRun(r.ProjectID) {
			lk.Unlock()
			continue
		}
		newRun := &model.DependencyAuditRun{
			TaskID: &task.ID, Attempt: r.Attempt + 1, OrgID: orgID, ProjectID: r.ProjectID,
			Branch: r.Branch, TriggerType: "manual", Status: "queued",
		}
		err := s.db.Create(newRun).Error
		lk.Unlock()
		if err != nil {
			zap.L().Warn("retry: create run failed", zap.Uint("project_id", r.ProjectID), zap.Error(err))
			continue
		}
		ids = append(ids, newRun.ID)
		created++
	}
	if created > 0 {
		s.db.Model(&model.DependencyAuditTask{}).Where("id = ?", taskID).
			Updates(map[string]interface{}{"status": "running", "finished_at": nil})
		s.schedule(ids)
	}
	return created, nil
}

// RecoverStale 回收滞留的 run（仅超过单项目超时的），并重算其所在任务状态
func (s *ScanService) RecoverStale() {
	cutoff := time.Now().Add(-projectTimeout - time.Minute)
	var stale []model.DependencyAuditRun
	s.db.Where("status IN ? AND (started_at IS NULL OR started_at < ?)", []string{"queued", "running"}, cutoff).
		Find(&stale)
	if len(stale) == 0 {
		return
	}
	ids := make([]uint, 0, len(stale))
	taskIDs := map[uint]bool{}
	for _, r := range stale {
		ids = append(ids, r.ID)
		if r.TaskID != nil {
			taskIDs[*r.TaskID] = true
		}
	}
	s.db.Model(&model.DependencyAuditRun{}).Where("id IN ?", ids).
		Updates(map[string]interface{}{"status": "failed", "error_msg": "stale after restart"})
	zap.L().Info("recovered stale dependency-audit runs", zap.Int("count", len(ids)))
	// 重算受影响任务，避免任务永久停留在 running
	for tid := range taskIDs {
		id := tid
		s.recomputeTask(&id)
	}
}

// CleanupExpiredTasks 清理超期任务及其 run/items/vulns 与 SBOM 对象
func (s *ScanService) CleanupExpiredTasks(orgID uint, keepDays int) {
	if keepDays <= 0 {
		keepDays = 30
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays)
	var tasks []model.DependencyAuditTask
	s.db.Where("org_id = ? AND created_at < ?", orgID, cutoff).Order("id asc").Limit(200).Find(&tasks)
	for _, t := range tasks {
		if t.SBOMKey != "" {
			if provider, err := service.GetObjectStorageProviderForOrg(t.OrgID); err == nil && provider != nil {
				_ = provider.Delete(context.Background(), t.SBOMKey)
			}
		}
		var runs []model.DependencyAuditRun
		s.db.Where("task_id = ?", t.ID).Find(&runs)
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			for _, r := range runs {
				tx.Where("run_id = ?", r.ID).Delete(&model.DependencyAuditItem{})
				tx.Where("run_id = ?", r.ID).Delete(&model.DependencyAuditVuln{})
			}
			tx.Where("task_id = ?", t.ID).Delete(&model.DependencyAuditRun{})
			return tx.Delete(&model.DependencyAuditTask{}, t.ID).Error
		}); err != nil {
			zap.L().Warn("cleanup task failed", zap.Uint("task_id", t.ID), zap.Error(err))
		}
	}

	// 清理已完成的同步队列项与超期作业日志，避免无限增长
	s.db.Where("status IN ? AND updated_at < ?", []string{"done", "skipped"}, cutoff).
		Delete(&model.DependencyAuditSyncQueue{})
	s.db.Where("org_id = ? AND created_at < ?", orgID, cutoff).
		Delete(&model.DependencyAuditJobLog{})
}

// ========== 辅助 ==========

// ensureRepoWithFallback 克隆仓库；配置分支不存在时回退 main/master
func ensureRepoWithFallback(project model.Project, branch string) (string, string, error) {
	if branch == "" {
		branch = "main"
	}
	mgr := pipeline.GetRepoManager()
	dir, err := mgr.EnsureRepo(project.ProjectPath, project.AccessToken, branch, project.ID)
	if err == nil {
		return dir, branch, nil
	}
	firstErr := err
	for _, alt := range []string{"main", "master"} {
		if alt == branch {
			continue
		}
		if d, e := mgr.EnsureRepo(project.ProjectPath, project.AccessToken, alt, project.ID); e == nil {
			zap.L().Info("fallback branch used",
				zap.Uint("project_id", project.ID), zap.String("configured", branch), zap.String("used", alt))
			return d, alt, nil
		}
	}
	return "", branch, firstErr
}

// urlCredRE 匹配 URL 中的凭据
var urlCredRE = regexp.MustCompile(`://[^/@\s]+@`)

func sanitizeMessage(s string) string {
	return urlCredRE.ReplaceAllString(s, "://***@")
}

// finishRunSkipped 写入 skipped 终态并记录可诊断的 skip_reason
func (s *ScanService) finishRunSkipped(runID uint, reason string) {
	now := time.Now()
	s.db.Model(&model.DependencyAuditRun{}).
		Where("id = ? AND status IN ?", runID, []string{"queued", "running"}).
		Updates(map[string]interface{}{
			"status": "skipped", "skip_reason": truncateStr(sanitizeMessage(reason), 250),
			"completed_at": now,
		})
}

func (s *ScanService) finishRun(runID uint, status, msg string) {
	now := time.Now()
	s.db.Model(&model.DependencyAuditRun{}).
		Where("id = ? AND status IN ?", runID, []string{"queued", "running"}).
		Updates(map[string]interface{}{
			"status": status, "error_msg": sanitizeMessage(msg), "completed_at": now,
		})
}

func itemReachable(items []*model.DependencyAuditItem, itemID uint) string {
	for _, it := range items {
		if it.ID == itemID {
			return it.Reachable
		}
	}
	return ""
}

func readCommitSHA(repoDir string) string {
	data, err := os.ReadFile(filepath.Join(repoDir, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref: ") {
		ref := strings.TrimPrefix(head, "ref: ")
		if b, err := os.ReadFile(filepath.Join(repoDir, ".git", ref)); err == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}
	return head
}

func toJSONArray(list []string) string {
	if list == nil {
		list = []string{}
	}
	return mustJSON(list)
}

// BuildPURL 生成包 purl
func BuildPURL(eco, name, version string) string {
	switch strings.ToLower(eco) {
	case "go":
		return fmt.Sprintf("pkg:golang/%s@%s", name, version)
	case "npm":
		enc := strings.ReplaceAll(name, "@", "%40")
		return fmt.Sprintf("pkg:npm/%s@%s", enc, version)
	case "pypi":
		return fmt.Sprintf("pkg:pypi/%s@%s", normalizeName("pypi", name), version)
	case "maven":
		parts := strings.SplitN(name, ":", 2)
		if len(parts) == 2 {
			return fmt.Sprintf("pkg:maven/%s/%s@%s", parts[0], parts[1], version)
		}
		return fmt.Sprintf("pkg:maven/%s@%s", name, version)
	default:
		return fmt.Sprintf("pkg:%s/%s@%s", strings.ToLower(eco), name, version)
	}
}
