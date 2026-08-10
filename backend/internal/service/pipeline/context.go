package pipeline

import (
	"context"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// StageContext 阶段执行上下文
type StageContext interface {
	context.Context

	// 基础信息
	Task() *model.Task
ExecutionID() uint
ParentExecutionID() *uint

	// 数据传递
	GetInput(key string) interface{}
	SetInput(key string, val interface{})
	GetOutput(key string) interface{}
SetOutput(key string, val interface{})

	// 子阶段管理
	CreateChildExecution(stageCode string, batchIndex int) *model.TaskPipelineExecution
	MarkRunning(exec *model.TaskPipelineExecution)
	MarkSuccess(exec *model.TaskPipelineExecution)
	MarkFailed(exec *model.TaskPipelineExecution, errMsg string)
	MarkSkipped(exec *model.TaskPipelineExecution)
	UpdateProgress(exec *model.TaskPipelineExecution, batchCompleted, batchTotal int)

	// 快照
	SaveInputSnapshot(exec *model.TaskPipelineExecution, data map[string]interface{})
	SaveOutputSnapshot(exec *model.TaskPipelineExecution, data map[string]interface{})

	// 广播事件
	BroadcastStageStatus(exec *model.TaskPipelineExecution)
}

// stageContextImpl StageContext 的实现
type stageContextImpl struct {
	context.Context
	task              *model.Task
	executionID       uint
	parentExecutionID *uint
	inputData         map[string]interface{}
	outputData        map[string]interface{}
	db                *gorm.DB
	broadcaster       func(uint, string, map[string]interface{})
	batchFiles        map[int][]interface{}
	selfExec          *model.TaskPipelineExecution // 当前阶段的 execution 记录
}

func newStageContext(ctx context.Context, task *model.Task, db *gorm.DB) *stageContextImpl {
	return &stageContextImpl{
		Context:    ctx,
		task:       task,
		inputData:  make(map[string]interface{}),
		outputData: make(map[string]interface{}),
		db:         db,
		batchFiles: make(map[int][]interface{}),
	}
}

func (c *stageContextImpl) Task() *model.Task { return c.task }
func (c *stageContextImpl) ExecutionID() uint { return c.executionID }
func (c *stageContextImpl) ParentExecutionID() *uint { return c.parentExecutionID }
func (c *stageContextImpl) GetInput(key string) interface{} { return c.inputData[key] }
func (c *stageContextImpl) SetInput(key string, val interface{}) { c.inputData[key] = val }
func (c *stageContextImpl) GetOutput(key string) interface{} { return c.outputData[key] }
func (c *stageContextImpl) SetOutput(key string, val interface{}) { c.outputData[key] = val }

func (c *stageContextImpl) CreateChildExecution(stageCode string, batchIndex int) *model.TaskPipelineExecution {
	exec := &model.TaskPipelineExecution{
		TaskID:     c.task.ID,
		StageCode:  stageCode,
		Status:     model.PipelineStagePending,
		ParentID:   &c.executionID,
		BatchIndex: batchIndex,
	}
	if err := c.db.Create(exec).Error; err != nil {
		zap.L().Error("create child execution failed", zap.Error(err))
	}
	return exec
}

func (c *stageContextImpl) MarkRunning(exec *model.TaskPipelineExecution) {
	now := time.Now()
	exec.Status = model.PipelineStageRunning
	exec.StartedAt = &now
	c.db.Model(exec).Updates(map[string]interface{}{
		"status":     model.PipelineStageRunning,
		"started_at": now,
	})
	c.BroadcastStageStatus(exec)
}

func (c *stageContextImpl) MarkSuccess(exec *model.TaskPipelineExecution) {
	now := time.Now()
	exec.Status = model.PipelineStageSuccess
	exec.CompletedAt = &now
	if exec.StartedAt != nil {
		exec.DurationMs = int(now.Sub(*exec.StartedAt).Milliseconds())
	}
	c.db.Model(exec).Updates(map[string]interface{}{
		"status":        model.PipelineStageSuccess,
		"completed_at":  now,
		"duration_ms":   exec.DurationMs,
		"input_tokens":  exec.InputTokens,
		"output_tokens": exec.OutputTokens,
		"model_name":    exec.ModelName,
		"llm_model_id":  exec.LLMModelID,
	})
	c.BroadcastStageStatus(exec)
}

func (c *stageContextImpl) MarkFailed(exec *model.TaskPipelineExecution, errMsg string) {
	now := time.Now()
	exec.Status = model.PipelineStageFailed
	exec.ErrorMessage = errMsg
	exec.CompletedAt = &now
	if exec.StartedAt != nil {
		exec.DurationMs = int(now.Sub(*exec.StartedAt).Milliseconds())
	}
	c.db.Model(exec).Updates(map[string]interface{}{
		"status":        model.PipelineStageFailed,
		"error_message": errMsg,
		"completed_at":  now,
		"duration_ms":   exec.DurationMs,
		"input_tokens":  exec.InputTokens,
		"output_tokens": exec.OutputTokens,
		"model_name":    exec.ModelName,
		"llm_model_id":  exec.LLMModelID,
	})
	c.BroadcastStageStatus(exec)
}

func (c *stageContextImpl) MarkSkipped(exec *model.TaskPipelineExecution) {
	exec.Status = model.PipelineStageSkipped
	c.db.Model(exec).Update("status", model.PipelineStageSkipped)
	c.BroadcastStageStatus(exec)
}

func (c *stageContextImpl) UpdateProgress(exec *model.TaskPipelineExecution, batchCompleted, batchTotal int) {
	if exec == nil {
		exec = c.selfExec
	}
	if exec == nil {
		return
	}
	exec.BatchCompleted = batchCompleted
	exec.BatchTotal = batchTotal
	c.db.Model(exec).Updates(map[string]interface{}{
		"batch_completed": batchCompleted,
		"batch_total":     batchTotal,
	})
	c.BroadcastStageStatus(exec)
}

func (c *stageContextImpl) SaveInputSnapshot(exec *model.TaskPipelineExecution, data map[string]interface{}) {
	mgr := NewSnapshotManager()
	_, err := mgr.SaveInputSnapshot(exec.ID, data)
	if err != nil {
		zap.L().Warn("save input snapshot failed", zap.Error(err))
	}
}

func (c *stageContextImpl) SaveOutputSnapshot(exec *model.TaskPipelineExecution, data map[string]interface{}) {
	mgr := NewSnapshotManager()
	_, err := mgr.SaveOutputSnapshot(exec.ID, data)
	if err != nil {
		zap.L().Warn("save output snapshot failed", zap.Error(err))
	}
}

func (c *stageContextImpl) BroadcastStageStatus(exec *model.TaskPipelineExecution) {
	if c.broadcaster != nil {
		c.broadcaster(c.task.ID, exec.Status, map[string]interface{}{
			"execution_id":    exec.ID,
			"stage_code":      exec.StageCode,
			"status":          exec.Status,
			"batch_completed": exec.BatchCompleted,
			"batch_total":     exec.BatchTotal,
			"duration_ms":     exec.DurationMs,
		})
	}
}

// SetBroadcaster 设置事件广播回调
func (c *stageContextImpl) SetBroadcaster(fn func(uint, string, map[string]interface{})) {
	c.broadcaster = fn
}

// SetBatchFiles 设置分批评审时的批次文件列表（用于子批次执行时获取文件）
func (c *stageContextImpl) SetBatchFiles(index int, files []interface{}) {
	c.batchFiles[index] = files
}

// GetBatchFiles 获取批次文件
func (c *stageContextImpl) GetBatchFiles(index int) []interface{} {
	return c.batchFiles[index]
}
