# CodeGuard - 功能清单

> 版本: v4.6
> 更新日期: 2026-07-24
> 适用范围: Go 后端 + Web 前端全栈开发

---

## 目录

1. [项目概述](#1-项目概述)
2. [首页统计仪表板](#2-首页统计仪表板)
   - [工作台 Dashboard](#工作台-dashboard)
3. [MR 统计](#3-mr-统计)
4. [MR 审查记录](#4-mr-审查记录)
5. [用户与认证管理](#5-用户与认证管理)
6. [项目管理](#6-项目管理)
7. [任务管理](#7-任务管理)
8. [AI 实时对话](#8-ai-实时对话)
9. [任务资源池](#9-任务资源池)
10. [大模型管理](#10-大模型管理)
11. [项目模板管理](#11-项目模板管理)
12. [企业微信通知](#12-企业微信通知)
13. [邮件通知](#13-邮件通知)
14. [报告管理](#14-报告管理)
15. [系统管理](#15-系统管理)
16. [数据洞察：Token 用量监控](#16-数据洞察token-用量监控)
17. [数据洞察：规则命中统计](#17-数据洞察规则命中统计)
18. [规则孵化台](#18-规则孵化台)
19. [前端体验：主题与导航](#19-前端体验主题与导航)
20. [数据模型定义](#20-数据模型定义)
21. [核心业务流程](#21-核心业务流程)
22. [接口清单](#22-接口清单)
23. [技术栈](#23-技术栈)
24. [附录：认证与鉴权](#24-附录认证与鉴权)

---

## 1. 项目概述

CodeGuard（代码智能门禁）是一个通过 GitLab Webhook 触发 AI 代码审查的平台。用户在 GitLab MR 上评论 `@AI` 命令，或 AI 审查分数低于阈值时，系统自动创建任务并调用 AI 服务执行代码审查。

### 核心特性

- **Webhook 触发**: GitLab Note Hook 接收 @AI 命令
- **MR 自动评审**: 接收 GitLab MR Webhook，直接使用大模型进行分批评审、自动评分
- **评分阈值触发**: AI 审查分数低于设定阈值时自动触发深度审查
- **AI 实时对话**: 任务执行中支持与 AI 多轮实时流式对话
- **任务类型区分**: `@AI` → Chat 任务，MR 代码合并 → AI 评审任务
- **模板选择**: Chat 使用系统 AI 日志模板，评分阈值触发使用代码审查模板，AI 评审使用项目模板
- **资源池管理**: 管理 OpenCode 服务连接，支持多资源池，支持查看资源池技能详情与完整配置弹窗
- **大模型管理**: 独立配置 LLM 提供商（OpenAI、vLLM 等），**支持主模型/备用模型角色切换与自动故障切换**，支持健康检查守护进程
- **MR 统计**: 独立的 MR 聚合统计页面，展示筛选范围内的 MR 总数、状态分布、平均评分、低质量 MR 数等
- **MR 审查记录**: 记录并展示 AI 评审产生的 MR 数据，支持卡片展示、评分聚合、Draft 状态管理
- **统计仪表板**: 全局数据统计页面（KPI、趋势图、雷达图、项目活跃度 TOP10、开发者全量排行）
- **报告管理**: 自动生成并发送周报/月报邮件，支持 Outlook 2016 兼容的纯表格 HTML 模板
- **邮件通知**: 独立的 SMTP 配置与收件人管理，支持快速启用/禁用
- **企业微信通知**: 多场景通知（任务成功/失败/超时/资源池异常）
- **敏感数据加密**: AES-256-GCM 加密存储
- **用户认证**: Token 持久化鉴权，支持密码修改（带可见性切换）；支持 GitLab OAuth 登录
- **全局鉴权拦截**: 前端 `apiFetch` / `fetch` 自动注入 `Authorization` Header，URL 查询 `token` 优先覆盖
- **Diff 截断保护**: 超过阈值的大代码块在入库前自动截断，防止 DB 写入失败
- **MR 状态同步**: 定时轮询刷新 GitLab opened MR 的合并/关闭状态
- **监控告警**: 资源池/大模型异常持续达阈值后自动发送企业微信告警，恢复后发送恢复通知
- **OpenCode 版本采集**: 健康检查时自动解析并保存 OpenCode 服务端版本号
- **人工复核与重试**: 支持对 AI 评审结果添加复核意见，重试时可选历史意见注入（倒序排列、默认全选、可折叠）
- **任务并发调度**: 同一项目深度评审（OpenCode）串行执行，AI 评审（LLM）可并发执行，互不阻塞
- **队列饥饿防护**: AI 评审失败或停止后自动唤起同项目 pending 深度评审任务，避免永久饿死
- **结构化 Issue 评审**: AI 评审结果按规则拆分为结构化 Issue 列表，支持单条/批量采纳、拒绝、状态筛选
- **评审规则库**: 内置通用/Go/Python/前端/Java 多语言规则库，支持项目级启用/禁用配置与严重程度覆盖
- **规则批量操作**: 项目详情页支持按分类/严重程度/语言筛选、批量启用/禁用、自定义每页条数
- **GitLab 评论模板**: AI 审核模版支持自定义 `text/template` 格式，实时预览渲染效果
- **SMTP 协议兼容**: 自定义 SMTP 客户端支持 AUTH LOGIN + STARTTLS，DATA 后完整读取响应码
- **Token 用量全链路监控**: 全量 LLM 调用按行记录（prompt/completion/cached tokens + 耗时 + 状态），后台异步落库；首页 + 独立监控页 + 周月报多视角展示
- **Token 用量多维聚合**: 按时间（today/7d/30d，含当天小时粒度）、模型、项目、作者、任务聚合；调用明细支持分页、最近优先
- **规则命中统计**: 独立的规则命中率、修复率、误报率、待处理率统计页面；支持单规则钻取（含趋势、项目/作者 TOP、最近命中分页 + 代码片段）
- **LLM HTTP 指数退避重试**: 502/503/504 + 网络层瞬时错误 + JSON 解析失败自动指数退避重试；最大次数/初始延迟/退避倍率/最大延迟全部 Web 可配置且立即生效
- **系统参数热加载**: `max_diff_files` / `max_tokens_per_batch` / LLM 重试参数等全部支持 Web 配置后立即生效（TTL 5min + cron 1min 主动刷新）
- **任务详情新布局**: 主信息卡 + 量化数据（时长/文件数/+N/-N）整合到一张卡片；综合评分徽章移至 MR 链接后（颜色编码：优/良/中/差）；Tab 按需加载（diff/报告/对话/Token）
- **暗色/亮色主题系统**: 三态主题切换（light/dark/system）；CSS 变量设计系统；localStorage 持久化；跟随系统偏好；自定义事件 `themechange` 跨页广播
- **菜单角色差异化**: 6 个折叠组（首页/任务/数据洞察/AI评审/通知/系统）按 admin/user 角色差异化；活跃组自动展开；折叠状态持久化
- **check_error 字段自适应**: 字段从 VARCHAR(512) 升级为 TEXT；service 层防御性截断到 4000 rune，避免 LiteLLM 错误链过长导致写入失败

---

## 2. 首页统计仪表板

`statistics.html` 为系统根页面（`/`）。

### 2.1 KPI 指标卡片

| 指标 | 说明 | 实现状态 |
|------|------|----------|
| MR 审查总数 | 筛选范围内的 MR 记录总数 | ✅ |
| 平均评分 | 筛选范围内所有 MR 的平均评分 | ✅ |
| 低质量 MR | 筛选范围内评分低于阈值（默认 60）的 MR 数量 | ✅ |
| 活跃项目数 | 筛选范围内有 MR 记录的项目数 | ✅ |
| 代码变更量 | 筛选范围内 Additions + Deletions 总和（短格式如 23.7K） | ✅ |
| 审查次数 | 筛选范围内 review_count 总和 | ✅ |
| 任务数 | 筛选范围内关联的任务总数 | ✅ |
| MR 状态分布 | Merged / Opened / Closed 的数量与占比 | ✅ |
| 项目总数 | 全量 projects 表记录数（不受筛选影响） | ✅ |

### 2.2 趋势图表

| 图表 | 说明 | 实现状态 |
|------|------|----------|
| 近 7/30 日 MR 趋势 | 折线图展示每日新增 MR 数量 | ✅ |
| 活跃开发者趋势 | 折线图展示每日活跃开发者数 | ✅ |

### 2.3 项目雷达图

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 雷达图展示 | 多维度展示各项目指标数据 | ✅ |
| 项目选择弹窗 | 点击选择项目，弹出面板支持关键词搜索与标签展示 | ✅ |

### 2.4 项目活跃度与开发者排行

| 模块 | 说明 | 实现状态 |
|------|------|----------|
| 项目活跃度 TOP10 | 按 MR 数量排序的柱状图 | ✅ |
| 开发者数据排行（全量） | 按 MR 数量排序的横向柱状图，展示所有开发者数据（不再限制 TOP10） | ✅ |

### 2.5 筛选条件

| 条件 | 说明 | 实现状态 |
|------|------|----------|
| 时间范围 | 今天 / 最近 7 天 / 最近 30 天 / 本月 / 本年 / 自定义 | ✅ |
| 项目筛选 | 多选，仅显示有 MR 记录的项目 | ✅ |
| 开发者筛选 | 多选，仅显示有 MR 记录的作者 | ✅ |
| 状态筛选 | All / Opened / Merged / Draft | ✅ |

---

## 工作台 Dashboard

面向不同角色的专属工作台，支持活跃/历史遗留数据分离展示。

### 3.1 数据活跃基准时间

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 基准时间配置 | `settings.html` 系统配置 Tab 内「数据活跃基准时间」卡片，独立保存至 `notification_global_settings` 表 | ✅ |
| 配置入口迁移 | 原 `notification-rules.html` 中的 baseline 面板已删除，统一迁移至系统配置 | ✅ |
| 影响预览 | 保存前可调 `GET /notification-global-settings/preview?baseline=` 预览影响范围 | ✅ |
| 清除操作 | 支持清除基准时间，清除后所有数据视为活跃 | ✅ |
| 操作人追溯 | 记录最后修改人 `baseline_set_by`，API 返回时 `display_name` 为空则 fallback 到 `username` | ✅ |

### 3.2 开发者工作台

`developer-dashboard.html` 为开发者角色默认首页。

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| Severity 四宫格 | 严重/高危/中危/低危，主数字展示**活跃数**；存在历史遗留时 inline 展示 `(历史遗留 N)` | ✅ |
| 本周统计 | 收到/已闭环/待处理/平均闭环；其中「待处理」展示**活跃数** | ✅ |
| 最早未处理距今 | 查询**活跃 Issue** 中最早的一条 | ✅ |
| 团队排名 | 基于近30天闭环率，**不区分**活跃/遗留 | ✅ |
| 质量趋势（近4周） | 每周收到/闭环/待处理，统计**全量数据** | ✅ |
| Toggle 开关 | 「展示历史遗留问题」仅控制 Issue 列表；默认关闭（只展示活跃） | ✅ |
| Issue 列表 | 默认只展示活跃 Issue；开启后展示全部，历史遗留 Issue 显示灰色徽章 | ✅ |
| 行内快捷操作 | 已处理/误报/忽略，无需进入详情 | ✅ |
| 无基准提示 | 未设置基准时间时，底部提示条引导前往系统配置 | ✅ |

### 3.3 项目负责人工作台

`project-dashboard.html` 展示项目维度的质量概览。

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 质量健康度 | 基于活跃 pending + 超期计算 | ✅ |
| 核心指标横排 | 待处理/严重/高危/7天闭环率/平均闭环；待处理/严重/高危 inline 展示历史遗留数 | ✅ |
| 超期 badge | 同时展示活跃超期数和历史遗留超期数 | ✅ |
| 历史遗留总览 | 可折叠面板，展示该项目历史遗留总数 | ✅ |

### 3.4 管理员全局大盘

`admin-dashboard.html` 全局视角。

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| KPI 卡片 | 今日新增/总待处理/超期/今日归档/今日升级；总待处理和超期 inline 展示历史遗留数 | ✅ |
| Issue 状态分布 | 饼图展示 pending / pending_inherited / resolved / false_positive / ignored / auto_filtered | ✅ |
| 积压告警 | 高 pending 项目列表，inline 展示历史遗留数 | ✅ |
| 待处理 TOP10 项目 | 按活跃 pending 排序，inline 展示历史遗留数 | ✅ |
| 历史遗留概览 | 可折叠面板，显示历史遗留总数/涉及项目/最早距今/severity 分布 | ✅ |
| 效率指标 | 7天闭环率、闭环速度中位数；**不区分**活跃/遗留 | ✅ |

---

## 4. MR 统计

`mr-stats.html` 为独立的 MR 聚合统计页面。

### 3.1 统计卡片

| 指标 | 说明 | 实现状态 |
|------|------|----------|
| MR 总数 | 筛选范围内 MR 总数，括号内展示 merged/opened/closed 三色状态分布 | ✅ |
| 平均评分 | 筛选范围内平均评分 | ✅ |
| 低质量 MR | 评分低于阈值的 MR 数量 | ✅ |
| 活跃项目数 | 有 MR 记录的项目数 | ✅ |
| 代码变更量 | Additions + Deletions 总和 | ✅ |
| 审查次数 | review_count 总和 | ✅ |
| 任务数 | 关联任务总数 | ✅ |
| 项目总数 | 全量项目数（不受筛选影响） | ✅ |

### 3.2 筛选条件

| 条件 | 说明 | 实现状态 |
|------|------|----------|
| 时间范围 | 今天 / 最近 7 天 / 最近 30 天 / 本月 / 本年 / 自定义 | ✅ |
| 项目筛选 | 仅显示有 MR 记录的项目 | ✅ |
| 作者筛选 | 仅显示有 MR 记录的作者 | ✅ |
| MR 状态筛选 | All / Opened / Merged / Closed | ✅ |

### 3.3 特性

- 统计卡片跟随筛选条件实时联动
- 状态分布展示格式：`总数（merged / opened / closed）`
- 筛选后 opened/closed 状态卡片显示 0（因为它们不在当前筛选结果中）

---

## 5. MR 审查记录

### 5.1 记录列表（卡片视图）

#### 卡片字段

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| MR 标题 | GitLab MR 原始标题 | ✅ |
| 分支信息 | source_branch → target_branch | ✅ |
| 状态标签 | merged（绿色）/ opened（蓝色）/ closed（灰色）/ draft（琥珀色） | ✅ |
| 项目名 | 所属 GitLab 项目 | ✅ |
| 作者 | MR 作者用户名 | ✅ |
| 创建时间 | GitLab MR 原始创建时间 | ✅ |
| 更新时间 | 最后更新时间 | ✅ |
| Commit SHA | 最后一次的 Commit ID（前 8 位） | ✅ |
| 最新评分 | 评分 | ✅ |
| 评分历史 | 显示所有历史评分的 sparkline 趋势 | ✅ |
| 审查次数 | 该 MR 被审查的总次数 | ✅ |
| 关联任务 | 显示关联的 Task ID 列表 | ✅ |

#### 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 查看详情 | 弹窗展示 MR 详细信息（diff、历史评分、任务关联） | ✅ |
| 标记 Draft | 调用 GitLab API 在标题前添加 `Draft:` 前缀 | ✅ |
| 标记 Ready | 调用 GitLab API 移除标题前的 `Draft:` 前缀 | ✅ |
| 统一分页 | 支持页码跳转及 10/20/50 条/页选择 | ✅ |

#### 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 查看详情 | 弹窗展示 MR 详细信息（diff、历史评分、任务关联） | ✅ |
| 标记 Draft | 调用 GitLab API 在标题前添加 `Draft:` 前缀 | ✅ |
| 标记 Ready | 调用 GitLab API 移除标题前的 `Draft:` 前缀 | ✅ |
| 统一分页 | 支持页码跳转及 10/20/50 条/页选择 | ✅ |

### 5.2 详情弹窗

| 信息 | 说明 | 实现状态 |
|------|------|----------|
| 基本信息 | 项目、作者、分支、状态、Draft 状态 | ✅ |
| 评分卡片 | 最新评分 + sparkline 趋势图 | ✅ |
| 评分历史 | 所有历史评分的表格列表 | ✅ |
| Commit 列表 | 该 MR 的所有 Commit SHA | ✅ |
| 关联任务 | 跳转任务详情 | ✅ |
| 系统信息 | 更新时间、Commit ID | ✅ |

### 5.4 筛选/搜索

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 关键词搜索 | MR 标题、作者、项目名模糊搜索 | ✅ |
| 项目筛选 | 下拉选择 | ✅ |
| 作者筛选 | 下拉选择 | ✅ |
| 时间范围 | 今天 / 最近 7 天 / 最近 30 天 / 本月 / 本年 / 自定义 | ✅ |
| 状态筛选 | All / Opened / Merged / Closed（与 Draft 过滤独立） | ✅ |
| 分页 | 统一分页组件，支持 10/20/50 条/页 | ✅ |
| 汇总统计 | 顶部显示当前筛选条件下的聚合数据 | ✅ |

---

## 6. 用户与认证管理

### 5.1 用户登录

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 账号密码登录 | bcrypt 哈希验证，返回 JWT Token | ✅ |
| GitLab OAuth 登录 | OAuth2 授权码模式，支持自动创建/绑定用户 | ✅ |
| Token 持久化 | Token 存入数据库，有效期 7 天 | ✅ |
| 自动跳转 | 登录后跳转到首页（统计仪表板） | ✅ |

### 5.2 用户登出

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 注销 Token | 从数据库删除 Token，防止重用 | ✅ |
| 清除本地存储 | 清除 localStorage 中的 token 和用户信息 | ✅ |

### 5.3 修改密码

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 旧密码验证 | 需提供原密码 | ✅ |
| 密码强度校验 | 最少 6 位 | ✅ |
| 新密码确认 | 需两次输入一致 | ✅ |
| 密码可见性切换 | 每个输入框右侧 👁 图标，点击切换 text/password | ✅ |
| 成功提示 | 右上角绿色 Toast 气泡（3秒自动消失），替代 alert | ✅ |
| 修改后登出 | 强制重新登录 | ✅ |

### 5.4 全局鉴权拦截

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 前端 fetch 拦截 | `window.fetch` 自动注入 `Authorization: Bearer <token>` | ✅ |
| 独立 apiFetch | 兼容所有 API 调用方式，自动处理 401 重定向 | ✅ |
| URL Token 优先 | URL 查询参数 `?token=xxx` 覆盖 Header/Cookie 中的 token（解决 EventSource 401） | ✅ |
| 登录页豁免 | `/login`、`/logout` 等白名单不加 token | ✅ |
| 401 自动跳转 | 收到 401 自动清除 token 并跳转登录页 | ✅ |

---

## 7. 项目管理

### 13.1 项目列表

#### 列表字段

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 项目名称 | 项目显示名称 | ✅ |
| 项目地址 | GitLab 仓库路径 | ✅ |
| 项目模板 | 关联的 AI 提示词模板名称 | ✅ |
| 任务状态 | 最近 5 个任务状态圆点 | ✅ |
| 启用 AI | 布尔开关 | ✅ |
| 关联资源池 | 任务执行使用的 OpenCode 资源池 | ✅ |
| **默认模型** | 项目级默认大模型（NULL = 走全局主备链路） | ✅ |
| 来源 | manual | ✅ |
| 修改时间 | 记录最后更新时间 | ✅ |

#### 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 查看详情 | 跳转项目详情页 | ✅ |
| 编辑 | 修改项目名称、模板、资源池、默认模型、启用 AI 状态 | ✅ |
| 删除 | 逻辑删除，需确认无运行中任务 | ✅ |
| 统一分页 | 支持 10/20/50 条/页选择 | ✅ |

#### 列表上方全局操作

| 按钮 | 说明 | 实现状态 |
|------|------|----------|
| 新建项目 | 手动录入项目 | ✅ |

### 13.2 项目详情页

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 基本信息 | 项目名称、仓库地址、来源 | ✅ |
| 配置信息 | 关联模板、关联资源池、默认模型、启用 AI 状态 | ✅ |
| 关联任务列表 | 展示项目关联的任务信息（**不含 AI 分支列与操作列**） | ✅ |
| **评审规则配置** | 项目级规则启用/禁用、严重程度覆盖 | ✅ |

### 13.3 评审规则配置

项目详情页「评审规则配置」标签页，管理该项目针对规则库的个性化配置。

#### 规则列表展示

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 规则名称 | 规则显示名称 + 代码 | ✅ |
| 描述 | 规则详细说明 | ✅ |
| 分类 | security / performance / readability 等 | ✅ |
| 开发语言 | 通用 / Go / Python / 前端 / Java | ✅ |
| 严重程度 | 规则默认级别（严重/高危/中危/低危/提示） | ✅ |
| 严重程度(覆盖) | 下拉选择项目自定义级别，空=使用默认 | ✅ |
| 状态 | 开关切换启用/禁用 | ✅ |

#### 筛选与搜索

| 条件 | 说明 | 实现状态 |
|------|------|----------|
| 分类筛选 | 按规则分类快速过滤 | ✅ |
| 严重程度筛选 | 严重/高危/中危/低危/提示 | ✅ |
| 开发语言筛选 | 全部 / 通用 / Go / Python / 前端 / Java | ✅ |
| 关键词搜索 | 规则名称、代码、描述模糊搜索 | ✅ |

#### 批量操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 复选框选择 | 每行左侧 checkbox，支持跨页保留选中 | ✅ |
| 全选本页 | 表头 checkbox，一键选中当前页全部规则 | ✅ |
| 全选全部 | toolbar 中「全选全部」，选中筛选后全部规则（跨页） | ✅ |
| 批量启用 | 蓝色确认弹框，显示选中规则名称预览（前3条） | ✅ |
| 批量禁用 | 红色确认弹框，二次确认后执行 | ✅ |
| 取消选择 | 清空所有选中状态 | ✅ |

#### 分页与展示

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 每页条数 | 10 / 15 / 25 / 50 / 100 条可选 | ✅ |
| 分页导航 | 上一页/下一页 + 页码快捷跳转 | ✅ |
| 重置默认 | 一键清除项目自定义配置，回退到全局规则默认状态 | ✅ |

### 13.4 成员映射管理

成员映射管理页面。

#### 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 统一分页 | 支持 10/20/50 条/页选择 | ✅ |

---

## 8. 任务管理

### 13.1 任务列表

#### 列表字段

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 任务 ID | 系统生成的唯一标识 | ✅ |
| 任务类型 | chat / review（蓝色/紫色标签） | ✅ |
| 触发方式 | 用户召唤 / 评分阈值 / 代码合并 | ✅ |
| 所属项目 | 项目名称 | ✅ |
| MR ID | GitLab MR IID | ✅ |
| 开发人员 | MR 作者用户名 | ✅ |
| 状态 | pending / running / success / failed / timeout / stopped | ✅ |
| 代码分支 | source → target 格式 | ✅ |
| 资源池/大模型 | review 任务展示 `[主]/[备用N] model_id`（如 `[主] Kimi-K2.6`）；非 review 展示资源池名称 | ✅ |
| 耗时 | 任务运行时长 | ✅ |
| 创建时间 | 任务创建时间 | ✅ |

#### 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 查看详情 | 弹出抽屉展示任务详情 | ✅ |
| 评审详情 | 查看 AI 评审内容 | ✅ |
| 智能体对话 | 打开 AI 实时对话弹窗（非 review 任务） | ✅ |
| 删除会话 | 删除 OpenCode 会话（非 review 任务） | ✅ |
| 重试 | failed / timeout / stopped / success 状态均可用 | ✅ |
| 停止 | 仅 running 状态可用 | ✅ |

#### 筛选/搜索

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 按项目筛选 | 下拉选择项目 | ✅ |
| 按状态筛选 | 多选状态 | ✅ |
| 按开发者筛选 | MR Author 模糊搜索（输入用户名） | ✅ |
| 按 MR IID 筛选 | 精确匹配 MR IID | ✅ |
| 按时间筛选 | 今天 / 最近 7 天 / 最近 30 天 / 本月 / 本年 / 自定义 | ✅ |
| 分页 | 统一分页组件，支持 10/20/50 条/页 | ✅ |

### 13.2 任务执行流程增强

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 任务超时 | 可配置超时时间（默认 30 分钟），超时自动终止 | ✅ |
| 停止任务 | 主动停止运行中任务，**不**向 MR 提交评论 | ✅ |
| 失败通知 | 任务失败自动向 MR 评论 "❌ 任务运行失败" | ✅ |
| 触发源追踪 | `trigger_source = manual/score_threshold`，入库并展示 | ✅ |
| 成功评论前缀 | 任务成功时在 MR 下评论：`深度代码审查任务【{taskID}】执行完成，审查报告：` | ✅ |
| AI 响应提取增强 | 提取 OpenCode 响应中 `reasoning` 类型的文本内容 | ✅ |
| 任务重试扩展 | `success` 状态任务也可重试（不仅 failed/timeout） | ✅ |
| **人工复核重试** | 重试时支持选择历史复核意见注入；倒序排列、默认全选、可折叠卡片、 最新高亮 | ✅ |
| **项目级并发调度** | 深度评审（`chat`）同项目串行；AI 评审（`review`）与深度评审并发，互不阻塞 | ✅ |
| **队列唤醒机制** | AI 评审成功/失败/停止/超时后自动唤起同项目 pending 深度评审，避免饿死 | ✅ |
| **模型使用记录** | review 任务完成后记录实际使用的 LLM 模型 ID（含主备切换场景） | ✅ |
| **主备链路追踪** | 任务列表/详情展示模型角色徽章（`[主]` 紫色 / `[备用N]` 橙色） | ✅ |
| **LLM HTTP 重试** | 502/503/504 + 网络层瞬时错误 + JSON 解析失败触发指数退避重试 | ✅ |
| **主备全失败聚合错误** | 任务 ErrorMsg 保留每个模型尝试的具体错误细节 | ✅ |

### 13.3 任务详情页布局（v4.5 重构）

#### 顶部主信息卡

| 元素 | 说明 |
|------|------|
| 项目名 + MR 链接 | 项目名 + GitLab 徽章链接到 MR |
| **综合评分徽章** | 仅 review 任务；分数等级颜色编码（≥85 优秀绿 / 70-84 良好蓝 / 60-69 一般橙 / <60 较差红 / 0 未评分灰） |
| MR 标题 | 完整 MR 标题（`title` 属性支持悬浮查看） |
| 辅助信息行 | 分支 + 作者 + 创建时间 + 触发方式（单行紧凑布局） |
| **量化数据行**（分隔线下） | 执行时长 + 变更文件数 + `+N / -N` 行数（紧凑一行展示） |

#### Tab 按需加载

| Tab | 数据来源 | 加载时机 |
|------|----------|----------|
| 代码变更 | `/tasks/:id/diff` | 进入页面时（异步加载，不阻塞顶部渲染） |
| 评审报告 | `/tasks/:id/structured-review` | 首次切换到评审报告 Tab |
| AI 对话 | `/tasks/:id/messages` + SSE `/tasks/:id/events` | 首次切换到 AI 对话 Tab |
| AI Response | 任务对象的 `ai_response` 字段（同步） | 同步渲染 |
| Token 用量 | `/token-usage/by-task` | 首次切换到 Token Tab（仅 review 任务） |

### 13.4 结构化评审结果（任务详情）

review 类型任务执行完成后，AI 评审结果按规则拆分为结构化 Issue 列表，支持逐条处理。

#### Issue 列表展示

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 规则代码 | 命中规则的 code（如 SQL-001） | ✅ |
| 严重程度 | 严重/高危/中危/低危/提示 彩色徽章 | ✅ |
| 问题描述 | AI 生成的具体问题说明 | ✅ |
| 建议修改 | AI 给出的修复建议 | ✅ |
| 涉及文件 | 文件路径 + 行号范围 | ✅ |
| 代码片段 | 可折叠的上下文代码块 | ✅ |

#### Issue 操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 展开/折叠详情 | 点击 Issue 卡片展开完整信息（代码片段、建议修改） | ✅ |
| 单条采纳 | 标记为已采纳，状态变为绿色 | ✅ |
| 单条拒绝 | 点击拒绝后弹出原因输入框，填写后标记为已拒绝 | ✅ |
| 批量采纳 | 勾选多个 Issue 后一键采纳 | ✅ |
| 批量拒绝 | 勾选多个 Issue 后一键拒绝（统一原因） | ✅ |
| 状态筛选 | 全部 / 待处理 / 已采纳 / 已拒绝 标签过滤 | ✅ |
| 批量操作栏 | 选中 Issue 后底部悬浮操作栏，显示已选数量 | ✅ |

#### 拒绝原因处理

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 原因必填 | 拒绝时必须填写原因 | ✅ |
| 展开详情自动显示 | 点击拒绝按钮时自动展开详情区域，避免输入框被遮挡 | ✅ |
| 后端日志记录 | Issue 状态变更写入操作日志，格式：`任务ID：N，Issue ID：M 状态变更为 rejected` | ✅ |

### 13.5 任务详情 Token 用量

review 类型任务详情页提供独立的 Token 用量 Tab。

| 功能 | 说明 |
|------|------|
| 5 个 KPI 卡 | 调用次数 / 输入 Tokens / 输出 Tokens / 总 Tokens / 平均耗时 |
| 调用明细列表 | 时间 / 模型 / 调用方（中文）/ 输入·输出 / 总计 / 状态·耗时（紧凑 6 列布局） |
| 调用方中文化 | `LLMService` / `PrimaryFallback` / `JSONRetry` 等英文 enum → 中文 |
| 分页 | 强制刷新重置第 1 页；非强制切 tab 保留上次分页位置 |
| 最近优先 | 默认按 `created_at DESC, id DESC` 排序 |
| 后端缺失任务兼容 | 失败/超时/功能上线前任务显示空状态提示 |

---

## 9. AI 实时对话

### 13.1 对话弹窗

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 流式消息展示 | OpenCode 实时 SSE 流式展示 AI 回复 | ✅ |
| 多轮对话 | 任务执行中可持续发送消息与 AI 对话 | ✅ |
| 历史记录加载 | 打开弹窗加载该任务已有的历史对话消息 | ✅ |
| 消息折叠 | 思考过程/tool 调用默认折叠，点击展开 | ✅ |
| 自动滚动 | 新消息自动滚动到底部 | ✅ |
| 白色消息气泡 | 用户消息白色背景（非蓝色） | ✅ |
| Markdown 渲染 | 完整渲染表格、代码块、标题、列表 | ✅ |

### 13.2 工具卡片渲染

SSE `part_updated` 事件触发工具卡片实时更新，支持以下工具：

| 工具 | 展示内容 | 实现状态 |
|------|----------|----------|
| `bash` / `shell` | 执行命令、输出、退出码 | ✅ |
| `edit` | 旧代码 → 新代码 diff 对比 | ✅ |
| `write` | 写入文件名和内容 | ✅ |
| `patch` | patch diff + 应用输出 | ✅ |
| `skill` | Markdown 格式的技能学习输出 | ✅ |

### 13.3 会话管理

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 会话保持 | 任务完成后保留 OpenCode Session | ✅ |
| 删除会话 | API 删除 Session 并清理数据库字段 | ✅ |
| 发送消息 | 向历史 Session 发送新 Prompt | ✅ |
| SSE 事件订阅 | 订阅 OpenCode `/global/event` 流 | ✅ |

---

## 10. 任务资源池

### 13.1 资源池列表

#### 列表字段

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 资源池名称 | 用户自定义名称 | ✅ |
| OpenCode 地址 | OpenCode 服务端点 | ✅ |
| 状态 | active / inactive / error | ✅ |
| 检查错误 | 健康检查失败时的错误信息 | ✅ |
| OpenCode 版本 | 登录后获取的版本号 | ✅ |
| 最大并行数 | 最大并行任务数 | ✅ |
| 检查间隔 | 健康检查间隔（秒） | ✅ |
| 默认资源池 | 是否为默认资源池 | ✅ |
| 最后检查时间 | 最近一次健康检查时间 | ✅ |
| 活跃任务数 | 当前正在执行的任务数 | ✅ |

#### 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 查看技能 | 跳转资源池技能详情页 | ✅ |
| 查看详情 | 弹窗卡片展示资源池完整字段信息 | ✅ |
| 编辑 | 修改名称、OpenCode 配置 | ✅ |
| 删除 | 需确认无运行中任务 | ✅ |
| 连通性检查 | 验证 OpenCode 服务可用性 | ✅ |
| 设为默认 | 设为默认资源池 | ✅ |
| 启用/禁用 | 切换状态 | ✅ |

### 13.2 新建/编辑资源池

| 配置项 | 必填 | 说明 | 实现状态 |
|--------|------|------|----------|
| 资源池名称 | 是 | 唯一标识 | ✅ |
| OpenCode 地址 | 是 | 服务端点 URL | ✅ |
| 用户名 | 否 | Basic 认证用户名 | ✅ |
| 密码 | 否 | Basic 认证密码（可解密） | ✅ |
| API Key | 否 | Bearer Token 认证 | ✅ |
| 最大并行数 | 是 | 默认 5 | ✅ |
| 检查间隔秒 | 是 | 默认 5 秒 | ✅ |

### 13.3 资源池详情弹窗

| 信息 | 说明 | 实现状态 |
|------|------|----------|
| 卡片布局 | 弹窗卡片展示资源池全部字段 | ✅ |
| 展示字段 | Name、OpenCode Endpoint、Username、Password、API Key、Version、Max Parallel、Check Interval、Status、Is Default、Last Check At、Check Error、Active Jobs | ✅ |

### 13.4 资源池技能详情

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 技能列表 | 调用 OpenCode `/skill` API 获取技能列表 | ✅ |
| 技能类型 | 内置技能（`<built-in>`）/ 自定义技能 | ✅ |
| 技能描述 | 悬浮提示显示完整描述 | ✅ |
| Markdown 详情 | 点击技能弹出模态框，使用 marked.js + highlight.js 渲染 Markdown 内容 | ✅ |
| 模态框滚动 | 打开模态框自动滚动到顶部 | ✅ |
| 实时搜索 | 输入框实时过滤技能列表 | ✅ |

---

## 11. 大模型管理

### 13.1 大模型列表

#### 列表字段

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| ID | 模型唯一标识 | ✅ |
| 提供商 | openai / vllm 等 | ✅ |
| 模型 ID | 如 gpt-4, Kimi-K2.6 | ✅ |
| Base URL | API 基础地址 | ✅ |
| API Key | 认证密钥（加密存储） | ✅ |
| 最大 Tokens | 最大输出 token 数 | ✅ |
| 超时时间 | API 调用超时秒数 | ✅ |
| 温度 | 采样温度 (0.0~1.0) | ✅ |
| **角色** | 主模型（紫色）/ 备用 N（橙色）/ 默认（蓝色）/ 无 | ✅ |
| 检查间隔 | 健康检查间隔（秒） | ✅ |
| 状态 | 健康检查状态 | ✅ |
| 检查错误 | 健康检查失败时的错误信息 | ✅ |
| 最后检查时间 | 最近健康检查时间 | ✅ |

#### 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 新建模型 | 配置提供商、模型、密钥等；支持选择角色（主/备用/无） | ✅ |
| 编辑模型 | 修改配置 | ✅ |
| 删除模型 | 删除配置 | ✅ |
| 设为默认 | 设为默认模型（不影响主/备角色） | ✅ |
| 取消默认 | 取消默认设置 | ✅ |
| **设为主模型** | 设置为主模型（自动取消其他主模型） | ✅ |
| **设为备用** | 设置为备用模型，可指定顺序 | ✅ |
| 连通性检查 | 发送测试请求验证 API 可用性 | ✅ |
| 查看详情 | 弹窗卡片展示模型完整配置（含角色徽章） | ✅ |

### 13.2 大模型主备切换

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| **全局主模型** | 系统范围内唯一主模型，所有未指定模型的 review 任务优先使用 | ✅ |
| **备用模型链** | 主模型失败时自动按 `backup_order` 顺序（1→2→3...）尝试备用模型 | ✅ |
| **项目级模型** | 项目可指定默认模型（`default_model_id`），NULL 则走全局主备链路 | ✅ |
| **强制指定** | Chat 任务传入 modelID > 0 时强制使用指定模型，不走主备链路 | ✅ |
| **失败降级** | 主模型调用失败（非 2xx）时自动尝试备用模型，记录实际使用的模型 | ✅ |
| **角色互斥** | 主模型和备用模型互斥，radio 单选组控制 | ✅ |

### 13.3 大模型健康检查守护进程

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 后台守护 | 独立守护进程自动执行模型健康检查 | ✅ |
| 状态更新 | 自动更新模型 Status、CheckError、LastCheckAt | ✅ |
| 告警通知 | 模型异常持续达阈值后自动发送企业微信告警 | ✅ |
| 恢复通知 | 模型恢复后自动发送恢复通知 | ✅ |
| 状态变化追踪 | 记录状态变化时间 status_changed_at | ✅ |
| 告警冷却 | 支持告警冷却期，避免重复告警 | ✅ |

### 13.4 大模型详情弹窗

| 信息 | 说明 | 实现状态 |
|------|------|----------|
| 卡片布局 | 弹窗卡片展示模型完整配置 | ✅ |
| 展示字段 | ID、Provider、ModelID、BaseURL、APIKey、MaxTokens、TimeoutSec、Temperature、IsDefault、**IsPrimary、BackupOrder**、CheckIntervalSec、Status、CheckError、LastCheckAt | ✅ |
| 角色信息卡 | 主模型（紫）、备用 N（橙）、默认（蓝）动态徽章展示 | ✅ |

---

## 12. 项目模板管理

### 13.1 模板列表

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 模板名称 | 唯一标识 | ✅ |
| 描述 | 用途说明 | ✅ |
| AI 提示词 | 系统 Prompt 模板 | ✅ |
| 关联项目数 | 使用此模板的项目数量 | ✅ |
| 创建时间 | 创建时间戳 | ✅ |

### 13.2 支持的 Prompt 变量

| 变量 | 说明 | 实现状态 |
|------|------|----------|
| `{{CLONE_URL}}` | Git 克隆地址（含 Token） | ✅ |
| `{{USER_INPUT}}` | 用户输入的 prompt | ✅ |
| `{{MR_DIFF}}` | MR 的代码 diff 内容 | ✅ |
| `{{MR_AUTHOR}}` | MR 作者 | ✅ |
| `{{SRC_BRANCH}}` | 源分支名 | ✅ |
| `{{DEST_BRANCH}}` | 目标分支名 | ✅ |
| `{{AI_BRANCH}}` | AI 创建的分支名 | ✅ |

### 13.3 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 编辑 | 修改名称、描述、提示词 | ✅ |
| 删除 | 删除模板 | ✅ |
| 克隆 | 基于现有模板复制 | ✅ |

### 13.4 AI 评审提示词格式规范

| 规范项 | 说明 | 实现状态 |
|--------|------|----------|
| 重点要求标题 | 使用 Markdown 三级标题 `### **重点要求（必须遵守）**` | ✅ |
| Commits 列表 | 使用 `- ` 前缀格式（替代 bullet points） | ✅ |

---

## 13. 企业微信通知

### 13.1 通知场景

| 场景 | 说明 | 实现状态 |
|------|------|----------|
| 任务失败 | 通知项目 MR 作者 | ✅ |
| 任务成功 | 通知项目 MR 作者 | ✅ |
| 任务超时 | 通知项目 MR 作者 | ✅ |
| 资源池异常 | 通知管理员 | ✅ |

### 13.2 通知配置

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 配置名称 | 通知配置名称 | ✅ |
| Webhook Key | 企业微信机器人 Key | ✅ |
| 启用状态 | 开关 | ✅ |
| 通知开关 | 成功/失败/超时/资源池异常 | ✅ |
| 关联项目 | 可绑定到具体项目 | ✅ |
| 测试按钮 | 发送测试消息 | ✅ |

### 13.3 列表操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 创建 | 新建通知配置 | ✅ |
| 编辑 | 修改配置 | ✅ |
| 删除 | 删除配置 | ✅ |
| 测试 | 发送测试消息 | ✅ |

---

## 14. 邮件通知

独立的 SMTP 配置与邮件收件人管理模块。

### 22.1 SMTP 配置

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| SMTP 服务器地址 | 如 smtp.office365.com:587 | ✅ |
| 发送邮箱 | From 地址 | ✅ |
| 用户名 | SMTP 认证用户名 | ✅ |
| 密码 | SMTP 认证密码（明文存储） | ✅ |
| 启用 TLS | STARTTLS 开关 | ✅ |
| 测试发送 | 发送测试邮件验证配置 | ✅ |

### 22.2 邮件认证

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| AUTH LOGIN | 支持 Exchange/Office365 的 `AUTH LOGIN` 认证（base64 用户名/密码） | ✅ |
| 纯 TCP + textproto | 自定义 SMTP 客户端，兼容不支持 PLAIN 的服务器 | ✅ |
| STARTTLS | 支持 TLS 升级 | ✅ |

### 22.3 收件人管理

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 收件人邮箱 | 邮箱地址 | ✅ |
| 收件人姓名 | 显示名称 | ✅ |
| 所属分组 | 默认分组，可自定义 | ✅ |
| 启用状态 | 是否接收邮件 | ✅ |
| 快速切换 | 点击状态徽章直接切换启用/禁用，无需打开编辑行 | ✅ |

### 22.4 报告发送分组

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 分组筛选 | 邮件管理页面按分组标签筛选收件人 | ✅ |
| 分组计数 | 各分组收件人数量统计（全部/各分组） | ✅ |
| 报告配置分组 | 周报/月报配置中勾选需要发送的分组 | ✅ |
| 手动发送分组 | 立即发送时弹出分组选择窗口，默认勾选配置中已选的分组 | ✅ |
| 分组过滤发送 | 按选中的分组过滤收件人发送邮件 | ✅ |

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 新增收件人 | 添加邮件收件人 | ✅ |
| 编辑 | 修改邮箱、姓名 | ✅ |
| 删除 | 删除收件人 | ✅ |
| 快速启用/禁用 | 点击状态徽章切换 | ✅ |

---

## 15. 报告管理

自动生成并发送周报/月报邮件的完整管理模块。

### 22.1 报告配置

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 报告类型 | weekly / monthly | ✅ |
| 生成开关 | 是否自动生成报告 | ✅ |
| 发送开关 | 是否自动发送邮件 | ✅ |
| 发送周期 | 每周第几天 / 每月第几天 | ✅ |
| 发送时间 | 小时:分钟 | ✅ |
| Cron 表达式 | 自动生成：`0 m h * * dow` / `0 m h dom * *` | ✅ |
| 热重载 | 保存配置后立即重载定时任务，无需重启服务 | ✅ |

### 22.2 并发安全

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| Mutex 保护 | `sync.Mutex` 防止并发保存配置时注册重复 cron 任务 | ✅ |
| EntryID 追踪 | `map[string]cron.EntryID]` 管理 weekly/monthly 两个定时器 | ✅ |
| 单实例要求 | 多实例部署会导致重复发送，需确保单实例运行 cron | ✅ |

### 22.3 报告数据时间对齐

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 周报周期 | 最近 7 个自然日（00:00 对齐） | ✅ |
| 月报周期 | 最近 30 个自然日（00:00 对齐） | ✅ |
| 周号计算 | 使用当前日期 ISOWeek，避免显示偏差 | ✅ |

### 22.4 报告内容（8 宫格 KPI）

| 区块 | 内容 | 实现状态 |
|------|------|----------|
| 总 MR 数 | 周期内的 MR 总数 | ✅ |
| 平均评分 | 周期内的平均评分 | ✅ |
| 低质量 MR | 周期内评分低于阈值的 MR 数 | ✅ |
| 活跃项目 | 周期内有 MR 记录的项目数 / 项目总数 | ✅ |
| 代码变更 | Additions + Deletions 总和（短格式） | ✅ |
| 审查次数 | 周期内 review_count 总和 | ✅ |
| 任务数 | 周期内关联任务总数 | ✅ |
| 状态分布 | Merged / Opened / Closed 数量 | ✅ |
| 评分分布图 | 柱状图展示评分区间分布 | ✅ |
| 开发者 TOP5 | 按 MR 数量排序的前 5 名开发者 | ✅ |

> **注**：发送时按「发送分组」过滤收件人，系统配置中 `send_groups` JSON 数组控制报告默认发送的分组列表。

### 22.5 Outlook 2016 兼容邮件模板

| 特性 | 说明 | 实现状态 |
|------|------|----------|
| DOCTYPE | HTML 4.01 Transitional | ✅ |
| 布局 | 纯 `<table>` 布局，无 flex/grid | ✅ |
| 样式 | `bgcolor=""`、`border="0"`、`<font>` 标签 | ✅ |
| 无 CSS 类 | 不使用 class 选择器 | ✅ |
| 固定宽度 | 外容器 `width="900"`，内容区 `width="852"` | ✅ |
| 无渐变/圆角 | 纯色背景、无 border-radius | ✅ |

### 22.6 报告日志管理

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 报告类型 | weekly / monthly | ✅ |
| 周期范围 | 开始日期 ~ 结束日期 | ✅ |
| 收件人 | JSON 序列化的收件人列表 | ✅ |
| HTML 内容 | 生成的完整报告 HTML | ✅ |
| 状态 | sent_success / sent_failed / generated_success / generated_failed | ✅ |
| 创建时间 | 生成时间 | ✅ |

### 22.7 报告操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 预览 | 弹窗预览报告内容（**不**写入日志） | ✅ |
| 手动发送 | 立即生成并发送报告邮件 | ✅ |
| 查看历史 | 分页查看已生成的报告 | ✅ |
| 状态筛选 | 按 sent_success / sent_failed 等筛选 | ✅ |
| 删除 | 删除报告记录 | ✅ |
| 查看 HTML | 打开新窗口查看完整 HTML | ✅ |

---

## 16. 系统管理

### 22.1 操作日志

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 操作时间 | 精确到秒 | ✅ |
| 操作类型 | 项目创建/编辑/删除、任务操作、资源池操作、模型操作（设为默认/主/备）等 | ✅ |
| 操作对象 | 具体对象名称 | ✅ |
| 操作结果 | 成功/失败 | ✅ |
| 错误信息 | 失败时记录 | ✅ |
| IP | 请求来源 IP | ✅ |

- 保留时长：默认 90 天
- 支持按操作类型筛选
- 统一分页组件，支持 10/20/50 条/页
- 支持清理过期日志
- **用户关联**：记录操作人用户 ID，接口返回中关联 `username` 字段展示

### 22.2 System Configuration

| Configuration Item | Default Value | Description | Implemented |
|--------|------|---------|----------|
| GitLab Webhook Secret | "" | Webhook signature verification key | ✅ |
| Task Timeout | 30 minutes | Global default task timeout | ✅ |
| MR Sync Interval | 60 seconds | MR status sync interval (s), ≤0 disables | ✅ |
| Max Parallel Tasks | 20 | Global parallel task limit | ✅ |
| Branch Prefix | AI- | Prefix for AI-created branches | ✅ |
| Random Char Length | 4 | Length of random part in branch name | ✅ |
| Log Retention | 90 days | Operation log retention days | ✅ |
| AI Conversation Template | default template | Prompt template for Chat tasks | ✅ |
| Score Threshold | 60 | Code review score threshold (1-100), below triggers deep review | ✅ |
| Code Review Template | default template | Prompt template for threshold-triggered review | ✅ |
| Diff Truncation Threshold | 5000 chars | Diff blocks exceeding this truncated before DB persist | ✅ |
| Alert Duration | 300 sec | Duration of exception before sending alert | ✅ |
| Alert Cooldown | 3600 sec | Minimum interval between same-exception alerts | ✅ |
| Alert Bot ID | 0 (disabled) | Enterprise WeChat bot ID for alerts | ✅ |
| Alert @ Members | "" | Enterprise WeChat member IDs to @ (comma-separated) | ✅ |
| **GitLab OAuth Enabled** | false | Enable GitLab OAuth login | ✅ |
| **GitLab Base URL** | "" | GitLab instance base URL | ✅ |
| **GitLab OAuth Client ID** | "" | OAuth app client ID | ✅ |
| **GitLab OAuth Client Secret** | "" | OAuth app client secret | ✅ |
| **GitLab OAuth Redirect URI** | "" | OAuth callback URI | ✅ |
| **GitLab OAuth Auto Create User** | true | Automatically create user on first OAuth login | ✅ |
| **GitLab OAuth Skip Verify** | false | Skip TLS certificate verification for GitLab | ✅ |
| **Max Diff Files** | 50 | 单次 review 任务允许的最大 diff 文件数 | ✅ |
| **Max Tokens Per Batch** | 100000 | 单批 prompt 的 token 上限（超过自动分批） | ✅ |
| **JSON Retry Max Attempts** | 3 | AI 输出 JSON 解析失败时的最大重试次数 | ✅ |
| **JSON Retry Initial Delay Sec** | 2 | JSON 重试初始延迟（秒） | ✅ |
| **JSON Retry Backoff Multiplier** | 2.0 | JSON 重试退避倍率 | ✅ |
| **JSON Retry Max Delay Sec** | 30 | JSON 重试最大延迟（秒） | ✅ |
| **JSON Retry Fallback Strategy** | regex | JSON 重试全部失败时的兜底策略（regex/manual） | ✅ |
| **LLM Retry Max Attempts** | 3 | LLM HTTP 调用重试最大次数（针对 502/503/504 + 网络层） | ✅ |
| **LLM Retry Initial Delay Ms** | 1000 | LLM HTTP 重试初始延迟（毫秒） | ✅ |
| **LLM Retry Backoff Multiplier** | 2.0 | LLM HTTP 重试退避倍率 | ✅ |
| **LLM Retry Max Delay Ms** | 30000 | LLM HTTP 重试最大延迟（毫秒） | ✅ |
| **Default Dimension Weights** | `{}` | JSON：评审维度默认权重（安全/质量/可读性/可维护性 等） | ✅ |
| **Default GitLab Comment Template** | text/template | GitLab 评论渲染的默认模板 | ✅ |

> **热加载**：Web 端保存上述配置项后立即生效（TTL 5min + cron 1min 主动刷新）。无需重启服务。

### 22.3 System Info

| Item | Description | Implemented |
|--------|------|---------|
| System Version | v2.0.0 | ✅ |
| Uptime | Duration since service start | ✅ |
| DB Status | ok | ✅ |
| Total Projects | All project count | ✅ |
| Total Tasks | All task count | ✅ |
| Total Pools | All resource pool count | ✅ |
| Total LLM Models | Configured LLM models count | ✅ |
| Running Tasks | Tasks in running status | ✅ |
| Failed Tasks | Tasks in failed status | ✅ |

### 22.4 Background Cron Jobs

| Job Name | Interval | Description | Implemented |
|----------|------|---------|----------|
| Pool Health Check | Every 1 sec triggers, actual check per pool's CheckIntervalSec | Check all non-inactive pools, support alert/recovery | ✅ |
| Model Health Check | Every 1 sec triggers, actual check per model's CheckIntervalSec | LLM model health daemon, support alert/recovery | ✅ |
| Task Timeout Check | Every 10 sec | Detect and terminate timed-out tasks | ✅ |
| MR Status Sync | Every N sec (config `mr_sync_interval_sec`, default 60, ≤0 disables) | Poll GitLab API to refresh opened MR state | ✅ |
| Report Generation | Per weekly/monthly config (Cron expression) | Generate and send email reports | ✅ |
| System Config Cache Refresh | Every 1 min | 主动刷新系统配置缓存，确保热加载生效 | ✅ |

---

## 17. 数据洞察：Token 用量监控

`token-usage.html` 是完整的 Token 用量监控页面（仅 admin 可见）。系统通过 `LLMCallLog` 表对每次 LLM 调用按行记录，含 prompt/completion/cached tokens、耗时、状态、错误信息等完整审计字段。

### 22.1 8 宫格 KPI 卡片

| 指标 | 说明 |
|------|------|
| 总调用次数 | 筛选范围内的 LLM 调用总次数 |
| 总 Tokens | prompt + completion + cached 之和（短格式如 23.7K） |
| 输入 Tokens | 总 prompt tokens |
| 输出 Tokens | 总 completion tokens |
| 缓存 Tokens | 总 cached tokens（命中 prompt cache 的部分） |
| 平均耗时 | 平均每次调用的 duration_ms |
| 成功率 | success / total 百分比 |
| 当前任务 | 关联任务数（去重） |

### 22.2 多维趋势图

| 图表 | 说明 |
|------|------|
| 用量趋势（折线图） | 按时间粒度展示总 tokens；today 按小时（24 桶），7d/30d 按天 |
| 模型分布（环形图） | 按 `model_name` 维度聚合调用次数占比 |
| 调用方分布 | 按 `caller` 字段（LLMService / PrimaryFallback / JSONRetry 等）中文化展示 |

### 22.3 Top 排行

| 维度 | 说明 |
|------|------|
| 项目 TOP 10 | 按 token 用量倒序的项目排行 |
| 模型 TOP 10 | 按 token 用量倒序的模型排行 |
| 用户 TOP 10 | 按 token 用量倒序的作者排行 |

### 22.4 调用明细

| 功能 | 说明 |
|------|------|
| 分页 | 支持 20/50/100 条/页选择 + 页码跳转 |
| 筛选 | 模型 / 项目 / 作者 / 状态 / 时间范围组合筛选 |
| 排序 | 默认按 `created_at DESC, id DESC` |
| 字段 | 时间 / 模型 / 调用方（中文）/ 输入·输出 / 总计 / 状态·耗时（紧凑 6 列） |
| **任务详情集成** | 单任务详情页 Token Tab 复用同一接口（`/token-usage/by-task`），支持任务维度筛选 |

### 22.5 首页摘要

`statistics.html` 首页对 admin 用户展示 Token 用量摘要块（4 个 KPI + 趋势小图），不展示完整监控页。

### 22.6 异步记录机制

| 维度 | 设计 |
|------|------|
| 拦截点 | `service/llm.go` 的 `callLLMAPI` 用 `defer` 异步落库，不阻塞主调用 |
| Channel | buffered channel (cap 1000)，2 worker goroutine 消费 |
| 批量写入 | 每 100 条 / 1s / 失败重试 3 次指数退避 |
| 字段最小集 | task_id / call_type / model_id / model_name / tokens / duration_ms / status / error_msg / cost_cents |
| Cost 计算 | 按模型的 `input_price_per_mtokens` / `output_price_per_mtokens` / `cached_price_per_mtokens` 计算成本（分 cents） |

---

## 18. 数据洞察：规则命中统计

`rule-stats.html` 是规则命中统计页面（admin/user 均可见）。基于 `ReviewIssue` 表（含 soft delete + 复合索引 `(rule_code, created_at)`）聚合规则命中率、修复率、误报率等核心指标。

### 22.1 4 个 KPI 卡

| 指标 | 说明 |
|------|------|
| 命中规则 | 筛选范围内命中 issue 的不同 rule_code 数 |
| 总命中 | 所有 issue 数（含 pending/accepted/rejected） |
| 涉及开发者 | 命中的不同作者数（去重） |
| 修复率（+误报率+待处理率） | 修复率 = accepted / total；误报率 = rejected / total；待处理率 = pending / total；三者之和 = 100% |

### 22.2 趋势 + 分布

| 图表 | 说明 |
|------|------|
| 命中趋势（折线图） | 按时间粒度展示命中数；today 按小时（24 桶），其他按天 |
| TOP 10 规则（横向条形图） | 按命中数倒序的 TOP 10 规则 |
| 分类分布（饼图） | 按 `category` 字段聚合占比（中文分类名） |
| 严重级别分布（饼图） | 按 `severity` 字段聚合（critical/high/medium/low/info → 严重/高/中/低/提示） |

### 22.3 TOP 10 规则表格

| 列 | 说明 |
|------|------|
| 排名 | 1-10 |
| 规则名称 + 代码 | 中文名 + rule_code |
| 分类 | 来自 `review_categories` JOIN（中文） |
| 严重级别 | critical/high/medium/low/info 颜色徽章 |
| 命中数 | 总命中 issue 数 |
| 修复率 | accepted / 命中数 百分比 |
| 操作 | 钻取按钮 → 进入单规则详情 |

### 22.4 单规则钻取（URL 参数 `?rule=XXX&range=YYY`）

| 模块 | 说明 |
|------|------|
| 规则元信息 | 规则名称、代码、分类、严重级别、描述 |
| 单规则 KPI | 总命中 / 修复 / 误报 / 待处理 |
| 单规则趋势 | 折线图展示时间维度命中数 |
| 项目 TOP 5 | 命中该规则的 TOP 5 项目 |
| 作者 TOP 5 | 命中该规则的 TOP 5 作者 |
| 最近命中分页 | 最近命中 issue 列表 + 代码片段（`<pre>` 截断 800 字符） |

### 22.5 时间范围

| 范围 | 说明 |
|------|------|
| today | 按小时（24 桶） |
| 7d | 按天（7 桶） |
| 30d | 按天（30 桶） |
| 90d | 按天（90 桶） |
| 全部 | 按天（历史全部） |

### 22.6 规则库联动

`review-rules.html` 规则卡片显示"近 7 天命中 N"，链接指向 `rule-stats.html?rule=XXX&range=7d` 自动打开对应规则的钻取抽屉。

---

## 19. 规则孵化台

`incubator.html` 是规则孵化台的完整管理页面（admin 可见）。系统通过 6 节点流水线从 Issue 自动聚类提炼候选规则，经 LLM 智能提炼、相似检测、模拟测试后发布上线。

### 22.1 6 节点流水线可视化

| 节点 | 说明 | 实现状态 |
|------|------|----------|
| 原始 Issue 池 | 展示未匹配规则的 Issue 数量与回溯时间窗口 | ✅ |
| 聚类分析 | 对 Issue 进行聚类，生成候选规则集 | ✅ |
| 候选规则池 | 展示所有候选规则，区分草稿/就绪状态 | ✅ |
| 智能提炼 | LLM 对候选规则进行自动命名、描述、Prompt 生成 | ✅ |
| 相似检测 | L1 Jaccard + L2 Embedding 余弦相似度双重检测 | ✅ |
| 模拟测试 | LLM 自动生成测试用例并验证规则有效性 | ✅ |

#### 画布交互

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| Dify 风格蛇形布局 | 6 节点按 3×2 蛇形排列，奇数行反向 | ✅ |
| 节点拖拽 | 支持鼠标拖拽调整节点位置 | ✅ |
| 实时进度 | 流水线执行期间 SSE 实时推送每节点进度 | ✅ |
| 状态色彩 | 运行中/已完成/空闲/错误 不同颜色标识 | ✅ |
| 节点点击 | 点击节点展开右侧详情面板 | ✅ |
| 自动重连 | 切换 Tab 后 SSE 自动恢复连接 | ✅ |

### 22.2 聚类分析

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 自动触发 | 流水线启动时自动执行聚类 | ✅ |
| 时间窗口 | 可配置回溯天数（默认 30 天） | ✅ |
| 最小簇大小 | 可配置最小聚类 Issue 数（默认 3） | ✅ |
| 最大候选数 | 可配置单次最多生成候选规则数（默认 20） | ✅ |
| 进度面板 | 聚类期间实时展示各簇处理进度 | ✅ |

### 22.3 候选规则管理

#### 列表展示

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| 状态标签 | draft（灰色）/ ready（蓝色）/ published（绿色） | ✅ |
| 来源标识 | 流水线任务编号 或 手动创建 | ✅ |
| 规则名称 | LLM 生成的语义化名称 | ✅ |
| 规则代码 | `incubated-{slug}-{rand}` 格式 | ✅ |
| 来源 Issue 数 | 关联的 Issue 数量 | ✅ |
| 置信度 | 0.40~1.00 动态评分 | ✅ |

#### 筛选与搜索

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 状态筛选 | draft / ready / published / rejected | ✅ |
| 严重程度筛选 | critical / high / medium / low / info | ✅ |
| 关键词搜索 | 规则名称、代码模糊搜索 | ✅ |
| 统一分页 | 支持页码跳转 | ✅ |

#### 操作

| 操作 | 说明 | 实现状态 |
|------|------|----------|
| 查看详情 | 右侧滑出抽屉展示规则完整信息 | ✅ |
| 向量投影 | 3D 散点图展示候选规则与 Top5 相似规则的空间分布 | ✅ |
| 删除 | 删除候选规则（保护 published），级联删除向量数据 | ✅ |
| 手动创建 | 支持从 Issue 多选批量创建候选规则 | ✅ |
| 发布 | 将候选规则转为正式 ReviewRule | ✅ |

#### 状态标签

| 标签 | 条件 | 实现状态 |
|------|------|----------|
| 相似检测未通过 | `similar_passed == false` | ✅ |
| 模拟测试未通过 | `test_results.accuracy < 0.5` | ✅ |

### 22.4 智能提炼

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| LLM 自动提炼 | 基于 Issue 聚类结果自动生成规则名称、描述、Prompt | ✅ |
| 语义化代码 | `incubated-{semantic-slug}-{4-char-rand}` 格式 | ✅ |
| 置信度计算 | 基于 Prompt 完整度 / 名称描述质量 / 模拟测试准确率 / 相似检测结果动态计算 | ✅ |
| 异步向量化 | 提炼完成后异步触发 Embedding 生成 | ✅ |

### 22.5 相似检测

#### 双重检测策略

| 层级 | 算法 | 阈值 | 说明 | 实现状态 |
|------|------|------|------|----------|
| L1 | Jaccard 关键词 | `similarity_pass_threshold`（默认 0.5） | 结构级快速匹配 | ✅ |
| L2 | Embedding 余弦相似度 | `similarity_pass_threshold` | 语义级深度匹配 | ✅ |

#### 检测过程

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 实时嵌入 | 向量缺失时实时调用 Embedding API 生成 | ✅ |
| 去重策略 | 同一 `rule_id` 取最高相似度 | ✅ |
| Top 5 限制 | 最多保留 5 条最相似的现有规则 | ✅ |
| 方法标记 | 标注匹配方法（jaccard / embedding） | ✅ |
| 代码碰撞 | 检测 code 重复，自动重命名 | ✅ |

#### 通过判断

| 字段 | 说明 | 实现状态 |
|------|------|----------|
| `similar_passed` | `true` = 无相似规则通过；`false` = 存在高相似规则 | ✅ |
| `similar_rules` | JSON 数组，存储 Top5 相似规则详情 | ✅ |

### 22.6 模拟测试

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 测试用例生成 | LLM 基于规则 Prompt 生成正例/反例 | ✅ |
| 双重验证 | LLM 对每个用例判断 YES/NO，计算准确率 | ✅ |
| 结果存储 | `test_results` JSON 存储 accuracy / cases / timestamp | ✅ |
| 置信度联动 | 测试准确率直接参与置信度评分计算 | ✅ |

### 22.7 向量投影（3D 可视化）

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| ECharts GL | 基于 ECharts GL 的 3D 散点图 | ✅ |
| 中心节点 | 候选规则自身（紫色） | ✅ |
| Top5 节点 | 最相似的 5 条现有规则（蓝色） | ✅ |
| 悬浮提示 | 展示规则 ID、名称、代码、相似度、分类、严重级别、Prompt 片段 | ✅ |
| 文本换行 | Tooltip 强制自动换行，防止长文本溢出 | ✅ |

### 22.8 Embedding 引擎配置

| 功能 | 说明 | 实现状态 |
|------|------|----------|
| 模型选择 | 从已配置的 Embedding 模型中选择 | ✅ |
| 测试连接 | 选中模型后可直接测试 API 连通性（无需先保存配置） | ✅ |
| 相似度阈值 | 可配置通过阈值（0.1~1.0，默认 0.5） | ✅ |
| 向量化触发 | 手动触发已有规则批量向量化 | ✅ |
| 增量同步 | 规则 Create / Update / Publish 时自动异步触发向量化 | ✅ |
| 进度持久化 | 从 `review_rule_vectors` 表实时查询，服务重启不丢失 | ✅ |

### 22.9 数据表

| 表名 | 说明 |
|------|------|
| `rule_incubations` | 候选规则主表（含 `similar_passed`, `similar_rules`, `test_results`） |
| `rule_incubation_jobs` | 流水线任务记录（cluster / refine / similar_check / sandbox_test / pipeline_run） |
| `rule_incubation_vectors` | 候选规则向量存储 |
| `incubator_configs` | 孵化台全局配置（单例表） |
| `review_rule_vectors` | 已有规则的向量存储 |

---

## 20. 前端体验：主题与导航

### 22.1 主题系统

| 功能 | 说明 |
|------|------|
| 三态切换 | light（亮）/ dark（暗）/ system（跟随系统）循环切换 |
| 默认状态 | system（首次进入跟随 OS `prefers-color-scheme`） |
| 持久化 | `localStorage['cg.theme']` 保存用户偏好 |
| 跨页广播 | `themechange` CustomEvent 在同一浏览器 tab 间实时同步 |
| 防 FOUC | `<head>` 顶部注入 `theme.js?v=1`，在 CSS 加载前应用主题 |
| API | `window.ThemeManager.get()/set()/toggle()/on('change', cb)` |

### 22.2 CSS 变量设计系统

| 变量类别 | 变量 |
|----------|------|
| 背景 | `--cg-bg-base` / `--cg-bg-surface` / `--cg-bg-elevated` / `--cg-bg-hover` / `--cg-bg-overlay` |
| 文字 | `--cg-text-primary` / `--cg-text-secondary` / `--cg-text-tertiary` / `--cg-text-disabled` |
| 品牌色 | `--cg-brand-primary` (indigo-600/400) / `--cg-brand-secondary` (cyan) / `--cg-brand-accent` (amber) |
| 侧边栏 | `--cg-sidebar-bg` (渐变) / `--cg-sidebar-text` / `--cg-sidebar-item-hover` / `--cg-sidebar-item-active` |
| 阴影 / 圆角 / 过渡 | `--cg-shadow-sm/md/lg` / `--cg-radius-sm/md/lg/xl` / `--cg-transition` (200ms cubic-bezier) |

### 22.3 菜单架构（6 折叠组 admin/user 差异化）

| 顶层 | admin 子项 | user 子项 |
|------|------------|-----------|
| 首页 | 统计看板 | 统计看板 |
| 任务列表 | 任务列表 | 任务列表 |
| **数据洞察** | MR 提交统计 / 规则命中统计 / **Token 用量** | MR 提交统计 / 规则命中统计 |
| **AI 评审** | 评审规则 | （隐藏） |
| **通知管理** | 企业微信 / 邮件报告 / 成员映射 / 报告管理 | （隐藏） |
| **系统管理** | 项目管理 / 项目模板 / 资源池 / 大模型 / 系统设置 | （隐藏） |

### 22.4 菜单交互细节

| 功能 | 说明 |
|------|------|
| 自动 URL 检测 | `activePageId` 从 `window.location.pathname` 自动识别，无需每页配置 |
| 自动展开活跃组 | DOMContentLoaded 一次性自动展开包含当前活跃项的组 |
| 折叠状态持久化 | 用户手动折叠/展开状态存入 localStorage |
| 子页面匹配 | `isMenuItemActive(href, path, search)` 支持 `settings.html?tab=xxx` 匹配 |
| 角色过滤 | 子项通过 `role: ['admin']` / `role: ['admin', 'user']` 字段控制可见性 |

---

## 21. 数据模型定义

### 22.1 User (User)

```go
type User struct {
    ID        uint      `gorm:"primaryKey"`
    Username  string    `gorm:"size:100;uniqueIndex;not null"`
    Password  string    `gorm:"size:255;not null"`  // bcrypt hash
    Role      string    `gorm:"size:20;default:'admin'"`
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

### 22.2 Token (Token)

```go
type Token struct {
    ID        uint      `gorm:"primaryKey"`
    UserID    uint      `gorm:"index"`
    Token     string    `gorm:"size:64;uniqueIndex;not null"`
    Username  string    `gorm:"size:100"`
    ExpiresAt time.Time `gorm:"index"`
    CreatedAt time.Time
}
```

### 22.3 Project (Project)

```go
type Project struct {
    ID              uint            `gorm:"primaryKey" json:"id"`
    Name            string          `gorm:"size:255;not null" json:"name"`
    ProjectPath     string          `gorm:"size:255;uniqueIndex" json:"project_path"`     // GitLab repo path
    GitLabProjectID int             `gorm:"column:gitlab_project_id" json:"gitlab_project_id"`
    TemplateID      uint            `gorm:"index" json:"template_id"`                     // Associated template ID
    PoolID          uint            `gorm:"index" json:"pool_id"`                         // Associated pool ID
    DefaultModelID  *uint           `gorm:"index" json:"default_model_id"`                // NULL = 未指定（不触发 review 任务）
    AIEnabled       bool            `gorm:"default:false" json:"ai_enabled"`              // AI enabled
    Source          string          `gorm:"size:20;default:'manual'" json:"source"`       // manual/...
    Language        string          `gorm:"size:32;default:'golang'" json:"language"`      // 项目主要编程语言
    AccessToken     string          `gorm:"size:500" json:"access_token"`                 // GitLab AccessToken
    LastSyncAt      *time.Time      `json:"last_sync_at"`                                 // 上次 GitLab 同步时间
    SyncStatus      string          `gorm:"size:20;default:'success'" json:"sync_status"` // success/failed
    SyncError       string          `gorm:"size:512" json:"sync_error"`                   // 同步失败时的错误
    CreatedAt       time.Time       `json:"created_at"`
    UpdatedAt       time.Time       `json:"updated_at"`
    DeletedAt       gorm.DeletedAt  `gorm:"index" json:"deleted_at"`
    Template        ProjectTemplate `gorm:"foreignKey:TemplateID;references:ID" json:"template,omitempty"`
    Pool            ResourcePool    `gorm:"foreignKey:PoolID;references:ID" json:"pool,omitempty"`
    Model           LLMModel        `gorm:"foreignKey:DefaultModelID;references:ID" json:"model,omitempty"`
    Tasks           []Task          `gorm:"foreignKey:ProjectID" json:"tasks,omitempty"`

    // 关联统计（非表字段，仅 API 展示使用）
    EnabledRuleCount int `gorm:"-" json:"enabled_rule_count,omitempty"`
    TotalRuleCount   int `gorm:"-" json:"total_rule_count,omitempty"`
}
```

### 22.4 Task (Task)

```go
type TaskStatus string

const (
    TaskPending  TaskStatus = "pending"
    TaskRunning  TaskStatus = "running"
    TaskSuccess  TaskStatus = "success"
    TaskFailed   TaskStatus = "failed"
    TaskTimeout  TaskStatus = "timeout"
    TaskStopped  TaskStatus = "stopped"
)

type Task struct {
    ID                uint         `gorm:"primaryKey" json:"id"`
    ProjectID         uint         `gorm:"index;not null" json:"project_id"`
    MRMergeID         int          `json:"mr_iid"`
    MRAuthor          string       `gorm:"size:100" json:"author"`
    MRTitle           string       `gorm:"size:512" json:"mr_title"`
    MRURL             string       `gorm:"size:512" json:"mr_url"`
    NoteID            int          `json:"note_id"`
    TriggerType       string       `gorm:"size:20;default:'webhook'" json:"trigger_type"`
    TriggerSource     string       `gorm:"size:30;default:'manual'" json:"trigger_source"`  // manual | score_threshold
    TaskType          string       `gorm:"size:20;default:'chat'" json:"task_type"`         // chat / review
    Status            TaskStatus   `gorm:"size:20;index;default:'pending'" json:"status"`
    SourceBranch      string       `gorm:"size:100" json:"source_branch"`
    TargetBranch      string       `gorm:"size:100" json:"target_branch"`
    PoolID            uint         `json:"pool_id"`
    UsedModelID       uint         `gorm:"column:model_id" json:"model_id"`          // Actual used LLM model ID
    GitlabTokenID     uint         `json:"gitlab_token_id"`
    StartedAt         *time.Time   `json:"started_at"`
    CompletedAt       *time.Time   `json:"completed_at"`
    DurationSec       int          `gorm:"default:0" json:"duration_sec"`
    ErrorMsg          string       `gorm:"type:longtext" json:"error_msg"`
    OpencodeSessionID string       `gorm:"size:128" json:"opencode_session_id"`
    DiffSummary       string       `gorm:"type:text" json:"diff_summary"`
    AIPrompt          string       `gorm:"type:longtext" json:"ai_prompt"`
    AIResponse        string       `gorm:"type:longtext" json:"ai_response"`
    RetryCount        int          `gorm:"default:0" json:"retry_count"`
    ScoreValue        int          `gorm:"default:0" json:"score_value"`
    CreatedAt         time.Time    `json:"created_at"`
    UpdatedAt         time.Time    `json:"updated_at"`
    Project           Project      `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
    Pool              ResourcePool `gorm:"foreignKey:PoolID" json:"pool,omitempty"`
    UsedModel         LLMModel     `gorm:"foreignKey:UsedModelID;references:ID" json:"used_model,omitempty"`
}
```

### 22.5 Project Template (ProjectTemplate)

```go
type ProjectTemplate struct {
    ID          uint      `gorm:"primaryKey"`
    Name        string    `gorm:"size:100;uniqueIndex;not null"`
    Description string    `gorm:"size:512"`
    Prompt      string    `gorm:"type:text;not null"`
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

### 22.6 Resource Pool (ResourcePool)

```go
type PoolStatus string

const (
    PoolActive   PoolStatus = "active"
    PoolInactive PoolStatus = "inactive"
    PoolError    PoolStatus = "error"
)

type ResourcePool struct {
    ID               uint       `gorm:"primaryKey" json:"id"`
    Name             string     `gorm:"size:100;uniqueIndex;not null" json:"name"`
    OpencodeEndpoint string     `gorm:"size:512;not null" json:"opencode_endpoint"`
    OpencodeUsername string     `gorm:"size:100" json:"opencode_username"`
    OpencodePassword string     `gorm:"size:512" json:"opencode_password"`
    OpencodeAPIKey   string     `gorm:"size:512" json:"opencode_api_key"`
    OpencodeVersion  string     `gorm:"size:50" json:"opencode_version"`
    MaxParallel      int        `gorm:"default:5" json:"max_parallel"`
    CheckIntervalSec int        `gorm:"default:5" json:"check_interval_sec"`
    Status           PoolStatus `gorm:"size:20;default:'active'" json:"status"`
    IsDefault        bool       `gorm:"default:false" json:"is_default"`
    LastCheckAt      *time.Time `json:"last_check_at"`
    StatusChangedAt  *time.Time `json:"status_changed_at"`
    LastAlertAt      *time.Time `json:"last_alert_at"`
    CheckError       string     `gorm:"type:text" json:"check_error"` // LiteLLM 错误链可能很长，升级为 TEXT；service 层防御性截断 4000 rune
    ActiveJobs       int        `gorm:"default:0" json:"active_jobs"`
    CreatedAt        time.Time  `json:"created_at"`
    UpdatedAt        time.Time  `json:"updated_at"`
}
```

### 22.7 LLM Model (LLMModel)

```go
type LLMModel struct {
    ID               uint       `gorm:"primaryKey" json:"id"`
    Provider         string     `gorm:"size:50;not null" json:"provider"`
    ModelID          string     `gorm:"size:100;not null" json:"model_id"`
    BaseURL          string     `gorm:"size:512;not null" json:"base_url"`
    APIKey           string     `gorm:"size:512;not null" json:"api_key"`
    MaxTokens        int        `gorm:"default:4096" json:"max_tokens"`
    TimeoutSec       int        `gorm:"default:120" json:"timeout_sec"`
    Temperature      float64    `gorm:"default:0.1" json:"temperature"`
    IsDefault        bool       `gorm:"default:false" json:"is_default"`
    IsPrimary        bool       `gorm:"default:false" json:"is_primary"`
    BackupOrder      int        `gorm:"default:0" json:"backup_order"`
    CheckIntervalSec int        `gorm:"default:5" json:"check_interval_sec"`
    Status           string     `gorm:"size:20;default:'active'" json:"status"`
    CheckError       string     `gorm:"type:text" json:"check_error"` // LiteLLM 错误链可能很长，升级为 TEXT；service 层防御性截断 4000 rune
    LastCheckAt      *time.Time `json:"last_check_at"`
    StatusChangedAt  *time.Time `json:"status_changed_at"`
    LastAlertAt      *time.Time `json:"last_alert_at"`
    LastTestAt       *time.Time `json:"last_test_at"`
    LastTestStatus   string     `gorm:"size:20;default:''" json:"last_test_status"`

    // 价格字段（USD / 百万 token；0 = 未知/不计入成本），用于 Token 用量监控的成本估算
    InputPricePerMTokens  float64 `gorm:"default:0;column:input_price_per_mtokens" json:"input_price_per_mtokens"`
    OutputPricePerMTokens float64 `gorm:"default:0;column:output_price_per_mtokens" json:"output_price_per_mtokens"`
    CachedPricePerMTokens float64 `gorm:"default:0;column:cached_price_per_mtokens" json:"cached_price_per_mtokens"`

    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
```

### 22.8 WeCom Notifier (WeComNotifier)

```go
type WeComNotifier struct {
    ID              uint       `gorm:"primaryKey"`
    Name            string     `gorm:"size:100;not null"`
    WebhookUrl      string     `gorm:"size:512;not null"`
    MessageTemplate string     `gorm:"type:text"`
    ProjectID       *uint      `gorm:"index"`
    Enabled         bool       `gorm:"default:false"`
    LastTestAt      *time.Time
    LastTestStatus  string     `gorm:"size:20"`
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

### 22.9 Operation Log (OperationLog)

```go
type OperationLog struct {
    ID          uint      `gorm:"primaryKey"`
    OpType      string    `gorm:"size:50;index;not null"`
    OpObject    string    `gorm:"size:100"`
    OpObjectID  uint
    OpResult    string    `gorm:"size:20"`
    ErrorMsg    string    `gorm:"size:512"`
    RequestIP   string    `gorm:"size:45"`
    CreatedAt   time.Time `gorm:"index"`
}
```

### 22.10 System Config (SystemConfig)

```go
type SystemConfig struct {
    ID                      uint      `gorm:"primaryKey"`
    GitlabToken             string    `gorm:"size:255"`
    TaskTimeoutMin          int       `gorm:"default:120"`
    SyncIntervalSec         int       `gorm:"default:60"`
    MRSyncIntervalSec       int       `gorm:"default:60"`
    MaxParallelTask         int       `gorm:"default:20"`
    BranchPrefix            string    `gorm:"size:20;default:'AI-'"`
    RandLength              int       `gorm:"default:4"`
    LogRetentionDay         int       `gorm:"default:90"`
    AILogTemplate           string    `gorm:"type:text"`
    ScoreThreshold          int       `gorm:"default:60"`
    ReviewTemplate          string    `gorm:"type:text"`
    DiffTruncationThreshold int       `gorm:"default:5000"`
    MaxDiffFiles            int       `gorm:"default:50;column:max_diff_files"`              // 单次 review 任务最大 diff 文件数
    MaxTokensPerBatch       int       `gorm:"default:100000;column:max_tokens_per_batch"`    // 单批 prompt 的 token 上限
    AlertDurationSec        int       `gorm:"default:300"`
    AlertCooldownSec        int       `gorm:"default:3600"`
    AlertNotifierID         uint      `gorm:"default:0"`
    AlertMentionUserIDs     string    `gorm:"size:512"`

    // JSON 结构化输出重试配置
    JSONRetryMaxAttempts       int     `gorm:"default:3;column:json_retry_max_attempts" json:"json_retry_max_attempts"`
    JSONRetryInitialDelaySec   int     `gorm:"default:2;column:json_retry_initial_delay_sec" json:"json_retry_initial_delay_sec"`
    JSONRetryBackoffMultiplier float64 `gorm:"default:2.0;column:json_retry_backoff_multiplier" json:"json_retry_backoff_multiplier"`
    JSONRetryMaxDelaySec       int     `gorm:"default:30;column:json_retry_max_delay_sec" json:"json_retry_max_delay_sec"`
    JSONRetryFallbackStrategy  string  `gorm:"size:20;default:'regex';column:json_retry_fallback_strategy" json:"json_retry_fallback_strategy"`

    // LLM HTTP 调用重试配置（针对 502/503/504 与网络层瞬时错误）
    LLMRetryMaxAttempts       int     `gorm:"default:3;column:llm_retry_max_attempts" json:"llm_retry_max_attempts"`
    LLMRetryInitialDelayMs    int     `gorm:"default:1000;column:llm_retry_initial_delay_ms" json:"llm_retry_initial_delay_ms"`
    LLMRetryBackoffMultiplier float64 `gorm:"default:2.0;column:llm_retry_backoff_multiplier" json:"llm_retry_backoff_multiplier"`
    LLMRetryMaxDelayMs        int     `gorm:"default:30000;column:llm_retry_max_delay_ms" json:"llm_retry_max_delay_ms"`

    DefaultDimensionWeights      string `gorm:"type:json;column:default_dimension_weights" json:"default_dimension_weights"`
    DefaultGitLabCommentTemplate string `gorm:"type:text;column:default_gitlab_comment_template" json:"default_gitlab_comment_template"`

    // GitLab OAuth 配置（从环境变量迁移到数据库动态配置）
    GitLabOAuthEnabled        bool   `gorm:"default:false;column:gitlab_oauth_enabled" json:"gitlab_oauth_enabled"`
    GitLabBaseURL             string `gorm:"size:255;column:gitlab_base_url" json:"gitlab_base_url"`
    GitLabOAuthClientID       string `gorm:"size:255;column:gitlab_oauth_client_id" json:"gitlab_oauth_client_id"`
    GitLabOAuthClientSecret   string `gorm:"size:255;column:gitlab_oauth_client_secret" json:"gitlab_oauth_client_secret"`
    GitLabOAuthRedirectURI    string `gorm:"size:512;column:gitlab_oauth_redirect_uri" json:"gitlab_oauth_redirect_uri"`
    GitLabOAuthAutoCreateUser bool   `gorm:"default:true;column:gitlab_oauth_auto_create_user" json:"gitlab_oauth_auto_create_user"`
    GitLabOAuthSkipVerify     bool   `gorm:"default:false;column:gitlab_oauth_skip_verify" json:"gitlab_oauth_skip_verify"`

    CreatedAt time.Time
    UpdatedAt time.Time
}
```

### 22.11 MR Review Log (MergeRequestReviewLog) (this app DB)

```go
type MergeRequestReviewLog struct {
    ID            uint      `gorm:"primaryKey;column:id"`
    URL           string    `gorm:"size:512;not null;column:mr_url"`
    ProjectName   string    `gorm:"size:255;column:project_name"`
    AuthorName    string    `gorm:"size:100;column:author_name"`
    SourceBranch  string    `gorm:"size:100;column:source_branch"`
    TargetBranch  string    `gorm:"size:100;column:target_branch"`
    LastCommitID  string    `gorm:"size:64;column:last_commit_id"`
    Score         float64   `gorm:"column:score"`
    ScoreHistory  string    `gorm:"type:text;column:score_history"`
    Commits       string    `gorm:"type:text;column:commits"`
    ReviewCount   int       `gorm:"column:review_count"`
    SyncedAt      time.Time `gorm:"column:synced_at"`
    MRCreatedAt   time.Time `gorm:"column:mr_created_at"`
    MRTitle       string    `gorm:"size:512;column:mr_title"`
    MRState       string    `gorm:"size:20;column:mr_state"`
    MRID          int       `gorm:"column:mr_id"`
    IsDraft       bool      `gorm:"default:false;column:is_draft"`
    Tasks         string    `gorm:"type:text;column:tasks"`
}
```

### 22.12 SMTP Config (SMTPConfig)

```go
type SMTPConfig struct {
    ID       uint   `gorm:"primaryKey"`
    Host     string `gorm:"size:255;not null"`
    Port     int    `gorm:"default:587"`
    Username string `gorm:"size:255"`
    Password string `gorm:"size:255"`
    From     string `gorm:"size:255"`
    UseTLS   bool   `gorm:"default:true"`
}
```

### 22.13 Report Config (ReportConfig)

```go
type ReportConfig struct {
    ID             uint      `gorm:"primaryKey"`
    Type           string    `gorm:"size:20;not null"`
    GenerateEnabled bool     `gorm:"default:false"`
    SendEnabled    bool      `gorm:"default:false"`
    SendGroups     string    `gorm:"type:text"`
    DayOfWeek      int       `gorm:"default:1"`
    DayOfMonth     int       `gorm:"default:1"`
    Hour           int       `gorm:"default:9"`
    Minute         int       `gorm:"default:0"`
    CreatedAt      time.Time
    UpdatedAt      time.Time
}
```

### 22.15 Report Recipient (ReportRecipient)

```go
type ReportRecipient struct {
    ID        uint   `gorm:"primaryKey"`
    Email     string `gorm:"size:255;not null"`
    Name      string `gorm:"size:100"`
    GroupName string `gorm:"size:100"`
    Enabled   bool   `gorm:"default:true"`
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

### 22.16 Review Rule (ReviewRule)

全局评审规则库，多语言内置规则。

```go
type ReviewRule struct {
    ID          uint      `gorm:"primaryKey" json:"id"`
    Code        string    `gorm:"uniqueIndex;size:64" json:"code"`              // 规则唯一代码
    Name        string    `gorm:"size:100" json:"name"`                        // 显示名称
    Category    string    `gorm:"size:32" json:"category"`                     // security/performance/readability/...
    Severity    string    `gorm:"size:16" json:"severity"`                     // critical/high/medium/low/info
    Language    string    `gorm:"size:32;default:'common'" json:"language"`     // golang/python/frontend/java/common
    Description string    `gorm:"type:text" json:"description"`                // 规则描述
    Prompt      string    `gorm:"type:text" json:"prompt"`                     // 评审提示词（可选）
    SortOrder   int       `gorm:"default:0" json:"sort_order"`                 // 排序号
    IsBuiltIn   bool      `gorm:"default:true" json:"is_built_in"`             // 是否内置
    IsEnabled   bool      `gorm:"default:true" json:"is_enabled"`              // 全局启用状态
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
}
```

### 22.17 Project Review Config (ProjectReviewConfig)

项目级规则配置，覆盖全局规则状态与严重程度。

```go
type ProjectReviewConfig struct {
    ID        uint      `gorm:"primaryKey" json:"id"`
    ProjectID uint      `gorm:"index" json:"project_id"`
    RuleID    uint      `gorm:"index" json:"rule_id"`
    IsEnabled bool      `gorm:"default:true" json:"is_enabled"`              // 项目自定义启用状态
    Severity  string    `gorm:"size:16" json:"severity"`                     // 项目自定义严重程度（空=用默认）
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
```

### 22.18 Task Review Rule (TaskReviewRule)

任务执行时实际使用的评审规则快照（包含截断记录）。

```go
type TaskReviewRule struct {
    ID          uint      `gorm:"primaryKey" json:"id"`
    TaskID      uint      `gorm:"index;not null" json:"task_id"`
    RuleID      *uint     `gorm:"index" json:"rule_id"`                        // NULL=规则已删除
    RuleCode    string    `gorm:"size:64;not null" json:"rule_code"`
    Name        string    `gorm:"size:100" json:"name"`
    Category    string    `gorm:"size:32" json:"category"`
    Severity    string    `gorm:"size:16" json:"severity"`
    SortOrder   int       `json:"sort_order"`
    WasSelected bool      `json:"was_selected"`                                // true=传入Prompt，false=被截断
    IssueCount  int       `gorm:"default:0" json:"issue_count"`                // 本次命中Issue数
    CreatedAt   time.Time `json:"created_at"`
}
```

### 22.19 Structured Review Result (Task 扩展字段)

任务表扩展字段，存储结构化评审结果。

```go
// Task 表新增字段
type Task struct {
    // ... 原有字段 ...
    StructuredReviewJSON string `gorm:"type:longtext" json:"structured_review_json"` // 结构化评审结果JSON
    ReviewMarkdown       string `gorm:"type:longtext" json:"review_markdown"`        // 渲染后的Markdown
    DiffFilesJSON        string `gorm:"type:longtext" json:"diff_files_json"`        // diff 文件列表 JSON（含 additions/deletions）
    TriggerType          string `gorm:"size:20;default:'webhook'" json:"trigger_type"`
}
```

### 22.20 Review Category (评审分类字典)

评审分类字典表（rule-stats 通过 LEFT JOIN 拿到中文分类名）。

```go
type ReviewCategory struct {
    ID        uint      `gorm:"primaryKey" json:"id"`
    Code      string    `gorm:"uniqueIndex;size:32" json:"code"`
    Name      string    `gorm:"size:100" json:"name"`           // 中文分类名（如：安全/性能/可读性）
    IsBuiltIn bool      `gorm:"default:false" json:"is_built_in"`
    SortOrder int       `gorm:"default:0" json:"sort_order"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
```

### 22.21 Review Issue (评审 Issue 主表)

AI 结构化评审产出的 Issue 主表，规则命中统计的数据源。

```go
type ReviewIssue struct {
    ID                 uint            `gorm:"primaryKey" json:"id"`
    TaskID             uint            `gorm:"index" json:"task_id"`
    RuleID             *uint           `gorm:"index" json:"rule_id"`                              // NULL=AI 自主发现
    RuleCode           string          `gorm:"size:64;index:idx_rule_code_created,priority:1" json:"rule_code"`
    Category           string          `gorm:"size:32" json:"category"`                          // security/performance/...
    Severity           string          `gorm:"size:16" json:"severity"`                          // critical/high/medium/low/info
    DeductScore        int             `gorm:"default:0;column:deduct_score" json:"deduct_score"` // 该 Issue 扣多少分
    File               string          `gorm:"size:255" json:"file"`
    LineStart          int             `gorm:"default:0" json:"line_start"`
    LineEnd            int             `gorm:"default:0" json:"line_end"`
    CodeSnippet        string          `gorm:"type:text" json:"code_snippet"`
    Message            string          `gorm:"type:text" json:"message"`
    Suggestion         string          `gorm:"type:text" json:"suggestion"`
    Status             string          `gorm:"size:20;default:'pending'" json:"status"`         // pending/accepted/rejected/dismissed
    ResolvedBy         uint            `gorm:"index;default:0" json:"resolved_by"`              // 操作人ID
    ResolvedAt         *time.Time      `json:"resolved_at"`
    RejectReason       string          `gorm:"type:text;column:reject_reason" json:"reject_reason"`
    GitlabDiscussionID string          `gorm:"size:64;column:gitlab_discussion_id" json:"gitlab_discussion_id"`
    IsResolved         bool            `gorm:"default:false;column:is_resolved" json:"is_resolved"`
    CreatedAt          time.Time       `gorm:"index:idx_rule_code_created,priority:2" json:"created_at"`
    DeletedAt          gorm.DeletedAt  `gorm:"index" json:"-"`                                  // soft delete（统计用默认 scope）
}
```

> **复合索引** `(rule_code, created_at)`：支撑规则命中统计的高频查询（按规则代码 + 时间范围聚合）。
>
> **soft delete 策略**：`gorm.DeletedAt` 启用后，`engine/persistor.go` 的 `Delete` 操作自动变为 soft delete（保留记录供审计与统计）；规则统计接口使用**默认 scope**（与任务详情列表一致），避免重试场景下软删记录与新记录重复统计。

### 22.22 LLM Call Log (LLM 调用流水)

全量 LLM 调用的按行审计日志，支撑 Token 用量监控与成本核算。

```go
type LLMCallLog struct {
    ID               uint      `gorm:"primaryKey" json:"id"`
    TaskID           *uint     `gorm:"index:idx_task_call_type,priority:1" json:"task_id,omitempty"`
    ModelID          *uint     `gorm:"index:idx_model_created,priority:2" json:"model_id,omitempty"` // NULL=OpenCode
    Provider         string    `gorm:"size:32;not null;default:'';index" json:"provider"`
    ModelName        string    `gorm:"size:128;not null;default:'';index" json:"model_name"`         // model_id 缺失时的 fallback 展示
    CallType         string    `gorm:"size:32;not null;default:'';index:idx_call_type_created,priority:2" json:"call_type"` // score/deep_review
    Caller           string    `gorm:"size:64;not null;default:'';index" json:"caller"`             // LLMService/PrimaryFallback/JSONRetry
    PromptTokens     int       `gorm:"not null;default:0" json:"prompt_tokens"`
    CompletionTokens int       `gorm:"not null;default:0" json:"completion_tokens"`
    CachedTokens     int       `gorm:"not null;default:0" json:"cached_tokens"`
    TotalTokens      int       `gorm:"not null;default:0" json:"total_tokens"`
    DurationMs       int       `gorm:"not null;default:0" json:"duration_ms"`
    Status           string    `gorm:"size:16;not null;default:'success';index:idx_status_created,priority:1" json:"status"` // success/failed
    ErrorMsg         string    `gorm:"type:text" json:"error_msg,omitempty"`
    CostCents        int64     `gorm:"not null;default:0" json:"cost_cents"`                       // 按模型价格计算的成本（cents）
    CreatedAt        time.Time `gorm:"index:idx_call_type_created,priority:1;index:idx_model_created,priority:1;index:idx_status_created,priority:2;not null" json:"created_at"`
}
```

> **MVP 范围**：当前仅 `call_type=score`（AI review 调用）落库；`call_type=deep_review`（OpenCode 深度评审）在 schema 中预留。
>
> **复合索引**：覆盖 `task_id+call_type`、`model_id+created_at`、`status+created_at`、`call_type+created_at` 四种高频查询路径。

---

## 22. 核心业务流程

### 22.1 GitLab Webhook Processing Flow

```
GitLab sends "Note Hook" POST request
    ↓
[API Layer] Receive payload, verify Secret Token
    ↓
[Parse Layer] Extract fields:
    - project.web_url → match local project
    - object_attributes.note → check content
    - merge_request.iid / author / source_branch / target_branch
    ↓
[Filter Layer] Check:
    ① Project exists?
    ② AI enabled?
    ↓ If either fails → log and discard
[Trigger Type]
    ├─ starts with "@AI" → chat, trigger_source = manual
    └─ other → ignore
    ↓
[Persist Layer] Create Task record
    ↓
[Execute Layer] Run task in goroutine
    ↓
[OpenCode Layer]
    1. Create OpenCode Session
    2. Create project dir
    3. Update Task StartedAt = now()
    4. Choose template per trigger_source + task_type
    5. Replace vars (CLONE_URL, USER_INPUT, MR_DIFF, ...)
    6. Send Prompt to OpenCode
    7. Get AI response
    ↓
[Callback Layer] Update Task:
    - status = success/failed
    - CompletedAt = now()
    - DurationSec = calculated
    - OpencodeSessionID, AIResponse filled
    ↓
[Notify Layer] Post AI comment to MR
```

### 22.2 AI Real-time Conversation Flow

```
User clicks [AI Conversation] button
    ↓
Open dialog, load history (GET /api/v1/tasks/:id/messages)
    ↓
Connect SSE stream (GET /api/v1/tasks/:id/events?token=xxx)
    ↓
User types new prompt, click send
    ↓
POST /api/v1/tasks/:id/messages
    ↓
Backend calls OpenCode SendPromptAsync(sessionID, prompt)
    ↓
SSE receives NDJSON events:
    - delta: text fragment
    - part_updated: tool/bash/edit/write/patch/skill
    - finish: AI response complete
```

### 22.3 MR Status Sync Flow

```
Background cron (per mr_sync_interval_sec, default 60s)
    ↓
Query all opened MergeRequestReviewLog
    ↓
For each opened MR:
    - Find associated Project
    - Call GitLab API to refresh mr_state, mr_title, is_draft, commits, mr_created_at
    - If mr_state changed to merged/closed → update local
    ↓
Update SyncedAt = now()
```

### 22.4 Task Timeout Flow

```
Cron (every 10 sec)
    ↓
Query status = running AND started_at < now() - TaskTimeoutMin
    ↓
Call OpenCode abort
    ↓
Update Task:
    - status = timeout
    - CompletedAt = now()
    - ErrorMsg = "Task timeout"
```

### 22.5 Score Threshold Trigger Flow

```
MR change triggers AI review
    ↓
AI completes review, extracts "AI评分：xx分"
    ↓
checkThresholdAndTrigger(score, threshold)
    ↓
Conditions:
    ① score < threshold?
    ② score > 0?
    ③ project AIEnabled == true?
    ↓ If any fails → ignore
[Trigger deep review]
    - Create Task, trigger_source = "score_threshold"
    - Comment on MR
    - Call OpenCode with ReviewTemplate
    - Post result to MR
```

### 22.6 Report Generation & Send Flow

```
Save ReportConfig
    ↓
service.ReloadReportCron() — hot reload
    ↓
Mutex.Lock, stop old weekly/monthly, register new
Mutex.Unlock
    ↓
Build period (natural day boundary, 00:00 aligned)
    ↓
Query stats
    ↓
Render HTML (Outlook 2016 compatible table layout)
    ↓
Write ReportLog
    ↓
If SendEnabled: send emails
```

### 22.7 AI Review + LLM Primary/Backup Switching Flow

```
GitLab MR Webhook → ExecuteAIReviewTask
    ↓
project.DefaultModelID != nil?
    ├─ yes → Force specific model
    └─ no → Global primary/backup chain
        ↓
Find primary: is_primary = true AND status = active
    ↓
ChatCompletion(primary, prompt)
    ↓
Success?
    ├─ yes → Record UsedModelID, return
    └─ no → Iterate backups by backup_order
        ↓
Try backup 1, 2, 3...
    ↓
Any backup success → Record UsedModelID
None success → Task failed
```

### 22.8 Frontend Auth Intercept Flow

```
Page load → js/auth.js
    ↓
Save native fetch as window._origFetch
    ↓
Override window.fetch:
    - Auto inject Authorization Bearer token for API calls
    - URL ?token= overrides Header
    - 401 → redirectToLogin()
```

---

## 23. 接口清单

### 22.1 Auth

| Method | Path | Description | Implemented |
|------|------|------|----------|
| POST | /api/v1/login | User login (password) | ✅ |
| POST | /api/v1/logout | User logout | ✅ |
| GET | /api/v1/auth/gitlab | GitLab OAuth redirect | ✅ |
| GET | /api/v1/auth/gitlab/callback | GitLab OAuth callback | ✅ |
| GET | /api/v1/users/me | Current user info | ✅ |
| PUT | /api/v1/users/password | Change password | ✅ |

### 22.2 Statistics Dashboard (Home)

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/statistics | KPI + trend + TOP10 data | ✅ |
| GET | /api/v1/dashboard/stats | Dashboard statistics | ✅ |
| GET | /api/v1/dashboard/trends | Trend data | ✅ |
| GET | /api/v1/dashboard/recent-projects | Recent active projects | ✅ |
| GET | /api/v1/dashboard/recent-failures | Recent failures | ✅ |
| GET | /api/v1/dashboard/task-distribution | Task distribution | ✅ |
| GET | /api/v1/dashboard/token-summary | 首页 Token 用量摘要（admin only） | ✅ |

### 22.3 Token Usage (admin only)

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/token-usage/overview | 总览 KPI（调用次数/总 tokens/平均耗时/成功率等） | ✅ |
| GET | /api/v1/token-usage/trend | 用量趋势（today 按小时，其他按天） | ✅ |
| GET | /api/v1/token-usage/by-model | 按模型聚合 | ✅ |
| GET | /api/v1/token-usage/by-project | 按项目聚合 | ✅ |
| GET | /api/v1/token-usage/by-author | 按作者聚合 | ✅ |
| GET | /api/v1/token-usage/calls | 调用明细（分页 + 多维筛选） | ✅ |
| GET | /api/v1/token-usage/by-task | 单任务 Token 用量（任务详情 Tab 使用） | ✅ |

### 22.4 Rule Stats (规则命中统计)

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/rule-stats/overview | 总览 KPI（命中规则/总命中/涉及开发者/修复率+误报率+待处理率） | ✅ |
| GET | /api/v1/rule-stats/by-rule/:code | 单规则钻取（元信息 + KPI + 趋势 + 项目/作者 TOP + 最近命中） | ✅ |
| GET | /api/v1/rule-stats/recent-issues | 最近命中分页（含代码片段） | ✅ |

### 22.5 MR Statistics

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/mr-review-logs | List (paginated, filtered, aggregated stats) | ✅ |

### 22.6 MR Review Log

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/mr-review-logs | List (paginated, filtered, aggregated stats) | ✅ |
| GET | /api/v1/mr-review-logs/:id | Detail | ✅ |
| POST | /api/v1/mr-review-logs/:id/mark-as-draft | Mark Draft | ✅ |
| POST | /api/v1/mr-review-logs/:id/mark-as-ready | Mark Ready | ✅ |
| GET | /api/v1/mr-review-logs/projects | Project dropdown | ✅ |
| GET | /api/v1/mr-review-logs/authors | Author dropdown | ✅ |

### 22.5 Projects

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/projects | Project list | ✅ |
| POST | /api/v1/projects | Create project | ✅ |
| GET | /api/v1/projects/:id | Project detail | ✅ |
| PUT | /api/v1/projects/:id | Edit project | ✅ |
| DELETE | /api/v1/projects/:id | Delete project | ✅ |
| GET | /api/v1/projects/:id/tasks | Project tasks | ✅ |
| GET | /api/v1/projects/options | Project dropdown (public) | ✅ |

### 22.6 Tasks

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/tasks | Global task list | ✅ |
| GET | /api/v1/tasks/:id | Task detail (with used_model preload) | ✅ |
| POST | /api/v1/tasks | Create task (admin) | ✅ |
| POST | /api/v1/tasks/:id/execute | Execute task (admin) | ✅ |
| POST | /api/v1/tasks/:id/retry | Retry task | ✅ |
| POST | /api/v1/tasks/:id/stop | Stop task | ✅ |
| POST | /api/v1/tasks/:id/messages | Send message (AI chat) | ✅ |
| GET | /api/v1/tasks/:id/events | SSE event stream | ✅ |
| GET | /api/v1/tasks/:id/logs | Task logs | ✅ |
| GET | /api/v1/tasks/:id/review-comments | List review comments | ✅ |
| DELETE | /api/v1/tasks/:id/session | Delete OpenCode session (admin) | ✅ |

### 22.7 Webhook

| Method | Path | Description | Implemented |
|------|------|------|----------|
| POST | /api/v1/webhooks/gitlab | GitLab Webhook (Note + MR) | ✅ |
| POST | /api/v1/tasks/callback | Task callback from OpenCode | ✅ |

### 22.8 Templates

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/templates | Template list | ✅ |
| POST | /api/v1/templates | Create template | ✅ |
| GET | /api/v1/templates/:id | Template detail | ✅ |
| PUT | /api/v1/templates/:id | Edit template | ✅ |
| DELETE | /api/v1/templates/:id | Delete template | ✅ |
| POST | /api/v1/templates/:id/clone | Clone template | ✅ |

### 22.9 Pools

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/pools | Pool list | ✅ |
| POST | /api/v1/pools | Create pool | ✅ |
| GET | /api/v1/pools/:id | Pool detail | ✅ |
| PUT | /api/v1/pools/:id | Edit pool | ✅ |
| DELETE | /api/v1/pools/:id | Delete pool | ✅ |
| POST | /api/v1/pools/test | Health test (any pool) | ✅ |
| POST | /api/v1/pools/:id/check | Health check (specific pool) | ✅ |
| PUT | /api/v1/pools/:id/toggle | Enable/disable pool | ✅ |
| PUT | /api/v1/pools/:id/default | Set default | ✅ |
| DELETE | /api/v1/pools/:id/default | Unset default | ✅ |
| GET | /api/v1/pools/:id/skills | Get pool skills | ✅ |

### 22.10 LLM Models

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/models | Model list | ✅ |
| POST | /api/v1/models | Create model | ✅ |
| GET | /api/v1/models/:id | Model detail | ✅ |
| GET | /api/v1/models/:id/edit | Model detail for edit | ✅ |
| PUT | /api/v1/models/:id | Edit model | ✅ |
| DELETE | /api/v1/models/:id | Delete model | ✅ |
| GET | /api/v1/models/default | Get default model | ✅ |
| PUT | /api/v1/models/:id/default | Set default | ✅ |
| DELETE | /api/v1/models/:id/default | Unset default | ✅ |
| POST | /api/v1/models/test | Create and test model | ✅ |
| POST | /api/v1/models/:id/check | API health check | ✅ |

### 22.11 WeCom Notifier

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/notifiers | Notifier list | ✅ |
| POST | /api/v1/notifiers | Create notifier | ✅ |
| GET | /api/v1/notifiers/:id | Notifier detail | ✅ |
| PUT | /api/v1/notifiers/:id | Edit notifier | ✅ |
| PUT | /api/v1/notifiers/:id/template | Update message template | ✅ |
| DELETE | /api/v1/notifiers/:id | Delete notifier | ✅ |
| POST | /api/v1/notifiers/:id/test | Send test message | ✅ |
| PUT | /api/v1/notifiers/:id/toggle | Enable/disable notifier | ✅ |

### 22.12 Member Mappings

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/member-mappings | Mapping list | ✅ |
| POST | /api/v1/member-mappings | Create mapping | ✅ |
| GET | /api/v1/member-mappings/:id | Mapping detail | ✅ |
| PUT | /api/v1/member-mappings/:id | Edit mapping | ✅ |
| DELETE | /api/v1/member-mappings/:id | Delete mapping | ✅ |
| GET | /api/v1/member-mappings/git-users | GitLab user list | ✅ |
| GET | /api/v1/member-mappings/check | Check mapping status | ✅ |

### 22.13 SMTP & Recipients

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/reports/smtp | Get SMTP config | ✅ |
| PUT | /api/v1/reports/smtp | Update SMTP config | ✅ |
| POST | /api/v1/reports/smtp/test | Test SMTP | ✅ |
| GET | /api/v1/reports/recipients | Recipient list | ✅ |
| POST | /api/v1/reports/recipients | Add recipient | ✅ |
| PUT | /api/v1/reports/recipients/:id | Edit recipient | ✅ |
| DELETE | /api/v1/reports/recipients/:id | Delete recipient | ✅ |

### 22.14 Report Management

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/reports/configs | Report config list | ✅ |
| PUT | /api/v1/reports/configs/:id | Update report config | ✅ |
| GET | /api/v1/reports/logs | Report log list | ✅ |
| POST | /api/v1/reports/preview | Preview report | ✅ |
| POST | /api/v1/reports/send | Manually send report | ✅ |

### 22.15 Users (Admin)

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/users | User list | ✅ |
| POST | /api/v1/users | Create user | ✅ |
| PUT | /api/v1/users/:id | Update user | ✅ |
| DELETE | /api/v1/users/:id | Delete user | ✅ |
| POST | /api/v1/users/:id/reset-password | Reset password | ✅ |

### 22.16 System

| Method | Path | Description | Implemented |
|------|------|------|----------|
| GET | /api/v1/system/config | Get system config | ✅ |
| PUT | /api/v1/system/config | Update system config | ✅ |
| GET | /api/v1/system/logs | Operation log list | ✅ |
| DELETE | /api/v1/system/logs | Clear operation logs | ✅ |
| GET | /api/v1/system/info | System info | ✅ |

---

## 24. 技术栈

| Layer | Tech | Description |
|------|------|------|
| Backend | Go 1.26+ + Gin | HTTP framework |
| ORM | GORM | Database operations |
| DB | MySQL 8.0+ | Primary database |
| Task Execution | OpenCode / LLM APIs | External AI services |
| Cron | robfig/cron | Scheduled tasks (health check, timeout, reports) |
| Logging | Uber Zap | Structured logs, ISO8601 |
| Encryption | AES-256-GCM / bcrypt | Sensitive data encryption + password hashing |
| Frontend | HTML + TailwindCSS + Vanilla JS | No framework dependency |
| Charts | ECharts / Chart.js | Dashboard charts |
| Auth | bcrypt + Bearer Token | Password hash, token persistance 7 days |
| Email | Custom SMTP LOGIN | Pure TCP + textproto, compatible with Exchange/Office365 |
| Markdown | marked.js + highlight.js | Skill details, AI chat messages |
| Container | Docker Multi-stage | ~48.5MB, Alpine Linux, Asia/Shanghai timezone |

---

## 25. 附录：认证与鉴权

### 25.1 Auth Flow

1. User submits username/password via `/api/v1/login`
2. Backend verifies with bcrypt
3. Generate random 32-byte Token, store in DB (`tokens` table), valid 7 days
4. Return Token to frontend
5. Frontend stores in `localStorage.auth_token`

### 25.2 Authorization Flow

1. `auth.js` globally intercepts all `fetch` calls
2. API requests auto-inject `Authorization: Bearer <token>`
3. URL `?token=xxx` **overrides** Header token (EventSource 401 fix)
4. Backend `Auth()` middleware verifies token
5. Frontend 401 → auto redirect to login

### 25.3 Whitelist Paths

| Path Pattern | Description |
|----------|------|
| `/api/v1/login` | 用户登录（密码） |
| `/api/v1/logout` | 用户登出 |
| `/api/v1/auth/gitlab` | GitLab OAuth 授权跳转 |
| `/api/v1/auth/gitlab/callback` | GitLab OAuth 回调 |
| `/api/v1/webhooks/gitlab` | GitLab Webhook |
| `/api/v1/tasks/callback` | OpenCode 任务回调 |
| `/health` | 健康检查 |
| 非 `/api/` 前缀 | 静态文件 |

### 22.4 Sensitive Data Encryption

| Field | Algorithm | Location | Description |
|------|----------|----------|------|
| `User.Password` | bcrypt | Local DB | Irreversible hash |
| `ResourcePool.Password` | AES-256-GCM | Local DB | Decryptable |
| `ResourcePool.APIKey` | AES-256-GCM | Local DB | Decryptable |
| `LLMModel.APIKey` | AES-256-GCM | Local DB | Decryptable |
| `WeComNotifier.WebhookKey` | AES-256-GCM | Local DB | Decryptable |
| `SMTPConfig.Password` | Plaintext | Local DB | Use with TLS |

> Encryption key injected via `ENCRYPTION_KEY` env var, must be 32 bytes.

