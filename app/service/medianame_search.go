// medianame 负责媒体搜索和分类，所有调用方复用同一份配置与缓存。
package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/xifofo/medianame"
	"github.com/xifofo/medianame/category"
)

// SearchMedia 返回 TMDB 候选；搜索本身不自动采用任何候选。
func (s *MediaRecognitionService) SearchMedia(ctx context.Context, keyword string, count int) ([]MoviePilotSearchResult, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, errors.New("搜索关键词不能为空")
	}
	if count <= 0 || count > 20 {
		count = 8
	}
	cfg, err := s.LoadMedianameConfig()
	if err != nil {
		return nil, err
	}
	policy, err := category.Parse([]byte(cfg.CategoryYAML))
	if err != nil {
		return nil, err
	}
	api := s.medianameClient
	if api == nil {
		api = s.tmdb.medianameAPI()
	}
	if api.err != nil {
		return nil, api.err
	}
	found, err := api.client.Search(ctx, medianame.SearchQuery{Title: keyword})
	if err != nil {
		return nil, err
	}
	results := make([]MoviePilotSearchResult, 0, count)
	for _, summary := range found.Results {
		if len(results) == count {
			break
		}
		media, err := api.client.Details(ctx, summary.Type, summary.TMDBID)
		if err != nil {
			return nil, err
		}
		name, err := media.Classify(policy)
		if err != nil {
			return nil, err
		}
		year := ""
		if media.Year > 0 {
			year = strconv.Itoa(media.Year)
		}
		titleYear := media.Title
		if year != "" {
			titleYear += " (" + year + ")"
		}
		genres := make([]string, 0, len(media.Genres))
		for _, genre := range media.Genres {
			genres = append(genres, genre.Name)
		}
		results = append(results, MoviePilotSearchResult{MediaType: string(media.Type), Title: media.Title, OriginalTitle: media.OriginalTitle, Year: year, TitleYear: titleYear, TmdbID: media.TMDBID, Category: name, PosterPath: media.PosterPath, BackdropPath: media.BackdropPath, Rating: media.VoteAverage, Genres: genres, Overview: media.Overview})
	}
	return results, nil
}
