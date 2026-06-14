package server

import (
	"context"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/filewatcher"
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
	Config                 *config.Config
	Logger                 *logger.Logger
	gin                    *gin.Engine
	http                   *http.Server
	tokenRefreshService    *service.TokenRefreshService
	web115KeepAliveService *service.Web115KeepAliveService
	download115Service     *service.Download115Service
	moviePilotService      *service.MoviePilotService
	embyCoverService       *service.EmbyCoverService
	embySortNameService    *service.EmbySortNameService
	embyStatsService       *service.EmbyStatsService
	embyMissingService     *service.EmbyMissingService
	balanceCleanupSvc      *service.BalanceCleanupService
	embyClient             *embyhelper.EmbyClient
	organizeLogCleaner     *service.OrganizeLogCleaner
	fileWatcher            *filewatcher.FileWatcherManager
	embyProxyServer        *EmbyProxyServer
	taskQueue              *service.PersistentTaskQueue
}

// NewServer 创建一个新的 Server 实例
func New(cfg *config.Config, log *logger.Logger) *Server {
	router := gin.Default()

	// 创建115Open下载服务
	download115Service := service.NewDownload115Service(log, cfg.Server.Download115Concurrency)
	moviePilotService := service.NewMoviePilotService(cfg, log)

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

	s := &Server{
		gin: router,
		http: &http.Server{
			Addr:    ":" + cfg.Server.Port,
			Handler: router,
		},
		Config:                 cfg,
		Logger:                 log,
		tokenRefreshService:    service.NewTokenRefreshService(log),
		web115KeepAliveService: service.NewWeb115KeepAliveService(log),
		download115Service:     download115Service,
		moviePilotService:      moviePilotService,
		embyCoverService:       embyCoverService,
		embySortNameService:    embySortNameService,
		embyStatsService:       embyStatsService,
		embyMissingService:     embyMissingService,
		balanceCleanupSvc:      service.NewBalanceCleanupService(log),
		embyClient:             embyClient,
		organizeLogCleaner:     service.NewOrganizeLogCleaner(log, 0, 0),
		taskQueue:              taskQueue,
	}

	// 设置路由
	s.setupRoutes()

	// 初始化并启动文件监控器
	s.setupFileWatcher()

	// 开启一个 Emby 代理服务
	if cfg.Emby.Enabled {
		s.Logger.Info("Emby服务已启用，正在创建代理服务器...")
		embyProxyServer := NewEmbyProxyServer(cfg, log)
		if embyProxyServer != nil {
			s.embyProxyServer = embyProxyServer
		} else {
			s.Logger.Error("创建Emby代理服务器失败")
		}
	}

	return s
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

	// 启动 Match302 子账号缓存清理器
	s.balanceCleanupSvc.Start()

	// 启动 Emby 缺集定时扫描调度
	s.embyMissingService.Start()

	// 启动Emby代理服务器（如果启用）
	if s.embyProxyServer != nil {
		if err := s.embyProxyServer.Start(); err != nil {
			s.Logger.Errorf("启动Emby代理服务器失败: %v", err)
		}
	}

	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
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

	if s.balanceCleanupSvc != nil {
		s.balanceCleanupSvc.Stop()
	}

	if s.embyMissingService != nil {
		s.embyMissingService.Stop()
	}

	// 停止Emby代理服务器
	if s.embyProxyServer != nil {
		if err := s.embyProxyServer.Stop(ctx); err != nil {
			s.Logger.Errorf("停止Emby代理服务器失败: %v", err)
		}
	}

	s.taskQueue.Stop()

	// 停止文件监控管理器
	s.fileWatcher.Stop()

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

	// 创建处理器实例
	systemConfigHandler := handler.NewSystemConfigHandler()
	authHandler := handler.NewAuthHandler(s.Config)
	cloudStorageHandler := handler.NewCloudStorageHandler()
	cloudPathHandler := handler.NewCloudPathHandler()
	cloudDirectoryHandler := handler.NewCloudDirectoryHandler()
	web115CookieHandler := handler.NewWeb115CookieHandler(s.Logger, s.web115KeepAliveService)
	auth115Handler := handler.NewAuth115Handler(s.Config, s.Logger)
	webhookHandler := handler.NewWebhookHandler(s.Logger, s.Config, s.download115Service, s.embySortNameService)
	strmHandler := handler.NewStrmHandler(s.Logger, s.download115Service)
	pickcodeCacheHandler := handler.NewPickcodeCacheHandler()
	match302Handler := handler.NewMatch302Handler(s.Logger)
	organizeHandler := handler.NewOrganizeHandler(s.Logger, s.moviePilotService, s.download115Service, s.embyClient)
	embyCoverHandler := handler.NewEmbyCoverHandler(s.Logger, s.embyCoverService)
	embySortNameHandler := handler.NewEmbySortNameHandler(s.Logger, s.embySortNameService)
	embyStatsHandler := handler.NewEmbyStatsHandler(s.Logger, s.embyStatsService)
	embyProxyLogHandler := handler.NewEmbyProxyLogHandler()
	embyBindingHandler := handler.NewEmbyBindingHandler(s.Logger, s.embyClient)
	embyMissingHandler := handler.NewEmbyMissingHandler(s.Logger, s.embyMissingService)
	organizeLogHandler := handler.NewOrganizeLogHandler()

	// API路由组
	api := s.gin.Group("/api")

	// 认证相关路由（不需要JWT验证）
	auth := api.Group("/auth")
	{
		auth.POST("/login", authHandler.Login)
		auth.POST("/register", authHandler.Register)
		auth.POST("/refresh", authHandler.RefreshToken)
	}

	// Webhook 路由组（不需要JWT验证，供外部服务调用）
	webhook := s.gin.Group("/webhook")
	{
		// clouddrive2 相关 webhook
		webhook.POST("/clouddrive2/file_notify", webhookHandler.CloudDrive2FileNotify)
		// webhook.POST("/clouddrive2/mount_notify", webhookHandler.CloudDrive2MountNotify)

		// movie-pilot v2 webhook
		webhook.Any("/movie-pilot/v2", webhookHandler.MoviePilotV2Webhook)

		// Emby webhook
		webhook.POST("/emby", webhookHandler.HandleEmbyWebhook)
	}

	// 需要JWT验证的路由
	protected := api.Group("/")
	protected.Use(middleware.JWTAuth(s.Config))
	{
		// 用户相关
		protected.GET("/me", authHandler.Me)

		// 系统配置相关路由
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

		// STRM 相关路由
		strm := protected.Group("/strm")
		{
			// 根据 115 目录树与 world 文件生成 STRM 文件
			strm.POST("/gen/115-directory-tree", strmHandler.GenStrmWith115DirectoryTree)
		}

		// 整理文件相关路由
		organize := protected.Group("/organize")
		{
			organize.POST("/115", organizeHandler.Organize115)
			organize.POST("/115-cookie", organizeHandler.Organize115Cookie)
			organize.POST("/media-search", organizeHandler.SearchMedia)
			organize.POST("/media-local-status", organizeHandler.CheckMediaLocalStatus)
		}

		// 整理日志（STRM 生成 / 文件下载等业务事件）
		organizeLogs := protected.Group("/organize-logs")
		{
			organizeLogs.GET("", organizeLogHandler.List)
			organizeLogs.GET("/stats", organizeLogHandler.Stats)
			organizeLogs.POST("/clear", organizeLogHandler.Clear)
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
		}

		// Emby 代理 302 重定向日志
		embyProxyLog := protected.Group("/emby-proxy")
		{
			embyProxyLog.GET("/302-logs", embyProxyLogHandler.List)
			embyProxyLog.DELETE("/302-logs", embyProxyLogHandler.Clear)
			embyProxyLog.GET("/balance-status", embyProxyLogHandler.BalanceStatus)
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

		// Emby 缺集扫描（含定时扫描与黑名单）
		embyMissing := protected.Group("/emby-missing")
		{
			embyMissing.GET("", embyMissingHandler.List)
			embyMissing.POST("/scan", embyMissingHandler.Scan)
			embyMissing.GET("/libraries", embyMissingHandler.ListLibraries)
			embyMissing.GET("/setting", embyMissingHandler.GetSetting)
			embyMissing.PUT("/setting", embyMissingHandler.UpdateSetting)
			embyMissing.GET("/blacklist", embyMissingHandler.ListBlacklist)
			embyMissing.POST("/blacklist", embyMissingHandler.AddBlacklist)
			embyMissing.DELETE("/blacklist/:id", embyMissingHandler.RemoveBlacklist)
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

// setupFileWatcher 设置文件监控器
func (s *Server) setupFileWatcher() {
	if !s.Config.FileWatcher.Enabled {
		s.Logger.Info("文件监控功能已禁用")
		return
	}

	s.Logger.Info("正在初始化文件监控管理器...")

	// 创建文件监控管理器
	manager, err := filewatcher.NewFileWatcherManager(&s.Config.FileWatcher, s.Logger)
	if err != nil {
		s.Logger.Errorf("创建文件监控管理器失败: %v", err)
		return
	}

	s.fileWatcher = manager

	// 启动文件监控管理器
	if err := s.fileWatcher.Start(); err != nil {
		s.Logger.Errorf("启动文件监控管理器失败: %v", err)
		return
	}

	s.Logger.Info("文件监控管理器初始化并启动成功")
}
