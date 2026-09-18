package handler

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service/depaudit"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const maxImportBytes = 20 << 20

// DependencyAuditHandler 依赖审查处理器
type DependencyAuditHandler struct {
	db          *gorm.DB
	scanService *depaudit.ScanService
}

func NewDependencyAuditHandler(db *gorm.DB, workspace string) *DependencyAuditHandler {
	return &DependencyAuditHandler{db: db, scanService: depaudit.NewScanService(db, workspace)}
}

// RegisterRoutes 注册路由
func (h *DependencyAuditHandler) RegisterRoutes(common, adminOnly *gin.RouterGroup) {
	requireOrg := func(c *gin.Context) {
		if middleware.GetCurrentOrgID(c) == 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "缺少组织上下文，请在顶部选择组织"})
		}
	}

	read := common.Group("/dependency-audit")
	read.Use(requireOrg)
	{
		read.GET("/projects", h.ListProjects)
		read.GET("/tasks", h.ListTasks)
		read.GET("/tasks/:id", h.GetTask)
		read.GET("/tasks/:id/report/items", h.ListTaskItems)
		read.GET("/tasks/:id/report/vulns", h.ListTaskVulns)
		read.GET("/config", h.GetConfig)
		read.GET("/licenses", h.ListLicenses)
		read.GET("/licenses/stats", h.LicenseStats)
		read.GET("/import-names", h.ListImportNames)
		read.GET("/import-names/stats", h.ImportNameStats)
		read.GET("/sync/status", h.SyncStatus)
		read.GET("/sync/queue", h.ListSyncQueue)
		read.GET("/collect/states", h.CollectStates)
	}

	write := adminOnly.Group("/dependency-audit")
	write.Use(requireOrg)
	{
		write.POST("/scan", h.TriggerScan)
		write.POST("/tasks/:id/cancel", h.CancelTask)
		write.POST("/tasks/:id/retry", h.RetryTask)
		write.DELETE("/tasks/:id", h.DeleteTask)
		write.GET("/tasks/:id/export", h.Export)
		write.PUT("/config", h.SaveConfig)
		write.POST("/licenses/import", h.ImportLicenses)
		write.POST("/import-names/import", h.ImportImportNames)
		write.POST("/sync", h.SyncNow)
		write.POST("/sync/import-names", h.SyncImportNamesNow)
	}
}

func (h *DependencyAuditHandler) orgID(c *gin.Context) uint { return middleware.GetCurrentOrgID(c) }

// ========== 项目 & 扫描 ==========

func (h *DependencyAuditHandler) ListProjects(c *gin.Context) {
	orgID := h.orgID(c)
	var projects []model.Project
	if err := h.db.Where("org_id = ?", orgID).Order("id desc").Find(&projects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	type projectView struct {
		ID              uint   `json:"id"`
		Name            string `json:"name"`
		Language        string `json:"language"`
		GraphBaseBranch string `json:"graph_base_branch"`
		SnapshotCommit  string `json:"snapshot_commit"`
		SnapshotAt      string `json:"snapshot_at"`
		DepCount        int    `json:"dep_count"`
	}
	states := map[uint]model.DependencyAuditCollectState{}
	var all []model.DependencyAuditCollectState
	h.db.Where("org_id = ?", orgID).Find(&all)
	for _, s := range all {
		states[s.ProjectID] = s
	}
	views := make([]projectView, 0, len(projects))
	for _, p := range projects {
		v := projectView{ID: p.ID, Name: p.Name, Language: p.Language, GraphBaseBranch: p.GraphBaseBranch}
		if s, ok := states[p.ID]; ok {
			v.SnapshotCommit = s.LastCommit
			v.DepCount = s.DepCount
			if s.LastCollectedAt != nil {
				v.SnapshotAt = s.LastCollectedAt.Format("2006-01-02 15:04:05")
			}
		}
		views = append(views, v)
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

func (h *DependencyAuditHandler) TriggerScan(c *gin.Context) {
	orgID := h.orgID(c)
	var req struct {
		ProjectIDs []uint `json:"project_ids"`
		All        bool   `json:"all"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	user, _ := middleware.GetUser(c)
	var taskID uint
	var err error
	if req.All {
		taskID, err = h.scanService.TriggerOrg(orgID, "manual", user.ID)
	} else {
		if len(req.ProjectIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未选择项目"})
			return
		}
		taskID, err = h.scanService.TriggerProjects(orgID, req.ProjectIDs, "manual", user.ID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"task_id": taskID}})
}

// ========== 任务 ==========

func (h *DependencyAuditHandler) ListTasks(c *gin.Context) {
	orgID := h.orgID(c)
	q := h.db.Where("org_id = ?", orgID)
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	var total int64
	q.Session(&gorm.Session{}).Model(&model.DependencyAuditTask{}).Count(&total)
	page, size := pagination(c)
	var tasks []model.DependencyAuditTask
	q.Session(&gorm.Session{}).Order("id desc").Offset((page - 1) * size).Limit(size).Find(&tasks)
	c.JSON(http.StatusOK, gin.H{"data": tasks, "total": total, "page": page, "page_size": size})
}

func (h *DependencyAuditHandler) loadTask(c *gin.Context) (*model.DependencyAuditTask, bool) {
	orgID := h.orgID(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return nil, false
	}
	var task model.DependencyAuditTask
	if err := h.db.Where("id = ? AND org_id = ?", id, orgID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return nil, false
	}
	return &task, true
}

func (h *DependencyAuditHandler) GetTask(c *gin.Context) {
	task, ok := h.loadTask(c)
	if !ok {
		return
	}
	type runView struct {
		Run         model.DependencyAuditRun `json:"run"`
		ProjectName string                   `json:"project_name"`
	}
	runs := depaudit.LatestRunsForTask(h.db, task.ID)
	views := make([]runView, 0, len(runs))
	for _, r := range runs {
		views = append(views, runView{Run: r.Run, ProjectName: r.ProjectName})
	}
	ecos, direct, indirect := depaudit.TaskDependencySummary(h.db, task.ID)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"task": task, "runs": views,
		"dep_ecosystems": ecos, "dep_direct": direct, "dep_indirect": indirect,
	}})
}

func (h *DependencyAuditHandler) CancelTask(c *gin.Context) {
	task, ok := h.loadTask(c)
	if !ok {
		return
	}
	if err := h.scanService.CancelTask(task.ID, task.OrgID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": "cancelled"})
}

func (h *DependencyAuditHandler) DeleteTask(c *gin.Context) {
	task, ok := h.loadTask(c)
	if !ok {
		return
	}
	if err := h.scanService.DeleteTask(task.ID, task.OrgID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": "deleted"})
}

func (h *DependencyAuditHandler) RetryTask(c *gin.Context) {
	task, ok := h.loadTask(c)
	if !ok {
		return
	}
	var req struct {
		ProjectIDs []uint `json:"project_ids"`
	}
	_ = c.ShouldBindJSON(&req)
	n, err := h.scanService.RetryTask(task.ID, task.OrgID, req.ProjectIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"retried": n}})
}

func (h *DependencyAuditHandler) taskRuns(c *gin.Context, task *model.DependencyAuditTask) ([]depaudit.RunWithProject, bool) {
	runs := depaudit.LatestRunsForTask(h.db, task.ID)
	if pid := c.Query("project_id"); pid != "" {
		id, _ := strconv.ParseUint(pid, 10, 64)
		filtered := runs[:0]
		for _, r := range runs {
			if uint64(r.Run.ProjectID) == id {
				filtered = append(filtered, r)
			}
		}
		runs = filtered
	}
	return runs, true
}

func runIDsOf(runs []depaudit.RunWithProject) []uint {
	ids := make([]uint, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.Run.ID)
	}
	return ids
}

func (h *DependencyAuditHandler) ListTaskItems(c *gin.Context) {
	task, ok := h.loadTask(c)
	if !ok {
		return
	}
	runs, _ := h.taskRuns(c, task)
	runIDs := runIDsOf(runs)
	if len(runIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"data": []interface{}{}, "total": 0})
		return
	}
	q := h.db.Where("run_id IN ?", runIDs)
	if v := c.Query("ecosystem"); v != "" {
		q = q.Where("ecosystem = ?", v)
	}
	if v := c.Query("license_status"); v != "" {
		q = q.Where("license_status = ?", v)
	}
	if v := c.Query("reachable"); v != "" {
		q = q.Where("reachable = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		q = q.Where("package_name LIKE ?", likePattern(v))
	}
	var total int64
	q.Session(&gorm.Session{}).Model(&model.DependencyAuditItem{}).Count(&total)
	page, size := pagination(c)
	var items []model.DependencyAuditItem
	q.Session(&gorm.Session{}).Order("project_id asc, id asc").Offset((page - 1) * size).Limit(size).Find(&items)
	ptrs := make([]*model.DependencyAuditItem, len(items))
	for i := range items {
		ptrs[i] = &items[i]
	}
	depaudit.EnrichItemsFromCache(h.db, task.OrgID, ptrs)
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": page, "page_size": size})
}

func (h *DependencyAuditHandler) ListTaskVulns(c *gin.Context) {
	task, ok := h.loadTask(c)
	if !ok {
		return
	}
	runs, _ := h.taskRuns(c, task)
	runIDs := runIDsOf(runs)
	if len(runIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"data": []interface{}{}, "total": 0})
		return
	}
	q := h.db.Where("run_id IN ?", runIDs)
	if v := c.Query("severity"); v != "" {
		q = q.Where("severity = ?", v)
	}
	if v := c.Query("reachable"); v != "" {
		q = q.Where("reachable = ?", v)
	}
	if v := c.Query("allowlisted"); v != "" {
		q = q.Where("allowlisted = ?", v == "true" || v == "1")
	}
	if v := c.Query("keyword"); v != "" {
		p := likePattern(v)
		q = q.Where("package_name LIKE ? OR vuln_id LIKE ? OR aliases LIKE ?", p, p, p)
	}
	var total int64
	q.Session(&gorm.Session{}).Model(&model.DependencyAuditVuln{}).Count(&total)
	page, size := pagination(c)
	var vulns []model.DependencyAuditVuln
	q.Session(&gorm.Session{}).Order("project_id asc, id asc").Offset((page - 1) * size).Limit(size).Find(&vulns)
	c.JSON(http.StatusOK, gin.H{"data": vulns, "total": total, "page": page, "page_size": size})
}

// ========== 配置 ==========

func (h *DependencyAuditHandler) GetConfig(c *gin.Context) {
	cfg, err := depaudit.GetConfig(h.db, h.orgID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg})
}

func (h *DependencyAuditHandler) SaveConfig(c *gin.Context) {
	orgID := h.orgID(c)
	var cfg model.DependencyAuditConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	cfg.OrgID = orgID
	if err := depaudit.SaveConfig(h.db, &cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": "saved"})
}

// ========== 许可证 / 导入名 / 同步 ==========

func (h *DependencyAuditHandler) LicenseStats(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": depaudit.LicenseStats(h.db)})
}
func (h *DependencyAuditHandler) ImportNameStats(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": depaudit.ImportNameStats(h.db)})
}

func (h *DependencyAuditHandler) ListLicenses(c *gin.Context) {
	q := h.db.Model(&model.PackageLicense{})
	if v := c.Query("ecosystem"); v != "" {
		q = q.Where("ecosystem = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		q = q.Where("package_name LIKE ?", likePattern(v))
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	page, size := pagination(c)
	var rows []model.PackageLicense
	if err := q.Session(&gorm.Session{}).Order("updated_at desc").
		Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": total, "page": page, "page_size": size})
}

func (h *DependencyAuditHandler) ListImportNames(c *gin.Context) {
	q := h.db.Model(&model.PackageImportName{})
	if v := c.Query("ecosystem"); v != "" {
		q = q.Where("ecosystem = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		q = q.Where("package_name LIKE ?", likePattern(v))
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	page, size := pagination(c)
	var rows []model.PackageImportName
	if err := q.Session(&gorm.Session{}).Order("updated_at desc").
		Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": total, "page": page, "page_size": size})
}

func (h *DependencyAuditHandler) SyncStatus(c *gin.Context) {
	logs := depaudit.LatestJobLogs(h.db, h.orgID(c))
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"logs": logs, "queue": depaudit.SyncQueueStats(h.db)}})
}

func (h *DependencyAuditHandler) ListSyncQueue(c *gin.Context) {
	page, size := pagination(c)
	rows, total := depaudit.ListSyncQueue(h.db, c.Query("status"), page, size, c.Query("keyword"))
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": total, "page": page, "page_size": size})
}

func (h *DependencyAuditHandler) CollectStates(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": depaudit.CollectStatus(h.db, h.orgID(c))})
}

// SyncNow 立即同步：收集本组织 + 入队 + 处理队列（后台执行）
func (h *DependencyAuditHandler) SyncNow(c *gin.Context) {
	orgID := h.orgID(c)
	go func() {
		collectSvc := depaudit.NewCollectService(h.db)
		collectSvc.CollectOrg(orgID, nil)
		lic, imp := depaudit.RunSyncNow(h.db, orgID)
		zap.L().Info("dependency-audit sync now done",
			zap.Uint("org_id", orgID), zap.Int("license", lic), zap.Int("import", imp))
	}()
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"started": true}})
}

// SyncImportNamesNow 立即同步导入名映射（入队本组织 + 处理队列，后台执行）
func (h *DependencyAuditHandler) SyncImportNamesNow(c *gin.Context) {
	orgID := h.orgID(c)
	go func() {
		n := depaudit.RunImportNameSyncNow(h.db, orgID)
		zap.L().Info("dependency-audit import-name sync done", zap.Uint("org_id", orgID), zap.Int("enqueued", n))
	}()
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"started": true}})
}

func (h *DependencyAuditHandler) ImportLicenses(c *gin.Context) {
	h.importFile(c, func(filename string, data io.Reader) (int, []string, error) {
		return depaudit.ImportLicenses(h.db, filename, data)
	})
}

func (h *DependencyAuditHandler) ImportImportNames(c *gin.Context) {
	h.importFile(c, func(filename string, data io.Reader) (int, []string, error) {
		return depaudit.ImportImportNames(h.db, filename, data)
	})
}

func (h *DependencyAuditHandler) importFile(c *gin.Context, fn func(string, io.Reader) (int, []string, error)) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少文件"})
		return
	}
	if file.Size > maxImportBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件过大（上限 20MB）"})
		return
	}
	f, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	imported, errs, err := fn(file.Filename, f)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"imported": imported, "errors": errs}})
}

// ========== 导出 ==========

func (h *DependencyAuditHandler) Export(c *gin.Context) {
	task, ok := h.loadTask(c)
	if !ok {
		return
	}
	runs := depaudit.LatestRunsForTask(h.db, task.ID)
	runIDs := runIDsOf(runs)
	data := depaudit.LoadRunsData(h.db, runIDs)
	for _, rp := range runs {
		depaudit.EnrichItemsFromCache(h.db, task.OrgID, data[rp.Run.ID].Items)
	}

	format := c.DefaultQuery("format", "excel")
	filename := fmt.Sprintf("dependency-audit-task-%d", task.ID)
	switch format {
	case "csv":
		var out []byte
		var err error
		if c.DefaultQuery("dataset", "items") == "vulns" {
			out, err = depaudit.BuildTaskVulnsCSV(runs, data)
		} else {
			out, err = depaudit.BuildTaskItemsCSV(runs, data)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Disposition", "attachment; filename="+filename+"-"+c.DefaultQuery("dataset", "items")+".csv")
		c.Data(http.StatusOK, "text/csv; charset=utf-8", out)
	case "sbom":
		out, err := depaudit.GenerateTaskSBOM(h.db, task)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Disposition", "attachment; filename="+filename+".zip")
		c.Data(http.StatusOK, "application/zip", out)
	default:
		out, err := depaudit.BuildTaskExcel(task, runs, data)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Disposition", "attachment; filename="+filename+".xlsx")
		c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", out)
	}
}

// ========== 辅助 ==========

func likePattern(kw string) string {
	kw = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(kw)
	return "%" + kw + "%"
}

func pagination(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	return page, size
}
