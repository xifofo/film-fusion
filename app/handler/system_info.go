package handler

import (
	"context"
	"net/http"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type systemInfoCollector interface {
	Collect(context.Context) service.SystemInfo
}

// SystemInfoHandler exposes administrator-only, read-only runtime metrics.
type SystemInfoHandler struct {
	collector systemInfoCollector
}

func NewSystemInfoHandler(collector systemInfoCollector) *SystemInfoHandler {
	return &SystemInfoHandler{collector: collector}
}

// Get GET /api/system-info returns a current host and FilmFusion process
// snapshot. Runtime metrics must never be served from browser/proxy caches.
func (h *SystemInfoHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: "获取系统信息成功",
		Data:    h.collector.Collect(c.Request.Context()),
	})
}
