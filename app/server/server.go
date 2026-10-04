package server

import (
	"context"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/handler"
	"film-fusion/app/logger"
	"film-fusion/app/middleware"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Server 表示 HTTP 服务器
type Server struct {
	Config                  *config.Config
	Logger                  *logger.Logger
	gin                     *gin.Engine
	http                    *http.Server
	tokenRefreshService     *service.TokenRefreshService
	web115KeepAliveService  *service.Web115KeepAliveService
	download115Service      *service.Download115Service
	moviePilotService       *service.MoviePilotService
	mediaRecognitionService *service.MediaRecognitionService
	tmdbService             *service.TMDBService
	embyCoverService        *service.EmbyCoverService
	embySortNameService     *service.EmbySortNameService
	embyStatsService        *service.EmbyStatsService
	embyMissingService      *service.EmbyMissingService
	embyAccountService      *service.EmbyAccountService
	versionCheckHandler     *handler.EmbyVersionCheckHandler
	balanceCleanupSvc       *service.BalanceCleanupService
	embyClient              *embyhelper.EmbyClient
	organizeLogCleaner      *service.OrganizeLogCleaner
	organizePreviewQueue    *service.OrganizePreviewQueue
	embyProxyServer         *EmbyProxyServer
	embyLoginProtection     *service.EmbyLoginProtection
	appLoginProtection      *service.EmbyLoginProtection
	notificationService     *service.NotificationService
	automationService       *service.AutomationService
	rssService              *service.RSSService
	taskQueue               *service.PersistentTaskQueue
}

// NewServer 创建一个新的 Server 实例
func New(cfg *config.Config, log *logger.Logger) *Server {
	router := newApplicationRouter()

	// 创建115Open下载服务
	download115Service := service.NewDownload115Service(log, cfg.Server.Download115Concurrency)
	moviePilotService := service.NewMoviePilotService(cfg, log)
	tmdbService := service.NewTMDBService(cfg, log)
	mediaRecognitionService := service.NewMediaRecognitionService(database.GetDB(), tmdbService, log)
	moviePilotService.SetLocalMediaRecognition(mediaRecognitionService)

	embyClient := embyhelper.New(cfg)

	// 创建回调函数包装器
	playbackCallback := func(itemID string, cfg *config.Config) error {
		_, err := embyClient.GetPlaybackInfo(itemID)
		return err
	}

	taskQueue := service.NewPersistentTaskQueue(cfg, log, playbackCallback)

	if taskQueue != nil {
		log.Info("✅ 任务队列初始化成功")

		// 获取队列状态
		if status, err := taskQueue.GetQueueStatus(); err == nil {
			log.Infof("📊 当前任务队列状态: %+v", status)
		}
	} else {
		log.Error("❌ 任务队列初始化失败")
	}

	embyCoverService := service.NewEmbyCoverService(cfg, log, embyClient)
	embySortNameService := service.NewEmbySortNameService(cfg, log, embyClient)
	embyStatsService := service.NewEmbyStatsService(cfg, log, embyClient)
	embyMissingService := service.NewEmbyMissingService(cfg, log, embyClient)
	embyVersionCheckHandler := handler.NewEmbyVersionCheckHandler(log)
	notificationService := service.NewNotificationService(cfg, log)

	s := &Server{
		gin: router,
		http: &http.Server{
			Addr:    ":" + cfg.Server.Port,
			Handler: router,
		},
		Config:                  cfg,
		Logger:                  log,
		tokenRefreshService:     service.NewTokenRefreshService(log),
		web115KeepAliveService:  service.NewWeb115KeepAliveService(cfg, log, notificationService),
		download115Service:      download115Service,
		moviePilotService:       moviePilotService,
		mediaRecognitionService: mediaRecognitionService,
		tmdbService:             tmdbService,
		embyCoverService:        embyCoverService,
		embySortNameService:     embySortNameService,
		embyStatsService:        embyStatsService,
		embyMissingService:      embyMissingService,
		embyAccountService:      service.NewEmbyAccountService(database.GetDB(), cfg, log),
		versionCheckHandler:     embyVersionCheckHandler,
		balanceCleanupSvc:       service.NewBalanceCleanupService(log),
		embyClient:              embyClient,
		organizeLogCleaner:      service.NewOrganizeLogCleaner(log, 0, 0),
		notificationService:     notificationService,
		taskQueue:               taskQueue,
	}
	s.rssService = service.NewRSSService(log)
	s.automationService = service.NewAutomationService(log, s.notificationService, s.moviePilotService)
	s.automationService.SetRSSService(s.rssService)
	s.automationService.SetLocalMediaRecognition(s.mediaRecognitionService)
	s.appLoginProtection = service.NewAppLoginProtection(cfg, log, s.notificationService)
	s.embyLoginProtection = service.NewEmbyLoginProtection(cfg, log, s.notificationService)

	// 设置路由
	s.setupRoutes()

	// 开启一个 Emby 代理服务
	if cfg.Emby.Enabled {
		s.Logger.Info("Emby服务已启用，正在创建代理服务器...")
		embyProxyServer := NewEmbyProxyServer(cfg, log, s.embyLoginProtection)
		if embyProxyServer != nil {
			s.embyProxyServer = embyProxyServer
		} else {
			s.Logger.Error("创建Emby代理服务器失败")
		}
	}

	return s
}

func newApplicationRouter() *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	return router
}

// Start 启动服务器
func (s *Server) Start() error {
	s.Logger.Infof("在端口 %s 启动服务器", s.http.Addr)

	// 启动令牌刷新服务
	s.tokenRefreshService.Start()

	// 启动 115 cookie 保活服务
	s.web115KeepAliveService.Start()

	// 启动115Open下载服务
	s.download115Service.StartWorkers()

	// 启动 MoviePilot 令牌刷新服务
	s.moviePilotService.Start()

	// 启动 Emby 封面生成定时任务调度
	s.embyCoverService.Start()

	// 启动整理日志清理器（默认 7 天保留）
	s.organizeLogCleaner.Start()

	// 启动预整理队列
	if s.organizePreviewQueue != nil {
		s.organizePreviewQueue.Start()
	}

	// 启动 Match302 子账号缓存清理器
	s.balanceCleanupSvc.Start()

	// 启动 Emby 缺集定时扫描调度
	s.embyMissingService.Start()
	s.embyAccountService.Start()

	// 启动 Emby 本地多版本定时检查调度
	s.versionCheckHandler.Start()

	// RSS 中转和自动化分别管理自己的调度生命周期。
	s.rssService.Start()
	s.automationService.Start()

	// 启动Emby代理服务器（如果启用）
	if s.embyProxyServer != nil {
		if err := s.embyProxyServer.Start(); err != nil {
			s.Logger.Errorf("启动Emby代理服务器失败: %v", err)
		}
	}

	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.embyAccountService.Stop()

	// 停止115Open下载服务
	s.download115Service.StopWorkers()

	// 停止令牌刷新服务
	s.tokenRefreshService.Stop()

	// 停止 115 cookie 保活服务
	s.web115KeepAliveService.Stop()

	// 停止 MoviePilot 令牌刷新服务
	s.moviePilotService.Stop()

	// 停止 Emby 封面生成 cron
	s.embyCoverService.Stop()

	// 停止整理日志清理器
	if s.organizeLogCleaner != nil {
		s.organizeLogCleaner.Stop()
	}

	if s.organizePreviewQueue != nil {
		s.organizePreviewQueue.Stop()
	}

	if s.balanceCleanupSvc != nil {
		s.balanceCleanupSvc.Stop()
	}

	if s.embyMissingService != nil {
		s.embyMissingService.Stop()
	}

	if s.versionCheckHandler != nil {
		s.versionCheckHandler.Stop()
	}

	if s.automationService != nil {
		s.automationService.Stop()
	}

	if s.rssService != nil {
		s.rssService.Stop()
	}

	// 停止Emby代理服务器
	if s.embyProxyServer != nil {
		if err := s.embyProxyServer.Stop(ctx); err != nil {
			s.Logger.Errorf("停止Emby代理服务器失败: %v", err)
		}
	}

	s.taskQueue.Stop()

	// 关闭数据库连接
	if err := database.Close(); err != nil {
		s.Logger.Errorf("关闭数据库连接失败: %v", err)
	}
	return s.http.Shutdown(ctx)
}

// setupRoutes 设置API路由
func (s *Server) setupRoutes() {
	// 加载HTML模板
	s.gin.LoadHTMLGlob("dist/*.html")

	// 前端路由处理 - 对于所有非API路径，返回index.html让前端路由处理
	s.gin.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		// 跳过API和webhook路径
		if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/webhook") {
			c.JSON(404, gin.H{"error": "API路径不存在"})
			return
		}
		// 如果是静态文件请求（有文件扩展名），尝试服务静态文件
		if strings.Contains(path, ".") {
			c.File("./dist" + path)
			return
		}
		// 返回前端入口文件
		c.HTML(200, "index.html", nil)
	})

	// 观看记录服务（被 webhook 与统计接口共用）
	embyWatchService := service.NewEmbyWatchService(s.Config, s.Logger, s.embyClient)

	// 创建处理器实例
	systemConfigHandler := handler.NewSystemConfigHandler()
	appConfigHandler := handler.NewAppConfigHandler(s.Logger, s.Config, s.embyClient, s.embyCoverService, s.tmdbService)
	authHandler := handler.NewAuthHandler(s.Config, s.appLoginProtection)
	apiTokenService := service.NewAPITokenService(database.GetDB())
	apiTokenHandler := handler.NewAPITokenHandler(apiTokenService)
	notificationHandler := handler.NewNotificationHandler(s.notificationService)
	automationHandler := handler.NewAutomationHandler(s.automationService)
	rssRelayHandler := handler.NewRSSRelayHandler(s.rssService)
	cloudStorageHandler := handler.NewCloudStorageHandler()
	cloudPathHandler := handler.NewCloudPathHandler()
	mediaRecognitionHandler := handler.NewMediaRecognitionHandler(s.mediaRecognitionService, s.moviePilotService)
	cloudDirectoryHandler := handler.NewCloudDirectoryHandler()
	web115CookieHandler := handler.NewWeb115CookieHandler(s.Logger, s.web115KeepAliveService)
	auth115Handler := handler.NewAuth115Handler(s.Config, s.Logger)
	webhookHandler := handler.NewWebhookHandler(s.Logger, s.Config, s.download115Service, s.embySortNameService, embyWatchService)
	strmHandler := handler.NewStrmHandler(s.Logger, s.download115Service)
	downloadQueueHandler := handler.NewDownloadQueueHandler(s.download115Service)
	pickcodeCacheHandler := handler.NewPickcodeCacheHandler()
	match302Handler := handler.NewMatch302Handler(s.Logger)
	organizeHandler := handler.NewOrganizeHandler(s.Logger, s.moviePilotService, s.tmdbService, s.download115Service, s.embyClient)
	s.automationService.SetOrganizer(organizeHandler)
	s.automationService.SetMediaStatusChecker(organizeHandler)
	s.automationService.SetEmbyClient(s.embyClient)
	organizePreviewQueue := service.NewOrganizePreviewQueue(s.Logger, organizeHandler.ProcessPreviewTask)
	organizeHandler.SetPreviewQueue(organizePreviewQueue)
	s.organizePreviewQueue = organizePreviewQueue
	embyCoverHandler := handler.NewEmbyCoverHandler(s.Logger, s.embyCoverService)
	embySortNameHandler := handler.NewEmbySortNameHandler(s.Logger, s.embySortNameService)
	embyStatsHandler := handler.NewEmbyStatsHandler(s.Logger, s.embyStatsService)
	embyProxyLogHandler := handler.NewEmbyProxyLogHandler(s.embyLoginProtection)
	embyBindingHandler := handler.NewEmbyBindingHandler(s.Logger, s.embyClient)
	embyAccountHandler := handler.NewEmbyAccountHandler(s.embyAccountService)
	embyMissingHandler := handler.NewEmbyMissingHandler(s.Logger, s.embyMissingService)
	embyImageOptimizationHandler := handler.NewEmbyImageOptimizationHandler(s.Logger, s.Config, s.embyClient)
	embyWatchHandler := handler.NewEmbyWatchHandler(s.Logger, embyWatchService)
	organizeLogHandler := handler.NewOrganizeLogHandler()
	logHandler := handler.NewLogHandler()
	systemInfoHandler := handler.NewSystemInfoHandler(service.NewSystemInfoCollector())

	// API路由组
	api := s.gin.Group("/api")
	api.GET("/rss-relay/:id/:token", rssRelayHandler.ReadRSSRelay)

	// 登录页公开配置与公开背景图片
	api.GET("/public-config", appConfigHandler.GetPublic)
	api.GET("/public-assets/login-background/:filename", appConfigHandler.GetLoginBackground)
	api.GET("/public-assets/login-background-emby/:itemID", appConfigHandler.GetEmbyLoginBackground)
	api.GET("/public-assets/avatar/:filename", authHandler.GetAvatar)

	// 认证相关路由（不需要JWT验证）
	auth := api.Group("/auth")
	{
		auth.POST("/login", authHandler.Login)
		auth.POST("/refresh", authHandler.RefreshToken)
		auth.POST("/logout", authHandler.Logout)
	}

	// Webhook 路由组（不需要JWT验证，供外部服务调用）
	webhook := s.gin.Group("/webhook")
	{
		// clouddrive2 相关 webhook
		cloudDrive2Webhook := webhook.Group("/clouddrive2")
		cloudDrive2Webhook.Use(middleware.CloudDrive2WebhookAuth(s.Config))
		cloudDrive2Webhook.POST("/file_notify", webhookHandler.CloudDrive2FileNotify)
		// webhook.POST("/clouddrive2/mount_notify", webhookHandler.CloudDrive2MountNotify)

		// movie-pilot v2 webhook
		webhook.Any("/movie-pilot/v2", webhookHandler.MoviePilotV2Webhook)

		// Emby webhook
		webhook.POST("/emby", webhookHandler.HandleEmbyWebhook)
	}

	// 管理 API 接受登录 JWT 或具有对应模块授权的 API Token。
	protected := api.Group("/")
	protected.Use(middleware.APIAuth(s.Config, apiTokenService), middleware.RequireAdmin())
	{
		// Token 管理不属于任何可授权模块，只允许后台登录 JWT。
		protected.GET("/api-tokens", apiTokenHandler.List)
		protected.GET("/api-tokens/scopes", apiTokenHandler.Scopes)
		protected.POST("/api-tokens", apiTokenHandler.Create)
		protected.POST("/api-tokens/:id/revoke", apiTokenHandler.Revoke)

		// 用户相关
		protected.GET("/me", authHandler.Me)
		protected.PUT("/me", authHandler.UpdateMe)
		protected.POST("/me/avatar", authHandler.UploadAvatar)
		protected.GET("/system-info", systemInfoHandler.Get)

		// 系统配置相关路由
		// 应用配置（config.yaml 在线编辑 + 热重载）
		protected.GET("/app-config", appConfigHandler.Get)
		protected.PUT("/app-config", appConfigHandler.Update)
		protected.POST("/site-assets/login-background", appConfigHandler.UploadLoginBackground)
		protected.POST("/notifications/channels/:channel/test", notificationHandler.TestChannel)
		// 旧版前端兼容入口。
		protected.POST("/telegram/test", notificationHandler.TestTelegram)

		rssRelays := protected.Group("/rss-relays")
		{
			rssRelays.GET("", rssRelayHandler.List)
			rssRelays.POST("", rssRelayHandler.Create)
			rssRelays.GET("/:id", rssRelayHandler.Get)
			rssRelays.PUT("/:id", rssRelayHandler.Update)
			rssRelays.PATCH("/:id/enabled", rssRelayHandler.SetEnabled)
			rssRelays.POST("/:id/refresh", rssRelayHandler.Refresh)
			rssRelays.POST("/:id/token", rssRelayHandler.RotateToken)
			rssRelays.DELETE("/:id", rssRelayHandler.Delete)
		}

		automation := protected.Group("/automations")
		{
			automation.GET("", automationHandler.Dashboard)
			automation.POST("", automationHandler.CreateAutomation)
			automation.POST("/batch", automationHandler.Batch)
			automation.GET("/templates", automationHandler.ListTemplates)
			automation.POST("/templates", automationHandler.SaveTemplate)
			automation.PUT("/templates/:id", automationHandler.SaveTemplate)
			automation.DELETE("/templates/:id", automationHandler.DeleteTemplate)
			automation.PUT("/:id/settings", automationHandler.SaveSettings)
			automation.GET("/:id/revisions", automationHandler.ListRevisions)
			automation.POST("/:id/restore", automationHandler.RestoreRevision)
			automation.POST("/sources/sample", automationHandler.SampleRSSSource)
			automation.PUT("/:id", automationHandler.UpdateAutomation)
			automation.GET("/node-protocols", automationHandler.NodeProtocols)
			automation.PATCH("/:id/enabled", automationHandler.SetAutomationEnabled)
			automation.DELETE("/:id", automationHandler.DeleteAutomation)
			automation.PUT("/:id/source", automationHandler.UpdateSource)
			automation.POST("/:id/scan", automationHandler.Scan)
			automation.POST("/workflow/validate", automationHandler.ValidateWorkflow)
			automation.PUT("/workflows/:id", automationHandler.UpdateWorkflow)
			automation.GET("/targets", automationHandler.ListTargets)
			automation.GET("/targets/status", automationHandler.ListTargetStatuses)
			automation.POST("/targets", automationHandler.CreateTarget)
			automation.PUT("/targets/:id", automationHandler.UpdateTarget)
			automation.DELETE("/targets/:id", automationHandler.DeleteTarget)
			automation.POST("/targets/:id/test", automationHandler.TestTarget)
			automation.GET("/entries", automationHandler.ListEntries)
			automation.POST("/entries/:id/run", automationHandler.RunEntry)
			automation.GET("/runs", automationHandler.ListRuns)
			automation.GET("/runs/:id", automationHandler.GetRun)
			automation.POST("/runs/:id/retry", automationHandler.RetryRun)
			automation.POST("/runs/:id/repeat", automationHandler.RepeatRun)
			automation.POST("/runs/:id/cancel", automationHandler.CancelRun)
		}

		// 下载器账号由独立设置页管理。
		downloaders := protected.Group("/downloaders")
		{
			downloaders.GET("", automationHandler.ListTargets)
			downloaders.GET("/status", automationHandler.ListTargetStatuses)
			downloaders.POST("", automationHandler.CreateTarget)
			downloaders.PUT("/:id", automationHandler.UpdateTarget)
			downloaders.DELETE("/:id", automationHandler.DeleteTarget)
			downloaders.POST("/:id/test", automationHandler.TestTarget)
		}

		config := protected.Group("/config")
		{
			config.GET("/categories", systemConfigHandler.GetConfigCategories)
			config.GET("/types", systemConfigHandler.GetConfigTypes)
		}

		// 网盘存储相关路由
		storage := protected.Group("/cloud-storage")
		{
			// 基础CRUD操作
			storage.POST("/", cloudStorageHandler.CreateCloudStorage)
			storage.GET("/", cloudStorageHandler.GetCloudStorages)
			storage.GET("/:id", cloudStorageHandler.GetCloudStorage)
			storage.PUT("/:id", cloudStorageHandler.UpdateCloudStorage)
			storage.DELETE("/:id", cloudStorageHandler.DeleteCloudStorage)

			// 额外功能
			storage.POST("/:id/refresh", cloudStorageHandler.RefreshToken)
			storage.POST("/:id/test", cloudStorageHandler.TestConnection)
			storage.GET("/types", cloudStorageHandler.GetStorageTypes)
		}

		// 115授权相关路由
		auth115 := protected.Group("/auth/115")
		{
			auth115.POST("/qrcode", auth115Handler.GetQrCode)
			auth115.POST("/status", auth115Handler.CheckStatus)
			auth115.POST("/complete", auth115Handler.CompleteAuth)
			auth115.GET("/sessions", auth115Handler.GetAuthSessions) // 调试用
		}

		// 云盘路径监控相关路由
		paths := protected.Group("/paths")
		{
			// 基础CRUD操作
			paths.POST("/", cloudPathHandler.CreateCloudPath)
			paths.GET("/", cloudPathHandler.GetCloudPaths)
			paths.GET("/:id", cloudPathHandler.GetCloudPath)
			paths.PUT("/:id", cloudPathHandler.UpdateCloudPath)
			paths.DELETE("/:id", cloudPathHandler.DeleteCloudPath)

			// 同步操作（通过webhook触发）
			paths.POST("/:id/sync", cloudPathHandler.SyncCloudPath)
			paths.GET("/:id/status", cloudPathHandler.GetSyncStatus)

			// 批量操作
			paths.POST("/batch", cloudPathHandler.BatchOperation)

			// 配置选项
			paths.GET("/link-types", cloudPathHandler.GetLinkTypes)
			paths.GET("/source-types", cloudPathHandler.GetSourceTypes)
			paths.GET("/strm-content-types", cloudPathHandler.GetStrmContentTypes)
			paths.GET("/path-types", cloudPathHandler.GetPathTypes)

			// 验证和统计
			paths.POST("/validate", cloudPathHandler.ValidateCloudPath)
			paths.GET("/statistics", cloudPathHandler.GetPathStatistics)

			// 导入导出
			paths.GET("/export", cloudPathHandler.ExportPaths)
			paths.POST("/import", cloudPathHandler.ImportPaths)

			// STRM 内容替换
			paths.POST("/:id/strm/replace", cloudPathHandler.ReplaceStrmContent)
		}

		// 云盘目录配置相关路由
		directories := protected.Group("/directories")
		{
			directories.POST("/", cloudDirectoryHandler.CreateCloudDirectory)
			directories.GET("/", cloudDirectoryHandler.GetCloudDirectories)
			directories.GET("/:id", cloudDirectoryHandler.GetCloudDirectory)
			directories.PUT("/:id", cloudDirectoryHandler.UpdateCloudDirectory)
			directories.DELETE("/:id", cloudDirectoryHandler.DeleteCloudDirectory)
		}

		// 115 Cookie 相关接口
		web115 := protected.Group("/115-cookie")
		{
			web115.POST("/dirs", web115CookieHandler.ListDirectories)
			// cookie 保活：手动换端续期 + 状态查询
			web115.POST("/keepalive/refresh", web115CookieHandler.RefreshCookie)
			web115.GET("/keepalive/status", web115CookieHandler.KeepaliveStatus)
		}
		web115Open := protected.Group("/115-open")
		{
			web115Open.POST("/dirs", web115CookieHandler.ListDirectoriesOpenAPI)
		}

		// STRM 相关路由
		strm := protected.Group("/strm")
		{
			// 根据 115 目录树与 world 文件生成 STRM 文件
			strm.POST("/gen/115-directory-tree", strmHandler.GenStrmWith115DirectoryTree)
			// 按云路径映射与云端源目录递归重生成 STRM
			strm.POST("/regenerate-directory", strmHandler.RegenerateDirectory)
		}

		// 115Open 下载队列（成功任务会自动出队）
		downloadQueue := protected.Group("/download-queue")
		{
			downloadQueue.GET("", downloadQueueHandler.List)
			downloadQueue.DELETE("/failed", downloadQueueHandler.ClearFailed)
			downloadQueue.POST("/:id/retry", downloadQueueHandler.Retry)
			downloadQueue.DELETE("/:id", downloadQueueHandler.Remove)
		}

		// FilmFusion 本地识别词、媒体分类配置与识别测试。
		mediaRecognition := protected.Group("/media-recognition")
		{
			mediaRecognition.GET("/medianame-config", mediaRecognitionHandler.GetMedianameConfig)
			mediaRecognition.PUT("/medianame-config", mediaRecognitionHandler.SaveMedianameConfig)
			mediaRecognition.POST("/medianame-config/validate", mediaRecognitionHandler.ValidateMedianameConfig)
			mediaRecognition.POST("/medianame-config/preview", mediaRecognitionHandler.PreviewMedianameTemplate)
			mediaRecognition.POST("/pipeline-test", organizeHandler.TestMediaPipeline)
			mediaRecognition.GET("/words", mediaRecognitionHandler.GetWords)
			mediaRecognition.PUT("/words", mediaRecognitionHandler.UpdateWords)
			mediaRecognition.GET("/category-config", mediaRecognitionHandler.GetCategoryConfig)
			mediaRecognition.POST("/category-config/validate", mediaRecognitionHandler.ValidateCategoryConfig)
			mediaRecognition.PUT("/category-config", mediaRecognitionHandler.UpdateCategoryConfig)
			mediaRecognition.GET("/rename-config", mediaRecognitionHandler.GetRenameConfig)
			mediaRecognition.PUT("/rename-config", mediaRecognitionHandler.UpdateRenameConfig)
			mediaRecognition.POST("/rename-config/validate", mediaRecognitionHandler.ValidateRenameTemplate)
			mediaRecognition.POST("/rename-config/preview", mediaRecognitionHandler.PreviewRenameTemplate)
			mediaRecognition.POST("/test", mediaRecognitionHandler.Test)
			mediaRecognition.POST("/shadow-test", mediaRecognitionHandler.ShadowTest)
		}

		// 整理文件相关路由
		organize := protected.Group("/organize")
		{
			organize.POST("/115", organizeHandler.Organize115)
			organize.POST("/115-cookie", organizeHandler.Organize115Cookie)
			organize.GET("/upgrade/presets", organizeHandler.UpgradePresets)
			organize.POST("/upgrade/test", organizeHandler.TestUpgradePolicy)
			organize.GET("/upgrade/jobs", organizeHandler.ListUpgradeJobs)
			organize.POST("/upgrade/jobs/:id/retry", organizeHandler.RetryUpgradeJob)
			organize.POST("/upgrade/jobs/:id/refresh", organizeHandler.RefreshUpgradeLibrary)
			organize.GET("/category-config", organizeHandler.GetCategoryConfig)
			organize.POST("/media-search", organizeHandler.SearchMedia)
			organize.POST("/media-local-status", organizeHandler.CheckMediaLocalStatus)
			previewTasks := organize.Group("/preview-tasks")
			{
				previewTasks.POST("", organizeHandler.CreatePreviewTasks)
				previewTasks.GET("", organizeHandler.ListPreviewTasks)
				previewTasks.DELETE("", organizeHandler.ClearPreviewTasks)
				previewTasks.GET("/events", organizeHandler.StreamPreviewTaskEvents)
				previewTasks.GET("/:id", organizeHandler.GetPreviewTask)
				previewTasks.POST("/:id/requeue", organizeHandler.RequeuePreviewTask)
				previewTasks.POST("/:id/assign-tmdb", organizeHandler.AssignPreviewTaskTMDB)
				previewTasks.DELETE("/:id", organizeHandler.DeletePreviewTask)
			}
		}

		// 整理日志（STRM 生成 / 文件下载等业务事件）
		organizeLogs := protected.Group("/organize-logs")
		{
			organizeLogs.GET("", organizeLogHandler.List)
			organizeLogs.GET("/stats", organizeLogHandler.Stats)
			organizeLogs.POST("/clear", organizeLogHandler.Clear)
		}

		// 运行日志（server 进程日志文件查看）
		logs := protected.Group("/logs")
		{
			logs.GET("", logHandler.GetLogs)
			logs.GET("/files", logHandler.ListFiles)
		}

		// Pickcode 缓存相关路由
		pickcode := protected.Group("/pickcode-cache")
		{
			// 基础CRUD操作
			pickcode.GET("/", pickcodeCacheHandler.GetPickcodeCaches)
			pickcode.GET("/:id", pickcodeCacheHandler.GetPickcodeCache)
			pickcode.POST("/", pickcodeCacheHandler.CreatePickcodeCache)
			pickcode.PUT("/:id", pickcodeCacheHandler.UpdatePickcodeCache)
			pickcode.DELETE("/:id", pickcodeCacheHandler.DeletePickcodeCache)

			// 批量操作
			pickcode.POST("/batch/delete", pickcodeCacheHandler.BatchDeletePickcodeCaches)
			pickcode.DELETE("/clear", pickcodeCacheHandler.ClearPickcodeCaches)

			// 统计信息
			pickcode.GET("/stats", pickcodeCacheHandler.GetPickcodeCacheStats)
		}

		// Emby 封面生成器
		embyCover := protected.Group("/emby-cover")
		{
			embyCover.GET("/templates", embyCoverHandler.ListTemplates)
			embyCover.GET("/libraries", embyCoverHandler.ListLibraries)
			embyCover.PUT("/libraries/:emby_id", embyCoverHandler.UpsertLibraryConfig)
			embyCover.POST("/libraries/:emby_id/preview", embyCoverHandler.PreviewLibraryCover)
			embyCover.POST("/libraries/:emby_id/generate", embyCoverHandler.GenerateLibraryCover)
			embyCover.POST("/batch-generate", embyCoverHandler.BatchGenerate)
		}

		// Emby SortName 拼音首字母批量回填
		embySortName := protected.Group("/emby-sortname")
		{
			embySortName.GET("/status", embySortNameHandler.Status)
			embySortName.POST("/backfill", embySortNameHandler.Backfill)
			embySortName.POST("/items/:id", embySortNameHandler.ProcessItem)
		}

		// Emby 媒体库电影 / 电视剧数量统计
		embyStats := protected.Group("/emby-stats")
		{
			embyStats.GET("", embyStatsHandler.GetStats)
			embyStats.GET("/image", embyStatsHandler.Image)
		}

		// Emby 代理 302 重定向日志
		embyProxyLog := protected.Group("/emby-proxy")
		{
			embyProxyLog.GET("/302-logs", embyProxyLogHandler.List)
			embyProxyLog.DELETE("/302-logs", embyProxyLogHandler.Clear)
			embyProxyLog.GET("/balance-status", embyProxyLogHandler.BalanceStatus)
			embyProxyLog.GET("/security-status", embyProxyLogHandler.SecurityStatus)
			embyProxyLog.POST("/security-unblock", embyProxyLogHandler.SecurityUnblock)
		}

		// Emby 账号 -> 115 存储 绑定（指定账号强制走指定 cookie）
		embyBindings := protected.Group("/emby-bindings")
		{
			embyBindings.GET("", embyBindingHandler.ListBindings)
			embyBindings.POST("", embyBindingHandler.CreateBinding)
			embyBindings.GET("/emby-users", embyBindingHandler.ListEmbyUsers)
			embyBindings.PUT("/:id", embyBindingHandler.UpdateBinding)
			embyBindings.DELETE("/:id", embyBindingHandler.DeleteBinding)
		}

		// Emby 账户管理与持久化到期禁用（仅 FilmFusion 管理员可访问）。
		embyAccounts := protected.Group("/emby-accounts")
		{
			embyAccounts.GET("", embyAccountHandler.List)
			embyAccounts.PUT("/:user_id/expiry", embyAccountHandler.SetExpiry)
		}

		// Emby 缺集扫描（含定时扫描与黑名单）
		embyMissing := protected.Group("/emby-missing")
		{
			embyMissing.GET("", embyMissingHandler.List)
			embyMissing.POST("/scan", embyMissingHandler.Scan)
			embyMissing.POST("/series/:series_id/scan", embyMissingHandler.RescanSeries)
			embyMissing.DELETE("/series/:series_id", embyMissingHandler.DeleteSeries)
			embyMissing.POST("/resolve-cloud-path", embyMissingHandler.ResolveCloudPath)
			embyMissing.GET("/external-links", embyMissingHandler.ExternalLinks)
			embyMissing.GET("/libraries", embyMissingHandler.ListLibraries)
			embyMissing.GET("/setting", embyMissingHandler.GetSetting)
			embyMissing.PUT("/setting", embyMissingHandler.UpdateSetting)
			embyMissing.GET("/blacklist", embyMissingHandler.ListBlacklist)
			embyMissing.POST("/blacklist", embyMissingHandler.AddBlacklist)
			embyMissing.DELETE("/blacklist/:id", embyMissingHandler.RemoveBlacklist)
		}

		// Emby 本地媒体多版本检查（按云路径映射扫描本地目录）
		embyVersionCheck := protected.Group("/emby-version-check")
		{
			embyVersionCheck.POST("/scan", s.versionCheckHandler.Scan)
			embyVersionCheck.GET("/status", s.versionCheckHandler.Status)
			embyVersionCheck.GET("/setting", s.versionCheckHandler.GetSetting)
			embyVersionCheck.PUT("/setting", s.versionCheckHandler.UpdateSetting)
		}

		// Emby 图片尺寸/质量控制与真实图片对比测试
		embyImageOptimization := protected.Group("/emby-image-optimization")
		{
			embyImageOptimization.GET("/settings", embyImageOptimizationHandler.GetSettings)
			embyImageOptimization.PUT("/settings", embyImageOptimizationHandler.UpdateSettings)
			embyImageOptimization.GET("/samples", embyImageOptimizationHandler.Samples)
			embyImageOptimization.POST("/test", embyImageOptimizationHandler.Test)
		}

		// Emby 观看记录（多用户隔离：配置统计用户 + 历史回填 + 实时采集）
		embyWatch := protected.Group("/emby-watch")
		{
			embyWatch.GET("/users", embyWatchHandler.ListUsers)
			embyWatch.PUT("/users", embyWatchHandler.SaveTrackedUsers)
			embyWatch.GET("/setting", embyWatchHandler.GetSetting)
			embyWatch.PUT("/setting", embyWatchHandler.UpdateSetting)
			embyWatch.POST("/backfill", embyWatchHandler.Backfill)
			embyWatch.GET("/backfill/status", embyWatchHandler.BackfillStatus)
			embyWatch.GET("/records", embyWatchHandler.ListRecords)
			embyWatch.DELETE("/records/:id", embyWatchHandler.DeleteRecord)
			embyWatch.DELETE("/records", embyWatchHandler.ClearRecords)
			embyWatch.GET("/gallery", embyWatchHandler.Gallery)
			embyWatch.GET("/calendar", embyWatchHandler.Calendar)
			embyWatch.GET("/summary", embyWatchHandler.Summary)
			embyWatch.GET("/annual-report", embyWatchHandler.AnnualReport)
			embyWatch.GET("/annual-report/share-image", embyWatchHandler.AnnualShareImage)
			embyWatch.GET("/image", embyWatchHandler.Image)
		}

		// Match302 匹配配置相关路由
		match302 := protected.Group("/match-302")
		{
			// 基础CRUD操作
			match302.GET("/", match302Handler.GetMatch302s)
			match302.POST("/", match302Handler.CreateMatch302)

			// 批量操作
			match302.POST("/batch/delete", match302Handler.BatchDeleteMatch302s)

			// 统计信息
			match302.GET("/stats", match302Handler.GetMatch302Stats)

			// 负载均衡 assignment
			match302.GET("/:id/assignments", match302Handler.GetAssignments)
			match302.POST("/:id/assignments/:assignment_id/retry", match302Handler.RetryAssignment)
			match302.POST("/:id/assignments/:assignment_id/cleanup", match302Handler.CleanupAssignment)
			match302.POST("/:id/assignments/:assignment_id/extend-retention", match302Handler.ExtendAssignmentRetention)
			match302.PATCH("/:id/balance-enabled", match302Handler.UpdateMatch302BalanceEnabled)

			match302.GET("/:id", match302Handler.GetMatch302)
			match302.PUT("/:id", match302Handler.UpdateMatch302)
			match302.DELETE("/:id", match302Handler.DeleteMatch302)
		}
	}
}
