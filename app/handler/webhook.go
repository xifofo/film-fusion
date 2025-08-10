package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"net/http"

	"github.com/gin-gonic/gin"
)

// WebhookHandler 处理 webhook 相关请求
type WebhookHandler struct {
	logger       *logger.Logger
	cd2NotifySvc *service.CD2NotifyService
	md2NotifySvc *service.MoviePilot2NotifyService
}

// NewWebhookHandler 创建新的 WebhookHandler
func NewWebhookHandler(log *logger.Logger, download115Svc *service.Download115Service) *WebhookHandler {
	return &WebhookHandler{
		logger:       log,
		cd2NotifySvc: service.NewCD2NotifyService(log, download115Svc),
		md2NotifySvc: service.NewMoviePilot2NotifyService(log, download115Svc),
	}
}

// CloudDrive2FileNotify 处理 clouddrive2 文件系统监听器的 webhook
func (h *WebhookHandler) CloudDrive2FileNotify(c *gin.Context) {
	// 解析请求体
	var requestBody service.Cd2FileNotifyRequest
	if err := c.Bind(&requestBody); err != nil {
		h.logger.Errorf("解析 clouddrive2 文件通知请求体失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	var cloudPaths []model.CloudPath
	err := database.DB.Where("source_type = ?", model.SourceTypeCloudDrive2).Preload("CloudStorage").Find(&cloudPaths).Error

	if err != nil {
		h.logger.Errorf("获取 CloudPath 记录失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"message": err})
		return
	}

	go h.cd2NotifySvc.ProcessFileNotify(requestBody.Data, cloudPaths)

	c.JSON(http.StatusOK, gin.H{
		"message": "File notification received and processing started",
	})
}

// MoviePilotV2Webhook 接收 movie-pilot v2 的 webhook，仅打印接收的参数
func (h *WebhookHandler) MoviePilotV2Webhook(c *gin.Context) {
	// 解析请求体
	var requestBody service.MoviePilot2NotifyRequestData
	if err := c.Bind(&requestBody); err != nil {
		h.logger.Errorf("解析 movie-pilot v2 请求体失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	var cloudPaths []model.CloudPath
	err := database.DB.Where("source_type = ?", model.SourceTypeMoviePilot2).Preload("CloudStorage").Find(&cloudPaths).Error

	if err != nil {
		h.logger.Errorf("获取 CloudPath 记录失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"message": err})
		return
	}

	go h.md2NotifySvc.ProcessNotify(requestBody, cloudPaths)

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}
