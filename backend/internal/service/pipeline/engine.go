package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/engine"
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
	e.Register(NewSecretScanExecutor(llm, 0))
	e.Register(NewSecurityAuditExecutor(llm, 0))
	e.Register(NewTestSuggestionExecutor(llm, 0))
	e.Register(NewImpactAnalysisExecutor(llm, 0))
	e.Register(NewBatchReviewFrameExecutor(llm))
	e.Register(NewReviewArbitrationExecutor(llm, 0, 60)) // modelID=0 表示使用 task.UsedModelID
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

	// 读取全局智能体配置
	var agentCfg model.ReviewAgentConfig
	enabledMap := make(map[string]bool)
	if err := e.db.First(&agentCfg, 1).Error; err == nil {
		for _, code := range agentCfg.EnabledStageCodes() {
			enabledMap[code] = true
		}
		// 同步布尔字段：扩展阶段开关为 true 但 enabled_stages 中缺失时自动补充
		// （兼容旧配置或前端布尔字段与 stages 列表不同步的情况）
		if agentCfg.SecretScanEnabled && !enabledMap["secret_scan"] {
			enabledMap["secret_scan"] = true
		}
		if agentCfg.SecurityAuditEnabled && !enabledMap["security_audit"] {
			enabledMap["security_audit"] = true
		}
		if agentCfg.TestSuggestionEnabled && !enabledMap["test_suggestion"] {
			enabledMap["test_suggestion"] = true
		}
		if agentCfg.ImpactAnalysisEnabled && !enabledMap["impact_analysis"] {
			enabledMap["impact_analysis"] = true
		}
	} else {
		// 配置未初始化，默认启用核心阶段（扩展阶段默认不启用）
		defaultEnabled := []string{"trigger_check", "git_clone", "context_extract",
			"dependency_scan", "batch_review_frame", "review_arbitration", "post_process"}
		for _, code := range defaultEnabled {
			enabledMap[code] = true
		}
	}

	// 复制 inputs map，避免副作用
	pipelineInputs := make(map[string]interface{}, len(inputs)+1)
	for k, v := range inputs {
		pipelineInputs[k] = v
	}
	// 将 context_extract 深度注入 inputs，供后续阶段读取
	pipelineInputs["_context_extract_depth"] = agentCfg.ContextExtractDepth()

	// 将 batch_review_frame 的并行批次上限注入 inputs
	if brCfg := agentCfg.StageConfig("batch_review_frame"); brCfg != nil {
		if pm, ok := brCfg["parallel_max"].(float64); ok && pm > 0 {
			pipelineInputs["_batch_parallel_max"] = int(pm)
		}
	}

	// 如果代码理解器被禁用，清空 AST 上下文，避免 AI 评审 Prompt 里混入无关内容
	if !enabledMap["context_extract"] {
		pipelineInputs["ast_context"] = ""
		// 同时清空 prompt_context 中的 ASTContext 字段（如果存在）
		if pc, ok := pipelineInputs["prompt_context"].(engine.PromptContext); ok {
			pc.ASTContext = ""
			pipelineInputs["prompt_context"] = pc
		}
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
	for k, v := range pipelineInputs {
		ctx.SetInput(k, v)
	}

	for _, stageDef := range stageDefs {
		// 检查任务是否已被手动停止
		select {
		case <-ctx.Done():
			zap.L().Info("Pipeline 收到取消信号，停止执行", zap.Uint("task_id", task.ID), zap.String("current_stage", stageDef.Code))
			e.markTaskFailed(task.ID, "任务被手动停止")
			return fmt.Errorf("任务被手动停止")
		default:
		}

		execImpl, ok := e.executors[stageDef.Code]
		if !ok {
			zap.L().Warn("未找到阶段执行器", zap.String("code", stageDef.Code))
			continue
		}

		// 检查是否被配置禁用
		if !enabledMap[stageDef.Code] {
			// 创建 skipped 执行记录
			exec := &model.TaskPipelineExecution{
				TaskID:      task.ID,
				StageCode:   stageDef.Code,
				Status:      model.PipelineStageSkipped,
				SortOrder:   stageDef.SortOrder,
				CompletedAt: &now,
			}
			if err := e.db.Create(exec).Error; err != nil {
				zap.L().Error("创建 skipped 阶段执行记录失败", zap.Error(err))
			}
			zap.L().Info("阶段被配置跳过", zap.Uint("task_id", task.ID), zap.String("code", stageDef.Code))
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

		// 【新增】检查 RequiredStages 依赖是否满足
		requiredStages := stageDef.GetRequiredStages()
		if len(requiredStages) > 0 {
			allDepsMet := true
			for _, depCode := range requiredStages {
				var depExec model.TaskPipelineExecution
				err := e.db.Where("task_id = ? AND stage_code = ?", task.ID, depCode).
					Order("id DESC").First(&depExec).Error
				if err != nil || depExec.Status != model.PipelineStageSuccess {
					var depStatus string
					if err != nil {
						depStatus = "not_found"
					} else {
						depStatus = depExec.Status
					}
					zap.L().Warn("阶段依赖未满足",
						zap.String("stage", stageDef.Code),
						zap.String("dependency", depCode),
						zap.String("status", depStatus))
					allDepsMet = false
					break
				}
			}
			if !allDepsMet {
				// 依赖失败 → 标记为 skipped（如果 AllowedToSkip）或 failed
				status := model.PipelineStageSkipped
				if !stageDef.AllowedToSkip {
					status = model.PipelineStageFailed
				}
				exec.Status = status
				exec.CompletedAt = &now
				exec.ErrorMessage = "依赖阶段未成功执行"
				e.db.Save(exec)
				zap.L().Info("阶段因依赖失败被跳过/失败",
					zap.String("stage", stageDef.Code),
					zap.String("status", status))
				continue
			}
		}

		// 执行阶段（带超时）
		// 优先从 stage_configs 读取自定义超时，否则使用阶段定义的默认值
		timeoutSec := stageDef.TimeoutSec
		if cfg := agentCfg.StageConfig(stageDef.Code); cfg != nil {
			if t, ok := cfg["timeout"].(float64); ok && t > 0 {
				timeoutSec = int(t)
			}
		}
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
		// 传递取消信号，确保各阶段能感知 Abort
		if ch, ok := ctx.inputData["_cancel_ch"].(chan struct{}); ok {
			stageCtx.cancelCh = ch
		}

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
	// 创建带 cancel 的 StageContext，超时后通过 cancel 通知 Executor 内部退出
	timeoutCtx, cancel := ctx.WithTimeout(time.Duration(timeoutSec) * time.Second)
	defer cancel()

	err := exec.Execute(timeoutCtx)
	// 如果超时，exec.Execute 会因为底层 context cancel 而提前返回（只要 Executor 内部检查 ctx.Done()）
	if timeoutCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("阶段执行超时 (%ds)", timeoutSec)
	}
	return err
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
