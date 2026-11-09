package service

import (
	"context"
	"sort"
	"strings"

	"film-fusion/app/utils/embyhelper"
)

// PlaybackStreamView 仅含媒体展示信息；Direct=nil 表示 Emby 未报告该流是否转码。
type PlaybackStreamView struct {
	Title    string `json:"title"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Channels int    `json:"channels"`
	Direct   *bool  `json:"direct"`
}

// NowPlayingView 是真实 Emby 会话，和代理负载均衡中的活跃请求计数分别展示。
type NowPlayingView struct {
	ID              string              `json:"id"`
	UserName        string              `json:"user_name"`
	DeviceName      string              `json:"device_name"`
	Client          string              `json:"client"`
	ClientVersion   string              `json:"client_version"`
	RemoteEndpoint  string              `json:"remote_endpoint"`
	Protocol        string              `json:"protocol"`
	ItemID          string              `json:"item_id"`
	PosterID        string              `json:"poster_id"`
	Title           string              `json:"title"`
	SeriesName      string              `json:"series_name"`
	SeasonNumber    *int                `json:"season_number"`
	EpisodeNumber   *int                `json:"episode_number"`
	PositionSeconds *int64              `json:"position_seconds"`
	DurationSeconds *int64              `json:"duration_seconds"`
	Paused          *bool               `json:"paused"`
	PlayMethod      string              `json:"play_method"`
	Container       string              `json:"container"`
	Bitrate         int64               `json:"bitrate"`
	Video           *PlaybackStreamView `json:"video"`
	Audio           *PlaybackStreamView `json:"audio"`
}

// NowPlaying 过滤没有 NowPlayingItem 的闲置连接，保留暂停会话及客户端实际报告的状态。
func (s *EmbyWatchService) NowPlaying(ctx context.Context) ([]NowPlayingView, error) {
	sessions, err := s.emby.ReadPlaybackSessions(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]NowPlayingView, 0)
	for _, session := range sessions {
		item := session.NowPlayingItem
		if item == nil || item.ID == "" || session.ID == "" {
			continue
		}
		row := NowPlayingView{
			ID: session.ID, UserName: session.UserName, DeviceName: session.DeviceName,
			Client: session.Client, ClientVersion: session.ApplicationVersion,
			RemoteEndpoint: session.RemoteEndPoint, Protocol: session.Protocol,
			ItemID: item.ID, PosterID: firstNonEmpty(item.SeriesID, firstNonEmpty(item.PrimaryImageItemID, item.ID)),
			Title: item.Name, SeriesName: item.SeriesName, SeasonNumber: item.ParentIndexNumber, EpisodeNumber: item.IndexNumber,
			Paused: session.PlayState.IsPaused, PlayMethod: session.PlayState.PlayMethod, Container: item.Container,
		}
		duration, streams := item.RunTimeTicks, item.MediaStreams
		source := session.PlayState.MediaSource
		if source == nil {
			for i := range item.MediaSources {
				candidate := &item.MediaSources[i]
				if candidate.ID == session.PlayState.MediaSourceID || (session.PlayState.MediaSourceID == "" && len(item.MediaSources) == 1) {
					source = candidate
					break
				}
			}
		}
		if source != nil {
			row.Container, row.Bitrate = firstNonEmpty(source.Container, row.Container), max(0, source.Bitrate)
			if source.RunTimeTicks > 0 {
				duration = source.RunTimeTicks
			}
			if len(source.MediaStreams) > 0 {
				streams = source.MediaStreams
			}
		}
		if duration > 0 {
			seconds := duration / 10000000
			row.DurationSeconds = &seconds
		}
		if ticks := session.PlayState.PositionTicks; ticks != nil {
			seconds := max(0, *ticks/10000000)
			row.PositionSeconds = &seconds
		}
		row.Video = playbackStreamView(streams, "Video", nil)
		row.Audio = playbackStreamView(streams, "Audio", session.PlayState.AudioStreamIndex)
		if row.PlayMethod == "DirectPlay" {
			direct := true
			if row.Video != nil {
				row.Video.Direct = &direct
			}
			if row.Audio != nil {
				row.Audio.Direct = &direct
			}
		} else if session.TranscodingInfo != nil {
			if row.Video != nil {
				row.Video.Direct = session.TranscodingInfo.IsVideoDirect
			}
			if row.Audio != nil {
				row.Audio.Direct = session.TranscodingInfo.IsAudioDirect
			}
		}
		rows = append(rows, row)
	}
	// 稳定顺序避免轮询时卡片跳动；不以 IP 合并不同设备的会话。
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

// 有选定音轨时必须精确匹配，不能把其他语言音轨冒充当前音轨。
func playbackStreamView(streams []embyhelper.PlaybackStream, kind string, selected *int) *PlaybackStreamView {
	var chosen *embyhelper.PlaybackStream
	for i := range streams {
		stream := &streams[i]
		if stream.Type != kind || (selected != nil && stream.Index != *selected) {
			continue
		}
		if chosen == nil || stream.IsDefault {
			chosen = stream
		}
		if selected != nil {
			break
		}
	}
	if chosen == nil {
		return nil
	}
	return &PlaybackStreamView{Title: chosen.DisplayTitle, Codec: chosen.Codec, Language: chosen.Language, Width: max(0, chosen.Width), Height: max(0, chosen.Height), Channels: max(0, chosen.Channels)}
}

// PlaybackHistoryView 是 Emby 开始/停止活动，不受本地已看完成度、统计用户及 webhook 配置限制。
type PlaybackHistoryView struct {
	Items       []PlaybackHistoryItem `json:"items"`
	Scanned     int                   `json:"scanned"`
	ScanLimited bool                  `json:"scan_limited"`
}

type PlaybackHistoryItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Event string `json:"event"`
	Date  string `json:"date"`
}

// PlaybackHistory 最多扫描最近 300 条活动，返回其中最近 20 条播放事件；不伪造全量历史总数。
func (s *EmbyWatchService) PlaybackHistory(ctx context.Context) (PlaybackHistoryView, error) {
	view := PlaybackHistoryView{Items: make([]PlaybackHistoryItem, 0)}
	seen := make(map[string]bool)
	start := 0
	for request := 0; request < 3; request++ {
		page, err := s.emby.ReadPlaybackActivities(ctx, start, 100)
		if err != nil {
			return view, err
		}
		view.Scanned += len(page.Items)
		for _, activity := range page.Items {
			event := playbackActivityEvent(activity.Type)
			id := activity.ID.String()
			if event == "" || id == "" || seen[id] {
				continue
			}
			seen[id] = true
			view.Items = append(view.Items, PlaybackHistoryItem{ID: id, Name: activity.Name, Event: event, Date: activity.Date})
			if len(view.Items) == 20 {
				return view, nil
			}
		}
		start += len(page.Items)
		if len(page.Items) == 0 || start >= page.TotalRecordCount {
			return view, nil
		}
		view.ScanLimited = request == 2
	}
	return view, nil
}

// 兼容当前 Emby 的 playback.* 与旧版活动类型；不依赖可翻译的 Name 文案做过滤。
func playbackActivityEvent(kind string) string {
	switch strings.ToLower(kind) {
	case "playback.start", "videoplayback", "audioplayback":
		return "start"
	case "playback.stop", "videoplaybackstopped", "audioplaybackstopped":
		return "stop"
	default:
		return ""
	}
}
