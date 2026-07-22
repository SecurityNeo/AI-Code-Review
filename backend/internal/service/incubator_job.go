package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// IncubatorJobRunner polls pending jobs and executes them asynchronously.
type IncubatorJobRunner struct {
	mu       sync.Mutex
	running  map[uint]bool
	incubSvc *IncubatorService
	embedSvc *EmbeddingService
}

// NewIncubatorJobRunner creates a new runner.
func NewIncubatorJobRunner(incubSvc *IncubatorService, embedSvc *EmbeddingService) *IncubatorJobRunner {
	return &IncubatorJobRunner{
		running:  make(map[uint]bool),
		incubSvc: incubSvc,
		embedSvc: embedSvc,
	}
}

// Start begins a background goroutine that polls every 10s.
func (r *IncubatorJobRunner) Start() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			r.pollAndRun()
		}
	}()
	zap.L().Info("incubator job runner started")
}

// pollAndRun fetches pending jobs and executes them.
func (r *IncubatorJobRunner) pollAndRun() {
	var jobs []model.RuleIncubationJob
	if err := model.DB.Where("status = ?", "pending").Order("created_at ASC").Limit(5).Find(&jobs).Error; err != nil {
		zap.L().Warn("poll incubator jobs failed", zap.Error(err))
		return
	}
	for _, job := range jobs {
		r.mu.Lock()
		if r.running[job.ID] { // already executing
			r.mu.Unlock()
			continue
		}
		r.running[job.ID] = true
		r.mu.Unlock()

		go r.executeJob(job)
	}
}

// executeJob dispatches to the correct handler by job_type.
func (r *IncubatorJobRunner) executeJob(job model.RuleIncubationJob) {
	defer func() {
		r.mu.Lock()
		delete(r.running, job.ID)
		r.mu.Unlock()
	}()

	// Mark as running immediately so it won't be picked up by the next poll.
	start := time.Now()
	if dbErr := model.DB.Model(&model.RuleIncubationJob{}).Where("id = ?", job.ID).Updates(map[string]any{
		"status":     "running",
		"started_at": &start,
	}).Error; dbErr != nil {
		zap.L().Warn("mark job as running failed", zap.Uint("job_id", job.ID), zap.Error(dbErr))
	}

	var jobErr error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				jobErr = fmt.Errorf("panic: %v", rec)
				zap.L().Error("incubator job panic recovered",
					zap.Uint("job_id", job.ID),
					zap.String("job_type", job.JobType),
					zap.Any("panic", rec))
			}
		}()
		jobErr = r.dispatch(job)
	}()

	now := time.Now()
	updates := map[string]any{
		"completed_at": &now,
	}
	if jobErr != nil {
		updates["status"] = "failed"
		updates["error_msg"] = jobErr.Error()
		zap.L().Error("incubator job failed", zap.Uint("job_id", job.ID), zap.String("job_type", job.JobType), zap.Error(jobErr))
	} else {
		updates["status"] = "success"
		zap.L().Info("incubator job completed", zap.Uint("job_id", job.ID), zap.String("job_type", job.JobType))
	}
	if dbErr := model.DB.Model(&model.RuleIncubationJob{}).Where("id = ?", job.ID).Updates(updates).Error; dbErr != nil {
		zap.L().Warn("update job status failed", zap.Uint("job_id", job.ID), zap.Error(dbErr))
	}
}

// dispatch routes the job to its handler.
func (r *IncubatorJobRunner) dispatch(job model.RuleIncubationJob) error {
	switch job.JobType {
	case "cluster":
		return r.handleCluster(job)
	case "retro_match":
		return r.handleRetroMatch(job)
	case "health_check":
		return r.handleHealthCheck(job)
	case "refine":
		return r.handleRefine(job)
	case "similar_check":
		return r.handleSimilarCheck(job)
	case "sandbox_test":
		return r.handleSandboxTest(job)
	default:
		return fmt.Errorf("unknown job type: %s", job.JobType)
	}
}

// --- Cluster Job ---
func (r *IncubatorJobRunner) handleCluster(job model.RuleIncubationJob) error {
	var params struct {
		TimeRangeDays int      `json:"time_range_days"`
		Languages     []string `json:"languages"`
		MinGroupSize  int      `json:"min_group_size"`
	}
	if err := json.Unmarshal([]byte(job.Params), &params); err != nil {
		params.TimeRangeDays = r.incubSvc.getConfig().ClusterTimeWindowDays
		params.MinGroupSize = r.incubSvc.getConfig().ClusterMinGroupSize
	}

	now := time.Now()
	model.DB.Model(&model.RuleIncubationJob{}).Where("id = ?", job.ID).Updates(map[string]any{
		"started_at": &now,
	})

	summary, err := r.incubSvc.doClusterIssues(params.TimeRangeDays, params.Languages, params.MinGroupSize)
	completedAt := time.Now()
	updates := map[string]any{"completed_at": &completedAt}
	if err != nil {
		updates["status"] = "failed"
		updates["error_msg"] = err.Error()
	} else {
		updates["status"] = "success"
		updates["result_summary"] = summary
	}
	model.DB.Model(&model.RuleIncubationJob{}).Where("id = ?", job.ID).Updates(updates)
	return err
}

// --- Retro Match Job ---
func (r *IncubatorJobRunner) handleRetroMatch(job model.RuleIncubationJob) error {
	var params struct {
		RuleID       uint    `json:"rule_id"`
		LookbackDays int     `json:"lookback_days"`
		Threshold    float64 `json:"threshold"`
	}
	if err := json.Unmarshal([]byte(job.Params), &params); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return r.incubSvc.runRetroMatch(params.RuleID, params.LookbackDays, params.Threshold)
}

// --- Health Check Job ---
func (r *IncubatorJobRunner) handleHealthCheck(job model.RuleIncubationJob) error {
	return r.incubSvc.runHealthCheck()
}

// --- Refine Job (LLM Prompt refinement) ---
func (r *IncubatorJobRunner) handleRefine(job model.RuleIncubationJob) error {
	var params struct {
		IncubationID uint `json:"incubation_id"`
	}
	if err := json.Unmarshal([]byte(job.Params), &params); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return r.incubSvc.runRefine(params.IncubationID)
}

// --- Similar Rule Check ---
func (r *IncubatorJobRunner) handleSimilarCheck(job model.RuleIncubationJob) error {
	var params struct {
		IncubationID uint `json:"incubation_id"`
	}
	if err := json.Unmarshal([]byte(job.Params), &params); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return r.incubSvc.runSimilarCheck(params.IncubationID)
}

// --- Sandbox Test Job ---
func (r *IncubatorJobRunner) handleSandboxTest(job model.RuleIncubationJob) error {
	var params struct {
		IncubationID uint `json:"incubation_id"`
	}
	if err := json.Unmarshal([]byte(job.Params), &params); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return r.incubSvc.runSandboxTest(params.IncubationID)
}

// QueueJob inserts a new pending job into the database.
func QueueJob(jobType string, params map[string]any) (*model.RuleIncubationJob, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	job := &model.RuleIncubationJob{
		JobType:   jobType,
		Status:    "pending",
		Params:    string(paramsJSON),
		CreatedAt: time.Now(),
	}
	if err := model.DB.Create(job).Error; err != nil {
		return nil, err
	}
	return job, nil
}
