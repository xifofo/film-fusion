package handler

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type RSSRelayHandler struct{ service *service.RSSService }

func NewRSSRelayHandler(rss *service.RSSService) *RSSRelayHandler {
	return &RSSRelayHandler{service: rss}
}

func rssRelayID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("RSS 中转 ID 无效", ""))
		return 0, false
	}
	return uint(id), true
}

func rssRelayResponse(c *gin.Context, data any, err error, status int) {
	c.Header("Cache-Control", "no-store")
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, gorm.ErrRecordNotFound) {
			code = http.StatusNotFound
		}
		var unavailable *service.RSSCacheUnavailableError
		if errors.As(err, &unavailable) {
			code = http.StatusServiceUnavailable
		}
		c.JSON(code, NewErrorResponse("RSS 中转操作失败", err.Error()))
		return
	}
	c.JSON(status, NewSuccessResponse("RSS 中转操作成功", data))
}

func (h *RSSRelayHandler) List(c *gin.Context) {
	data, err := h.service.ListRSSRelays()
	rssRelayResponse(c, data, err, http.StatusOK)
}
func (h *RSSRelayHandler) Get(c *gin.Context) {
	id, ok := rssRelayID(c)
	if !ok {
		return
	}
	data, err := h.service.GetRSSRelay(id)
	rssRelayResponse(c, data, err, http.StatusOK)
}
func (h *RSSRelayHandler) Create(c *gin.Context) {
	var input service.RSSRelayInput
	if err := c.ShouldBindJSON(&input); err != nil {
		rssRelayResponse(c, nil, err, 0)
		return
	}
	data, err := h.service.CreateRSSRelay(input)
	rssRelayResponse(c, data, err, http.StatusCreated)
}
func (h *RSSRelayHandler) Update(c *gin.Context) {
	id, ok := rssRelayID(c)
	if !ok {
		return
	}
	var input service.RSSRelayInput
	if err := c.ShouldBindJSON(&input); err != nil {
		rssRelayResponse(c, nil, err, 0)
		return
	}
	data, err := h.service.UpdateRSSRelay(id, input)
	rssRelayResponse(c, data, err, http.StatusOK)
}
func (h *RSSRelayHandler) SetEnabled(c *gin.Context) {
	id, ok := rssRelayID(c)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		rssRelayResponse(c, nil, err, 0)
		return
	}
	if input.Enabled == nil {
		rssRelayResponse(c, nil, errors.New("必须指定启用状态"), 0)
		return
	}
	data, err := h.service.SetRSSRelayEnabled(id, *input.Enabled)
	rssRelayResponse(c, data, err, http.StatusOK)
}
func (h *RSSRelayHandler) RotateToken(c *gin.Context) {
	id, ok := rssRelayID(c)
	if !ok {
		return
	}
	data, err := h.service.RotateRSSRelayToken(id)
	rssRelayResponse(c, data, err, http.StatusOK)
}
func (h *RSSRelayHandler) Refresh(c *gin.Context) {
	id, ok := rssRelayID(c)
	if !ok {
		return
	}
	data, err := h.service.RefreshRSSRelay(c.Request.Context(), id)
	rssRelayResponse(c, data, err, http.StatusOK)
}
func (h *RSSRelayHandler) Delete(c *gin.Context) {
	id, ok := rssRelayID(c)
	if !ok {
		return
	}
	rssRelayResponse(c, gin.H{}, h.service.DeleteRSSRelay(id), http.StatusOK)
}

// ReadRSSRelay uses a per-relay capability URL, never the administrator JWT.
func (h *RSSRelayHandler) ReadRSSRelay(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	feed, err := h.service.ReadRSSRelay(c.Request.Context(), uint(id), c.Param("token"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		var unavailable *service.RSSCacheUnavailableError
		if errors.As(err, &unavailable) && unavailable.NextFetchAt != nil {
			seconds := max(1, int(math.Ceil(time.Until(*unavailable.NextFetchAt).Seconds())))
			c.Header("Retry-After", strconv.Itoa(seconds))
		}
		c.String(http.StatusServiceUnavailable, "RSS 缓存暂不可用，请稍后重试")
		return
	}
	state := "MISS"
	if feed.CacheHit {
		state = "HIT"
	}
	if feed.CacheStale {
		state = "STALE"
	}
	c.Header("X-RSS-Cache", state)
	if feed.CacheFetchedAt != nil {
		c.Header("X-RSS-Last-Success", feed.CacheFetchedAt.UTC().Format(time.RFC3339))
	}
	if feed.NextFetchAt != nil {
		c.Header("X-RSS-Next-Fetch", feed.NextFetchAt.UTC().Format(time.RFC3339))
	}
	c.Data(http.StatusOK, feed.ContentType, feed.Body)
}
