package pipeline

import (
	"encoding/json"
	"errors"
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

// Agentic 批次"完成度"相关常量
const (
	completionComplete = "complete"
	completionNeedMore = "need_more_analysis"
	// maxSubmitNudges 连续引导模型显式提交的最大次数，超过则交由上层降级（预组装复评）
	maxSubmitNudges = 2
	// submitReviewToolName 终止工具：模型必须调用它来提交最终评审结论
	submitReviewToolName = "submit_review"
	// submitNudgeText 引导模型显式提交结论的系统提示
	submitNudgeText = "[系统提示] 你尚未提交最终评审结果。请调用 submit_review 工具提交结论；" +
		"若本次变更确认无问题，请提交空 issues，并在 no_issue_reason 中说明已核实的依据。" +
		"禁止输出“我再核实/继续查看”之类未完成表述。"
)

// errAgenticIncomplete 批次结果未通过完成度校验（模型仍在分析中/结论不完整）
var errAgenticIncomplete = errors.New("agentic result incomplete")

// recordSkippedToolCalls 记录"被跳过的工具调用"（预算耗尽/最终轮）：
// 追加 assistant(tool_calls) 与对应 tool 结果消息（保持历史协议完整），
// 并写入一条 role="tool_execution" 的指标（含未执行原因），保证前端时间线可见、可审计。
func recordSkippedToolCalls(state *AgenticReviewState, toolCalls []llm.ToolCall, reason string) {
	if len(toolCalls) == 0 {
		return
	}
	state.Messages = append(state.Messages, llm.Message{Role: "assistant", ToolCalls: toolCalls})
	note := "skipped: " + reason
	m := AgenticRoundMetrics{RoundIndex: state.CurrentRound, Role: "tool_execution"}
	for _, tc := range toolCalls {
		state.Messages = append(state.Messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: note})
		m.ToolCalls = append(m.ToolCalls, tc.Function.Name)
		m.ToolResults = append(m.ToolResults, ToolResultAudit{
			ToolName:    tc.Function.Name,
			Arguments:   tc.Function.Arguments,
			Result:      note,
			ResultChars: 0,
		})
	}
	state.Rounds = append(state.Rounds, m)
}

// nudgeToSubmit 追加一次"显式提交结论"的引导。
// 返回非 nil 表示已超过引导上限，调用方应终止本批次并交由上层降级（预组装复评）。
func nudgeToSubmit(state *AgenticReviewState, text string) error {
	if state.SubmitNudges >= maxSubmitNudges {
		return fmt.Errorf("agentic did not submit a valid result after %d nudges", state.SubmitNudges)
	}
	state.SubmitNudges++
	state.Messages = append(state.Messages, llm.Message{Role: "user", Content: text})
	return nil
}

// AgenticReviewState 单批次 Agentic 评审状态
type AgenticReviewState struct {
	Messages               []llm.Message
	CurrentRound           int
	MaxRounds              int
	MaxMessages            int // 消息条数软上限，超过则强制最终轮
	HistoryTokenLimit      int // 历史消息累积 token 超此值触发截断
	ToolCallsLeft          int
	ForceFinalRound        bool
	ContentFilterTries     int
	CumulativeInputTokens  int
	CumulativeOutputTokens int
	ToolExecutor           *ToolExecutor
	ActualCapability       ToolCapability  // 在线验证后的实际能力（可能从 PseudoTag 升级）
	CapabilityProbed       bool            // 是否已完成在线验证
	SubmitNudges           int             // 引导模型显式提交结论的次数（用于限制重试）
	SubmitAttempts         []SubmitAttempt // submit_review 提交尝试审计
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

// SubmitAttempt 一次 submit_review 提交尝试的审计记录
type SubmitAttempt struct {
	Round      int    `json:"round"`
	OK         bool   `json:"ok"`
	Reason     string `json:"reason,omitempty"`
	IssueCount int    `json:"issue_count"`
	ContentLen int    `json:"content_len"`
	Completion string `json:"completion,omitempty"`
}

// submitReviewToolDef 终止工具定义（包级初始化一次；Parameters 仅只读序列化，可并发共享）
var submitReviewToolDef = buildSubmitReviewTool()

// buildSubmitReviewTool 构造终止工具 submit_review。
// 约定：模型只有调用本工具才算"结束评审"；否则视为仍在分析中。
func buildSubmitReviewTool() llm.ToolDefinition {
	schema, _ := llm.GetAgenticSubmitReviewJSONSchema().(map[string]interface{})
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunction{
			Name: submitReviewToolName,
			Description: "提交最终评审结论并结束本批次评审。必须调用本工具来结束评审；" +
				"若确认本次变更无问题，请提交空 issues 并给出 no_issue_reason；" +
				"禁止在未调用本工具的情况下直接停止或输出未完成表述。",
			Parameters: schema,
		},
	}
}

// validateAgenticCompletion 判定批次结果是否为"有效完成"（不依赖任何语气/关键词）：
//   - 有 issues → 完成；
//   - 无 issues → 必须显式声明 completion=complete 且给出非空 no_issue_reason；
//   - 否则视为未完成。
func validateAgenticCompletion(r *llm.BatchReviewResult) (bool, string) {
	if r == nil {
		return false, "nil result"
	}
	if len(r.Issues) > 0 {
		return true, ""
	}
	if r.Completion != completionComplete {
		return false, fmt.Sprintf("completion=%q (want %q)", r.Completion, completionComplete)
	}
	if strings.TrimSpace(r.NoIssueReason) == "" {
		return false, "empty issues without no_issue_reason"
	}
	return true, ""
}

// extractSubmitReviewCall 从工具调用列表中找出 submit_review（若存在）
func extractSubmitReviewCall(calls []llm.ToolCall) *llm.ToolCall {
	for i := range calls {
		if calls[i].Function.Name == submitReviewToolName {
			return &calls[i]
		}
	}
	return nil
}

// parseSubmittedReview 解析 submit_review 的工具参数为批次结果
func parseSubmittedReview(tc *llm.ToolCall) (*llm.BatchReviewResult, error) {
	if tc == nil {
		return nil, errors.New("nil submit_review call")
	}
	var r llm.BatchReviewResult
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &r); err != nil {
		return nil, fmt.Errorf("parse submit_review arguments failed: %w", err)
	}
	if r.Issues == nil {
		r.Issues = []llm.AIReviewIssue{}
	}
	if r.Recommendations == nil {
		r.Recommendations = []string{}
	}
	return &r, nil
}

// submitResultText 生成 submit_review 的"工具结果"文本（会回传给模型，并落库审计）
func submitResultText(ok bool, reason string) string {
	if ok {
		return "submit_review accepted（评审结论已提交）"
	}
	if strings.TrimSpace(reason) == "" {
		reason = errAgenticIncomplete.Error()
	}
	return "submit_review rejected：" + reason + "。请补全后重新调用 submit_review 提交。"
}

// tryHandleSubmitReview 处理一次 submit_review 调用。
// 无论成功与否都会：
//   - 追加一条 role="tool_execution" 的 round metric（含提交 payload 与接受/拒绝原因）；
//   - 向对话历史追加 assistant(tool_call) 与 tool 结果（便于模型看到自身提交、便于审计）；
//   - 记录一次 SubmitAttempt（写入快照）。
//
// 返回：
//   - (result, nil): 校验通过，完成；
//   - (nil, nil):    校验未通过但未超限，已追加引导，调用方应 continue；
//   - (nil, err):    已超引导上限或参数无法解析，需要上层降级（预组装复评）。
func tryHandleSubmitReview(state *AgenticReviewState, submit *llm.ToolCall, task *model.Task) (*llm.BatchReviewResult, error) {
	res, perr := parseSubmittedReview(submit)

	ok, reason := false, ""
	var issueCount int
	var completion string
	if perr == nil {
		issueCount = len(res.Issues)
		completion = res.Completion
		if vok, vreason := validateAgenticCompletion(res); vok {
			ok = true
		} else {
			reason = vreason
		}
	} else {
		reason = perr.Error()
	}

	// 1) 记录本轮工具执行事件（提交 payload + 结果），供前端展示与审计
	resultText := submitResultText(ok, reason)
	state.Rounds = append(state.Rounds, AgenticRoundMetrics{
		RoundIndex: state.CurrentRound,
		Role:       "tool_execution",
		ToolCalls:  []string{submitReviewToolName},
		ToolResults: []ToolResultAudit{{
			ToolName:    submitReviewToolName,
			Arguments:   submit.Function.Arguments,
			Result:      resultText,
			ResultChars: len(submit.Function.Arguments),
		}},
	})

	// 2) 补全对话历史：assistant(tool_call) + tool 结果
	state.Messages = append(state.Messages, llm.Message{
		Role:      "assistant",
		ToolCalls: []llm.ToolCall{*submit},
	})
	state.Messages = append(state.Messages, llm.Message{
		Role:       "tool",
		Content:    resultText,
		ToolCallID: submit.ID,
	})

	// 3) 审计：记录提交尝试（含失败原因）
	state.SubmitAttempts = append(state.SubmitAttempts, SubmitAttempt{
		Round:      state.CurrentRound,
		OK:         ok,
		Reason:     reason,
		IssueCount: issueCount,
		ContentLen: len(submit.Function.Arguments),
		Completion: completion,
	})

	// 4) 失败原因写入日志（便于定位模型为何首次提交不合规）
	if ok {
		zap.L().Info("agentic submit_review accepted",
			zap.Uint("task_id", task.ID), zap.Int("round", state.CurrentRound),
			zap.Int("issue_count", issueCount))
		return res, nil
	}
	zap.L().Warn("agentic submit_review rejected",
		zap.Uint("task_id", task.ID), zap.Int("round", state.CurrentRound),
		zap.String("reason", reason), zap.Int("arguments_len", len(submit.Function.Arguments)))

	if nerr := nudgeToSubmit(state, submitNudgeText); nerr != nil {
		return nil, fmt.Errorf("%w (submit_review invalid: %s)", nerr, reason)
	}
	return nil, nil
}

// acceptContentOrNudge 尝试把模型 content 作为最终结果：
//   - 解析并通过完成度校验 → (result, true, nil)
//   - 未完成/解析失败但可继续 → 追加引导后 (nil, false, nil)，调用方应 continue
//   - 超过引导上限 → (nil, false, err)，交由上层降级（预组装复评）
func (e *BatchReviewFrameExecutor) acceptContentOrNudge(
	ctx StageContext,
	exec *model.TaskPipelineExecution,
	state *AgenticReviewState,
	content string,
	detail BatchDetail,
	totalBatches int,
	deductCfg engine.DeductScoreConfig,
	task *model.Task,
) (*llm.BatchReviewResult, bool, error) {
	result, perr := e.parseAgenticResultWithRetry(ctx, exec, state, content, detail, totalBatches, deductCfg, task)
	if perr == nil {
		return result, true, nil
	}
	if errors.Is(perr, errAgenticIncomplete) {
		if nerr := nudgeToSubmit(state, submitNudgeText); nerr != nil {
			return nil, false, fmt.Errorf("%w: %v", nerr, perr)
		}
		return nil, false, nil
	}
	return nil, false, perr
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

	initialPrompt := buildAgenticInitialPrompt(promptCtx, batchFiles, detail.Index == plan.BatchCount)

	state := &AgenticReviewState{
		Messages: []llm.Message{
			{Role: "system", Content: initialPrompt.SystemPrompt},
			{Role: "user", Content: initialPrompt.UserPrompt},
		},
		MaxRounds:          getAgenticMaxRounds(ctx),
		MaxMessages:        getAgenticMaxMessages(ctx),
		HistoryTokenLimit:  getAgenticHistoryTokenLimit(ctx),
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

		// 历史截断：当消息累积过长时，保留最近对话（阈值可配，默认 80000）
		if state.HistoryTokenLimit > 0 && estimatedTokens(state.Messages) > state.HistoryTokenLimit {
			state.Messages = truncateHistory(state.Messages)
			zap.L().Info("agentic history truncated",
				zap.Uint("task_id", task.ID),
				zap.Int("round", state.CurrentRound),
				zap.Int("history_token_limit", state.HistoryTokenLimit))
		}

		// 消息条数控制：超过软上限（可配，缺省 = max_tools + 4）强制最终轮
		// 避免消息历史无限膨胀导致模型 confused
		if !state.ForceFinalRound && state.MaxMessages > 0 && len(state.Messages) > state.MaxMessages {
			zap.L().Warn("agentic message count exceeded, forcing final round",
				zap.Uint("task_id", task.ID),
				zap.Int("round", state.CurrentRound),
				zap.Int("message_count", len(state.Messages)),
				zap.Int("max_messages", state.MaxMessages))
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
			// 不再盲信"stop 即完成"：必须先通过完成度校验（validateAgenticCompletion）。
			// 未完成时引导模型显式提交结论；超过上限则交由上层降级（预组装复评）。
			result, done, perr := e.acceptContentOrNudge(ctx, exec, state, llmResp.Content, detail, plan.BatchCount, promptCtx.DeductScoreConfig, task)
			if perr != nil {
				return nil, 0, 0, exec.ID, perr
			}
			if done {
				return result, state.CumulativeInputTokens, state.CumulativeOutputTokens, exec.ID, nil
			}
			continue

		case "tool_calls":
			// 优先识别终止工具 submit_review：调用即代表"提交最终结论"
			if submit := extractSubmitReviewCall(llmResp.ToolCalls); submit != nil {
				res, herr := tryHandleSubmitReview(state, submit, task)
				if herr != nil {
					return nil, 0, 0, exec.ID, herr
				}
				if res != nil {
					gr, gerr := e.parseAgenticResult(ctx, exec, state, submit.Function.Arguments, detail, plan.BatchCount, promptCtx.DeductScoreConfig, task, res)
					return gr, state.CumulativeInputTokens, state.CumulativeOutputTokens, exec.ID, gerr
				}
				continue
			}
			if state.ForceFinalRound || state.ToolCallsLeft <= 0 {
				// 工具不可用（最终轮/额度耗尽）却仍在调用分析工具 → 记录"跳过"事件并引导提交结论
				reason := "工具预算已用尽，未执行"
				if state.ForceFinalRound {
					reason = "已进入最终轮，未执行分析工具"
				}
				recordSkippedToolCalls(state, llmResp.ToolCalls, reason)
				if nerr := nudgeToSubmit(state, submitNudgeText); nerr != nil {
					return nil, 0, 0, exec.ID, nerr
				}
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
				Role: "user",
				Content: "[系统提示] 上下文长度已达上限，请立即调用 submit_review 提交最终评审结论" +
					"（仅基于已有信息；无问题时提交空 issues 并给出 no_issue_reason），不要继续调用分析工具。",
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
			// 尝试先按 JSON 解析结果（同样需通过完成度校验）
			if resultContent, ok := parseAgenticResultFromContent(llmResp.Content); ok {
				result, done, perr := e.acceptContentOrNudge(ctx, exec, state, resultContent, detail, plan.BatchCount, promptCtx.DeductScoreConfig, task)
				if perr != nil {
					return nil, 0, 0, exec.ID, perr
				}
				if done {
					return result, state.CumulativeInputTokens, state.CumulativeOutputTokens, exec.ID, nil
				}
				continue
			}
			// PseudoTag 兜底：尝试从 content 中解析 <tool_call> 标签
			if pseudoCalls, ok := parseToolCallsFromContent(llmResp.Content); ok {
				// 优先识别伪标签形式的 submit_review
				if submit := extractSubmitReviewCall(pseudoCalls); submit != nil {
					res, herr := tryHandleSubmitReview(state, submit, task)
					if herr != nil {
						return nil, 0, 0, exec.ID, herr
					}
					if res != nil {
						gr, gerr := e.parseAgenticResult(ctx, exec, state, submit.Function.Arguments, detail, plan.BatchCount, promptCtx.DeductScoreConfig, task, res)
						return gr, state.CumulativeInputTokens, state.CumulativeOutputTokens, exec.ID, gerr
					}
					continue
				}
				if state.ForceFinalRound || state.ToolCallsLeft <= 0 {
					reason := "工具预算已用尽，未执行"
					if state.ForceFinalRound {
						reason = "已进入最终轮，未执行分析工具"
					}
					recordSkippedToolCalls(state, pseudoCalls, reason)
					if nerr := nudgeToSubmit(state, submitNudgeText); nerr != nil {
						return nil, 0, 0, exec.ID, nerr
					}
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
			if nerr := nudgeToSubmit(state, submitNudgeText); nerr != nil {
				return nil, 0, 0, exec.ID, nerr
			}
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
	submitTool := submitReviewToolDef
	var tools []llm.ToolDefinition
	if state.ForceFinalRound {
		// 最终轮只保留"提交结论"工具：模型必须显式调用 submit_review 结束，避免直接 stop 输出半成品
		tools = []llm.ToolDefinition{submitTool}
	} else {
		tools = append(buildToolDefinitions(state.ToolExecutor.mode), submitTool)
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
				Schema: llm.GetAgenticSubmitReviewJSONSchema(),
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
		"completion":         batchResult.Completion,
		"no_issue_reason":    batchResult.NoIssueReason,
		"submit_nudges":      state.SubmitNudges,
		"submit_attempts":    state.SubmitAttempts,
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
			"请立即基于已有上下文，直接输出符合 JSON Schema 的评审结果（completion 必须为 complete）。" +
			"若确认无问题，请给出空 issues 并在 no_issue_reason 中说明依据；" +
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
				Schema: llm.GetAgenticSubmitReviewJSONSchema(),
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
	// 强制最终轮同样必须通过完成度校验，否则交由上层降级（预组装复评）
	if ok, reason := validateAgenticCompletion(batchResult); !ok {
		return nil, fmt.Errorf("%w: force final %s", errAgenticIncomplete, reason)
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
		// 【完成度校验】不通过则视为未完成（模型仍在分析/结论不完整），不得落库为"成功"
		if ok, reason := validateAgenticCompletion(result); !ok {
			zap.L().Warn("agentic result incomplete, not accepted",
				zap.Uint("task_id", task.ID),
				zap.Int("batch_index", detail.Index),
				zap.String("reason", reason),
				zap.String("completion", result.Completion),
				zap.Int("issue_count", len(result.Issues)))
			return nil, fmt.Errorf("%w: %s", errAgenticIncomplete, reason)
		}
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
				Schema: llm.GetAgenticSubmitReviewJSONSchema(),
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
	// 重试结果同样需要完成度校验
	if ok, reason := validateAgenticCompletion(result); !ok {
		return nil, fmt.Errorf("%w: %s", errAgenticIncomplete, reason)
	}

	// 重试成功，追加一轮metrics。
	// 使用独立 role（llm_retry）避免与同轮原始 llm_call 冲突（否则前端按 round_index 分组时
	// 后者会覆盖前者，导致展示的是被截断的重试预览）；同时保存完整内容，保证详情展示完整。
	state.Rounds = append(state.Rounds, AgenticRoundMetrics{
		RoundIndex:     state.CurrentRound,
		Role:           "llm_retry",
		InputTokens:    llmResp.InputTokens,
		OutputTokens:   llmResp.OutputTokens,
		FinishReason:   llmResp.FinishReason,
		ContentPreview: llmResp.Content,
	})

	return e.parseAgenticResult(ctx, exec, state, llmResp.Content, detail, totalBatches, deductCfg, task, result)
}

// AgenticPrompt 极简初始 Prompt
type AgenticPrompt struct {
	SystemPrompt string
	UserPrompt   string
}

// buildAgenticInitialPrompt 构建 Agentic 模式的极简初始 Prompt
func buildAgenticInitialPrompt(promptCtx *engine.PromptContext, batchFiles []map[string]interface{}, isLastBatch bool) AgenticPrompt {
	systemPrompt := engine.BuildAgenticSystemPrompt(promptCtx)
	userPrompt := engine.BuildAgenticUserPrompt(promptCtx, batchFiles, isLastBatch)
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
	// 按 rune 截断，避免在多字节（中文）字符中间截断导致乱码
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}

func calculateMaxTokens(modelID uint) int {
	// 从模型配置读取 max_tokens
	var m model.LLMModel
	if err := model.DB.Select("max_tokens").First(&m, modelID).Error; err == nil && m.MaxTokens > 0 {
		return m.MaxTokens
	}
	return 4096
}
