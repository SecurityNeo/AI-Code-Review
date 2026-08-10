package handler

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"github.com/gin-gonic/gin"
)

// PipelineHandler Pipeline 状态查询与事件推送
type PipelineHandler struct {
	Hub *service.PipelineSSEHub // Pipeline review SSE hub (shared with TaskService)
}

func NewPipelineHandler(hub *service.PipelineSSEHub) *PipelineHandler {
	return &PipelineHandler{Hub: hub}
}

// GetPipelineStatus 获取任务的 Pipeline 执行状态
func (h *PipelineHandler) GetPipelineStatus(c *gin.Context) {
	taskID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid task id"})
		return
	}

	// 检查是否存在 Pipeline 执行记录
	var count int64
	model.DB.Model(&model.TaskPipelineExecution{}).Where("task_id = ?", taskID).Count(&count)
	if count == 0 {
		c.JSON(200, gin.H{
			"task_id":     taskID,
			"legacy_mode": true,
			"message":     "该任务在旧模式下执行，无链路数据",
		})
		return
	}

	// 查询所有阶段（parent_id IS NULL 的顶层阶段）
	var executions []model.TaskPipelineExecution
	if err := model.DB.Where("task_id = ? AND parent_id IS NULL", taskID).Order("sort_order ASC").Find(&executions).Error; err != nil {
		c.JSON(500, gin.H{"error": "查询失败"})
		return
	}

	// 兜底：若 count>0 但顶层阶段为空，说明数据异常，回退查询全部记录
	if len(executions) == 0 && count > 0 {
		model.DB.Where("task_id = ?", taskID).Order("sort_order ASC, id ASC").Find(&executions)
	}

	// 构建树形响应
	var stages []gin.H
	for _, exec := range executions {
		stage := buildStageResponse(exec)
		// 查询子阶段（只对分组阶段）
		if exec.StageCode == "batch_review_frame" {
			var children []model.TaskPipelineExecution
			model.DB.Where("parent_id = ?", exec.ID).Order("batch_index ASC, sort_order ASC, id ASC").Find(&children)
			childList := make([]gin.H, 0, len(children))
			for _, child := range children {
				childList = append(childList, buildStageResponse(child))
			}
			stage["children"] = childList
			stage["has_children"] = true
		}
		stages = append(stages, stage)
	}

	// 计算整体进度
	var task model.Task
	model.DB.First(&task, taskID)
	overallStatus := string(task.Status)
	if overallStatus == string(model.TaskRunning) && len(stages) > 0 {
		// 若任务仍在 running，以 pipeline 最后一个阶段状态为准
		last := stages[len(stages)-1]
		if last["status"] == "running" {
			overallStatus = "running"
		}
	}

	c.JSON(200, gin.H{
		"task_id":        taskID,
		"legacy_mode":    false,
		"overall_status": overallStatus,
		"current_stage":  getCurrentStageCode(executions),
		"started_at":     task.StartedAt,
		"completed_at":   task.CompletedAt,
		"duration_sec":   task.DurationSec,
		"stages":         stages,
	})
}

// GetStageDetail 获取阶段详情（含输入输出快照）
func (h *PipelineHandler) GetStageDetail(c *gin.Context) {
	execID, err := strconv.Atoi(c.Param("execution_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid execution id"})
		return
	}

	var exec model.TaskPipelineExecution
	if err := model.DB.First(&exec, execID).Error; err != nil {
		c.JSON(404, gin.H{"error": "阶段不存在"})
		return
	}

	// 解析快照（支持对象存储引用和数据库直接存储）
	var inputSnap, outputSnap map[string]interface{}
	mgr := pipeline.NewSnapshotManager()

	if exec.InputSnapshot != "" {
		var ref pipeline.SnapshotRef
		if err := json.Unmarshal([]byte(exec.InputSnapshot), &ref); err == nil && ref.StorageType == "object_storage" {
			inputSnap, _ = mgr.GetInputSnapshot(&exec)
		} else {
			json.Unmarshal([]byte(exec.InputSnapshot), &inputSnap)
		}
	}
	if exec.OutputSnapshot != "" {
		var ref pipeline.SnapshotRef
		if err := json.Unmarshal([]byte(exec.OutputSnapshot), &ref); err == nil && ref.StorageType == "object_storage" {
			outputSnap, _ = mgr.GetOutputSnapshot(&exec)
		} else {
			json.Unmarshal([]byte(exec.OutputSnapshot), &outputSnap)
		}
	}

	resp := gin.H{
		"execution_id":    exec.ID,
		"stage_code":      exec.StageCode,
		"status":          exec.Status,
		"started_at":      exec.StartedAt,
		"completed_at":    exec.CompletedAt,
		"duration_ms":     exec.DurationMs,
		"input_tokens":    exec.InputTokens,
		"output_tokens":   exec.OutputTokens,
		"model_name":      exec.ModelName,
		"model_id":        exec.LLMModelID,
		"error_message":   exec.ErrorMessage,
		"degraded":        exec.Degraded,
		"degraded_reason": exec.DegradedReason,
		"batch_index":     exec.BatchIndex,
		"batch_total":     exec.BatchTotal,
		"batch_completed": exec.BatchCompleted,
		"input_snapshot":  inputSnap,
		"output_snapshot": outputSnap,
	}

	// Group 阶段（如 batch_review_frame）额外返回子阶段列表，便于前端汇总数据
	if exec.StageCode == "batch_review_frame" {
		var children []model.TaskPipelineExecution
		model.DB.Where("parent_id = ?", exec.ID).Order("batch_index ASC, sort_order ASC, id ASC").Find(&children)
		childList := make([]gin.H, 0, len(children))
		for _, child := range children {
			childList = append(childList, buildStageResponse(child))
		}
		resp["children"] = childList
		resp["has_children"] = true
	}

	c.JSON(200, resp)
}

// SubscribePipelineEvents SSE 推送 Pipeline 事件（事件驱动为主， ticker 兜底）
func (h *PipelineHandler) SubscribePipelineEvents(c *gin.Context) {
	taskID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid task id"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	// 检查任务是否存在 Pipeline 记录
	var count int64
	model.DB.Model(&model.TaskPipelineExecution{}).Where("task_id = ?", taskID).Count(&count)
	if count == 0 {
		c.SSEvent("message", gin.H{"type": "legacy_mode", "task_id": taskID})
		c.Writer.Flush()
		return
	}

	// 首次推送当前全量状态
	h.sendPipelineUpdate(c, taskID)

	// 如果 hub 未初始化，退化为每 2 秒轮询兜底
	if h.Hub == nil {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case <-ticker.C:
				isCompleted := h.sendPipelineUpdate(c, taskID)
				if isCompleted {
					return
				}
			}
		}
	}

	// 事件驱动：订阅 hub，等待 Pipeline 执行时 broadcaster 推送的事件
	client := h.Hub.Subscribe(int64(taskID), c.Writer)
	defer h.Hub.Unsubscribe(int64(taskID), client)

	// ticker 兜底：若 10 秒内无事件，主动推送一次（防止连接被代理静默断开）
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-client.Done:
			return
		case msg := <-client.Chan:
			// 收到事件后组装并推送最新全量状态
			_ = msg // 事件数据仅在 debug 日志中使用，全量状态从数据库组装
			isCompleted := h.sendPipelineUpdate(c, taskID)
			if isCompleted {
				return
			}
		case <-ticker.C:
			isCompleted := h.sendPipelineUpdate(c, taskID)
			if isCompleted {
				return
			}
		}
	}
}

// sendPipelineUpdate 查询当前全量 stages 并通过 SSE 推送（格式与 REST API 完全一致）。
// 返回 true 表示任务已完成（应关闭连接）。
func (h *PipelineHandler) sendPipelineUpdate(c *gin.Context, taskID int) bool {
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return true // 任务不存在，直接结束
	}

	// 查询顶层阶段
	var executions []model.TaskPipelineExecution
	model.DB.Where("task_id = ? AND parent_id IS NULL", taskID).
		Order("sort_order ASC").Find(&executions)

	// 兜底：若顶层阶段为空但 count>0，回退查询全部
	var count int64
	model.DB.Model(&model.TaskPipelineExecution{}).Where("task_id = ?", taskID).Count(&count)
	if len(executions) == 0 && count > 0 {
		model.DB.Where("task_id = ?", taskID).Order("sort_order ASC, id ASC").Find(&executions)
	}

	// 组装 stages（含 children）
	var stages []gin.H
	for _, exec := range executions {
		stage := buildStageResponse(exec)
		if exec.StageCode == "batch_review_frame" {
			var children []model.TaskPipelineExecution
			model.DB.Where("parent_id = ?", exec.ID).Order("sort_order ASC, id ASC").Find(&children)
			childList := make([]gin.H, 0, len(children))
			for _, child := range children {
				childList = append(childList, buildStageResponse(child))
			}
			stage["children"] = childList
			stage["has_children"] = true
		}
		stages = append(stages, stage)
	}

	// 计算整体状态
	overallStatus := string(task.Status)
	if overallStatus == string(model.TaskRunning) && len(stages) > 0 {
		last := stages[len(stages)-1]
		if last["status"] == "running" {
			overallStatus = "running"
		}
	}

	c.SSEvent("message", gin.H{
		"type":           "pipeline_update",
		"task_id":        taskID,
		"legacy_mode":    false,
		"overall_status": overallStatus,
		"started_at":     task.StartedAt,
		"completed_at":   task.CompletedAt,
		"duration_sec":   task.DurationSec,
		"stages":         stages,
	})
	c.Writer.Flush()

	completed := task.Status != model.TaskRunning
	if completed {
		c.SSEvent("message", gin.H{
			"type":    "pipeline_completed",
			"task_id": taskID,
			"status":  overallStatus,
		})
		c.Writer.Flush()
	}
	return completed
}

func buildStageResponse(exec model.TaskPipelineExecution) gin.H {
	resp := gin.H{
		"id":              exec.ID,
		"stage_code":      exec.StageCode,
		"status":          exec.Status,
		"started_at":      exec.StartedAt,
		"completed_at":    exec.CompletedAt,
		"duration_ms":     exec.DurationMs,
		"input_tokens":    exec.InputTokens,
		"output_tokens":   exec.OutputTokens,
		"model_name":      exec.ModelName,
		"error_message":   exec.ErrorMessage,
		"degraded":        exec.Degraded,
		"batch_index":     exec.BatchIndex,
		"batch_total":     exec.BatchTotal,
		"batch_completed": exec.BatchCompleted,
		"has_detail":      exec.InputSnapshot != "" || exec.OutputSnapshot != "",
	}

	// 加载阶段名称
	var stageDef model.ReviewPipelineStage
	if err := model.DB.Where("code = ?", exec.StageCode).First(&stageDef).Error; err == nil {
		resp["name"] = stageDef.Name
		resp["description"] = stageDef.Description
		resp["icon"] = stageDef.Icon
		resp["is_group"] = stageDef.IsGroup
	} else {
		resp["name"] = exec.StageCode
	}
	// 子批次名称优化
	if strings.HasPrefix(exec.StageCode, "batch_review_") {
		idx := strings.TrimPrefix(exec.StageCode, "batch_review_")
		resp["name"] = "批次 " + idx
	}

	return resp
}

func getCurrentStageCode(execs []model.TaskPipelineExecution) string {
	for _, exec := range execs {
		if exec.Status == model.PipelineStageRunning {
			return exec.StageCode
		}
	}
	return ""
}
