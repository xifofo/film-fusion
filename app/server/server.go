package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/handler"
	"film-fusion/app/logger"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
)

// Server 表示 HTTP 服务器
type Server struct {
	shutdownOnce            sync.Once
	streamShutdown          chan struct{}
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
	plutoFishService        *service.PlutoFishService
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
		streamShutdown: make(chan struct{}),
		gin:            router,
		http: &http.Server{
			Addr:              ":" + cfg.Server.Port,
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
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
	s.plutoFishService = service.NewPlutoFishService(database.GetDB())
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
	if err := s.plutoFishService.Start(); err != nil {
		s.Logger.Errorf("PlutoFish 通知客户端未启动: %v", err)
	}

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

// Shutdown drains both HTTP listeners before stopping their dependencies. If a
// handler exceeds the caller's deadline, leave the database open: Shutdown does
// not prove that handler has stopped using it.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		if s.streamShutdown != nil {
			close(s.streamShutdown)
		}
	})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	if s.http != nil {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.http.Shutdown(ctx) }()
	}
	if s.embyProxyServer != nil {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.embyProxyServer.Stop(ctx) }()
	}
	wg.Wait()
	close(results)
	var ingressErr error
	for err := range results {
		ingressErr = errors.Join(ingressErr, err)
	}

	// Stop producers before downstream downloads, playback and provider clients.
	if s.plutoFishService != nil {
		s.plutoFishService.Stop()
	}
	if s.rssService != nil {
		s.rssService.Stop()
	}
	if s.automationService != nil {
		s.automationService.Stop()
	}
	if s.organizePreviewQueue != nil {
		s.organizePreviewQueue.Stop()
	}
	if s.embyAccountService != nil {
		s.embyAccountService.Stop()
	}
	if s.embyCoverService != nil {
		s.embyCoverService.Stop()
	}
	if s.embyMissingService != nil {
		s.embyMissingService.Stop()
	}
	if s.versionCheckHandler != nil {
		s.versionCheckHandler.Stop()
	}
	if s.download115Service != nil {
		s.download115Service.StopWorkers()
	}
	if s.taskQueue != nil {
		s.taskQueue.Stop()
	}
	if s.tokenRefreshService != nil {
		s.tokenRefreshService.Stop()
	}
	if s.web115KeepAliveService != nil {
		s.web115KeepAliveService.Stop()
	}
	if s.moviePilotService != nil {
		s.moviePilotService.Stop()
	}
	if s.organizeLogCleaner != nil {
		s.organizeLogCleaner.Stop()
	}
	if s.balanceCleanupSvc != nil {
		s.balanceCleanupSvc.Stop()
	}
	if ingressErr != nil {
		return ingressErr
	}
	service.WaitOrganizeLogs()
	return database.Close()
}
