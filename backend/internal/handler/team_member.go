package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
)

// TeamMemberHandler 团队人员管理 Handler
type TeamMemberHandler struct{}

func NewTeamMemberHandler() *TeamMemberHandler {
	return &TeamMemberHandler{}
}

// ==================== TeamMember API ====================

// List 人员列表
func (h *TeamMemberHandler) List(c *gin.Context) {
	search := c.Query("search")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	members, total, err := service.NewTeamMemberService().List(search, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": members, "total": total})
}

// Get 人员详情
func (h *TeamMemberHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	member, err := service.NewTeamMemberService().Get(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": member})
}

// Create 创建人员
func (h *TeamMemberHandler) Create(c *gin.Context) {
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	member, err := service.NewTeamMemberService().Create(data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": member})
}

// Update 更新人员
func (h *TeamMemberHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := service.NewTeamMemberService().Update(uint(id), data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// Delete 删除人员
func (h *TeamMemberHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := service.NewTeamMemberService().Delete(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// GitUsers 获取已知 Git 用户列表
func (h *TeamMemberHandler) GitUsers(c *gin.Context) {
	usernames, err := service.NewTeamMemberService().GetGitUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": usernames})
}

// ==================== Responsibility API ====================

// ResponsibilityView 职责前端视图（兼容旧 stewardView 字段）
type ResponsibilityView struct {
	ID             uint      `json:"id"`
	ProjectID      uint      `json:"project_id"`
	ProjectName    string    `json:"project_name,omitempty"`
	MemberID       uint      `json:"member_id"`
	GitlabUsername string    `json:"gitlab_username"`
	DisplayName    string    `json:"display_name"`
	IMPlatform     string    `json:"im_platform"`
	IMUserID       string    `json:"im_user_id"`
	ScopeType      string    `json:"scope_type"`
	ScopeValue     string    `json:"scope_value"`
	Priority       int       `json:"priority"`
	CreatedAt      time.Time `json:"created_at"`
}

func toResponsibilityView(r model.ProjectResponsibility) ResponsibilityView {
	memberID := uint(0)
	if r.MemberID != nil {
		memberID = *r.MemberID
	}
	v := ResponsibilityView{
		ID:         r.ID,
		ProjectID:  r.ProjectID,
		MemberID:   memberID,
		ScopeType:  r.ScopeType,
		ScopeValue: r.ScopeValue,
		Priority:   r.Priority,
		CreatedAt:  r.CreatedAt,
	}
	if r.Member != nil && r.Member.ID > 0 {
		v.GitlabUsername = r.Member.GitlabUsername
		v.DisplayName = r.Member.DisplayName
		v.IMPlatform = r.Member.IMPlatform
		v.IMUserID = r.Member.IMUserID
	}
	// toResponsibilityView falls back to the User association when Member is not preloaded.
	if v.DisplayName == "" && r.User.ID > 0 {
		v.GitlabUsername = r.User.GitlabUsername
		v.DisplayName = r.User.DisplayName
		v.IMPlatform = r.User.IMPlatform
		v.IMUserID = r.User.IMUserID
	}
	if r.Project.ID > 0 {
		v.ProjectName = r.Project.Name
	}
	return v
}

// ListResponsibilitiesByMember 列出某人的职责
// Deprecated: 使用 ListResponsibilitiesByUser 替代。
func (h *TeamMemberHandler) ListResponsibilitiesByMember(c *gin.Context) {
	memberID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	list, err := service.NewTeamMemberService().ListResponsibilitiesByMember(uint(memberID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	views := make([]ResponsibilityView, len(list))
	for i, r := range list {
		views[i] = toResponsibilityView(r)
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

// ListResponsibilitiesByProject 列出某项目的职责（用于项目详情页）
func (h *TeamMemberHandler) ListResponsibilitiesByProject(c *gin.Context) {
	projectID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	list, err := service.NewTeamMemberService().ListResponsibilitiesByProject(uint(projectID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	views := make([]ResponsibilityView, len(list))
	for i, r := range list {
		views[i] = toResponsibilityView(r)
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

// AddResponsibility 为人员添加职责
// Deprecated: 使用 AddResponsibilityByUser 替代。
func (h *TeamMemberHandler) AddResponsibility(c *gin.Context) {
	memberID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := service.NewTeamMemberService().AddResponsibility(uint(memberID), data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": toResponsibilityView(*resp)})
}

// UpdateResponsibility 更新职责
func (h *TeamMemberHandler) UpdateResponsibility(c *gin.Context) {
	rid, _ := strconv.ParseUint(c.Param("rid"), 10, 64)
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := service.NewTeamMemberService().UpdateResponsibility(uint(rid), data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 查询项目名称用于返回提示
	var resp model.ProjectResponsibility
	model.DB.Preload("Project").First(&resp, uint(rid))
	projectName := ""
	if resp.Project.ID > 0 {
		projectName = resp.Project.Name
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("项目%s责任人已更新", projectName)})
}

// DeleteResponsibility 删除职责
func (h *TeamMemberHandler) DeleteResponsibility(c *gin.Context) {
	rid, _ := strconv.ParseUint(c.Param("rid"), 10, 64)
	if err := service.NewTeamMemberService().DeleteResponsibility(uint(rid)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
