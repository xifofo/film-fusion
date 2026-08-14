package handler

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// RSSGeneratorHandler serves both JWT-protected management actions and the
// standalone feed endpoint. Server integration must mount PublicFeed outside
// the protected route group.
type RSSGeneratorHandler struct {
	service *service.RSSGeneratorService
	config  *config.Config
}

func NewRSSGeneratorHandler(generator *service.RSSGeneratorService, cfg *config.Config) *RSSGeneratorHandler {
	return &RSSGeneratorHandler{service: generator, config: cfg}
}

func (h *RSSGeneratorHandler) Dashboard(c *gin.Context) {
	result, err := h.service.Dashboard(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取 RSS Generator 仪表盘失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取 RSS Generator 仪表盘成功", result))
}

func (h *RSSGeneratorHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, NewSuccessResponse("获取 RSS Worker 状态成功", h.service.WorkerHealth(c.Request.Context())))
}

func (h *RSSGeneratorHandler) ListFeeds(c *gin.Context) {
	feeds, err := h.service.ListFeeds()
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取 RSS Feed 失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取 RSS Feed 成功", feeds))
}

func (h *RSSGeneratorHandler) GetFeed(c *gin.Context) {
	id, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	feed, err := h.service.GetFeed(id)
	if respondRSSGeneratorError(c, err, "获取 RSS Feed 失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取 RSS Feed 成功", feed))
}

func (h *RSSGeneratorHandler) CreateFeed(c *gin.Context) {
	var input service.RSSGeneratorFeedInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	feed, err := h.service.CreateFeed(input)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, NewErrorResponse("创建 RSS Feed 失败", err.Error()))
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("RSS Feed 已创建", feed))
}

func (h *RSSGeneratorHandler) UpdateFeed(c *gin.Context) {
	id, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	var input service.RSSGeneratorFeedInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	feed, err := h.service.UpdateFeed(id, input)
	if respondRSSGeneratorError(c, err, "更新 RSS Feed 失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS Feed 已更新", feed))
}

func (h *RSSGeneratorHandler) DeleteFeed(c *gin.Context) {
	id, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	if respondRSSGeneratorError(c, h.service.DeleteFeed(id), "删除 RSS Feed 失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS Feed 已删除", gin.H{}))
}

func (h *RSSGeneratorHandler) Preview(c *gin.Context) {
	var input service.RSSGeneratorPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Definition == nil {
		detail := "definition 不能为空"
		if err != nil {
			detail = err.Error()
		}
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", detail))
		return
	}
	feed, err := h.service.PreviewDefinitionForFeed(c.Request.Context(), input.FeedID, *input.Definition, input.Params)
	if err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("RSS Feed 预览失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS Feed 预览成功", feed))
}

func (h *RSSGeneratorHandler) PreviewSaved(c *gin.Context) {
	id, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	var input service.RSSGeneratorPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	feed, err := h.service.PreviewSaved(c.Request.Context(), id, input.Params)
	if respondRSSGeneratorPreviewError(c, err) {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS Feed 预览成功", feed))
}

func (h *RSSGeneratorHandler) ListTokens(c *gin.Context) {
	feedID, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	tokens, err := h.service.ListTokens(feedID)
	if respondRSSGeneratorError(c, err, "获取订阅凭证失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取订阅凭证成功", tokens))
}

func (h *RSSGeneratorHandler) CreateToken(c *gin.Context) {
	feedID, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	var input service.RSSGeneratorTokenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	result, err := h.service.CreateToken(feedID, input)
	if respondRSSGeneratorError(c, err, "创建订阅凭证失败") {
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("订阅凭证已创建，请立即保存明文 Token", result))
}

func (h *RSSGeneratorHandler) RotateToken(c *gin.Context) {
	feedID, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	tokenID, ok := rssGeneratorParamID(c, "token_id", "订阅凭证")
	if !ok {
		return
	}
	result, err := h.service.RotateToken(feedID, tokenID)
	if respondRSSGeneratorError(c, err, "轮换订阅凭证失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("订阅凭证已轮换，请立即保存新 Token", result))
}

func (h *RSSGeneratorHandler) RevokeToken(c *gin.Context) {
	feedID, ok := rssGeneratorID(c, "RSS Feed")
	if !ok {
		return
	}
	tokenID, ok := rssGeneratorParamID(c, "token_id", "订阅凭证")
	if !ok {
		return
	}
	if respondRSSGeneratorError(c, h.service.RevokeToken(feedID, tokenID), "撤销订阅凭证失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("订阅凭证已撤销", gin.H{}))
}

// PublicFeed handles GET /rss/:feed where :feed is a public feed ID ending in
// .xml or .atom. Direct LAN clients may omit token; non-LAN clients must send
// the feed-specific credential as the token query parameter.
func (h *RSSGeneratorHandler) PublicFeed(c *gin.Context) {
	// Token URLs are credentials. Keep error responses out of shared caches and
	// prevent browser navigation from forwarding the subscription URL as Referer.
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Robots-Tag", "noindex, nofollow")
	publicID, format, ok := parseRSSGeneratorFeedPath(c.Param("feed"))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	query := c.Request.URL.Query()
	clearToken := ""
	if values, exists := query["token"]; exists {
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			c.Status(http.StatusNotFound)
			return
		}
		clearToken = strings.TrimSpace(values[0])
	}
	access, err := h.service.ResolveFeedAccess(
		c.Request.Context(),
		publicID,
		clearToken,
		rssGeneratorRequestIsLAN(c.Request, h.trustedProxyCIDRs()),
	)
	if errors.Is(err, service.ErrRSSGeneratorTokenHidden) {
		c.Status(http.StatusNotFound)
		return
	}
	if errors.Is(err, service.ErrRSSGeneratorRateLimited) {
		c.Header("Retry-After", "60")
		c.Status(http.StatusTooManyRequests)
		return
	}
	if err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	rawParams := make(map[string]any, len(query))
	for key, values := range query {
		if key == "token" {
			continue
		}
		if len(values) != 1 {
			c.Status(http.StatusBadRequest)
			return
		}
		rawParams[key] = values[0]
	}
	rendered, err := h.service.RenderPublic(c.Request.Context(), access, format, rawParams)
	if err != nil {
		if strings.Contains(err.Error(), "参数") {
			c.Status(http.StatusUnprocessableEntity)
		} else {
			c.Status(http.StatusBadGateway)
		}
		return
	}
	c.Header("Cache-Control", "private, max-age=0, must-revalidate")
	c.Header("ETag", rendered.ETag)
	c.Header("Last-Modified", rendered.LastModified.UTC().Format(http.TimeFormat))
	c.Header("X-Feed-Cache", rendered.CacheStatus)
	if requestMatchesRSSGeneratorCache(c.Request, rendered) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, rendered.ContentType, rendered.Body)
}

func (h *RSSGeneratorHandler) trustedProxyCIDRs() []string {
	if h == nil || h.config == nil {
		return nil
	}
	return h.config.Server.Security.TrustedProxyCIDRs
}

func parseRSSGeneratorFeedPath(value string) (string, string, bool) {
	if strings.HasSuffix(value, ".xml") {
		publicID := strings.TrimSuffix(value, ".xml")
		return publicID, "rss", publicID != ""
	}
	if strings.HasSuffix(value, ".atom") {
		publicID := strings.TrimSuffix(value, ".atom")
		return publicID, "atom", publicID != ""
	}
	return "", "", false
}

func rssGeneratorRequestIsLAN(request *http.Request, trustedProxyCIDRs []string) bool {
	client, ok := rssGeneratorClientAddress(request, trustedProxyCIDRs)
	if !ok {
		return false
	}
	return client.IsPrivate() || client.IsLoopback() || client.IsLinkLocalUnicast()
}

// rssGeneratorClientAddress resolves forwarding headers only when the TCP peer
// is explicitly trusted. A forwarding header from any other peer fails closed
// so an unconfigured reverse proxy cannot accidentally turn public requests
// into LAN requests merely because its backend address is private.
func rssGeneratorClientAddress(request *http.Request, trustedProxyCIDRs []string) (netip.Addr, bool) {
	remote, ok := rssGeneratorRemoteAddress(request.RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	hasForwardedHeader := strings.TrimSpace(request.Header.Get("X-Forwarded-For")) != "" ||
		strings.TrimSpace(request.Header.Get("X-Real-IP")) != "" ||
		strings.TrimSpace(request.Header.Get("Forwarded")) != ""
	if !rssGeneratorAddressInCIDRs(remote, trustedProxyCIDRs) {
		if hasForwardedHeader {
			return netip.Addr{}, false
		}
		return remote, true
	}

	candidates := make([]netip.Addr, 0, 4)
	if forwardedFor := strings.TrimSpace(request.Header.Get("X-Forwarded-For")); forwardedFor != "" {
		for _, value := range strings.Split(forwardedFor, ",") {
			candidate, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil {
				return netip.Addr{}, false
			}
			candidates = append(candidates, candidate.Unmap())
		}
	} else if realIP := strings.TrimSpace(request.Header.Get("X-Real-IP")); realIP != "" {
		candidate, err := netip.ParseAddr(realIP)
		if err != nil {
			return netip.Addr{}, false
		}
		candidates = append(candidates, candidate.Unmap())
	}
	if len(candidates) == 0 {
		return netip.Addr{}, false
	}
	for index := len(candidates) - 1; index >= 0; index-- {
		if !rssGeneratorAddressInCIDRs(candidates[index], trustedProxyCIDRs) {
			return candidates[index], true
		}
	}
	return candidates[0], true
}

func rssGeneratorRemoteAddress(value string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		host = strings.TrimSpace(value)
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func rssGeneratorAddressInCIDRs(address netip.Addr, cidrs []string) bool {
	address = address.Unmap()
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Contains(address) {
			return true
		}
		if trusted, err := netip.ParseAddr(raw); err == nil && trusted.Unmap() == address {
			return true
		}
	}
	return false
}

func requestMatchesRSSGeneratorCache(request *http.Request, rendered service.RSSGeneratorRenderedFeed) bool {
	if etag := strings.TrimSpace(request.Header.Get("If-None-Match")); etag != "" {
		for _, candidate := range strings.Split(etag, ",") {
			if strings.TrimSpace(candidate) == rendered.ETag || strings.TrimSpace(candidate) == "*" {
				return true
			}
		}
		return false
	}
	if modifiedSince := request.Header.Get("If-Modified-Since"); modifiedSince != "" {
		if parsed, err := http.ParseTime(modifiedSince); err == nil {
			return !rendered.LastModified.After(parsed.Add(time.Second))
		}
	}
	return false
}

func rssGeneratorID(c *gin.Context, label string) (uint, bool) {
	return rssGeneratorParamID(c, "id", label)
}

func rssGeneratorParamID(c *gin.Context, param, label string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param(param), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse(label+" ID 无效", ""))
		return 0, false
	}
	return uint(id), true
}

func respondRSSGeneratorError(c *gin.Context, err error, message string) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, service.ErrRSSGeneratorNotFound), errors.Is(err, service.ErrRSSGeneratorTokenHidden):
		c.JSON(http.StatusNotFound, NewErrorResponse(message, ""))
	default:
		c.JSON(http.StatusUnprocessableEntity, NewErrorResponse(message, err.Error()))
	}
	return true
}

func respondRSSGeneratorPreviewError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, service.ErrRSSGeneratorNotFound) {
		c.JSON(http.StatusNotFound, NewErrorResponse("RSS Feed 不存在", ""))
	} else {
		c.JSON(http.StatusBadGateway, NewErrorResponse("RSS Feed 预览失败", err.Error()))
	}
	return true
}
