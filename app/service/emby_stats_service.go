package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/utils/embyhelper"
)

// LibraryStat 单个媒体库的电影 / 电视剧统计
type LibraryStat struct {
	EmbyLibraryID  string `json:"emby_library_id"`
	EmbyName       string `json:"emby_name"`
	CollectionType string `json:"collection_type"` // movies / tvshows / mixed / homevideos / boxsets / music ...
	MovieCount     int    `json:"movie_count"`
	SeriesCount    int    `json:"series_count"`
}

// EmbyStats 整体统计快照
type EmbyStats struct {
	GeneratedAt    time.Time     `json:"generated_at"`
	TotalLibraries int           `json:"total_libraries"`
	TotalMovies    int           `json:"total_movies"`
	TotalSeries    int           `json:"total_series"`
	Libraries      []LibraryStat `json:"libraries"`
	// PartialErrors 部分库统计失败时的错误信息，前端可降级展示已成功的库
	PartialErrors []string `json:"partial_errors,omitempty"`
}

// EmbyStatsService 负责按媒体库聚合 Movie / Series 数量
//
// 设计原则：
//   - 仅遍历 Emby 顶层 CollectionFolder（用户视角的"媒体库文件夹"）
//   - 每个库 2 次轻量 HTTP（Movie / Series 各一次，Limit=1 + TotalRecordCount）
//   - 各库并发拉取，汇总后排序：内容多的库置顶
type EmbyStatsService struct {
	cfg  *config.Config
	log  *logger.Logger
	emby *embyhelper.EmbyClient
}

// NewEmbyStatsService 构造
func NewEmbyStatsService(cfg *config.Config, log *logger.Logger, emby *embyhelper.EmbyClient) *EmbyStatsService {
	return &EmbyStatsService{cfg: cfg, log: log, emby: emby}
}

// Collect 实时拉取统计快照。partial 错误不阻断；ListLibraries 失败才整体失败。
func (s *EmbyStatsService) Collect(ctx context.Context) (*EmbyStats, error) {
	libs, err := s.emby.ListLibraries()
	if err != nil {
		return nil, fmt.Errorf("获取 Emby 媒体库列表失败: %w", err)
	}

	stats := &EmbyStats{
		GeneratedAt:    time.Now(),
		TotalLibraries: len(libs),
		Libraries:      make([]LibraryStat, len(libs)),
	}
	for i, lib := range libs {
		stats.Libraries[i] = LibraryStat{
			EmbyLibraryID:  lib.ID,
			EmbyName:       lib.Name,
			CollectionType: lib.CollectionType,
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, lib := range libs {
		wg.Add(1)
		go func(idx int, libID, libName, collectionType string) {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				mu.Lock()
				stats.PartialErrors = append(stats.PartialErrors, fmt.Sprintf("库 %s 取消: %v", libName, err))
				mu.Unlock()
				return
			}

			// 仅根据库类型拉对应实体，避免无意义请求：
			//   - tvshows / mixed / 其它 / homevideos 都可能存 Series（mixed 库可能两者都有）
			//   - movies / mixed / 其它 都可能存 Movie
			// 为了通用性，统一两种都拉一次：Emby 不存在的类型只是 TotalRecordCount=0，开销很小
			movieCount, mErr := s.emby.CountItems(libID, "Movie")
			seriesCount, sErr := s.emby.CountItems(libID, "Series")

			mu.Lock()
			defer mu.Unlock()
			if mErr != nil {
				s.log.Warnf("[emby-stats] 统计电影失败 lib=%s(%s): %v", libName, libID, mErr)
				stats.PartialErrors = append(stats.PartialErrors,
					fmt.Sprintf("库 %s 电影统计失败: %v", libName, mErr))
			} else {
				stats.Libraries[idx].MovieCount = movieCount
			}
			if sErr != nil {
				s.log.Warnf("[emby-stats] 统计剧集失败 lib=%s(%s): %v", libName, libID, sErr)
				stats.PartialErrors = append(stats.PartialErrors,
					fmt.Sprintf("库 %s 剧集统计失败: %v", libName, sErr))
			} else {
				stats.Libraries[idx].SeriesCount = seriesCount
			}
		}(i, lib.ID, lib.Name, lib.CollectionType)
	}
	wg.Wait()

	for _, ls := range stats.Libraries {
		stats.TotalMovies += ls.MovieCount
		stats.TotalSeries += ls.SeriesCount
	}

	// 按内容总量降序，同量按名称升序稳定排序
	sort.SliceStable(stats.Libraries, func(i, j int) bool {
		ti := stats.Libraries[i].MovieCount + stats.Libraries[i].SeriesCount
		tj := stats.Libraries[j].MovieCount + stats.Libraries[j].SeriesCount
		if ti != tj {
			return ti > tj
		}
		return stats.Libraries[i].EmbyName < stats.Libraries[j].EmbyName
	})

	s.log.Infof("[emby-stats] 收集完成 libs=%d movies=%d series=%d partial_errors=%d",
		stats.TotalLibraries, stats.TotalMovies, stats.TotalSeries, len(stats.PartialErrors))
	return stats, nil
}
