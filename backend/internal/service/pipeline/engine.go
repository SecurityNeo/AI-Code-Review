package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Engine Pipeline 执行引擎
type Engine struct {
	db          *gorm.DB
	executors   map[string]StageExecutor
	snapshotMgr *SnapshotManager
}

// NewEngine 创建 Pipeline 引擎（llm: LLMService 实例由调用方注入）
func NewEngine(db *gorm.DB, llm LLMService) *Engine {
	e := &Engine{
		db:          db,
		executors:   make(map[string]StageExecutor),
		snapshotMgr: NewSnapshotManager(),
	}
	// 注册阶段执行器
	e.Register(&TriggerCheckExecutor{})
	e.Register(&GitCloneExecutor{})
	e.Register(&ContextExtractExecutor{})
	e.Register(&DependencyScanExecutor{})
	e.Register(&RiskAnalysisExecutor{})
	e.Register(NewBatchReviewFrameExecutor(llm))
	e.Register(&PostProcessExecutor{})
	return e
}

// Register 注册阶段执行器
func (e *Engine) Register(exec StageExecutor) {
	e.executors[exec.Code()] = exec
}

// ExecuteTask 执行任务 Pipeline
// inputs 预加载数据：diff_files, commits_text, project_template, ast_context
func (e *Engine) ExecuteTask(taskID uint, inputs map[string]interface{}, broadcaster func(uint, string, map[string]interface{})) error {
	var task model.Task
	if err := e.db.Preload("Project").Preload("Project.Template").Preload("UsedModel").First(&task, taskID).Error; err != nil {
		return fmt.Errorf("任务不存在: %w", err)
	}

	// 加载阶段定义
	var stageDefs []model.ReviewPipelineStage
	if err := e.db.Order("sort_order ASC").Find(&stageDefs).Error; err != nil {
		return fmt.Errorf("加载阶段定义失败: %w", err)
	}
	if len(stageDefs) == 0 {
		return fmt.Errorf("未配置 Pipeline 阶段")
	}

	// 更新任务状态为 running
	task.Status = model.TaskRunning
	now := time.Now()
	task.StartedAt = &now
	e.db.Omit("pool_id").Save(&task)

	zap.L().Info("Pipeline 开始执行", zap.Uint("task_id", task.ID), zap.Int("stages", len(stageDefs)))

	ctx := newStageContext(context.Background(), &task, e.db)
	ctx.SetBroadcaster(broadcaster)

	// 注入预加载数据
	for k, v := range inputs {
		ctx.SetInput(k, v)
	}

	for _, stageDef := range stageDefs {
		execImpl, ok := e.executors[stageDef.Code]
		if !ok {
			zap.L().Warn("未找到阶段执行器", zap.String("code", stageDef.Code))
			continue
		}

		// 创建 execution 记录
		exec := &model.TaskPipelineExecution{
			TaskID:    task.ID,
			StageCode: stageDef.Code,
			Status:    model.PipelineStagePending,
			SortOrder: stageDef.SortOrder,
		}
		if err := e.db.Create(exec).Error; err != nil {
			zap.L().Error("创建阶段执行记录失败", zap.Error(err))
			continue
		}

		// 执行阶段（带超时）
		timeoutSec := stageDef.TimeoutSec
		if timeoutSec <= 0 {
			timeoutSec = 300
		}
		stageCtx := newStageContext(context.Background(), &task, e.db)
		stageCtx.executionID = exec.ID
		stageCtx.selfExec = exec
		stageCtx.SetBroadcaster(broadcaster)
		// 共享数据
		stageCtx.inputData = ctx.inputData
		stageCtx.outputData = ctx.outputData
		stageCtx.batchFiles = ctx.batchFiles

		ctx.MarkRunning(exec)

		err := e.executeWithTimeout(stageCtx, execImpl, timeoutSec)

		if err != nil {
			ctx.MarkFailed(exec, err.Error())
			e.markTaskFailed(task.ID, fmt.Sprintf("阶段 %s 失败: %v", stageDef.Code, err))
			return err
		}

		ctx.MarkSuccess(exec)
	}

	e.markTaskSuccess(task.ID, ctx)
	zap.L().Info("Pipeline 执行完成", zap.Uint("task_id", task.ID))
	return nil
}

func (e *Engine) executeWithTimeout(ctx StageContext, exec StageExecutor, timeoutSec int) error {
	c, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-c.Done():
		return fmt.Errorf("阶段执行超时 (%ds)", timeoutSec)
	}
}

func (e *Engine) markTaskFailed(taskID uint, reason string) {
	e.db.Model(&model.Task{}).Where("id = ? AND status = ?", taskID, model.TaskRunning).Updates(map[string]interface{}{
		"status":       model.TaskFailed,
		"error_msg":    reason,
		"completed_at": time.Now(),
	})
}

func (e *Engine) markTaskSuccess(taskID uint, ctx *stageContextImpl) {
	finalReport := ""
	if v, ok := ctx.GetOutput("final_report").(string); ok {
		finalReport = v
	}
	score := 0
	if v, ok := ctx.GetOutput("score").(int); ok {
		score = v
	}
	actualModelID := uint(0)
	// 优先使用 Pipeline 运行期间实际调用的模型 ID（由 PostProcessExecutor 记录）
	if v, ok := ctx.GetOutput("model_id").(uint); ok && v > 0 {
		actualModelID = v
	} else if t := ctx.Task(); t != nil && t.UsedModelID > 0 {
		actualModelID = t.UsedModelID // fallback: 任务初始化时指定的模型
	}

	completedAt := time.Now()
	var startedAt time.Time
	if t := ctx.Task(); t != nil && t.StartedAt != nil {
		startedAt = *t.StartedAt
	}
	durationSec := int(completedAt.Sub(startedAt).Seconds())

	e.db.Model(&model.Task{}).Where("id = ? AND status = ?", taskID, model.TaskRunning).Updates(map[string]interface{}{
		"status":       model.TaskSuccess,
		"ai_response":  finalReport,
		"score_value":  score,
		"model_id":     actualModelID,
		"completed_at": completedAt,
		"duration_sec": durationSec,
	})
}
