// Package llmcall 提供 LLM 调用日志的异步落库能力。
//
// 调用方在每次 LLM 调用完成时调用 Record，事件被送入有缓冲 channel，
// 后台 worker 协程按批次（每 100 条或 1 秒）将记录写入 llm_call_logs 表。
//
// 设计动机：
//   - 不在 LLM 响应热路径上执行 DB INSERT，避免抖动调用延迟
//   - channel 满时丢弃并记 warn，保证主流程不被阻塞
//   - 应用退出时通过 Stop() 排空残留 buffer
//   - 用 atomic.Bool 管理 started，避免热路径锁竞争与 TOCTOU 竞态
//   - 用 stopCh 信号 + drain 模式退出，logChan 永远不 close，避免 producer race
package llmcall

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

const (
	channelBufferSize = 1000
	flushBatchSize    = 100
	flushInterval     = 1 * time.Second
	workerCount       = 2
	flushTimeout      = 5 * time.Second
)

// RecordRequest 一次 LLM 调用的记录请求。
// TaskID/ModelID 允许为空（预留 OpenCode 等无 task 场景）。
// TotalTokens 可由调用方传入（LLM 返回的 usage.total_tokens），<=0 时由 recorder 计算 PromptTokens+CompletionTokens。
// CostCents 成本（单位：分），<=0 表示未配置价格或无需计算。
type RecordRequest struct {
	TaskID           *uint
	ModelID          *uint
	OrgID            uint
	Provider         string
	ModelName        string
	CallType         string
	Caller           string
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	TotalTokens      int
	CostCents        int64
	DurationMs       int
	Status           string
	ErrorMsg         string
}

// started 用 atomic.Bool 控制，避免热路径锁竞争；
// stopOnce 保证 stopCh 只 close 一次（幂等）；
// dbWarnOnce 保证 "model.DB 未初始化" 只 warn 一次（启动早期不刷屏）；
// startOnce 保证 Start 多次调用只有第一次生效。
var (
	started    atomic.Bool
	logChan    chan RecordRequest
	stopCh     chan struct{}
	stopOnce   sync.Once
	startOnce  sync.Once
	dbWarnOnce atomic.Bool
	wg         sync.WaitGroup
)

// Record 将一次 LLM 调用送入异步落库队列。
// channel 满时丢弃并记 warn，保证主流程不被阻塞。
// Stop() 调用之后 Record 自动失效（直接 return），无需调用方感知生命周期。
func Record(req RecordRequest) {
	if !started.Load() {
		return
	}
	select {
	case logChan <- req:
	default:
		zap.L().Warn("llm call log channel full, dropping record",
			zap.String("call_type", req.CallType),
			zap.String("model", req.ModelName))
	}
}

// Start 启动后台 worker 协程。必须在 DB 初始化完成之后调用一次。
// 多次调用安全：仅有一次生效。
func Start() {
	startOnce.Do(func() {
		logChan = make(chan RecordRequest, channelBufferSize)
		stopCh = make(chan struct{})
		started.Store(true)
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go worker(i)
		}
		zap.L().Info("llmcall worker started", zap.Int("workers", workerCount))
	})
}

// Stop 通知 worker 排空 channel 后退出，并等待所有 worker 完成。
// 多次调用安全：通过 stopOnce 保证 stopCh 只 close 一次。
// 注意：必须在所有 Record 调用方停止后再调用。
func Stop() {
	stopOnce.Do(func() {
		if !started.Load() {
			return
		}
		// 先标记关闭，阻止新 producer；再发信号让 worker drain 后退出
		started.Store(false)
		close(stopCh)
	})
	wg.Wait()
}

func worker(id int) {
	defer wg.Done()
	batch := make([]RecordRequest, 0, flushBatchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		flushBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case req := <-logChan:
			batch = append(batch, req)
			if len(batch) >= flushBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-stopCh:
			// drain 剩余数据：started=false 已阻挡新 producer，可安全排空
			for {
				select {
				case req := <-logChan:
					batch = append(batch, req)
					if len(batch) >= flushBatchSize {
						flush()
					}
				default:
					flush()
					zap.L().Info("llmcall worker exited", zap.Int("worker_id", id))
					return
				}
			}
		}
	}
}

func flushBatch(batch []RecordRequest) {
	if len(batch) == 0 {
		return
	}
	if model.DB == nil {
		// 仅在启动早期一次性 warn，避免高频日志刷屏
		if dbWarnOnce.CompareAndSwap(false, true) {
			zap.L().Warn("llmcall flush skipped: model.DB not initialized")
		}
		return
	}
	now := time.Now()
	rows := make([]model.LLMCallLog, len(batch))
	for i, r := range batch {
		status := r.Status
		if status == "" {
			status = "success"
		}
		caller := r.Caller
		if caller == "" {
			caller = "unknown"
		}
		// 优先用调用方传入的 TotalTokens（如 LLM 返回的 usage.total_tokens），缺失时回退到求和
		total := r.TotalTokens
		if total <= 0 {
			total = r.PromptTokens + r.CompletionTokens
		}
		if r.OrgID == 0 {
			r.OrgID = 1 // fallback 到根组织
		}
		rows[i] = model.LLMCallLog{
			OrgID:            r.OrgID,
			TaskID:           r.TaskID,
			ModelID:          r.ModelID,
			Provider:         r.Provider,
			ModelName:        r.ModelName,
			CallType:         r.CallType,
			Caller:           caller,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			CachedTokens:     r.CachedTokens,
			TotalTokens:      total,
			CostCents:        r.CostCents,
			DurationMs:       r.DurationMs,
			Status:           status,
			ErrorMsg:         r.ErrorMsg,
			CreatedAt:        now,
		}
	}

	// 指数退避重试：抖动避免雪崩；最终失败保留 batch 等待下次 flush。
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		err := model.DB.WithContext(ctx).CreateInBatches(rows, flushBatchSize).Error
		cancel()
		if err == nil {
			if attempt > 1 {
				zap.L().Info("llmcall flush recovered",
					zap.Int("attempt", attempt), zap.Int("count", len(rows)))
			}
			return
		}
		lastErr = err
		// 退避：200ms / 400ms / 800ms + jitter
		backoff := time.Duration(200*(1<<(attempt-1))) * time.Millisecond
		jitter := time.Duration(time.Now().UnixNano() % 100)
		time.Sleep(backoff + jitter*time.Millisecond)
	}
	zap.L().Error("llmcall flush failed after retries",
		zap.Int("count", len(rows)),
		zap.Int("attempts", maxAttempts),
		zap.Error(lastErr))
}
