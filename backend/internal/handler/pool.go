package handler

import (
	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"strconv"
)

type PoolHandler struct{}

func NewPoolHandler() *PoolHandler {
	return &PoolHandler{}
}

func (h *PoolHandler) List(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	name := c.Query("name")
	pools, err := service.NewPoolService().List(scope, name)
	if err != nil {
		zap.L().Error("list pools failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 多租户改造：非super_admin只返回当前组织可见的资源池
	if !scope.IsSuperAdmin {
		var filtered []model.ResourcePool
		for _, p := range pools {
			for _, orgID := range scope.VisibleOrgIDs {
				if p.OrgID == orgID || p.IsShared {
					filtered = append(filtered, p)
					break
				}
			}
		}
		pools = filtered
	}

	// 清理敏感字段
	for i := range pools {
		pools[i].OpencodePassword = ""
		pools[i].OpencodeAPIKey = ""
	}

	// 批量填充组织名称
	orgIDs := make([]uint, 0, len(pools))
	for _, p := range pools {
		orgIDs = append(orgIDs, p.OrgID)
	}
	orgNameMap := model.BatchOrgNames(orgIDs)
	for i := range pools {
		pools[i].OrgName = orgNameMap[pools[i].OrgID]
	}

	c.JSON(200, gin.H{"data": pools})
}

func (h *PoolHandler) Get(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	pool, err := service.NewPoolService().Get(scope, uint(id))
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}

	// 多租户改造：非super_admin需校验org_id
	if !scope.IsSuperAdmin {
		found := false
		for _, orgID := range scope.VisibleOrgIDs {
			if pool.OrgID == orgID || pool.IsShared {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权访问该资源池"})
			return
		}
	}

	// 清理敏感字段
	pool.OpencodePassword = ""
	pool.OpencodeAPIKey = ""

	c.JSON(200, gin.H{"data": pool})
}

func (h *PoolHandler) Create(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	required := []string{"name", "opencode_endpoint", "max_parallel", "check_interval_sec"}
	for _, key := range required {
		if _, ok := data[key]; !ok {
			c.JSON(400, gin.H{"error": "missing field: " + key})
			return
		}
	}

	// 多租户改造：注入 org_id（super_admin 可以传入）
	if _, ok := data["org_id"]; !ok || !scope.IsSuperAdmin {
		data["org_id"] = middleware.GetCurrentOrgID(c)
	}

	pool, err := service.NewPoolService().Create(scope, data)
	if err != nil {
		zap.L().Error("create pool failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("资源池创建", pool.Name, pool.ID, userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "created", "data": pool})
}

func (h *PoolHandler) Update(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if !scope.IsSuperAdmin {
		delete(data, "org_id")
	}

	// 多租户改造：校验资源池归属
	pool, _ := service.NewPoolService().Get(scope, uint(id))
	if pool.ID > 0 && !scope.IsSuperAdmin {
		found := false
		for _, orgID := range scope.VisibleOrgIDs {
			if pool.OrgID == orgID || pool.IsShared {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权访问该资源池"})
			return
		}
	}

	// super_admin 可以修改 org_id：用 UpdateColumn 绕过 GORM hook
	if scope.IsSuperAdmin {
		if orgID, ok := extractOrgIDFromMap(data); ok {
			zap.L().Info("pool update: changing org_id", zap.Int("id", id), zap.Uint("new_org_id", orgID))
			if err := model.DB.Model(&model.ResourcePool{}).Where("id = ?", id).UpdateColumn("org_id", orgID).Error; err != nil {
				zap.L().Error("pool update: org_id update failed", zap.Int("id", id), zap.Uint("org_id", orgID), zap.Error(err))
				c.JSON(500, gin.H{"error": "组织归属更新失败: " + err.Error()})
				return
			}
			zap.L().Info("pool update: org_id updated successfully", zap.Int("id", id), zap.Uint("new_org_id", orgID))
			delete(data, "org_id")
		}
	}

	if err := service.NewPoolService().Update(scope, uint(id), data); err != nil {
		zap.L().Error("update pool failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("资源池更新", pool.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "updated"})
}

func (h *PoolHandler) Delete(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	pool, _ := service.NewPoolService().Get(scope, uint(id))

	// 多租户改造：校验资源池归属
	if pool.ID > 0 && !scope.IsSuperAdmin {
		found := false
		for _, orgID := range scope.VisibleOrgIDs {
			if pool.OrgID == orgID || pool.IsShared {
				found = true
				break
			}
		}
		if !found {
			c.JSON(403, gin.H{"error": "无权访问该资源池"})
			return
		}
	}

	if err := service.NewPoolService().Delete(scope, uint(id)); err != nil {
		zap.L().Error("delete pool failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("资源池删除", pool.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "deleted"})
}

func (h *PoolHandler) CheckConnectivity(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	connected, msg, version, err := service.NewPoolService().CheckConnectivity(uint(id))
	if err != nil {
		zap.L().Error("check pool failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if connected {
		c.JSON(200, gin.H{"message": "连接成功", "status": "connected", "version": version})
	} else {
		c.JSON(200, gin.H{"message": "连接失败: " + msg, "status": "error"})
	}
}

func (h *PoolHandler) TestConnectivity(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	endpoint, _ := data["opencode_endpoint"].(string)
	username, _ := data["opencode_username"].(string)
	password, _ := data["opencode_password"].(string)

	connected, msg, err := service.NewOpencodeService().TestConnectivityByConfig(endpoint, username, password)
	if err != nil {
		zap.L().Error("test connectivity failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if connected {
		c.JSON(200, gin.H{"message": msg, "status": "connected"})
	} else {
		c.JSON(200, gin.H{"message": msg, "status": "error"})
	}
}

func (h *PoolHandler) Toggle(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	pool, _ := service.NewPoolService().Get(scope, uint(id))

	if err := service.NewPoolService().Toggle(scope, uint(id)); err != nil {
		zap.L().Error("toggle pool failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	action := "启用"
	if pool.Status == model.PoolActive {
		action = "禁用"
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("资源池"+action, pool.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "toggled"})
}

func (h *PoolHandler) SetDefault(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	pool, _ := service.NewPoolService().Get(scope, uint(id))
	if err := service.NewPoolService().SetDefault(scope, uint(id)); err != nil {
		zap.L().Error("set default pool failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("设为默认资源池", pool.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "set as default"})
}

func (h *PoolHandler) GetPoolSkills(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))

	skills, err := service.NewOpencodeService().GetSkills(uint(id))
	if err != nil {
		zap.L().Error("get pool skills failed", zap.Error(err), zap.Int("pool_id", id))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"data": skills})
}

func (h *PoolHandler) UnsetDefault(c *gin.Context) {
	scope := middleware.GetAuthScope(c)
	if scope == nil {
		c.JSON(401, gin.H{"error": "未登录"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	pool, _ := service.NewPoolService().Get(scope, uint(id))
	if err := service.NewPoolService().UnsetDefault(scope); err != nil {
		zap.L().Error("unset default pool failed", zap.Error(err))
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	userID, _ := c.Get("user_id")
	model.RecordOpLog("取消默认资源池", pool.Name, uint(id), userID.(uint), "success", "", c.ClientIP())
	c.JSON(200, gin.H{"message": "unset default"})
}
