package pipeline

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

// AgenticReviewState 单批次 Agentic 评审状态
type AgenticReviewState struct {
	Messages               []llm.Message
	CurrentRound           int
	MaxRounds              int
	ToolCallsLeft          int
	ForceFinalRound        bool
	ContentFilterTries     int
	CumulativeInputTokens  int
	CumulativeOutputTokens int
	ToolExecutor           *ToolExecutor
	ActualCapability       ToolCapability // 在线验证后的实际能力（可能从 PseudoTag 升级）
	CapabilityProbed       bool           // 是否已完成在线验证
	Rounds                 []AgenticRoundMetrics
}

// AgenticRoundMetrics 单轮指标（用于 snapshot 和前端展示）
type AgenticRoundMetrics struct {
	RoundIndex     int               `json:"round_index"`
	Role           string            `json:"role"` // "llm_call" | "tool_execution"
	InputTokens    int               `json:"input_tokens"`
	OutputTokens   int               `json:"output_tokens"`
	ToolCalls      []string          `json:"tool_calls,omitempty"`
	ToolResults    []ToolResultAudit `json:"tool_results,omitempty"`
	FinishReason   string            `json:"finish_reason,omitempty"`
	ContentPreview string            `json:"content_preview,omitempty"`
}

// ToolResultAudit 工具执行审计（快照用，含完整结果供诊断）
type ToolResultAudit struct {
	ToolName    string `json:"tool_name"`
	Arguments   string `json:"arguments"`
	Result      string `json:"result"`
	ResultChars int    `json:"result_chars"`
}

// executeAgenticReview 执行 Agentic 评审（多批次，按 waves 并行）
func (e *BatchReviewFrameExecutor) executeAgenticReview(
	ctx StageContext,
	plan *BatchPlan,
	promptCtx *engine.PromptContext,
	task *model.Task,
	initialCapability ToolCapability,
) ([]*llm.BatchReviewResult, error) {
	batchResults := make([]*llm.BatchReviewResult, len(plan.Batches))

	var codeReport *CodeUnderstandingReport
	if cr, ok := ctx.GetOutput("code_understanding_structured").(*CodeUnderstandingReport); ok {
		codeReport = cr
	}

	batchDiffs := make(map[string]string)
	if diffFiles := ctx.GetInput("diff_files"); diffFiles != nil {
		if files, ok := diffFiles.([]map[string]interface{}); ok {
			for _, f := range files {
				if path, ok := f["path"].(string); ok && path != "" {
					if diff, ok := f["diff"].(string); ok {
						batchDiffs[path] = diff
					}
				}
			}
		}
	}

	// 并行度（复用预组装模式的配置）
	maxParallel := 3
	if pm, ok := ctx.GetInput("_batch_parallel_max").(int); ok && pm > 0 {
		maxParallel = pm
	}
	waves := GroupBatchesIntoWaves(len(plan.Batches), maxParallel)

	var mu sync.Mutex
	var completedCount int32
	totalInputTokens := 0
	totalOutputTokens := 0
	hasFallback := false

	for waveIdx, wave := range waves {
		zap.L().Info("agentic executing wave",
			zap.Int("wave", waveIdx+1),
			zap.Int("total_waves", len(waves)),
			zap.Int("batches", len(wave)))

		var wg sync.WaitGroup
		errCh := make(chan error, len(wave))

		for _, batchIdx := range wave {
			batchIdx := batchIdx
			batch := plan.Batches[batchIdx]
			wg.Add(1)
			go func() {
				defer wg.Done()

				// 检查任务是否已被取消
				select {
				case <-ctx.Done():
					errCh <- fmt.Errorf("任务已取消")
					return
				default:
				}

				// 获取批次文件列表
				rawFiles := ctx.GetBatchFiles(batch.Index - 1)
				batchFiles := make([]map[string]interface{}, 0, len(rawFiles))
				for _, f := range rawFiles {
					if fm, ok := f.(map[string]interface{}); ok {
						batchFiles = append(batchFiles, fm)
					}
				}

				result, inTk, outTk, agenticExecID, err := e.executeAgenticBatch(ctx, batch, promptCtx, task, codeReport, plan, batchDiffs, batchFiles, initialCapability)
				if err != nil {
					zap.L().Warn("agentic batch failed, fallback to batch review",
						zap.Uint("task_id", task.ID),
						zap.Int("wave", waveIdx+1),
						zap.Int("batch_index", batch.Index),
						zap.Error(err))
					// fallback 到预组装模式
					fbResult, _, fbInTk, fbOutTk, _, fbErr := e.executeBatchCollection(ctx, batch, promptCtx, task)
					if fbErr != nil {
						errCh <- fmt.Errorf("wave %d, batch %d Agentic fallback 也失败: %w", waveIdx+1, batchIdx+1, fbErr)
						return
					}
				result = fbResult
				inTk = fbInTk
				outTk = fbOutTk
				mu.Lock()
				hasFallback = true
				mu.Unlock()
				// 【更新 agentic_batch 快照】标记 fallback 信息，前端可据此展示切换原因
					if agenticExecID > 0 {
						ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: agenticExecID}, map[string]interface{}{
							"agentic_mode":    true,
							"fallback":        true,
							"fallback_reason": err.Error(),
						})
					}
				}

				mu.Lock()
				batchResults[batchIdx] = result
				totalInputTokens += inTk
				totalOutputTokens += outTk
				mu.Unlock()

				completed := int(atomic.AddInt32(&completedCount, 1))
				ctx.UpdateProgress(nil, completed, plan.BatchCount)
			}()
		}

		wg.Wait()
		close(errCh)

		for err := range errCh {
			if err != nil {
				return nil, err
			}
		}
	}

	mu.Lock()
	if selfExec := ctx.GetSelfExec(); selfExec != nil {
		selfExec.InputTokens += totalInputTokens
		selfExec.OutputTokens += totalOutputTokens
	}
	mu.Unlock()

	if hasFallback {
		ctx.SetOutput("_agentic_fallback", true)
	}

	return batchResults, nil
}

// executeAgenticBatch 单批次 Agentic 评审
func (e *BatchReviewFrameExecutor) executeAgenticBatch(
	ctx StageContext,
	detail BatchDetail,
	promptCtx *engine.PromptContext,
	task *model.Task,
	codeReport *CodeUnderstandingReport,
	plan *BatchPlan,
	batchDiffs map[string]string,
	batchFiles []map[string]interface{},
	initialCapability ToolCapability,
) (*llm.BatchReviewResult, int, int, uint, error) {

	exec := ctx.CreateChildExecution(fmt.Sprintf("agentic_batch_%d", detail.Index), detail.Index)
	ctx.MarkRunning(exec)

	// 防御：Agentic 未完成时标记为 skipped（fallback 场景），避免前端展示 failed 红标
	defer func() {
		if exec.Status == model.PipelineStageRunning {
			ctx.MarkSkipped(exec)
		}
	}()

	initialPrompt := buildAgenticInitialPrompt(promptCtx, batchFiles)

	state := &AgenticReviewState{
		Messages: []llm.Message{
			{Role: "system", Content: initialPrompt.SystemPrompt},
			{Role: "user", Content: initialPrompt.UserPrompt},
		},
		MaxRounds:          getAgenticMaxRounds(ctx),
		ToolCallsLeft:      getAgenticMaxTools(ctx),
		ToolExecutor:       NewToolExecutor(codeReport, batchDiffs),
		Rounds:             []AgenticRoundMetrics{},
		ForceFinalRound:    false,
		ContentFilterTries: 0,
		ActualCapability:   initialCapability,
		CapabilityProbed:   false,
	}

	for state.CurrentRound < state.MaxRounds {
		state.CurrentRound++

		// 历史截断：当消息累积过长时，保留最近对话
		if estimatedTokens(state.Messages) > 80000 {
			state.Messages = truncateHistory(state.Messages)
			zap.L().Info("agentic history truncated",
				zap.Uint("task_id", task.ID),
				zap.Int("round", state.CurrentRound))
		}

		// 消息条数控制：超过 10 条（约 5 轮 LLM + 5 轮 tool）强制最终轮
		// 避免消息历史无限膨胀导致模型 confused
		if !state.ForceFinalRound && len(state.Messages) > 10 {
			zap.L().Warn("agentic message count exceeded, forcing final round",
				zap.Uint("task_id", task.ID),
				zap.Int("round", state.CurrentRound),
				zap.Int("message_count", len(state.Messages)))
			state.ForceFinalRound = true
		}

		llmResp, err := e.callLLMWithTools(ctx, state, task)
		if err != nil {
			// 【ForceFinalRound 场景特殊处理】当强制最终轮时模型返回 empty content，
			// 通常是模型 confused（msg 过长 + json_schema 约束），重试大概率仍失败，直接返回明确错误
			if state.ForceFinalRound && strings.Contains(err.Error(), "empty content and no tool calls") {
				return nil, 0, 0, exec.ID, fmt.Errorf("agentic force final round returned empty content, model confused at round %d", state.CurrentRound)
			}
			return nil, 0, 0, exec.ID, fmt.Errorf("agentic round %d llm call failed: %w", state.CurrentRound, err)
		}

		state.CumulativeInputTokens += llmResp.InputTokens
		state.CumulativeOutputTokens += llmResp.OutputTokens

		// 在线验证：PseudoTag 模型是否实际支持原生 tool calling
		if !state.CapabilityProbed {
			state.CapabilityProbed = true
			if state.ActualCapability == ToolCapPseudoTag {
				if llmResp.FinishReason == "tool_calls" && len(llmResp.ToolCalls) > 0 {
					state.ActualCapability = ToolCapNative
					zap.L().Info("model capability upgraded from pseudo_tag to native",
						zap.Uint("task_id", task.ID),
						zap.Uint("model_id", task.UsedModelID))
				}
			}
		}

		llmMetric := AgenticRoundMetrics{
			RoundIndex:   state.CurrentRound,
			Role:         "llm_call",
			InputTokens:  llmResp.InputTokens,
			OutputTokens: llmResp.OutputTokens,
			FinishReason: llmResp.FinishReason,
		}
		if len(llmResp.ToolCalls) > 0 {
			for _, tc := range llmResp.ToolCalls {
				llmMetric.ToolCalls = append(llmMetric.ToolCalls, tc.Function.Name)
			}
		}
		if len(llmResp.Content) > 0 {
			llmMetric.ContentPreview = llmResp.Content
		}
		state.Rounds = append(state.Rounds, llmMetric)

		switch llmResp.FinishReason {
		case "stop":
			result, err := e.parseAgenticResultWithRetry(ctx, exec, state, llmResp.Content, detail, plan.BatchCount, promptCtx.DeductScoreConfig, task)
			return result, state.CumulativeInputTokens, state.CumulativeOutputTokens, exec.ID, err

		case "tool_calls":
			if state.ForceFinalRound || state.ToolCallsLeft <= 0 {
				state.Messages = append(state.Messages, llm.Message{
					Role:    "user",
					Content: "[系统提示] 工具调用不再可用，请基于已有上下文直接输出最终 JSON 评审结果。",
				})
				continue
			}
			if err := e.executeToolCalls(ctx, state, llmResp.ToolCalls); err != nil {
				zap.L().Warn("agentic tool execution failed", zap.Error(err))
			}

		case "length":
			if state.ForceFinalRound {
				return nil, 0, 0, exec.ID, fmt.Errorf("agentic length limit reached twice at round %d", state.CurrentRound)
			}
			zap.L().Warn("agentic length limit reached, forcing final round",
				zap.Uint("task_id", task.ID),
				zap.Int("round", state.CurrentRound))
			state.ForceFinalRound = true
			state.Messages = append(state.Messages, llm.Message{
				Role:    "user",
				Content: "[系统提示] 上下文长度已达上限，请立即仅基于已有信息输出最终 JSON 结果，不再调用工具。",
			})

		case "content_filter":
			if state.ContentFilterTries >= 1 {
				return nil, 0, 0, exec.ID, fmt.Errorf("content filter triggered twice at round %d", state.CurrentRound)
			}
			state.ContentFilterTries++
			zap.L().Warn("agentic content filter triggered, retrying",
				zap.Uint("task_id", task.ID),
				zap.Int("round", state.CurrentRound))
			state.Messages = append(state.Messages, llm.Message{
				Role:    "user",
				Content: "[系统提示] 输出被内容过滤器拦截，请重新生成评审结果，注意避免敏感或不当内容。",
			})

		default:
			// 尝试先按 JSON 解析结果
			if resultContent, ok := parseAgenticResultFromContent(llmResp.Content); ok {
				result, err := e.parseAgenticResultWithRetry(ctx, exec, state, resultContent, detail, plan.BatchCount, promptCtx.DeductScoreConfig, task)
				return result, state.CumulativeInputTokens, state.CumulativeOutputTokens, exec.ID, err
			}
			// PseudoTag 兜底：尝试从 content 中解析 <tool_call> 标签
			if pseudoCalls, ok := parseToolCallsFromContent(llmResp.Content); ok {
				if state.ForceFinalRound || state.ToolCallsLeft <= 0 {
					state.Messages = append(state.Messages, llm.Message{
						Role:    "user",
						Content: "[系统提示] 工具调用不再可用，请基于已有上下文直接输出最终 JSON 评审结果。",
					})
					continue
				}
				llmMetric := AgenticRoundMetrics{
					RoundIndex:   state.CurrentRound,
					Role:         "llm_call",
					InputTokens:  llmResp.InputTokens,
					OutputTokens: llmResp.OutputTokens,
					FinishReason: "tool_calls(pseudo)",
					ToolCalls:    make([]string, 0, len(pseudoCalls)),
				}
				for _, tc := range pseudoCalls {
					llmMetric.ToolCalls = append(llmMetric.ToolCalls, tc.Function.Name)
				}
				state.Rounds = append(state.Rounds, llmMetric)
				if err := e.executeToolCalls(ctx, state, pseudoCalls); err != nil {
					zap.L().Warn("agentic pseudo-tag tool execution failed", zap.Error(err))
				}
				continue
			}
			// 无法识别模型响应，追加提示要求输出 JSON 并继续（避免无变更死循环）
			zap.L().Warn("agentic unrecognized response, prompting for JSON output",
				zap.Uint("task_id", task.ID),
				zap.Int("round", state.CurrentRound),
				zap.String("finish_reason", llmResp.FinishReason))
			state.Messages = append(state.Messages, llm.Message{
				Role:    "user",
				Content: "[系统提示] 请直接输出符合 JSON Schema 的评审结果，不要包含其他内容。",
			})
		}
	}

	result, err := e.forceFinalRound(ctx, exec, state, detail, plan.BatchCount, promptCtx.DeductScoreConfig, task)
	return result, state.CumulativeInputTokens, state.CumulativeOutputTokens, exec.ID, err
}

// callLLMWithTools 调用支持工具调用的 LLM
func (e *BatchReviewFrameExecutor) callLLMWithTools(
	ctx StageContext,
	state *AgenticReviewState,
	task *model.Task,
) (*llm.ToolChatResponse, error) {
	var tools []llm.ToolDefinition
	if !state.ForceFinalRound {
		tools = buildToolDefinitions(state.ToolExecutor.mode)
	}

	// 【修复】最终轮必须强制 json_schema，否则模型不知道输出什么格式
	// 非最终轮不使用 json_schema，避免压制 tool calling
	var respFormat *llm.ResponseFormat
	if state.ForceFinalRound || state.CurrentRound >= state.MaxRounds {
		respFormat = &llm.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &llm.JSONSchema{
				Name:   "batch_review_result",
				Strict: true,
				Schema: llm.GetBatchCollectionJSONSchema(),
			},
		}
	}

	req := llm.ToolChatRequest{
		TaskID:         &task.ID,
		ModelID:        task.UsedModelID,
		Caller:         fmt.Sprintf("agentic_batch_round_%d", state.CurrentRound),
		Messages:       state.Messages,
		Tools:          tools,
		ResponseFormat: respFormat,
		MaxTokens:      calculateMaxTokens(task.UsedModelID),
		Temperature:    getAgenticTemperature(ctx),
	}
	return e.llmService.ChatWithToolCalls(ctx, req)
}

// executeToolCalls 执行工具调用
func (e *BatchReviewFrameExecutor) executeToolCalls(
	ctx StageContext,
	state *AgenticReviewState,
	toolCalls []llm.ToolCall,
) error {
	state.Messages = append(state.Messages, llm.Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: toolCalls,
	})

	roundMetric := AgenticRoundMetrics{
		RoundIndex: state.CurrentRound,
		Role:       "tool_execution",
		ToolCalls:  make([]string, 0, len(toolCalls)),
	}

	for _, tc := range toolCalls {
		state.ToolCallsLeft--
		roundMetric.ToolCalls = append(roundMetric.ToolCalls, tc.Function.Name)
		result, err := state.ToolExecutor.Execute(ctx, tc)
		if err != nil {
			result = fmt.Sprintf("工具执行错误: %v", err)
		}
		state.Messages = append(state.Messages, llm.Message{
			Role:       "tool",
			Content:    result,
			ToolCallID: tc.ID,
		})
		roundMetric.ToolResults = append(roundMetric.ToolResults, ToolResultAudit{
			ToolName:    tc.Function.Name,
			Arguments:   tc.Function.Arguments,
			Result:      result,
			ResultChars: len(result),
		})
	}

	state.Rounds = append(state.Rounds, roundMetric)
	return nil
}

// parseAgenticResult 保存 Agentic 批次快照并更新 exec 状态（解析由调用方完成）
func (e *BatchReviewFrameExecutor) parseAgenticResult(
	ctx StageContext,
	exec *model.TaskPipelineExecution,
	state *AgenticReviewState,
	content string,
	detail BatchDetail,
	totalBatches int,
	deductCfg engine.DeductScoreConfig,
	task *model.Task,
	batchResult *llm.BatchReviewResult,
) (*llm.BatchReviewResult, error) {
	exec.InputTokens = state.CumulativeInputTokens
	exec.OutputTokens = state.CumulativeOutputTokens

	var llmPrompt string
	if len(state.Messages) >= 2 {
		llmPrompt = state.Messages[0].Content + "\n\n" + state.Messages[1].Content
	} else if len(state.Messages) == 1 {
		llmPrompt = state.Messages[0].Content
	}

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"batch_index":        detail.Index,
		"total_batches":      totalBatches,
		"file_count":         detail.FileCount,
		"agentic_mode":       true,
		"agentic_rounds":     state.CurrentRound,
		"agentic_tool_calls": countToolCalls(state.Rounds),
		"input_tokens":       state.CumulativeInputTokens,
		"output_tokens":      state.CumulativeOutputTokens,
		"round_metrics":      state.Rounds,
		"final_content":      content,
		"llm_prompt":         llmPrompt,
	})

	for i, round := range state.Rounds {
		if round.Role == "llm_call" {
			ctx.SaveInputSnapshot(exec, map[string]interface{}{
				fmt.Sprintf("agentic_round_%d_metrics", i+1): round,
			})
		}
	}

	// 保存完整对话历史（调试用）
	ctx.SaveInputSnapshot(exec, map[string]interface{}{
		"agentic_full_messages": state.Messages,
		"agentic_total_rounds":  state.CurrentRound,
	})

	ctx.MarkSuccess(exec)
	return batchResult, nil
}

// forceFinalRound 强制最终轮（不带 tools）
func (e *BatchReviewFrameExecutor) forceFinalRound(
	ctx StageContext,
	exec *model.TaskPipelineExecution,
	state *AgenticReviewState,
	detail BatchDetail,
	totalBatches int,
	deductCfg engine.DeductScoreConfig,
	task *model.Task,
) (*llm.BatchReviewResult, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("agentic force final round cancelled: %w", ctx.Err())
	default:
	}

	zap.L().Warn("agentic force final round",
		zap.Uint("task_id", task.ID),
		zap.Int("max_rounds", state.MaxRounds))

	// 【关键】ForceFinalRound 的系统提示必须明确：工具已禁用，必须直接输出 JSON
	// 避免与 System Prompt 的"建议调用工具"产生冲突
	state.Messages = append(state.Messages, llm.Message{
		Role: "user",
		Content: "[系统提示] 当前已进入最终输出阶段，工具调用已结束。" +
			"请立即基于已有上下文，直接输出符合 JSON Schema 的评审结果。" +
			"不需要调用工具，也不需要额外解释，只输出 JSON 内容即可。",
	})

	req := llm.ToolChatRequest{
		TaskID:   &task.ID,
		ModelID:  task.UsedModelID,
		Caller:   "agentic_force_final",
		Messages: state.Messages,
		Tools:    nil,
		ResponseFormat: &llm.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &llm.JSONSchema{
				Name:   "batch_review_result",
				Strict: true,
				Schema: llm.GetBatchCollectionJSONSchema(),
			},
		},
		MaxTokens:   calculateMaxTokens(task.UsedModelID),
		Temperature: getAgenticTemperature(ctx),
	}

	llmResp, err := e.llmService.ChatWithToolCalls(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("agentic force final round failed: %w", err)
	}

	// 空 content 防御（reasoning-only 场景）
	if strings.TrimSpace(llmResp.Content) == "" {
		return nil, fmt.Errorf("agentic force final round returned empty content (finish_reason=%s, output_tokens=%d)", llmResp.FinishReason, llmResp.OutputTokens)
	}

	state.CumulativeInputTokens += llmResp.InputTokens
	state.CumulativeOutputTokens += llmResp.OutputTokens

	batchResult, err := engine.ParseBatchReviewResult(llmResp.Content, deductCfg)
	if err != nil {
		return nil, fmt.Errorf("parse agentic force final result failed: %w", err)
	}

	// 复用 parseAgenticResult 保存快照（避免重复代码）
	// forceFinalRound 的特殊标记通过 state.Rounds 已无法追加，需在 snapshot 中体现
	// 在调用 parseAgenticResult 前注入强制标记
	state.Rounds = append(state.Rounds, AgenticRoundMetrics{
		RoundIndex:   state.CurrentRound,
		Role:         "llm_call",
		InputTokens:  llmResp.InputTokens,
		OutputTokens: llmResp.OutputTokens,
		FinishReason: llmResp.FinishReason,
	})

	exec.InputTokens = state.CumulativeInputTokens
	exec.OutputTokens = state.CumulativeOutputTokens

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"batch_index":        detail.Index,
		"total_batches":      totalBatches,
		"file_count":         detail.FileCount,
		"agentic_mode":       true,
		"agentic_rounds":     state.CurrentRound,
		"agentic_tool_calls": countToolCalls(state.Rounds),
		"forced_final":       true,
		"input_tokens":       state.CumulativeInputTokens,
		"output_tokens":      state.CumulativeOutputTokens,
		"round_metrics":      state.Rounds,
		"final_content":      llmResp.Content,
	})

	for i, round := range state.Rounds {
		if round.Role == "llm_call" {
			ctx.SaveInputSnapshot(exec, map[string]interface{}{
				fmt.Sprintf("agentic_round_%d_metrics", i+1): round,
			})
		}
	}
	ctx.SaveInputSnapshot(exec, map[string]interface{}{
		"agentic_full_messages": state.Messages,
		"agentic_total_rounds":  state.CurrentRound,
	})

	ctx.MarkSuccess(exec)
	return batchResult, nil
}

// buildToolDefinitions 构建可用工具定义列表
func buildToolDefinitions(mode string) []llm.ToolDefinition {
	tools := []llm.ToolDefinition{
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_file_ast_summary",
				Description: "获取指定文件的 AST 结构摘要，包括函数列表、类型列表、导入列表等",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径"},
					},
					"required": []string{"file_path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_function_callers",
				Description: "获取指定函数的调用者列表（谁调用了这个函数）",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"function_name": map[string]interface{}{"type": "string", "description": "函数名"},
					},
					"required": []string{"function_name"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_function_callees",
				Description: "获取指定函数的调用目标列表（这个函数调用了谁）",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"function_name": map[string]interface{}{"type": "string", "description": "函数名"},
					},
					"required": []string{"function_name"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_data_flow_path",
				Description: "获取两个函数之间的数据流传播路径（污点分析）",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"source_func": map[string]interface{}{"type": "string", "description": "源头函数"},
						"sink_func":   map[string]interface{}{"type": "string", "description": "汇聚函数"},
					},
					"required": []string{"source_func", "sink_func"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_api_endpoints",
				Description: "获取指定文件中的 API 端点信息",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径"},
					},
					"required": []string{"file_path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_breaking_changes",
				Description: "获取指定文件中的破坏性变更",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径"},
					},
					"required": []string{"file_path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_import_dependencies",
				Description: "获取指定文件的导入依赖列表",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径"},
					},
					"required": []string{"file_path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "get_diff",
				Description: "获取指定文件的 diff 内容，用于查看不在当前批次中的文件变更",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径"},
					},
					"required": []string{"file_path"},
				},
			},
		},
	}
	return tools
}

// pseudoTagRegexp 预编译正则，避免每次响应都重新编译
var pseudoTagRegexp = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)

// parseToolCallsFromContent 从文本中解析 <tool_call> 伪标签（PseudoTag fallback）
func parseToolCallsFromContent(content string) ([]llm.ToolCall, bool) {
	matches := pseudoTagRegexp.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil, false
	}
	var tcs []llm.ToolCall
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		var tc struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &tc); err != nil {
			zap.L().Warn("pseudo-tag tool_call parse failed",
				zap.String("raw", m[1]),
				zap.Error(err))
			continue
		}
		argsBytes, _ := json.Marshal(tc.Arguments)
		tcs = append(tcs, llm.ToolCall{
			ID:   fmt.Sprintf("pseudo_%d", len(tcs)),
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      tc.Name,
				Arguments: string(argsBytes),
			},
		})
	}
	return tcs, len(tcs) > 0
}

// parseAgenticResultWithRetry 解析Agentic结果，失败时重试一次（不带tools）
func (e *BatchReviewFrameExecutor) parseAgenticResultWithRetry(
	ctx StageContext,
	exec *model.TaskPipelineExecution,
	state *AgenticReviewState,
	content string,
	detail BatchDetail,
	totalBatches int,
	deductCfg engine.DeductScoreConfig,
	task *model.Task,
) (*llm.BatchReviewResult, error) {
	result, err := engine.ParseBatchReviewResult(content, deductCfg)
	if err == nil {
		return e.parseAgenticResult(ctx, exec, state, content, detail, totalBatches, deductCfg, task, result)
	}

	// 首次解析失败：重试一次（不带tools，强制最终轮）
	zap.L().Warn("agentic result parse failed, retrying once without tools",
		zap.Uint("task_id", task.ID),
		zap.Int("batch_index", detail.Index),
		zap.Error(err))

	state.Messages = append(state.Messages, llm.Message{
		Role:    "user",
		Content: "[系统提示] 上次输出格式不正确，请严格输出符合 JSON Schema 的评审结果，不要再调用工具。",
	})

	req := llm.ToolChatRequest{
		TaskID:   &task.ID,
		ModelID:  task.UsedModelID,
		Caller:   "agentic_parse_retry",
		Messages: state.Messages,
		Tools:    nil,
		ResponseFormat: &llm.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &llm.JSONSchema{
				Name:   "batch_review_result",
				Strict: true,
				Schema: llm.GetBatchCollectionJSONSchema(),
			},
		},
		MaxTokens:   calculateMaxTokens(task.UsedModelID),
		Temperature: getAgenticTemperature(ctx),
	}

	llmResp, retryErr := e.llmService.ChatWithToolCalls(ctx, req)
	if retryErr != nil {
		return nil, fmt.Errorf("parse agentic result failed and retry also failed: %w (original: %v)", retryErr, err)
	}

	// 防御空 content
	if strings.TrimSpace(llmResp.Content) == "" {
		return nil, fmt.Errorf("parse agentic result retry returned empty content (finish_reason=%s, output_tokens=%d)", llmResp.FinishReason, llmResp.OutputTokens)
	}

	state.CumulativeInputTokens += llmResp.InputTokens
	state.CumulativeOutputTokens += llmResp.OutputTokens

	result, retryErr = engine.ParseBatchReviewResult(llmResp.Content, deductCfg)
	if retryErr != nil {
		return nil, fmt.Errorf("parse agentic result failed and retry also failed: %w (original: %v)", retryErr, err)
	}

	// 重试成功，追加一轮metrics
	state.Rounds = append(state.Rounds, AgenticRoundMetrics{
		RoundIndex:     state.CurrentRound,
		Role:           "llm_call",
		InputTokens:    llmResp.InputTokens,
		OutputTokens:   llmResp.OutputTokens,
		FinishReason:   llmResp.FinishReason,
		ContentPreview: truncateString(llmResp.Content, 500),
	})

	return e.parseAgenticResult(ctx, exec, state, llmResp.Content, detail, totalBatches, deductCfg, task, result)
}

// AgenticPrompt 极简初始 Prompt
type AgenticPrompt struct {
	SystemPrompt string
	UserPrompt   string
}

// buildAgenticInitialPrompt 构建 Agentic 模式的极简初始 Prompt
func buildAgenticInitialPrompt(promptCtx *engine.PromptContext, batchFiles []map[string]interface{}) AgenticPrompt {
	systemPrompt := engine.BuildAgenticSystemPrompt(promptCtx)
	userPrompt := engine.BuildAgenticUserPrompt(promptCtx, batchFiles)
	return AgenticPrompt{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
	}
}

func estimatedTokens(msgs []llm.Message) int {
	estimator := NewTokenEstimator()
	total := 0
	for _, m := range msgs {
		total += estimator.Estimate(m.Content)
		for _, tc := range m.ToolCalls {
			total += estimator.Estimate(tc.Function.Name + tc.Function.Arguments)
		}
	}
	return total
}

func truncateHistory(msgs []llm.Message) []llm.Message {
	if len(msgs) <= 4 {
		return msgs
	}
	preserved := 2
	// 复制 recent 避免修改原始切片底层数组
	recent := make([]llm.Message, len(msgs[preserved:]))
	copy(recent, msgs[preserved:])

	// 优先删除最早的 assistant + tool 对（一轮交互）
	found := false
	for i := 0; i < len(recent)-1; i++ {
		if recent[i].Role == "assistant" && recent[i+1].Role == "tool" {
			recent = append(recent[:i], recent[i+2:]...)
			found = true
			break
		}
	}
	// 若未找到（模型未调用工具），直接删掉最老的两条，避免截断失效
	if !found && len(recent) > 0 {
		drop := 2
		if len(recent) < drop {
			drop = len(recent)
		}
		recent = recent[drop:]
	}
	return append(msgs[:preserved], recent...)
}

func parseAgenticResultFromContent(content string) (string, bool) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		content = strings.Join(jsonLines, "\n")
		content = strings.TrimSpace(content)
	}
	if strings.HasPrefix(content, "{") && json.Valid([]byte(content)) {
		return content, true
	}
	return "", false
}

func countToolCalls(rounds []AgenticRoundMetrics) int {
	count := 0
	for _, r := range rounds {
		if r.Role == "tool_execution" {
			count += len(r.ToolCalls)
		}
	}
	return count
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func calculateMaxTokens(modelID uint) int {
	// 从模型配置读取 max_tokens
	var m model.LLMModel
	if err := model.DB.Select("max_tokens").First(&m, modelID).Error; err == nil && m.MaxTokens > 0 {
		return m.MaxTokens
	}
	return 4096
}
