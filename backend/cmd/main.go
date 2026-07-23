package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-optimizer/backend/config"
	"github.com/ai-optimizer/backend/internal/handler"
	"github.com/ai-optimizer/backend/internal/middleware"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"github.com/ai-optimizer/backend/pkg/encrypt"
	"github.com/ai-optimizer/backend/pkg/llmcall"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	cronRunner *cron.Cron
)

func main() {
	// 1. 加载配置
	cfg := config.Load()

	// 1.5 初始化加密模块
	encrypt.Init(cfg.EncryptKey)

	// 2. 初始化日志
	logger := initLogger(cfg)
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

	// 3. 初始化数据库
	if err := model.InitDB(cfg); err != nil {
		logger.Fatal("init database failed", zap.Error(err))
	}

	// 4. 初始化加密模块
	initEncrypt(cfg.EncryptKey)

	// 4.1 初始化 admin 用户
	if err := service.NewUserService().InitAdmin(); err != nil {
		logger.Warn("init admin user failed", zap.Error(err))
	}

	// 4.2. 初始化报表默认配置
	handler.InitReportConfigs()

	// 5. 初始化定时任务
	cronRunner := initCron(cfg)
	defer cronRunner.Stop()

	// 5.1. 初始化报表定时任务
	service.SetReportCron(cronRunner)
	service.InitReportCron()

	// 5.2. 启动资源池与大模型健康检查守护进程
	service.NewPoolService().StartHealthCheckDaemon()
	service.NewModelService().StartHealthCheckDaemon()

	// 5.2.5. 预热 SystemConfig 缓存 + 定时刷新（cache TTL 5min，刷新周期 1min 必须 < TTL）
	service.RefreshSysCfgCache()
	_, _ = cronRunner.AddFunc("@every 1m", service.RefreshSysCfgCache)

	// 5.3. 启动规则孵化台异步 Job 运行器
	vectorStore := vectorstore.NewMySQLStore(model.DB)
	embedSvc := service.NewEmbeddingService(vectorStore)
	incubSvc := service.NewIncubatorService(embedSvc, vectorStore)
	service.NewIncubatorJobRunner(incubSvc, embedSvc).Start()

	// 5.3.5. 启动规则健康检查定时 Job（每周一次）
	_, _ = cronRunner.AddFunc("0 2 * * 1", func() {
		if _, err := service.QueueJob("health_check", map[string]any{}); err != nil {
			zap.L().Warn("queue health check job failed", zap.Error(err))
		}
	})

	// 5.4. 启动 LLM 调用日志后台 worker（依赖 model.DB，必须在 InitDB 之后调用）
	llmcall.Start()

	// 6. 初始化 HTTP Router
	router := setupRouter(cfg)

	// 7. 启动服务
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}

	go func() {
		logger.Info("server starting", zap.Int("port", cfg.Port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server listen failed", zap.Error(err))
		}
	}()

	// 8. 优雅退出
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down server...")

	// 顺序：先停 HTTP（阻断新 Record），再排空 llmcall buffer，最后停 cron
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Warn("server forced to shutdown", zap.Error(err))
	}
	llmcall.Stop()
	logger.Info("server exited")
}

func initLogger(cfg *config.Config) *zap.Logger {
	level := zap.InfoLevel
	if cfg.Debug {
		level = zap.DebugLevel
	}
	cfgZap := zap.NewProductionConfig()
	cfgZap.Level = zap.NewAtomicLevelAt(level)
	cfgZap.EncoderConfig.TimeKey = "ts"
	cfgZap.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := cfgZap.Build()
	if err != nil {
		panic(err)
	}
	return logger
}

func initEncrypt(key string) {
	if key == "" {
		zap.L().Warn("ENCRYPTION_KEY is empty, sensitive data will be stored in plain text")
	}
}

func initCron(cfg *config.Config) *cron.Cron {
	cronRunner = cron.New(cron.WithSeconds())

	service.InitMRSyncCron(cronRunner)

	_, _ = cronRunner.AddFunc("@every 10s", func() {
		service.NewTaskService().TimeoutCheck()
	})

	cronRunner.Start()
	zap.L().Info("cron jobs started")
	return cronRunner
}

func setupRouter(cfg *config.Config) *gin.Engine {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.Logger())
	r.Use(middleware.CORS())

	frontendPath := cfg.FrontendPath
	if frontendPath == "" {
		frontendPath = "/data/ai-bug-fix/prototype"
	}
	r.Static("/js", frontendPath+"/js")
	r.Static("/css", frontendPath+"/css")
	r.Static("/vendor", frontendPath+"/vendor")
	r.Static("/static", frontendPath+"/static")

	r.GET("/projects.html", func(c *gin.Context) {
		c.File(frontendPath + "/projects.html")
	})
	r.GET("/models.html", func(c *gin.Context) {
		c.File(frontendPath + "/models.html")
	})
	r.GET("/tasks.html", func(c *gin.Context) {
		c.File(frontendPath + "/tasks.html")
	})
	r.GET("/task-detail.html", func(c *gin.Context) {
		c.File(frontendPath + "/task-detail.html")
	})
	r.GET("/pools.html", func(c *gin.Context) {
		c.File(frontendPath + "/pools.html")
	})
	r.GET("/notifiers.html", func(c *gin.Context) {
		c.File(frontendPath + "/notifiers.html")
	})
	r.GET("/settings.html", func(c *gin.Context) {
		c.File(frontendPath + "/settings.html")
	})
	r.GET("/templates.html", func(c *gin.Context) {
		c.File(frontendPath + "/templates.html")
	})
	r.GET("/project-detail.html", func(c *gin.Context) {
		c.File(frontendPath + "/project-detail.html")
	})
	r.GET("/pool-detail.html", func(c *gin.Context) {
		c.File(frontendPath + "/pool-detail.html")
	})
	r.GET("/login.html", func(c *gin.Context) {
		c.File(frontendPath + "/login.html")
	})
	r.GET("/mr-stats.html", func(c *gin.Context) {
		c.File(frontendPath + "/mr-stats.html")
	})
	r.GET("/statistics.html", func(c *gin.Context) {
		c.File(frontendPath + "/statistics.html")
	})
	r.GET("/", func(c *gin.Context) {
		c.File(frontendPath + "/statistics.html")
	})
	r.GET("/index.html", func(c *gin.Context) {
		// 301 重定向需显式保留 query 参数，避免 GitLab OAuth 回调的 ?token=xxx 被丢弃
		target := "/"
		if raw := c.Request.URL.RawQuery; raw != "" {
			target += "?" + raw
		}
		c.Redirect(http.StatusMovedPermanently, target)
	})
	r.GET("/mail.html", func(c *gin.Context) {
		c.File(frontendPath + "/mail.html")
	})
	r.GET("/report.html", func(c *gin.Context) {
		c.File(frontendPath + "/report.html")
	})
	r.GET("/member-mappings.html", func(c *gin.Context) {
		c.File(frontendPath + "/member-mappings.html")
	})
	r.GET("/review-rules.html", func(c *gin.Context) {
		c.File(frontendPath + "/review-rules.html")
	})
	r.GET("/token-usage.html", func(c *gin.Context) {
		c.File(frontendPath + "/token-usage.html")
	})
	r.GET("/rule-stats.html", func(c *gin.Context) {
		c.File(frontendPath + "/rule-stats.html")
	})
	r.GET("/incubator.html", func(c *gin.Context) {
		c.File(frontendPath + "/incubator.html")
	})

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	api := r.Group("/api/v1")

	// 用户认证（无需认证）
	userHandler := handler.NewUserHandler()
	api.POST("/login", userHandler.Login)
	api.POST("/logout", userHandler.Logout)

	// GitLab OAuth（无需认证）
	gitlabAuthHandler := handler.NewGitLabAuthHandler()
	api.GET("/auth/gitlab", gitlabAuthHandler.Redirect)
	api.GET("/auth/gitlab/callback", gitlabAuthHandler.Callback)

	// GitLab Webhook（无需认证）
	api.POST("/webhooks/gitlab", handler.NewWebhookHandler().GitLabWebhook)
	api.POST("/tasks/callback", handler.NewTaskHandler().Callback)

	// 公共需要认证的API（数据在handler/service层按user过滤）
	common := api.Group("")
	common.Use(middleware.Auth())
	{
		// 用户信息
		common.GET("/users/me", userHandler.GetCurrentUser)
		common.PUT("/users/password", userHandler.ChangePassword)

		// Dashboard
		dashboard := common.Group("/dashboard")
		{
			h := handler.NewDashboardHandler()
			dashboard.GET("/stats", h.GetStats)
			dashboard.GET("/trends", h.GetTrends)
			dashboard.GET("/recent-projects", h.GetRecentProjects)
			dashboard.GET("/recent-failures", h.GetRecentFailures)
			dashboard.GET("/task-distribution", h.GetTaskDistribution)
		}

		// Token 用量监控
		th := handler.NewTokenUsageHandler()
		common.GET("/dashboard/token-summary", th.GetTokenSummary)
		tokenUsage := common.Group("/token-usage")
		{
			tokenUsage.GET("/overview", th.GetOverview)
			tokenUsage.GET("/trend", th.GetTrend)
			tokenUsage.GET("/by-model", th.GetByModel)
			tokenUsage.GET("/by-project", th.GetByProject)
			tokenUsage.GET("/by-author", th.GetByAuthor)
			tokenUsage.GET("/by-call-type", th.GetByCallType)
			tokenUsage.GET("/calls", th.ListCalls)
			tokenUsage.GET("/by-task", th.GetByTask)
		}

		// 规则命中统计
		rsh := handler.NewRuleStatsHandler()
		ruleStats := common.Group("/rule-stats")
		{
			ruleStats.GET("/overview", rsh.Overview)
			ruleStats.GET("/by-rule/:code", rsh.ByRule)
			ruleStats.GET("/recent-issues", rsh.RecentIssues)
		}

		// 任务管理（只读 + 日志/消息/事件 + 用户可操作的写操作）
		task := common.Group("/tasks")
		{
			h := handler.NewTaskHandler()
			task.GET("", h.List)
			task.GET("/:id", h.Get)
			task.GET("/:id/logs", h.Logs)
			task.GET("/:id/messages", h.Messages)
			task.POST("/:id/messages", h.SendMessage)
			task.GET("/:id/events", h.SubscribeEvents)
			task.GET("/:id/diff", h.GetDiff)              // 获取任务 diff 文件内容
			task.GET("/:id/rules", h.ListTaskReviewRules) // 获取任务实际使用的评审规则
			task.GET("/:id/review-comments", h.ListReviewComments)
			task.POST("/:id/retry", h.Retry)
			task.POST("/:id/stop", h.Stop)
			// 任务详情：结构化评审结果查询
			reviewH := handler.NewProjectReviewHandler()
			task.GET("/:id/structured-review", reviewH.QueryStructuredReview)
			// Issue 状态管理：批量接纳/拒绝
			task.POST("/review-issues/batch-resolve", reviewH.BatchResolveIssues)
		}

		// MR 审查日志（数据已按user过滤）
		mrLog := common.Group("/mr-review-logs")
		{
			h := handler.NewMRReviewLogHandler()
			mrLog.GET("", h.List)
			mrLog.POST("/:id/mark-as-draft", h.MarkAsDraft)
			mrLog.POST("/:id/mark-as-ready", h.MarkAsReady)
			mrLog.GET("/projects", h.Projects)
			mrLog.GET("/authors", h.Authors)
			mrLog.GET("/statistics", handler.NewStatisticsHandler().Get)
		}

		// 项目选项（任务列表/通知配置等下拉框使用，不含敏感字段）
		common.GET("/projects/options", handler.NewProjectHandler().Options)
	}

	// 管理员专属API
	adminOnly := api.Group("")
	adminOnly.Use(middleware.Auth(), middleware.AdminOnly())
	{
		// 任务管理 - 写操作（仅限管理员）
		adminTask := adminOnly.Group("/tasks")
		{
			h := handler.NewTaskHandler()
			adminTask.POST("", h.Create)
			adminTask.POST("/:id/execute", h.Execute)
			adminTask.DELETE("/:id/session", h.DeleteSession)
		}

		// 项目管理
		project := adminOnly.Group("/projects")
		{
			h := handler.NewProjectHandler()
			project.GET("", h.List)
			project.POST("", h.Create)
			project.GET("/:id", h.Get)
			project.PUT("/:id", h.Update)
			project.DELETE("/:id", h.Delete)
			project.GET("/:id/tasks", h.Tasks)
			// 项目评审规则配置
			reviewH := handler.NewProjectReviewHandler()
			project.GET("/:id/review-rules", reviewH.ListRules)
			project.PUT("/:id/review-rules", reviewH.UpdateRules)
			project.POST("/:id/review-rules/reset", reviewH.ResetRules)
		}

		// 评审规则库管理
		reviewRules := adminOnly.Group("/review-rules")
		{
			h := handler.NewReviewRuleHandler()
			reviewRules.GET("", h.List)
			reviewRules.GET("/tree", h.Tree)
			reviewRules.POST("", h.Create)
			reviewRules.PUT("/:id", h.Update)
			reviewRules.DELETE("/:id", h.Delete)
			reviewRules.PUT("/batch-enable", h.BatchEnable)
		}

		// 评审维度管理
		reviewCats := adminOnly.Group("/review-categories")
		{
			h := handler.NewReviewCategoryHandler()
			reviewCats.GET("", h.List)
			reviewCats.POST("", h.Create)
			reviewCats.PUT("/:id", h.Update)
			reviewCats.DELETE("/:id", h.Delete)
		}

		// 模版管理
		template := adminOnly.Group("/templates")
		{
			h := handler.NewTemplateHandler()
			template.GET("", h.List)
			template.GET("/:id", h.Get)
			template.POST("", h.Create)
			template.PUT("/:id", h.Update)
			template.DELETE("/:id", h.Delete)
			template.POST("/:id/clone", h.Clone)
			template.POST("/:id/preview-comment", h.PreviewComment)
		}

		// 资源池管理
		pool := adminOnly.Group("/pools")
		{
			h := handler.NewPoolHandler()
			pool.GET("", h.List)
			pool.GET("/:id", h.Get)
			pool.POST("", h.Create)
			pool.PUT("/:id", h.Update)
			pool.DELETE("/:id", h.Delete)
			pool.POST("/test", h.TestConnectivity)
			pool.POST("/:id/check", h.CheckConnectivity)
			pool.PUT("/:id/toggle", h.Toggle)
			pool.PUT("/:id/default", h.SetDefault)
			pool.DELETE("/:id/default", h.UnsetDefault)
			pool.GET("/:id/skills", h.GetPoolSkills)
		}

		// 大模型管理
		modelGroup := adminOnly.Group("/models")
		{
			h := handler.NewModelHandler()
			modelGroup.GET("", h.List)
			modelGroup.GET("/default", h.GetDefault)
			modelGroup.GET("/:id/edit", h.GetForUpdate)
			modelGroup.GET("/:id", h.Get)
			modelGroup.POST("", h.Create)
			modelGroup.POST("/test", h.CreateTest)
			modelGroup.PUT("/:id", h.Update)
			modelGroup.DELETE("/:id", h.Delete)
			modelGroup.PUT("/:id/default", h.SetDefault)
			modelGroup.DELETE("/:id/default", h.UnsetDefault)
			modelGroup.POST("/:id/check", h.CheckAPI)
			modelGroup.PUT("/:id/disable", h.Disable)
			modelGroup.PUT("/:id/enable", h.Enable)
		}

		// 企业微信通知
		notifier := adminOnly.Group("/notifiers")
		{
			h := handler.NewNotifierHandler()
			notifier.GET("", h.List)
			notifier.GET("/:id", h.Get)
			notifier.POST("", h.Create)
			notifier.PUT("/:id", h.Update)
			notifier.PUT("/:id/template", h.UpdateTemplate)
			notifier.DELETE("/:id", h.Delete)
			notifier.POST("/:id/test", h.Test)
			notifier.PUT("/:id/toggle", h.Toggle)
		}

		// 成员映射管理
		memberMapping := adminOnly.Group("/member-mappings")
		{
			h := handler.NewMemberMappingHandler()
			memberMapping.GET("", h.List)
			memberMapping.GET("/git-users", h.GitUsers)
			memberMapping.GET("/:id", h.Get)
			memberMapping.POST("", h.Create)
			memberMapping.PUT("/:id", h.Update)
			memberMapping.DELETE("/:id", h.Delete)
			memberMapping.GET("/check", h.CheckMapping)
		}

		// 用户管理（管理员）
		users := adminOnly.Group("/users")
		{
			users.GET("", userHandler.ListUsers)
			users.POST("", userHandler.CreateUser)
			users.PUT("/:id", userHandler.UpdateUser)
			users.DELETE("/:id", userHandler.DeleteUser)
			users.POST("/:id/reset-password", userHandler.ResetPassword)
		}

		// 规则孵化台
		incubator := adminOnly.Group("/incubator")
		{
			h := handler.NewIncubatorHandler(vectorstore.NewMySQLStore(model.DB))
			incubator.GET("/status", h.Status)
			incubator.GET("/pipeline", h.Pipeline)
			incubator.GET("/pipeline/events", h.SubscribePipelineEvents)
			incubator.POST("/pipeline/run", h.RunPipeline)
			incubator.GET("/issues", h.ListIssues)
			incubator.GET("/issues/:id/trace", h.IssueTrace)
			incubator.POST("/cluster", h.Cluster)
			incubator.GET("/cluster/jobs/:id", h.GetClusterJob)
			incubator.GET("/candidates", h.ListCandidates)
			incubator.POST("/candidates", h.CreateCandidate)
			incubator.GET("/candidates/:id", h.GetCandidate)
			incubator.GET("/candidates/:id/trace", h.CandidateTrace)
			incubator.PUT("/candidates/:id", h.UpdateCandidate)
			incubator.DELETE("/candidates/:id", h.DeleteCandidate)
			incubator.POST("/candidates/:id/publish", h.PublishCandidate)
			incubator.POST("/candidates/:id/refine", h.TriggerRefine)
			incubator.POST("/candidates/:id/similar-check", h.TriggerSimilarCheck)
			incubator.POST("/candidates/:id/test", h.TriggerSandboxTest)
			incubator.POST("/retro-match", h.RetroMatch)
			incubator.GET("/health/rules", h.RuleHealth)
			incubator.GET("/config", h.GetConfig)
			incubator.PUT("/config", h.SaveConfig)
			incubator.POST("/config/validate-embedding", h.ValidateEmbedding)
		}

		// 系统管理
		sys := adminOnly.Group("/system")
		{
			h := handler.NewSystemHandler()
			sys.GET("/config", h.GetConfig)
			sys.PUT("/config", h.UpdateConfig)
			sys.GET("/logs", h.OperationLogs)
			sys.DELETE("/logs", h.ClearLogs)
			sys.GET("/info", h.Info)
		}

		// 报表管理
		report := adminOnly.Group("/reports")
		{
			h := handler.NewReportHandler()
			report.GET("/smtp", h.GetSMTPConfig)
			report.PUT("/smtp", h.SaveSMTPConfig)
			report.POST("/smtp/test", h.TestSMTP)
			report.GET("/recipients", h.ListRecipients)
			report.POST("/recipients", h.CreateRecipient)
			report.PUT("/recipients/:id", h.UpdateRecipient)
			report.DELETE("/recipients/:id", h.DeleteRecipient)
			report.GET("/config/:type", h.GetReportConfig)
			report.PUT("/config/:type", h.SaveReportConfig)
			report.GET("/preview/:type", h.PreviewReport)
			report.POST("/send/:type", h.SendReport)
			report.GET("/logs", h.ListLogs)
			report.POST("/logs/:id/resend", h.ResendReport)
			report.DELETE("/logs/:id", h.DeleteLog)
			report.GET("/logs/:id/html", h.GetReportLogHTML)
		}
	}

	return r
}
