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
	"net/http"

	"github.com/gin-gonic/gin"
)

// Server 表示 HTTP 服务器
type Server struct {
	Config              *config.Config
	Logger              *logger.Logger
	gin                 *gin.Engine
	http                *http.Server
	tokenRefreshService *service.TokenRefreshService
	download115Service  *service.Download115Service
	fileWatcher         *filewatcher.FileWatcherManager
}

// NewServer 创建一个新的 Server 实例
func New(cfg *config.Config, log *logger.Logger) *Server {
	router := gin.Default()

	// 创建115Open下载服务
	download115Service := service.NewDownload115Service(log, cfg.Server.Download115Concurrency)

	s := &Server{
		gin: router,
		http: &http.Server{
			Addr:    ":" + cfg.Server.Port,
			Handler: router,
		},
		Config:              cfg,
		Logger:              log,
		tokenRefreshService: service.NewTokenRefreshService(log),
		download115Service:  download115Service,
	}

	// 设置路由
	s.setupRoutes()

	// 初始化并启动文件监控器
	s.setupFileWatcher()

	return s
}

// Start 启动服务器
func (s *Server) Start() error {
	s.Logger.Infof("在端口 %s 启动服务器", s.http.Addr)

	// 启动令牌刷新服务
	s.tokenRefreshService.Start()

	// 启动115Open下载服务
	s.download115Service.StartWorkers()

	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	// 停止115Open下载服务
	s.download115Service.StopWorkers()

	// 停止令牌刷新服务
	s.tokenRefreshService.Stop()

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
	// 创建处理器实例
	systemConfigHandler := handler.NewSystemConfigHandler()
	authHandler := handler.NewAuthHandler(s.Config)
	cloudStorageHandler := handler.NewCloudStorageHandler()
	cloudPathHandler := handler.NewCloudPathHandler()
	auth115Handler := handler.NewAuth115Handler(s.Config, s.Logger)
	webhookHandler := handler.NewWebhookHandler(s.Logger, s.download115Service)
	strmHandler := handler.NewStrmHandler(s.Logger, s.download115Service)

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

		// STRM 相关路由
		strm := protected.Group("/strm")
		{
			// 新增：根据 115 目录树与 world 文件生成 STRM
			strm.POST("/gen/115-directory-tree", strmHandler.GenStrmWith115DirectoryTree)
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
