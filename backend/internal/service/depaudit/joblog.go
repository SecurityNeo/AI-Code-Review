package depaudit

import (
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"gorm.io/gorm"
)

// 作业类型
const (
	JobCollectCoordinates = "coordinate_collect"
	JobSyncLicense        = "license_sync"
	JobSyncImportNames    = "import_name_sync"
)

// jobTracker 记录一次作业的进度并落库
type jobTracker struct {
	mu     sync.Mutex
	id     uint
	db     *gorm.DB
	done   int
	failed int
	total  int
}

func startJobLog(db *gorm.DB, orgID uint, kind string, total int) *jobTracker {
	now := time.Now()
	rec := &model.DependencyAuditJobLog{
		OrgID: orgID, Kind: kind, Status: "running", Total: total,
		StartedAt: &now, CreatedAt: now,
	}
	db.Create(rec)
	return &jobTracker{id: rec.ID, db: db, total: total}
}

func (t *jobTracker) add(done, failed int) {
	t.mu.Lock()
	t.done += done
	t.failed += failed
	d, f := t.done, t.failed
	t.mu.Unlock()
	t.db.Model(&model.DependencyAuditJobLog{}).Where("id = ?", t.id).
		Updates(map[string]interface{}{"done": d, "failed": f})
}

func (t *jobTracker) finish(msg string) {
	status := "success"
	t.mu.Lock()
	failed := t.failed
	done := t.done
	t.mu.Unlock()
	if failed > 0 {
		status = "failed"
	}
	now := time.Now()
	t.db.Model(&model.DependencyAuditJobLog{}).Where("id = ?", t.id).Updates(map[string]interface{}{
		"status": status, "message": msg, "done": done, "failed": failed, "finished_at": now,
	})
}

// LatestJobLogs 返回各类作业最近一次记录
func LatestJobLogs(db *gorm.DB, orgID uint) []model.DependencyAuditJobLog {
	kinds := []string{JobCollectCoordinates, JobSyncLicense, JobSyncImportNames}
	out := make([]model.DependencyAuditJobLog, 0, len(kinds))
	for _, k := range kinds {
		var l model.DependencyAuditJobLog
		if err := db.Where("org_id = ? AND kind = ?", orgID, k).Order("id desc").First(&l).Error; err == nil {
			out = append(out, l)
		}
	}
	return out
}
