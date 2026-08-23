package tools

import (
	"encoding/json"

	"github.com/ai-optimizer/backend/internal/mcp"
)

// RegisterAllTools 注册所有 MCP Tools
func RegisterAllTools(server interface {
	RegisterTool(name, description string, schema json.RawMessage, handler mcp.ToolHandler)
}) {
	// 任务查询
	server.RegisterTool("list_tasks", "查询 MR 评审任务列表", listTasksSchema, handleListTasks)
	server.RegisterTool("get_task", "查询单个任务详情", getTaskSchema, handleGetTask)
	server.RegisterTool("get_task_pipeline", "查询 Pipeline 执行状态", getTaskPipelineSchema, handleGetTaskPipeline)
	server.RegisterTool("get_mr_review", "获取 AI 评审 Issue 列表", getMRReviewSchema, handleGetMRReview)
	// Issue 查询
	server.RegisterTool("get_issue_detail", "查询单个评审 Issue 的详细信息", getIssueDetailSchema, handleGetIssueDetail)

	// 任务操作
	server.RegisterTool("retry_task", "重试失败的任务", retryTaskSchema, handleRetryTask)
	server.RegisterTool("stop_task", "停止正在执行的任务", stopTaskSchema, handleStopTask)

	// MR 查询
	server.RegisterTool("list_merge_requests", "查询 GitLab MR 列表", listMRsSchema, handleListMRs)
	server.RegisterTool("get_merge_request_detail", "查询 MR 详情", getMRDetailSchema, handleGetMRDetail)

	// 工作台
	server.RegisterTool("get_workbench", "获取工作台/控制台数据", getWorkbenchSchema, handleGetWorkbench)

	// 大盘统计
	server.RegisterTool("get_dashboard_stats", "查看大盘统计数据", getDashboardStatsSchema, handleGetDashboardStats)
	server.RegisterTool("get_token_usage", "查看 Token 消耗统计", getTokenUsageSchema, handleGetTokenUsage)

	// 项目查询
	server.RegisterTool("list_projects", "查看项目列表", listProjectsSchema, handleListProjects)

	// 通知
	server.RegisterTool("list_notifications", "查看通知列表", listNotificationsSchema, handleListNotifications)
	server.RegisterTool("mark_all_read", "标记所有通知已读", markAllReadSchema, handleMarkAllRead)
}

// ---------------------- Schemas ----------------------

var listTasksSchema = json.RawMessage(`{
	"type": "object",
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
		"task_id": {"type": "integer"}
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
	"properties": {
		"project_id": {"type": "integer"},
		"state": {"type": "string", "default": "opened"},
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
	"properties": {
		"view": {"type": "string", "default": "developer", "description": "developer|admin"}
	}
}`)

var getDashboardStatsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"time_range": {"type": "string", "default": "week", "description": "today|week|month"},
		"mine": {"type": "boolean", "default": true},
		"all": {"type": "boolean", "default": false}
	}
}`)

var getTokenUsageSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"time_range": {"type": "string"},
		"group_by": {"type": "string", "default": "day", "description": "day|project|model"}
	}
}`)

var listProjectsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"mine": {"type": "boolean", "default": true},
		"all": {"type": "boolean", "default": false},
		"limit": {"type": "integer", "default": 10}
	}
}`)

var listNotificationsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"status": {"type": "string", "default": "unread", "description": "unread|all"},
		"limit": {"type": "integer", "default": 5}
	}
}`)

var markAllReadSchema = json.RawMessage(`{"type": "object", "properties": {}}`)
