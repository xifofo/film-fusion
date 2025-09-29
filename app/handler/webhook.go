package handler

import (
	"encoding/json"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// WebhookHandler 处理 webhook 相关请求
type WebhookHandler struct {
	logger       *logger.Logger
	config       *config.Config
	cd2NotifySvc *service.CD2NotifyService
	md2NotifySvc *service.MoviePilot2NotifyService
}

// NewWebhookHandler 创建新的 WebhookHandler
func NewWebhookHandler(log *logger.Logger, cfg *config.Config, download115Svc *service.Download115Service) *WebhookHandler {
	return &WebhookHandler{
		logger:       log,
		config:       cfg,
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

	jsonData, _ := json.Marshal(requestBody)
	h.logger.Debugf("记录 CD2 Webhook 数据: %s", jsonData)

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

	jsonData, _ := json.Marshal(requestBody)
	h.logger.Debugf("MoviePilot V2 Webhook 请求体: %s", string(jsonData))

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

// EmbyWebhookRequest 定义 Emby webhook 请求的数据结构
type EmbyWebhookRequest struct {
	Title       string     `json:"Title"`
	Description string     `json:"Description,omitempty"`
	Date        time.Time  `json:"Date"`
	Event       string     `json:"Event"`
	Severity    string     `json:"Severity"`
	Item        EmbyItem   `json:"Item"`
	Server      EmbyServer `json:"Server"`
}

// EmbyItem 定义 Emby 媒体项目的数据结构
type EmbyItem struct {
	Name                    string                 `json:"Name"`
	ServerId                string                 `json:"ServerId"`
	Id                      string                 `json:"Id"`
	DateCreated             time.Time              `json:"DateCreated"`
	SortName                string                 `json:"SortName"`
	PremiereDate            *time.Time             `json:"PremiereDate,omitempty"`
	ExternalUrls            []ExternalURL          `json:"ExternalUrls"`
	Path                    string                 `json:"Path"`
	Overview                string                 `json:"Overview,omitempty"`
	Taglines                []string               `json:"Taglines"`
	Genres                  []string               `json:"Genres"`
	FileName                string                 `json:"FileName"`
	ProductionYear          int                    `json:"ProductionYear,omitempty"`
	IndexNumber             *int                   `json:"IndexNumber,omitempty"`
	ParentIndexNumber       *int                   `json:"ParentIndexNumber,omitempty"`
	RemoteTrailers          []interface{}          `json:"RemoteTrailers"`
	ProviderIds             map[string]interface{} `json:"ProviderIds"`
	IsFolder                bool                   `json:"IsFolder"`
	ParentId                string                 `json:"ParentId,omitempty"`
	Type                    string                 `json:"Type"`
	Studios                 []interface{}          `json:"Studios"`
	GenreItems              []interface{}          `json:"GenreItems"`
	TagItems                []interface{}          `json:"TagItems"`
	ParentLogoItemId        string                 `json:"ParentLogoItemId,omitempty"`
	ParentBackdropItemId    string                 `json:"ParentBackdropItemId,omitempty"`
	ParentBackdropImageTags []string               `json:"ParentBackdropImageTags,omitempty"`
	SeriesName              string                 `json:"SeriesName,omitempty"`
	SeriesId                string                 `json:"SeriesId,omitempty"`
	SeasonId                string                 `json:"SeasonId,omitempty"`
	PrimaryImageAspectRatio float64                `json:"PrimaryImageAspectRatio,omitempty"`
	SeriesPrimaryImageTag   string                 `json:"SeriesPrimaryImageTag,omitempty"`
	SeasonName              string                 `json:"SeasonName,omitempty"`
	ImageTags               map[string]string      `json:"ImageTags,omitempty"`
	BackdropImageTags       []string               `json:"BackdropImageTags"`
	ParentLogoImageTag      string                 `json:"ParentLogoImageTag,omitempty"`
	ParentThumbItemId       string                 `json:"ParentThumbItemId,omitempty"`
	ParentThumbImageTag     string                 `json:"ParentThumbImageTag,omitempty"`
	MediaType               string                 `json:"MediaType"`
}

// ExternalURL 定义外部链接的数据结构
type ExternalURL struct {
	Name string `json:"Name"`
	Url  string `json:"Url"`
}

// EmbyServer 定义 Emby 服务器的数据结构
type EmbyServer struct {
	Name    string `json:"Name"`
	Id      string `json:"Id"`
	Version string `json:"Version"`
}

// HandleEmbyWebhook 处理 Emby webhook 请求
func (h *WebhookHandler) HandleEmbyWebhook(c *gin.Context) {
	// 读取请求体
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.logger.Errorf("读取 webhook 请求体失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}

	// 解析 webhook 数据
	var webhookData EmbyWebhookRequest
	if err := json.Unmarshal(body, &webhookData); err != nil {
		h.logger.Errorf("Emby webhook JSON 解析失败: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "JSON 解析失败"})
		return
	}

	// 处理不同类型的事件
	switch webhookData.Event {
	case "library.new":
		h.handleLibraryNew(webhookData)
	default:
		h.logger.Infof("收到事件类型: %s，暂不处理", webhookData.Event)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "ok",
		"event":   webhookData.Event,
		"status":  "已处理",
	})
}

// handleLibraryNew 处理新增媒体事件
func (h *WebhookHandler) handleLibraryNew(data EmbyWebhookRequest) {
	// 判断是否处理该事件
	if !h.config.Server.ProcessNewMedia {
		h.logger.Infof("新增媒体事件处理已禁用，跳过处理: %s", data.Item.Name)
		return
	}

	// 获取持久化任务队列并添加任务
	taskQueue := service.GetTaskQueue()
	if taskQueue == nil {
		h.logger.Error("任务队列未初始化，无法添加任务")
		return
	}

	// 添加任务到持久化队列
	if err := taskQueue.AddTask(data.Item.Id); err != nil {
		h.logger.Errorf("添加媒体处理任务失败: %v", err)
	} else {
		h.logger.Infof("媒体处理任务已添加到队列: ItemID=%s", data.Item.Id)
	}
}
