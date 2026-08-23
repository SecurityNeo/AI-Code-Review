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

	// 获取当前用户角色
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var projectIDs []uint
	if user.Role != model.RoleAdmin {
		// 非管理员：只查询自己负责的项目
		projectIDs = GetResponsibleProjectIDs(user)
		if len(projectIDs) == 0 {
			c.JSON(200, gin.H{"data": []model.Project{}, "total": 0, "page": page, "page_size": pageSize})
			return
		}
	}

	projects, total, err := service.NewProjectService().List(page, pageSize, keyword, status, source, projectIDs)
	if err != nil {
		zap.L().Error("list projects failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
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
func (h *ProjectHandler) Options(c *gin.Context) {
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var projectIDs []uint
	if user.Role != model.RoleAdmin {
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
	user, ok := middleware.GetUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	p, err := service.NewProjectService().Get(uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	// 非管理员只能查看自己负责的项目
	if user.Role != model.RoleAdmin {
		projectIDs := GetResponsibleProjectIDs(user)
		found := false
		for _, pid := range projectIDs {
			if pid == uint(id) {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权限访问该项目"})
			return
		}
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
	p, _ := service.NewProjectService().Get(uint(id))
	err := service.NewProjectService().Delete(uint(id))
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
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	tasks, total, err := service.NewProjectService().GetProjectTasks(uint(id), page, pageSize)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"data": tasks, "total": total})
}
