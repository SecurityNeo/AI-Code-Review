package handler

import (
	"net/http"
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// OrgProjectHandler 多租户项目 Handler（改造样板）
// 展示如何在 handler 层正确使用 org_id 隔离、配额校验、事务及 Scope 过滤
type OrgProjectHandler struct{}

func NewOrgProjectHandler() *OrgProjectHandler {
	return &OrgProjectHandler{}
}

// CreateProjectRequest 创建项目请求 DTO
// org_id 仅 super_admin 可指定，其他角色由后端强制注入当前组织
type CreateProjectRequest struct {
	Name             string `json:"name" binding:"required"`
	ProjectPath      string `json:"project_path" binding:"required"`
	GitLabInstanceID uint   `json:"gitlab_instance_id" binding:"required"`
	GitLabProjectID  int    `json:"gitlab_project_id"`
	TemplateID       uint   `json:"template_id"`
	PoolID           uint   `json:"pool_id"`
	DefaultModelID   *uint  `json:"default_model_id"`
	Source           string `json:"source"`
	Language         string `json:"language"`
	GraphBaseBranch  string `json:"graph_base_branch"`
	OrgID            uint   `json:"org_id"` // 仅 super_admin 可指定
}

// UpdateProjectRequest 更新项目请求 DTO（不含 org_id）
type UpdateProjectRequest struct {
	Name            string `json:"name"`
	ProjectPath     string `json:"project_path"`
	GitLabProjectID int    `json:"gitlab_project_id"`
	TemplateID      uint   `json:"template_id"`
	PoolID          uint   `json:"pool_id"`
	DefaultModelID  *uint  `json:"default_model_id"`
	AIEnabled       *bool  `json:"ai_enabled"`
	Language        string `json:"language"`
	GraphBaseBranch string `json:"graph_base_branch"`
}

// CreateProject 创建项目（多租户改造样板）
//
// 核心要点：
//  1. 从 gin context 获取 org_id
//  2. 校验 GitLab 实例是否属于当前组织（gitlab_instances.org_id == currentOrgID）
//  3. 原子化配额检查
//  4. 事务：创建项目 + 增加 used_value（如果 limit > 0）
//  5. 显式设置 org_id，严禁依赖前端传入
func (h *OrgProjectHandler) CreateProject(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未认证"})
		return
	}

	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 解析 org_id：super_admin 可指定，其他角色强制当前组织
	orgID := ResolveOrgID(c, req.OrgID)
	if orgID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少组织 ID"})
		return
	}

	// 2. 校验 GitLab 实例是否属于当前组织
	var instance model.GitLabInstance
	if err := model.DB.Where("id = ? AND org_id = ?", req.GitLabInstanceID, orgID).First(&instance).Error; err != nil {
		zap.L().Error("gitlab instance not found or not belong to current org",
			zap.Uint("org_id", orgID),
			zap.Uint("gitlab_instance_id", req.GitLabInstanceID),
			zap.Error(err),
		)
		c.JSON(http.StatusForbidden, gin.H{"error": "GitLab 实例不属于当前组织"})
		return
	}

	// 3/4. 开启事务：原子化配额检查 + 创建项目
	tx := model.DB.Begin()

	// 原子化配额检查：仅当存在配额记录且 limit > 0 时执行
	var quota model.ResourceQuota
	if err := tx.Where("org_id = ? AND resource_type = ?", orgID, "project").First(&quota).Error; err == nil && quota.LimitValue > 0 {
		result := tx.Exec(
			"UPDATE resource_quotas SET used_value = used_value + 1 WHERE org_id = ? AND resource_type = ? AND used_value + 1 <= limit_value",
			orgID, "project",
		)
		if result.Error != nil {
			tx.Rollback()
			zap.L().Error("quota update failed", zap.Uint("org_id", orgID), zap.Error(result.Error))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "配额更新失败"})
			return
		}
		if result.RowsAffected == 0 {
			tx.Rollback()
			c.JSON(http.StatusForbidden, gin.H{"error": "项目数量已达配额上限"})
			return
		}
	}

	// 5. 构造 model 并显式设置 org_id（禁止依赖请求体中的 org_id）
	project := model.Project{
		Name:             req.Name,
		ProjectPath:      req.ProjectPath,
		GitLabInstanceID: req.GitLabInstanceID,
		GitLabProjectID:  req.GitLabProjectID,
		TemplateID:       req.TemplateID,
		PoolID:           req.PoolID,
		DefaultModelID:   req.DefaultModelID,
		Source:           req.Source,
		Language:         req.Language,
		GraphBaseBranch:  req.GraphBaseBranch,
	}
	project.OrgID = orgID // 显式覆盖，确保归属正确

	if project.Source == "" {
		project.Source = "manual"
	}
	if project.Language == "" {
		project.Language = "golang"
	}
	if project.GraphBaseBranch == "" {
		project.GraphBaseBranch = "main"
	}

	// 若直接绑定 model.Project，应使用 Omit("org_id").Create 防止外部篡改，
	// 但 Omit 会导致数据库使用默认值，必须配合显式 Update("org_id") 修正。
	// 推荐做法：使用 DTO 接收参数，再手动赋值给 model，然后直接 Create。
	if err := tx.Create(&project).Error; err != nil {
		tx.Rollback()
		zap.L().Error("create project failed", zap.Uint("org_id", orgID), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit().Error; err != nil {
		zap.L().Error("create project commit failed", zap.Uint("org_id", orgID), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "事务提交失败: " + err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	if uid, ok := userID.(uint); ok {
		model.RecordOpLog("项目创建", project.Name, project.ID, uid, "success", "", c.ClientIP())
	}
	c.JSON(http.StatusOK, gin.H{"message": "created", "data": project})
}

// UpdateProject 更新项目（多租户改造样板）
//
// 核心要点：
//  - 使用 Model(...).Where("id = ? AND org_id = ?", ...).Omit("org_id").Updates(...)
//  - 明确禁止 DB.Save：Save 会执行全字段更新，可能导致 org_id 被篡改或覆盖为 0
func (h *OrgProjectHandler) UpdateProject(c *gin.Context) {
	orgID := middleware.GetCurrentOrgID(c)
	if orgID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少组织 ID"})
		return
	}

	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未认证"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的项目 ID"})
		return
	}

	var req UpdateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 将 DTO 转换为更新 map，避免零值误更新
	updates := make(map[string]interface{})
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.ProjectPath != "" {
		updates["project_path"] = req.ProjectPath
	}
	if req.GitLabProjectID != 0 {
		updates["gitlab_project_id"] = req.GitLabProjectID
	}
	if req.TemplateID != 0 {
		updates["template_id"] = req.TemplateID
	}
	if req.PoolID != 0 {
		updates["pool_id"] = req.PoolID
	}
	if req.DefaultModelID != nil {
		updates["default_model_id"] = req.DefaultModelID
	}
	if req.AIEnabled != nil {
		updates["ai_enabled"] = *req.AIEnabled
	}
	if req.Language != "" {
		updates["language"] = req.Language
	}
	if req.GraphBaseBranch != "" {
		updates["graph_base_branch"] = req.GraphBaseBranch
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有可更新的字段"})
		return
	}

	// 明确禁止 DB.Save：Save 会执行全字段更新，可能重置 org_id。
	// 必须使用 Updates，并 Omit("org_id") 确保组织归属不可被修改。
	result := model.DB.
		Model(&model.Project{}).
		Where("id = ? AND org_id = ?", uint(id), orgID).
		Scopes(model.OrgScope(scope)).
		Omit("org_id").
		Updates(updates)

	if result.Error != nil {
		zap.L().Error("update project failed", zap.Uint("org_id", orgID), zap.Int("id", id), zap.Error(result.Error))
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在或无权限"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// ListProjects 列出项目（多租户改造样板）
//
// 核心要点：
//  - 使用 Scopes(model.OrgScope(scope)) 过滤
//  - 支持分页
func (h *OrgProjectHandler) ListProjects(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未认证"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := c.Query("keyword")

	var projects []model.Project
	var total int64

	db := model.DB.Model(&model.Project{}).Scopes(model.OrgScope(scope))
	if keyword != "" {
		db = db.Where("name LIKE ? OR project_path LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	if err := db.Count(&total).Error; err != nil {
		zap.L().Error("count projects failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := db.Scopes(model.Paginate(page, pageSize)).
		Preload("Template").
		Preload("Pool").
		Preload("Model").
		Order("created_at DESC").
		Find(&projects).Error; err != nil {
		zap.L().Error("list projects failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(projects))
	for _, p := range projects {
		orgIDs = append(orgIDs, p.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range projects {
		projects[i].OrgName = orgNameMap[projects[i].OrgID]
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      projects,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetProject 获取单个项目（多租户改造样板）
//
// 核心要点：
//  - 使用 Where("id = ? AND org_id = ?", ...) 双重限定
//  - 叠加 Scopes(model.OrgScope(scope)) 进行可见性校验
func (h *OrgProjectHandler) GetProject(c *gin.Context) {
	orgID := middleware.GetCurrentOrgID(c)
	if orgID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少组织 ID"})
		return
	}

	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未认证"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的项目 ID"})
		return
	}

	var project model.Project
	if err := model.DB.
		Where("id = ? AND org_id = ?", uint(id), orgID).
		Scopes(model.OrgScope(scope)).
		Preload("Template").
		Preload("Pool").
		Preload("Model").
		First(&project).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在或无权限"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": project})
}

// DeleteProject 删除项目（多租户改造样板）
//
// 核心要点：
//  - 使用 Where("id = ? AND org_id = ?", ...) 双重限定
//  - 叠加 Scopes(model.OrgScope(scope)) 进行可见性校验
//  - 使用事务：删除项目前检查该项目下是否还有运行中的任务
func (h *OrgProjectHandler) DeleteProject(c *gin.Context) {
	orgID := middleware.GetCurrentOrgID(c)
	if orgID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少组织 ID"})
		return
	}

	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未认证"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的项目 ID"})
		return
	}

	// 检查是否有运行中的任务
	var runningCount int64
	if err := model.DB.Model(&model.Task{}).Where("project_id = ? AND status = ?", uint(id), model.TaskRunning).Count(&runningCount).Error; err != nil {
		zap.L().Error("check running tasks failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检查运行中任务失败"})
		return
	}
	if runningCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该项目还有运行中的任务，无法删除"})
		return
	}

	// 执行删除（双重限定 + Scope 校验）
	result := model.DB.
		Where("id = ? AND org_id = ?", uint(id), orgID).
		Scopes(model.OrgScope(scope)).
		Delete(&model.Project{})
	if result.Error != nil {
		zap.L().Error("delete project failed", zap.Uint("org_id", orgID), zap.Int("id", id), zap.Error(result.Error))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除项目失败"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在或无权限"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
