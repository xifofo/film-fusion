package embyhelper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// PlaybackStream 仅解码展示所需的流信息，不读取文件路径或播放地址。
type PlaybackStream struct {
	Index        int    `json:"Index"`
	Type         string `json:"Type"`
	Codec        string `json:"Codec"`
	DisplayTitle string `json:"DisplayTitle"`
	Language     string `json:"Language"`
	Width        int    `json:"Width"`
	Height       int    `json:"Height"`
	Channels     int    `json:"Channels"`
	IsDefault    bool   `json:"IsDefault"`
}

// PlaybackMediaSource 是当前选定媒体版本，不包含 URL、HTTP 凭据或路径。
type PlaybackMediaSource struct {
	ID           string           `json:"Id"`
	Container    string           `json:"Container"`
	Bitrate      int64            `json:"Bitrate"`
	RunTimeTicks int64            `json:"RunTimeTicks"`
	MediaStreams []PlaybackStream `json:"MediaStreams"`
}

// PlaybackItem 是会话附带的媒体信息；不为工作台额外逐条请求媒体详情。
type PlaybackItem struct {
	ID                 string                `json:"Id"`
	Name               string                `json:"Name"`
	Type               string                `json:"Type"`
	SeriesID           string                `json:"SeriesId"`
	SeriesName         string                `json:"SeriesName"`
	ParentIndexNumber  *int                  `json:"ParentIndexNumber"`
	IndexNumber        *int                  `json:"IndexNumber"`
	PrimaryImageItemID string                `json:"PrimaryImageItemId"`
	Container          string                `json:"Container"`
	RunTimeTicks       int64                 `json:"RunTimeTicks"`
	MediaStreams       []PlaybackStream      `json:"MediaStreams"`
	MediaSources       []PlaybackMediaSource `json:"MediaSources"`
}

// PlaybackSession 是管理员会话快照；缺失进度与暂停状态不解释为零或正在播放。
type PlaybackSession struct {
	ID                 string        `json:"Id"`
	UserID             string        `json:"UserId"`
	UserName           string        `json:"UserName"`
	DeviceName         string        `json:"DeviceName"`
	Client             string        `json:"Client"`
	ApplicationVersion string        `json:"ApplicationVersion"`
	RemoteEndPoint     string        `json:"RemoteEndPoint"`
	Protocol           string        `json:"Protocol"`
	NowPlayingItem     *PlaybackItem `json:"NowPlayingItem"`
	PlayState          struct {
		PositionTicks    *int64               `json:"PositionTicks"`
		IsPaused         *bool                `json:"IsPaused"`
		PlayMethod       string               `json:"PlayMethod"`
		AudioStreamIndex *int                 `json:"AudioStreamIndex"`
		MediaSourceID    string               `json:"MediaSourceId"`
		MediaSource      *PlaybackMediaSource `json:"MediaSource"`
	} `json:"PlayState"`
	TranscodingInfo *struct {
		IsVideoDirect *bool `json:"IsVideoDirect"`
		IsAudioDirect *bool `json:"IsAudioDirect"`
	} `json:"TranscodingInfo"`
}

// PlaybackActivity 保留 Emby 原始事件描述；ID 使用字符串输出，避免 JavaScript 整数精度丢失。
type PlaybackActivity struct {
	ID     json.Number `json:"Id"`
	Name   string      `json:"Name"`
	Type   string      `json:"Type"`
	ItemID string      `json:"ItemId"`
	Date   string      `json:"Date"`
	UserID string      `json:"UserId"`
}

// PlaybackActivityPage 是 Emby 活动日志的分页结果，包含所有类型的活动。
type PlaybackActivityPage struct {
	Items            []PlaybackActivity `json:"Items"`
	TotalRecordCount int                `json:"TotalRecordCount"`
}

// ReadPlaybackSessions 只读会话，沿用配置中的管理员凭据并响应调用方取消。
func (e *EmbyClient) ReadPlaybackSessions(ctx context.Context) ([]PlaybackSession, error) {
	var sessions []PlaybackSession
	err := e.readPlaybackDashboard(ctx, "/Sessions", nil, &sessions)
	if err == nil && sessions == nil {
		err = fmt.Errorf("Emby 播放会话响应格式错误")
	}
	return sessions, err
}

// ReadPlaybackActivities 只读活动日志，单次页大小限制为 100，避免无界下载。
func (e *EmbyClient) ReadPlaybackActivities(ctx context.Context, start, limit int) (PlaybackActivityPage, error) {
	var page PlaybackActivityPage
	err := e.readPlaybackDashboard(ctx, "/System/ActivityLog/Entries", map[string]string{
		"StartIndex": strconv.Itoa(max(0, start)),
		"Limit":      strconv.Itoa(max(1, min(100, limit))),
	}, &page)
	if err == nil && (page.Items == nil || page.TotalRecordCount < 0) {
		err = fmt.Errorf("Emby 播放历史响应格式错误")
	}
	return page, err
}

// 不将 Resty 错误或上游响应正文返回到浏览器，其中可能包含带 API Key 的请求 URL。
func (e *EmbyClient) readPlaybackDashboard(ctx context.Context, path string, query map[string]string, result any) error {
	if e == nil || e.config == nil || e.config.Emby.URL == "" || e.config.Emby.APIKey == "" {
		return fmt.Errorf("请先配置 Emby 地址和 API Key")
	}
	response, err := e.client.R().SetContext(ctx).SetQueryParams(query).Get(path)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("读取 Emby 播放状态失败，请检查连接和响应格式")
	}
	if response.StatusCode() != http.StatusOK {
		return fmt.Errorf("读取 Emby 播放状态失败，HTTP %d", response.StatusCode())
	}
	if json.Unmarshal([]byte(response.String()), result) != nil {
		return fmt.Errorf("Emby 播放状态响应格式错误")
	}
	return nil
}
