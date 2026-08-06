package handler

import (
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/logger"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// EmbyStatsHandler 媒体库电影 / 电视剧数量统计接口
type EmbyStatsHandler struct {
	log *logger.Logger
	svc *service.EmbyStatsService
}

// NewEmbyStatsHandler 构造
func NewEmbyStatsHandler(log *logger.Logger, svc *service.EmbyStatsService) *EmbyStatsHandler {
	return &EmbyStatsHandler{log: log, svc: svc}
}

// GetStats GET /api/emby-stats
// 返回各媒体库 Movie / Series 数量及全局合计
func (h *EmbyStatsHandler) GetStats(c *gin.Context) {
	stats, err := h.svc.Collect(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取 Emby 统计失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", stats))
}

// Image GET /api/emby-stats/image 代理媒体库封面（前端 <img> 通过 ?token= 鉴权）。
func (h *EmbyStatsHandler) Image(c *gin.Context) {
	maxWidth, _ := strconv.Atoi(c.Query("max_width"))
	data, contentType, err := h.svc.LibraryImage(
		strings.TrimSpace(c.Query("item_id")),
		strings.TrimSpace(c.Query("type")),
		maxWidth,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, NewErrorResponse("获取媒体库封面失败", err.Error()))
		return
	}
	if contentType == "" {
		contentType = "image/jpeg"
	}
	c.Header("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, contentType, data)
}
