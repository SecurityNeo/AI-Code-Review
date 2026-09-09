package handler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/pkg/llm"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"strconv"
)

type TemplateHandler struct{}

func NewTemplateHandler() *TemplateHandler {
	return &TemplateHandler{}
}

func (h *TemplateHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	templates, err := service.NewTemplateService().List(scope)
	if err != nil {
		zap.L().Error("list templates failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：非super_admin只返回当前组织可见的模板
	if !scope.IsSuperAdmin {
		var filtered []model.ProjectTemplate
		for _, t := range templates {
			for _, orgID := range scope.VisibleOrgIDs {
				if t.OrgID == orgID {
					filtered = append(filtered, t)
					break
				}
			}
		}
		templates = filtered
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(templates))
	for _, t := range templates {
		orgIDs = append(orgIDs, t.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range templates {
		templates[i].OrgName = orgNameMap[templates[i].OrgID]
	}

	c.JSON(200, gin.H{"data": templates})
}

func (h *TemplateHandler) Create(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var t model.ProjectTemplate
	if err := c.ShouldBindJSON(&t); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	// 多租户改造：注入当前 org_id（非 super_admin 禁止客户端传入）
	if t.OrgID == 0 || !scope.IsSuperAdmin {
		t.OrgID = middleware.GetCurrentOrgID(c)
	}

	// 如果未设置 Prompt 但有 CustomInstruction，动态生成
	if t.Prompt == "" && t.CustomInstruction != "" {
		t.Prompt = "请根据以下规则进行代码审查：\n\n" + t.CustomInstruction
	}
	err := service.NewTemplateService().Create(scope, &t)
	if err != nil {
		zap.L().Error("create template failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("配置化模板创建", t.Name, t.ID, userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "created", "data": t})
}

func (h *TemplateHandler) Get(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	t, err := service.NewTemplateService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	// 多租户改造：非super_admin需校验org_id
	if !scope.IsSuperAdmin {
		found := false
		for _, orgID := range scope.VisibleOrgIDs {
			if t.OrgID == orgID {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权访问该模板"})
			return
		}
	}

	c.JSON(200, gin.H{"data": t})
}

func (h *TemplateHandler) Update(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	updates := make(map[string]interface{})

	// 支持所有新的配置化字段
	if v, ok := req["name"]; ok {
		updates["name"] = v.(string)
	}
	if v, ok := req["description"]; ok {
		updates["description"] = v.(string)
	}
	// 多租户改造：super_admin 允许更新 org_id
	if scope.IsSuperAdmin {
		if v, ok := req["org_id"]; ok {
			updates["org_id"] = uint(v.(float64))
		}
	}
	if v, ok := req["prompt"]; ok {
		updates["prompt"] = v.(string)
	}
	if v, ok := req["custom_instruction"]; ok {
		updates["custom_instruction"] = v.(string)
	}
	var v interface{}
	var dwOk bool
	if v, dwOk = req["dimension_weights"]; dwOk {
		dwStr := v.(string)
		if dwStr == "" {
			dwStr = "{}"
		}
		// 校验权重 JSON 格式合法且和为 100
		if dwStr != "" && dwStr != "{}" {
			parsed, err := parseDimWeights(dwStr)
			if err != nil {
				c.JSON(400, gin.H{"error": "维度权重 JSON 格式无效: " + err.Error()})
				return
			}
			total := 0
			for _, w := range parsed {
				total += w
				if w < 0 || w > 100 {
					c.JSON(400, gin.H{"error": "维度权重必须在 0-100 之间"})
					return
				}
			}
			if total != 100 {
				c.JSON(400, gin.H{"error": fmt.Sprintf("维度权重之和必须等于 100，当前为 %d", total)})
				return
			}
		}
		updates["dimension_weights"] = dwStr
	}
	if v, ok := req["max_rules_per_review"]; ok {
		updates["max_rules_per_review"] = int(v.(float64))
	}
	if v, ok := req["gitlab_comment_template"]; ok {
		updates["gitlab_comment_template"] = v.(string)
	}

	// 扣分规则配置
	if v, ok := req["deduct_score_config"]; ok {
		dsStr := v.(string)
		if dsStr == "" {
			dsStr = "{}"
		}
		// 格式为 JSON: {"critical":10,"high":5,"medium":3,"low":1,"info":0}
		var dsMap map[string]int
		if err := json.Unmarshal([]byte(dsStr), &dsMap); err != nil {
			c.JSON(400, gin.H{"error": "扣分规则配置 JSON 格式无效: " + err.Error()})
			return
		}
		// 校验范围
		for sev, score := range dsMap {
			sev = strings.ToLower(sev)
			if score < 0 || score > 100 {
				c.JSON(400, gin.H{"error": fmt.Sprintf("扣分值必须在 0-100 之间: %s=%d", sev, score)})
				return
			}
		}
		updates["deduct_score_config"] = dsStr
	}

	t, err := service.NewTemplateService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "template not found"})
		return
	}
	err = service.NewTemplateService().Update(scope, uint(id), updates)
	if err != nil {
		zap.L().Error("update template failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("配置化模板更新", t.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "updated"})
}

func (h *TemplateHandler) Delete(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	t, err := service.NewTemplateService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "template not found"})
		return
	}
	err = service.NewTemplateService().Delete(scope, uint(id))
	if err != nil {
		zap.L().Error("delete template failed", zap.Error(err))
		if err == service.ErrTemplateInUse {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("模板删除", t.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "deleted"})
}

func (h *TemplateHandler) Clone(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Name == "" {
		c.JSON(400, gin.H{"error": "name is required"})
		return
	}
	original, err := service.NewTemplateService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "template not found"})
		return
	}
	t, err := service.NewTemplateService().Clone(scope, uint(id), req.Name)
	if err != nil {
		zap.L().Error("clone template failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("模板克隆", fmt.Sprintf("%s->%s", original.Name, t.Name), t.ID, userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "cloned", "data": t})
}

// parseDimWeights 解析维度权重 JSON 字符串
func parseDimWeights(jsonStr string) (map[string]int, error) {
	var result map[string]int
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// PreviewComment 预览评论模板渲染效果
func (h *TemplateHandler) PreviewComment(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))

	var req struct {
		TotalScore      int                      `json:"total_score"`
		Summary         string                   `json:"summary"`
		Dimensions      map[string]map[string]int `json:"dimensions"`
		Issues          []struct {
			RuleCode    string `json:"rule_code"`
			Severity    string `json:"severity"`
			Category    string `json:"category"`
			File        string `json:"file"`
			LineStart   int    `json:"line_start"`
			LineEnd     int    `json:"line_end"`
			CodeSnippet string `json:"code_snippet"`
			Message     string `json:"message"`
			Suggestion  string `json:"suggestion"`
		} `json:"issues"`
		Recommendations []string `json:"recommendations"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 获取模板
	t, err := service.NewTemplateService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "template not found"})
		return
	}

	// 构建模拟的 AIReviewResult
	result := &llm.AIReviewResult{
		SchemaVersion:   "1.0",
		TotalScore:      req.TotalScore,
		Summary:         req.Summary,
		Recommendations: req.Recommendations,
	}

	// 转换 dimensions
	if req.Dimensions != nil {
		result.Dimensions = make(map[string]llm.Dimension)
		for k, v := range req.Dimensions {
			result.Dimensions[k] = llm.Dimension{
				Score:  v["score"],
				Weight: v["weight"],
			}
		}
	}

	// 转换 issues
	for _, issue := range req.Issues {
		result.Issues = append(result.Issues, llm.AIReviewIssue{
			RuleCode:    issue.RuleCode,
			Severity:    issue.Severity,
			Category:    issue.Category,
			File:        issue.File,
			LineStart:   issue.LineStart,
			LineEnd:     issue.LineEnd,
			CodeSnippet: issue.CodeSnippet,
			Message:     issue.Message,
			Suggestion:  issue.Suggestion,
		})
	}

	// 组装 Markdown
	var templateStr string
	if t.GitLabCommentTemplate != "" {
		templateStr = t.GitLabCommentTemplate
	} else {
		templateStr = engine.DefaultGitLabCommentTemplate
	}

	markdown, err := engine.AssembleMarkdownComment(result, templateStr)
	if err != nil {
		zap.L().Error("preview comment assembly failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"data": markdown})
}
