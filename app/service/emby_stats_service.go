package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	ContentCount   int    `json:"content_count"`
	ImageType      string `json:"image_type,omitempty"`
	ImageTag       string `json:"image_tag,omitempty"`
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
//   - 根据 CollectionType 只统计该库实际承载的内容类型；混合库才请求 Movie + Series
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
		imageType, imageTag := embyLibraryImageMeta(lib)
		stats.Libraries[i] = LibraryStat{
			EmbyLibraryID:  lib.ID,
			EmbyName:       lib.Name,
			CollectionType: lib.CollectionType,
			ContentCount:   lib.ChildCount,
			ImageType:      imageType,
			ImageTag:       imageTag,
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

			targets := embyStatsItemTypes(collectionType)
			typedContentCount := 0
			for _, itemType := range targets {
				count, countErr := s.emby.CountItems(libID, itemType)

				mu.Lock()
				if countErr != nil {
					label := embyStatsItemTypeLabel(itemType)
					s.log.Warnf("[emby-stats] 统计%s失败 lib=%s(%s): %v", label, libName, libID, countErr)
					stats.PartialErrors = append(stats.PartialErrors,
						fmt.Sprintf("库 %s %s统计失败: %v", libName, label, countErr))
				} else if itemType == "Movie" {
					stats.Libraries[idx].MovieCount = count
					typedContentCount += count
				} else if itemType == "Series" {
					stats.Libraries[idx].SeriesCount = count
					typedContentCount += count
				} else if itemType == "BoxSet" {
					// 合集是 Emby 的虚拟视图，媒体库 ChildCount 可能为 0。
					// 只把 BoxSet 实体数写入本库内容数，不重复计入电影 / 剧集总数。
					typedContentCount += count
				}
				mu.Unlock()
			}

			if len(targets) > 0 {
				mu.Lock()
				stats.Libraries[idx].ContentCount = typedContentCount
				mu.Unlock()
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
		ti := stats.Libraries[i].ContentCount
		tj := stats.Libraries[j].ContentCount
		if ti != tj {
			return ti > tj
		}
		return stats.Libraries[i].EmbyName < stats.Libraries[j].EmbyName
	})

	s.log.Infof("[emby-stats] 收集完成 libs=%d movies=%d series=%d partial_errors=%d",
		stats.TotalLibraries, stats.TotalMovies, stats.TotalSeries, len(stats.PartialErrors))
	return stats, nil
}

// LibraryImage 拉取媒体库封面；仅开放页面实际使用的主图和背景图类型。
func (s *EmbyStatsService) LibraryImage(itemID, imageType string, maxWidth int) ([]byte, string, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, "", fmt.Errorf("item_id 不能为空")
	}

	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case "", "primary":
		imageType = "Primary"
	case "backdrop":
		imageType = "Backdrop"
	default:
		return nil, "", fmt.Errorf("不支持的媒体库图片类型")
	}

	if maxWidth <= 0 {
		maxWidth = 720
	}
	if maxWidth > 1600 {
		maxWidth = 1600
	}
	return s.emby.DownloadImage(itemID, imageType, maxWidth)
}

func embyStatsItemTypes(collectionType string) []string {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "movies":
		return []string{"Movie"}
	case "tvshows":
		return []string{"Series"}
	case "mixed", "":
		return []string{"Movie", "Series"}
	case "boxsets":
		return []string{"BoxSet"}
	default:
		// 音乐、家庭视频等库继续使用 Emby 返回的 ChildCount，不伪装成影视库。
		return nil
	}
}

func embyStatsItemTypeLabel(itemType string) string {
	switch itemType {
	case "Series":
		return "剧集"
	case "BoxSet":
		return "合集"
	default:
		return "电影"
	}
}

func embyLibraryImageMeta(lib embyhelper.EmbyLibrary) (string, string) {
	if tag := strings.TrimSpace(lib.ImageTags["Primary"]); tag != "" {
		return "Primary", tag
	}
	if len(lib.BackdropImageTags) > 0 {
		if tag := strings.TrimSpace(lib.BackdropImageTags[0]); tag != "" {
			return "Backdrop", tag
		}
	}
	return "", ""
}
