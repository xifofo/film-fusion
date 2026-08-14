package handler

import (
	"context"
	"net/http"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type systemInfoRSSGenerator interface {
	WorkerURL() string
	WorkerAccessToken() (string, error)
	WorkerHealth(context.Context) service.RSSGeneratorWorkerStatus
}

// SystemInfoHandler 提供只读运行信息。响应可能包含内部服务凭证，因此只能挂在
// 管理员路由下，并明确禁止浏览器或中间代理缓存。
type SystemInfoHandler struct {
	rssGenerator systemInfoRSSGenerator
}

type RSSGeneratorWorkerSystemInfo struct {
	URL        string                           `json:"url"`
	Token      string                           `json:"token,omitempty"`
	TokenError string                           `json:"token_error,omitempty"`
	Status     service.RSSGeneratorWorkerStatus `json:"status"`
}

type SystemInfo struct {
	RSSGeneratorWorker RSSGeneratorWorkerSystemInfo `json:"rss_generator_worker"`
}

func NewSystemInfoHandler(rssGenerator systemInfoRSSGenerator) *SystemInfoHandler {
	return &SystemInfoHandler{rssGenerator: rssGenerator}
}

// Get GET /api/system-info 返回管理员可见的只读系统信息。
func (h *SystemInfoHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	worker := RSSGeneratorWorkerSystemInfo{
		URL:    h.rssGenerator.WorkerURL(),
		Status: h.rssGenerator.WorkerHealth(c.Request.Context()),
	}
	var err error
	worker.Token, err = h.rssGenerator.WorkerAccessToken()
	if err != nil {
		worker.TokenError = err.Error()
	}

	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: "获取系统信息成功",
		Data:    SystemInfo{RSSGeneratorWorker: worker},
	})
}
