package service

import (
	"errors"
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
		return MediaRecognitionSourceMedianame
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
	if s.local == nil {
		return nil, errors.New("medianame 服务未初始化")
	}
	return s.local.SearchMedia(s.requestContext(), keyword, count)
}

func (s *MoviePilotService) RecognizeFile(filePath string) (MoviePilotMediaInfo, map[string]any, error) {
	return s.RecognizeFileWithSource(filePath, "")
}

// RecognizeFileWithSource 统一使用 medianame，旧引擎配置由入口归一化。
func (s *MoviePilotService) RecognizeFileWithSource(filePath, source string) (MoviePilotMediaInfo, map[string]any, error) {
	_, err := s.ResolveMediaRecognitionSource(source)
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}
	return s.recognizeMedianameCompat(filePath, MediaRecognitionModeFile)
}

// RecognizeTitle identifies a release title without treating it as a filesystem path.
func (s *MoviePilotService) RecognizeTitle(title string) (MoviePilotMediaInfo, map[string]any, error) {
	return s.RecognizeTitleWithSource(title, "")
}

// RecognizeTitleWithSource mirrors RecognizeFileWithSource for title input.
func (s *MoviePilotService) RecognizeTitleWithSource(title, source string) (MoviePilotMediaInfo, map[string]any, error) {
	_, err := s.ResolveMediaRecognitionSource(source)
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}
	return s.recognizeMedianameCompat(title, MediaRecognitionModeTitle)
}
