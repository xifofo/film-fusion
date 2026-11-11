package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

// WatchOverviewParams 是跨用户总览的查询范围。
// StartDate/EndDate 为闭区间 YYYY-MM-DD，可为空表示不限；EmbyUserID 为空表示全部被统计用户。
type WatchOverviewParams struct {
	EmbyUserID string
	StartDate  string
	EndDate    string
}

// WatchPeriodTotals 是某一时间段的观看次数与时长，用于和上一周期对比。
type WatchPeriodTotals struct {
	Total        int64 `json:"total"`
	TotalMinutes int64 `json:"total_minutes"`
}

// WatchDailyCount 是某天的电影与单集观看次数；只返回有记录的日期，前端补零。
type WatchDailyCount struct {
	Date         string `json:"date"`
	MovieCount   int    `json:"movie_count"`
	EpisodeCount int    `json:"episode_count"`
}

// WatchUserRank 是单个 Emby 用户在范围内的观看汇总。
type WatchUserRank struct {
	EmbyUserID    string `json:"emby_user_id"`
	EmbyUserName  string `json:"emby_user_name"`
	Total         int    `json:"total"`
	TotalMinutes  int    `json:"total_minutes"`
	LastWatchedAt string `json:"last_watched_at"`
}

// WatchTopItem 是热门内容；电影按影片聚合，剧集按剧聚合。
// Viewers 为不同观看用户数，Plays 对电影是观看次数、对剧集是观看集次。
type WatchTopItem struct {
	ID       string `json:"id"`
	PosterID string `json:"poster_id"`
	Title    string `json:"title"`
	ItemType string `json:"item_type"`
	Viewers  int    `json:"viewers"`
	Plays    int    `json:"plays"`
}

// WatchHeatCell 是星期（0=周日）× 小时（0-23，服务器本地时区）的观看次数。
type WatchHeatCell struct {
	Weekday int `json:"weekday"`
	Hour    int `json:"hour"`
	Total   int `json:"total"`
}

// WatchOverview 是观影记录「总览」页签的数据。
// Previous 仅在起止日期都给定时返回，表示紧邻的等长上一周期。
type WatchOverview struct {
	StartDate    string                  `json:"start_date"`
	EndDate      string                  `json:"end_date"`
	Total        int64                   `json:"total"`
	MovieCount   int64                   `json:"movie_count"`
	EpisodeCount int64                   `json:"episode_count"`
	TotalMinutes int64                   `json:"total_minutes"`
	ActiveUsers  int64                   `json:"active_users"`
	TrackedUsers int64                   `json:"tracked_users"`
	ActiveDays   int64                   `json:"active_days"`
	Previous     *WatchPeriodTotals      `json:"previous"`
	Daily        []WatchDailyCount       `json:"daily"`
	Users        []WatchUserRank         `json:"users"`
	TopItems     []WatchTopItem          `json:"top_items"`
	Heatmap      []WatchHeatCell         `json:"heatmap"`
	Recent       []model.EmbyWatchRecord `json:"recent"`
}

const watchOverviewTopLimit = 10
const watchOverviewRecentLimit = 10

// Overview 汇总被统计用户（或指定用户）在日期范围内的观看数据。
// 只统计当前启用的被统计用户，与前端用户选择器一致；取消统计的用户历史记录不计入。
func (s *EmbyWatchService) Overview(p WatchOverviewParams) (*WatchOverview, error) {
	start, end, err := parseOverviewRange(p.StartDate, p.EndDate)
	if err != nil {
		return nil, err
	}

	var trackedIDs []string
	if err := s.db.Model(&model.EmbyWatchUser{}).Where("enabled = ?", true).
		Pluck("emby_user_id", &trackedIDs).Error; err != nil {
		return nil, err
	}

	userID := strings.TrimSpace(p.EmbyUserID)
	if userID != "" && !containsString(trackedIDs, userID) {
		return nil, fmt.Errorf("该 Emby 用户未启用统计")
	}

	out := &WatchOverview{
		StartDate: start, EndDate: end, TrackedUsers: int64(len(trackedIDs)),
		Daily: []WatchDailyCount{}, Users: []WatchUserRank{}, TopItems: []WatchTopItem{},
		Heatmap: []WatchHeatCell{}, Recent: []model.EmbyWatchRecord{},
	}
	if len(trackedIDs) == 0 {
		return out, nil
	}

	scopeFor := func(from, to string) func() *gorm.DB {
		return func() *gorm.DB {
			q := s.db.Model(&model.EmbyWatchRecord{}).Where("emby_user_id IN ?", trackedIDs)
			if userID != "" {
				q = q.Where("emby_user_id = ?", userID)
			}
			if from != "" {
				q = q.Where("watched_date >= ?", from)
			}
			if to != "" {
				q = q.Where("watched_date <= ?", to)
			}
			return q
		}
	}
	scope := scopeFor(start, end)

	// 总量与时长
	scope().Where("item_type = ?", "Movie").Count(&out.MovieCount)
	scope().Where("item_type = ?", "Episode").Count(&out.EpisodeCount)
	scope().Count(&out.Total)
	out.TotalMinutes = sumMinutes(scope())
	scope().Distinct("emby_user_id").Count(&out.ActiveUsers)
	scope().Distinct("watched_date").Count(&out.ActiveDays)

	// 上一等长周期，仅在范围有界时计算。
	if start != "" && end != "" {
		prevStart, prevEnd := previousRange(start, end)
		prev := scopeFor(prevStart, prevEnd)
		out.Previous = &WatchPeriodTotals{TotalMinutes: sumMinutes(prev())}
		prev().Count(&out.Previous.Total)
	}

	// 每日电影/单集计数
	if err := scope().
		Select("watched_date as date, " +
			"SUM(CASE WHEN item_type = 'Movie' THEN 1 ELSE 0 END) as movie_count, " +
			"SUM(CASE WHEN item_type = 'Episode' THEN 1 ELSE 0 END) as episode_count").
		Group("watched_date").Order("watched_date asc").
		Scan(&out.Daily).Error; err != nil {
		return nil, err
	}

	// 用户排行：按时长降序，同时长按次数
	if err := scope().
		Select("emby_user_id, MAX(emby_user_name) as emby_user_name, COUNT(*) as total, " +
			"COALESCE(SUM(runtime_minutes),0) as total_minutes, MAX(watched_at) as last_watched_at").
		Group("emby_user_id").
		Order("total_minutes desc, total desc").
		Scan(&out.Users).Error; err != nil {
		return nil, err
	}

	// SQLite 的 MAX(watched_at) 返回存储文本，统一转换为 RFC3339 供前端解析。
	for i := range out.Users {
		out.Users[i].LastWatchedAt = normalizeSQLiteTime(out.Users[i].LastWatchedAt)
	}

	// 热门内容：电影与剧集分别聚合后合并，按观看人数、次数排序
	var movies, series []WatchTopItem
	if err := scope().Where("item_type = ?", "Movie").
		Select("item_id as id, item_id as poster_id, MAX(title) as title, 'Movie' as item_type, " +
			"COUNT(DISTINCT emby_user_id) as viewers, COUNT(*) as plays").
		Group("item_id").Order("viewers desc, plays desc").Limit(watchOverviewTopLimit).
		Scan(&movies).Error; err != nil {
		return nil, err
	}
	if err := scope().Where("item_type = ? AND series_id != ?", "Episode", "").
		Select("series_id as id, series_id as poster_id, MAX(series_name) as title, 'Series' as item_type, " +
			"COUNT(DISTINCT emby_user_id) as viewers, COUNT(*) as plays").
		Group("series_id").Order("viewers desc, plays desc").Limit(watchOverviewTopLimit).
		Scan(&series).Error; err != nil {
		return nil, err
	}
	top := append(movies, series...)
	sort.SliceStable(top, func(i, j int) bool {
		if top[i].Viewers != top[j].Viewers {
			return top[i].Viewers > top[j].Viewers
		}
		return top[i].Plays > top[j].Plays
	})
	if len(top) > watchOverviewTopLimit {
		top = top[:watchOverviewTopLimit]
	}
	out.TopItems = top

	// 热力图：按服务器本地时区分桶，与年度报告的时段分布一致。
	var times []struct{ WatchedAt time.Time }
	scope().Select("watched_at").Scan(&times)
	var buckets [7][24]int
	for _, row := range times {
		local := row.WatchedAt.Local()
		buckets[int(local.Weekday())][local.Hour()]++
	}
	for weekday := 0; weekday < 7; weekday++ {
		for hour := 0; hour < 24; hour++ {
			if buckets[weekday][hour] > 0 {
				out.Heatmap = append(out.Heatmap, WatchHeatCell{Weekday: weekday, Hour: hour, Total: buckets[weekday][hour]})
			}
		}
	}

	// 最近观看
	if err := scope().Order("watched_at desc").Limit(watchOverviewRecentLimit).
		Find(&out.Recent).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// parseOverviewRange 校验 YYYY-MM-DD 并保证起始不晚于结束。
func parseOverviewRange(start, end string) (string, string, error) {
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	for _, value := range []string{start, end} {
		if value == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return "", "", fmt.Errorf("日期格式应为 YYYY-MM-DD")
		}
	}
	if start != "" && end != "" && start > end {
		return "", "", fmt.Errorf("开始日期不能晚于结束日期")
	}
	return start, end, nil
}

// previousRange 返回紧邻 [start, end] 之前的等长闭区间。
func previousRange(start, end string) (string, string) {
	from, _ := time.Parse("2006-01-02", start)
	to, _ := time.Parse("2006-01-02", end)
	days := int(to.Sub(from).Hours()/24) + 1
	prevEnd := from.AddDate(0, 0, -1)
	prevStart := prevEnd.AddDate(0, 0, -(days - 1))
	return prevStart.Format("2006-01-02"), prevEnd.Format("2006-01-02")
}

// normalizeSQLiteTime 把 SQLite 中 time.Time 的文本存储格式转为 RFC3339；无法识别时原样返回。
func normalizeSQLiteTime(value string) string {
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.Format(time.RFC3339)
		}
	}
	return value
}

func sumMinutes(q *gorm.DB) int64 {
	var row struct{ Total int64 }
	q.Select("COALESCE(SUM(runtime_minutes),0) as total").Scan(&row)
	return row.Total
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
