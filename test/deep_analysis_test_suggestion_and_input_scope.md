# 深度分析报告：测试建议阶段固定耗时 & 输入范围核查

## 一、任务背景

基于用户指令，对预发布环境中两个评审任务（任务1：前端项目 chip-ui，任务2：Java 项目 chip-core）进行以下深度分析：

1. **走读 `test_suggestion`（测试建议）阶段代码**，分析其固定耗时约 5 分钟（300 秒）的根因，结合智能体配置的超时时间。
2. **逐项核查每个阶段的输入范围**，检查是否存在遗漏（应输入的未输入）或扩大（不应输入的被输入）。

**数据来源**：
- 代码库：`/data/ai-bug-fix/backend/internal/service/pipeline/`
- 数据库：`ai-code-review` @ `10.210.20.40`
- 智能体配置表：`review_agent_configs`
- 流水线执行记录：`task_pipeline_executions`

---

## 二、问题1：test_suggestion 阶段固定耗时 300 秒（5分钟）的代码级根因分析

### 2.1 现象

| 任务 | 变更文件数 | test_suggestions 产出数 | 实际耗时 | output_tokens |
|------|----------|----------------------|---------|---------------|
| 任务1 (chip-ui) | 5 | 84 条 | **300,033 ms** | 0 |
| 任务2 (chip-core) | 48 | 1 条 | **300,063 ms** | 0 |

**核心异常**：
- 无论产出数量多少（84 条 vs 1 条），耗时都**精确锁定在 300 秒左右**
- `output_tokens = 0`，说明 **LLM 未成功返回任何内容**
- stage 状态为 `success`（说明降级机制生效）

### 2.2 代码走读：超时参数的三层结构

系统中存在**三层超时控制**，它们共同决定了 `test_suggestion` 的实际行为：

#### 第一层：Pipeline Stage 级超时（外层 guard）

文件：`backend/internal/service/pipeline/engine.go`

```go
// 第 253-261 行
timeoutSec := stageDef.TimeoutSec          // 数据库 review_pipeline_stages 中的默认值
if cfg := agentCfg.StageConfig(stageDef.Code); cfg != nil {
    if t, ok := cfg["timeout"].(float64); ok && t > 0 {
        timeoutSec = int(t)               // 允许 stage_configs 覆盖
    }
}
if timeoutSec <= 0 {
    timeoutSec = 300
}
err := e.executeWithTimeout(stageCtx, execImpl, timeoutSec)
```

- `stageDef.TimeoutSec`（`db.go` 硬编码）：`test_suggestion = 30` 秒
- `agentCfg.StageConfig("test_suggestion")["timeout"]`（数据库实际配置）：**510 秒**
- **结论**：外层 stage timeout = **510 秒**

#### 第二层：Executor 内部 LLM 调用超时（实际生效）

文件：`backend/internal/service/pipeline/test_suggestion_executor.go`

```go
// 第 46-47 行
llmMaxTokens := 3000
llmTimeoutSec := 300   // ← 【硬编码默认值：300 秒 = 5 分钟】

// 第 52-53 行：尝试从配置读取
if cfg != nil {
    llmTimeoutSec = cfg.GetStageParam("test_suggestion", "llm_timeout_sec", llmTimeoutSec)
    // 参数名："llm_timeout_sec"
}
```

这里 `llmTimeoutSec` 默认值是 **300 秒**，然后尝试从 `stage_configs` 读取 `"llm_timeout_sec"` 参数覆盖。

#### 第三层：AgentEnricher.callLLM 超时（最内层）

文件：`backend/internal/service/pipeline/agent_enricher.go`

```go
// 第 456-461 行（EnricherConfig 默认值）
func NewAgentEnricher(svc LLMService, cfg EnricherConfig) *AgentEnricher {
    if cfg.EnrichTimeout <= 0 {
        cfg.EnrichTimeout = 300 * time.Second   // ← 【默认 300 秒】
    }
    ...
}

// 第 763-771 行（callLLM 实现）
func (e *AgentEnricher) callLLM(...) (*StructuredChatResult, error) {
    c, cancel := context.WithTimeout(ctx, e.enrichTimeout)  // ← 使用 300 秒
    defer cancel()
    ...
}
```

### 2.3 关键发现：配置参数名不匹配导致配置失效

**数据库实际配置**（`review_agent_configs.stage_configs`）：

```json
{
  "test_suggestion": {
    "timeout": 510,
    "llm_enhance_timeout": 500        // ← 注意参数名
  }
}
```

**代码读取的参数名**：
- `test_suggestion_executor.go` 第 53 行：`"llm_timeout_sec"`
- `review_agent_config_service.go` 第 75 行：为扩展智能体补充 `llm_enhance_timeout: 300`

**问题**：
- 代码读取 `llm_timeout_sec`（不存在于配置中）→ 回退到硬编码 **300 秒**
- 配置写入的是 `llm_enhance_timeout: 500` → **从未被读取**

这是典型的**参数命名不一致 bug**。

### 2.4 超时触发链路

```
Pipeline Stage timeout = 510 秒（外层）
    │
    ▼
TestSuggestionExecutor.Execute()
    │
    ├── 启发式分析（耗时 < 1 秒）
    │
    ├── EnrichTestSuggestions()
    │   │
    │   ├── callLLM() with timeout = 300 秒
    │   │   │
    │   │   ├── context.WithTimeout(ctx, 300s) ← 实际生效
    │   │   │   │
    │   │   │   ├── LLM 调用发送到模型服务
    │   │   │   │   │
    │   │   │   │   ├── 模型响应极慢 / 不可用
    │   │   │   │   │
    │   │   │   │   └── 300 秒后 context 超时
    │   │   │   │
    │   │   │   └── 返回 "AgentEnricher LLM 调用超时 (5m0s)"
    │   │   │
    │   │   └── 无结果返回，input/output_tokens = 0
    │   │
    │   └── 降级为空结果
    │
    └── 启发式结果作为最终产出
```

### 2.5 结论

| 因素 | 值 | 是否触发 |
|------|---|---------|
| Stage 级 timeout | 510 秒 | ❌ 未触发（内部 300 秒先超时） |
| Executor `llmTimeoutSec` 硬编码 | 300 秒 | ✅ **实际生效** |
| `EnrichTimeout` 硬编码 | 300 秒 | ✅ **实际生效** |
| 配置 `llm_enhance_timeout` | 500 秒 | ❌ **从未被读取（参数名不匹配）** |

**根本原因**：
1. `llmTimeoutSec` 和 `EnrichTimeout` 的**硬编码默认值为 300 秒**
2. 数据库配置的 `llm_enhance_timeout = 500` 因**参数名不匹配**从未生效
3. **LLM 实际调用超时**（无论 suggestions 数量），降级为启发式结果
4. 两个任务耗时几乎相同（300,033 ms vs 300,063 ms），因为都是**等待 LLM 超时后降级**

---

## 三、问题1扩展：review_arbitration 阶段耗时长（任务1: 321秒，任务2: 595秒）

### 3.1 发现

数据库配置：
```json
{
  "review_arbitration": {
    "timeout": 1600   // ← 约 26.7 分钟
  }
}
```

代码层面（`review_arbitration_executor.go`）：
- `callLLMWithStructuredPrompt` **没有设置独立的 LLM 子超时**
- 直接使用传入的 `ctx`，其超时 = stage timeout = **1600 秒**
- 如果 LLM 服务响应慢，会等待很长时间

### 3.2 与 test_suggestion 的对比

| 维度 | test_suggestion | review_arbitration |
|------|----------------|-------------------|
| Stage timeout | 510 秒 | 1600 秒 |
| LLM 子超时 | **300 秒（硬编码）** | **无（使用 stage timeout）** |
| 实际耗时 | ≈ 300 秒（固定） | 321 ~ 595 秒（可变） |
| 超时行为 | 固定超时 → 降级 | 取决于模型响应速度 |
| output_tokens | 0 | 0（表中未记录） |

**为什么 review_arbitration 没有也固定为某个值？**
- 因为 review_arbitration 没有硬编码的 LLM 子超时，完全依赖模型实际响应速度
- 任务1（5 个文件，prompt 较小 → 321 秒）
- 任务2（48 个文件，prompt 较大 → 595 秒）

---

## 四、问题2：各阶段输入范围逐项核查

### 4.1 核查方法

对每个阶段，核查：
1. **输入来源**：从哪个 context 字段读取文件/数据
2. **范围界定**：是否严格限定在变更文件（`diff_files`）内
3. **基线/引用数据**：是否引用了非变更文件的内容（如果是，是否合理）

### 4.2 逐项核查结果

#### Stage 1: trigger_check（触发条件评估）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `task` 对象（MR 元数据）、`project` 配置 |
| 范围 | webhook payload / 数据库记录 |
| 是否扩大 | ❌ 否，仅使用任务元数据 |
| 是否遗漏 | ❌ 否 |

#### Stage 2: git_clone（代码克隆）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `project_path`（仓库 URL）、`branch`、`access_token` |
| 范围 | 拉取**完整仓库**到本地 workspace |
| 是否扩大 | ⚠️ 是（完整仓库），但**属于必要性扩大**（git 协议不支持只拉取变更文件） |
| 是否遗漏 | ❌ 否 |

#### Stage 3: code_understanding（代码理解器）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `diff_files`（`ctx.GetInput("diff_files")`） |
| 范围 | **仅变更文件**（`contextExtract` 函数遍历 `files` 列表） |
| 基线数据 | `graphStorage.Exists(projectID)` → 加载项目基线符号图（增量 overlay） |
| 是否扩大 | ❌ 否（AST 解析只针对变更文件；基线图谱加载是增量分析的标准设计） |
| 是否遗漏 | ❌ 否（所有变更文件均进入循环） |

**关键代码**（`code_understanding_executor.go:374-418`）：
```go
func (e *CodeUnderstandingExecutor) contextExtract(...) ([]*parser.UnifiedAST, int, error) {
    for _, f := range files {   // ← files 即 diff_files
        path, _ := f["path"].(string)
        content := fullFileMap[path]
        if content == "" && repoDir != "" {
            contentBytes, err := os.ReadFile(filepath.Join(repoDir, path))
            ...
        }
        // 仅解析变更文件
        ast, err := extractor.ParseFile(path, []byte(content))
        ...
    }
}
```

#### Stage 4: symbol_graph（符号图，code_understanding 的子阶段）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `asts`（上一步解析的变更文件 AST）+ `graphStorage` 基线图谱 |
| 范围 | `changedFileSet` 只包含变更文件路径 |
| 是否扩大 | ❌ 否（`BuildFromASTs` 接收 `changedFileSet`，用于增量标记） |
| 是否遗漏 | ❌ 否 |

**关键代码**（`code_understanding_executor.go:250-255`）：
```go
changedFileSet := make(map[string]bool)
for _, ast := range asts {
    changedFileSet[ast.FilePath] = true
}
graphResult, err = sg.BuildFromASTs(projectID, asts, activeAdapters, changedFileSet)
```

#### Stage 5: dependency_scan（依赖漏洞扫描）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `diff_files_raw` → fallback `diff_files` |
| 范围 | **仅变更文件中的依赖清单**（`go.mod`、`package.json`、`pom.xml` 等） |
| 过滤逻辑 | `ParseDependenciesFromFile(path, content)` 只解析依赖文件 |
| 生态过滤 | `effectiveEcosystems` 按项目语言过滤（Go/Maven/npm 等） |
| 是否扩大 | ❌ 否（只扫描变更的依赖文件） |
| 是否遗漏 | ❌ 否 |

#### Stage 6: secret_scan（密钥泄露扫描）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `diff_files` |
| 范围 | **仅变更文件的 diff 内容** |
| 是否扩大 | ❌ 否 |
| 是否遗漏 | ❌ 否 |

#### Stage 7: security_audit（安全审计）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `diff_files` + `code_understanding_structured`（污点路径） |
| 范围 | **仅变更文件**；污点路径过滤只保留与审计文件相关的 |
| 是否扩大 | ❌ 否（`extractRelevantTaintFlows` 按文件过滤） |
| 是否遗漏 | ❌ 否 |

#### Stage 8: test_suggestion（测试建议）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `ast_structured`（code_understanding 产出的 ASTContext） |
| 范围 | **仅变更文件中的函数**（`isInChangedFiles` 过滤） |
| 扩展引用 | `findTestCallersForFunction` 通过 cross_file_call_chain 查找测试调用者（非变更文件） |
| 是否扩大 | ⚠️ 引用非变更文件中的测试调用者信息，但**属于合理扩展**（需知已有测试覆盖） |
| 是否遗漏 | ❌ 否（所有变更文件的函数均进入分析） |

**关键代码**（`test_suggestion_executor.go:79-83`）：
```go
for _, fn := range astStructured.Functions {
    if !isInChangedFiles(fn.FilePath, changedFiles) {   // ← 严格过滤
        continue
    }
    scenarios := analyzeFunctionForTests(fn)
    ...
}
```

#### Stage 9: impact_analysis（影响分析）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `cross_file_call_chain` + `ast_context` + `diff_files` |
| 范围 | **仅变更文件**（`changedFileSet` 标记） |
| 基线数据 | `getBaselineFileContent` 通过 `git show HEAD:path` 获取基线版本的变更文件内容 |
| 是否扩大 | ❌ 否（基线对比只针对变更文件） |
| 是否遗漏 | ❌ 否 |

#### Stage 10: batch_review_frame / batch_review（AI 评审）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `diff_files` |
| 范围 | **仅变更文件**，分批处理 |
| code_understanding 注入 | 第 1276-1290 行：**按批次文件过滤**，每批只注入当前批次相关的 AST/图谱摘要 |
| 是否扩大 | ❌ 否 |
| 是否遗漏 | ❌ 否 |

**关键代码**（`executors.go:1276-1290`）：
```go
// 按批次文件过滤 code_understanding 内容，每批仅注入当前批次相关的 AST/图谱摘要
if codeReport, ok := ctx.GetOutput("code_understanding_structured").(*CodeUnderstandingReport); ok && codeReport != nil {
    batchMarkdown := filterCodeUnderstandingMarkdownByFiles(fullMarkdown, detail.FilePaths)
    batchAgentMarkdowns["code_understanding"] = batchMarkdown
}
```

#### Stage 11: review_arbitration（评审裁决）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `batch_review_results` + `agent findings` + `code_understanding_structured` |
| code_understanding 过滤 | **严格过滤**：只提取变更文件相关的图谱节点（max 100 个）和关系（max 100 条） |
| 是否扩大 | ❌ 否（`changedSet` 过滤 + maxArbNodes/maxArbRels 限制） |
| 是否遗漏 | ❌ 否 |

**关键代码**（`review_arbitration_executor.go:91-148`）：
```go
changedSet := make(map[string]bool)
for _, f := range codeReport.FileList {
    changedSet[f] = true
}
nodeCount := 0
const maxArbNodes = 100
for _, node := range g.Nodes {
    if file, _ := node["file"].(string); file != "" && changedSet[file] {
        symbolGraphData.Nodes = append(symbolGraphData.Nodes, sgn)
        nodeCount++
        if nodeCount >= maxArbNodes { break }
    }
}
```

#### Stage 12: post_process（后处理）

| 检查项 | 结果 |
|--------|------|
| 输入来源 | `ai_review_result`、`batch_review_results` 等上游产出 |
| 范围 | 上游已过滤的数据 |
| 是否扩大 | ❌ 否 |
| 是否遗漏 | ❌ 否 |

### 4.3 输入范围核查总结

| 阶段 | 输入范围 | 是否扩大 | 是否遗漏 | 备注 |
|------|---------|---------|---------|------|
| trigger_check | 任务元数据 | ❌ | ❌ | — |
| git_clone | 完整仓库 | ⚠️ 必要 | ❌ | git 协议限制 |
| code_understanding | 变更文件 | ❌ | ❌ | 仅 diff_files |
| symbol_graph | 变更文件 + 基线图谱增量 | ❌ | ❌ | 增量分析设计 |
| dependency_scan | 变更的依赖清单 | ❌ | ❌ | — |
| secret_scan | 变更文件 | ❌ | ❌ | — |
| security_audit | 变更文件 | ❌ | ❌ | — |
| test_suggestion | 变更文件函数 | ⚠️ 合理 | ❌ | 引用测试调用者 |
| impact_analysis | 变更文件 | ❌ | ❌ | 基线对比 |
| batch_review | 变更文件（分批） | ❌ | ❌ | code_understanding 按批过滤 |
| review_arbitration | 过滤后的结果 | ❌ | ❌ | max 100 节点/关系 |
| post_process | 上游产出 | ❌ | ❌ | — |

**结论**：
1. **无扩大输入范围的情况**：所有核心分析阶段（AST 解析、安全审计、测试建议、影响分析、AI 评审、裁决）都严格限制在 `diff_files`（变更文件）范围内。
2. **唯一必要扩大是 `git_clone`**：必须拉取完整仓库才能进行 diff 和基线对比。
3. **两个合理引用非变更数据的情况**：
   - `symbol_graph` 加载项目基线图谱（增量分析的标准做法）
   - `test_suggestion` 引用跨文件调用链中的测试调用者（判断已有测试覆盖）
4. **无遗漏**：`diff_files` 没有被任何阶段显式过滤排除。

---

## 五、问题修正建议

### 5.1 急需修复：统一 LLM 超时参数名

**问题**：`llm_enhance_timeout`（配置）与 `llm_timeout_sec`（代码）参数名不匹配。

**方案 A（推荐）**：将代码中的参数名统一为 `llm_enhance_timeout`。

修改 `test_suggestion_executor.go` 第 53 行：
```go
// 修改前
llmTimeoutSec = cfg.GetStageParam("test_suggestion", "llm_timeout_sec", llmTimeoutSec)

// 修改后
llmTimeoutSec = cfg.GetStageParam("test_suggestion", "llm_enhance_timeout", llmTimeoutSec)
```

同理检查 `impact_analysis_executor.go` 第 395 行是否也需要同步修改。

**方案 B**：调整数据库配置的参数名，但会影响前端配置界面和其他地方。

### 5.2 建议优化：为 review_arbitration 增加 LLM 子超时

`review_arbitration` 目前完全依赖 stage timeout（1600 秒），没有独立 LLM 子超时，导致模型响应慢时长时间阻塞。

建议在 `callLLMWithStructuredPrompt` 中增加独立的 LLM 调用超时（如 120 秒），超时时降级到 Layer 2 本地去重。

### 5.3 建议调优：降低不合理的 stage timeout 配置

当前数据库配置的部分 stage timeout 过长：

| Stage | 配置 timeout | 建议值 | 原因 |
|-------|-------------|--------|------|
| test_suggestion | 510 秒 | 60~120 秒 | LLM 增强不应超过 2 分钟 |
| impact_analysis | 510 秒 | 120~180 秒 | 含基线对比可稍长，但 8.5 分钟不合理 |
| security_audit | 510 秒 | 120~180 秒 | 同上 |
| review_arbitration | 1600 秒 | 300~600 秒 | 26.7 分钟明显过长 |
| secret_scan | 500 秒 | 120~180 秒 | 密钥扫描通常很快 |

---

## 六、附录：关键配置数据快照

### 6.1 数据库 `review_agent_configs` 配置

```json
{
  "test_suggestion":    {"timeout": 510, "llm_enhance_timeout": 500},
  "impact_analysis":    {"timeout": 510, "llm_enhance_timeout": 500},
  "security_audit":     {"timeout": 510, "llm_enhance_timeout": 500},
  "secret_scan":        {"timeout": 500, "llm_enhance_timeout": 300},
  "review_arbitration": {"timeout": 1600},
  "batch_review_frame": {"timeout": 3000, "parallel_max": 3},
  "code_understanding": {"timeout": 100, "context_extract_timeout": 30, "symbol_graph_timeout": 60, "report_merge_timeout": 10}
}
```

### 6.2 代码中的硬编码默认值

| 参数 | 文件 | 行号 | 默认值 |
|------|------|------|--------|
| `llmTimeoutSec` | `test_suggestion_executor.go` | 47 | 300 秒 |
| `EnrichTimeout` | `agent_enricher.go` | 461 | 300 秒 |
| `VerifyTimeout` | `agent_enricher.go` | 60 | 300 秒 |
| `timeoutSec` | `review_arbitration_executor.go` (New) | 23 | 60 秒（被 stage config 覆盖为 1600） |

---

*报告生成时间：2026-08-21*  
*核查文件总数：6 个核心执行器文件 + 1 个引擎文件 + 1 个配置服务文件 + 1 个模型定义文件*  
*数据来源：生产数据库 + 代码静态走读 + 实际 diff 交叉验证*
