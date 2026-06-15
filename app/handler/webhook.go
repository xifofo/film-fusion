package handler

import (
	"context"
	"encoding/json"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// WebhookHandler 处理 webhook 相关请求
type WebhookHandler struct {
	logger       *logger.Logger
	config       *config.Config
	cd2NotifySvc *service.CD2NotifyService
	md2NotifySvc *service.MoviePilot2NotifyService
	sortNameSvc  *service.EmbySortNameService
	watchSvc     *service.EmbyWatchService
}

// NewWebhookHandler 创建新的 WebhookHandler
func NewWebhookHandler(log *logger.Logger, cfg *config.Config, download115Svc *service.Download115Service, sortNameSvc *service.EmbySortNameService, watchSvc *service.EmbyWatchService) *WebhookHandler {
	return &WebhookHandler{
		logger:       log,
		config:       cfg,
		cd2NotifySvc: service.NewCD2NotifyService(log, download115Svc),
		md2NotifySvc: service.NewMoviePilot2NotifyService(log, download115Svc),
		sortNameSvc:  sortNameSvc,
		watchSvc:     watchSvc,
	}
}

// CloudDrive2FileNotify 处理 clouddrive2 文件系统监听器的 webhook
func (h *WebhookHandler) CloudDrive2FileNotify(c *gin.Context) {
	// 解析请求体
	var requestBody service.Cd2FileNotifyRequest
	if err := c.Bind(&requestBody); err != nil {
		h.logger.Errorf("解析 clouddrive2 文件通知请求体失败: %v", err)
		service.WriteOrganizeLog(h.logger, service.OrganizeLogEntry{
			Action: model.OrganizeActionWebhookRecv, Status: model.OrganizeStatusFailed,
			Trigger: model.OrganizeTriggerCD2, Error: err.Error(), Message: "解析 clouddrive2 请求体失败",
		})
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	// 记录接收：多条 data 合并成一条，重点存首个文件路径 + 总数
	cd2Summary := fmt.Sprintf("items=%d", len(requestBody.Data))
	var cd2Source, cd2Target string
	if len(requestBody.Data) > 0 {
		first := requestBody.Data[0]
		cd2Source = first.SourceFile
		cd2Target = first.DestinationFile
		cd2Summary = fmt.Sprintf("action=%s isDir=%s items=%d", first.Action, first.IsDir, len(requestBody.Data))
	}
	service.WriteOrganizeLog(h.logger, service.OrganizeLogEntry{
		Action: model.OrganizeActionWebhookRecv, Status: model.OrganizeStatusSuccess,
		Trigger: model.OrganizeTriggerCD2, Source: cd2Source, Target: cd2Target, Message: cd2Summary,
	})

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
		service.WriteOrganizeLog(h.logger, service.OrganizeLogEntry{
			Action: model.OrganizeActionWebhookRecv, Status: model.OrganizeStatusFailed,
			Trigger: model.OrganizeTriggerMP2, Error: err.Error(), Message: "解析 movie-pilot v2 请求体失败",
		})
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	service.WriteOrganizeLog(h.logger, service.OrganizeLogEntry{
		Action: model.OrganizeActionWebhookRecv, Status: model.OrganizeStatusSuccess,
		Trigger: model.OrganizeTriggerMP2,
		Target:  requestBody.Data.Transferinfo.TargetItem.Path,
		Message: fmt.Sprintf("type=%s transferType=%s success=%v", requestBody.Type, requestBody.Data.Transferinfo.TransferType, requestBody.Data.Transferinfo.Success),
	})

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
	Title        string            `json:"Title"`
	Description  string            `json:"Description,omitempty"`
	Date         time.Time         `json:"Date"`
	Event        string            `json:"Event"`
	Severity     string            `json:"Severity"`
	Item         EmbyItem          `json:"Item"`
	Server       EmbyServer        `json:"Server"`
	User         *EmbyWebhookUser  `json:"User,omitempty"`
	PlaybackInfo *EmbyPlaybackInfo `json:"PlaybackInfo,omitempty"`
}

// EmbyWebhookUser 播放事件里的用户信息
type EmbyWebhookUser struct {
	Id   string `json:"Id"`
	Name string `json:"Name"`
}

// EmbyPlaybackInfo 播放事件里的播放进度信息
type EmbyPlaybackInfo struct {
	PositionTicks      int64 `json:"PositionTicks"`
	PlayedToCompletion bool  `json:"PlayedToCompletion"`
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
	RunTimeTicks            int64                  `json:"RunTimeTicks,omitempty"`
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
		service.WriteOrganizeLog(h.logger, service.OrganizeLogEntry{
			Action: model.OrganizeActionWebhookRecv, Status: model.OrganizeStatusFailed,
			Trigger: model.OrganizeTriggerWebhook, Error: err.Error(), Message: "Emby webhook JSON 解析失败",
		})
		c.JSON(http.StatusBadRequest, gin.H{"error": "JSON 解析失败"})
		return
	}

	service.WriteOrganizeLog(h.logger, service.OrganizeLogEntry{
		Action: model.OrganizeActionWebhookRecv, Status: model.OrganizeStatusSuccess,
		Trigger: model.OrganizeTriggerWebhook,
		Source:  webhookData.Item.Path, Target: webhookData.Item.Id,
		Message: fmt.Sprintf("event=%s itemType=%s name=%s", webhookData.Event, webhookData.Item.Type, webhookData.Item.Name),
	})

	// 处理不同类型的事件
	switch webhookData.Event {
	case "library.new":
		h.handleLibraryNew(webhookData)
	case "playback.stop", "item.markplayed":
		h.handlePlaybackEvent(webhookData)
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
	// SortName 拼音首字母回写：和封面任务并行，独立判断
	h.triggerSortName(data.Item.Id, data.Item.Name)
	// 同时处理父 Folder。Emby 的 library.new 只推 Movie/Episode 本体，
	// 不会为它所在目录单独推送；如果不顺手把父项也写一下，
	// 新入库电影的 Folder 壳子（SortName 默认是目录中文名）永远回不到拼音，
	// 会让字母索引一直有中文 prefix。ProcessItem 内部会按 allowedTypes 过滤，
	// 非 Folder/Series/BoxSet/Movie 的父项会被自动跳过，安全。
	if data.Item.ParentId != "" && data.Item.ParentId != data.Item.Id {
		h.triggerSortName(data.Item.ParentId, "(parent of "+data.Item.Name+")")
	}

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

// handlePlaybackEvent 处理 Emby 播放事件，记录被统计用户的观看记录。
//   - playback.stop：需完成度达标(PlayedToCompletion 或进度≥90%)才计；
//   - item.markplayed：标记已看，直接计。
func (h *WebhookHandler) handlePlaybackEvent(data EmbyWebhookRequest) {
	if h.watchSvc == nil {
		return
	}
	if data.User == nil || strings.TrimSpace(data.User.Id) == "" {
		return
	}
	// 仅统计已配置(启用)的 Emby 用户
	if _, ok := h.watchSvc.GetTrackedUser(data.User.Id); !ok {
		return
	}

	item := data.Item
	itemType := strings.TrimSpace(item.Type)
	if itemType != "Movie" && itemType != "Episode" {
		return
	}

	// 采集规则（阈值/事件开关）可配置，读取失败时回退到默认值
	threshold := 0.9
	countStop, countMark := true, true
	if setting, err := h.watchSvc.GetSetting(); err == nil && setting != nil {
		threshold = setting.CompletionThreshold
		countStop = setting.CountPlaybackStop
		countMark = setting.CountMarkPlayed
	}

	switch data.Event {
	case "playback.stop":
		if !countStop {
			return
		}
		completed := false
		if data.PlaybackInfo != nil {
			if data.PlaybackInfo.PlayedToCompletion {
				completed = true
			} else if item.RunTimeTicks > 0 && data.PlaybackInfo.PositionTicks > 0 &&
				float64(data.PlaybackInfo.PositionTicks)/float64(item.RunTimeTicks) >= threshold {
				completed = true
			}
		}
		if !completed {
			return
		}
	case "item.markplayed":
		if !countMark {
			return
		}
	}

	watchedAt := data.Date
	if watchedAt.IsZero() {
		watchedAt = time.Now()
	}

	in := service.WatchEventInput{
		EmbyUserID:     data.User.Id,
		EmbyUserName:   data.User.Name,
		ItemID:         item.Id,
		ItemType:       itemType,
		Title:          item.Name,
		ProductionYear: item.ProductionYear,
		RuntimeMinutes: embyhelper.RuntimeMinutesFromTicks(item.RunTimeTicks),
		WatchedAt:      watchedAt,
	}
	if itemType == "Episode" {
		in.SeriesID = item.SeriesId
		in.SeriesName = item.SeriesName
		in.SeasonNumber = item.ParentIndexNumber
		in.EpisodeNumber = item.IndexNumber
	}
	if err := h.watchSvc.RecordWatch(in); err != nil {
		h.logger.Warnf("[emby-watch] 记录观看失败: %v", err)
	}
}

// triggerSortName 异步触发 SortName 处理。失败仅记日志，不影响主流程。
func (h *WebhookHandler) triggerSortName(itemID, name string) {
	if h.sortNameSvc == nil || itemID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// webhook 自动触发：不强制覆盖（保护用户/工具已锁定的字段）
		res := h.sortNameSvc.ProcessItem(ctx, itemID, false)
		if res.Err != nil {
			h.logger.Warnf("[emby-sortname] webhook 处理失败 itemID=%s name=%s: %v", itemID, name, res.Err)
		}
	}()
}
