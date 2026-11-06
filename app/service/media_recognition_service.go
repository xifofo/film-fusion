package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"film-fusion/app/logger"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

const (
	MediaRecognitionModeTitle = "title"
	MediaRecognitionModeFile  = "file"
	maxMediaRecognitionInput  = 16 << 10
)

// MediaRecognitionMetaInfo 是不依赖外部服务即可得到的发布标题解析结果。
type MediaRecognitionMetaInfo struct {
	OriginalInput  string   `json:"original_input"`
	ProcessedInput string   `json:"processed_input"`
	FileName       string   `json:"file_name,omitempty"`
	Extension      string   `json:"extension,omitempty"`
	Name           string   `json:"name"`
	Year           string   `json:"year,omitempty"`
	MediaType      string   `json:"media_type"`
	TMDBID         string   `json:"tmdb_id,omitempty"`
	BeginSeason    *int     `json:"begin_season,omitempty"`
	EndSeason      *int     `json:"end_season,omitempty"`
	BeginEpisode   *int     `json:"begin_episode,omitempty"`
	EndEpisode     *int     `json:"end_episode,omitempty"`
	SeasonEpisode  string   `json:"season_episode,omitempty"`
	ResourceType   string   `json:"resource_type,omitempty"`
	ResourcePix    string   `json:"resource_pix,omitempty"`
	VideoEncode    string   `json:"video_encode,omitempty"`
	VideoBit       string   `json:"video_bit,omitempty"`
	AudioEncode    string   `json:"audio_encode,omitempty"`
	WebSource      string   `json:"web_source,omitempty"`
	ResourceEffect []string `json:"resource_effect,omitempty"`
	ResourceTeam   string   `json:"resource_team,omitempty"`
	AppliedWords   []string `json:"applied_words"`
}

// MediaRecognitionMediaInfo 是medianame 解析与 TMDB 匹配后的统一媒体实体。
type MediaRecognitionMediaInfo struct {
	Source              string   `json:"source"`
	MediaType           string   `json:"media_type"`
	Title               string   `json:"title"`
	OriginalTitle       string   `json:"original_title,omitempty"`
	Year                string   `json:"year,omitempty"`
	TitleYear           string   `json:"title_year,omitempty"`
	TMDBID              string   `json:"tmdb_id,omitempty"`
	Category            string   `json:"category,omitempty"`
	CategoryPath        string   `json:"category_path,omitempty"`
	PosterPath          string   `json:"poster_path,omitempty"`
	BackdropPath        string   `json:"backdrop_path,omitempty"`
	Rating              float64  `json:"rating,omitempty"`
	Genres              []string `json:"genres,omitempty"`
	GenreIDs            []string `json:"genre_ids,omitempty"`
	Overview            string   `json:"overview,omitempty"`
	OriginalLanguage    string   `json:"original_language,omitempty"`
	OriginCountries     []string `json:"origin_countries,omitempty"`
	ProductionCountries []string `json:"production_countries,omitempty"`
}

// MediaRecognitionCandidate 是medianame 结果向旧自动化输出投影的候选项。
type MediaRecognitionCandidate struct {
	MediaRecognitionMediaInfo
	Score      int     `json:"score"`
	Confidence float64 `json:"confidence"`
}

// MediaRecognitionResult 汇总 medianame 的识别词轨迹、解析和可选 TMDB 匹配结果。
type MediaRecognitionResult struct {
	Engine     string                      `json:"engine"`
	Mode       string                      `json:"mode"`
	WordResult MediaRecognitionWordResult  `json:"word_result"`
	MetaInfo   MediaRecognitionMetaInfo    `json:"meta_info"`
	MediaInfo  MediaRecognitionMediaInfo   `json:"media_info"`
	Candidates []MediaRecognitionCandidate `json:"candidates"`
	TMDBStatus string                      `json:"tmdb_status"`
	Warning    string                      `json:"warning,omitempty"`
	Raw        map[string]any              `json:"raw"`
}

// MediaRecognitionOptions 控制一次 medianame 识别是否使用临时词表以及是否查询 TMDB。
type MediaRecognitionOptions struct {
	Mode             string
	Words            []string
	UseProvidedWords bool
	LookupTMDB       bool
}

// MediaRecognitionService 管理 medianame 的识别词、配置和 TMDB 请求。
type MediaRecognitionService struct {
	frozen          *MediaPipelineSnapshot
	medianameClient *medianameTMDBClient
	db              *gorm.DB
	tmdb            *TMDBService
	logger          *logger.Logger
}

// NewMediaRecognitionService 创建本地媒体识别服务。
func NewMediaRecognitionService(db *gorm.DB, tmdb *TMDBService, log *logger.Logger) *MediaRecognitionService {
	return &MediaRecognitionService{db: db, tmdb: tmdb, logger: log}
}

func (s *MediaRecognitionService) LoadWords() (words []string, configured bool, err error) {
	if s != nil && s.frozen != nil {
		return s.frozen.Words, s.frozen.WordsConfigured, nil
	}
	if s == nil || s.db == nil {
		return nil, false, errors.New("数据库未初始化")
	}
	var row model.SystemConfig
	err = s.db.Where("config_key = ?", model.ConfigKeyMediaRecognitionWords).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(row.ConfigValue) == "" {
		return []string{}, true, nil
	}
	if err := json.Unmarshal([]byte(row.ConfigValue), &words); err != nil {
		return nil, true, fmt.Errorf("解析本地识别词失败: %w", err)
	}
	words = NormalizeMediaRecognitionWords(words)
	if err := ValidateMediaRecognitionWords(words); err != nil {
		return nil, true, fmt.Errorf("数据库中的本地识别词无效: %w", err)
	}
	return words, true, nil
}

func (s *MediaRecognitionService) SaveWords(words []string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("数据库未初始化")
	}
	words = NormalizeMediaRecognitionWords(words)
	if err := ValidateMediaRecognitionWords(words); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(words)
	if err != nil {
		return nil, err
	}

	var existing model.SystemConfig
	err = s.db.Unscoped().Where("config_key = ?", model.ConfigKeyMediaRecognitionWords).First(&existing).Error
	switch {
	case err == nil:
		err = s.db.Unscoped().Model(&existing).Updates(map[string]any{
			"config_value": string(encoded),
			"config_type":  model.TypeJSON,
			"category":     model.CategoryMediaRecognition,
			"description":  "FilmFusion 本地媒体识别词",
			"is_system":    true,
			"is_visible":   true,
			"sort_order":   10,
			"deleted_at":   nil,
		}).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		err = s.db.Create(&model.SystemConfig{
			ConfigKey:   model.ConfigKeyMediaRecognitionWords,
			ConfigValue: string(encoded),
			ConfigType:  model.TypeJSON,
			Category:    model.CategoryMediaRecognition,
			Description: "FilmFusion 本地媒体识别词",
			IsSystem:    true,
			IsVisible:   true,
			SortOrder:   10,
		}).Error
	}
	if err != nil {
		return nil, err
	}
	return words, nil
}

func (s *MediaRecognitionService) TMDBConfigured() bool {
	return s != nil && s.tmdb != nil && s.tmdb.mediaRecognitionConfigured()
}

func normalizeMediaRecognitionMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), MediaRecognitionModeTitle) {
		return MediaRecognitionModeTitle
	}
	return MediaRecognitionModeFile
}

// Recognize 让自动化和识别测试共用 medianame，保留统一的结果字段。
func (s *MediaRecognitionService) Recognize(ctx context.Context, input string, options MediaRecognitionOptions) (MediaRecognitionResult, error) {
	r := s.RecognizeMedianame(ctx, input, options)
	out := adaptMedianameRecognition(r)
	out.Mode = normalizeMediaRecognitionMode(options.Mode)
	if r.Error != "" {
		return out, errors.New(r.Error)
	}
	return out, nil
}
