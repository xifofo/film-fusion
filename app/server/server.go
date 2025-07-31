package server

import (
	"context"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/handler"
	"film-fusion/app/logger"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Server 表示 HTTP 服务器
type Server struct {
	Config *config.Config
	Logger *logger.Logger
	gin    *gin.Engine
	http   *http.Server
}

// NewServer 创建一个新的 Server 实例
func New(cfg *config.Config, log *logger.Logger) *Server {
	router := gin.Default()

	s := &Server{
		gin: router,
		http: &http.Server{
			Addr:    ":" + cfg.Server.Port,
			Handler: router,
		},
		Config: cfg,
		Logger: log,
	}

	// 设置路由
	s.setupRoutes()

	return s
}

// Start 启动服务器
func (s *Server) Start() error {
	s.Logger.Infof("在端口 %s 启动服务器", s.http.Addr)

	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
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

	// API路由组
	api := s.gin.Group("/api/v1")

	// 系统配置相关路由
	config := api.Group("/config")
	{
		config.GET("/categories", systemConfigHandler.GetConfigCategories)
		config.GET("/types", systemConfigHandler.GetConfigTypes)
	}
}
