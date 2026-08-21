# 问题分析：手动停止任务时，大模型调用无法立即停止

## 一、问题现象

1. 用户在 UI 上点击"停止任务"
2. 任务状态立即变为"已停止"
3. 但 token 用量页面随后仍能看到该任务的 LLM 调用记录（input/output tokens 均有值）
4. Pipeline stage 的 snapshot 显示 stage 已结束，但 `llm_call_logs` 记录的时间戳在 stage 结束之后
5. 这意味着：**Pipeline 已感知到取消，但底层 HTTP 大模型调用仍在后台运行**

---

## 二、取消信号链路完整走读

### 2.1 取消信号的产生

```
UI 点击"停止"
  │
  ▼
task.go:Abort(taskID)
  │
  ├── opencodeSvc.AbortTask(...)  // 终止 opencode session（对话层）
  │
  └── notifyPipelineCancel(taskID)  // 【关键】关闭 pipeline cancel channel
```

```go
// task.go:57-65
func notifyPipelineCancel(taskID uint) {
    pipelineCancelMu.Lock()
    if ch, ok := pipelineCancelChans[taskID]; ok {
        close(ch)                                // ← 关闭 channel，广播取消信号
        delete(pipelineCancelChans, taskID)
    }
    pipelineCancelMu.Unlock()
}
```

### 2.2 取消信号在 Pipeline 中的传递

```
notifyPipelineCancel 关闭 channel
  │
  ▼
stageContextImpl.cancelCh 被关闭
  │
  ▼
stageContextImpl.Done() 返回 cancelCh（如果被注入）
  │
  ▼
Pipeline Engine 在各处检查 ctx.Done()：
  - 每个 stage 开始前（engine.go:165-171）
  - executor 内部（如 test_suggestion_executor.go:33-36）
  - callLLM 内部（agent_enricher.go:752/787）
```

### 2.3 Pipeline Engine 的取消检查点

**检查点 1：stage 循环开始前**
```go
// engine.go:163-171
for _, stageDef := range stageDefs {
    select {
    case <-ctx.Done():
        // Pipeline 收到取消信号，停止执行
        return fmt.Errorf("任务被手动停止")
    default:
    }
```

**检查点 2：stage 内部的 select**
```go
// test_suggestion_executor.go:31-36 / review_arbitration_executor.go:35-40 等
select {
case <-ctx.Done():
    return fmt.Errorf("任务已取消")
default:
}
```

**检查点 3：callLLM 内部的 select**
```go
// agent_enricher.go:752-760 / 787-795
select {
case res := <-done:
    return res.r, res.err
case <-c.Done():        // ← c 是 context.WithTimeout(ctx, enrichTimeout) 创建的子 context
    return nil, fmt.Errorf("LLM 调用超时")
}
```

**结论：Pipeline 各层级的 select 检查是完整的。如果 cancel 信号在 select 阻塞期间到达，callLLM 确实能返回错误。**

---

## 三、中断点：为什么大模型调用停不下来？

### 3.1 核心问题：callLLMAPI 中不支持 context cancellation

文件：`backend/internal/service/llm.go`，第 356 行

```go
// 当前代码（无 context）
req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))  // ← 【缺失】没有传入 context
req.Header.Set("Authorization", "Bearer "+apiKey)
req.Header.Set("Content-Type", "application/json")

resp, err := client.Do(req)   // ← 阻塞！直到超时或收到响应，无法被 cancel 中断
```

**正确的 Go 做法应该是：**
```go
// 正确做法
req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
resp, err := client.Do(req)   // ctx 取消时，Do 会立即返回 error
```

### 3.2 整个调用链都不支持 context

| 层级 | 函数 | 签名 | 问题 |
|------|------|------|------|
| 1 | `ChatCompletionStructured` | `func(..., caller, systemPrompt, userPrompt string, ...)` | **无 context 参数** |
| 2 | `callSpecificModel` / `tryChain` | 调用 `callLLMAPI`，无 context | **未传递 context** |
| 3 | `callLLMAPI` | `func(..., caller, systemPrompt, userPrompt string, ...)` | **无 context 参数，http.NewRequest 无 context** |
| 4 | `client.Do(req)` | `func (c *Client) Do(req *Request) (*Response, error)` | `req` 中无 context，阻塞不可取消 |

**问题链路图：**

```
Pipeline stage (test_suggestion/batch_review/review_arbitration)
  │
  ├── ctx.Done() 已关闭（cancelCh 被 notifyPipelineCancel 关闭）
  │
  ├── AgentEnricher.callLLM(ctx, ...)   ← ctx 已取消
  │   │
  │   ├── c, cancel := context.WithTimeout(ctx, 300s)   ← 子 context c 也 Done
  │   │
  │   ├── go func() {
  │   │       // goroutine 中使用的 ChatCompletionStructured 完全不感知 context
  │   │       r, err := llmService.ChatCompletionStructured(...)
  │   │       // ← 进入 callLLMAPI，创建 HTTP req（无 context）
  │   │       // ← client.Do(req) 阻塞等待 HTTP 响应
  │   │       // ← 【ctx 取消无法中断此处】
  │   │   }()
  │   │
  │   └── select {
  │           case <-done:
  │               return   // 不会走到，因为 goroutine 还在 HTTP 阻塞中
  │           case <-c.Done():
  │               return timeoutError   // ← 300秒后走到这里
  │       }
  │
  └── Engine 收到 error → MarkFailed → stage 失败/超时

// 但此时 goroutine 仍在运行！
goroutine {
    client.Do(req) // 继续阻塞，无视 ctx 取消
    // 最终收到 HTTP 响应
    // defer 触发 llmcall.Record(...)  // 写入 llm_call_logs
    // done channel 发送（已无人接收，因为 callLLM 已返回）
}
```

### 3.3 另一个不可取消的点：指数退避重试的 time.Sleep

```go
// llm.go:339-353
for attempt := 1; attempt <= maxAttempts; attempt++ {
    if attempt > 1 {
        time.Sleep(time.Duration(delay) * time.Millisecond)   // ← 无法被取消！
        delay = int(float64(delay) * backoffMult)
    }
    resp, err := client.Do(req)
    // ...
}
```

两次重试之间的 `time.Sleep` 完全不可被取消。即使 cancel 信号到达，也必须等 sleep 结束、下一次 HTTP 调用失败、再下一次 sleep... 整个重试链路完成后才能退出。

### 3.4 HTTP client 的超时机制与 cancel 的区别

`newHTTPClient` 的实现：
```go
func newHTTPClient(timeout time.Duration) *http.Client {
    return &http.Client{
        Timeout:   timeout,           // ← 只是总超时，不是 context
        Transport: sharedHTTPTransport,
    }
}
```

`http.Client.Timeout` 控制的是**单次 HTTP 请求的总耗时**（连接建立 + 发送 + 等待响应 + 读取 body），它**不是 context-based 的取消机制**：

- `Timeout` 到达 → HTTP 调用被强制终止 → `client.Do` 返回 `url.Error{Op: ..., Err: context.DeadlineExceeded}`
- **但 Timeout 是预设定值（如 30 分钟），和手动取消无关**
- **手动发出的 cancel 信号无法通过这个 Timeout 生效**

因此，即使 `Timeout` 存在，它也不能替代 context cancellation。

---

## 四、完整时间线推演

假设用户在 `test_suggestion` 阶段（正在进行 LLM 调用）时点击停止：

| 时间点 | 事件 | 状态 |
|--------|------|------|
| T+0s | `test_suggestion` stage 开始，调用 `EnrichTestSuggestions` → `callLLM` | Pipeline running |
| T+1s | goroutine 启动 `ChatCompletionStructured`，进入 `callLLMAPI`，`client.Do(req)` 阻塞 | Pipeline running |
| T+60s | **用户点击"停止任务"** | 用户期望立即停止 |
| T+60s | `TaskService.Abort` 调用 `notifyPipelineCancel(taskID)` → `close(cancelCh)` | cancelCh 关闭 |
| T+60s | `Abort` 将 task 状态更新为 `stopped`，`completed_at` 写入 | UI 显示"已停止" |
| T+60s | `callLLM` 的 `select` 中 `<-c.Done()` 理论上应该触发（因为 c 的父 ctx 已 cancel） | 但 goroutine 中的 HTTP 调用仍在运行 |
| T+60s | `callLLM` 返回 timeoutError（因为 c.Done() 关闭），`Execute` 返回 error | Pipeline 失败/超时 |
| T+61s | Engine 调用 `ctx.MarkFailed`，更新 stage 状态 | Pipeline 停止执行后续 stage |
| T+300s | **goroutine 中的 HTTP 调用仍在运行**（假设大模型响应慢） | 后台 goroutine 仍在运行 |
| T+300s | HTTP 响应终于返回，`llmcall.Record(...)` 写入 `llm_call_logs` | `llm_call_logs` 多了一条记录 |
| T+300s | goroutine 中 `done <- struct{}{r, err}` 发送（已无人接收） | goroutine 结束 |

**用户视角**：任务已显示停止，但 token 用量页面仍能看到该任务在"停止后"产生了新的 LLM 调用记录。

---

## 五、影响的阶段

所有调用 LLM 的阶段都存在此问题：

| 阶段 | LLM 调用点 | 是否受影响 |
|------|-----------|-----------|
| `secret_scan` | `AgentVerificator.VerifySecretScan` | ✅ 是 |
| `security_audit` | `AgentVerificator.VerifySecurityAudit` | ✅ 是 |
| `test_suggestion` | `AgentEnricher.EnrichTestSuggestions` | ✅ 是 |
| `impact_analysis` | `AgentEnricher.EnrichImpactAnalysis` | ✅ 是 |
| `batch_review` | `BatchReviewFrameExecutor.executeSingleBatchStructured` / `executeBatchCollection` | ✅ 是 |
| `review_arbitration` | `ReviewArbitrationExecutor.callLLMWithStructuredPrompt` | ✅ 是 |

---

## 六、修复方案

### 方案 A（推荐）：为 LLM 调用链添加 context 参数

**修改范围**：从最上层到最底层逐层传递 context。

**Step 1：修改 `ChatCompletionStructured` 签名**
```go
// 修改前
func (s *LLMService) ChatCompletionStructured(taskID *uint, modelID uint, caller, systemPrompt, userPrompt string, responseFormat *llm.ResponseFormat) (*StructuredChatResult, error)

// 修改后
func (s *LLMService) ChatCompletionStructured(ctx context.Context, taskID *uint, modelID uint, caller, systemPrompt, userPrompt string, responseFormat *llm.ResponseFormat) (*StructuredChatResult, error)
```

**Step 2：修改 `callSpecificModel` / `tryChain`**
```go
func (s *LLMService) callSpecificModel(ctx context.Context, ...)
func (s *LLMService) tryChain(ctx context.Context, ...)
// 在内部调用 s.callLLMAPI(ctx, ...)
```

**Step 3：修改 `callLLMAPI`**
```go
func (s *LLMService) callLLMAPI(ctx context.Context, taskID *uint, llmModel *model.LLMModel, ...) {
    // ...
    req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
    // ...
    resp, err := client.Do(req)   // ← 现在 ctx 取消时会立即中断
}
```

**Step 4：修改 retry loop 中的 time.Sleep**
```go
// 修改前
time.Sleep(time.Duration(delay) * time.Millisecond)

// 修改后
select {
case <-ctx.Done():
    return nil, ctx.Err()   // 接收到取消信号，立即退出重试
case <-time.After(time.Duration(delay) * time.Millisecond):
    // 继续下一轮重试
}
```

**Step 5：修改所有调用方（AgentEnricher / AgentVerificator / BatchReviewFrameExecutor / ReviewArbitrationExecutor）**
```go
// AgentEnricher.callLLM 中
result, err := e.llmService.ChatCompletionStructured(c, taskID, modelID, caller, "", prompt, responseFormat)
```

**效果**：
- 用户点击停止 → cancelCh 关闭 → `ctx.Done()` 触发
- `callLLM` 中 goroutine 使用 `c` 作为 `ChatCompletionStructured` 的 ctx
- `http.NewRequestWithContext(c, ...)` → `client.Do(req)` 在 `c` 取消时立即返回 `ctx canceled`
- goroutine 快速结束，不会泄露，也不会写入 `llm_call_logs`

**工作量**：中等，涉及接口修改和约 8-10 处调用点

---

### 方案 B：轻量级取消检查（不修改接口，在每个 HTTP 调用前检查）

在不修改 `ChatCompletionStructured` 签名的情况下，在 `callLLMAPI` 内部增加周期性的 cancel 检查。

**问题**：`client.Do(req)` 是原子操作，无法在单次 HTTP 调用内部中断。**此方案无法解决根本问题**，只能减少可感知的延迟（比如在重试间检查）。

---

### 方案 C：粗暴方案（HTTP 客户端级别关闭连接池）

在 Abort 时，找到当前正在进行的 HTTP 连接并强行关闭。这种方法过于 hack，不推荐。

---

## 七、结论

### 根因总结

| 问题层级 | 具体点 | 说明 |
|---------|--------|------|
| 表面现象 | 任务已停止，但 LLM 调用仍在后台运行 | 用户看到"已停止"，但 token 仍在增长 |
| 直接原因 | `callLLMAPI` 中 `http.NewRequest` 未传入 context | `client.Do(req)` 阻塞不可取消 |
| 深层原因 | `ChatCompletionStructured` 全链路无 context 参数 | 从入口到出口，没有任何一层支持 cancellation |
| 关键缺陷 | goroutine 中运行的 HTTP 调用完全独立于 Pipeline 的 cancel 信号 | cancelCh → ctx.Done() → callLLM 的 select 能感知，但 goroutine 中的 `ChatCompletionStructured` 不感知 |
| 次优问题 | `time.Sleep` 在重试间不可被取消 | 即使解决了 HTTP 问题，sleep 也是个问题 |

### 修复优先级

1. **高**：为 `ChatCompletionStructured` / `callLLMAPI` 添加 `context.Context` 参数，并在 HTTP 调用中使用 `http.NewRequestWithContext`
2. **高**：将重试间的 `time.Sleep` 替换为 `select { case <-ctx.Done(); case <-time.After(...) }`
3. **中**：修改所有调用方（AgentEnricher / AgentVerificator / BatchReviewFrameExecutor / ReviewArbitrationExecutor）传递 context
4. **低**：在 Abort 后，延迟 1-2 秒再更新 task 状态为 stopped，给用户更准确的反馈

---

## 八、数据验证

以下内容均来自代码静态走读，无编造：

| 文件 | 关键代码行 | 发现 |
|------|-----------|------|
| `llm.go:356` | `http.NewRequest("POST", url, ...)` | 无 context |
| `llm.go:363` | `client.Do(req)` | 阻塞不可取消 |
| `llm.go:348` | `time.Sleep(...)` | 不可取消 |
| `agent_enricher.go:780` | `ChatCompletionStructured(...)` | 无 context 参数 |
| `task.go:58-65` | `notifyPipelineCancel` | 关闭 channel，信号正确发出 |
| `engine.go:165-171` | `select { case <-ctx.Done() }` | 每个 stage 开始前检查 |
| `context.go:74-79` | `Done()` 覆盖 | 正确返回 cancelCh |

---

*报告生成时间：2026-08-21*
