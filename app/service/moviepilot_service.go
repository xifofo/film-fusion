package service

import (
	"context"
	"net/http"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
)

const (
	moviePilotTokenCheckInterval     = 30 * time.Minute
	moviePilotTokenSkew              = 2 * time.Minute
	moviePilotRequestTimeout         = 8 * time.Second
	moviePilotLocalFallbackTTL       = 2 * time.Minute
	MediaRecognitionSourceMoviePilot = config.MediaRecognitionSourceMoviePilot
	MediaRecognitionSourceLocal      = config.MediaRecognitionSourceLocal
	MediaRecognitionSourceMedianame  = config.MediaRecognitionSourceMedianame
	MediaRecognitionSourceShadow     = config.MediaRecognitionSourceShadow
)

type MoviePilotService struct {
	logger       *logger.Logger
	cfg          *config.Config
	client       *http.Client
	operationCtx context.Context
	stateOwner   *MoviePilotService
	local        *MediaRecognitionService

	mu             sync.RWMutex
	accessToken    string
	tokenExpiresAt time.Time
	tokenRefresh   *moviePilotTokenRefresh
	fallbackUntil  time.Time
	fallbackReason string

	stopChan chan struct{}
	wg       sync.WaitGroup
	ticker   *time.Ticker
}

func NewMoviePilotService(cfg *config.Config, log *logger.Logger) *MoviePilotService {
	return &MoviePilotService{
		logger:   log,
		cfg:      cfg,
		client:   &http.Client{Timeout: moviePilotRequestTimeout},
		stopChan: make(chan struct{}),
	}
}

// SetLocalMediaRecognition 为识别路由和影子对比接入 FilmFusion 本地引擎。
func (s *MoviePilotService) SetLocalMediaRecognition(local *MediaRecognitionService) {
	s.local = local
}

func (s *MoviePilotService) Start() {
	if !s.isConfigured() {
		s.logger.Warn("MoviePilot 未配置，跳过令牌定时刷新")
		return
	}

	s.ticker = time.NewTicker(moviePilotTokenCheckInterval)
	s.wg.Add(1)
	go s.run()
	s.logger.Info("MoviePilot 令牌刷新服务已启动")
}

func (s *MoviePilotService) Stop() {
	if s.ticker == nil {
		return
	}
	close(s.stopChan)
	s.ticker.Stop()
	s.wg.Wait()
	s.logger.Info("MoviePilot 令牌刷新服务已停止")
}

func (s *MoviePilotService) run() {
	defer s.wg.Done()

	_, _ = s.refreshToken()

	for {
		select {
		case <-s.ticker.C:
			_, _ = s.refreshToken()
		case <-s.stopChan:
			return
		}
	}
}
