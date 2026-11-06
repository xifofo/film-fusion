package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
)

// videoPlayURIRegex 用于识别 Emby 视频播放请求 URI
// 命中表示这是一个播放流请求；未命中表示是其它 API/资源请求
var videoPlayURIRegex = regexp.MustCompile(`/[Vv]ideos/(\S+)/(stream|original|master)`)

// EmbyProxyHandler Emby代理处理器
type EmbyProxyHandler struct {
	config          *config.Config
	logger          *logger.Logger
	proxy           *httputil.ReverseProxy
	goCache         *cache.Cache
	sdk115Open      *sdk115.Client
	balanceSvc      *service.BalanceAssignmentService
	web115Svc       *service.Web115Service
	loginProtection *service.EmbyLoginProtection
}

type embyLoginAttemptContextKey struct{}

// NewEmbyProxyHandler 创建新的Emby代理处理器
func NewEmbyProxyHandler(cfg *config.Config, log *logger.Logger, loginProtection *service.EmbyLoginProtection) *EmbyProxyHandler {
	// 解析Emby服务器URL
	embyURL, err := url.Parse(cfg.Emby.URL)
	if err != nil {
		log.Errorf("解析Emby URL失败: %v", err)
		return nil
	}

	balanceSvc := service.NewBalanceAssignmentService(log)

	// 创建反向代理
	proxy := httputil.NewSingleHostReverseProxy(embyURL)

	// 自定义Director函数来修改请求
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		// 添加自定义逻辑，比如修改请求头等
		req.Header.Set("X-Forwarded-Proto", "http")
		req.Header.Set("X-Forwarded-Host", req.Host)
	}

	// 错误处理
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Errorf("Emby代理请求失败: %v", err)
		http.Error(w, "代理服务暂时不可用", http.StatusBadGateway)
	}

	// 创建缓存，默认过期时间使用配置中的缓存时间，清理间隔为10分钟
	cacheExpiration := time.Duration(cfg.Emby.CacheTime) * time.Minute
	goCache := cache.New(cacheExpiration, 10*time.Minute)

	h := &EmbyProxyHandler{
		config:          cfg,
		logger:          log,
		proxy:           proxy,
		goCache:         goCache,
		sdk115Open:      sdk115.New(),
		balanceSvc:      balanceSvc,
		web115Svc:       service.NewWeb115Service(log),
		loginProtection: loginProtection,
	}
	proxy.ModifyResponse = h.modifyResponse
	return h
}

// ProxyRequest 代理所有Emby请求的主要处理函数
func (h *EmbyProxyHandler) ProxyRequest(c *gin.Context) {
	if attempt, blocked := h.loginProtection.Inspect(c.Request); attempt != nil {
		if blocked {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"ResponseStatus": gin.H{
					"ErrorCode": "Unauthorized",
					"Message":   "Invalid username or password",
				},
			})
			return
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), embyLoginAttemptContextKey{}, attempt))
	}

	optimization := service.ApplyEmbyImageOptimization(
		c.Request.URL.Path,
		c.Request.URL.Query(),
		h.config.Emby.ImageOptimization,
	)
	if optimization.Changed {
		c.Request.URL.RawQuery = optimization.Query.Encode()
		c.Request.RequestURI = c.Request.URL.RequestURI()
		h.logger.Debugf("[EMBY PROXY] 图片优化 profile=%s uri=%s", optimization.Profile, c.Request.RequestURI)
	}

	currentURI := c.Request.RequestURI

	u, err := url.Parse(currentURI)
	if err == nil {
		// 移除 /emby 前缀（如果存在）
		removeEmbyRequestPath := strings.Replace(u.Path, "/emby", "", 1)

		// 特殊路径处理
		if isEmbyPlayingEventPath(removeEmbyRequestPath) {
			h.handlePlaying(c, removeEmbyRequestPath)
			return
		}
	}

	// 尝试代理播放请求
	redirectURL, logEntry, skip, fallbackReason := h.proxyPlay(c)
	if !skip {
		h.log302Entry(logEntry)
		c.Redirect(http.StatusFound, redirectURL)
		return
	}

	// 走到这里说明没命中 302 重定向；如果是视频播放请求，单独打一条
	// fallback 日志，方便排查"该走 302 但没走"的场景。
	if videoPlayURIRegex.MatchString(currentURI) {
		if strings.TrimSpace(fallbackReason) == "" {
			fallbackReason = "未命中 match302 / 缓存，走默认反代"
		}
		h.logFallback(c, fallbackReason)
	}

	// 默认代理请求
	h.proxy.ServeHTTP(c.Writer, c.Request)
}

func (h *EmbyProxyHandler) modifyResponse(resp *http.Response) error {
	if resp != nil && resp.Request != nil && h.loginProtection != nil {
		attempt, _ := resp.Request.Context().Value(embyLoginAttemptContextKey{}).(*service.EmbyLoginAttempt)
		h.loginProtection.ObserveResponse(attempt, resp.StatusCode)
	}
	if resp == nil || resp.Request == nil || !embyhelper.IsPlaybackInfoURI(resp.Request.RequestURI) {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.logger.Errorf("读取 PlaybackInfo 响应体失败: %v", err)
		return err
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	go h.preheatPlaybackInfo(resp.Request.RequestURI, body)
	return nil
}
