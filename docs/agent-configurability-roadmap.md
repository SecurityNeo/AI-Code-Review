# 智能体可配置化需求分析

> 状态：需求分析阶段 | 最后更新：2026-08-10
>
> 本文档基于 CodeGuard Pipeline 架构（v2.0+）设计，定义 AI 评审任务的"智能体"概念化配置方案。
> 文档为讨论基准，**尚未进入编码阶段**。

---

## 1. 核心洞察：Pipeline 就是一组"智能体"

从用户视角看，CodeGuard 的 Pipeline 不是底层技术流程，而是一组可以按需组合的 **AI 智能体（AI Agent）**。每个 Pipeline 阶段本质上是一个拥有独立职责、可被独立启用的智能体：

| 智能体概念 | 对应 Pipeline 阶段 | 核心职责 | 能否独立禁用 |
|:---|:---|:---|:---:|
| 触发过滤器 | `trigger_check` | 判断 MR 是否值得审查 | ✅ |
| 代码理解器 | `context_extract` | AST 解析变更上下文 | ✅ |
| 安全扫描智能体 | `dependency_scan` | 解析依赖并匹配已知漏洞库 | ✅ |
| 风险评估智能体 | `risk_analysis` | 基于变更复杂度与依赖风险判定审查策略 | ✅ |
| AI 评审智能体 | `batch_review_frame` | LLM 结构化代码审查（含分批/波次/汇总） | ✅ |
| 发布智能体 | `post_process` | 评论发布、阈值检查、通知发送 | ✅ |

> **边界约定**：`batch_review_frame` 作为 Group 阶段，内部包含 `batch_plan`、`batch_review`、`batch_summary` 三个子阶段。智能体开关作用于 Group 级别，不暴露子阶段给用户。

---

## 2. 需求场景

### 场景 A：轻量级评审（小团队 / 快速通道）

> "我们团队小，不想让 AI 做深度分析，只要它看看有没有安全漏洞就行"

- **启用**：`trigger_check` + `dependency_scan` + `post_process`
- **禁用**：`context_extract`、`risk_analysis`、`batch_review_frame`
- **评价耗时**：约 5-15 秒（跳过 LLM 大模型调用）
- **Token 消耗**：不调用 LLM，仅做静态分析
- **适用**：日常小修小改、依赖升级类 MR

### 场景 B：纯深度代码评审（已有安全扫描工具）

> "我们已集成 SonarQube / Snyk 做安全扫描，想让 AI 只专注于代码质量和架构问题"

- **启用**：`trigger_check` + `context_extract` + `risk_analysis` + `batch_review_frame` + `post_process`
- **禁用**：`dependency_scan`
- **评价耗时**：30-120 秒（取决于 diff 大小）
- **Token 消耗**：正常 LLM 消耗
- **适用**：核心业务代码变更、架构评审

### 场景 C：灰度开关（逐项目差异化）

> "想在部分项目试运行新智能体，先在小范围验证效果"

- 项目 A（核心服务）：启用全部阶段
- 项目 B（内部工具）：禁用 `dependency_scan`，其余启用
- 项目 C（实验项目）：仅启用 `batch_review_frame` + `post_process`
- **价值**：降低新智能体接入风险，支持 A/B 效果对比

### 场景 D：合规限制（无外部网络 / 安全红线）

> "我们的 CI 环境不能访问外部漏洞库，需要关闭依赖扫描"

- **禁用**：`dependency_scan`
- **说明**：即使漏洞库已本地化（OSV JSON 导入），用户仍可要求禁用该阶段

---

## 3. 三阶段演进方案

### 阶段 1：模板级阶段开关（低投入，全场景支持）

#### 3.1.1 设计目标

利用现有 `ProjectTemplate` 配置体系，新增阶段启用/禁用能力。用户看到的是阶段列表与开关，技术门槛最低，与现有 `review_categories` 配置模式一致。

#### 3.1.2 数据模型

```go
// ProjectTemplate 扩展字段
type ProjectTemplate struct {
    // ... 现有字段（ID, Name, Description, MaxRulesPerReview 等）
    
    // 新增：阶段开关配置
    EnabledStages datatypes.JSON `gorm:"type:json" json:"enabled_stages"`
}

// 示例数据
{
    "enabled_stages": [
        "trigger_check",
        "dependency_scan", 
        "post_process"
    ]
}
```

> **向后兼容策略**：
> - `EnabledStages` 为 `NULL` 或空数组时，默认启用全部阶段（存量项目无感）
> - 新增模板时，前端默认全选所有阶段
> - 禁止全部禁用（至少保留 `trigger_check` + `post_process`）

#### 3.1.3 Engine 改造点

```go
// Engine.ExecuteTask 中，在执行阶段前增加检查
func (e *Engine) ExecuteTask(task *model.Task, inputs map[string]interface{}, broadcaster func(uint, string, map[string]interface{})) error {
    // ... 现有逻辑
    
    // 获取模板配置
    template := getTemplateForTask(task)
    enabledStages := getEnabledStages(template) // 兜底：全部启用
    
    for _, stageDef := range stageDefs {
        // 新增：检查阶段是否被启用
        if !isStageEnabled(stageDef.Code, enabledStages) {
            // 创建记录但标记为 skipped
            exec := createExecutionRecord(task, stageDef)
            ctx.MarkSkipped(exec)
            continue
        }
        
        // ... 原有执行逻辑
    }
}
```

#### 3.1.4 前端配置面板

在"项目模板管理" → "AI 评审配置" Tab 中新增"阶段开关"子面板：

```
┌─ 项目模板：后端服务标准模板 ─────────────────────┐
│                                                  │
│  [AI 评审配置]  [通知规则]  [阶段开关]            │
│                                                  │
│  ⚙️ 阶段启用配置                                  │
│                                                  │
│  ☑️ 触发过滤器 (trigger_check)                   │
│     判断 MR 是否值得审查 · 不可禁用               │
│                                                  │
│  ☑️ 代码理解器 (context_extract)                  │
│     AST 解析变更上下文                            │
│                                                  │
│  ☑️ 安全扫描智能体 (dependency_scan)              │
│     解析依赖并匹配已知漏洞库                      │
│                                                  │
│  ☑️ 风险评估智能体 (risk_analysis)                │
│     基于变更复杂度决定审查策略                    │
│                                                  │
│  ☑️ AI 评审智能体 (batch_review_frame)            │
│     LLM 结构化代码审查（含分批）                 │
│                                                  │
│  ☑️ 发布智能体 (post_process)                     │
│     评论发布、阈值检查 · 不可禁用                 │
│                                                  │
│  [保存配置]                                       │
└──────────────────────────────────────────────────┘
```

| 优点 | 缺点 |
|:---|:---|
| 实现简单，复用现有 Engine 阶段循环 | 用户看到的是技术术语（"阶段"），缺乏业务语义 |
| 与 `review_categories` 配置模式一致 | 无法组合自定义逻辑（如"仅看并发问题"） |
| 项目/模板级别独立配置，天然支持灰度 | `batch_review_frame` 内部子阶段不可单独控制 |

#### 3.1.5 工作量评估

| 模块 | 改动点 | 预估工时 |
|:---|:---|:---:|
| 数据层 | `ProjectTemplate` 新增 `enabled_stages` JSON 字段 + 迁移脚本 | 2h |
| Engine | `ExecuteTask` 中增加 `isStageEnabled` 检查逻辑 | 2h |
| API | `ProjectTemplate` CRUD 接口支持新字段 | 1h |
| 前端 | 配置面板新增阶段开关组件 | 4h |
| 测试 | 各阶段禁用/启用组合场景测试 | 3h |
| **合计** | | **~1 天** |

---

### 阶段 2：UI 包装为"智能体"概念（中度投入，提升用户体验）

#### 3.2.1 设计目标

后端数据层不变（仍用 `enabled_stages` 存储），前端增加"智能体"概念包装层。将技术术语转化为业务语言，降低用户认知门槛。

#### 3.2.2 智能体聚合定义

前端维护智能体与阶段的映射关系（后端不透出）：

```javascript
const BUILTIN_AGENTS = [
    {
        id: 'trigger_filter',
        name: '触发过滤器',
        description: '判断 MR 是否值得审查，过滤无关变更',
        icon: 'fa-filter',
        stages: ['trigger_check'],
        builtin: true,
        required: true,  // 不可禁用
    },
    {
        id: 'code_understander',
        name: '代码理解器',
        description: '提取函数、结构体、接口等 AST 上下文',
        icon: 'fa-sitemap',
        stages: ['context_extract'],
        builtin: true,
        required: false,
    },
    {
        id: 'security_scanner',
        name: '安全扫描智能体',
        description: '解析依赖文件并匹配本地漏洞库',
        icon: 'fa-shield-alt',
        stages: ['dependency_scan'],
        builtin: true,
        required: false,
    },
    {
        id: 'risk_analyzer',
        name: '风险评估智能体',
        description: '基于变更行数、敏感路径、依赖风险决定审查策略',
        icon: 'fa-exclamation-triangle',
        stages: ['risk_analysis'],
        builtin: true,
        required: false,
    },
    {
        id: 'ai_reviewer',
        name: 'AI 代码评审智能体',
        description: 'LLM 结构化代码审查（自动分批、波次并行、汇总裁决）',
        icon: 'fa-robot',
        stages: ['batch_review_frame'],  // 包含 batch_plan + batch_review + batch_summary
        builtin: true,
        required: false,
    },
    {
        id: 'publisher',
        name: '发布智能体',
        description: '发布评审评论、触发通知、执行阈值检查',
        icon: 'fa-paper-plane',
        stages: ['post_process'],
        builtin: true,
        required: true,  // 不可禁用
    }
];
```

#### 3.2.3 一键模板（Preset Profiles）

将常用组合封装为"评审模式"：

```javascript
const REVIEW_PRESETS = [
    {
        id: 'lightweight',
        name: '轻量级评审',
        description: '仅安全扫描，极速完成（~5秒）',
        icon: 'fa-bolt',
        agents: ['trigger_filter', 'security_scanner', 'publisher']
    },
    {
        id: 'standard',
        name: '标准评审',
        description: '完整代码评审流程（~30-120秒）',
        icon: 'fa-check-circle',
        agents: ['trigger_filter', 'code_understander', 'risk_analyzer', 'ai_reviewer', 'publisher']
    },
    {
        id: 'security_focus',
        name: '深度安全',
        description: '安全扫描 + 完整代码评审',
        icon: 'fa-shield-alt',
        agents: ['trigger_filter', 'code_understander', 'security_scanner', 'risk_analyzer', 'ai_reviewer', 'publisher']
    }
];
```

#### 3.2.4 前端交互设计

```
┌─ 项目模板：后端服务标准模板 ─────────────────────┐
│                                                  │
│  [AI 评审配置]  [通知规则]  [智能体配置]          │
│                                                  │
│  ⚡ 快速模板                                      │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐        │
│  │ 轻量级    │ │ 标准评审  │ │ 深度安全  │        │
│  │ ⚡ ~5秒   │ │ ✓ 完整   │ │ 🛡️ 全量  │        │
│  │ [应用]    │ │ [应用]    │ │ [应用]    │        │
│  └──────────┘ └──────────┘ └──────────┘        │
│                                                  │
│  🤖 智能体配置                                    │
│  ┌─────────────────────────────────────────┐    │
│  │ ☑️ 触发过滤器     [不可禁用]             │    │
│  │    └── 判断 MR 是否值得审查              │    │
│  ├─────────────────────────────────────────┤    │
│  │ ☑️ 代码理解器                          │    │
│  │    └── 提取函数、结构体等 AST 上下文     │    │
│  ├─────────────────────────────────────────┤    │
│  │ ☐  安全扫描智能体  ← 未启用              │    │
│  │    └── 解析依赖并匹配漏洞库              │    │
│  ├─────────────────────────────────────────┤    │
│  │ ☑️ 风险评估智能体                        │    │
│  │    └── 基于复杂度决定审查策略            │    │
│  ├─────────────────────────────────────────┤    │
│  │ ☑️ AI 代码评审智能体                     │    │
│  │    └── LLM 结构化审查（自动分批）        │    │
│  ├─────────────────────────────────────────┤    │
│  │ ☑️ 发布智能体     [不可禁用]             │    │
│  │    └── 发布评论、发送通知                │    │
│  └─────────────────────────────────────────┘    │
│                                                  │
│  [保存配置]                                       │
└──────────────────────────────────────────────────┘
```

**交互规则：**
- 选择"快速模板"时，自动勾选/取消对应的智能体
- 用户可基于模板做微调（如标准评审模板但关闭安全扫描）
- 每个智能体卡片展开显示其包含的阶段及预计耗时
- "不可禁用"的智能体置灰显示，不予许取消勾选

| 优点 | 缺点 |
|:---|:---|
| 用户认知门槛大幅降低 | 前端需要维护 Agent → Stage 映射表 |
| "快速模板"极大提升配置效率 | 后端仍是阶段粒度，无法组合自定义逻辑 |
| 与阶段 1 完全兼容，可无缝升级 | 需要设计讨论确定 Agent 命名和描述 |

#### 3.2.5 工作量评估

| 模块 | 改动点 | 预估工时 |
|:---|:---|:---:|
| 前端 | 智能体卡片组件 + 一键模板 + 展开详情 | 8h |
| 前端 | 映射逻辑：Agent 选择与 `enabled_stages` 双向同步 | 3h |
| 设计 | Agent 命名、描述、图标、预设模板确定 | 2h |
| 测试 | 预设模板应用、自定义微调场景测试 | 3h |
| **合计** | | **~2 天** |

---

### 阶段 3：自定义智能体组合（高投入，长期演进）

#### 3.3.1 设计目标

用户不仅可以选择系统预置的智能体，还可以**创建自己的智能体**——指定名称、选择阶段、甚至注入自定义 Prompt 规则。

#### 3.3.2 数据模型

新增 `PipelineAgent` 表：

```go
// PipelineAgent 用户自定义/系统预置的智能体
type PipelineAgent struct {
    ID          uint           `gorm:"primaryKey" json:"id"`
    Name        string         `gorm:"size:64" json:"name"`
    Description string         `gorm:"size:256" json:"description"`
    Icon        string         `gorm:"size:32" json:"icon"`           // FontAwesome class
    Stages      datatypes.JSON `gorm:"type:json" json:"stages"`       // ["context_extract", "batch_review_frame"]
    CustomRules datatypes.JSON `gorm:"type:json" json:"custom_rules"` // 可选：自定义规则注入
    IsBuiltin   bool           `gorm:"default:false" json:"is_builtin"` // 系统预置不可删
    Enabled     bool           `gorm:"default:true" json:"enabled"`
    CreatedBy   uint           `json:"created_by"`                     // 0 = 系统
    CreatedAt   time.Time      `json:"created_at"`
    UpdatedAt   time.Time      `json:"updated_at"`
}

// ProjectTemplate-Agent 关联表（多对多）
type ProjectTemplateAgent struct {
    ID                uint `gorm:"primaryKey"`
    ProjectTemplateID uint `gorm:"index"`
    PipelineAgentID   uint `gorm:"index"`
    Enabled           bool `gorm:"default:true"`
    SortOrder         int  `json:"sort_order"`  // 控制 Agent 执行顺序
}
```

#### 3.3.3 应用场景

**应用 1："仅看 Go 并发问题"**
- 选择阶段：`context_extract` + `batch_review_frame`
- 自定义规则：仅注入并发相关规则（`go_concurrency` 维度）
- 预期效果：LLM 只关注 channel、goroutine、mutex 相关问题

**应用 2："仅做安全扫描，不做代码审查"**
- 选择阶段：`trigger_check` + `dependency_scan` + `post_process`
- 无自定义规则
- 预期效果：5 秒内完成，仅检查依赖漏洞

**应用 3："评审 + 强制风险分析"**
- 选择阶段：全部阶段
- 自定义规则：扩展 `risk_analysis` 的敏感度路径列表
- 预期效果：对核心业务路径变更加强审查

#### 3.3.4 前端交互设计

```
┌─ 智能体管理 ─────────────────────────────────────┐
│                                                  │
│  🤖 系统预置                                      │
│  ┌─────────────────────────────────────────┐    │
│  │ 触发过滤器 · 代码理解器 · 安全扫描...   │    │
│  │ [仅可开关，不可编辑/删除]               │    │
│  └─────────────────────────────────────────┘    │
│                                                  │
│  ➕ 自定义智能体                                  │
│  ┌─────────────────────────────────────────┐    │
│  │ 我的智能体 #1: "Go 并发审查"            │    │
│  │ 阶段: context_extract → batch_review   │    │
│  │ 规则: 仅 go_concurrency                │    │
│  │ [编辑] [删除] [应用到模板]              │    │
│  ├─────────────────────────────────────────┤    │
│  │ 我的智能体 #2: "仅安全扫描"             │    │
│  │ 阶段: trigger_check → dependency_scan  │    │
│  │ [编辑] [删除] [应用到模板]              │    │
│  └─────────────────────────────────────────┘    │
│                                                  │
│  [+ 新建智能体]                                   │
└──────────────────────────────────────────────────┘
```

**新建智能体弹窗：**
```
┌─ 新建自定义智能体 ────────────────────────────────┐
│                                                  │
│  名称: [Go 并发审查                    ]         │
│  描述: [仅关注 goroutine、channel、mutex ]         │
│                                                  │
│  选择阶段：                                       │
│  ☑️ 代码理解器 (context_extract)                  │
│  ☑️ AI 代码评审智能体 (batch_review_frame)        │
│                                                  │
│  评审规则过滤（可选）：                           │
│  [☑️] go_concurrency                              │
│  [☐] security                                    │
│  [☐] performance                                 │
│  ...                                              │
│                                                  │
│  [取消] [创建]                                    │
└──────────────────────────────────────────────────┘
```

#### 3.3.5 技术挑战

| 挑战 | 说明 | 解决思路 |
|:---|:---|:---|
| Agent 排序 | 多个 Agent 包含相同阶段时的执行顺序 | `SortOrder` 字段控制，同阶段去重 |
| 规则过滤 | Agent 注入的 `CustomRules` 如何影响 `batch_review_frame` 的 Prompt | 在 `PromptContext` 中增加 `custom_rules_override` |
| Agent 间依赖 | 若 Agent A 需要 `context_extract` 输出，但 Agent B 禁用了它 | Engine 自动解析阶段依赖，确保前置阶段被启用 |
| 模板绑定 | 同一 Agent 在不同模板中的启用状态不同 | `ProjectTemplateAgent` 关联表管理 |

#### 3.3.6 工作量评估

| 模块 | 改动点 | 预估工时 |
|:---|:---|:---:|
| 数据层 | `PipelineAgent` 模型 + 关联表 + 迁移 | 4h |
| Engine | Agent 解析器 + 阶段依赖自动补全 + 规则注入 | 8h |
| API | Agent CRUD + 模板绑定接口 | 4h |
| 前端 | Agent 管理页面 + 新建/编辑弹窗 + 规则过滤 | 12h |
| 测试 | 自定义 Agent 全流程测试 + 边界场景 | 8h |
| **合计** | | **~4-5 天** |

---

## 4. 技术可行性评估

| 维度 | 阶段 1 | 阶段 2 | 阶段 3 |
|:---|:---:|:---:|:---:|
| **数据库迁移** | 轻（1 JSON 字段） | 无（复用阶段 1） | 中（新表 + 关联表） |
| **Pipeline Engine 改造** | 轻（跳过逻辑） | 无（复用阶段 1） | 中（Agent 解析 + 依赖补全） |
| **前端改动** | 中（配置面板） | 中（Agent 包装层） | 高（Agent 管理 + 规则过滤） |
| **向后兼容** | 优（默认全启用） | 优（复用数据层） | 良（需迁移存量配置） |
| **性能影响** | 无（跳过不执行） | 无 | 微（Agent 解析开销） |
| **实施周期** | ~1 天 | ~2 天 | ~4-5 天 |
| **风险** | 低 | 低 | 中（概念复杂度高） |

---

## 5. 建议落地顺序

```
Week 1: 阶段 1 —— 模板级阶段开关
    ├── Day 1: 数据库迁移 + Engine 跳过逻辑
    ├── Day 2: API 接口 + 前端配置面板
    └── Day 3: 集成测试 + 边界场景验证

Week 2-3: 阶段 2 —— 智能体概念包装
    ├── Day 1-2: 设计确认（Agent 命名/描述/图标/预设）
    ├── Day 3-4: 前端智能体卡片组件 + 一键模板
    ├── Day 5: 映射逻辑 + 预设模板应用
    └── Day 6-8: 测试 + 体验微调

Week 4+: 阶段 3 —— 自定义智能体（按需启动）
    ├── Day 1-2: PipelineAgent 模型 + 关联表
    ├── Day 3-4: Engine Agent 解析器 + 依赖补全
    ├── Day 5: API 接口
    ├── Day 6-8: 前端 Agent 管理页面
    └── Day 9-10: 测试 + 文档
```

### 关键决策建议

1. **现阶段（Pipeline 已拆分完毕）推荐立即启动阶段 1**：
   - `dependency_scan` 已从 `risk_analysis` 独立，阶段边界清晰
   - 各 `StageExecutor` 实现 `Code()` 接口，跳过逻辑零耦合
   - 投入仅 1 天，即可覆盖场景 A/B/C/D 全部需求

2. **阶段 2 是体验分水岭**：
   - 决定用户是否愿意主动配置（"阶段" vs "智能体"的认知差异）
   - 建议在阶段 1 上线后收集用户反馈，再决定是否投入阶段 2

3. **阶段 3 属于生态建设**：
   - 只有在用户明确表示"预设不够用"时才启动
   - 前置依赖：阶段 2 的 Agent 概念已被用户接受
   - 避免过度设计，防止功能膨胀

---

## 6. 与当前架构的契合度分析

当前 Pipeline 架构已具备的实施条件：

| 条件 | 说明 | 状态 |
|:---|:---|:---:|
| `ReviewPipelineStage` 表 | 阶段元数据完整（名称、描述、图标、超时、SortOrder） | ✅ |
| `TaskPipelineExecution` 表 | 父/子阶段关系已建立，支持 `MarkSkipped` | ✅ |
| `StageExecutor` 接口 | 每个执行器实现 `Code()` 方法，可直接匹配 | ✅ |
| `Engine` 阶段循环 | `for _, stageDef := range stageDefs` 循环天然支持跳过 | ✅ |
| `MarkSkipped` 状态 | 已有跳过状态，前端有对应 UI（灰显 + 标注"跳过"） | ✅ |
| `ProjectTemplate` 模型 |  review 任务已关联模板，配置有挂载点 | ✅ |
| `DependencyScanExecutor` 独立 | 为场景 A/B 提供了清晰的跳过目标 | ✅ |
| 快照系统 | 跳过阶段不产生快照，无数据残留问题 | ✅ |

**结论：当前架构完全支持阶段 1 立即实施，无需重构。**

---

## 7. 待讨论事项

1. **阶段 1 中 `batch_review_frame` 的子阶段控制**：
   - 是否需要暴露 `batch_plan`、`batch_review`、`batch_summary` 给用户？
   - 如果允许单独禁用 `batch_summary`，多批评审后的汇总逻辑如何处理？
   - **建议**：阶段 1 不暴露子阶段，以 Group 级别控制；阶段 3 再考虑子阶段级别

2. **阶段 2 的 Agent 命名与描述**：
   - 当前命名（"安全扫描智能体"）是否为最终命名？
   - 是否需要支持国际化（i18n）？
   - **建议**：先以中文命名上线，英文版在国际化需求明确后再补充

3. **阶段 3 的自定义规则过滤**：
   - `CustomRules` 的存储格式（JSON Schema / 规则 ID 列表 / 完整规则内容）？
   - 规则过滤是仅影响 Prompt（前端过滤），还是影响规则选择器（后端过滤）？
   - **建议**：先支持规则 ID 列表（`["go_concurrency", "sql_injection"]`），后端在构建 Prompt 时过滤

4. **权限控制**：
   - 谁可以创建/编辑/删除自定义 Agent？
   - 预置 Agent 是否允许组织管理员修改？
   - **建议**：普通用户可创建 Agent（组织内共享）；预置 Agent 仅超级管理员可编辑

5. **监控与审计**：
   - 是否需要记录 Agent 启用状态变更日志？
   - 任务执行时是否需要记录启用了哪些 Agent？
   - **建议**：阶段 1 不增加额外监控；阶段 2 在任务详情页显示"本次启用的智能体"列表

---

## 附录 A：阶段开关设计参考图


    Pipeline 执行流程（阶段 1 实施后）
    
    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
    │ trigger_check│───▶│  git_clone  │───▶│context_extract│
    │  (always ON) │    │  (always ON) │    │ Optional    │
    └─────────────┘    └─────────────┘    └──────┬──────┘
                                                  │
    ┌─────────────┐    ┌─────────────┐    ┌─────▼──────┐
    │ post_process │◀───│ batch_review│◀───│risk_analysis│
    │  (always ON) │    │   _frame    │    │  Optional   │
    └─────────────┘    └──────▲──────┘    └─────┬──────┘
                               │                 │
                         ┌─────┴─────┐    ┌─────▼──────┐
                         │ dependency │    │  Skipped   │
                         │   _scan    │    │  (disabled)│
                         │  Optional  │    │            │
                         └───────────┘    └────────────┘


---

## 附录 B：API 设计草案（阶段 1）

### GET /api/v1/project-templates/:id/agents-config

获取模板的阶段开关配置：

```json
{
    "data": {
        "template_id": 1,
        "enabled_stages": ["trigger_check", "git_clone", "context_extract", "risk_analysis", "batch_review_frame", "post_process"],
        "available_stages": [
            { "code": "trigger_check", "name": "触发条件评估", "description": "...", "icon": "fas fa-filter", "required": true },
            { "code": "dependency_scan", "name": "依赖漏洞扫描", "description": "...", "icon": "fas fa-shield-alt", "required": false }
        ]
    }
}
```

### PUT /api/v1/project-templates/:id/agents-config

更新阶段开关：

```json
{
    "enabled_stages": ["trigger_check", "git_clone", "dependency_scan", "post_process"]
}
```

---

## 附录 C：前端组件设计（阶段 1）

### AgentTogglePanel 组件

```vue
<!-- 伪代码，用于讨论 -->
<template>
  <div class="agent-toggle-panel">
    <div class="preset-templates mb-4" v-if="showPresets">
      <button v-for="preset in presets" :key="preset.id" @click="applyPreset(preset)">
        {{ preset.name }}
      </button>
    </div>
    
    <div class="agent-list">
      <div v-for="stage in stages" :key="stage.code" class="agent-item">
        <toggle-switch 
          v-model="stage.enabled" 
          :disabled="stage.required"
        />
        <div class="agent-info">
          <div class="agent-name">
            <i :class="stage.icon"></i>
            {{ stage.name }}
            <span v-if="stage.required" class="badge">必选</span>
          </div>
          <div class="agent-desc">{{ stage.description }}</div>
        </div>
      </div>
    </div>
  </div>
</template>
```

---

*本文件为需求分析文档，非最终设计规格。进入编码阶段前需针对"待讨论事项"完成决策确认。*
