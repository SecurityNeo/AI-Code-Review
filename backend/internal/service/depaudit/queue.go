package depaudit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	syncBatchSize    = 200
	syncConcurrency  = 3
	syncMaxAttempts  = 8
	syncWorkerPeriod = 30 * time.Second
)

// coordRow 去重坐标查询行
type coordRow struct {
	Ecosystem      string
	PackageName    string
	PackageVersion string
}

type coordTuple struct {
	Ecosystem, Name, Version string
}

// EnqueueMissingForOrg 将组织内快照中、本地库缺失的坐标写入同步队列
func EnqueueMissingForOrg(db *gorm.DB, orgID uint) (licCount int, importCount int, err error) {
	var coords []coordRow
	if err := db.Model(&model.DependencyAuditProjectDep{}).
		Select("ecosystem, package_name, package_version").
		Where("org_id = ? AND ((version_resolved = ? AND package_version <> '') OR (package_version = '' AND ecosystem IN ?))",
			orgID, true, []string{"pypi", "PyPI", "PYPI"}).
		Group("ecosystem, package_name, package_version").
		Find(&coords).Error; err != nil {
		return 0, 0, err
	}
	if len(coords) == 0 {
		return 0, 0, nil
	}
	tuples := make([]coordTuple, 0, len(coords))
	for _, c := range coords {
		tuples = append(tuples, coordTuple{c.Ecosystem, c.PackageName, c.PackageVersion})
	}
	licCount = enqueueKind(db, tuples, SyncKindLicense)
	importCount = enqueueKind(db, tuples, SyncKindImportNames)
	return licCount, importCount, nil
}

func enqueueKind(db *gorm.DB, coords []coordTuple, kind string) int {
	// 载入本地库已有坐标集合，避免重复入队
	have := map[string]bool{}
	switch kind {
	case SyncKindLicense:
		var rows []model.PackageLicense
		db.Model(&model.PackageLicense{}).Select("ecosystem, package_name, version").Find(&rows)
		for _, r := range rows {
			have[candidateKey(r.Ecosystem, r.PackageName)+"|"+r.Version] = true
		}
	case SyncKindImportNames:
		var rows []model.PackageImportName
		db.Model(&model.PackageImportName{}).Select("ecosystem, package_name, version").Find(&rows)
		for _, r := range rows {
			have[candidateKey(r.Ecosystem, r.PackageName)+"|"+r.Version] = true
		}
	}

	seen := map[string]bool{}
	var batch []*model.DependencyAuditSyncQueue
	for _, c := range coords {
		eco := c.Ecosystem
		if kind == SyncKindImportNames && (!isImportNameEco(eco) || c.Version == "") {
			continue
		}
		key := candidateKey(eco, c.Name) + "|" + c.Version
		if have[key] || seen[key] {
			continue
		}
		seen[key] = true
		batch = append(batch, &model.DependencyAuditSyncQueue{
			Ecosystem: eco, Name: c.Name, Version: c.Version, Kind: kind, Status: "pending",
		})
	}
	if len(batch) == 0 {
		return 0
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(batch, 500).Error; err != nil {
		zap.L().Warn("enqueue sync coords failed", zap.String("kind", kind), zap.Error(err))
		return 0
	}
	return len(batch)
}

func isImportNameEco(eco string) bool {
	switch eco {
	case "pypi", "maven", "PyPI", "Maven":
		return true
	}
	return false
}

func isPypiEco(eco string) bool {
	switch eco {
	case "pypi", "PyPI", "PYPI":
		return true
	}
	return false
}

// SyncQueueStats 队列统计
func SyncQueueStats(db *gorm.DB) map[string]interface{} {
	res := map[string]interface{}{}
	for _, st := range []string{"pending", "running", "done", "failed", "skipped"} {
		var c int64
		db.Model(&model.DependencyAuditSyncQueue{}).Where("status = ?", st).Count(&c)
		res[st] = c
	}
	return res
}

// ListSyncQueue 队列分页
func ListSyncQueue(db *gorm.DB, status string, page, size int, keyword string) ([]model.DependencyAuditSyncQueue, int64) {
	q := db.Model(&model.DependencyAuditSyncQueue{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword != "" {
		q = q.Where("name LIKE ?", "%"+keyword+"%")
	}
	var total int64
	q.Session(&gorm.Session{}).Count(&total)
	var rows []model.DependencyAuditSyncQueue
	q.Session(&gorm.Session{}).Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows)
	return rows, total
}

// ProcessSyncBatch 处理一批待同步队列项；返回 (处理条数, 失败条数)。
func ProcessSyncBatch(ctx context.Context, db *gorm.DB, kind string) (processed, failed int) {
	now := time.Now()
	var items []model.DependencyAuditSyncQueue
	db.Where("kind = ? AND status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", kind, "pending", now).
		Order("id asc").Limit(syncBatchSize).Find(&items)
	if len(items) == 0 {
		return 0, 0
	}

	client := NewLicenseClient()
	sem := make(chan struct{}, syncConcurrency)
	var wg sync.WaitGroup
	var failCount int32
	for i := range items {
		it := items[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					zap.L().Error("sync queue item panic", zap.Uint("id", it.ID), zap.Any("panic", r))
					atomic.AddInt32(&failCount, 1)
				}
			}()
			if !processQueueItem(ctx, db, client, &it) {
				atomic.AddInt32(&failCount, 1)
			}
		}()
	}
	wg.Wait()
	return len(items), int(atomic.LoadInt32(&failCount))
}

func processQueueItem(ctx context.Context, db *gorm.DB, client *LicenseClient, it *model.DependencyAuditSyncQueue) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch it.Kind {
	case SyncKindImportNames:
		rec := ensureImportNames(reqCtx, db, it.Ecosystem, it.Name, it.Version)
		if rec == nil {
			markQueue(ctx, db, it, "skipped", "no artifact/import names", false)
			return false
		}
		markQueue(ctx, db, it, "done", "", false)
		return true
	default: // license
		// 未固定版本：仅 PyPI 支持按最新版本估算
		if it.Version == "" {
			if !strings.EqualFold(it.Ecosystem, "pypi") {
				markQueue(ctx, db, it, "skipped", "no version", false)
				return false
			}
			latest, err := latestPyPIVersion(reqCtx, it.Name)
			if err != nil {
				markQueue(ctx, db, it, "pending", err.Error(), true)
				return false
			}
			expr, raw, err := client.VersionLicense(reqCtx, it.Ecosystem, it.Name, latest)
			if err != nil {
				markQueue(ctx, db, it, "pending", err.Error(), true)
				return false
			}
			if err := upsertLicense(db, &model.PackageLicense{
				Ecosystem: it.Ecosystem, PackageName: it.Name, Version: "",
				LicenseSPDX: expr, LicenseRaw: "latest=" + latest + ";" + raw, DataSource: "latest",
			}); err != nil {
				markQueue(ctx, db, it, "pending", err.Error(), true)
				return false
			}
			markQueue(ctx, db, it, "done", "", false)
			return true
		}
		expr, raw, err := client.VersionLicense(reqCtx, it.Ecosystem, it.Name, it.Version)
		if err != nil {
			markQueue(ctx, db, it, "pending", err.Error(), true)
			return false
		}
		if err := upsertLicense(db, &model.PackageLicense{
			Ecosystem: it.Ecosystem, PackageName: it.Name, Version: it.Version,
			LicenseSPDX: expr, LicenseRaw: raw, DataSource: "sync",
		}); err != nil {
			markQueue(ctx, db, it, "pending", err.Error(), true)
			return false
		}
		markQueue(ctx, db, it, "done", "", false)
		return true
	}
}

// markQueue 更新队列状态；retry=true 时按退避安排重试，超过上限置 failed。
func markQueue(_ context.Context, db *gorm.DB, it *model.DependencyAuditSyncQueue, status, errMsg string, retry bool) {
	updates := map[string]interface{}{"status": status, "last_error": truncateStr(errMsg, 500)}
	if retry {
		attempts := it.Attempts + 1
		updates["attempts"] = attempts
		if attempts >= syncMaxAttempts {
			updates["status"] = "failed"
			updates["next_retry_at"] = nil
		} else {
			backoff := time.Duration(1<<uint(attempts)) * time.Minute
			if backoff > 24*time.Hour {
				backoff = 24 * time.Hour
			}
			next := time.Now().Add(backoff)
			updates["status"] = "pending"
			updates["next_retry_at"] = &next
		}
	}
	db.Model(&model.DependencyAuditSyncQueue{}).Where("id = ?", it.ID).Updates(updates)
}

// syncGuard 保证同一时间只有一个同步循环在处理队列，避免 worker 与"立即同步"重复处理同一批
var syncGuard int32

func tryLockSync() bool { return atomic.CompareAndSwapInt32(&syncGuard, 0, 1) }
func unlockSync()       { atomic.StoreInt32(&syncGuard, 0) }

// drainKind 处理指定类型的队列直到清空
func drainKind(ctx context.Context, db *gorm.DB, kind string) (processed, failed int) {
	for {
		n, f := ProcessSyncBatch(ctx, db, kind)
		processed += n
		failed += f
		if n < syncBatchSize {
			break
		}
	}
	return processed, failed
}

// drainQueue 处理许可证与导入名两类队列
func drainQueue(ctx context.Context, db *gorm.DB) (processed, failed int) {
	lp, lf := drainKind(ctx, db, SyncKindLicense)
	ip, if_ := drainKind(ctx, db, SyncKindImportNames)
	return lp + ip, lf + if_
}

// StartSyncWorker 启动后台同步 worker（阻塞，需在 goroutine 中调用）
func StartSyncWorker(ctx context.Context, db *gorm.DB) {
	ticker := time.NewTicker(syncWorkerPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !tryLockSync() {
				continue
			}
			drainQueue(ctx, db)
			unlockSync()
		}
	}
}

// RunSyncNow 立即同步：入队本组织缺失坐标并处理队列。
// 若 worker 正在处理，则仅入队后返回（交由 worker 处理），避免并发重复处理。
func RunSyncNow(db *gorm.DB, orgID uint) (lic, imp int) {
	lic, imp, err := EnqueueMissingForOrg(db, orgID)
	if err != nil {
		zap.L().Warn("enqueue missing coords failed", zap.Uint("org_id", orgID), zap.Error(err))
		return 0, 0
	}
	if !tryLockSync() {
		return lic, imp
	}
	defer unlockSync()

	ctx := context.Background()
	licJob := startJobLog(db, orgID, JobSyncLicense, lic)
	lp, lf := drainKind(ctx, db, SyncKindLicense)
	licJob.add(lp, lf)
	licJob.finish(fmt.Sprintf("新增 %d；处理 %d（失败 %d）", lic, lp, lf))

	impJob := startJobLog(db, orgID, JobSyncImportNames, imp)
	ip, if_ := drainKind(ctx, db, SyncKindImportNames)
	impJob.add(ip, if_)
	impJob.finish(fmt.Sprintf("新增 %d；处理 %d（失败 %d）", imp, ip, if_))

	return lic, imp
}

// EnqueueImportNamesForOrg 仅将组织内缺失的导入名映射坐标入队
func EnqueueImportNamesForOrg(db *gorm.DB, orgID uint) int {
	var coords []coordRow
	if err := db.Model(&model.DependencyAuditProjectDep{}).
		Select("ecosystem, package_name, package_version").
		Where("org_id = ? AND version_resolved = ? AND package_version <> '' AND ecosystem IN ?",
			orgID, true, []string{"pypi", "maven", "PyPI", "Maven"}).
		Group("ecosystem, package_name, package_version").
		Find(&coords).Error; err != nil {
		return 0
	}
	tuples := make([]coordTuple, 0, len(coords))
	for _, c := range coords {
		tuples = append(tuples, coordTuple{c.Ecosystem, c.PackageName, c.PackageVersion})
	}
	return enqueueKind(db, tuples, SyncKindImportNames)
}

// RunImportNameSyncNow 立即同步导入名映射：入队本组织缺失坐标并处理导入名队列
func RunImportNameSyncNow(db *gorm.DB, orgID uint) int {
	n := EnqueueImportNamesForOrg(db, orgID)
	if !tryLockSync() {
		return n
	}
	defer unlockSync()
	job := startJobLog(db, orgID, JobSyncImportNames, n)
	p, f := drainKind(context.Background(), db, SyncKindImportNames)
	job.add(p, f)
	job.finish(fmt.Sprintf("新增 %d；处理 %d（失败 %d）", n, p, f))
	return n
}
