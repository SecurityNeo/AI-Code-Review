# 代码评审平台 - Issue 治理与通知系统设计方案 v1.0

---

## 1. 设计目标

1. **消除重复打扰**：同一 MR 反复重试时，开发者无需重复处理已标记的误报/忽略。  
2. **精准责任归因**：Issue 的第一责任人永远是 MR 提交者，升级链清晰可追溯。  
3. **全平台闭环**：从 Issue 产生 → 通知触达 → 处理闭环 → 超期升级 → 自动归档，全部在平台内完成。  
4. **复用现有资产**：完全不推翻现有的 `MemberMapping`、`WeComNotifier` 及企微机器人推送逻辑，在此基础上增强。

---

## 2. 核心原则

| 原则 | 说明 |
|------|------|
| **MR 原子归因** | `review_issue.owner_id` = `mr.author_id`（MR 提交者），与任务触发者、重试者无关。 |
| **单任务重试模型** | 重试复用同一 `task_id`，旧 Issue 软删除，新 Issue 覆盖。页面始终只呈现当前最新结果。 |
| **指纹状态继承** | 新 Issue 通过语义指纹匹配历史（含已软删除），自动继承 `false_positive` / `ignored`，避免重复打扰。 |
| **时间免疫重置** | 升级倒计时继承 Issue **首次发现时间**，不因任务重试而刷新，防止管理员通过重试钻空子。 |
| **站内信兜底** | 无论 IM 机器人是否送达，所有通知必须写入站内信；IM 仅作为增强渠道。 |
| **合并降噪** | 同一任务的多次重试在 15 分钟内合并为一条通知，防止刷屏。 |

---

## 3. 数据模型设计

### 3.1 新增字段（最小化变更）

```sql
-- review_issue：增加指纹与继承关系
ALTER TABLE review_issue ADD COLUMN fingerprint VARCHAR(64) NOT NULL DEFAULT '' COMMENT '语义指纹（路径+规则+代码签名）';
ALTER TABLE review_issue ADD COLUMN inherited_from_issue_id BIGINT UNSIGNED DEFAULT NULL COMMENT '继承自历史 issue id';
ALTER TABLE review_issue ADD INDEX idx_mr_fingerprint (mr_id, fingerprint);

-- review_task：增加执行次数
ALTER TABLE review_task ADD COLUMN execution_count INT NOT NULL DEFAULT 1 COMMENT '同一任务第几次运行（首次=1，重试累加）';

-- member_mapping：可选，增加启用状态（不破坏现有逻辑）
ALTER TABLE member_mapping ADD COLUMN enabled BOOL NOT NULL DEFAULT TRUE COMMENT '映射是否有效';

-- 新增：站内信/通知中心表
CREATE TABLE notification (
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id         BIGINT UNSIGNED NOT NULL COMMENT '接收人平台用户ID',
    type            VARCHAR(32) NOT NULL COMMENT 'task_completed / issue_escalation / deadline_digest / daily_digest',
    title           VARCHAR(200) NOT NULL,
    content         TEXT NOT NULL COMMENT 'Markdown 纯文本内容',
    link            VARCHAR(512) DEFAULT NULL COMMENT '点击跳转链接',
    related_task_id BIGINT UNSIGNED DEFAULT NULL,
    is_read         BOOL NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    read_at         TIMESTAMP NULL,
    INDEX idx_user_unread (user_id, is_read, created_at DESC),
    INDEX idx_task (related_task_id)
) ENGINE=InnoDB COMMENT='站内信/通知中心';

-- 新增：IM 投递日志（用于监控与兜底）
CREATE TABLE notification_delivery_log (
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    notifier_id     BIGINT UNSIGNED COMMENT '关联 wecom_notifier.id',
    task_id         BIGINT UNSIGNED COMMENT '关联任务ID',
    channel         VARCHAR(32) NOT NULL DEFAULT 'wecom' COMMENT 'wecom / dingtalk / lark',
    recipient_type  VARCHAR(32) COMMENT 'mr_author / steward / admin',
    message_preview VARCHAR(500) COMMENT '消息前 200 字',
    status          VARCHAR(20) NOT NULL COMMENT 'success / failed',
    error_msg       VARCHAR(512),
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_task (task_id),
    INDEX idx_status (status),
    INDEX idx_created (created_at)
) ENGINE=InnoDB COMMENT='通知渠道投递日志';
```

### 3.2 review_issue 状态机

| 状态 | 语义 | 出现在默认待办列表 |
|------|------|------------------|
| `pending` | 待处理（当前版本新产生，无历史匹配） | ✅ |
| `pending_inherited` | 待处理（历史为 `resolved`，需重新确认） | ✅ |
| `resolved` | 用户确认已修复 | ❌ |
| `false_positive` | 用户判定为误报 | ❌ |
| `ignored` | 用户主动忽略 | ❌ |
| `auto_filtered` | **系统自动过滤**（因历史为 false_positive/ignored） | ❌ |
| `auto_archived` | 超期自动归档（约 15 工作日） | ❌ |

---

## 4. 关键流程设计

### 4.1 任务重试 & Issue 生命周期（事务内原子操作）

```
任务 #100 状态变为 completed（首次或第 N 次重试）
    │
    ▼
数据库事务开始
    │
    ├── Step 1: execution_count += 1
    │
    ├── Step 2: 软删除该任务此前所有 "当前可见" Issue
    │   UPDATE review_issue SET deleted_at = NOW()
    │   WHERE task_id = 100 AND deleted_at IS NULL;
    │
    ├── Step 3: AI 产生新 Issue 列表 [X, Y, Z]，逐条处理：
    │   FOR each newIssue:
    │       a) 计算语义指纹 hash
    │       b) 在同一 mr_id 下检索历史（含 deleted_at IS NOT NULL）
    │          按指纹精确匹配，取最新一条记录
    │       c) 判定：
    │          • 匹配成功 + 历史 status ∈ (false_positive, ignored, auto_filtered)
    │            → 新 Issue status = 'auto_filtered'
    │          • 匹配成功 + 历史 status = 'resolved'
    │            → 新 Issue status = 'pending_inherited'
    │            → inherited_from_issue_id = 历史.id
    │            → created_at = 历史.created_at  **（关键：继承首次发现时间）**
    │          • 无匹配
    │            → 新 Issue status = 'pending'
    │            → created_at = NOW()
    │       d) 写入新记录（行号/列号/code_snippet 使用新值）
    │
    ├── Step 4: 更新 project 的 pending_count 统计
    │
    └── 事务提交
    │
    ▼
触发通知引擎（异步）
    ├── 写入 Redis 延迟队列：key="pending_notify:task:100", TTL=15min
    │   （首次运行 TTL 缩短为 2min）
    └── 队列到期后消费，生成并发送通知
```

### 4.2 语义指纹算法

```go
// 指纹 = SHA256( 文件路径 + "|" + 规则Code + "|" + 语义化代码签名 )
func ComputeFingerprint(issue ReviewIssue) string {
    // 1. 代码片段标准化
    snippet := normalizeCode(issue.CodeSnippet) // 去首尾空白、统一缩进、去行尾注释
    if len(snippet) > 150 {
        snippet = snippet[:150] // 截断，避免上下文扰动
    }
    signature := sha256Hex(snippet)
    
    // 2. 组合
    raw := fmt.Sprintf("%s|%s|%s", issue.FilePath, issue.RuleCode, signature)
    return sha256Hex(raw)
}

// 模糊匹配兜底（用于前端提示，不自动继承状态）
func IsLikelySameIssue(newIssue, oldIssue ReviewIssue) bool {
    if newIssue.FilePath != oldIssue.FilePath || newIssue.RuleCode != oldIssue.RuleCode {
        return false
    }
    similarity := levenshteinNormalized(newIssue.CodeSnippet, oldIssue.CodeSnippet)
    return similarity > 0.82
}
```

**关键设计**：
- 指纹**不包含行号**，行号变化不会导致指纹漂移。
- 匹配成功后，新 Issue 的 `file_path`、`line`、`column`、`code_snippet` 均使用本次最新值，确保开发者在页面看到的位置是准确的。

### 4.3 升级路由状态机（Escalation）

仅针对 **当前有效 pending Issue**（`deleted_at IS NULL AND status IN ('pending','pending_inherited')`）。

| 时间节点 | 动作 | 通知对象 | 渠道 | 站内信类型 |
|---------|------|---------|------|-----------|
| T+0 | 任务完成通知 | MR 提交者 | IM + 站内信 | `task_completed` |
| T+24h（1 工作日） | 首次提醒 | MR 提交者 | 站内信 | `issue_escalation` |
| T+72h（3 工作日） | 强提醒 + 抄送 | MR 提交者 + 项目环节负责人 | IM + 站内信 | `issue_escalation` |
| T+120h（5 工作日） | **正式升级** | 项目环节负责人（current_owner 变更）| IM + 站内信 | `issue_escalation` |
| T+240h（10 工作日） | 管理员介入 | 系统管理员 | 站内信 | `issue_escalation` |
| T+360h（15 工作日）| **自动归档** | 原提交者 + 负责人 + 管理员 | 站内信 | `auto_archived` |

**关键规则**：
- 所有时间阈值按**工作日小时**计算，周末与法定假日不计时。
- 节假日列表由管理员后台配置。
- Issue 在升级途中被处理（status 变为 resolved/false_positive/ignored），立即取消未来所有待执行的升级通知。
- 批量优化：同一开发者的多条 Issue 同时达到同一阈值时，合并为单条通知（"你有 5 条 Issue 已超期 3 天"）。

### 4.4 通知触发事件清单

| 事件 ID | 触发条件 | 默认收件人 | IM 群推送 |
|---------|---------|-----------|----------|
| `task.completed` | 任务完成且 `issue.total > 0` | MR 提交者 | ✅（延迟合并后） |
| `task.completed.no_issue` | 任务完成且 `issue.total = 0` | MR 提交者 | ✅（可选开启） |
| `task.failed` | 任务执行失败 | 任务触发者 + 管理员 | ✅ |
| `issue.pending.24h` | Pending 超 24 工作日小时 | MR 提交者 | ❌ |
| `issue.pending.72h` | Pending 超 72 工作日小时 | MR 提交者 + 抄送负责人 | ✅ |
| `issue.escalation.120h` | Pending 超 120 工作日小时 | 项目环节负责人（正式升级） | ✅ |
| `issue.escalation.240h` | Pending 超 240 工作日小时 | 系统管理员 | ❌ |
| `issue.auto_archived` | 超 360 工作日小时自动归档 | 原提交者 + 负责人 + 管理员 | ❌ |
| `project.batch_alert` | 项目未处理 Issue 总数超过阈值（如 50） | 项目环节负责人 + 管理员 | ✅ |
| `daily.digest` | 每日 09:00（仅工作日） | 所有有 pending Issue 的开发者 | ❌ |

---

## 5. 通知模板引擎

### 5.1 配置层级

```
全局默认规则（所有项目继承）
  └── 项目级规则（覆盖/新增/禁用继承）
        └── 规则维度：可指定 issue.severity / issue.rule_category 做子规则
```

### 5.2 规则结构

```json
{
  "name": "任务完成-通知开发者",
  "trigger": "task.completed",
  "condition": {
    "operator": "AND",
    "conditions": [
      { "field": "task.status", "op": "eq", "value": "completed" },
      { "field": "issue.pending_count", "op": "gt", "value": 0 }
    ]
  },
  "actions": [
    {
      "recipient_type": "issue_owner",
      "channel": "inbox",
      "template_id": "task_completed_developer",
      "priority": "normal"
    }
  ],
  "delay_minutes": 0,
  "quiet_hours": { "start": "22:00", "end": "09:00" },
  "dedup_window_minutes": 15,
  "enabled": true
}
```

### 5.3 全量模板变量（后端统一上下文）

| 变量名 | 来源 | 说明 |
|--------|------|------|
| `{{PROJECT_NAME}}` | task.Project.Name | 项目名称 |
| `{{TASK_ID}}` | task.ID | 任务 ID |
| `{{EXECUTION_COUNT}}` | task.ExecutionCount | 第几次运行 |
| `{{MR_IID}}` | task.MRIID | MR 编号 |
| `{{MR_TITLE}}` | task.MRTitle | MR 标题 |
| `{{MR_AUTHOR}}` | task.MRAuthor | MR 提交者 Git 用户名 |
| `{{DEVELOPER}}` | task.MRAuthor (+ DisplayName) | 拼接展示名 |
| `{{BRANCH}}` | task.SourceBranch + " -> " + task.TargetBranch | 分支 |
| `{{SCORE}}` | task.ScoreValue | 评审分数 |
| `{{CHANGES}}` | +additions/-deletions | 代码变更量 |
| `{{MR_URL}}` | task.MRURL | MR 链接 |
| `{{ISSUE_TOTAL}}` | calcIssueStats | 本次发现总数 |
| `{{ISSUE_PENDING}}` | calcIssueStats | 当前待处理数（已减 auto_filtered） |
| `{{ISSUE_CRITICAL}}` | calcIssueStats | 严重数 |
| `{{ISSUE_HIGH}}` | calcIssueStats | 高危数 |
| `{{ISSUE_MEDIUM}}` | calcIssueStats | 中危数 |
| `{{ISSUE_LOW}}` | calcIssueStats | 低危数 |
| `{{ISSUE_AUTO_FILTERED}}` | calcIssueStats | 自动过滤数 |
| `{{ISSUE_GONE}}` | calcIssueStats | 上次有本次未复现数 |
| `{{ISSUE_NEW}}` | calcIssueStats | 相较上次全新 Issue 数 |
| `{{STEWARD_NAME}}` | project steward | 项目环节负责人名 |
| `{{DEADLINE_HOURS}}` | 全局配置 | 距离下次升级的剩余工作小时 |
| `{{AT_RECIPIENT}}` | MemberMapping 解析 | 平台根据收件人自动替换为 IM @ 语法 |

### 5.4 企微 Markdown 消息模板示例（配置到 WeComNotifier 表）

```markdown
**🔍 AI 代码评审任务完成**

**项目：** {{PROJECT_NAME}}
**MR：** !{{MR_IID}} {{MR_TITLE}}
**提交者：** {{DEVELOPER}}
**运行次数：** 第 {{EXECUTION_COUNT}} 次

**📊 Issue 统计**
本次发现：{{ISSUE_TOTAL}} 条
{{#if ISSUE_CRITICAL}}🔴 严重：{{ISSUE_CRITICAL}} 条{{/if}}
{{#if ISSUE_HIGH}}🟡 高危：{{ISSUE_HIGH}} 条{{/if}}
{{#if ISSUE_MEDIUM}}🟢 中危：{{ISSUE_MEDIUM}} 条{{/if}}

{{#if ISSUE_AUTO_FILTERED}}
🔍 智能过滤：{{ISSUE_AUTO_FILTERED}} 条历史已判定问题已自动过滤，无需处理
{{/if}}
{{#if ISSUE_GONE}}
🧹 已清除：{{ISSUE_GONE}} 条上次问题本次未复现
{{/if}}

**📋 当前待处理：{{ISSUE_PENDING}} 条**
⏰ 请在 2 个工作日内处理，超期将通知项目负责人

**查看完整报告：** {{MR_URL}}

{{AT_RECIPIENT}}
```

---

## 6. 项目环节负责人体系

### 6.1 配置表模型

```sql
CREATE TABLE project_steward (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    project_id  BIGINT UNSIGNED NOT NULL COMMENT '关联项目',
    user_id     BIGINT UNSIGNED NOT NULL COMMENT '平台用户ID（负责人）',
    role_type   VARCHAR(32) NOT NULL DEFAULT 'default' COMMENT 'default / security / performance / golang / java / ...',
    priority    INT NOT NULL DEFAULT 0 COMMENT '同类型下优先级（越小越优先）',
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_project_role_user (project_id, role_type, user_id),
    INDEX idx_project (project_id)
) ENGINE=InnoDB COMMENT='项目环节负责人';
```

### 6.2 解析优先级

当需要通知"某 Issue 相关的负责人"时：

1. **规则类型优先**：根据 `issue.rule_category`（如 security）匹配 `role_type = 'security'` 的负责人。
2. **语言维度次之**：如果规则类型未匹配，按 `issue.language` 匹配（如 `role_type = 'golang'`）。
3. **默认兜底**：最后匹配 `role_type = 'default'` 的负责人。
4. **同类型多人**：全部通知（站内信发所有人，IM 群 @所有人）。

### 6.3 后台管理 UI

- 路径：`后台管理 → 项目管理 → 选择项目 → 环节负责人配置`
- 支持添加/删除/修改负责人，选择角色类型。
- 展示当前项目的映射完成度（哪些 Git 用户还没有 IM 映射）。

---

## 7. 站内信中心（Inbox）

### 7.1 功能规格

- **导航栏常驻徽标**：铃铛图标 + 数字红点（未读数 > 0 时展示）。
- **Dropdown 浮层**：hover/click 铃铛，下拉展示最近 5 条消息（标题 + 时间），底部「查看全部」跳转站内信页。
- **站内信列表页**：
  - 左侧分类：全部 / 未读 / 任务 / 升级 / 日报 / 系统
  - 右侧消息卡片：标题 + 摘要 + 时间 + 已读/未读状态
  - 快捷操作：标记已读 / 标记全部已读 / 删除 30 天前已读
- **消息详情**：点击后跳转消息中的 `link`，同时自动标记为已读。

### 7.2 消息分类标签

| 类型 | 颜色 | 示例场景 |
|------|------|---------|
| `task` | 蓝色 | 任务完成通知 |
| `escalation` | 橙色 | 超期升级提醒 |
| `deadline` | 红色 | 即将归档警告 |
| `digest` | 灰色 | 日/周报摘要 |
| `system` | 紫色 | 系统级（规则退化、配置失效） |

### 7.3 关键 API

```
GET    /api/notifications          // 列表，支持 ?type=&is_read=&page=
POST   /api/notifications/:id/read // 单条已读
POST   /api/notifications/read-all // 全部已读
GET    /api/notifications/unread-count // 导航栏徽标数字
```

---

## 8. 开发者工作台（Developer Dashboard）

开发者角色登录后的**默认首页**。

### 8.1 页面布局

```
┌─────────────────────────────────────────────────────────┐
│  👤 我的工作台（开发者视角）                               │
├─────────────────────────┬───────────────────────────────┤
│  📋 待处理 Issue（8）    │  📊 本周统计                 │
│  ├── 🔴 严重：1         │  ├── 收到 Issue：15 条        │
│  ├── 🟡 高危：2         │  ├── 已闭环：10 条            │
│  ├── 🟢 中危：3         │  ├── 待处理：5 条             │
│  └── ⚪ 低危：2         │  └── 平均闭环：1.8 天         │
│                         │                               │
│  [一键前往处理]          │  ⏰ 最早未处理距今：3 天       │
│                         │  🏅 团队排名：前 20%          │
├─────────────────────────────────────────────────────────┤
│  📋 待处理 Issue 列表（按超期风险排序）                    │
│  ┌─────────────────────────────────────────────────┐   │
│  │ ⚠️ 🔴 opencode-prod  MR!456  空指针解引用      │   │
│  │    规则：nil-guard | 距今：3 天 | [标记已处理]   │   │
│  │                                                 │   │
│  │ 🟡 opencode-prod  MR!460  错误未处理           │   │
│  │    规则：error-handling | 距今：1 天 | [标记误报]│   │
│  │                                                 │   │
│  │ ...                                             │   │
│  └─────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────┤
│  📈 我的质量趋势（近 30 天）                              │
│  [折线图：每周收到 / 闭环 / 待处理]                       │
└─────────────────────────────────────────────────────────┘
```

### 8.2 排序逻辑

默认按**超期风险**排序，而非时间：
1. 距离下次升级 < 12 小时的置顶
2. critical > high > medium > low
3. 同级别按 created_at 升序（越早的越靠前）

### 8.3 行内快捷操作

每条 Issue 右侧直接提供按钮：
- **✅ 已处理**：标记 resolved
- **❌ 误报**：标记 false_positive（下次重试自动过滤）
- **⏸️ 忽略**：标记 ignored

无需进入详情页即可完成 triage。

### 8.4 关键 API

```
GET /api/dashboard/developer       // 工作台聚合数据（待处理统计、本周数据、趋势）
GET /api/dashboard/developer/issues // 待处理 Issue 列表（支持排序、过滤）
```

---

## 9. 项目级 & 管理员治理视图

### 9.1 项目负责人工作台

```
项目：opencode-prod

质量健康度：72 分（🟡）
├── 当前未处理：23 条
├── 超期未处理：4 条（🔴 需关注）
└── 今日新增：5 条

待督促人员：
├── 张三：8 条（3 条超期）→ [批量催促]
├── 李四：6 条（1 条超期）
└── 王五：9 条（全部正常）

[查看项目报告] [导出 Excel]
```

### 9.2 管理员全局大盘

```
全平台概览
├── 今日新增 Issue：127 条
├── 当前待处理：342 条
├── 超期未处理：45 条（🔴）
├── 7 天闭环率：78%
├── 闭环速度中位数：1.2 天
└── 警报：
    ├── 项目 payment-service 积压 89 条 ⚠️
    ├── 项目 opencode-prod 的 IM 推送失败 2 天 ⚠️
    └── 规则 nil-guard 误触发率过高 ⚠️
```

---

## 10. IM 机器人集成（复用与增强）

### 10.1 复用已有能力清单

| 已有能力 | 使用方式 |
|---------|---------|
| `MemberMapping`（git_username → wecom UserID） | 通知引擎 `RecipientResolver` 直接查询，用于 IM @ |
| `WeComNotifier`（Webhook + MessageTemplate + ProjectID） | 作为 `ChannelProvider` 接入通知引擎 |
| `NotifyAIReviewCompleted` 发送链路 | 保留 `buildReviewMessage` + `SendMessage`，扩展变量 |
| `settings.html / member-mappings.html` 前端 CRUD | 零改动复用，仅增加 `enabled` 字段和测试推送按钮 |

### 10.2 增强点

1. **延迟合并队列**
   - 任务完成时写入 Redis：`SET pending_notify:task:{id} {json} EX 900`（15min）
   - 重试时覆盖同名 key。
   - TTL 到期后消费，调用通知引擎。
   - 首次运行 TTL = 120s。

2. **模板变量扩展**
   - `buildReviewMessage` 增加 `calcIssueStats` 调用，注入 `ISSUE_*` 系列变量。

3. **非工作时间降级**
   - 22:00 - 09:00 期间，IM 消息中的 `{{AT_RECIPIENT}}` 解析为纯文本（不 @），避免强提醒打扰。
   - 紧急升级（超期 5 天以上）无视静默时段。

4. **投递失败监控**
   - `SendMessage` 后写入 `notification_delivery_log`。
   - 管理员首页展示失败告警：「⚠️ 项目 X 的企微推送失败（最后成功：2 天前）」。

5. **测试发送按钮**
   - 通知模板配置页增加「测试发送」：用最近一条真实任务数据渲染，调用 Webhook 发送。

---

## 11. 关键边界情况与兜底策略

| 场景 | 处理策略 |
|------|---------|
| **任务 #101 执行失败** | 不软删除旧 Issue，旧 Issue 保持原状；通知：「任务重试失败，保留上次评审结果」 |
| **重试后 0 Issue** | 软删除所有旧 Issue；通知：「本次评审未发现问题，历史 Issue 已清除」 |
| **重试后全为历史误报** | 全部标记 `auto_filtered`；通知：「本次未发现新问题，历史误报已自动过滤」 |
| **历史 resolved 问题复现** | 新 Issue 标记 `pending_inherited`，通知中提示「曾修复的问题复现，请重新确认」 |
| **MR 提交者未配置 IM 映射** | IM 消息不 @任何人，纯文本展示「提交者：张三」；站内信正常送达 |
| **项目未配置环节负责人** | 跳过项目负责人升级环节，超期后直接通知管理员 |
| **MR 提交者已离职/禁用** | `owner_id` 保留用于审计；`current_owner` 立即设为项目默认负责人；通知转给负责人 |
| **节假日/周末** | 升级计时器暂停，工作日 09:00 恢复 |
| **Issue 在升级途中被处理** | 取消所有未来待执行的升级通知；站内信标记为已读可选 |
| **管理员疯狂重试 5 次 / 15min** | 延迟队列合并为一条通知 |
| **IM Webhook 失效/被禁言** | 站内信不受影响；管理员后台告警；日志记录失败原因 |
| **代码片段标准化后仍不同（变量重命名）** | 精确匹配失败 → 进入模糊匹配。相似度 > 82% 视为新 pending，UI 提示「疑似历史已判定问题」 |

---

## 12. 后端接口清单

### 12.1 通知中心

```
GET    /api/notifications
GET    /api/notifications/unread-count
POST   /api/notifications/:id/read
POST   /api/notifications/read-all
```

### 12.2 开发者工作台

```
GET    /api/dashboard/developer
GET    /api/dashboard/developer/issues?sort=risk&severity=
```

### 12.3 项目治理视图

```
GET    /api/dashboard/project/:project_id
GET    /api/dashboard/admin/global
```

### 12.4 项目环节负责人（后台管理）

```
GET    /api/admin/projects/:project_id/stewards
POST   /api/admin/projects/:project_id/stewards
PUT    /api/admin/stewards/:id
DELETE /api/admin/stewards/:id
```

### 12.5 通知规则模板（后台管理）

```
GET    /api/admin/notification-rules
POST   /api/admin/notification-rules
PUT    /api/admin/notification-rules/:id
DELETE /api/admin/notification-rules/:id
POST   /api/admin/notification-rules/:id/test   // 测试发送
```

### 12.6 现有接口复用（零改动或轻量增强）

```
// 已有接口，复用
GET    /api/member-mappings
POST   /api/member-mappings
PUT    /api/member-mappings/:id
DELETE /api/member-mappings/:id

// 已有 WeComNotifier 管理接口，复用
GET    /api/admin/notifiers
POST   /api/admin/notifiers
...
```

---

## 13. 非功能性要求

1. **性能**：
   - 指纹匹配查询必须命中 `idx_mr_fingerprint`（MR 维度通常 Issue 量级 < 1000，性能无压力）。
   - .delay 队列使用 Redis，不依赖数据库轮询。
   - 升级路由定时任务每小时执行一次，避免过度扫描。

2. **可靠性**：
   - 任务完成时的"软删除 + 插入 + 指纹匹配"必须在**同一个数据库事务**内完成。
   - 通知发送失败（IM）不影响主流程，仅记录日志。

3. **扩展性**：
   - `IMPlatform` 枚举预留多平台；`channel` 字段使用字符串而非枚举，便于未来新增 IM 渠道。
   - `notification_rule.condition` 使用 JSON，支持后续增加可视化规则编排而不改表结构。

4. **可观测性**：
   - 所有通知发送动作（成功/失败）写入 `notification_delivery_log`。
   - 管理员后台提供「通知投递成功率」仪表盘。

---

## 14. 与孵化台的边界声明

- 本方案**完全不涉及规则孵化台**的提醒逻辑。
- 孵化台作为管理员专用功能，其通知（如候选规则生成）如需提醒，单独设计，不混入本通知引擎。
- `review_issue` 表中的 Issue 仅指**代码评审任务**产生的 Issue，不包含孵化台 Issue。

---

## 15. 实施检查清单（Checklist）

- [ ] 数据模型变更 SQL 执行（review_issue + review_task + member_mapping enabled + 新表）
- [ ] `ComputeFingerprint` 算法实现 + 单元测试
- [ ] 任务完成事务内逻辑改造（软删除 + 插入 + 指纹匹配 + 状态继承）
- [ ] `calcIssueStats` 统计函数实现
- [ ] `buildReviewMessage` 模板变量扩展
- [ ] 延迟合并队列（Redis TTL）实现
- [ ] 站内信中心后端 API + 前端页面
- [ ] 开发者工作台后端 API + 前端页面（默认首页）
- [ ] 项目环节负责人配置后台页面
- [ ] 通知规则模板管理后台页面
- [ ] 升级路由定时任务（Go Cron / Linux Cron）
- [ ] 工作日/节假日工具函数 + 后台配置
- [ ] IM 投递日志与失败监控
- [ ] 全量回归测试（任务重试场景、指纹匹配场景、升级场景）

---

*文档版本：v1.0*  
*基于历史讨论结论整理，涵盖 Issue 治理、通知引擎、责任人体系、指纹继承、IM 集成等全部模块。*
