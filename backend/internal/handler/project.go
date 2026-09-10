package handler

import (
	"strconv"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ProjectHandler struct{}

func NewProjectHandler() *ProjectHandler {
	return &ProjectHandler{}
}

func (h *ProjectHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := c.Query("keyword")
	status := c.Query("status")
	source := c.Query("source")
	orgIDQuery := c.Query("org_id")

	// 获取当前用户认证范围
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var projectIDs []uint
	var filterOrgID uint

	// 若显式传入 org_id（如分配职责场景），按组织查询，跳过 responsibilities 过滤
	if orgIDQuery != "" {
		oid, _ := strconv.Atoi(orgIDQuery)
		filterOrgID = uint(oid)
		if filterOrgID > 0 && !scope.IsSuperAdmin {
			visible := false
			for _, vid := range scope.VisibleOrgIDs {
				if vid == filterOrgID {
					visible = true
					break
				}
			}
			if !visible {
				c.JSON(403, gin.H{"error": "无权访问该组织的项目"})
				return
			}
		}
	} else {
		// 默认逻辑：developer 只返回自己负责的项目；org_admin / super_admin 返回整个组织
		if !scope.IsSuperAdmin && scope.CurrentOrgRole != "org_admin" {
			user, _ := middleware.GetUser(c)
			projectIDs = GetResponsibleProjectIDs(user)
			if len(projectIDs) == 0 {
				c.JSON(200, gin.H{"data": []model.Project{}, "total": 0, "page": page, "page_size": pageSize})
				return
			}
		}
	}

	projects, total, err := service.NewProjectService().List(page, pageSize, keyword, status, source, projectIDs, filterOrgID)
	if err != nil {
		zap.L().Error("list projects failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
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

	c.JSON(200, gin.H{"data": projects, "total": total, "page": page, "page_size": pageSize})
}

// GetResponsibleProjectIDs 根据 User 对象获取负责的项目 ID 列表
func GetResponsibleProjectIDs(user model.User) []uint {
	if user.ID == 0 {
		return nil
	}
	var responsibilities []model.ProjectResponsibility
	if err := model.DB.Where("user_id = ?", user.ID).Find(&responsibilities).Error; err != nil {
		return nil
	}
	ids := make([]uint, 0, len(responsibilities))
	for _, r := range responsibilities {
		ids = append(ids, r.ProjectID)
	}
	return ids
}

// Options 返回项目名称列表（用于下拉框选择，不含敏感字段）
// 多租户改造：叠加 OrgScope 过滤，只返回当前组织可见项目
func (h *ProjectHandler) Options(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var projectIDs []uint
	if !scope.IsSuperAdmin {
		user, _ := middleware.GetUser(c)
		projectIDs = GetResponsibleProjectIDs(user)
		if len(projectIDs) == 0 {
			c.JSON(200, gin.H{"data": []service.ProjectOption{}})
			return
		}
	}

	projects, err := service.NewProjectService().Options(projectIDs)
	if err != nil {
		zap.L().Error("list project options failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"data": projects})
}

func (h *ProjectHandler) Get(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	p, err := service.NewProjectService().Get(uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	// 多租户改造：使用 CanAccessProject 校验组织归属
	if !CanAccessProject(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权限访问该项目"})
		return
	}

	c.JSON(200, gin.H{"data": p})
}

func (h *ProjectHandler) Update(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：禁止更新org_id（super_admin 除外）
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	if !scope.IsSuperAdmin {
		delete(req, "org_id")
	}

	if !CanAccessProject(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权限访问该项目"})
		return
	}

	// super_admin 可以修改 org_id：用 UpdateColumn 绕过 GORM hook
	if scope.IsSuperAdmin {
		if orgID, ok := extractOrgIDFromMap(req); ok {
			zap.L().Info("project update: changing org_id", zap.Int("id", id), zap.Uint("new_org_id", orgID))
			if err := model.DB.Model(&model.Project{}).Where("id = ?", id).UpdateColumn("org_id", orgID).Error; err != nil {
				zap.L().Error("project update: org_id update failed", zap.Int("id", id), zap.Uint("org_id", orgID), zap.Error(err))
				c.JSON(500, gin.H{"error": "组织归属更新失败: " + err.Error()})
				return
			}
			zap.L().Info("project update: org_id updated successfully", zap.Int("id", id), zap.Uint("new_org_id", orgID))
			delete(req, "org_id")
		}
	}

	p, _ := service.NewProjectService().Get(uint(id))
	err := service.NewProjectService().Update(uint(id), req)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("项目更新", p.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "updated"})
}

func (h *ProjectHandler) Create(c *gin.Context) {
	var p model.Project
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if p.Source == "" {
		p.Source = "manual"
	}

	// 多租户改造：注入当前用户的 org_id（super_admin 可以传入）
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	if p.OrgID == 0 || !scope.IsSuperAdmin {
		orgID := middleware.GetCurrentOrgID(c)
		if orgID == 0 {
			c.JSON(400, gin.H{"error": "缺少组织 ID"})
			return
		}
		p.OrgID = orgID
	}

	err := service.NewProjectService().Create(&p)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("项目创建", p.Name, p.ID, userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "created", "data": p})
}

func (h *ProjectHandler) Delete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	// 多租户改造：删除前先校验项目归属
	p, err := service.NewProjectService().Get(uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if !CanAccessProject(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权限访问该项目"})
		return
	}

	err = service.NewProjectService().Delete(uint(id))
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("项目删除", p.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "deleted"})
}

func (h *ProjectHandler) Tasks(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	// 多租户改造：校验项目权限
	if !CanAccessProject(scope, uint(id)) {
		c.JSON(403, gin.H{"error": "无权限访问该项目"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	tasks, total, err := service.NewProjectService().GetProjectTasks(uint(id), page, pageSize)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"data": tasks, "total": total})
}
