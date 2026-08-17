package handler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/internal/service/graphscan"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type WebhookHandler struct{}

func NewWebhookHandler() *WebhookHandler {
	return &WebhookHandler{}
}

type MRInfo struct {
	Title        string
	Author       string
	SourceBranch string
	TargetBranch string
	WebURL       string
	Diff         string
}

func extractProtocolAndHost(projectPath string) (string, string) {
	protocol := "https"
	if strings.HasPrefix(projectPath, "http://") {
		protocol = "http"
		projectPath = strings.TrimPrefix(projectPath, "http://")
	} else if strings.HasPrefix(projectPath, "https://") {
		protocol = "https"
		projectPath = strings.TrimPrefix(projectPath, "https://")
	}
	return protocol, projectPath
}

func fetchMRInfo(projectPath, gitlabToken string, mrIID int) (*MRInfo, error) {
	protocol, path := extractProtocolAndHost(projectPath)
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid project path: %s", projectPath)
	}
	namespaceProject := parts[1]
	namespaceProject = strings.TrimSuffix(namespaceProject, ".git")

	url := fmt.Sprintf("%s://%s/api/v4/projects/%s/merge_requests/%d",
		protocol, parts[0], strings.ReplaceAll(namespaceProject, "/", "%2F"), mrIID)

	zap.L().Debug("fetching MR info", zap.String("url", url))

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", gitlabToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch MR failed: %d", resp.StatusCode)
	}

	var mr map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}

	info := &MRInfo{}
	if title, ok := mr["title"].(string); ok {
		info.Title = title
	}
	if sourceBranch, ok := mr["source_branch"].(string); ok {
		info.SourceBranch = sourceBranch
	}
	if targetBranch, ok := mr["target_branch"].(string); ok {
		info.TargetBranch = targetBranch
	}
	if webURL, ok := mr["web_url"].(string); ok {
		info.WebURL = webURL
	}
	if author, ok := mr["author"].(map[string]interface{}); ok {
		if username, ok := author["username"].(string); ok {
			info.Author = username
		}
	}

	// 获取 MR diff
	diffURL := fmt.Sprintf("%s://%s/api/v4/projects/%s/merge_requests/%d/changes",
		protocol, parts[0], strings.ReplaceAll(namespaceProject, "/", "%2F"), mrIID)
	diffReq, err := http.NewRequest("GET", diffURL, nil)
	if err == nil {
		diffReq.Header.Set("PRIVATE-TOKEN", gitlabToken)
		diffResp, err := client.Do(diffReq)
		if err == nil {
			if diffResp.StatusCode == 200 {
				var diffData map[string]interface{}
				if json.NewDecoder(diffResp.Body).Decode(&diffData) == nil {
					if changes, ok := diffData["changes"].([]interface{}); ok {
						var diffBuilder strings.Builder
						for _, change := range changes {
							if c, ok := change.(map[string]interface{}); ok {
								if diff, ok := c["diff"].(string); ok {
									diffBuilder.WriteString(diff)
									diffBuilder.WriteString("\n")
								}
							}
						}
						info.Diff = diffBuilder.String()
					}
				}
			}
			diffResp.Body.Close()
		}
	}

	return info, nil
}

func postMRComment(projectPath, gitlabToken string, mrIID int, comment string, noteID int) error {
	protocol, path := extractProtocolAndHost(projectPath)
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("invalid project path: %s", projectPath)
	}
	namespaceProject := parts[1]
	namespaceProject = strings.TrimSuffix(namespaceProject, ".git")

	url := fmt.Sprintf("%s://%s/api/v4/projects/%s/merge_requests/%d/notes",
		protocol, parts[0], strings.ReplaceAll(namespaceProject, "/", "%2F"), mrIID)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	body := map[string]interface{}{"body": comment}
	if noteID > 0 {
		body["note_id"] = noteID
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", gitlabToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post comment failed: %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GitLabWebhook 统一处理 GitLab Webhook（note / merge_request）
func (h *WebhookHandler) GitLabWebhook(c *gin.Context) {
	zap.L().Info("========== Webhook received ==========")
	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		zap.L().Warn("invalid webhook payload", zap.Error(err))
		c.JSON(400, gin.H{"error": "invalid payload"})
		return
	}

	zap.L().Debug("webhook payload keys", zap.Any("keys", getMapKeys(payload)))

	objectKind, _ := payload["object_kind"].(string)
	switch objectKind {
	case "note":
		h.handleNoteHook(c, payload)
	case "merge_request":
		h.handleMergeRequestHook(c, payload)
	default:
		zap.L().Debug("ignored unsupported webhook type", zap.String("object_kind", objectKind))
		c.JSON(200, gin.H{"message": "ignored"})
	}
}

// ========== Note Hook 处理（评论触发） ==========

func (h *WebhookHandler) handleNoteHook(c *gin.Context, payload map[string]interface{}) {
	noteableType := ""
	if objAttr, ok := payload["object_attributes"].(map[string]interface{}); ok {
		noteableType, _ = objAttr["noteable_type"].(string)
	}
	if noteableType != "MergeRequest" {
		zap.L().Debug("ignored non-MR note", zap.String("noteable_type", noteableType))
		c.JSON(200, gin.H{"message": "ignored"})
		return
	}

	// 解析基础信息
	note := ""
	noteableIID := 0
	projectID := 0
	noteID := 0
	authorDisplayName := ""

	if user, ok := payload["user"].(map[string]interface{}); ok {
		if name, ok := user["name"].(string); ok {
			authorDisplayName = name
		}
	}

	if objAttr, ok := payload["object_attributes"].(map[string]interface{}); ok {
		note, _ = objAttr["note"].(string)
		if n, ok := objAttr["id"].(float64); ok {
			noteID = int(n)
		}
	}

	if mr, ok := payload["merge_request"].(map[string]interface{}); ok {
		if iid, ok := mr["iid"].(float64); ok {
			noteableIID = int(iid)
		}
	}
	if noteableIID == 0 {
		if objAttr, ok := payload["object_attributes"].(map[string]interface{}); ok {
			if n, ok := objAttr["noteable_id"].(float64); ok {
				noteableIID = int(n)
			}
		}
		if noteableIID == 0 {
			if n, ok := payload["noteable_iid"].(float64); ok {
				noteableIID = int(n)
			}
		}
	}

	if noteID == 0 {
		if n, ok := payload["note"].(map[string]interface{}); ok {
			if id, ok := n["id"].(float64); ok {
				noteID = int(id)
			}
		}
	}

	if projectID == 0 {
		if n, ok := payload["project_id"].(float64); ok {
			projectID = int(n)
		}
	}

	// 忽略 bot 评论
	noteLower := strings.ToLower(note)
	if strings.HasPrefix(note, "消息已接收") ||
		strings.HasPrefix(note, "## ") ||
		strings.Contains(noteLower, "现在我可以看到") ||
		strings.Contains(noteLower, "task completed") ||
		strings.Contains(noteLower, "本次提交总结") ||
		strings.Contains(noteLower, "您的代码审查分数低于") ||
		strings.Contains(noteLower, "ai 评审") ||
		strings.Contains(noteLower, "ai评审") {
		zap.L().Debug("ignored bot comment", zap.String("note", note[:minWebhook(len(note), 30)]))
		c.JSON(200, gin.H{"message": "ignored"})
		return
	}

	if projectID == 0 {
		c.JSON(400, gin.H{"error": "missing project_id"})
		return
	}
	if noteableIID == 0 {
		c.JSON(400, gin.H{"error": "missing noteable_iid"})
		return
	}

	// 获取项目信息
	projectPath := ""
	if proj, ok := payload["project"].(map[string]interface{}); ok {
		if webURL, ok := proj["web_url"].(string); ok {
			projectPath = webURL
		} else if httpURL, ok := proj["http_url"].(string); ok {
			projectPath = strings.TrimSuffix(httpURL, ".git")
		}
	}
	if projectPath == "" {
		c.JSON(400, gin.H{"error": "cannot get project path"})
		return
	}

	var project model.Project
	if err := model.DB.Where("project_path = ? OR project_path = ?",
		projectPath, projectPath+".git").First(&project).Error; err != nil {
		zap.L().Warn("project not found", zap.String("project_path", projectPath))
		c.JSON(404, gin.H{"error": "project not found"})
		return
	}
	if !project.AIEnabled {
		zap.L().Info("AI disabled for project", zap.String("project_path", projectPath))
		c.JSON(200, gin.H{"message": "AI disabled for this project"})
		return
	}

	// ======= 只处理 @AI / @ai 召唤 =======
	// 2026-06-08: 废弃 @AI BUGFIX 前端入口和评论分数解析触发
	// @AI 召唤是用户主动触发的交流入口，不受智能体触发事件配置影响
	if strings.HasPrefix(note, "@AI") || strings.HasPrefix(note, "@ai") {
		triggerSource := "manual"
		taskType := "chat"

		// 兼容保留 @AI BUGFIX 后端逻辑（前端已废弃入口）
		noteForCheck := strings.ToUpper(strings.TrimSpace(note))
		if strings.HasPrefix(noteForCheck, "@AI BUGFIX") || strings.HasPrefix(noteForCheck, "@AI\nBUGFIX") {
			taskType = "bugfix"
		}

		noteContent := strings.TrimSpace(strings.TrimPrefix(note, "@AI"))
		noteContent = strings.TrimSpace(strings.TrimPrefix(noteContent, "@AI BUGFIX"))
		noteContent = strings.TrimSpace(strings.TrimPrefix(noteContent, "@ai"))
		noteContent = strings.TrimSpace(strings.TrimPrefix(noteContent, "@ai bugfix"))
		prompt := noteContent
		if prompt == "" {
			if taskType == "bugfix" {
				prompt = "请修复这个问题"
			} else {
				prompt = "请审查这个合并请求并提供代码审查意见"
			}
		}

		task, err := buildTaskFromTrigger(project, noteableIID, noteID, projectPath, triggerSource, taskType, prompt, 0, note, authorDisplayName)
		if err != nil {
			c.JSON(500, gin.H{"error": "create task failed: " + err.Error()})
			return
		}
		createAndExecuteTask(projectPath, project.AccessToken, noteableIID, noteID, task,
			fmt.Sprintf("消息已接收，任务执行中，任务ID：%d", task.ID))
		c.JSON(200, gin.H{"message": "task created", "task_id": task.ID, "trigger_source": triggerSource})
		return
	}

	// 其他：忽略（不再解析评论中的"AI评分"）
	zap.L().Debug("ignored non-trigger comment", zap.String("note", note[:minWebhook(len(note), 50)]))
	c.JSON(200, gin.H{"message": "ignored"})
}

// ========== Merge Request Hook 处理（代码合并触发 AI 评审） ==========

func (h *WebhookHandler) handleMergeRequestHook(c *gin.Context, payload map[string]interface{}) {
	// 1. 解析 action
	attrs, ok := payload["object_attributes"].(map[string]interface{})
	if !ok {
		zap.L().Warn("invalid merge_request webhook: no object_attributes")
		c.JSON(400, gin.H{"error": "invalid payload"})
		return
	}

	action, _ := attrs["action"].(string)

	// ===== MR 合入主线：触发图谱增量更新 =====
	if action == "merge" {
		h.handleMRMergeEvent(c, payload, attrs)
		return
	}

	validActions := map[string]bool{"open": true, "update": true, "reopen": true}
	if !validActions[action] {
		zap.L().Info("ignored MR webhook action", zap.String("action", action))
		c.JSON(200, gin.H{"message": "ignored"})
		return
	}

	// 2. 解析项目信息
	projectPath := ""
	if proj, ok := payload["project"].(map[string]interface{}); ok {
		if webURL, ok := proj["web_url"].(string); ok {
			projectPath = webURL
		} else if httpURL, ok := proj["http_url"].(string); ok {
			projectPath = strings.TrimSuffix(httpURL, ".git")
		}
	}
	if projectPath == "" {
		c.JSON(400, gin.H{"error": "cannot get project path"})
		return
	}

	var project model.Project
	if err := model.DB.Where("project_path = ? OR project_path = ?",
		projectPath, projectPath+".git").First(&project).Error; err != nil {
		zap.L().Warn("project not found for MR webhook", zap.String("project_path", projectPath))
		c.JSON(404, gin.H{"error": "project not found"})
		return
	}
	if !project.AIEnabled {
		zap.L().Info("AI disabled for project", zap.String("project_path", projectPath))
		c.JSON(200, gin.H{"message": "AI disabled for this project"})
		return
	}

	// 4. 全局触发事件过滤
	triggerSource := "merge_request_" + action
	if !isTriggerEventAllowed(triggerSource) {
		zap.L().Info("merge_request trigger disabled by agent config", zap.String("action", action), zap.String("project_path", projectPath))
		c.JSON(200, gin.H{"message": "trigger disabled"})
		return
	}

	// 5. 解析 MR 信息
	mrIID := 0
	if iid, ok := attrs["iid"].(float64); ok {
		mrIID = int(iid)
	}
	if mrIID == 0 {
		c.JSON(400, gin.H{"error": "missing mr_iid"})
		return
	}

	mrTitle, _ := attrs["title"].(string)
	sourceBranch, _ := attrs["source_branch"].(string)
	targetBranch, _ := attrs["target_branch"].(string)
	mrURL, _ := attrs["url"].(string)

	author := ""
	authorDisplayName := ""
	if user, ok := payload["user"].(map[string]interface{}); ok {
		if username, ok := user["username"].(string); ok {
			author = username
		}
		if name, ok := user["name"].(string); ok {
			authorDisplayName = name
		}
	}

	// 获取最新的 commit id
	lastCommitID := ""
	if lastCommit, ok := attrs["last_commit"].(map[string]interface{}); ok {
		if id, ok := lastCommit["id"].(string); ok {
			lastCommitID = id
		}
	}

	// 去重检查：同一项目 + 同一 MR + 同一 Commit 是否已有 running/success 的 review 任务
	var existingCount int64
	model.DB.Model(&model.Task{}).
		Where("project_id = ? AND mr_merge_id = ? AND task_type = ? AND diff_summary = ? AND status IN ?",
			project.ID, mrIID, "review", lastCommitID, []string{"running", "success"}).
		Count(&existingCount)

	if existingCount > 0 {
		zap.L().Info("duplicate review task ignored",
			zap.Uint("project_id", project.ID),
			zap.Int("mr_iid", mrIID),
			zap.String("last_commit_id", lastCommitID))
		c.JSON(200, gin.H{"message": "duplicate ignored"})
		return
	}

	// 创建 AI 评审任务（pool_id 不传入，由 Create 方法默认为 0）
	taskData := map[string]interface{}{
		"project_id":          float64(project.ID),
		"mr_iid":              float64(mrIID),
		"mr_title":            mrTitle,
		"mr_url":              mrURL,
		"author":              author,
		"author_display_name": authorDisplayName,
		"source_branch":       sourceBranch,
		"target_branch":       targetBranch,
		"diff_summary":        lastCommitID, // 用于去重和追踪 commit
		"ai_prompt":           "",           // 由执行时动态组装
		"task_type":           "review",
		"trigger_type":        "webhook",
		"trigger_source":      "merge_request_" + action,
	}

	task, err := service.NewTaskService().Create(taskData)
	if err != nil {
		zap.L().Error("create review task failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "create task failed: " + err.Error()})
		return
	}

	// 6. 异步执行 AI 评审
	go func() {
		zap.L().Info("starting AI review task", zap.Uint("task_id", task.ID))
		if err := service.NewTaskService().ExecuteAIReviewTask(task.ID); err != nil {
			zap.L().Error("AI review task failed", zap.Uint("task_id", task.ID), zap.Error(err))
		}
	}()

	c.JSON(200, gin.H{"message": "review task created", "task_id": task.ID})
}

// handleMRMergeEvent 处理 MR 合入主线事件：触发图谱基线增量更新
func (h *WebhookHandler) handleMRMergeEvent(c *gin.Context, payload map[string]interface{}, attrs map[string]interface{}) {
	projectPath := ""
	if proj, ok := payload["project"].(map[string]interface{}); ok {
		if webURL, ok := proj["web_url"].(string); ok {
			projectPath = webURL
		} else if httpURL, ok := proj["http_url"].(string); ok {
			projectPath = strings.TrimSuffix(httpURL, ".git")
		}
	}
	if projectPath == "" {
		c.JSON(400, gin.H{"error": "cannot get project path"})
		return
	}

	var project model.Project
	if err := model.DB.Where("project_path = ? OR project_path = ?",
		projectPath, projectPath+".git").First(&project).Error; err != nil {
		zap.L().Warn("project not found for merge webhook", zap.String("project_path", projectPath))
		c.JSON(404, gin.H{"error": "project not found"})
		return
	}

	targetBranch, _ := attrs["target_branch"].(string)
	if targetBranch == "" {
		targetBranch = project.GraphBaseBranch
	}

	// 仅当合入的是配置的基线分支时才触发更新
	if targetBranch != project.GraphBaseBranch {
		zap.L().Info("merge target branch not graph base branch, skipped",
			zap.String("target", targetBranch),
			zap.String("base", project.GraphBaseBranch))
		c.JSON(200, gin.H{"message": "skipped: target branch is not graph base branch"})
		return
	}

	// 触发增量扫描（后台异步）
	workspace := os.Getenv("CODEGUARD_WORKSPACE")
	if workspace == "" {
		workspace = "/tmp/codeguard"
	}
	scanService := graphscan.NewScanService(model.DB, workspace)
	_, err := scanService.TriggerScan(uint64(project.ID), targetBranch)
	if err != nil {
		zap.L().Warn("trigger graph scan on merge failed", zap.Error(err))
		c.JSON(200, gin.H{"message": "merge handled, graph scan queued already running"})
		return
	}

	zap.L().Info("graph baseline incremental update triggered by MR merge",
		zap.Uint("project_id", project.ID),
		zap.String("branch", targetBranch))
	c.JSON(200, gin.H{"message": "graph baseline update queued", "project_id": project.ID, "branch": targetBranch})
}

func buildTaskFromTrigger(project model.Project, noteableIID, noteID int, projectPath string,
	triggerSource, taskType, prompt string, score int, note string, authorDisplayName string) (*model.Task, error) {
	var mrTitle, mrURL, mrAuthor, mrDiff, sourceBranch, targetBranch string

	mrInfo, err := fetchMRInfo(projectPath, project.AccessToken, noteableIID)
	if err != nil {
		zap.L().Warn("fetch MR info failed", zap.Error(err), zap.String("project_path", projectPath))
	} else {
		mrTitle = mrInfo.Title
		mrURL = mrInfo.WebURL
		mrAuthor = mrInfo.Author
		mrDiff = mrInfo.Diff
		sourceBranch = mrInfo.SourceBranch
		targetBranch = mrInfo.TargetBranch
	}

	taskData := map[string]interface{}{
		"project_id":          float64(project.ID),
		"pool_id":             float64(project.PoolID),
		"model_id":            fmt.Sprintf("%d", *project.DefaultModelID),
		"mr_iid":              float64(noteableIID),
		"mr_title":            mrTitle,
		"mr_url":              mrURL,
		"author":              mrAuthor,
		"author_display_name": authorDisplayName,
		"diff_summary":        mrDiff,
		"source_branch":       sourceBranch,
		"target_branch":       targetBranch,
		"note_id":             float64(noteID),
		"ai_prompt":           prompt,
		"task_type":           taskType,
		"trigger_type":        "webhook",
		"trigger_source":      triggerSource,
		"score_value":         float64(score),
	}

	return service.NewTaskService().Create(taskData)
}

func createAndExecuteTask(projectPath, gitlabToken string, noteableIID, noteID int, task *model.Task, comment string) {
	if comment != "" {
		if err := postMRComment(projectPath, gitlabToken, noteableIID, comment, noteID); err != nil {
			zap.L().Warn("post MR comment failed", zap.Error(err))
		} else {
			zap.L().Info("MR comment posted", zap.String("content", comment[:minWebhook(len(comment), 50)]))
		}
	}

	taskID := task.ID
	go func() {
		zap.L().Info("goroutine: starting task execution", zap.Uint("task_id", taskID))
		err := service.NewTaskService().Execute(taskID)
		if err != nil {
			zap.L().Error("goroutine: task execution failed", zap.Uint("task_id", taskID), zap.Error(err))
		} else {
			zap.L().Info("goroutine: task execution completed", zap.Uint("task_id", taskID))
		}
	}()
}

func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func minWebhook(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isTriggerEventAllowed 检查给定的触发事件是否被全局智能体配置允许
func isTriggerEventAllowed(source string) bool {
	var cfg model.ReviewAgentConfig
	if err := model.DB.First(&cfg, 1).Error; err != nil {
		// 配置未初始化时默认放行
		return true
	}
	// 若未配置任何事件，默认放行（兼容旧数据）
	events := cfg.TriggerEventCodes()
	if len(events) == 0 {
		return true
	}
	for _, ev := range events {
		if ev == source {
			return true
		}
		// 兼容旧配置：merge_request 包含所有子事件
		if ev == "merge_request" && strings.HasPrefix(source, "merge_request_") {
			return true
		}
	}
	return false
}
