package tools

import (
	"encoding/json"

	"github.com/ai-optimizer/backend/internal/mcp"
)

// RegisterAllTools 注册所有 MCP Tools
func RegisterAllTools(server interface {
	RegisterTool(name, description string, schema json.RawMessage, handler mcp.ToolHandler, annotations ...*mcp.ToolAnnotations)
}) {
	// 任务查询（只读）
	server.RegisterTool("list_tasks", "查询我的代码评审任务列表。当用户问'最近的评审任务''失败的任务有哪些'时使用。支持按状态(running/pending/failed/success/stopped)和项目过滤。是获取 task_id 的入口。", listTasksSchema, handleListTasks, &mcp.ToolAnnotations{Title: "查询任务列表", ReadOnlyHint: true})
	server.RegisterTool("get_task", "获取单个评审任务的完整详情（含 MR 信息、评审得分、Issue 数量、使用模型）。需要已知 task_id（通常从 list_tasks 获取）。当用户问'任务 N 怎么样了'时使用。", getTaskSchema, handleGetTask, &mcp.ToolAnnotations{Title: "获取任务详情", ReadOnlyHint: true})
	server.RegisterTool("get_task_pipeline", "查询任务 Pipeline 各阶段执行详情，用于定位'卡在哪一步'或'为什么失败'。需要 task_id。", getTaskPipelineSchema, handleGetTaskPipeline, &mcp.ToolAnnotations{Title: "获取任务 Pipeline", ReadOnlyHint: true})
	server.RegisterTool("get_mr_review", "获取指定任务的 AI 代码评审结果（Issue 列表）。当用户问'这个 MR 有什么问题''帮我 review 一下''找出安全隐患'时使用。支持按 severity 和 category 过滤。需要 task_id。", getMRReviewSchema, handleGetMRReview, &mcp.ToolAnnotations{Title: "获取评审结果", ReadOnlyHint: true})
	// Issue 查询（只读）
	server.RegisterTool("get_issue_detail", "获取单个 Issue 的完整详情（含代码上下文、详细描述、修复方案）。当用户追问'第 N 个问题具体是什么'时使用。需要 issue_id。", getIssueDetailSchema, handleGetIssueDetail, &mcp.ToolAnnotations{Title: "获取 Issue 详情", ReadOnlyHint: true})
	server.RegisterTool("list_pending_issues", "查询当前用户的待处理 Issue 列表（pending / pending_inherited）。当用户问'我有什么待处理的问题''我的待办'时使用。返回结果包含 index 编号，后续用户可能用'第 N 个'引用。", listPendingIssuesSchema, handleListPendingIssues, &mcp.ToolAnnotations{Title: "查询待处理 Issue", ReadOnlyHint: true})
	server.RegisterTool("list_historical_issues", "查询当前用户已处理的历史 Issue（resolved / false_positive / ignored / auto_archived）。当用户问'我之前处理过哪些问题'时使用。支持按状态过滤。", listHistoricalIssuesSchema, handleListHistoricalIssues, &mcp.ToolAnnotations{Title: "查询历史 Issue", ReadOnlyHint: true})

	// Issue 操作（破坏性）
	server.RegisterTool("resolve_issue", "将单个待处理 Issue 标记为已处理（resolved）。当用户说'这个我修好了''第 N 个已处理'时使用。需要 issue_id，可选择填写 comment 说明。", resolveIssueSchema, handleResolveIssue, &mcp.ToolAnnotations{Title: "处理 Issue", DestructiveHint: true})
	server.RegisterTool("reject_issue", "将单个待处理 Issue 标记为误报（false_positive）。当用户说'这是误报''第 N 个是误报'时使用。需要 issue_id 和 reason（必填，说明误报原因）。", rejectIssueSchema, handleRejectIssue, &mcp.ToolAnnotations{Title: "标记误报", DestructiveHint: true})
	server.RegisterTool("ignore_issue", "将单个待处理 Issue 标记为忽略（ignored）。当用户说'先忽略这个''暂时不处理'时使用。需要 issue_id 和 reason（必填，说明忽略原因）。", ignoreIssueSchema, handleIgnoreIssue, &mcp.ToolAnnotations{Title: "忽略 Issue", DestructiveHint: true})

	// 任务操作（破坏性）
	server.RegisterTool("retry_task", "重试 AI 评审任务。支持对已完成(success)、失败(failed)、停止(stopped)、超时(timeout)的任务重新触发评审；review 任务可附加 user_review_comment 补充复核意见（如'第32行判断有误'），AI 会参考意见进行针对性重审。需要 task_id。", retryTaskSchema, handleRetryTask, &mcp.ToolAnnotations{Title: "重试任务", DestructiveHint: true, IdempotentHint: true})
	server.RegisterTool("stop_task", "停止正在运行或排队中的评审任务（当前阶段完成后终止）。当用户说'停掉那个任务''取消评审'时使用。需要 task_id。", stopTaskSchema, handleStopTask, &mcp.ToolAnnotations{Title: "停止任务", DestructiveHint: true})

	// MR 查询（只读）
	server.RegisterTool("list_merge_requests", "查询 GitLab 上的 MR 列表（包含已触发或未触发 AI 评审的全部 MR）。当用户说'看看最近的 MR''未合并的 MR 有多少'时使用。支持按真实状态(opened/merged/closed)和项目名称过滤。", listMRsSchema, handleListMRs, &mcp.ToolAnnotations{Title: "查询 MR 列表", ReadOnlyHint: true})
	server.RegisterTool("get_merge_request_detail", "获取指定 MR 的评审详情和对应任务信息。当用户问'MR !N 评审结果如何'时使用。需要 project_id 和 mr_iid。", getMRDetailSchema, handleGetMRDetail, &mcp.ToolAnnotations{Title: "获取 MR 详情", ReadOnlyHint: true})

	// 工作台（只读）
	server.RegisterTool("get_workbench", "获取个人工作台数据，包含待处理 Issue 统计（按严重程度分档）、超时预警。当用户说'我的待办'时使用。Admin 可切换 admin 视图。", getWorkbenchSchema, handleGetWorkbench, &mcp.ToolAnnotations{Title: "获取工作台", ReadOnlyHint: true})

	// 大盘统计（只读）
	server.RegisterTool("get_dashboard_stats", "获取代码评审大盘统计（任务量、成功率、失败率、Top 项目）。当用户问'这周评审了多少'时使用。支持 today/week/month。", getDashboardStatsSchema, handleGetDashboardStats, &mcp.ToolAnnotations{Title: "获取大盘统计", ReadOnlyHint: true})
	server.RegisterTool("get_token_usage", "查看 AI 评审 Token 消耗和成本（按天汇总）。当用户问'Token 花了多少''AI 调用成本'时使用。支持 day/project/model 分组。", getTokenUsageSchema, handleGetTokenUsage, &mcp.ToolAnnotations{Title: "获取 Token 用量", ReadOnlyHint: true})

	// 项目查询（只读）
	server.RegisterTool("list_projects", "查询已接入 AI 代码评审的项目列表。当用户问'有哪些项目在用评审'时使用。默认只返回我有任务的项目。", listProjectsSchema, handleListProjects, &mcp.ToolAnnotations{Title: "查询项目列表", ReadOnlyHint: true})

	// 通知（读/写混合）
	server.RegisterTool("list_notifications", "查看站内通知列表（评审完成、升级提醒等）。当用户说'看看有没有新通知'时使用。支持 unread/all 过滤。", listNotificationsSchema, handleListNotifications, &mcp.ToolAnnotations{Title: "查询通知", ReadOnlyHint: true})
	server.RegisterTool("mark_all_read", "一键将当前用户所有未读通知标记为已读。当用户说'全部已读'时使用。这是一个状态变更操作，不要在仅查看通知时调用。", markAllReadSchema, handleMarkAllRead, &mcp.ToolAnnotations{Title: "全部已读", DestructiveHint: true, IdempotentHint: true})
}

// ---------------------- Schemas ----------------------

var listTasksSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"status": {"type": "string", "description": "running|pending|failed|success|stopped"},
		"project_id": {"type": "integer"},
		"author": {"type": "string"},
		"mine": {"type": "boolean", "default": true},
		"all": {"type": "boolean", "default": false},
		"limit": {"type": "integer", "default": 5},
		"offset": {"type": "integer", "default": 0}
	}
}`)

var getTaskSchema = json.RawMessage(`{
	"type": "object",
	"required": ["task_id"],
	"properties": {
		"task_id": {"type": "integer", "description": "任务ID"}
	}
}`)

var getTaskPipelineSchema = json.RawMessage(`{
	"type": "object",
	"required": ["task_id"],
	"properties": {
		"task_id": {"type": "integer"}
	}
}`)

var getMRReviewSchema = json.RawMessage(`{
	"type": "object",
	"required": ["task_id"],
	"properties": {
		"task_id": {"type": "integer"},
		"severity": {"type": "string", "description": "high|medium|low"},
		"category": {"type": "string", "description": "security|performance|style|bug"},
		"limit": {"type": "integer", "default": 10}
	}
}`)

var getIssueDetailSchema = json.RawMessage(`{
	"type": "object",
	"required": ["issue_id"],
	"properties": {
		"issue_id": {"type": "integer", "description": "Issue ID (可通过 get_mr_review 或 get_task 的返回结果中获取)"}
	}
}`)

var retryTaskSchema = json.RawMessage(`{
	"type": "object",
	"required": ["task_id"],
	"properties": {
		"task_id": {"type": "integer", "description": "需要重试的任务 ID"},
		"user_review_comment": {"type": "string", "description": "补充复核意见（可选）。描述上次评审不准确之处，例如：第32行的并发处理判断有误。最多5000字符。review 任务支持。"},
		"selected_comment_ids": {"type": "array", "items": {"type": "integer"}, "description": "选中的历史复核意见 ID 列表（可选）。会将历史意见与新意见一并注入到重试任务中。"}
	}
}`)

var stopTaskSchema = json.RawMessage(`{
	"type": "object",
	"required": ["task_id"],
	"properties": {
		"task_id": {"type": "integer"}
	}
}`)

var listMRsSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"project_name": {"type": "string", "description": "项目名称（精确匹配）"},
		"state": {"type": "string", "default": "opened", "description": "opened|merged|closed|all"},
		"author": {"type": "string"},
		"mine": {"type": "boolean", "default": true},
		"all": {"type": "boolean", "default": false},
		"limit": {"type": "integer", "default": 5},
		"offset": {"type": "integer", "default": 0}
	}
}`)

var getMRDetailSchema = json.RawMessage(`{
	"type": "object",
	"required": ["project_id", "mr_iid"],
	"properties": {
		"project_id": {"type": "integer"},
		"mr_iid": {"type": "integer"}
	}
}`)

var getWorkbenchSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"view": {"type": "string", "default": "developer", "description": "developer|admin"}
	}
}`)

var getDashboardStatsSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"time_range": {"type": "string", "default": "week", "description": "today|week|month"},
		"mine": {"type": "boolean", "default": true},
		"all": {"type": "boolean", "default": false}
	}
}`)

var getTokenUsageSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"time_range": {"type": "string"},
		"group_by": {"type": "string", "default": "day", "description": "day|project|model"}
	}
}`)

var listProjectsSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"mine": {"type": "boolean", "default": true},
		"all": {"type": "boolean", "default": false},
		"limit": {"type": "integer", "default": 10}
	}
}`)

var listNotificationsSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"status": {"type": "string", "default": "unread", "description": "unread|all"},
		"limit": {"type": "integer", "default": 5}
	}
}`)

var markAllReadSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
	}
}`)

// ---------------------- Issue Schemas ----------------------

var listPendingIssuesSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"limit": {"type": "integer", "default": 20, "description": "返回数量上限，最大 50"}
	}
}`)

var listHistoricalIssuesSchema = json.RawMessage(`{
	"type": "object",
	"required": [],
	"properties": {
		"status": {"type": "string", "description": "resolved|false_positive|ignored|auto_archived，为空返回全部"},
		"limit": {"type": "integer", "default": 20, "description": "返回数量上限，最大 50"}
	}
}`)

var resolveIssueSchema = json.RawMessage(`{
	"type": "object",
	"required": ["issue_id"],
	"properties": {
		"issue_id": {"type": "integer", "description": "Issue ID"},
		"comment": {"type": "string", "description": "处理备注（可选）"}
	}
}`)

var rejectIssueSchema = json.RawMessage(`{
	"type": "object",
	"required": ["issue_id", "reason"],
	"properties": {
		"issue_id": {"type": "integer", "description": "Issue ID"},
		"reason": {"type": "string", "description": "误报原因（必填）"}
	}
}`)

var ignoreIssueSchema = json.RawMessage(`{
	"type": "object",
	"required": ["issue_id", "reason"],
	"properties": {
		"issue_id": {"type": "integer", "description": "Issue ID"},
		"reason": {"type": "string", "description": "忽略原因（必填）"}
	}
}`)
