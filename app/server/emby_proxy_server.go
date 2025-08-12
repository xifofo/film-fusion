package server

import (
	"context"
	"film-fusion/app/config"
	"film-fusion/app/handler"
	"film-fusion/app/logger"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// EmbyProxyServer Emby代理服务器
type EmbyProxyServer struct {
	config  *config.Config
	logger  *logger.Logger
	gin     *gin.Engine
	http    *http.Server
	handler *handler.EmbyProxyHandler
}

// NewEmbyProxyServer 创建新的Emby代理服务器
func NewEmbyProxyServer(cfg *config.Config, log *logger.Logger) *EmbyProxyServer {
	// 设置gin模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	// 添加中间件
	router.Use(gin.Recovery())
	router.Use(func(c *gin.Context) {
		// 简单的访问日志
		start := time.Now()
		c.Next()
		latency := time.Since(start)

		log.Debugf("Emby代理 %s %s %d %v",
			c.Request.Method,
			c.Request.RequestURI,
			c.Writer.Status(),
			latency,
		)
	})

	// 创建Emby代理处理器
	embyHandler := handler.NewEmbyProxyHandler(cfg, log)
	if embyHandler == nil {
		log.Errorf("创建Emby代理处理器失败")
		return nil
	}

	s := &EmbyProxyServer{
		config:  cfg,
		logger:  log,
		gin:     router,
		handler: embyHandler,
		http: &http.Server{
			Addr:         fmt.Sprintf(":%d", cfg.Emby.RunProxyPort),
			Handler:      router,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
	}

	// 设置路由
	s.setupRoutes()

	return s
}

// setupRoutes 设置Emby代理路由
func (s *EmbyProxyServer) setupRoutes() {
	// 代理所有其他请求到Emby服务器（通配符路由必须放在最后）
	s.gin.NoRoute(s.handler.ProxyRequest)
}

// Start 启动Emby代理服务器
func (s *EmbyProxyServer) Start() error {
	s.logger.Infof("启动Emby代理服务器，端口: %d，目标: %s",
		s.config.Emby.RunProxyPort,
		s.config.Emby.URL)

	go func() {
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Errorf("Emby代理服务器启动失败: %v", err)
		}
	}()

	return nil
}

// Stop 停止Emby代理服务器
func (s *EmbyProxyServer) Stop(ctx context.Context) error {
	s.logger.Info("正在停止Emby代理服务器...")
	return s.http.Shutdown(ctx)
}
