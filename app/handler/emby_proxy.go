package handler

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/store/embyproxylog"
	"film-fusion/app/utils/embyhelper"
	"film-fusion/app/utils/pathhelper"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"gorm.io/gorm"
	"resty.dev/v3"
)

// videoPlayURIRegex 用于识别 Emby 视频播放请求 URI
// 命中表示这是一个播放流请求；未命中表示是其它 API/资源请求
var videoPlayURIRegex = regexp.MustCompile(`/[Vv]ideos/(\S+)/(stream|original|master)`)

// code115FileNotFound 是 115 开放平台「文件(夹)不存在或已删除」的错误码。
const code115FileNotFound = 430004

// SimpleStartInfo 播放开始信息结构体
type SimpleStartInfo struct {
	ItemId string `json:"ItemId"`
}

// SimpleEmbyItemResponse Emby项目响应结构体
type SimpleEmbyItemResponse struct {
	Id          string `json:"Id"`
	SeasonId    string `json:"SeasonId"`
	IndexNumber int    `json:"IndexNumber"`
}

// SimpleEmbyItemResponseList Emby项目列表响应结构体
type SimpleEmbyItemResponseList struct {
	Items []SimpleEmbyItemResponse `json:"Items"`
}

// EmbyProxyHandler Emby代理处理器
type EmbyProxyHandler struct {
	config     *config.Config
	logger     *logger.Logger
	proxy      *httputil.ReverseProxy
	goCache    *cache.Cache
	sdk115Open *sdk115.Client
	balanceSvc *service.BalanceAssignmentService
	web115Svc  *service.Web115Service
}

// NewEmbyProxyHandler 创建新的Emby代理处理器
func NewEmbyProxyHandler(cfg *config.Config, log *logger.Logger) *EmbyProxyHandler {
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
		config:     cfg,
		logger:     log,
		proxy:      proxy,
		goCache:    goCache,
		sdk115Open: sdk115.New(),
		balanceSvc: balanceSvc,
		web115Svc:  service.NewWeb115Service(log),
	}
	proxy.ModifyResponse = h.modifyResponse
	return h
}

// md5CacheKey 生成MD5缓存键
func (h *EmbyProxyHandler) md5CacheKey(input string) string {
	// 创建一个 MD5 哈希实例
	hash := md5.New()

	// 写入数据
	hash.Write([]byte(input))

	// 获取哈希结果
	hashBytes := hash.Sum(nil)

	// 将结果转换为十六进制字符串
	hashString := hex.EncodeToString(hashBytes)

	return hashString
}

// removeQueryParams 移除URL中的查询参数
func (h *EmbyProxyHandler) removeQueryParams(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.RawQuery = ""
	return u.String()
}

// log302 统一记录 302 重定向日志（同时写入内存环形缓冲，供前端查询）。
func (h *EmbyProxyHandler) log302(c *gin.Context, source, target string) {
	method := c.Request.Method
	uri := c.Request.RequestURI
	ua := c.Request.UserAgent()
	remote := c.ClientIP()

	h.logger.Infof("[EMBY PROXY] 302 重定向 source=%s method=%s uri=%s ua=%q remote=%s -> %s",
		source, method, uri, ua, remote, target,
	)

	h.log302Entry(embyproxylog.Entry{
		Source:    source,
		Method:    method,
		URI:       uri,
		UserAgent: ua,
		RemoteIP:  remote,
		Target:    target,
	})
}

func (h *EmbyProxyHandler) log302Entry(entry embyproxylog.Entry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	h.logger.Infof("[EMBY PROXY] 302 source=%s method=%s uri=%s status=%s assignment=%d actual_storage=%d fallback=%s -> %s",
		entry.Source,
		entry.Method,
		entry.URI,
		entry.BalanceStatus,
		entry.AssignmentID,
		entry.ActualStorageID,
		entry.FallbackReason,
		entry.Target,
	)
	embyproxylog.Default().Append(entry)
	embyplayback.Default().AttachRedirect(entry)
}

// ProxyRequest 代理所有Emby请求的主要处理函数
func (h *EmbyProxyHandler) ProxyRequest(c *gin.Context) {
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

// logFallback 记录"播放请求未走 302、走了默认反代"的事件。
// 复用同一份内存环形缓冲，但 source=fallback、target 为空、reason 描述原因。
func (h *EmbyProxyHandler) logFallback(c *gin.Context, reason string) {
	method := c.Request.Method
	uri := c.Request.RequestURI
	ua := c.Request.UserAgent()
	remote := c.ClientIP()

	h.logger.Infof("[EMBY PROXY] 未走302 method=%s uri=%s ua=%q remote=%s reason=%s",
		method, uri, ua, remote, reason,
	)

	h.log302Entry(embyproxylog.Entry{
		Source:         "fallback",
		Method:         method,
		URI:            uri,
		UserAgent:      ua,
		RemoteIP:       remote,
		Target:         reason,
		FallbackReason: reason,
	})
}

func (h *EmbyProxyHandler) modifyResponse(resp *http.Response) error {
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

func (h *EmbyProxyHandler) preheatPlaybackInfo(requestURI string, body []byte) {
	var payload struct {
		MediaSources []struct {
			ID     string `json:"Id"`
			ItemID string `json:"ItemId"`
			Path   string `json:"Path"`
		} `json:"MediaSources"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	for _, mediaSource := range payload.MediaSources {
		mediaPath := pathhelper.EnsureLeadingSlash(mediaSource.Path)
		if strings.TrimSpace(mediaPath) == "" {
			continue
		}
		match, matchedPath, err := h.balanceSvc.FindMatch(mediaPath)
		if err != nil || match == nil || !match.BalanceEnabled {
			continue
		}
		err = h.balanceSvc.PreheatAssignment(context.Background(), service.BalancePlaybackRequest{
			Match:         match,
			SourcePath:    mediaPath,
			MatchedPath:   matchedPath,
			EmbyItemID:    mediaSource.ItemID,
			MediaSourceID: mediaSource.ID,
		})
		if err != nil {
			h.logger.Warnf("[EMBY PROXY] PlaybackInfo 预热失败 uri=%s path=%s err=%v", requestURI, mediaPath, err)
		}
	}
}

// handlePlaying 处理播放会话请求
func (h *EmbyProxyHandler) handlePlaying(c *gin.Context, embyPath string) {
	h.logger.Debug("处理播放会话请求")

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.logger.Warnf("读取播放会话请求失败: %v", err)
		body = nil
	}

	// 将请求正文指针重置到开头
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	playEvent := h.parsePlayingEvent(c, body)
	if strings.EqualFold(embyPath, "/Sessions/Playing") && playEvent.ItemID != "" {
		if h.config.Emby.AddCurrentMediaInfo {
			err := h.GETPlaybackInfo(playEvent.ItemID)
			if err != nil {
				h.logger.Warnf("补充媒体信息失败了: %v", err)
			}
		}

		// 使用 goroutine 获取下一集的媒体信息
		go func() {
			h.GetNextMediaInfo(playEvent.ItemID)
		}()
	}

	// 创建记录器来存储响应内容
	recorder := httptest.NewRecorder()

	// 代理请求
	h.proxy.ServeHTTP(recorder, c.Request)

	if recorder.Code < http.StatusBadRequest {
		h.recordPlayingEvent(embyPath, playEvent)
	}

	// 将记录器的响应写回给客户端
	for key, values := range recorder.Header() {
		for _, value := range values {
			c.Header(key, value)
		}
	}
	c.Status(recorder.Code)
	c.Writer.Write(recorder.Body.Bytes())
}

func isEmbyPlayingEventPath(path string) bool {
	normalized := strings.ToLower(path)
	return normalized == "/sessions/playing" ||
		normalized == "/sessions/playing/progress" ||
		normalized == "/sessions/playing/stopped"
}

func (h *EmbyProxyHandler) parsePlayingEvent(c *gin.Context, body []byte) embyplayback.Event {
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	return embyplayback.Event{
		ItemID:        stringValue(payload, "ItemId", "ItemID"),
		MediaSourceID: stringValue(payload, "MediaSourceId", "MediaSourceID"),
		PlaySessionID: stringValue(payload, "PlaySessionId", "PlaySessionID"),
		RemoteIP:      c.ClientIP(),
		UserAgent:     c.Request.UserAgent(),
	}
}

func (h *EmbyProxyHandler) recordPlayingEvent(embyPath string, event embyplayback.Event) {
	switch strings.ToLower(embyPath) {
	case "/sessions/playing", "/sessions/playing/progress":
		embyplayback.Default().MarkActive(event)
	case "/sessions/playing/stopped":
		embyplayback.Default().MarkStopped(event)
	}
}

func stringValue(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case fmt.Stringer:
			if strings.TrimSpace(typed.String()) != "" {
				return strings.TrimSpace(typed.String())
			}
		}
	}
	return ""
}

// proxyPlay 代理播放请求，返回重定向URL、日志条目、是否跳过和跳过原因。
func (h *EmbyProxyHandler) proxyPlay(c *gin.Context) (string, embyproxylog.Entry, bool, string) {
	currentURI := c.Request.RequestURI
	baseEntry := embyproxylog.Entry{
		Method:    c.Request.Method,
		URI:       currentURI,
		UserAgent: c.Request.UserAgent(),
		RemoteIP:  c.ClientIP(),
	}

	h.logger.Debugf("[EMBY PROXY] ProxyPlay 请求 URI: %s", currentURI)
	re := regexp.MustCompile(`/[Vv]ideos/(\S+)/(stream|original|master)`)
	matches := re.FindStringSubmatch(currentURI)

	if len(matches) < 1 {
		h.logger.Debugf("[EMBY PROXY] ProxyPlay 请求 URI 不匹配: %s", currentURI)
		return "", baseEntry, true, "不是 Emby 视频播放请求"
	}

	// 开始计时
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		h.logger.Infof("[EMBY PROXY] ProxyPlay 执行时间: %v", duration)
	}()

	itemInfoUri, itemId, etag, mediaSourceId, apiKey := embyhelper.GetItemPathInfo(c, h.config)
	embyRes, err := embyhelper.GetEmbyItems(itemInfoUri, itemId, etag, mediaSourceId, apiKey)

	if err != nil {
		h.logger.Errorf("获取 EmbyItems 错误: %v", err)
		return "", baseEntry, true, "获取 EmbyItems 失败: " + err.Error()
	}

	h.logger.Infof("[EMBY PROXY] Request URI: %s", currentURI)

	// EMBY 的播放地址, 兼容 Windows 的 Emby 路径
	embyPlayPath := pathhelper.EnsureLeadingSlash(embyRes.Path)

	h.logger.Infof("[EMBY PROXY] Emby 原地址: %s", embyPlayPath)

	if strings.HasPrefix(embyPlayPath, "http") {
		h.logger.Infof("[EMBY PROXY] Emby 播放地址是完整的 URL: %s", embyPlayPath)
		baseEntry.Source = "proxyPlay"
		baseEntry.Target = embyPlayPath
		baseEntry.ItemID = itemId
		baseEntry.MediaSourceID = mediaSourceId
		baseEntry.MediaPath = embyPlayPath
		return embyPlayPath, baseEntry, false, ""
	}

	// Emby 账号绑定：命中则强制走指定 115 存储，跳过通用就绪缓存命中(避免被路由到非绑定账号)。
	forcedStorageID := h.resolveBoundStorageID(c, itemId)

	if forcedStorageID == 0 {
		if redirectURL, entry, ok := h.proxyReadyBalanceCache(c, baseEntry, embyPlayPath, itemId, mediaSourceId); ok {
			return redirectURL, entry, false, ""
		}
	}

	match, matchedPath, err := h.balanceSvc.FindMatch(embyPlayPath)
	if err != nil {
		h.logger.Debugf("[EMBY PROXY] 路径 %s 未匹配任何 match302 规则", embyPlayPath)
		return "", baseEntry, true, "路径未匹配任何 match302 规则: " + embyPlayPath
	}

	playbackReq := service.BalancePlaybackRequest{
		Match:           match,
		SourcePath:      embyPlayPath,
		MatchedPath:     matchedPath,
		EmbyItemID:      itemId,
		MediaSourceID:   mediaSourceId,
		UserAgent:       c.Request.UserAgent(),
		RemoteIP:        c.ClientIP(),
		ForcedStorageID: forcedStorageID,
	}
	decision, err := h.balanceSvc.ResolvePlayback(context.Background(), playbackReq)
	if err != nil {
		h.logger.Errorf("[EMBY PROXY] Match302 负载均衡决策失败: %v", err)
		return "", baseEntry, true, "Match302 负载均衡决策失败: " + err.Error()
	}

	redirectURL, fromCache, err := h.getDownloadURLForStorage(*decision.PlaybackStorage, decision.ActualPickCode, c.Request.UserAgent())
	if err != nil && decision.UseBalance && !decision.IsSourcePlayback {
		if is115FileNotFound(err) && decision.Assignment != nil {
			h.logger.Warnf("[EMBY PROXY] 子账号目标文件已删除(430004)，失效并重新秒传 assignment=%d", decision.Assignment.ID)
			h.invalidateBalanceAssignment(decision.Assignment.Match302ID, decision.Assignment.ID)
		}
		if decision.SourceStorage == nil {
			h.logger.Warnf("[EMBY PROXY] 获取子账号直链失败，源账号信息缺失，无法回退: %v", err)
			return "", baseEntry, true, "获取子账号直链失败，源账号信息缺失: " + err.Error()
		}
		if limitErr := h.balanceSvc.EnsureStrictStorageAllowed(decision.Match, decision.SourceStorage.ID, playbackReq); limitErr != nil {
			h.logger.Warnf("[EMBY PROXY] 获取子账号直链失败，但严格模式不允许回退源账号: %v", limitErr)
			return "", baseEntry, true, "获取子账号直链失败，严格模式不允许回退源账号: " + limitErr.Error()
		}
		h.logger.Warnf("[EMBY PROXY] 获取子账号直链失败，回退源账号: %v", err)
		decision.PlaybackStorage = decision.SourceStorage
		decision.ActualPickCode = decision.SourceFile.PickCode
		decision.IsSourcePlayback = true
		decision.AccountType = "source"
		decision.Status = "失败回退"
		decision.FallbackReason = "获取子账号直链失败: " + err.Error()
		redirectURL, fromCache, err = h.getDownloadURLForStorage(*decision.SourceStorage, decision.SourceFile.PickCode, c.Request.UserAgent())
	}
	if err != nil {
		h.logger.Errorf("[EMBY PROXY] 获取下载URL失败: %v", err)
		return "", baseEntry, true, "获取下载URL失败: " + err.Error()
	}

	entry := h.buildPlaybackLogEntry(baseEntry, redirectURL, fromCache, itemId, mediaSourceId, embyPlayPath, decision)
	h.logger.Infof("[EMBY PROXY] Match302 匹配成功，重定向到: %s", redirectURL)
	return redirectURL, entry, false, ""
}

// resolveBoundStorageID 解析当前播放的 Emby 用户并返回其绑定的 115 存储ID。
// 无法识别用户或未配置绑定时返回 0。
func (h *EmbyProxyHandler) resolveBoundStorageID(c *gin.Context, itemID string) uint {
	// 无任何启用的绑定时直接跳过，避免每次播放都去解析 Emby 用户
	var bindingCount int64
	if err := database.DB.Model(&model.EmbyAccountBinding{}).Where("enabled = ?", true).Count(&bindingCount).Error; err != nil || bindingCount == 0 {
		return 0
	}

	userID := h.resolveEmbyUserID(c, itemID)
	if userID == "" {
		return 0
	}

	var binding model.EmbyAccountBinding
	if err := database.DB.Where("emby_user_id = ? AND enabled = ?", userID, true).First(&binding).Error; err != nil {
		return 0
	}
	if binding.CloudStorageID == 0 {
		return 0
	}
	h.logger.Infof("[EMBY PROXY] 命中 Emby 账号绑定 user=%s -> storage=%d", userID, binding.CloudStorageID)
	return binding.CloudStorageID
}

// resolveEmbyUserID 解析当前播放用户ID：
// 方案A 用请求 token 调 /Users/Me；A 取不到时用方案B 通过 /Sessions 反查兜底。
func (h *EmbyProxyHandler) resolveEmbyUserID(c *gin.Context, itemID string) string {
	if id := h.resolveEmbyUserIDByToken(embyUserTokenFromRequest(c)); id != "" {
		return id
	}
	return h.resolveEmbyUserIDBySession(itemID, c.ClientIP())
}

// resolveEmbyUserIDByToken 方案A：用请求 token 调 /Users/Me，结果按 token 缓存。
func (h *EmbyProxyHandler) resolveEmbyUserIDByToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	// token 与管理员 APIKey 相同则无用户上下文，跳过(交给方案B)
	if strings.TrimSpace(h.config.Emby.APIKey) != "" && token == strings.TrimSpace(h.config.Emby.APIKey) {
		return ""
	}

	cacheKey := "emby-user-id:token:" + token
	if v, found := h.goCache.Get(cacheKey); found {
		if s, ok := v.(string); ok {
			return s
		}
	}
	user, err := embyhelper.GetUserByToken(h.config, token)
	if err != nil {
		h.logger.Warnf("[EMBY PROXY] 解析 Emby 用户(token)失败: %v", err)
		return "" // 出错不缓存，下次重试
	}
	userID := ""
	if user != nil {
		userID = user.ID
	}
	h.goCache.Set(cacheKey, userID, h.userResolveCacheTTL())
	return userID
}

// resolveEmbyUserIDBySession 方案B：通过 /Sessions 反查正在播放的用户(兜底)，按 item+IP 短时缓存。
func (h *EmbyProxyHandler) resolveEmbyUserIDBySession(itemID, remoteIP string) string {
	itemID = strings.TrimSpace(itemID)
	remoteIP = strings.TrimSpace(remoteIP)
	if itemID == "" && remoteIP == "" {
		return ""
	}

	cacheKey := "emby-user-id:session:" + itemID + "|" + remoteIP
	if v, found := h.goCache.Get(cacheKey); found {
		if s, ok := v.(string); ok {
			return s
		}
	}
	user, err := embyhelper.New(h.config).GetUserBySession(itemID, remoteIP)
	if err != nil {
		h.logger.Warnf("[EMBY PROXY] 解析 Emby 用户(session)失败: %v", err)
		return ""
	}
	userID := ""
	if user != nil {
		userID = user.ID
	}
	// 会话状态会变化，命中/未命中都只短时间缓存
	h.goCache.Set(cacheKey, userID, 60*time.Second)
	return userID
}

func (h *EmbyProxyHandler) userResolveCacheTTL() time.Duration {
	ttl := time.Duration(h.config.Emby.CacheTime) * time.Minute
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return ttl
}

// authTokenRegex 从 Authorization / X-Emby-Authorization 头里提取 Token="xxx"
var authTokenRegex = regexp.MustCompile(`(?i)Token="?([^",\s]+)"?`)

// embyUserTokenFromRequest 从请求中提取 Emby 用户 token(兼容 header / Authorization / query)。
func embyUserTokenFromRequest(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("X-Emby-Token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(c.GetHeader("X-MediaBrowser-Token")); v != "" {
		return v
	}
	// 标准头：Authorization: MediaBrowser Token="xxx" / X-Emby-Authorization
	for _, header := range []string{"Authorization", "X-Emby-Authorization"} {
		if raw := strings.TrimSpace(c.GetHeader(header)); raw != "" {
			if m := authTokenRegex.FindStringSubmatch(raw); len(m) == 2 {
				if token := strings.TrimSpace(m[1]); token != "" {
					return token
				}
			}
		}
	}
	query := c.Request.URL.Query()
	if v := strings.TrimSpace(query.Get("X-Emby-Token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(query.Get("api_key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(query.Get("ApiKey")); v != "" {
		return v
	}
	return ""
}

func (h *EmbyProxyHandler) proxyReadyBalanceCache(c *gin.Context, baseEntry embyproxylog.Entry, embyPlayPath, itemID, mediaSourceID string) (string, embyproxylog.Entry, bool) {
	assignment, err := h.balanceSvc.FindReadyPlaybackCacheByPath(embyPlayPath)
	if err != nil {
		h.logger.Warnf("[EMBY PROXY] 查询 Match302 负载均衡缓存失败 path=%s err=%v", embyPlayPath, err)
		return "", baseEntry, false
	}
	if assignment == nil {
		return "", baseEntry, false
	}
	if assignment.Match302 == nil || assignment.PlaybackStorage == nil {
		h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存关联数据不完整 assignment=%d", assignment.ID)
		return "", baseEntry, false
	}

	playbackReq := service.BalancePlaybackRequest{
		Match:         assignment.Match302,
		SourcePath:    assignment.SourceFilePath,
		MatchedPath:   assignment.TargetPath,
		EmbyItemID:    itemID,
		MediaSourceID: mediaSourceID,
		UserAgent:     c.Request.UserAgent(),
		RemoteIP:      c.ClientIP(),
	}
	if err := h.balanceSvc.EnsureStrictStorageAllowed(assignment.Match302, assignment.PlaybackStorageID, playbackReq); err != nil {
		h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存命中但账号并发限制不允许播放 assignment=%d err=%v", assignment.ID, err)
		return "", baseEntry, false
	}

	redirectURL, _, err := h.getDownloadURLForStorage(*assignment.PlaybackStorage, assignment.TargetPickcode, c.Request.UserAgent())
	if err != nil {
		if is115FileNotFound(err) {
			h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存目标文件已删除(430004)，失效并重新秒传 assignment=%d err=%v", assignment.ID, err)
			h.invalidateBalanceAssignment(assignment.Match302ID, assignment.ID)
		} else {
			h.logger.Warnf("[EMBY PROXY] Match302 负载均衡缓存命中但获取直链失败 assignment=%d err=%v", assignment.ID, err)
		}
		return "", baseEntry, false
	}

	h.balanceSvc.MarkAssignmentPlayed(assignment.ID)
	decision := &service.BalancePlaybackDecision{
		UseBalance:       true,
		Status:           "子账号播放",
		Match:            assignment.Match302,
		Assignment:       assignment,
		SourceStorage:    assignment.SourceStorage,
		PlaybackStorage:  assignment.PlaybackStorage,
		ActualPickCode:   assignment.TargetPickcode,
		IsSourcePlayback: false,
		AccountType:      "member",
	}
	if decision.SourceStorage == nil {
		decision.SourceStorage = assignment.Match302.CloudStorage
	}

	entry := h.buildPlaybackLogEntry(baseEntry, redirectURL, true, itemID, mediaSourceID, embyPlayPath, decision)
	h.logger.Infof("[EMBY PROXY] Match302 负载均衡缓存命中 assignment=%d storage=%d，重定向到: %s", assignment.ID, assignment.PlaybackStorageID, redirectURL)
	return redirectURL, entry, true
}

func (h *EmbyProxyHandler) buildPlaybackLogEntry(base embyproxylog.Entry, target string, fromCache bool, itemID, mediaSourceID, mediaPath string, decision *service.BalancePlaybackDecision) embyproxylog.Entry {
	if fromCache {
		base.Source = "cache"
	} else {
		base.Source = "proxyPlay"
	}
	base.Target = target
	base.ItemID = itemID
	base.MediaSourceID = mediaSourceID
	base.MediaPath = mediaPath
	if decision == nil {
		return base
	}
	if decision.Match != nil {
		base.Match302ID = decision.Match.ID
	}
	if decision.Assignment != nil {
		base.AssignmentID = decision.Assignment.ID
		base.AssignedStorageID = decision.Assignment.PlaybackStorageID
		if decision.Assignment.PlaybackStorage != nil {
			base.AssignedStorageName = decision.Assignment.PlaybackStorage.StorageName
		}
	}
	if decision.PlaybackStorage != nil {
		if base.AssignedStorageID == 0 {
			base.AssignedStorageID = decision.PlaybackStorage.ID
		}
		if base.AssignedStorageName == "" {
			base.AssignedStorageName = decision.PlaybackStorage.StorageName
		}
	}
	if decision.PlaybackStorage != nil {
		base.ActualStorageID = decision.PlaybackStorage.ID
		base.ActualStorageName = decision.PlaybackStorage.StorageName
	}
	base.AccountType = decision.AccountType
	base.BalanceStatus = decision.Status
	base.FallbackReason = decision.FallbackReason
	return base
}

// checkMatch302 检查路径是否匹配 match302 规则
// 参数:
//   - filePath: 要检查的文件路径
//   - userAgent: 用户代理字符串
//
// 返回:
//   - string: 匹配的重定向URL（如果匹配）
//   - bool: 是否匹配到规则
func (h *EmbyProxyHandler) checkMatch302(filePath, userAgent string) (string, bool) {
	// 预加载关联的 CloudStorage 数据，避免 N+1 查询
	var matches []model.Match302
	if err := database.DB.Preload("CloudStorage").Find(&matches).Error; err != nil {
		h.logger.Errorf("[EMBY PROXY] 查询 match302 配置失败: %v", err)
		return "", false
	}

	if len(matches) == 0 {
		h.logger.Debugf("[EMBY PROXY] 未找到任何 match302 规则")
		return "", false
	}

	h.logger.Debugf("[EMBY PROXY] 检查路径 %s 是否匹配 %d 个 match302 规则", filePath, len(matches))

	// 遍历所有规则，找到匹配的
	for _, match := range matches {
		// 检查关联的云存储是否存在
		if match.CloudStorage == nil {
			h.logger.Warnf("[EMBY PROXY] Match302 规则 ID:%d 缺少关联的云存储配置", match.ID)
			continue
		}

		// 暂时只处理 StorageType115Open 的规则
		if match.CloudStorage.StorageType != model.StorageType115Open {
			continue
		}

		// 使用 Match302 模型的 GetMatchedPath 方法检查是否匹配
		matchedPath := match.GetMatchedPath(filePath)

		// 如果路径没发生变化，说明匹配失败
		if matchedPath == filePath {
			continue
		}

		h.logger.Infof("[EMBY PROXY] Match302 规则匹配: %s -> %s (规则: %s -> %s)",
			filePath, matchedPath, match.SourcePath, match.TargetPath)

		// 尝试获取下载URL
		// filePath 是 Emby 看到的播放地址(STRM 内容空间)，用作 pickcode 缓存 key
		// matchedPath 是 115 盘内路径，用于调 115 API 反查 pickcode
		downloadURL, _, err := h.getDownloadURL(filePath, matchedPath, *match.CloudStorage, userAgent)
		if err != nil {
			h.logger.Errorf("[EMBY PROXY] 获取下载URL失败: %v", err)
			continue
		}

		return downloadURL, true
	}

	h.logger.Debugf("[EMBY PROXY] 路径 %s 未匹配任何 match302 规则", filePath)
	return "", false
}

// getDownloadURL 获取文件的下载URL
// filePath: Emby 播放地址(STRM 内容空间的完整绝对路径)，用作 pickcode 缓存 key
// matchedPath: 115 盘内路径，用于调 115 API 反查 pickcode
func (h *EmbyProxyHandler) getDownloadURL(filePath, matchedPath string, storage model.CloudStorage, userAgent string) (string, bool, error) {
	// 缓存 key 使用解码后的 filePath（与 walk 时写入缓存的 key 语义一致）
	cacheKey, decodeErr := url.PathUnescape(filePath)
	if decodeErr != nil {
		cacheKey = filePath
	}
	cacheKey = pathhelper.EnsureLeadingSlash(cacheKey)

	// 检查是否有 pickcode 缓存
	var pickcodeCache model.PickcodeCache
	err := database.DB.Where("file_path = ?", cacheKey).First(&pickcodeCache).Error

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, fmt.Errorf("查询 pickcode 缓存失败: %w", err)
	}

	// 如果没有缓存或 pickcode 为空，则获取新的 pickcode
	if errors.Is(err, gorm.ErrRecordNotFound) || pickcodeCache.Pickcode == "" {
		h.logger.Infof("[EMBY PROXY] 路径 %s 未找到 pickcode 缓存，正在获取", cacheKey)

		pickcode, err := h.fetchPickcodeFromAPI(matchedPath, storage.AccessToken)
		if err != nil {
			return "", false, fmt.Errorf("获取 pickcode 失败: %w", err)
		}

		// 创建或更新缓存（key 用 STRM 内容空间的 cacheKey）
		cache, _, err := model.CreateIfNotExistsStatic(database.DB, cacheKey, pickcode)
		if err != nil {
			h.logger.Errorf("[EMBY PROXY] 保存 pickcode 缓存失败: %v", err)
		}
		pickcodeCache = *cache
	}

	if pickcodeCache.Pickcode == "" {
		return "", false, fmt.Errorf("pickcode 为空")
	}

	return h.getDownloadURLForStorage(storage, pickcodeCache.Pickcode, userAgent)
}

func (h *EmbyProxyHandler) getDownloadURLForStorage(storage model.CloudStorage, pickcode, userAgent string) (string, bool, error) {
	pickcode = strings.TrimSpace(pickcode)
	if pickcode == "" {
		return "", false, fmt.Errorf("pickcode 为空")
	}
	cacheKey := h.md5CacheKey(fmt.Sprintf("download-url:%d:%s:%s", storage.ID, pickcode, userAgent))
	if cacheLink, found := h.goCache.Get(cacheKey); found {
		if link, ok := cacheLink.(string); ok && link != "" {
			return link, true, nil
		}
	}

	var redirectURL string
	if strings.TrimSpace(storage.AccessToken) != "" {
		h.sdk115Open.SetAccessToken(storage.AccessToken)
		downURLResp, err := h.sdk115Open.DownURL(context.Background(), pickcode, userAgent)
		if err != nil {
			return "", false, fmt.Errorf("调用 DownURL API 失败: %w", err)
		}
		for _, urlInfo := range downURLResp {
			if urlInfo.URL.URL != "" {
				redirectURL = urlInfo.URL.URL
				break
			}
		}
	} else if strings.TrimSpace(storage.Cookie) != "" {
		client, err := h.web115Svc.NewClient(storage.Cookie)
		if err != nil {
			return "", false, fmt.Errorf("115 Cookie 无效: %w", err)
		}
		info, err := client.DownloadWithUA(pickcode, userAgent)
		if err != nil {
			return "", false, fmt.Errorf("调用 115 Web 下载接口失败: %w", err)
		}
		redirectURL = info.Url.Url
	}
	if redirectURL == "" {
		return "", false, fmt.Errorf("未找到可用的下载URL，pickcode: %s", pickcode)
	}
	h.goCache.Set(cacheKey, redirectURL, h.downloadURLCacheTTL(redirectURL))
	return redirectURL, false, nil
}

// is115FileNotFound 判断错误是否为 115 开放平台「文件(夹)不存在或已删除」(code 430004)。
// SDK 在业务失败时返回结构化的 *sdk115.Error，可用 errors.As 可靠识别，无需脆弱的字符串匹配。
func is115FileNotFound(err error) bool {
	var sdkErr *sdk115.Error
	if errors.As(err, &sdkErr) {
		return sdkErr.Code == code115FileNotFound
	}
	return false
}

// invalidateBalanceAssignment 让失效的负载均衡 assignment 重新进入秒传流程。
// 场景：子账号上的秒传目标文件已被 115 删除(430004)，但缓存仍标记为 ready 并指向它，
// 导致每次播放都先命中失效缓存、调用 DownURL 失败再回退源账号。通过 RetryAssignment 将其
// 重置为 pending 并后台重新秒传：本次请求回退源账号播放，重传完成后自动恢复负载均衡。
func (h *EmbyProxyHandler) invalidateBalanceAssignment(matchID, assignmentID uint) {
	if matchID == 0 || assignmentID == 0 {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.logger.Errorf("[EMBY PROXY] 失效负载均衡 assignment=%d panic: %v", assignmentID, r)
			}
		}()
		if _, err := h.balanceSvc.RetryAssignment(context.Background(), matchID, assignmentID); err != nil {
			h.logger.Warnf("[EMBY PROXY] 失效负载均衡 assignment=%d 失败: %v", assignmentID, err)
			return
		}
		h.logger.Infof("[EMBY PROXY] 负载均衡目标文件已删除，已触发重新秒传 assignment=%d", assignmentID)
	}()
}

func (h *EmbyProxyHandler) downloadURLCacheTTL(rawURL string) time.Duration {
	configured := time.Duration(h.config.Emby.CacheTime) * time.Minute
	if configured <= 0 {
		configured = cache.DefaultExpiration
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return configured
	}
	rawT := parsed.Query().Get("t")
	if rawT == "" {
		return configured
	}
	expiresAt, err := strconv.ParseInt(rawT, 10, 64)
	if err != nil {
		return configured
	}
	until := time.Until(time.Unix(expiresAt, 0))
	if until <= 0 {
		return time.Second
	}
	if configured > 0 && until < configured {
		return until
	}
	return configured
}

// fetchPickcodeFromAPI 从API获取 pickcode
func (h *EmbyProxyHandler) fetchPickcodeFromAPI(matchedPath, accessToken string) (string, error) {
	h.sdk115Open.SetAccessToken(accessToken)

	folderInfo, err := h.sdk115Open.GetFolderInfoByPath(context.Background(), pathhelper.EnsureLeadingSlash(matchedPath))
	if err != nil {
		return "", fmt.Errorf("获取115Open文件夹信息失败: %w", err)
	}

	return folderInfo.PickCode, nil
}

// GETPlaybackInfo 获取播放信息，使用新的emby客户端方法
func (h *EmbyProxyHandler) GETPlaybackInfo(itemID string) error {
	h.logger.Debugf("获取播放信息，ItemId: %s", itemID)

	// 创建emby客户端
	embyClient := embyhelper.New(h.config)

	// 使用新的GetPlaybackInfo方法获取媒体源信息
	mediaSources, err := embyClient.GetPlaybackInfo(itemID)
	if err != nil {
		return fmt.Errorf("获取播放信息失败: %w", err)
	}

	// 检查是否有媒体源
	if len(mediaSources) == 0 {
		return fmt.Errorf("MediaSources not found or empty")
	}

	// 记录成功获取的信息
	h.logger.Infof("媒体播放信息获取成功: ItemID=%s, MediaSources数量=%d", itemID, len(mediaSources))

	return nil
}

// GetNextMediaInfo 获取下一集媒体信息
func (h *EmbyProxyHandler) GetNextMediaInfo(itemId string) {
	h.logger.Debugf("获取下一集媒体信息，ItemId: %s", itemId)

	if !h.config.Emby.AddNextMediaInfo {
		return
	}

	if h.config.Emby.AdminUserID == "" {
		h.logger.Error("获取下一集的媒体信息失败，因为 admin_user_id 未设置")
		return
	}

	// 这里实现获取下一集媒体信息的逻辑
	// 可以根据具体需求来实现，比如获取剧集的下一集信息

	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("获取下一集媒体信息时发生panic: %v", r)
		}
	}()

	// 获取当前播放详情获取 SeasonId
	client := resty.New()
	defer client.Close()

	res, err := client.R().
		SetQueryParams(map[string]string{
			"api_key": h.config.Emby.APIKey,
		}).
		SetResult(&SimpleEmbyItemResponse{}).
		Get(fmt.Sprintf("%s/Users/%s/Items/%s", h.config.Emby.URL, h.config.Emby.AdminUserID, itemId))

	if err != nil {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", err)
		return
	}

	if res.StatusCode() != 200 {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", res.String())
		return
	}

	response := res.Result().(*SimpleEmbyItemResponse)

	// 请求所有集数
	responseList := &SimpleEmbyItemResponseList{}
	res, err = client.R().
		SetQueryParams(map[string]string{
			"api_key":   h.config.Emby.APIKey,
			"ParentId":  response.SeasonId,
			"Recursive": "true",
			"IsFolder":  "false",
		}).
		SetResult(responseList).
		Get(fmt.Sprintf("%s/Users/%s/Items", h.config.Emby.URL, h.config.Emby.AdminUserID))

	if err != nil {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", err)
		return
	}

	if res.StatusCode() != 200 {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", res.String())
		return
	}

	// 根据 IndexNumber 去获取下一集，并补充媒体信息, 可能存在最后一集没有下一集的情况
	if response.IndexNumber >= len(responseList.Items) {
		return
	}

	nextItem := responseList.Items[response.IndexNumber]

	err = h.GETPlaybackInfo(nextItem.Id)
	if err != nil {
		h.logger.Warnf("补充媒体信息失败了: %v", err)
	}

	h.logger.Infof("成功获取下一集媒体信息，ItemId: %s", itemId)
}
