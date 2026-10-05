package service

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"film-fusion/app/config"
)

// NormalizeMediaRecognitionSource returns the canonical recognition mode.
func NormalizeMediaRecognitionSource(value string) (string, error) {
	return config.NormalizeMediaRecognitionSource(value)
}

// DefaultMediaRecognitionSource 返回系统设置中的默认识别方式。
func (s *MoviePilotService) DefaultMediaRecognitionSource() string {
	value := ""
	if s != nil && s.cfg != nil {
		value = s.cfg.MediaRecognition.Source
	}
	normalized, err := NormalizeMediaRecognitionSource(value)
	if err != nil {
		return MediaRecognitionSourceShadow
	}
	return normalized
}

// ResolveMediaRecognitionSource 将空请求解析为系统默认值，显式值则保持单次覆盖语义。
func (s *MoviePilotService) ResolveMediaRecognitionSource(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return s.DefaultMediaRecognitionSource(), nil
	}
	return NormalizeMediaRecognitionSource(value)
}

type MoviePilotMediaInfo struct {
	MediaType           string
	Title               string
	OriginalTitle       string
	Year                string
	Category            string
	TitleYear           string
	TmdbID              string
	PosterPath          string
	BackdropPath        string
	Rating              float64
	Genres              []string
	SeasonEpisode       string
	ResourceType        string
	ResourcePix         string
	VideoEncode         string
	GenreIDs            []string
	OriginalLanguages   []string
	OriginCountries     []string
	ProductionCountries []string
	CategoryFields      map[string][]string
	BeginSeason         int
	HasBeginSeason      bool
}

type MoviePilotSearchResult struct {
	MediaType     string   `json:"media_type"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Year          string   `json:"year,omitempty"`
	TitleYear     string   `json:"title_year,omitempty"`
	TmdbID        string   `json:"tmdb_id"`
	Category      string   `json:"category,omitempty"`
	PosterPath    string   `json:"poster_path,omitempty"`
	BackdropPath  string   `json:"backdrop_path,omitempty"`
	Rating        float64  `json:"rating,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Overview      string   `json:"overview,omitempty"`
}

func (s *MoviePilotService) SearchMedia(keyword string, count int) ([]MoviePilotSearchResult, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, errors.New("搜索关键词不能为空")
	}
	if count <= 0 || count > 20 {
		count = 8
	}
	if fallbackErr, active := s.activeLocalRecognitionFallback(); active {
		return s.searchMediaLocally(keyword, count, fallbackErr)
	}

	values := url.Values{}
	values.Set("title", keyword)
	values.Set("type", "media")
	values.Set("page", "1")
	values.Set("count", strconv.Itoa(count))

	body, err := s.doGet("/api/v1/media/search", values)
	if err != nil {
		return s.searchMediaLocally(keyword, count, err)
	}
	if err := validateMoviePilotSuccess(body); err != nil {
		return s.searchMediaLocally(keyword, count, err)
	}

	rawItems := extractSearchResultMaps(body)
	results := make([]MoviePilotSearchResult, 0, len(rawItems))
	seen := make(map[string]struct{}, len(rawItems))
	for _, raw := range rawItems {
		item := parseSearchResult(raw)
		categoryInfo := parseMediaInfo(raw)
		s.applyConfiguredLocalCategory(&categoryInfo)
		item.Category = categoryInfo.Category
		if strings.TrimSpace(item.TmdbID) == "" || strings.TrimSpace(item.Title) == "" {
			continue
		}
		key := item.MediaType + ":" + item.TmdbID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		results = append(results, item)
	}
	if len(results) == 0 && s.local != nil {
		return s.searchMediaLocally(keyword, count, errors.New("MoviePilot 未返回媒体搜索结果"))
	}
	s.clearLocalRecognitionFallback()
	return results, nil
}

func (s *MoviePilotService) RecognizeFile(filePath string) (MoviePilotMediaInfo, map[string]any, error) {
	return s.RecognizeFileWithSource(filePath, "")
}

// RecognizeFileWithSource uses exactly the requested recognition mode. An
// empty source resolves to the system setting; shadow mode always keeps MP2 as
// the authoritative result even when local recognition succeeds.
func (s *MoviePilotService) RecognizeFileWithSource(filePath, source string) (MoviePilotMediaInfo, map[string]any, error) {
	normalized, err := s.ResolveMediaRecognitionSource(source)
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}
	if normalized == MediaRecognitionSourceMedianame {
		return s.recognizeMedianameCompat(filePath, MediaRecognitionModeFile)
	}
	if normalized == MediaRecognitionSourceLocal {
		if s.local == nil {
			return MoviePilotMediaInfo{}, nil, errors.New("FilmFusion 本地识别未初始化")
		}
		return s.local.RecognizeFallback(s.requestContext(), filePath, MediaRecognitionModeFile)
	}
	info, raw, err := s.recognizeFileWithMoviePilot(filePath)
	if err == nil {
		s.clearLocalRecognitionFallback()
	}
	if normalized == MediaRecognitionSourceShadow {
		return s.finishRecognitionWithShadow(filePath, MediaRecognitionModeFile, info, raw, err)
	}
	return info, raw, err
}

func (s *MoviePilotService) recognizeFileWithMoviePilot(filePath string) (MoviePilotMediaInfo, map[string]any, error) {
	values := url.Values{}
	endpoint := "/api/v1/media/recognize_file"
	values.Set("path", filePath)
	if customWords, configured := s.localRecognitionWords(); configured {
		endpoint = "/api/v1/media/recognize"
		values = url.Values{}
		values.Set("title", filePath)
		values.Set("custom_words", customWords)
	}
	info, raw, err := s.recognizeMedia(endpoint, values)
	if err != nil {
		return MoviePilotMediaInfo{}, raw, err
	}
	if strings.TrimSpace(info.Title) == "" {
		return MoviePilotMediaInfo{}, raw, errors.New("MoviePilot 未识别到媒体信息")
	}
	return info, raw, nil
}

// RecognizeTitle identifies a release title without treating it as a filesystem path.
func (s *MoviePilotService) RecognizeTitle(title string) (MoviePilotMediaInfo, map[string]any, error) {
	return s.RecognizeTitleWithSource(title, "")
}

// RecognizeTitleWithSource mirrors RecognizeFileWithSource for title input.
func (s *MoviePilotService) RecognizeTitleWithSource(title, source string) (MoviePilotMediaInfo, map[string]any, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return MoviePilotMediaInfo{}, nil, errors.New("识别标题不能为空")
	}
	normalized, err := s.ResolveMediaRecognitionSource(source)
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}
	if normalized == MediaRecognitionSourceMedianame {
		return s.recognizeMedianameCompat(title, MediaRecognitionModeTitle)
	}
	if normalized == MediaRecognitionSourceLocal {
		if s.local == nil {
			return MoviePilotMediaInfo{}, nil, errors.New("FilmFusion 本地识别未初始化")
		}
		return s.local.RecognizeFallback(s.requestContext(), title, MediaRecognitionModeTitle)
	}
	info, raw, err := s.recognizeTitleWithMoviePilot(title)
	if err == nil {
		s.clearLocalRecognitionFallback()
	}
	if normalized == MediaRecognitionSourceShadow {
		return s.finishRecognitionWithShadow(title, MediaRecognitionModeTitle, info, raw, err)
	}
	return info, raw, err
}

func (s *MoviePilotService) recognizeMedia(endpoint string, values url.Values) (MoviePilotMediaInfo, map[string]any, error) {
	body, err := s.doGet(endpoint, values)
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}
	if err := validateMoviePilotSuccess(body); err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}

	dataMap := unwrapDataMap(body)
	info := parseMediaInfo(dataMap)
	info.BeginSeason, info.HasBeginSeason = extractBeginSeason(dataMap)
	s.applyConfiguredLocalCategory(&info)
	return info, dataMap, nil
}

func (s *MoviePilotService) applyConfiguredLocalCategory(info *MoviePilotMediaInfo) {
	if s == nil || s.local == nil || info == nil {
		return
	}
	category, configured, err := s.local.ApplyConfiguredCategory(*info)
	if err != nil {
		if s.logger != nil {
			s.logger.Warnf("[media-recognition] 应用本地分类配置失败，保留上游分类: %v", err)
		}
		return
	}
	if configured {
		info.Category = category
	}
}
