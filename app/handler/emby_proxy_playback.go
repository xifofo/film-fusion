package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"film-fusion/app/service"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/utils/embyhelper"
	"film-fusion/app/utils/pathhelper"

	"github.com/gin-gonic/gin"
	"resty.dev/v3"
)

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
	client.SetTimeout(30 * time.Second)
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
