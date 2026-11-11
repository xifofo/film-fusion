package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"film-fusion/app/utils/embyhelper"
)

// embyInsightsTTL 是媒体库洞察的缓存时长；画质与类型计数需要多次 Emby 计数请求，
// 页面每次打开都实时拉取会给大型媒体库带来不必要的压力。
const embyInsightsTTL = 10 * time.Minute

// embyInsightsConcurrency 限制回退逐类型计数时并发打到 Emby 的请求数。
const embyInsightsConcurrency = 6

// LibraryQuality 是电影与剧集单集的画质计数。
// HD 包含 4K；UHDAvailable=false 表示 Emby 未正确响应 Is4K 过滤，前端不展示 4K 数字。
type LibraryQuality struct {
	Total        int  `json:"total"`
	HD           int  `json:"hd"`
	UHD          int  `json:"uhd"`
	UHDAvailable bool `json:"uhd_available"`
}

// GenreStat 是一个类型下的电影与剧集数量。
type GenreStat struct {
	Name        string `json:"name"`
	MovieCount  int    `json:"movie_count"`
	SeriesCount int    `json:"series_count"`
}

// RecentItem 是最近入库的电影或剧集，图片通过 /emby-stats/image 代理读取。
type RecentItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	DateCreated string `json:"date_created"`
	ImageTag    string `json:"image_tag"`
}

// EmbyInsights 是媒体库统计页的补充快照：集数、近 7 天新增、画质、类型与最近入库。
// 与 EmbyStats 分开读取，补充数据失败或较慢时不影响媒体库列表。
type EmbyInsights struct {
	GeneratedAt     time.Time      `json:"generated_at"`
	TotalEpisodes   int            `json:"total_episodes"`
	AddedMovies7d   int            `json:"added_movies_7d"`
	AddedEpisodes7d int            `json:"added_episodes_7d"`
	LibraryAdded7d  map[string]int `json:"library_added_7d"`
	Quality         LibraryQuality `json:"quality"`
	Genres          []GenreStat    `json:"genres"`
	Recent          []RecentItem   `json:"recent"`
	PartialErrors   []string       `json:"partial_errors,omitempty"`
}

// embyInsightsCache 保存最近一次成功快照；仅在进程内存中，重启后重新拉取。
type embyInsightsCache struct {
	mu   sync.Mutex
	data *EmbyInsights
}

var insightsCache embyInsightsCache

// Insights 返回媒体库补充统计；refresh=true 时忽略缓存重新拉取。
// 单项失败记录在 PartialErrors 中，其他项照常返回；媒体库列表读取失败才整体失败。
func (s *EmbyStatsService) Insights(ctx context.Context, refresh bool) (*EmbyInsights, error) {
	insightsCache.mu.Lock()
	cached := insightsCache.data
	insightsCache.mu.Unlock()
	if !refresh && cached != nil && time.Since(cached.GeneratedAt) < embyInsightsTTL {
		return cached, nil
	}

	emby := s.emby.WithContext(ctx)
	libs, err := emby.ListLibraries()
	if err != nil {
		return nil, fmt.Errorf("获取 Emby 媒体库列表失败: %w", err)
	}

	out := &EmbyInsights{GeneratedAt: time.Now(), LibraryAdded7d: map[string]int{}}
	since := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var mu sync.Mutex
	var wg sync.WaitGroup
	fail := func(label string, err error) {
		mu.Lock()
		defer mu.Unlock()
		s.log.Warnf("[emby-stats] %s失败: %v", label, err)
		out.PartialErrors = append(out.PartialErrors, fmt.Sprintf("%s失败: %v", label, err))
	}
	count := func(label, libraryID, types string, filters map[string]string, target func(int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := emby.CountItemsFiltered(libraryID, types, filters)
			if err != nil {
				fail(label, err)
				return
			}
			mu.Lock()
			target(n)
			mu.Unlock()
		}()
	}

	// 全局计数：集数、近 7 天新增、画质。画质统计电影与单集，与播放实际文件一致。
	count("统计集数", "", "Episode", nil, func(n int) { out.TotalEpisodes = n })
	count("统计新增电影", "", "Movie", map[string]string{"MinDateCreated": since}, func(n int) { out.AddedMovies7d = n })
	count("统计新增单集", "", "Episode", map[string]string{"MinDateCreated": since}, func(n int) { out.AddedEpisodes7d = n })
	count("统计视频总数", "", "Movie,Episode", nil, func(n int) { out.Quality.Total = n })
	count("统计高清数量", "", "Movie,Episode", map[string]string{"IsHD": "true"}, func(n int) { out.Quality.HD = n })
	count("统计 4K 数量", "", "Movie,Episode", map[string]string{"Is4K": "true"}, func(n int) { out.Quality.UHD = n })

	// 各库近 7 天新增：电影库按电影、剧集库按单集，混合库两者都算。
	for _, lib := range libs {
		types := embyInsightsAddedTypes(lib.CollectionType)
		if types == "" {
			continue
		}
		id := lib.ID
		count("统计 "+lib.Name+" 新增", id, types, map[string]string{"MinDateCreated": since}, func(n int) {
			out.LibraryAdded7d[id] = n
		})
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		genres, err := s.collectGenres(emby)
		if err != nil {
			fail("统计类型", err)
			return
		}
		mu.Lock()
		out.Genres = genres
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		items, err := emby.ListRecentItems(12)
		if err != nil {
			fail("读取最近入库", err)
			return
		}
		recent := make([]RecentItem, 0, len(items))
		for _, item := range items {
			recent = append(recent, RecentItem{
				ID: item.ID, Name: item.Name, Type: item.Type,
				DateCreated: item.DateCreated, ImageTag: item.ImageTags["Primary"],
			})
		}
		mu.Lock()
		out.Recent = recent
		mu.Unlock()
	}()
	wg.Wait()

	// 不支持 Is4K 的 Emby 会忽略该参数并返回未过滤总数；4K 不应超过高清数量。
	out.Quality.UHDAvailable = out.Quality.UHD <= out.Quality.HD && out.Quality.HD <= out.Quality.Total
	if !out.Quality.UHDAvailable {
		out.Quality.UHD = 0
	}

	// 有部分失败的快照不写入缓存，下次打开重新尝试。
	if len(out.PartialErrors) == 0 {
		insightsCache.mu.Lock()
		insightsCache.data = out
		insightsCache.mu.Unlock()
	}
	return out, nil
}

// collectGenres 优先使用 /Genres 的 ItemCounts；若 Emby 未填充计数，
// 则对每个类型并发计数（受 embyInsightsConcurrency 限制），最后按内容总数降序取前 12。
func (s *EmbyStatsService) collectGenres(emby *embyhelper.EmbyClient) ([]GenreStat, error) {
	genres, err := emby.ListGenres("Movie,Series")
	if err != nil {
		return nil, err
	}

	stats := make([]GenreStat, len(genres))
	hasCounts := false
	for i, genre := range genres {
		stats[i] = GenreStat{Name: genre.Name, MovieCount: genre.MovieCount, SeriesCount: genre.SeriesCount}
		if genre.MovieCount > 0 || genre.SeriesCount > 0 {
			hasCounts = true
		}
	}

	if !hasCounts && len(stats) > 0 {
		limit := min(len(stats), 60)
		stats = stats[:limit]
		sem := make(chan struct{}, embyInsightsConcurrency)
		var wg sync.WaitGroup
		for i := range stats {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				filter := map[string]string{"Genres": stats[i].Name}
				stats[i].MovieCount, _ = emby.CountItemsFiltered("", "Movie", filter)
				stats[i].SeriesCount, _ = emby.CountItemsFiltered("", "Series", filter)
			}(i)
		}
		wg.Wait()
	}

	sort.SliceStable(stats, func(i, j int) bool {
		return stats[i].MovieCount+stats[i].SeriesCount > stats[j].MovieCount+stats[j].SeriesCount
	})
	filtered := make([]GenreStat, 0, 12)
	for _, genre := range stats {
		if genre.MovieCount+genre.SeriesCount > 0 && len(filtered) < 12 {
			filtered = append(filtered, genre)
		}
	}
	return filtered, nil
}

// embyInsightsAddedTypes 返回媒体库新增统计使用的条目类型；非影视库不统计。
func embyInsightsAddedTypes(collectionType string) string {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "movies":
		return "Movie"
	case "tvshows":
		return "Episode"
	case "mixed", "":
		return "Movie,Episode"
	default:
		return ""
	}
}
