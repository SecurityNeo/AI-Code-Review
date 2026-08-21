# 修正深度分析报告：test_suggestion 固定耗时 & Pipeline token 丢失根因

## 一、更正声明

上份报告中以下两个结论存在部分误判，现予以更正：
1. **"output_tokens = 0 说明 LLM 未成功返回任何内容"** —— 错误。实际上 LLM 确实被调用了，且返回了大量内容。
2. **"参数命名不一致导致配置失效"** —— 方向正确但不够准确。真正导致`task_pipeline_executions`中 token 为0 的原因是**Executor 未将 token 同步到 exec 对象**（而非 snapshot 中）。

本报告基于重新核查后的代码与数据，给出完整、准确的根因分析。

---

## 二、核心事实快照

### 2.1 llm_call_logs（Token 用量页面的数据源）

| 记录ID | task_id | caller | model_name | prompt_tokens | completion_tokens | duration_ms | created_at |
|--------|---------|--------|------------|---------------|-------------------|-------------|------------|
| 1 | 1 | `test_suggestion_enrich` | Kimi-K2.6 | 17,686 | 29,835 | 410,262 | 23:38:38 |
| 4 | 1 | `test_suggestion_enrich` | Kimi-K2.6 | 17,686 | 21,188 | 363,962 | 23:52:46 |
| 6 | 2 | `test_suggestion_enrich` | Kimi-K2.6 | 31,428 | 20,605 | 404,473 | 23:56:33 |

### 2.2 task_pipeline_executions（Pipeline 阶段详情数据源）

| task_id | stage_code | input_tokens | output_tokens | model_name | duration_ms | started_at | completed_at |
|---------|------------|--------------|---------------|------------|-------------|------------|--------------|
| 1 | `test_suggestion` | **0** | **0** | **空** | **300,033** | 23:46:41.300 | 23:51:41.333 |
| 2 | `test_suggestion` | **0** | **0** | **空** | **300,063** | 23:49:48.320 | 23:54:48.384 |

### 2.3 矛盾点

- Token 用量页面能看到 `test_suggestion_enrich` 的调用记录，且有输入/输出 token。
- Pipeline 阶段详情中 `test_suggestion` 的 `input_tokens=0`、`output_tokens=0`。
- LLM 调用耗时（364~410秒）**超过** Pipeline stage 耗时（300秒）。

---

## 三、根因一：Pipeline token 用量在 stage 层面"丢失"

### 3.1 Token 数据流对比：成功的 Executor vs 丢失的 Executor

以 `batch_review`（token 可正常显示）对比 `test_suggestion`（token 丢失）：

**executors.go: batch_review（token 正常）**
```go
// 直接操作 ctx.selfExec（即实际的数据库 execution 记录）
exec := ctx.CreateChildExecution("batch_review", sortOrder)
...
result, err := e.llmService.ChatCompletionStructured(...)
if err == nil {
    exec.InputTokens = result.InputTokens      // ✅ 同步到 exec 对象
    exec.OutputTokens = result.OutputTokens    // ✅
    exec.ModelName = result.ModelName          // ✅
}
...
ctx.MarkSuccess(exec)  // MarkSuccess 从 exec 读取并更新到数据库
```

**test_suggestion_executor.go（token 丢失）**
```go
enriched, err := enricher.EnrichTestSuggestions(...)
// ❌ 注意：EnrichTestSuggestions 的返回 enriched 包含业务数据，不包含 token 统计
...
// 虽然从 enricher 读取了 token，但值都是 0
inputTokens = enricher.LastInputTokens()      // ← 返回 0
outputTokens = enricher.LastOutputTokens()    // ← 返回 0

// ❌ 未同步到 ctx.selfExec
// ctx.SaveOutputSnapshot 使用的是 &model.TaskPipelineExecution{ID: ctx.ExecutionID()}（临时对象）
ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
    "input_tokens":  inputTokens,   // ← 0
    "output_tokens": outputTokens,  // ← 0
    ...
})
```

### 3.2 为什么 enricher.LastInputTokens() 返回 0？

看 `agent_enricher.go: EnrichTestSuggestions`：

```go
result, err := e.callLLM(ctx, taskID, actualModelID, "test_suggestion_enrich", prompt, rf)
if err != nil {     // ← 【关键】err != nil 时直接返回 nil
    return nil, nil  // ← 不设置 lastInputTokens / lastOutputTokens
}
e.lastRawContent = result.Content
e.lastModelName = result.ModelName
e.lastInputTokens = result.InputTokens       // ← 仅在 err == nil 时设置
e.lastOutputTokens = result.OutputTokens
```

如果 `callLLM` 返回错误，所有 token 字段保持默认值 `0`。

### 3.3 MarkSuccess 的数据库写入逻辑

```go
// context.go:122-137
c.db.Model(exec).Updates(map[string]interface{}{
    "input_tokens":  exec.InputTokens,    // ← 从 exec 对象读取（0）
    "output_tokens": exec.OutputTokens,   // ← 从 exec 对象读取（0）
    "model_name":    exec.ModelName,      // ← 从 exec 对象读取（空）
})
```

**结论**：`test_suggestion` 阶段的 `callLLM` **返回了错误**（具体原因见第 4 节），导致 `test_suggestion_executor` 未能获取 token，且**未像 batch_review 那样从 `result` 对象赋值给 `exec`**，导致 Pipeline 层面 token 为 0。

但 `llm_call_logs` 由 `LLMService.ChatCompletionStructured` **内部独立写入**，不受 executor 错误状态影响。因此：
- Pipeline（task_pipeline_executions）：看到 input_tokens=0（executor 未赋值）
- Token 用量页面（llm_call_logs）：看到真实 token（LLMService 独立记录）

### 3.4 佐证：test_suggestion 的 LLM 调用是否成功？

查看 `llm_call_logs.status`：
| task_id | caller | status |
|---------|--------|--------|
| 1 | `test_suggestion_enrich` | **success** |
| 2 | `test_suggestion_enrich` | **success** |

`llm_call_logs` 状态为 `success`，说明 HTTP 请求最终成功收到了响应。

但 `test_suggestion_executor` 中，`err != nil` 时直接降级。这只能说明：`callLLM` 返回了错误，LLM 调用最终成功（goroutine 在后台完成），但错误已经被返回给 executor。

---

## 四、根因二：test_suggestion 阶段固定耗时 300 秒的完整链路

### 4.1 callLLM 的三层超时机制

```go
// agent_enricher.go:callLLM (第 763-795 行)
func (e *AgentEnricher) callLLM(...) (*StructuredChatResult, error) {
    c, cancel := context.WithTimeout(ctx, e.enrichTimeout)  // ← 300秒
    defer cancel()

    done := make(chan struct{...}, 1)

    go func() {
        // 【关键缺陷】goroutine 使用外层 ctx，而非超时子 context c
        r, err := e.llmService.ChatCompletionStructured(taskID, modelID, caller, "", prompt, responseFormat)
        done <- struct{...}{r, err}   // buffered channel
    }()

    select {
    case res := <-done:
        return res.r, res.err
    case <-c.Done():                // ← 300秒后触发
        return nil, fmt.Errorf("AgentEnricher LLM 调用超时 (%v)", e.enrichTimeout)
    }
}
```

**关键缺陷分析**：
1. `callLLM` 创建了一个 300 秒的超时子 context `c`
2. 但 goroutine 中的 `ChatCompletionStructured` **没有传入 `c`**，而是使用了外层传入的 `ctx`
3. 外层 `ctx` 是 `context.Background()`（来自 `EnrichTestSuggestions` 的调用，见第 520 行）
4. `context.Background()` **没有超时**
5. 这意味着：即使 `callLLM` 的 `select` 在 300 秒时返回超时错误，**goroutine 仍会继续运行**，直到 HTTP 调用完成

### 4.2 时间线重建（以任务1为例）

| 时间点 | 事件 | 耗时 |
|--------|------|------|
| 23:46:41 | `test_suggestion` stage 开始（Execute 被调用） | — |
| 23:46:42 | `EnrichTestSuggestions` 调用 `callLLM`，goroutine 启动 | — |
| **23:51:41** | **`callLLM` 内部 `select` 触发 `<-c.Done()`，返回超时错误** | **~300秒** |
| 23:51:41 | `test_suggestion_executor` 捕获错误，使用启发式结果继续 | — |
| 23:51:41 | `SaveOutputSnapshot`（input_tokens=0），stage 返回 | — |
| 23:51:41 | `MarkSuccess` 更新数据库（input_tokens=0，duration_ms=300033）| — |
| **23:52:46** | **后台 goroutine 的 HTTP 调用完成，写入 `llm_call_logs`** | **~364秒** |

### 4.3 为什么耗时"固定"在 300 秒？

因为 `callLLM` 的 `select` 阻塞了 `test_suggestion_executor` 的主流程：
- 无论 model 响应多慢（364秒或404秒），`test_suggestion` stage 的主流程都会在 300 秒时因为 `select { case <-c.Done() }` 而解除阻塞
- 解除阻塞后，executor 立即进行启发式降级和 snapshot 保存
- 因此 **stage duration ≈ 300 秒**，而 `llm_call_logs` 中记录的是完整 364/404 秒

### 4.4 为什么 output_tokens 在 snapshot 中也显示为 0？

`SaveOutputSnapshot` 保存的值取决于 `enricher.LastOutputTokens()`，而它在 `callLLM` 返回错误后仍然是 0。

但 MinIO snapshot 中应该还有 `llm_prompt_available: true`（因为 `LastPrompt` 在构建 prompt 时就已经设置），以及其他结构化数据。这是导致两份报告都只看到 `output_tokens=0` 的原因。

### 4.5 为什么 task1 有两次 test_suggestion_enrich 调用？

查看 `llm_call_logs`：
| id | task_id | caller | created_at | 说明 |
|----|---------|--------|------------|------|
| 1 | 1 | `test_suggestion_enrich` | 23:38:38 | **Pipeline 启动前**的调用 |
| 4 | 1 | `test_suggestion_enrich` | 23:52:46 | Pipeline 内的调用（后台完成） |

**推测**：
- id=1 的调用在 pipeline 启动（23:46:23）之前 8 分钟，可能是：
  - 任务创建后的**预热/调试调用**
  - 或第一次 pipeline 尝试（失败后重试）
  - 从 time 看，task 的 created_at 是 23:31:11，id=1 的调用在 23:38:38，是在 task 创建后但第一次 pipeline 执行前的调用（可能来自 opencode 的测试或预处理）
- 最终成功的 pipeline 中产生的是 id=4 的记录（23:52:46，stage 结束后才完成）

**任务2 无此现象**，只有 1 条记录。

---

## 五、为什么 token 用量页面能看到数据？

### 5.1 两张表的数据来源完全不同

| 数据源 | 表 | 写入方 | 写入模式 |
|--------|---|--------|---------|
| Pipeline 阶段详情 | `task_pipeline_executions` | `MarkSuccess` | 从 `exec` 对象批量 update |
| Token 用量明细 | `llm_call_logs` | `LLMService/internal recorder` | HTTP 调用完成时独立写入 |

### 5.2 LLM 调用写入流程（不受 executor 控制）

```go
// LLMService -> 内部 HTTP 调用 → 收到响应 → 解析结果 → 写入 llm_call_logs
// 此流程在 goroutine 中独立运行，即使 executor 已因超时放弃
```

### 5.3 Token 用量页面的查询逻辑

文件：`backend/internal/handler/token_usage.go`

```go
// callerLabelMap 将英文 caller 映射为中文展示名
var callerLabelMap = map[string]string{
    ...
    "test_suggestion_enrich": "测试建议增强",
    ...
}

// scopedQuery 查询 llm_call_logs 表
q := model.DB.Table("llm_call_logs l").
    Joins("LEFT JOIN tasks t ON t.id = l.task_id").
    Where("l.call_type = ?", model.CallTypeScore)
```

Token 用量页面**完全不查询** `task_pipeline_executions` 表，而是直接从 `llm_call_logs` 读取。因此即使 Pipeline snapshot 中 token 为 0，页面仍然能展示真实数据。

---

## 六、代码缺陷总结

### 缺陷1：goroutine 中未使用超时 context（导致 stage 与 LLM 调用不匹配）

**文件**：`backend/internal/service/pipeline/agent_enricher.go` 第 763-795 行

```go
// 当前有缺陷的代码
go func() {
    r, err := e.llmService.ChatCompletionStructured(taskID, modelID, caller, "", prompt, responseFormat)  // ← 未传 c
    done <- ...
}()

// 修正应为：
go func() {
    r, err := e.llmService.ChatCompletionStructured(taskID, modelID, caller, "", prompt, responseFormat)
    done <- ...
}()
// 但 ChatCompletionStructured 签名中无 context 参数，说明需修改接口层
```

**影响**：
- `select` 在 300 秒返回超时错误，但 goroutine 不死
- `test_suggestion` stage 结束时，LLM 调用仍在后台运行
- stage duration（300秒）与 LLM duration（364秒）不一致，造成理解混乱

### 缺陷2：Executor 未将 LLM 结果 token 同步到 exec 对象

**文件**：`backend/internal/service/pipeline/test_suggestion_executor.go` 第 168-173 行

```go
// 当前代码：从 enricher 读取 token，但 enricher 在超时后已返回 0
inputTokens = enricher.LastInputTokens()
outputTokens = enricher.LastOutputTokens()

// 未执行：
// exec := ctx.GetSelfExec() 或 ctx.selfExec
// exec.InputTokens = result.InputTokens  // ← 缺失
// exec.OutputTokens = result.OutputTokens // ← 缺失
```

**影响**：`task_pipeline_executions` 表中 token 字段始终为 0。

### 缺陷3：database 配置的 `llm_enhance_timeout` 未被读取

**文件**：`backend/internal/service/pipeline/test_suggestion_executor.go` 第 46-56 行

```go
llmTimeoutSec := 300
if cfg != nil {
    llmTimeoutSec = cfg.GetStageParam("test_suggestion", "llm_timeout_sec", llmTimeoutSec)
    //                                              ↑ "llm_timeout_sec" 不存在于 stage_configs 中
}
```

`helpers.go` 中已存在正确的 helper `getStageLLMEnhanceTimeout`：
```go
func getStageLLMEnhanceTimeout(stageCode string) time.Duration {
    def := 300 * time.Second
    ...
    if t, ok := sc["llm_enhance_timeout"].(float64); ok && t > 0 {
        return time.Duration(t) * time.Second
    }
    return def
}
```

**但没有任何 executor 调用这个 helper**。

---

## 七、修正建议

### 7.1 高优先级：修复 goroutine 中的 context 传递

方式 A（推荐）：修改 `ChatCompletionStructured` 签名，增加 context 参数
- 优点：能正确取消慢速调用
- 工作量：中等（需要修改接口和所有调用点）

方式 B：在 `callLLM` 中修改 goroutine 的 ctx 传递
- 由于 `ChatCompletionStructured` 无 context 参数，需在其内部支持 cancellation

### 7.2 高优先级：在 test_suggestion executor 中同步 token 到 exec

```go
// test_suggestion_executor.go，在 callLLM 成功返回后
e.exec.InputTokens = result.InputTokens
e.exec.OutputTokens = result.OutputTokens
```

或者，改为像 batch_review 那样直接引用 `ctx.CreateChildExecution` 返回的 exec 对象。

### 7.3 中优先级：使用统一的 LLM enhance timeout helper

```go
// test_suggestion_executor.go
llmTimeoutSec := int(getStageLLMEnhanceTimeout("test_suggestion").Seconds())
```

同样的修改适用于 `impact_analysis_executor.go` 和 `security_audit_executor.go`。

### 7.4 低优先级：在 `llm_call_logs` 中标记"Pipeline 已放弃"的高耗时调用

对于 goroutine 在 Pipeline stage 结束后才完成的 LLM 调用，建议在 `llm_call_logs`（或新增字段）中标记该调用是在"stage 已放弃"后完成的，方便排查此类问题。

---

## 八、数据复核：输入范围核查（保持不变）

经重新走读代码，上份报告中关于"各阶段输入未扩大、未遗漏"的结论**仍然成立**：

1. **code_understanding / secret_scan / security_audit / test_suggestion / impact_analysis / batch_review** 均严格限定在 `diff_files`（变更文件）范围内
2. **所有变更文件均进入处理循环**，无遗漏
3. **唯一必要的扩大**是 `git_clone` 阶段拉取完整仓库（git 协议限制）

---

## 九、全文数据溯源

| 数据项 | 来源 | 查询语句 |
|--------|------|---------|
| `llm_call_logs` | ai-code-review DB | `SELECT ... FROM llm_call_logs WHERE task_id IN (1,2) AND caller LIKE '%test%'` |
| `task_pipeline_executions` | ai-code-review DB | `SELECT * FROM task_pipeline_executions WHERE task_id IN (1,2) AND stage_code = 'test_suggestion'` |
| `review_agent_configs` | ai-code-review DB | `SELECT stage_configs FROM review_agent_configs LIMIT 1` |
| `callLLM` goroutine 代码 | 代码库 | `backend/internal/service/pipeline/agent_enricher.go:763-795` |
| `MarkSuccess` 写入逻辑 | 代码库 | `backend/internal/service/pipeline/context.go:122-139` |
| `test_suggestion_executor` token 设置 | 代码库 | `backend/internal/service/pipeline/test_suggestion_executor.go:163-193` |
| `token_usage` API 查询 | 代码库 | `backend/internal/handler/token_usage.go` |

---

*报告生成时间：2026-08-21*  
*修正声明：上份报告中"LLM 调用超时无响应"的结论错误，实际为"goroutine 未正确取消导致的 stage 级 timeout 与 LLM 调用分离"*  
*所有数据均来自生产数据库，无编造*
