# AI 代码评审 Pipeline 扩展设计方案

## 1. 概述

### 1.1 背景

CodeGuard 平台原有的 AI 代码评审采用单体模式：任务启动后直接调用大模型 API，一次性接收所有 diff 文件，生成 Markdown 格式的评审报告。该模式存在以下瓶颈：

- **Token 上限限制**：大模型 API 单次请求有 Token 上限（如 8K/16K/32K），大 MR（Merge Request）的 diff 总量动辄几十万字符，一次塞入会导致截断或接口拒绝。
- **纯 Markdown 输出**：旧模式无法输出结构化的维度评分、逐条 Issue、改进建议，前端评审报告页只能做简单文本渲染。
- **无法监控中间状态**：评审过程中没有任何节点可以观测，失败时只能看到最终结果「失败」，难以定位是哪个环节出了问题。
- **快照数据膨胀**：Pipeline 执行过程中的中间数据（如 AST 上下文、分批计划、批次评审结果）直接写入数据库文本字段，导致表膨胀，性能下降。

### 1.2 目标

引入 **Pipeline 执行引擎**，将 AI 评审拆解为可编排、可观测、可扩展的多个阶段。同时：

- **智能分批**：自动计算 token 开销并按批次/波次并行评审，突破单条请求 Token 上限。
- **结构化输出**：无论单批还是多批，始终通过 JSON Schema 约束 LLM 输出结构化结果（含各维度评分、Issue 列表、汇总评论），前端报告页基于结构化数据渲染。
- **AST 上下文增强**：对 Go/Java/Python/JS 文件提取函数、结构体、接口、调用链等 AST 信息，作为批次评审 Prompt 的一部分注入，提高评审质量。
- **对象存储卸载**：Pipeline 中间快照大于 32KB 时自动写入对象存储（COS / OSS / S3 / MinIO），数据库仅保存元数据。
- **Pipeline 可视化**：任务详情页以「卡片流水线」形式展示 9 个阶段的实时执行状态，点击卡片可查看输入/输出快照。
- **灰度发布**：通过系统开关 PipelineEnabled 控制新/旧模式切换，默认关闭，可逐项目灰度启用。

### 1.3 适用范围

- AI 代码评审任务（review 任务）
- 不适用 chat 任务（chat 保持原有对话逻辑）

---

## 2. 架构设计

### 2.1 总体架构

```text
                    GitLab Webhook
                         |
                         V
              ExecuteAIReviewTaskWithComment
                         |
            +------------+------------+
            |                         |
    PipelineEnabled=false     PipelineEnabled=true
            |                         |
            V                         V
    旧模式：                    新模式：
    runStructuredAIReview       Pipeline Engine
                                      |
                          +-----------+-----------+
                          |                       |
                    结构化评审引擎            对象存储
                    (Parse/Assemble)      (COS/OSS/S3/MinIO)
                          |
                          V
                    前端 Pipeline Tab
                    (卡片流水线可视化)
```

核心组件与职责：

| 组件 | 职责 |
|------|------|
| TaskService | Pipeline 入口，预加载数据、选择分支、后处理（评论/通知/阈值） |
| Pipeline Engine | 阶段编排、超时控制、错误传播、上下文共享 |
| Batch Algorithm | SmartSplitIntoBatches + GroupBatchesIntoWaves |
| AST Extractor | 解析变更文件结构，生成 prompt 注入文本 |
| Snapshot Manager | 自动路由 <=32KB 存 DB，>32KB 存对象存储 |
| Structured Review Engine | ParseReviewResult + PersistStructuredReview + AssembleMarkdownComment |
| Object Storage Service | S3-compatible REST 客户端（stdlib），支持 COS/OSS/S3/MinIO |

### 2.2 数据流

1. **Webhook 触发**：GitLab MR 创建/更新事件触发 Webhook Handler
2. **任务创建**：TaskService 创建 Task 记录，状态 pending
3. **分支判断**：读取 SystemConfig.PipelineEnabled
   - false → 旧模式：fetchMRDiffFiles -> runStructuredAIReview（单批/多批 fallback 到旧 runAIReview）
   - true → 新模式：继续以下步骤
4. **数据预加载**：
   - `fetchMRDiffFiles`：从 GitLab API 获取所有 diff 文件
   - `formatCommitsForReview`：格式化 commits 为文本
   - `projectTemplate`：读取项目绑定的评审模板 Prompt
5. **Diff 预截断**：按 SystemConfig.DiffTruncationThreshold（默认 5000 字符）截断每个 diff 文件，避免原始数据过大
6. **AST 上下文提取**：按文件扩展名投票决定语言，调用 go/parser（Go）或正则（Java/Python/JS）提取函数/结构体/接口/导入信息，格式化为文本
7. **Pipeline 执行**：`engine.ExecuteTask(taskID, inputs, nil)`
   - 按 DB 中 ReviewPipelineStage 表 9 个阶段顺序执行
   - batch_review_frame 阶段内部 wave 并行处理所有批次
8. **Pipeline 完成后**：再次调用 `runStructuredAIReview` 补全结构化数据
   - 多批时：截断 diff 列表，强制走单批结构化（JSON Schema 约束）
   - 调用 `ParseReviewResult` 解析 LLM 原始输出
   - 调用 `PersistStructuredReview` 保存结构化 JSON、维度评分、Issue 记录
9. **后处理**：
   - 生成 diff_files_json 元信息（前端展示用）
   - 保存 ReviewLog（审计日志）
   - 发布 GitLab 评论（Markdown 格式）
   - 阈值检查（score_value 超过阈值时触发告警）
   - 通知（企微即时通知 + 延迟通知队列）
   - 唤醒同项目 pending 任务
10. **任务完成**：状态变为 success/failed/timeout

---

## 3. Pipeline 引擎设计

### 3.1 阶段定义

ReviewPipelineStage 表存储阶段元数据（9 个阶段），启动时自动初始化（不存在则插入，已存在则更新）：

| Code | Name | SortOrder | IsGroup | TimeoutSec | Description |
|------|------|-----------|---------|------------|-------------|
| trigger_check | 触发条件评估 | 10 | false | 5 | 检查 MR 是否符合自动审查条件 |
| git_clone | 代码获取 | 20 | false | 60 | 从 Git 仓库获取变更 diff（数据已预加载，轻量级） |
| context_extract | 上下文提取 | 30 | false | 30 | Tree-sitter 解析变更文件结构 |
| risk_analysis | 风险预分析 | 40 | false | 10 | 基于变更复杂度决定审查策略 |
| batch_review_frame | AI 评审 | 50 | **true** | 600 | 结构化 AI 代码审查（含分批决策+并行评审批次+汇总） |
| batch_plan | 分批决策 | 51 | false | 5 | 子阶段：根据 diff 大小和 token 开销决定分批策略 |
| batch_review | 批次评审 | 52 | **true** | 120 | 子阶段：逐批次 LLM 代码审查（Wave 并行） |
| batch_summary | 汇总评审 | 53 | false | 120 | 子阶段：汇总各批次结果生成综合报告 |
| post_process | 后处理 | 60 | false | 30 | 发布评论、发送通知、阈值检查 |

注：IsGroup=true 的阶段在执行器中自行创建子 TaskPipelineExecution 记录。batch_review_frame 包含 batch_plan、batch_review、batch_summary 三个子阶段。

### 3.2 执行器接口与注册

```go
// StageExecutor 阶段执行器接口
type StageExecutor interface {
    Code() string                      // 返回阶段 code，与 ReviewPipelineStage.Code 匹配
    Execute(ctx StageContext) error   // 执行业务逻辑
}
```

Engine 构造函数注册所有执行器：

```go
func NewEngine(db *gorm.DB, llm LLMService) *Engine {
    e := &Engine{db: db, executors: make(map[string]StageExecutor)}
    e.Register(&TriggerCheckExecutor{})
    e.Register(&GitCloneExecutor{})
    e.Register(&ContextExtractExecutor{})
    e.Register(&RiskAnalysisExecutor{})
    e.Register(NewBatchReviewFrameExecutor(llm))
    e.Register(&PostProcessExecutor{})
    return e
}
```

### 3.3 超时控制

每个阶段有独立超时（ReviewPipelineStage.TimeoutSec），默认 300 秒：

```go
func (e *Engine) executeWithTimeout(ctx StageContext, exec StageExecutor, timeoutSec int) error {
    c, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
    defer cancel()
    
    done := make(chan error, 1)
    go func() { done <- exec.Execute(ctx) }()
    
    select {
    case err := <-done:
        return err
    case <-c.Done():
        return fmt.Errorf("阶段执行超时 (%ds)", timeoutSec)
    }
}
```

### 3.4 关键执行器详解

#### 3.4.1 TriggerCheckExecutor

- 检查 MR.Merged 状态，已合并的 MR 不再做 AI 评审
- 检查项目配置是否启用 AI 评审
- 若未通过，阶段标记为 skipped

#### 3.4.2 GitCloneExecutor

- diff 文件数据已在 `ExecuteTask` 调用前预加载（通过 inputs map）
- 本阶段仅做数据校验和轻量标记

#### 3.4.3 ContextExtractExecutor

- 从 inputs 中读取 diff_files
- 调用 `detectLanguage()` 投票决定语言
- 调用 `NewASTExtractor(lang).Extract(fileMaps).Format()` 生成 ASTContext
- AST 上下文存入 StageContext，后续批次评审通过 `getASTContextText()` 注入 prompt

#### 3.4.4 RiskAnalysisExecutor

- 分析 diff 文件数量和变更复杂度
- 决定是否需要启用分批策略（目前由 batch_plan 自动判断）

#### 3.4.5 BatchReviewFrameExecutor（核心评审阶段）

这是最复杂的阶段，内部分为 3 个子阶段：

**S1: 分批决策（executeBatchPlan）**

核心算法 `SmartSplitIntoBatches`：

```go
// 输入：diff 文件列表、每批最大 token、overhead token
// 输出：Batch 列表（每个 Batch 包含文件列表和 token 估算）
func SmartSplitIntoBatches(files []map[string]interface{}, maxTokens, overheadTokens int) []Batch {
    availableTokens := maxTokens - overheadTokens  // 扣除 overhead 后的可用额度
    
    // 单文件 diff 超过 availableTokens 时截断（map copy 避免副作用）
    // 按顺序累加文件，超过额度时开新 batch
    // 返回 Batch 列表
}
```

关键设计：
- **overhead 计算**：prompt 模板指令 + commits_text + ast_context + format 格式说明 + 10% buffer
- **单文件截断**：当单个文件 diff 超过 availableTokens 时，做 map copy 截断，保留前缀并标记 truncated=true
- **可用额度**：`availableTokens = maxTokensPerBatch - overheadTokens`
- overhead 由 `BuildBatchContext(ctx)` 计算

**S2: Wave 并行批处理（executeBatch / executeSingleBatch）**

```go
// 输入：总批次数、最大并行度（SystemConfig.BatchParallelMax，默认 3）
// 输出：wave 列表，每个 wave 包含一批 batch index
func GroupBatchesIntoWaves(totalBatches, maxParallel int) [][]int
```

执行逻辑：
1. 遍历每个 wave
2. wave 内启动 `sync.WaitGroup`，每个 batch 一个 goroutine
3. 调用 `llmService.ChatCompletion()` 进行批次评审
4. 任何一批失败 → 错误写入 `errCh`，整个 wave 失败
5. 成功后 `atomic.AddInt32` 更新父阶段 `batch_completed`
6. 所有 wave 完成后进入 S3

**S3: 强制汇总（executeSummary）**

- 收集所有批次的评审子报告
- 构建汇总 Prompt，要求 LLM 基于所有批次结果输出综合评审报告
- 强制包含评分：`AI评分：xx分`
- 不支持跳过（batch_summary 是强制阶段）

### 3.5 任务状态机与并发安全

```text
                    pending
                      |
                      V ExecuteTask 开始
                    running
                      |
        +-------------+-------------+
        |             |             |
        V             V             V
     success       failed       timeout
```

状态更新使用**条件更新**，防止 TimeoutChecker 与 Pipeline 并发覆盖：

```go
e.db.Model(&model.Task{}).Where("id = ? AND status = ?", taskID, model.TaskRunning).Updates(map[string]interface{}{
    "status": model.TaskSuccess,
    ...
})
```

---

## 4. 智能分批算法

### 4.1 算法流程

```text
输入：diff 文件列表 F，每批最大 token M，overhead token O
输出：Batch 列表 B

1. available = M - O
2. 如果 available <= 0：available = M / 2（保底）
3. for each file f in F:
   a. diffLen = len(f.diff)
   b. 如果 diffLen > available:
      - 创建 f 的副本 f'
      - f'.diff = f.diff[:available] + "\n...（已截断）"
      - f'.truncated = true
      - f = f'
   c. 如果 currentBatchChars + diffLen > available 且 currentBatch 非空:
      - 将 currentBatch 推入 B
      - 新建 currentBatch
   d. currentBatch = append(currentBatch, f)
   e. currentBatchChars += diffLen
4. 将最后一个 currentBatch 推入 B
```

### 4.2 Wave 分组算法

```text
输入：总批次数 N，最大并行度 P
输出：wave 列表 W

1. W = []
2. current = []
3. for i in 0..N-1:
   a. current = append(current, i)
   b. 如果 len(current) >= P:
      - W = append(W, current)
      - current = []
4. 如果 len(current) > 0：W = append(W, current)
```

Wave 执行特性：
- 单 wave 内所有 batch 并发执行
- 任一 batch 失败 → 整个 wave 失败
- 父阶段 `batch_completed` 通过 `atomic.AddInt32` 更新，前端可感知进度
- 所有 wave 完成后才能进入 batch_summary

---

## 5. AST 上下文提取

### 5.1 语言检测

```go
// 按文件扩展名投票决定
.go      -> Go
.java    -> Java
.py      -> Python
.js/.jsx/.ts/.tsx -> JavaScript
```

取数量最多的语言作为当前 MR 的主语言。

### 5.2 Go AST 提取（标准库，零外部依赖）

```go
// 使用 go/parser + go/ast
func extractGoAST(fileMap) *ASTContext {
    // 1. 从 diff 中提取以 + 开头的代码行（新增/修改行）
    // 2. parser.ParseFile 解析语法树
    // 3. ast.Inspect 遍历节点：
    //    - FuncDecl -> 函数名列表
    //    - TypeSpec -> 结构体名列表
    //    - ImportSpec -> 导入包列表
    //    - InterfaceType -> 接口名列表
    // 4. 返回 ASTContext{Functions, Structs, Imports, Interfaces}
}
```

### 5.3 其他语言正则 Fallback

```go
// Java:   class X { ... }    |   public void func() { ... }    |   interface X { ... }
// Python: def func():         |   class X:
// JS:     function X()        |   const X = () =>               |   class X { ... }
```

### 5.4 Prompt 注入格式

```text
## 代码上下文（AST 分析）
- 文件：src/service/user.go
- 函数：CreateUser, ValidateUser
- 结构体：UserRequest, UserResponse
- 接口：UserService
- 导入：context, database/sql
```

每个 batch review prompt 都会通过 `getASTContextText()` 注入上述内容。

---

## 6. 对象存储设计

### 6.1 数据模型

```go
type ObjectStorageConfig struct {
    ID            uint        // 主键
    Name          string      // 配置名称（如：生产环境 COS）
    Type          string      // cos / oss / s3 / minio
    Bucket        string      // bucket 名称
    Region        string      // region（COS/OSS 必填）
    Endpoint      string      // 自定义 endpoint（MinIO 必填）
    UseSSL        bool        // https（默认 true）或 http
    Prefix        string      // 对象前缀（默认 codeguard）
    AccessKey     string      // 加密存储，不返回前端
    SecretKey     string      // 加密存储，不返回前端
    AccessKeyMask string      // AK 掩码（展示用）
    SecretKeyMask string      // SK 掩码
    IsDefault     bool        // 是否为默认配置
    Enabled       bool        // 是否启用
    ConnectionStatus string   // ok / error / unknown
    RetentionDays int         // 保留天数（默认 30）
    MaxObjectSize int64       // 单对象上限（默认 50MB）
    LastError     string      // 上次错误信息
    LastCheckedAt *time.Time  // 上次检查时间
}
```

### 6.2 支持的后端

| 类型 | Endpoint 构建 | 说明 |
|------|---------------|------|
| COS | bucket.cos.region.myqcloud.com | Region 必填 |
| OSS | bucket.oss-region.aliyuncs.com | Region 必填 |
| S3  | bucket.s3.region.amazonaws.com | 默认 us-east-1 |
| MinIO | scheme://endpoint | Endpoint 必填，端口默认 9000 |

### 6.3 加密方案

AK/SK 使用 AES-GCM 加密存储：

```go
func EncryptString(plaintext string) (string, error) {
    key := []byte(os.Getenv("CODEGUARD_ENCRYPTION_KEY"))
    block, _ := aes.NewCipher(sha256.Sum256(key)[:])
    gcm, _ := cipher.NewGCM(block)
    nonce := make([]byte, gcm.NonceSize())
    io.ReadFull(rand.Reader, nonce)
    ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
    return base64.StdEncoding.EncodeToString(ciphertext), nil
}
```

### 6.4 配置变更即时生效

四个操作触发 `InitObjectStorageProvider()` 重载：

| 操作 | 触发条件 |
|------|----------|
| CreateConfig | 新建配置且 IsDefault=true |
| UpdateConfig | 更新的是当前默认配置 |
| DeleteConfig | 删除的是当前默认配置 |
| SetDefault | 切换默认配置 |

### 6.5 Snapshot 存储路由

```go
func (m *SnapshotManager) SaveSnapshot(taskID, executionID uint, data []byte) error {
    if len(data) <= 32*1024 {
        // <=32KB：直接存入数据库
        return m.saveToDB(taskID, executionID, data)
    }
    // >32KB：存入对象存储
    key := fmt.Sprintf("snapshots/%d/%d.json", taskID, executionID)
    if p := GetObjectStorageProvider(); p != nil {
        _, err := p.Put(ctx, key, data)
        if err == nil {
            return m.saveRefToDB(taskID, executionID, key)  // DB 只存引用
        }
    }
    // fallback：存 DB
    return m.saveToDB(taskID, executionID, data)
}
```

---

## 7. 前端 Pipeline 可视化

### 7.1 Tab 顺序

| 顺序 | Tab | 说明 |
|------|-----|------|
| 1 | **Pipeline** | AI 评审执行链路可视化（review 任务默认 active） |
| 2 | 代码变更 | Diff 文件浏览 |
| 3 | 评审报告 | 结构化评审报告（基于 ai_response_json） |
| 4 | AI Response | 原始 LLM 输出 |
| 5 | Token 用量 | LLM 调用明细 |

### 7.2 卡片流水线布局

横向卡片流（overflow-x-auto 可横向滚动）：

```text
[触发检查] -> [代码获取] -> [上下文提取] -> [风险预分析] -> [AI评审] -> [分批决策] -> [批处理] -> [汇总] -> [后处理]
    2s           1s             1s              0s            120s          0s           45s         5s         1s
                                       ● 2/3 [批次进度条]
                                       ● ● ● [子阶段圆点]
```

卡片元素：
- **图标**：根据状态展示（时钟/旋转/对勾/叉号/沙漏）
- **阶段名**：如「AI 评审」
- **耗时**：`(45s)` 或 `-`
- **批次进度**：微型进度条 + `2/3`
- **子阶段点**：AI 评审卡片下方显示 3 个小圆点（绿/蓝/红）
- **运行中标记**：蓝色脉冲动画 + 右上角蓝色圆点
- **连线**：`fa-chevron-right` 箭头连接各卡片

### 7.3 详情面板

点击任意卡片后，下方展开详情面板：

```text
+-------------------------------------------------------------+
| 分批决策 详情                                        [关闭]  |
+-------------------------------------------------------------+
| [成功] [耗时 0s] [批次 2/3]                                |
|                                                             |
| 输入快照                                                    |
| +--------------------------------------------------------+ |
| | {                                                      | |
| |   "plan": { "batch_count": 3, "strategy": "multi" }     | |
| |   "total_files": 15                                    | |
| | }                                                      | |
| +--------------------------------------------------------+ |
|                                                             |
| 输出快照                                                    |
| +--------------------------------------------------------+ |
| | {                                                      | |
| |   "final_report": "## 评审报告\n..."                    | |
| |   "score": 85                                          | |
| | }                                                      | |
| +--------------------------------------------------------+ |
+-------------------------------------------------------------+
```

### 7.4 实时更新

- **SSE**：`EventSource` 连接 `/api/v1/tasks/:id/pipeline/events`
  - 服务器每 2 秒推送一次全量 stages 更新
  - `pipeline_completed` 事件后主动关闭连接
- **定时轮询**：SSE 失败时，5 秒轮询兜底
- **指数退避重连**：断线后 1s → 2s → 4s → ... → 32s

---

## 8. 灰度发布策略

### 8.1 配置项

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| PipelineEnabled | bool | false | 全局开关：是否启用 Pipeline 模式 |
| BatchParallelMax | int | 3 | 每 wave 最大并行批次（1-10） |
| DiffTruncationThreshold | int | 5000 | 单文件 diff 截断阈值（字符数） |

### 8.2 灰度控制

- 当前实现：全局开关，PipelineEnabled=true 时所有 review 任务走 Pipeline
- Legacy 任务：无 TaskPipelineExecution 记录，前端展示「旧模式下执行，无链路数据」
- 后续可扩展：项目级灰度开关（ProjectConfig.pipeline_enabled）

---

## 9. 关键接口

### 9.1 Pipeline 状态查询

```http
GET /api/v1/tasks/{id}/pipeline
```

返回：task_id, legacy_mode, overall_status, current_stage, duration_sec, stages[]

### 9.2 Pipeline SSE 事件流

```http
GET /api/v1/tasks/{id}/pipeline/events
```

推送：{type: "pipeline_update" | "pipeline_completed", task_id, stages[]}

### 9.3 阶段详情

```http
GET /api/v1/pipeline-stages/{execution_id}/detail
```

返回：execution_id, stage_code, status, started_at, completed_at, duration_ms, input_tokens, output_tokens, model_id, error_message, batch_index, batch_total, batch_completed, input_snapshot, output_snapshot

### 9.4 对象存储配置

```http
GET    /api/v1/object-storage/configs                # 列表
POST   /api/v1/object-storage/configs                # 创建
PUT    /api/v1/object-storage/configs/{id}           # 更新
DELETE /api/v1/object-storage/configs/{id}           # 删除
POST   /api/v1/object-storage/configs/{id}/test      # 测试连接
POST   /api/v1/object-storage/configs/{id}/set-default  # 设为默认
```

---

## 10. 部署与运维

### 10.1 环境变量

| 变量名 | 说明 | 示例 |
|--------|------|------|
| CODEGUARD_ENCRYPTION_KEY | AES-GCM 加密密钥 | `my-secret-key-32chars-long!` |

### 10.2 数据库迁移

启动时自动执行 `initPipelineStages()`：
- 自动创建/更新 ReviewPipelineStage 表 9 条记录
- 兼容旧数据：如果 execution 中 parent_id 字段导致查不到顶层阶段，自动回退查询全部

### 10.3 MinIO 特殊配置

- 默认 Region：`us-east-1`（AWS S3 V4 签名兼容性要求）
- 默认端口：如果 endpoint 不含 `:`，自动追加 `:9000`
- 路径风格 URL：`http://host:9000/bucket/prefix/key`
- UseSSL 切换：前端下拉框选择 http/https

---

## 附录：文件对应关系

| 功能 | 后端文件 | 前端文件 |
|------|----------|----------|
| Pipeline 引擎 | `backend/internal/service/pipeline/engine.go` | - |
| 分批算法 | `backend/internal/service/pipeline/batch_algorithm.go` | - |
| 阶段执行器 | `backend/internal/service/pipeline/executors.go` | - |
| AST 提取 | `backend/internal/service/pipeline/ast_extractor.go` | - |
| Snapshot 管理 | `backend/internal/service/pipeline/snapshot.go` | - |
| Pipeline 接口定义 | `backend/internal/service/pipeline/interfaces.go` | - |
| Pipeline Handler | `backend/internal/handler/pipeline.go` | - |
| 对象存储 Service | `backend/internal/service/object_storage.go` | - |
| 对象存储 Handler | `backend/internal/handler/object_storage.go` | - |
| 加密工具 | `backend/internal/service/crypto.go` | - |
| 结构化评审引擎 | `backend/internal/engine/*.go` | - |
| 任务 Service（Pipeline 分支） | `backend/internal/service/task.go` | - |
| DB 初始化 | `backend/internal/model/db.go` | - |
| Pipeline Tab 可视化 | - | `prototype/task-detail.html` |
| 对象存储配置页面 | - | `prototype/storage-config.html` |
| 系统配置页面（灰度开关） | - | `prototype/settings.html` |
