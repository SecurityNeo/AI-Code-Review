package depaudit

import (
	"sync"
	"time"
)

// 同步任务类型
const (
	SyncKindLicense     = "license"
	SyncKindImportNames = "import_names"
)

// 同步状态
const (
	SyncIdle    = "idle"
	SyncRunning = "running"
	SyncSuccess = "success"
	SyncFailed  = "failed"
)

// SyncState 同步任务的可观测状态
type SyncState struct {
	Kind       string     `json:"kind"`
	Status     string     `json:"status"` // idle/running/success/failed
	Total      int        `json:"total"`
	Done       int        `json:"done"`
	Failed     int        `json:"failed"`
	Message    string     `json:"message"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

var syncStates = struct {
	sync.Mutex
	m map[string]*SyncState
}{m: map[string]*SyncState{}}

// TryStartSync 尝试开始同步；若该类型已在运行则返回 false
func TryStartSync(kind string, total int) bool {
	syncStates.Lock()
	defer syncStates.Unlock()
	if st, ok := syncStates.m[kind]; ok && st.Status == SyncRunning {
		return false
	}
	now := time.Now()
	syncStates.m[kind] = &SyncState{
		Kind:      kind,
		Status:    SyncRunning,
		Total:     total,
		StartedAt: &now,
	}
	return true
}

// SetSyncProgress 更新同步进度
func SetSyncProgress(kind string, done, failed int) {
	syncStates.Lock()
	defer syncStates.Unlock()
	if st, ok := syncStates.m[kind]; ok {
		st.Done = done
		st.Failed = failed
	}
}

// FinishSync 标记同步结束
func FinishSync(kind string, failed int, message string) {
	syncStates.Lock()
	defer syncStates.Unlock()
	now := time.Now()
	st, ok := syncStates.m[kind]
	if !ok {
		st = &SyncState{Kind: kind, StartedAt: &now}
		syncStates.m[kind] = st
	}
	st.Failed = failed
	st.FinishedAt = &now
	st.Message = message
	if failed > 0 {
		st.Status = SyncFailed
	} else {
		st.Status = SyncSuccess
	}
}

// SyncStatus 返回某类同步的当前状态（不存在时返回 idle）
func SyncStatus(kind string) SyncState {
	syncStates.Lock()
	defer syncStates.Unlock()
	if st, ok := syncStates.m[kind]; ok {
		return *st
	}
	return SyncState{Kind: kind, Status: SyncIdle}
}

// IsSyncing 是否正在同步
func IsSyncing(kind string) bool {
	syncStates.Lock()
	defer syncStates.Unlock()
	st, ok := syncStates.m[kind]
	return ok && st.Status == SyncRunning
}
