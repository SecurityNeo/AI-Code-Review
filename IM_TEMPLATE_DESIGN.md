# Issue 升级 IM 消息模板化 - 需求分析与设计方案

## 一、需求明确

1. **站内信保持当前代码硬编码的方式**，无需改动。
2. **IM 消息需要支持模板配置**，每个升级阶段（Level）独立配置 IM 模板。
3. **IM 消息中提供聚合 Issue 的模板变量**，格式如 `【1222】存在信息泄露风险`。
4. **趁模块未正式投入使用，一次性按最完善方案实现**，避免后期存量数据兼容包袱。

---

## 二、当前架构全景分析

### 2.1 数据模型

```go
// NotificationRule（通知规则表）
type NotificationRule struct {
    ID                uint
    Name              string
    Trigger           string              // task.completed / task.failed / issue.escalation
    Provider          string              // wecom / dingtalk / lark
    NotifierID        *uint               // 关联的机器人 ID
    Template          string              // 通用模板（目前仅 task.completed 在用）
    EscalationConfig  string  `gorm:"type:json"`  // 升级策略 JSON
    // ... 其他字段
}

// EscalationStage（升级阶段，存储于 EscalationConfig 字段中）
type EscalationStage struct {
    Level          int    `json:"level"`
    ThresholdHours int    `json:"threshold_hours"`
    Recipient      string `json:"recipient"`   // owner / owner+steward / steward / admin / auto_archive
    NotifyIM       bool   `json:"notify_im"`
}
```

当前 `EscalationConfig` 示例（DB 中实际存储）：
```json
[
  {"level":1,"threshold_hours":1,"recipient":"owner","notify_im":false},
  {"level":2,"threshold_hours":2,"recipient":"owner+steward","notify_im":true},
  {"level":3,"threshold_hours":3,"recipient":"steward","notify_im":true},
  {"level":4,"threshold_hours":4,"recipient":"admin","notify_im":true},
  {"level":5,"threshold_hours":5,"recipient":"admin","notify_im":true}
]
```

### 2.2 当前 IM 发送链路

| 升级阶段 | 站内信 | IM 发送 | 当前实现方式 | 模板支持？ |
|---------|--------|---------|-------------|-----------|
| **Level 1** (24h/首次提醒) | ✅ Owner | ❌ 不发送 | `collectAlert` 硬编码 | ❌ |
| **Level 2** (72h/协助督促) | ✅ Owner + Steward | ✅ Steward群 | `collectIM` + `flushIMs` 硬编码 | ❌ |
| **Level 3** (120h/正式升级) | ✅ Steward + Owner | ✅ Steward群 | `collectIM` + `flushIMs` 硬编码 | ❌ |
| **Level 4** (240h/全局告警) | ✅ Admin | ✅ Admin逐条 | `sendEscalationIMToAdmins` 硬编码 | ❌ |
| **Level 5** (归档) | ✅ Owner + Admin | ❌ 不发送 | `collectAlert` 硬编码 | ❌ |

**核心问题**：5 个阶段的 IM 消息全部是代码里写死的字符串，**零模板支持**。

### 2.3 当前模板系统的状态

现有 `RenderMessage` 函数支持以下变量（专用于 `task.completed`）：

```
{{PROJECT_NAME}} {{TASK_ID}} {{EXECUTION_COUNT}} {{MR_IID}} {{MR_TITLE}}
{{MR_AUTHOR}} {{DEVELOPER}} {{BRANCH}} {{SCORE}} {{CHANGES}} {{MR_URL}}
{{ISSUE_TOTAL}} {{ISSUE_PENDING}} {{ISSUE_CRITICAL}} ...（统计类）
{{STEWARD_NAME}} {{DEADLINE_HOURS}} {{AT_RECIPIENT}}
```

**当前 Issue 升级中这个系统完全没被使用**：
- `sendEscalationIM` 函数（第 611 行）虽然调用了 `GetNotificationRuleTemplate("issue.escalation")`，但**没有任何调用方**
- 它就是一段死代码

### 2.4 当前聚合逻辑

`collectIM` → `flushIMs` 采用**按 `taskID:stage` 维度聚合**：
- 同一 task 同一 stage 的多个 Issue → 合并为 1 条 IM
- 聚合内容只是简单字符串拼接，丢失了 Issue 的 ID/描述结构信息

```go
// 当前聚合后格式（硬编码）
**⚠️ Issue 升级告警（15 条）**

项目：cmdb
MR：!21 feat: AI审核...

Issue #72 已超期 3 个工作日
问题：函数未处理 nil 指针
---
Issue #73 已超期 3 个工作日
问题：变量未初始化
---
...
@steward1 @steward2
```

---

## 三、最完善方案设计

### 3.1 设计原则

1. **站内信不变**：维持当前硬编码 `collectAlert` + `flushAlerts`
2. **IM 全模板化**：每个 Level 独立配置 IM 模板，存储于 `escalation_config` JSON 中
3. **聚合变量支持**：提供 `{{ISSUE_LIST}}` 变量，聚合后渲染为 `【{id}】{message}` 格式
4. **向后兼容**：旧数据无模板时自动降级为硬编码默认文案
5. **最小 DB 改动**：利用现有 `escalation_config` JSON 字段扩展，**无需 migration**

### 3.2 数据模型扩展

**`EscalationStage` 新增 `im_template` 字段**：

```go
type EscalationStage struct {
    Level          int    `json:"level"`
    ThresholdHours int    `json:"threshold_hours"`
    Recipient      string `json:"recipient"`
    NotifyIM       bool   `json:"notify_im"`
    IMTemplate     string `json:"im_template"`  // ← 新增：IM 模板（Markdown）
}
```

**扩展后 `EscalationConfig` 存储示例**：

```json
[
  {"level":1,"threshold_hours":24,"recipient":"owner","notify_im":false,"im_template":""},
  {
    "level":2,
    "threshold_hours":72,
    "recipient":"owner+steward",
    "notify_im":true,
    "im_template":"**⚠️ Issue 协助督促（{{ISSUE_COUNT}} 条）**\n\n项目：{{PROJECT_NAME}}\nMR：!{{MR_IID}} {{MR_TITLE}}\n\n{{ISSUE_LIST}}\n\n{{AT_RECIPIENT}}\n> 请在 2 个工作日内处理，否则将正式升级给项目负责人。"
  },
  {
    "level":3,
    "threshold_hours":120,
    "recipient":"steward",
    "notify_im":true,
    "im_template":"**⬆️ Issue 正式升级（{{ISSUE_COUNT}} 条）**\n\n项目：{{PROJECT_NAME}}\nMR：!{{MR_IID}} {{MR_TITLE}}\n\n{{ISSUE_LIST}}\n\n{{AT_RECIPIENT}}\n> 本 Issue 已超期 5 个工作日，请尽快协助处理。"
  },
  {
    "level":4,
    "threshold_hours":240,
    "recipient":"admin",
    "notify_im":true,
    "im_template":"**【全局告警】Issue 超期 10 天（{{ISSUE_COUNT}} 条）**\n\n项目：{{PROJECT_NAME}}\nMR：!{{MR_IID}} {{MR_TITLE}}\n开发者：{{DEVELOPER}}\n\n{{ISSUE_LIST}}\n\n{{AT_RECIPIENT}}\n> Issue 已超期 10 天未闭环，请关注。"
  },
  {"level":5,"threshold_hours":360,"recipient":"auto_archive","notify_im":false,"im_template":""}
]
```

> **设计说明**：
> - Level 1/5 的 `notify_im` 始终是 false，`im_template` 可以为空
> - Level 2/3/4 的 `im_template` 默认预填系统推荐文案，允许用户自定义
> - 空模板 → 自动降级为当前硬编码默认文案（向后兼容兜底）

### 3.3 IM 模板变量体系

#### 通用变量（所有 Level 均可用）

| 变量 | 说明 | 示例 |
|------|------|------|
| `{{PROJECT_NAME}}` | 项目名称 | cmdb |
| `{{MR_IID}}` | MR 编号 | 21 |
| `{{MR_TITLE}}` | MR 标题 | feat: AI审核... |
| `{{MR_AUTHOR}}` | MR 提交者 GitLab 用户名 | root |
| `{{DEVELOPER}}` | 开发者展示名 | root |
| `{{TASK_ID}}` | 任务 ID | 138 |
| `{{AT_RECIPIENT}}` | @收件人列表（已拼接好） | `<@WangXiaoMing>` |

#### 聚合专用变量（只有聚合发送时可用）

| 变量 | 说明 | 示例（2条聚合） |
|------|------|----------------|
| `{{ISSUE_COUNT}}` | 本次聚合的 Issue 数量 | 2 |
| `{{ISSUE_LIST}}` | 聚合后的 Issue 列表 | `【72】存在信息泄露风险\n【73】未处理nil指针` |

`{{ISSUE_LIST}}` 的渲染规则：
- 每条 Issue → 一行 `【{ID}】{Message}`
- 多条之间用 `\n` 换行
- 单条场景下就是一行

#### 单条专用变量（逐条发送时可用）

| 变量 | 说明 | 示例 |
|------|------|------|
| `{{ISSUE_ID}}` | 当前 Issue ID | 72 |
| `{{ISSUE_MESSAGE}}` | 当前 Issue 描述 | 存在信息泄露风险 |

> **什么时候用单条？**
> 如果一次执行中某个 task 的某个 level 只有 1 条 Issue，聚合后和单条效果一致。模板可以统一使用 `{{ISSUE_LIST}}`，它会自动渲染为一行。

### 3.4 后端渲染引擎

#### 新增 `IMTemplateContext` 结构体

```go
// IMIssueItem 聚合中的单条 Issue
type IMIssueItem struct {
    ID      uint
    Message string
}

// IMTemplateContext IM 模板渲染上下文
type IMTemplateContext struct {
    Project      model.Project
    Task         model.Task
    Developer    string
    AtRecipient  string
    // 聚合模式
    IssueCount   int
    IssueList    []IMIssueItem
    // 单条模式（兜底兼容）
    IssueID      uint
    IssueMessage string
}
```

#### 新增 `RenderIMTemplate` 渲染函数

```go
// RenderIMTemplate 渲染 IM 升级模板
func RenderIMTemplate(template string, ctx IMTemplateContext) string {
    if template == "" {
        return ""
    }

    msg := template
    msg = strings.ReplaceAll(msg, "{{PROJECT_NAME}}", ctx.Project.Name)
    msg = strings.ReplaceAll(msg, "{{TASK_ID}}", fmt.Sprintf("%d", ctx.Task.ID))
    msg = strings.ReplaceAll(msg, "{{MR_IID}}", fmt.Sprintf("%d", ctx.Task.MRMergeID))
    msg = strings.ReplaceAll(msg, "{{MR_TITLE}}", ctx.Task.MRTitle)
    msg = strings.ReplaceAll(msg, "{{MR_AUTHOR}}", ctx.Task.MRAuthor)
    msg = strings.ReplaceAll(msg, "{{DEVELOPER}}", ctx.Developer)
    msg = strings.ReplaceAll(msg, "{{AT_RECIPIENT}}", ctx.AtRecipient)

    // 聚合变量
    msg = strings.ReplaceAll(msg, "{{ISSUE_COUNT}}", fmt.Sprintf("%d", ctx.IssueCount))

    // ISSUE_LIST 渲染：每条 → 【ID】Message
    if strings.Contains(msg, "{{ISSUE_LIST}}") {
        var lines []string
        if len(ctx.IssueList) > 0 {
            for _, item := range ctx.IssueList {
                lines = append(lines, fmt.Sprintf("【%d】%s", item.ID, item.Message))
            }
        } else {
            // 单条模式兜底
            lines = append(lines, fmt.Sprintf("【%d】%s", ctx.IssueID, ctx.IssueMessage))
        }
        msg = strings.ReplaceAll(msg, "{{ISSUE_LIST}}", strings.Join(lines, "\n"))
    }

    // 单条变量兜底
    msg = strings.ReplaceAll(msg, "{{ISSUE_ID}}", fmt.Sprintf("%d", ctx.IssueID))
    msg = strings.ReplaceAll(msg, "{{ISSUE_MESSAGE}}", ctx.IssueMessage)

    return msg
}
```

### 3.5 IM 聚合逻辑改造

#### 改造 `imBatch` 结构体

当前 `imBatch` 只保存字符串切片，丢失结构化信息：

```go
// 改造前（当前代码）
type imBatch struct {
    project    model.Project
    task       model.Task
    stage      string                    // "72h", "120h" 等
    stewards   []model.ProjectResponsibility
    issueItems []string                  // ← 只存了字符串，丢失了ID
}
```

改造后：

```go
// 改造后
type imBatch struct {
    project    model.Project
    task       model.Task
    stage      string                    // "72h", "120h", "240h"
    level      int                       // 2, 3, 4
    mentions   []string                  // @列表（"<@im_user_id>" 格式）
    issues     []imIssue                 // 结构化 Issue 列表
    imTemplate string                    // 该 stage 的 IM 模板
}

type imIssue struct {
    ID      uint
    Message string
}
```

> **关键改动**：`stewards []model.ProjectResponsibility` → `mentions []string`
> 
> 原因为 Level 4 的收件人是管理员，不是 ProjectResponsibility。统一为 `@mentions` 字符串列表，steward 和 admin 都用同一套聚合逻辑。

#### 改造 `collectIM` 方法

```go
// collectIM 将单条 IM 收入聚合桶（按 taskID:stage 聚合）
func (s *EscalationService) collectIM(
    project model.Project,
    task model.Task,
    stage string,
    level int,
    mentions []string,           // ← 直接传 @列表
    imTemplate string,
    issueID uint,
    issueMessage string,
) {
    key := fmt.Sprintf("%d:%s", task.ID, stage)
    if s.imCollector == nil {
        s.imCollector = make(map[string]*imBatch)
    }
    batch, ok := s.imCollector[key]
    if !ok {
        batch = &imBatch{
            project:    project,
            task:       task,
            stage:      stage,
            level:      level,
            mentions:   mentions,
            issues:     make([]imIssue, 0),
            imTemplate: imTemplate,
        }
        s.imCollector[key] = batch
    }
    batch.issues = append(batch.issues, imIssue{ID: issueID, Message: issueMessage})
}
```

#### 改造 `flushIMs` 方法

```go
func (s *EscalationService) flushIMs() {
    if len(s.imCollector) == 0 {
        return
    }
    for _, batch := range s.imCollector {
        var msg string

        if batch.imTemplate != "" {
            // === 走模板渲染 ===
            ctx := IMTemplateContext{
                Project:     batch.project,
                Task:        batch.task,
                Developer:   batch.task.MRAuthor,
                AtRecipient: strings.Join(batch.mentions, " "),
                IssueCount:  len(batch.issues),
                IssueList:   batch.issues,  // []imIssue → RenderIMTemplate 内部转为 【ID】Message
            }
            msg = RenderIMTemplate(batch.imTemplate, ctx)
        } else {
            // === 空模板兜底：使用当前硬编码格式 ===
            msg = s.defaultIMMessage(batch)
        }

        s.sendEscalationIMRaw(batch.project, batch.task, batch.mentions, msg)
    }
    s.imCollector = nil
}
```

> **向后兼容策略**：`batch.imTemplate == ""` 时自动降级为当前硬编码文案，确保旧数据不受影响。

#### `sendEscalationIMRaw` 签名调整

```go
// sendEscalationIMRaw 底层发送 IM
func (s *EscalationService) sendEscalationIMRaw(
    project model.Project,
    task model.Task,
    mentions []string,      // ← 直接接收 @列表
    msg string,
) {
    notifier := s.findProjectNotifier(project)
    if notifier == nil {
        return
    }

    // 拼接 @ 到消息末尾
    if len(mentions) > 0 {
        msg = msg + "\n" + strings.Join(mentions, " ")
    }

    ok, detail, err := s.notifierSvc.SendMessage(notifier.WebhookUrl, msg, "")
    // ... 日志记录（不变）
}
```

### 3.6 各 Level 调用点改造

#### Level 2 (72h) - 协助督促

```go
case EscalationLevel72h:
    stewards := s.findStewards(task.ProjectID, issue.Category, project.Language)

    // 1. 站内信（不变）
    if issue.OwnerID != nil {
        s.collectAlert(*issue.OwnerID, nextLevel, model.NotificationTypeIssueEscalation, ...)
    }
    for _, st := range stewards {
        s.collectAlert(st.MemberID, nextLevel, model.NotificationTypeIssueEscalation, ...)
    }

    // 2. IM（改为模板化）
    if currentStage.NotifyIM {
        mentions := buildStewardMentions(stewards)
        s.collectIM(project, task, "72h", nextLevel, mentions,
            currentStage.IMTemplate,  // ← 从配置读取模板
            issue.ID, issue.Message)
    }
```

#### Level 3 (120h) - 正式升级

```go
case EscalationLevel120hSteward:
    stewards := s.findStewards(task.ProjectID, issue.Category, project.Language)
    if len(stewards) > 0 {
        model.DB.Model(issue).UpdateColumn("current_owner_id", stewards[0].MemberID)
        // 站内信（不变）
        for _, st := range stewards {
            s.collectAlert(...)
        }
        // IM（模板化）
        if currentStage.NotifyIM {
            mentions := buildStewardMentions(stewards)
            s.collectIM(project, task, "120h", nextLevel, mentions,
                currentStage.IMTemplate,
                issue.ID, issue.Message)
        }
    }
    // Owner 站内信（不变）
    if issue.OwnerID != nil {
        s.collectAlert(*issue.OwnerID, nextLevel, ...)
    }
```

#### Level 4 (240h) - 全局告警

**关键改动点**：Level 4 原来是逐条发送给管理员，现在改为**聚合发送**（与 2/3 级一致），并且复用同一套 `collectIM` 逻辑。

```go
case EscalationLevel240hAdmin:
    var admins []model.User
    model.DB.Where("role = ?", model.RoleAdmin).Find(&admins)

    // 1. 站内信（不变）
    for _, admin := range admins {
        s.collectAlert(admin.ID, nextLevel, model.NotificationTypeIssueEscalation, ...)
    }

    // 2. IM（改造为聚合模式）
    if currentStage.NotifyIM {
        mentions := s.buildAdminMentions(admins)
        s.collectIM(project, task, "240h", nextLevel, mentions,
            currentStage.IMTemplate,
            issue.ID, issue.Message)
    }
```

新增辅助方法：

```go
// buildStewardMentions 从 ProjectResponsibility 构建 @ 列表
func buildStewardMentions(stewards []model.ProjectResponsibility) []string {
    var mentions []string
    for _, st := range stewards {
        if st.Member.IMUserID != "" {
            mentions = append(mentions, fmt.Sprintf("<@%s>", st.Member.IMUserID))
        }
    }
    return mentions
}

// buildAdminMentions 从 User 列表构建 @ 列表
func (s *EscalationService) buildAdminMentions(admins []model.User) []string {
    var gitlabUsernames []string
    for _, admin := range admins {
        if admin.GitlabUsername != "" {
            gitlabUsernames = append(gitlabUsernames, admin.GitlabUsername)
        }
    }
    if len(gitlabUsernames) == 0 {
        return nil
    }
    var members []model.TeamMember
    model.DB.Where("gitlab_username IN ?", gitlabUsernames).Find(&members)
    var mentions []string
    for _, m := range members {
        if m.IMUserID != "" {
            mentions = append(mentions, fmt.Sprintf("<@%s>", m.IMUserID))
        }
    }
    return mentions
}
```

#### 删除死代码

`sendEscalationIM`（第 610-644 行）和 `sendEscalationIMToAdmins`（第 646-699 行）在改造后不再被调用，可以删除。

---

## 四、前端改动设计

### 4.1 升级阶段表单增加 IM 模板编辑

当前每个 stage row 只有：级别 + 阈值 + 通知对象 + IM 开关 + 删除。需要增加一个**可折叠的模板编辑区**。

**UI 方案**：
- 每行 stage 增加一个「编辑 IM 模板」链接，点击后展开 textarea
- 或默认展示为一行小高度的 textarea（2行），获得焦点时自动增高

```html
<!-- 升级阶段表单每行增加 -->
<div class="mt-2" style="width:100%;">
    <div class="flex items-center justify-between">
        <label class="text-[10px] text-gray-400 font-medium">IM 消息模板</label>
        <button onclick="resetIMTemplate(${i})" class="text-[10px] text-blue-600 hover:text-blue-800">
            <i class="fas fa-undo mr-0.5"></i>重置默认
        </button>
    </div>
    <textarea rows="3" 
        class="w-full text-[11px] border rounded px-2 py-1 font-mono focus:ring-1 focus:ring-blue-500 focus:outline-none bg-gray-50 mt-0.5"
        placeholder="支持 {{PROJECT_NAME}} {{MR_TITLE}} {{ISSUE_COUNT}} {{ISSUE_LIST}} {{AT_RECIPIENT}} 等变量"
        onchange="updateStage(${i},'im_template',this.value)"
    >${escapeHtml(s.im_template || '')}</textarea>
</div>
```

### 4.2 默认模板常量

```javascript
const IM_TEMPLATE_DEFAULTS = {
    1: '',  // Level 1 不发送 IM
    2: '**⚠️ Issue 协助督促（{{ISSUE_COUNT}} 条）**\n\n项目：{{PROJECT_NAME}}\nMR：!{{MR_IID}} {{MR_TITLE}}\n\n{{ISSUE_LIST}}\n\n{{AT_RECIPIENT}}\n> 请在规定时间内处理，否则将正式升级。',
    3: '**⬆️ Issue 正式升级（{{ISSUE_COUNT}} 条）**\n\n项目：{{PROJECT_NAME}}\nMR：!{{MR_IID}} {{MR_TITLE}}\n\n{{ISSUE_LIST}}\n\n{{AT_RECIPIENT}}\n> 本 Issue 已超期，请尽快协助处理。',
    4: '**【全局告警】Issue 超期 10 天（{{ISSUE_COUNT}} 条）**\n\n项目：{{PROJECT_NAME}}\nMR：!{{MR_IID}} {{MR_TITLE}}\n开发者：{{DEVELOPER}}\n\n{{ISSUE_LIST}}\n\n{{AT_RECIPIENT}}\n> Issue 已超期未闭环，请关注。',
    5: ''  // Level 5 不发送 IM
};
```

### 4.3 模板变量参考面板更新

右侧变量参考面板增加 IM 专用变量分组：

```javascript
const imTemplateVars = [
    { name: '{{ISSUE_COUNT}}', desc: '本次聚合 Issue 数量' },
    { name: '{{ISSUE_LIST}}', desc: 'Issue 列表（聚合后）' },
    { name: '{{ISSUE_ID}}', desc: '单条 Issue ID' },
    { name: '{{ISSUE_MESSAGE}}', desc: '单条 Issue 描述' },
];
```

### 4.4 STAGE_DEFAULTS 更新

```javascript
const STAGE_DEFAULTS = [
    { threshold_hours: 24, recipient: 'owner', notify_im: false, im_template: '' },
    { threshold_hours: 72, recipient: 'owner+steward', notify_im: true, im_template: IM_TEMPLATE_DEFAULTS[2] },
    { threshold_hours: 120, recipient: 'steward', notify_im: true, im_template: IM_TEMPLATE_DEFAULTS[3] },
    { threshold_hours: 240, recipient: 'admin', notify_im: true, im_template: IM_TEMPLATE_DEFAULTS[4] },
    { threshold_hours: 360, recipient: 'auto_archive', notify_im: false, im_template: '' }
];
```

### 4.5 getEscalationJSON 更新

```javascript
function getEscalationJSON() {
    if (stages.length === 0) return '';
    stages.forEach((s, i) => { 
        if (!s.level) s.level = i + 1; 
        if (s.level === 1 || s.level === 5) s.notify_im = false;
    });
    return JSON.stringify(stages);
}
// im_template 字段会自然被序列化，因为 stages 数组中已有该属性
```

---

## 五、向后兼容策略

### 5.1 旧数据兼容（无需 migration）

| 场景 | 处理方案 |
|------|---------|
| 旧数据无 `im_template` 字段 | JSON unmarshal 时 `IMTemplate` 为空字符串，`flushIMs` 时走 `defaultIMMessage` 兜底 |
| 旧数据有 `im_template` 但值为空 | 同上 |
| 前端编辑旧规则保存 | `getEscalationJSON` 会包含 `im_template: ""`，不影响 |

### 5.2 默认文案兜底

```go
// defaultIMMessage 当模板为空时使用硬编码默认文案
func (s *EscalationService) defaultIMMessage(batch *imBatch) string {
    var stageText string
    switch batch.level {
    case 2:
        stageText = "已超期 3 个工作日"
    case 3:
        stageText = "已超期 5 个工作日，正式升级"
    case 4:
        stageText = "已超期 10 天"
    default:
        stageText = "升级提醒"
    }

    if len(batch.issues) == 1 {
        return fmt.Sprintf("**Issue #%d %s**\n问题：%s",
            batch.issues[0].ID, stageText, batch.issues[0].Message)
    }

    var lines []string
    for _, issue := range batch.issues {
        lines = append(lines, fmt.Sprintf("**Issue #%d** %s\n问题：%s", issue.ID, stageText, issue.Message))
    }
    return fmt.Sprintf("**⚠️ Issue 升级告警（%d 条）**\n\n项目：%s\nMR：!%d %s\n\n%s",
        len(batch.issues), batch.project.Name, batch.task.MRMergeID, batch.task.MRTitle,
        strings.Join(lines, "\n---\n"))
}
```

> 这个默认文案与当前硬编码完全一致，确保旧数据升级后行为不变。

### 5.3 前端兼容

`renderStages()` 中 `s.im_template` 可能为 undefined，模板 textarea 的 value 要用 `s.im_template || ''` 处理。

---

## 六、最终效果示例

### 场景：Level 2（72h），同一个 task 有 3 条 Issue 同时升级

**用户配置的模板**：
```markdown
**⚠️ Issue 协助督促（{{ISSUE_COUNT}} 条）**

项目：{{PROJECT_NAME}}
MR：!{{MR_IID}} {{MR_TITLE}}

{{ISSUE_LIST}}

{{AT_RECIPIENT}}
> 请在 2 个工作日内处理，否则将正式升级。
```

**聚合后渲染结果**：
```markdown
**⚠️ Issue 协助督促（3 条）**

项目：cmdb
MR：!21 feat: AI审核优化...

【72】存在信息泄露风险
【73】未处理 nil 指针
【74】SQL注入风险

<@wxid_steward1> <@wxid_steward2>
> 请在 2 个工作日内处理，否则将正式升级。
```

---

### 场景：Level 3（120h），单条 Issue 升级

**渲染结果**（`{{ISSUE_LIST}}` 自动适配单行）：
```markdown
**⬆️ Issue 正式升级（1 条）**

项目：cmdb
MR：!21 feat: AI审核优化...

【72】存在信息泄露风险

<@wxid_steward1>
> 本 Issue 已超期，请尽快协助处理。
```

---

## 七、改造影响范围清单

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `backend/internal/service/escalation.go` | 重构 | 核心改造：imBatch、collectIM、flushIMs、各 level case、删除死代码 |
| `backend/internal/service/notification_service.go` | 新增 | RenderIMTemplate 函数 |
| `prototype/notification-rules.html` | 新增 | 升级阶段表单增加 IM 模板输入、默认模板常量 |
| `backend/internal/model/notification_models.go` | 无需改动 | EscalationConfig 是 JSON 文本，扩展字段无需 migration |

**改动的代码量预估**：
- 后端：~100 行新增/修改，~60 行删除（死代码）
- 前端：~50 行新增
- **总计**：约 150 行有效改动，非常克制。

---

## 八、风险与注意事项

1. **Level 4 改为聚合发送**：原来 Level 4 是逐条给管理员发 IM，改造后变为按 task 聚合发送。行为变化：同一个 task 的多条 Issue 以前会发多条 IM 给管理员，现在只发 1 条聚合 IM。这是**优点**（减少刷屏），但需要确认是否符合业务预期。

2. **模板语法简单替换**：当前方案使用 `strings.ReplaceAll`（与现有 RenderMessage 一致），不支持复杂循环/条件语法。如果用户想在模板里做 `{{#each ISSUE_LIST}}` 之类的操作，当前引擎不支持。但 `{{ISSUE_LIST}}` 已能覆盖 95% 场景。

3. **顿号、单引号问题**：Issue Message 中可能包含特殊字符，直接拼接进模板会导致企业微信 Markdown 渲染异常。当前方案不做转义，因为企业微信 Bot 的 Markdown 支持比较宽松。如有需要，后续可增加 `strings.ReplaceAll(msg, "*", "\*")` 等简单转义。
