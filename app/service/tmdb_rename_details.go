package service

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type TMDBTVEpisodeRenameDetails struct {
	SeasonYear   string
	EpisodeTitle string
	EpisodeDate  string
}

type tmdbTVSeasonRenameDetails struct {
	SeasonYear string
	Episodes   map[int]TMDBTVEpisodeRenameDetails
}

type tmdbRenameDetailsCacheEntry struct {
	details   tmdbTVSeasonRenameDetails
	expiresAt time.Time
}

// GetTVEpisodeRenameDetails supplies the three TV-only rename variables that
// are not part of FilmFusion's compact recognition result. The whole season is
// cached, so organizing several episodes performs one TMDB request.
func (s *TMDBService) GetTVEpisodeRenameDetails(
	ctx context.Context,
	tmdbID string,
	seasonNumber int,
	episodeNumber int,
) (TMDBTVEpisodeRenameDetails, error) {
	tmdbID = strings.TrimSpace(tmdbID)
	if tmdbID == "" {
		return TMDBTVEpisodeRenameDetails{}, errors.New("TMDB ID 不能为空")
	}
	if seasonNumber < 0 {
		return TMDBTVEpisodeRenameDetails{}, errors.New("TMDB 季号不能小于 0")
	}
	if s == nil || !s.mediaRecognitionConfigured() {
		return TMDBTVEpisodeRenameDetails{}, errors.New("TMDB 未启用或未配置")
	}

	cacheKey := tmdbID + ":rename-season:" + strconv.Itoa(seasonNumber)
	details, ok := s.getCachedTVSeasonRenameDetails(cacheKey)
	if !ok {
		var err error
		details, err = s.fetchTVSeasonRenameDetails(ctx, tmdbID, seasonNumber)
		if err != nil {
			return TMDBTVEpisodeRenameDetails{}, err
		}
		s.setCachedTVSeasonRenameDetails(cacheKey, details)
	}

	result := TMDBTVEpisodeRenameDetails{SeasonYear: details.SeasonYear}
	if episode, exists := details.Episodes[episodeNumber]; exists {
		result.EpisodeTitle = episode.EpisodeTitle
		result.EpisodeDate = episode.EpisodeDate
	}
	return result, nil
}

func (s *TMDBService) fetchTVSeasonRenameDetails(
	ctx context.Context,
	tmdbID string,
	seasonNumber int,
) (tmdbTVSeasonRenameDetails, error) {
	var response struct {
		AirDate  string `json:"air_date"`
		Episodes []struct {
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			AirDate       string `json:"air_date"`
		} `json:"episodes"`
	}
	err := s.doMediaRecognitionTMDBRequest(
		ctx,
		"/3/tv/"+url.PathEscape(tmdbID)+"/season/"+strconv.Itoa(seasonNumber),
		url.Values{"language": []string{"zh-CN"}},
		&response,
	)
	if err != nil {
		return tmdbTVSeasonRenameDetails{}, err
	}

	details := tmdbTVSeasonRenameDetails{
		SeasonYear: mediaRenameYearFromDate(response.AirDate),
		Episodes:   make(map[int]TMDBTVEpisodeRenameDetails, len(response.Episodes)),
	}
	for _, episode := range response.Episodes {
		if episode.EpisodeNumber < 0 {
			continue
		}
		if details.SeasonYear == "" {
			details.SeasonYear = mediaRenameYearFromDate(episode.AirDate)
		}
		details.Episodes[episode.EpisodeNumber] = TMDBTVEpisodeRenameDetails{
			SeasonYear: details.SeasonYear, EpisodeTitle: strings.TrimSpace(episode.Name),
			EpisodeDate: strings.TrimSpace(episode.AirDate),
		}
	}
	return details, nil
}

func mediaRenameYearFromDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 4 {
		return value[:4]
	}
	return ""
}

func (s *TMDBService) getCachedTVSeasonRenameDetails(key string) (tmdbTVSeasonRenameDetails, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.renameCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			delete(s.renameCache, key)
		}
		return tmdbTVSeasonRenameDetails{}, false
	}
	return entry.details, true
}

func (s *TMDBService) setCachedTVSeasonRenameDetails(key string, details tmdbTVSeasonRenameDetails) {
	cacheMinutes := s.cfg.TMDB.CacheMinutes
	if cacheMinutes <= 0 {
		cacheMinutes = 60
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renameCache[key] = tmdbRenameDetailsCacheEntry{
		details: details, expiresAt: time.Now().Add(time.Duration(cacheMinutes) * time.Minute),
	}
}
