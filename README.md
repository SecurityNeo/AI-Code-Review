<div align="center">

# CodeGuard

**面向 GitLab 工作流的多智能体自动化代码审查门禁系统**
<br>
**Multi-Agent Automated Code Review Gate for GitLab**

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Gin](https://img.shields.io/badge/Gin-v1.9+-009485?logo=go&logoColor=white)](https://gin-gonic.com)
[![MySQL](https://img.shields.io/badge/MySQL-8.0-4479A1?logo=mysql&logoColor=white)](https://mysql.com)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker&logoColor=white)](https://docker.com)
[![Code Review](https://img.shields.io/badge/AI-Code%20Review-8A2BE2)](https://github.com/SecurityNeo/CodeGuard)

</div>

---

CodeGuard 是一个基于多智能体流水线（Multi-Agent Pipeline）的自动化代码审查平台，专为 GitLab Merge Request 工作流设计。系统在 MR 创建或评论触发时，自动编排多个专业 Agent 对代码变更进行理解、检索、规则匹配、信号聚合与评分，最终在代码合入前拦截潜在风险。

与传统单轮大模型审查不同，CodeGuard 将代码评审拆解为可观测、可干预、可进化的流水线节点，并通过结构化 Issue、规则库沉淀与人工复核闭环，让 AI 审查从“一次性评语”变成“可持续运营的质量工程”。

---

## 核心能力

### 多智能体流水线评审

CodeGuard 不依赖单轮 LLM 直接输出结论，而是将审查任务编排为多阶段智能体流水线：

| 阶段 | Agent | 职责 |
|------|-------|------|
| 1 | **代码理解器** | AST 跨文件解析、依赖图谱构建、变更影响面分析 |
| 2 | **知识图谱检索** | 基于向量嵌入的历史相似 Issue 召回，防止同类问题反复出现 |
| 3 | **规则引擎** | 匹配内置多语言规则库（通用 / Go / Python / 前端 / Java），按项目配置启用与覆盖 |
| 4 | **信号聚合器** | 汇聚代码理解、知识图谱、规则引擎三路信号，生成带上下文的多维审查线索 |
| 5 | **评分引擎** | 对聚合信号进行 3 次独立 LLM 评分取中位数，消除单轮模型波动导致的误判 |
| 6 | **报告生成器** | 输出结构化 Issue 列表，精确到文件与行号，支持逐条采纳/拒绝/挂起 |

流水线支持可视化追踪，每个 Agent 的输入输出、耗时与 Token 消耗均可审计。

### 算法与工程亮点

- **代码映射与行号校正**：GitLab diff 缺少标准 `diff --git` 头时自动注入头信息解析，建立原始代码行号 ↔ 变更行号的精确映射，Issue 定位不再漂移
- **信号聚合评分算法**：汇聚代码理解、知识图谱、规则引擎三路信号后分步评分，3 次独立评分取中位数，避免单轮 LLM 方差导致的高估或误判
- **Issue 向量相似检测**：采用向量嵌入 + T-SNE 投影，支持规则孵化阶段的自动去重与聚类，也用于评审时的历史相似问题关联推荐
- **Token 级成本管控**：每次 LLM 调用精确记录 prompt / completion / cached tokens 与成本，异常时自动指数退避重试，避免资损与无限重试
- **规则自进化**：从历史 Issue 自动聚类提炼候选规则，经模拟测试与相似检测后发布，形成可生长的审查知识库

### MCP Server：AI 编辑器的结构化能力接口

CodeGuard 内置 MCP（Model Context Protocol）Server，向 Cursor、Windsurf 等 AI 编辑器暴露标准化工具集：

- 查询当前项目未解决的结构化 Issue
- 按 Issue ID 采纳/拒绝/忽略审查意见
- 注入历史复核意见后重试审查任务
- 查看资源池与大模型的实时健康状态

AI 编辑器可以直接调用 CodeGuard 的审查数据与执行能力，实现“编辑器内闭环修复”。

### 系统级工程配套

- **模型高可用调度**：每个项目独立配置主模型与备用模型，主模型 502/503/504 或超时后自动切换；指数退避重试参数全部 Web 可配置
- **对象存储分离**：MR 差异元数据存 MySQL，原始 diff 文本存入 S3-compatible 对象存储，防止大仓库差异撑爆数据库
- **通知与治理**：企业微信机器人实时推送、SMTP 周报月报、站内信中心、通知规则引擎（支持 24h→72h→240h 升级路由与自动归档）
- **审计与权限**：bcrypt 密码存储、JWT 鉴权、GitLab OAuth 登录、全量操作日志；普通用户仅可见自己的任务与 MR 记录
- **监控告警**：资源池与模型异常持续达阈值后自动企业微信告警，恢复后发送恢复通知

---

## 快速开始

### Docker 启动

```bash
cat > .env <<'EOF'
DB_HOST=127.0.0.1
DB_NAME=codeguard
DB_PASSWORD=change_me
ENCRYPTION_KEY=your_32_byte_key_here!!
CODEGUARD_WORKSPACE=/data/codeguard/repos

# GitLab（当项目未单独配置 AccessToken 时的全局 fallback）
GITLAB_TOKEN=your_gitlab_personal_token
EOF

docker run -d -p 8080:8080 --env-file .env \
  docker.m.daocloud.io/securityneo/ai-code-review:latest
```

### 首次配置

1. 访问 `http://localhost:8080/login.html`  
   默认管理员账号：`admin / admin123`（首次登录后请立即修改密码）

2. 在「系统管理 - 对象存储」中配置 S3-compatible 存储接入点  
   用于存储原始 diff 文本与构建产物；不配置则差异数据仅保存在 MySQL

3. **（推荐）配置 GitLab OAuth 单点登录**  
   进入「系统管理 - OAuth 配置」，填入 GitLab Application 的 Client ID 与 Client Secret，回调地址固定为 `http://<host>/api/v1/auth/gitlab/callback`。启用后团队成员可直接使用 GitLab 账号登录，无需单独维护密码

4. 在「项目管理」中绑定 GitLab 仓库，配置项目级 AccessToken 或依赖全局 `GITLAB_TOKEN`

5. 在「大模型管理」中配置 LLM 提供商（支持 OpenAI、vLLM、OpenCode 等），设置主模型与备用模型

6. 将 `http://<host>/api/v1/webhooks/gitlab` 填入 GitLab 项目的 Webhook 地址，勾选 **Merge Request events** 与 **Note events**

7. 在「系统管理」中调整评分阈值、通知策略、规则库启用范围与 LLM 重试参数

---

## 技术架构

```
┌──────────────────────────────────────────────────────────────────────┐
│                        Client Browser                                │
│        (Vanilla HTML + TailwindCSS + Dark/Light/System 三态主题)         │
└──────────────────────────────┬───────────────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────────────┐
│                     Gin HTTP Server (Go 1.26)                        │
│   JWT Auth · CORS · Recovery · Static File Serving (prototype/)      │
└──────────────┬──────────────┬──────────────┬─────────────────────────┘
               │              │              │
   ┌───────────▼───┐ ┌──────▼──────┐ ┌─────▼──────┐ ┌───────────────┐
   │    Dashboard   │ │  MCP API    │ │  Webhook   │ │    Report     │
   │    Handler     │ │  SSE/Socket │ │  Handler   │ │    Handler    │
   └───────────────┘ └─────────────┘ └────────────┘ └───────────────┘
               │                                         │
               │                                         │
┌──────────────┴─────────────────────────────────────────┴─────────────┐
│                           Service Layer                                │
│   Task · Project · Rule · Model · User · Token · Notifier · MR Log   │
└───────────────────────────────────┬──────────────────────────────────┘
                                    │
              ┌─────────────────────┼─────────────────────┐
              │                     │                     │
              ▼                     ▼                     ▼
   ┌──────────────────────┐ ┌──────────────┐ ┌────────────────────────┐
   │   Agent Pipeline     │ │  GORM + MySQL │ │   Object Storage       │
   │                      │ │              │ │   (S3-compatible)      │
   │  ┌──────────────┐    │ │  · tasks     │ │                        │
   │  │ 代码理解器    │──┐ │ │  · issues    │ │  · raw_diff_full       │
   │  │(AST/依赖图谱) │  │ │ │  · rules     │ │  · build artifacts     │
   │  └──────────────┘  │ │ │  · mr_stats  │ │  · rule incubation     │
   │          │         │ │ │  · token_log │ │     simulation results   │
   │          ▼         │ │ └──────────────┘ └────────────────────────┘
   │  ┌──────────────┐  │ │
   │  │ 知识图谱检索  │  │ │       ┌─────────┐   ┌──────────┐
   │  │(向量相似召回) │  │ │       │  Cron   │   │ Notifier │
   │  └──────────────┘  │ │       │ Scheduler│   │ Service  │
   │          │         │ │       │         │   │          │
   │          ▼         │ │       │·状态同步  │   │·WeCom    │
   │  ┌──────────────┐  │ │       │·周月报   │   │·Email    │
   │  │ 规则引擎      │──┤ │       │·超时巡检  │   │·站内信   │
   │  │(多语言规则库) │  │ │       └─────────┘   └──────────┘
   │  └──────────────┘  │ │
   │          │         │ │
   │          ▼         │ │       ┌──────────────┐
   │  ┌──────────────┐  │ │       │   OpenCode   │
   │  │ 信号聚合器    │  │ │       │   Pool API   │
   │  │(多源置信汇聚) │  │ │       └──────────────┘
   │  └──────────────┘  │ │
   │          │         │ │
   │          ▼         │ │       ┌──────────────┐
   │  ┌──────────────┐  │ │       │   GitLab     │
   │  │ 评分引擎      │  └─┼──────→│   API/CE/EE  │
   │  │(3次中位数)   │    │       │              │
   │  └──────────────┘    │       │·MR Webhook   │
   │          │           │       │·Diff Fetch   │
   └──────────┼───────────┘       │·Comment Push │
              │                   └──────────────┘
              ▼
   ┌──────────────────────┐
   │    LLM Router        │
   │  (主模型 / 备用 /     │
   │   故障切换 / 退避重试)  │
   └──────────┬───────────┘
              │
      ┌───────┴───────┐
      ▼               ▼
  ┌───────┐      ┌────────┐      ┌──────────┐
  │ OpenAI │      │ vLLM   │      │ Claude   │
  │ (GPT)  │      │(私有)  │      │(Anthropic)│
  └───────┘      └────────┘      └──────────┘
```

---

## License

MIT License © 2026 Li Hu, UNICLOUD
