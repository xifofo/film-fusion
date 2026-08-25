package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"film-fusion/app/model"

	"github.com/nikolalohinski/gonja/v2/builtins"
	gonjaconfig "github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"
	"gorm.io/gorm"
)

const (
	DefaultMovieRenameFormat = "{{title}}{% if year %} ({{year}}){% endif %}/{{title}}{% if year %} ({{year}}){% endif %}{% if part %}-{{part}}{% endif %}{% if videoFormat %} - {{videoFormat}}{% endif %}{{fileExt}}"
	DefaultTVRenameFormat    = "{{title}}{% if year %} ({{year}}){% endif %}/Season {{season}}/{{title}} - {{season_episode}}{% if part %}-{{part}}{% endif %}{% if episode %} - 第 {{episode}} 集{% endif %}{{fileExt}}"

	maxMediaRenameTemplateBytes = 16 << 10
	maxMediaRenameOutputBytes   = 4 << 10
	maxMediaRenameSegmentBytes  = 255
)

var (
	mediaRenameForbiddenTagRE  = regexp.MustCompile(`(?is){%\s*[-+]?\s*(?:include|import|from|extends|block|macro|for)\b`)
	mediaRenameIdentifierRE    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	mediaRenameWindowsDriveRE  = regexp.MustCompile(`^[A-Za-z]:`)
	mediaRenameSeasonEpisodeRE = regexp.MustCompile(`(?i)S(\d{1,3})(?:E(\d{1,4})(?:\s*(?:-|~|E)\s*E?(\d{1,4}))?)?`)
	mediaRenameLeadingNumberRE = regexp.MustCompile(`^\s*(\d+)`)
	mediaRenameWindowsDeviceRE = regexp.MustCompile(`(?i)^(?:CON|PRN|AUX|NUL|CLOCK\$|COM[1-9]|LPT[1-9])$`)

	errMediaRenameOutputTooLong = errors.New("重命名模板输出超过 4 KiB")

	mediaRenameEnvironment = &exec.Environment{
		Context:           exec.EmptyContext(),
		Filters:           builtins.Filters,
		Tests:             builtins.Tests,
		ControlStructures: builtins.ControlStructures,
		Methods:           builtins.Methods,
	}
)

type MediaRenameFormats struct {
	MovieFormat string `json:"movie_format"`
	TVFormat    string `json:"tv_format"`
}

type MediaRenameVariable struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Example     any    `json:"example"`
}

type MediaRenameConfigResult struct {
	Configured         bool                  `json:"configured"`
	Active             bool                  `json:"active"`
	MovieFormat        string                `json:"movie_format"`
	TVFormat           string                `json:"tv_format"`
	DefaultMovieFormat string                `json:"default_movie_format"`
	DefaultTVFormat    string                `json:"default_tv_format"`
	CommonVariables    []MediaRenameVariable `json:"common_variables"`
	TVVariables        []MediaRenameVariable `json:"tv_variables"`
	LoadErrors         []string              `json:"load_errors"`
}

type MediaRenameValidationResult struct {
	Valid     bool     `json:"valid"`
	Errors    []string `json:"errors"`
	Warnings  []string `json:"warnings"`
	Variables []string `json:"variables"`
}

type MediaRenamePreviewResult struct {
	Path      string         `json:"path"`
	Template  string         `json:"template"`
	Variables map[string]any `json:"variables"`
	Warnings  []string       `json:"warnings"`
}

// MediaRenameValidationError lets handlers identify which of the two global
// templates blocked an atomic save and return its structured diagnostics.
type MediaRenameValidationError struct {
	MediaType  string
	Validation MediaRenameValidationResult
	Err        error
}

func (e *MediaRenameValidationError) Error() string {
	if e == nil || e.Err == nil {
		return "重命名模板无效"
	}
	return e.Err.Error()
}

func (e *MediaRenameValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// MediaRenameInput contains only inert recognition data. No service, file, or
// arbitrary struct is exposed to the user-authored template.
type MediaRenameInput struct {
	Info               MoviePilotMediaInfo
	MediaType          string
	Category           string
	RecognizeName      string
	TransferName       string
	OriginalName       string
	LegacyOriginalName string
	RecognitionRaw     map[string]any
	TransferRaw        map[string]any
}

func MediaRenameCommonVariables() []MediaRenameVariable {
	return []MediaRenameVariable{
		{Name: "title", Label: "标题", Description: "TMDB/豆瓣中的标题", Example: "流浪地球"},
		{Name: "en_title", Label: "英文标题", Description: "TMDB 中的英文标题", Example: "The Wandering Earth"},
		{Name: "original_title", Label: "原语种标题", Description: "元数据中的原语种标题", Example: "流浪地球"},
		{Name: "name", Label: "识别名称", Description: "从文件名中识别的名称", Example: "流浪地球"},
		{Name: "en_name", Label: "识别英文名", Description: "从文件名中识别的英文名称", Example: "The Wandering Earth"},
		{Name: "original_name", Label: "原文件名", Description: "整理前的原文件名（含后缀）", Example: "The.Wandering.Earth.2019.2160p.WEB-DL.mkv"},
		{Name: "year", Label: "年份", Description: "发行或首播年份", Example: 2019},
		{Name: "title_year", Label: "标题年份", Description: "标题与年份的组合", Example: "流浪地球 (2019)"},
		{Name: "type", Label: "媒体类型", Description: "电影或电视剧", Example: "电影"},
		{Name: "category", Label: "媒体分类", Description: "分类策略匹配出的分类", Example: "华语电影"},
		{Name: "vote_average", Label: "评分", Description: "元数据站评分", Example: 7.9},
		{Name: "poster", Label: "海报", Description: "海报图片地址", Example: "/poster.jpg"},
		{Name: "backdrop", Label: "背景图", Description: "背景图片地址", Example: "/backdrop.jpg"},
		{Name: "actors", Label: "演员", Description: "前五位演员，以顿号连接", Example: "吴京、 屈楚萧、 李光洁"},
		{Name: "overview", Label: "简介", Description: "媒体简介", Example: "太阳即将毁灭，人类开启流浪地球计划。"},
		{Name: "resourceType", Label: "资源类型", Description: "BluRay、WEB-DL、Remux 等", Example: "WEB-DL"},
		{Name: "effect", Label: "特效", Description: "HDR、DoVi 等效果标签", Example: "HDR"},
		{Name: "edition", Label: "版本", Description: "资源类型与特效组合", Example: "WEB-DL HDR"},
		{Name: "videoFormat", Label: "分辨率", Description: "视频分辨率", Example: "2160p"},
		{Name: "resource_term", Label: "质量", Description: "资源类型、特效与分辨率组合", Example: "WEB-DL HDR 2160p"},
		{Name: "releaseGroup", Label: "制作组", Description: "制作组或字幕组", Example: "OurTV"},
		{Name: "videoCodec", Label: "视频编码", Description: "视频编码格式", Example: "H.265"},
		{Name: "videoBit", Label: "视频位深", Description: "8bit、10bit 等", Example: "10bit"},
		{Name: "audioCodec", Label: "音频编码", Description: "音频编码格式", Example: "DDP 5.1"},
		{Name: "fps", Label: "帧率", Description: "视频帧率", Example: "23.976"},
		{Name: "webSource", Label: "流媒体平台", Description: "WEB 来源平台", Example: "Netflix"},
		{Name: "tmdbid", Label: "TMDB ID", Description: "TMDB 媒体 ID", Example: "535167"},
		{Name: "imdbid", Label: "IMDb ID", Description: "IMDb 媒体 ID", Example: "tt7605074"},
		{Name: "doubanid", Label: "豆瓣 ID", Description: "豆瓣媒体 ID", Example: "26266893"},
		{Name: "part", Label: "分段", Description: "多段文件的段号", Example: "Part1"},
		{Name: "fileExt", Label: "文件后缀", Description: "带前导点的源文件后缀", Example: ".mkv"},
		{Name: "customization", Label: "自定义占位符", Description: "识别结果中的自定义内容", Example: "收藏版"},
	}
}

func MediaRenameTVVariables() []MediaRenameVariable {
	return []MediaRenameVariable{
		{Name: "season", Label: "季号", Description: "季号数字", Example: 1},
		{Name: "season_fmt", Label: "格式化季号", Description: "补零后的季号", Example: "S01"},
		{Name: "season_year", Label: "季年份", Description: "当前季首播年份", Example: 2023},
		{Name: "episode", Label: "集号", Description: "集号或集号范围", Example: "2"},
		{Name: "season_episode", Label: "季集", Description: "格式化后的季集编号", Example: "S01E02"},
		{Name: "episode_title", Label: "集标题", Description: "对应集标题", Example: "科学边界"},
		{Name: "episode_date", Label: "播出日期", Description: "对应集播出日期", Example: "2023-01-15"},
	}
}

func (s *MediaRecognitionService) LoadMediaRenameConfig() (MediaRenameConfigResult, error) {
	result := mediaRenameConfigView(MediaRenameFormats{
		MovieFormat: DefaultMovieRenameFormat,
		TVFormat:    DefaultTVRenameFormat,
	}, false)
	if s == nil || s.db == nil {
		return result, errors.New("数据库未初始化")
	}

	var row model.SystemConfig
	err := s.db.Where("config_key = ?", model.ConfigKeyMediaRecognitionRename).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, nil
	}
	if err != nil {
		return result, err
	}

	var formats MediaRenameFormats
	if err := json.Unmarshal([]byte(row.ConfigValue), &formats); err != nil {
		result.Configured = true
		result.LoadErrors = append(result.LoadErrors, "已保存的重命名配置无法解析，当前展示默认模板；重新保存后即可修复")
		return result, nil
	}
	result = mediaRenameConfigView(formats, true)
	if _, err := ValidateMediaRenameTemplate("movie", formats.MovieFormat); err != nil {
		result.LoadErrors = append(result.LoadErrors, "已保存的电影重命名格式无效: "+err.Error())
	}
	if _, err := ValidateMediaRenameTemplate("tv", formats.TVFormat); err != nil {
		result.LoadErrors = append(result.LoadErrors, "已保存的电视剧重命名格式无效: "+err.Error())
	}
	if len(result.LoadErrors) > 0 {
		result.Active = false
	}
	return result, nil
}

func (s *MediaRecognitionService) SaveMediaRenameConfig(movieFormat, tvFormat string) (MediaRenameConfigResult, error) {
	if s == nil || s.db == nil {
		return MediaRenameConfigResult{}, errors.New("数据库未初始化")
	}
	movieFormat = strings.TrimSpace(movieFormat)
	tvFormat = strings.TrimSpace(tvFormat)
	if validation, err := ValidateMediaRenameTemplate("movie", movieFormat); err != nil {
		return MediaRenameConfigResult{}, &MediaRenameValidationError{
			MediaType: "movie", Validation: validation, Err: fmt.Errorf("电影重命名格式无效: %w", err),
		}
	}
	if validation, err := ValidateMediaRenameTemplate("tv", tvFormat); err != nil {
		return MediaRenameConfigResult{}, &MediaRenameValidationError{
			MediaType: "tv", Validation: validation, Err: fmt.Errorf("电视剧重命名格式无效: %w", err),
		}
	}
	formats := MediaRenameFormats{MovieFormat: movieFormat, TVFormat: tvFormat}
	encoded, err := json.Marshal(formats)
	if err != nil {
		return MediaRenameConfigResult{}, err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existing model.SystemConfig
		err := tx.Unscoped().Where("config_key = ?", model.ConfigKeyMediaRecognitionRename).First(&existing).Error
		switch {
		case err == nil:
			return tx.Unscoped().Model(&existing).Updates(map[string]any{
				"config_value": string(encoded),
				"config_type":  model.TypeJSON,
				"category":     model.CategoryMediaRecognition,
				"description":  "FilmFusion 自定义电影与电视剧重命名格式",
				"is_system":    true,
				"is_visible":   true,
				"sort_order":   30,
				"deleted_at":   nil,
			}).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(&model.SystemConfig{
				ConfigKey: model.ConfigKeyMediaRecognitionRename, ConfigValue: string(encoded),
				ConfigType: model.TypeJSON, Category: model.CategoryMediaRecognition,
				Description: "FilmFusion 自定义电影与电视剧重命名格式",
				IsSystem:    true, IsVisible: true, SortOrder: 30,
			}).Error
		default:
			return err
		}
	})
	if err != nil {
		return MediaRenameConfigResult{}, err
	}
	return mediaRenameConfigView(formats, true), nil
}

func mediaRenameConfigView(formats MediaRenameFormats, configured bool) MediaRenameConfigResult {
	return MediaRenameConfigResult{
		Configured: configured, Active: configured,
		MovieFormat: formats.MovieFormat, TVFormat: formats.TVFormat,
		DefaultMovieFormat: DefaultMovieRenameFormat, DefaultTVFormat: DefaultTVRenameFormat,
		CommonVariables: MediaRenameCommonVariables(), TVVariables: MediaRenameTVVariables(),
		LoadErrors: make([]string, 0),
	}
}

func ValidateMediaRenameTemplate(mediaType, source string) (MediaRenameValidationResult, error) {
	result := MediaRenameValidationResult{
		Errors: make([]string, 0), Warnings: make([]string, 0),
		Variables: mediaRenameUsedVariables(source, mediaType),
	}
	mediaType, err := normalizeMediaRenameType(mediaType)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		err = errors.New("重命名模板不能为空")
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	if len([]byte(source)) > maxMediaRenameTemplateBytes {
		err = fmt.Errorf("重命名模板不能超过 %d KiB", maxMediaRenameTemplateBytes>>10)
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	if !utf8.ValidString(source) {
		err = errors.New("重命名模板必须是有效 UTF-8")
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	if tag := mediaRenameForbiddenTagRE.FindString(source); tag != "" {
		err = fmt.Errorf("重命名模板不允许使用 %s", strings.TrimSpace(tag))
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}

	template, err := compileMediaRenameTemplate(source)
	if err != nil {
		err = fmt.Errorf("Jinja2 语法错误: %w", err)
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	variables := defaultMediaRenameSample(mediaType)
	if _, err := executeMediaRenameTemplate(template, variables); err != nil {
		err = fmt.Errorf("模板执行失败: %w", err)
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	rendered, err := renderMediaRenameTemplate(source, variables)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	if _, err := normalizeMediaRenameRelativePath(rendered); err != nil {
		err = fmt.Errorf("模板生成的路径不安全: %w", err)
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}
	result.Warnings = mediaRenameWarnings(source, mediaType)
	result.Valid = true
	return result, nil
}

func (s *MediaRecognitionService) PreviewMediaRenameTemplate(mediaType, source string, sample map[string]any) (MediaRenamePreviewResult, error) {
	validation, err := ValidateMediaRenameTemplate(mediaType, source)
	if err != nil {
		return MediaRenamePreviewResult{}, err
	}
	mediaType, _ = normalizeMediaRenameType(mediaType)
	variables := defaultMediaRenameSample(mediaType)
	unknownSampleVariables := mergeMediaRenameSample(variables, sample, mediaType)
	rendered, err := renderMediaRenameTemplate(strings.TrimSpace(source), variables)
	if err != nil {
		return MediaRenamePreviewResult{}, err
	}
	rendered, err = normalizeMediaRenameRelativePath(rendered)
	if err != nil {
		return MediaRenamePreviewResult{}, err
	}
	// Keep the JSON contract stable: a successful preview with no warnings must
	// serialize as [] instead of null so array consumers can render it directly.
	warnings := append([]string{}, validation.Warnings...)
	if len(unknownSampleVariables) > 0 {
		warnings = append(warnings, "已忽略未知示例变量: "+strings.Join(unknownSampleVariables, ", "))
	}
	return MediaRenamePreviewResult{
		Path: rendered, Template: strings.TrimSpace(source), Variables: variables,
		Warnings: warnings,
	}, nil
}

func (s *MediaRecognitionService) BuildMediaRenameTargetPath(categoryPrefix string, input MediaRenameInput) (string, bool, error) {
	config, err := s.LoadMediaRenameConfig()
	if err != nil {
		return "", false, err
	}
	if !config.Active {
		return "", false, nil
	}
	mediaType := normalizeMediaRenameTypeFromInput(input)
	source := config.MovieFormat
	if mediaType == "tv" {
		source = config.TVFormat
	}
	variables := BuildMediaRenameVariables(input)
	s.enrichMediaRenameTVVariables(source, input, variables)
	rendered, err := renderMediaRenameTemplate(source, variables)
	if err != nil {
		return "", true, err
	}
	rendered, err = normalizeMediaRenameRelativePath(rendered)
	if err != nil {
		return "", true, err
	}
	parts := []string{"/"}
	if category := strings.Trim(strings.TrimSpace(categoryPrefix), "/"); category != "" {
		parts = append(parts, category)
	}
	parts = append(parts, rendered)
	return path.Join(parts...), true, nil
}

func (s *MediaRecognitionService) enrichMediaRenameTVVariables(
	source string,
	input MediaRenameInput,
	variables map[string]any,
) {
	if s == nil || s.tmdb == nil || normalizeMediaRenameTypeFromInput(input) != "tv" {
		return
	}
	needsSeasonYear := strings.Contains(source, "season_year") && mediaRenameVariableEmpty(variables["season_year"])
	needsEpisodeTitle := strings.Contains(source, "episode_title") && mediaRenameVariableEmpty(variables["episode_title"])
	needsEpisodeDate := strings.Contains(source, "episode_date") && mediaRenameVariableEmpty(variables["episode_date"])
	if !needsSeasonYear && !needsEpisodeTitle && !needsEpisodeDate {
		return
	}

	tmdbID := strings.TrimSpace(fmt.Sprint(variables["tmdbid"]))
	season, hasSeason := mediaRenameValueInt(variables["season"])
	if tmdbID == "" || !hasSeason {
		return
	}
	episode := 0
	if match := mediaRenameLeadingNumberRE.FindStringSubmatch(fmt.Sprint(variables["episode"])); len(match) > 1 {
		episode, _ = strconv.Atoi(match[1])
	}
	details, err := s.tmdb.GetTVEpisodeRenameDetails(context.Background(), tmdbID, season, episode)
	if err != nil {
		if s.logger != nil {
			s.logger.Debugf("[media-rename] 获取 TMDB 剧集重命名变量失败 tmdb_id=%s season=%d episode=%d: %v", tmdbID, season, episode, err)
		}
		return
	}
	if needsSeasonYear {
		variables["season_year"] = sanitizeMediaRenameContextValue(details.SeasonYear)
	}
	if needsEpisodeTitle {
		variables["episode_title"] = sanitizeMediaRenameContextValue(details.EpisodeTitle)
	}
	if needsEpisodeDate {
		variables["episode_date"] = sanitizeMediaRenameContextValue(details.EpisodeDate)
	}
}

func BuildMediaRenameVariables(input MediaRenameInput) map[string]any {
	mediaMaps, metaMaps := mediaRenameRawMaps(input.RecognitionRaw, input.TransferRaw)
	mediaType := normalizeMediaRenameTypeFromInput(input)
	title := firstNonEmptyMoviePilotString(input.Info.Title, mediaRenameString(mediaMaps, "title"))
	year := firstNonEmptyMoviePilotString(input.Info.Year, mediaRenameString(mediaMaps, "year", "release_year"), mediaRenameString(metaMaps, "year"))
	titleYear := firstNonEmptyMoviePilotString(input.Info.TitleYear, mediaRenameString(mediaMaps, "title_year", "titleYear"), buildMediaRenameTitleYear(title, year))
	name := firstNonEmptyMoviePilotString(mediaRenameString(metaMaps, "name", "cn_name"), mediaRenameBaseName(input.RecognizeName))
	resourceType := firstNonEmptyMoviePilotString(input.Info.ResourceType, mediaRenameString(metaMaps, "resource_type", "resourceType"))
	effect := mediaRenameJoinedValue(metaMaps, "effect", "resource_effect", "resourceEffect")
	videoFormat := firstNonEmptyMoviePilotString(input.Info.ResourcePix, mediaRenameString(metaMaps, "video_format", "videoFormat", "resource_pix", "resourcePix"))
	videoCodec := firstNonEmptyMoviePilotString(input.Info.VideoEncode, mediaRenameString(metaMaps, "video_codec", "videoCodec", "video_encode", "videoEncode"))
	releaseGroup := mediaRenameString(metaMaps, "release_group", "releaseGroup", "resource_team", "resourceTeam", "team")
	actors := mediaRenameNamedValues(mediaMaps, "actors", "actor_names", "cast")
	if len(actors) > 5 {
		actors = actors[:5]
	}
	originalName := strings.TrimSpace(input.OriginalName)
	if originalName == "" {
		originalName = path.Base(strings.TrimSpace(input.RecognizeName))
	}
	fileExt := path.Ext(originalName)
	if fileExt == "" {
		fileExt = path.Ext(strings.TrimSpace(input.TransferName))
	}

	seasonEpisode := firstNonEmptyMoviePilotString(input.Info.SeasonEpisode, mediaRenameString(mediaMaps, "season_episode", "seasonEpisode"), mediaRenameString(metaMaps, "season_episode", "seasonEpisode"))
	season, hasSeason := mediaRenameInt(metaMaps, "begin_season", "beginSeason", "season")
	if !hasSeason && input.Info.HasBeginSeason {
		season, hasSeason = input.Info.BeginSeason, true
	}
	episode, hasEpisode := mediaRenameInt(metaMaps, "begin_episode", "beginEpisode", "episode")
	endEpisode, hasEndEpisode := mediaRenameInt(metaMaps, "end_episode", "endEpisode")
	if parsedSeason, parsedEpisode, parsedEnd, ok := parseMediaRenameSeasonEpisode(seasonEpisode); ok {
		if !hasSeason {
			season, hasSeason = parsedSeason, true
		}
		if !hasEpisode && parsedEpisode >= 0 {
			episode, hasEpisode = parsedEpisode, true
		}
		if !hasEndEpisode && parsedEnd >= 0 {
			endEpisode, hasEndEpisode = parsedEnd, true
		}
	}
	if mediaType == "tv" && !hasSeason {
		season, hasSeason = 1, true
	}
	episodeText := ""
	if hasEpisode {
		episodeText = strconv.Itoa(episode)
		if hasEndEpisode && endEpisode != episode {
			episodeText += "-" + strconv.Itoa(endEpisode)
		}
	}
	if seasonEpisode == "" && hasSeason && hasEpisode {
		seasonEpisode = fmt.Sprintf("S%02dE%02d", season, episode)
		if hasEndEpisode && endEpisode != episode {
			seasonEpisode += fmt.Sprintf("-E%02d", endEpisode)
		}
	}

	values := map[string]any{
		"title":          title,
		"en_title":       mediaRenameString(mediaMaps, "en_title", "english_title", "englishTitle"),
		"original_title": firstNonEmptyMoviePilotString(input.Info.OriginalTitle, mediaRenameString(mediaMaps, "original_title", "originalTitle")),
		"name":           name,
		"en_name":        mediaRenameString(metaMaps, "en_name", "english_name", "englishName"),
		"original_name":  originalName,
		"year":           year,
		"title_year":     titleYear,
		"type":           mediaRenameTypeLabel(mediaType),
		"category":       firstNonEmptyMoviePilotString(input.Category, input.Info.Category),
		"vote_average":   firstNonZeroMoviePilotFloat(input.Info.Rating, mediaRenameFloat(mediaMaps, "vote_average", "voteAverage", "rating", "score")),
		"poster":         firstNonEmptyMoviePilotString(input.Info.PosterPath, mediaRenameString(mediaMaps, "poster", "poster_path", "posterPath")),
		"backdrop":       firstNonEmptyMoviePilotString(input.Info.BackdropPath, mediaRenameString(mediaMaps, "backdrop", "backdrop_path", "backdropPath")),
		"actors":         strings.Join(actors, "、 "),
		"overview":       mediaRenameString(mediaMaps, "overview", "description", "desc"),
		"resourceType":   resourceType,
		"effect":         effect,
		"edition":        firstNonEmptyMoviePilotString(mediaRenameString(metaMaps, "edition"), mediaRenameJoinTerms(resourceType, effect)),
		"videoFormat":    videoFormat,
		"resource_term":  firstNonEmptyMoviePilotString(mediaRenameString(metaMaps, "resource_term", "resourceTerm"), mediaRenameJoinTerms(resourceType, effect, videoFormat)),
		"releaseGroup":   releaseGroup,
		"videoCodec":     videoCodec,
		"videoBit":       mediaRenameString(metaMaps, "video_bit", "videoBit"),
		"audioCodec":     mediaRenameString(metaMaps, "audio_codec", "audioCodec", "audio_encode", "audioEncode"),
		"fps":            mediaRenameString(metaMaps, "fps", "frame_rate", "frameRate"),
		"webSource":      mediaRenameString(metaMaps, "web_source", "webSource"),
		"tmdbid":         input.Info.TmdbID,
		"imdbid":         mediaRenameString(mediaMaps, "imdb_id", "imdbid", "imdbId"),
		"doubanid":       mediaRenameString(mediaMaps, "douban_id", "doubanid", "doubanId"),
		"part":           mediaRenameString(metaMaps, "part", "part_name", "partName"),
		"fileExt":        fileExt,
		"customization":  mediaRenameString(metaMaps, "customization"),
		"season":         mediaRenameOptionalNumberString(season, hasSeason),
		"season_fmt":     mediaRenameSeasonFormat(season, hasSeason),
		"season_year":    mediaRenameString(mediaMaps, "season_year", "seasonYear"),
		"episode":        episodeText,
		"season_episode": seasonEpisode,
		"episode_title":  mediaRenameString(mediaMaps, "episode_title", "episodeTitle"),
		"episode_date":   mediaRenameString(mediaMaps, "episode_date", "episodeDate", "air_date", "airDate"),
	}
	for key, value := range values {
		values[key] = sanitizeMediaRenameContextValue(value)
	}
	return values
}

func compileMediaRenameTemplate(source string) (*exec.Template, error) {
	loader, err := loaders.NewMemoryLoader(map[string]string{"/rename": source})
	if err != nil {
		return nil, err
	}
	return exec.NewTemplate("/rename", gonjaconfig.New(), loader, mediaRenameEnvironment)
}

func renderMediaRenameTemplate(source string, variables map[string]any) (string, error) {
	template, err := compileMediaRenameTemplate(source)
	if err != nil {
		return "", err
	}
	return executeMediaRenameTemplate(template, variables)
}

func executeMediaRenameTemplate(template *exec.Template, variables map[string]any) (string, error) {
	writer := &mediaRenameLimitedWriter{limit: maxMediaRenameOutputBytes}
	if err := template.Execute(writer, exec.NewContext(variables)); err != nil {
		return "", err
	}
	return writer.String(), nil
}

type mediaRenameLimitedWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (w *mediaRenameLimitedWriter) Write(data []byte) (int, error) {
	if w.buffer.Len()+len(data) > w.limit {
		return 0, errMediaRenameOutputTooLong
	}
	return w.buffer.Write(data)
}

func (w *mediaRenameLimitedWriter) String() string { return w.buffer.String() }

func ValidateMediaRenameRelativePath(value string) error {
	_, err := normalizeMediaRenameRelativePath(value)
	return err
}

func normalizeMediaRenameRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("重命名模板生成了空路径")
	}
	if !utf8.ValidString(value) {
		return "", errors.New("路径不是有效 UTF-8")
	}
	if len([]byte(value)) > maxMediaRenameOutputBytes {
		return "", errMediaRenameOutputTooLong
	}
	if strings.ContainsRune(value, 0) {
		return "", errors.New("路径包含 NUL 字符")
	}
	if strings.Contains(value, "\\") {
		return "", errors.New("路径不能包含反斜杠")
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || mediaRenameWindowsDriveRE.MatchString(value) {
		return "", errors.New("路径必须是相对路径")
	}
	if strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return "", errors.New("路径不能包含空目录段")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("路径不能包含换行或控制字符")
		}
	}
	segments := strings.Split(value, "/")
	if len(segments) == 0 {
		return "", errors.New("路径不能为空")
	}
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			return "", errors.New("路径不能包含空目录段")
		}
		if segment == "." || segment == ".." {
			return "", errors.New("路径不能包含 . 或 ..")
		}
		if len([]byte(segment)) > maxMediaRenameSegmentBytes {
			return "", fmt.Errorf("路径段不能超过 %d 字节", maxMediaRenameSegmentBytes)
		}
		if strings.ContainsAny(segment, `:*?"<>|`) {
			return "", errors.New("路径段包含 Windows 不允许的字符")
		}
		if strings.HasSuffix(segment, " ") || strings.HasSuffix(segment, ".") {
			return "", errors.New("路径段不能以空格或句点结尾")
		}
		deviceName := segment
		if dot := strings.IndexByte(deviceName, '.'); dot >= 0 {
			deviceName = deviceName[:dot]
		}
		if mediaRenameWindowsDeviceRE.MatchString(strings.TrimSpace(deviceName)) {
			return "", errors.New("路径段不能使用 Windows 保留设备名")
		}
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("路径包含目录穿越或非规范目录段")
	}
	return cleaned, nil
}

func normalizeMediaRenameType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie", "movies", "film", "电影":
		return "movie", nil
	case "tv", "series", "show", "tvshow", "电视剧", "剧集", "番剧", "动漫", "动画", "动画番剧":
		return "tv", nil
	default:
		return "", errors.New("媒体类型必须是 movie 或 tv")
	}
}

func normalizeMediaRenameTypeFromInput(input MediaRenameInput) string {
	for _, value := range []string{input.MediaType, input.Info.MediaType} {
		if normalized, err := normalizeMediaRenameType(value); err == nil {
			return normalized
		}
	}
	return "movie"
}

func mediaRenameTypeLabel(mediaType string) string {
	if mediaType == "tv" {
		return "电视剧"
	}
	return "电影"
}

func defaultMediaRenameSample(mediaType string) map[string]any {
	variables := make(map[string]any)
	for _, variable := range MediaRenameCommonVariables() {
		variables[variable.Name] = sanitizeMediaRenameContextValue(variable.Example)
	}
	if mediaType == "tv" {
		for _, variable := range MediaRenameTVVariables() {
			variables[variable.Name] = sanitizeMediaRenameContextValue(variable.Example)
		}
		variables["title"] = "三体"
		variables["title_year"] = "三体 (2023)"
		variables["year"] = 2023
		variables["type"] = "电视剧"
		variables["category"] = "国产剧"
		variables["original_name"] = "Three-Body.S01E02.2160p.WEB-DL.mkv"
	}
	return variables
}

func mergeMediaRenameSample(target, sample map[string]any, mediaType string) []string {
	if len(sample) == 0 {
		return nil
	}
	known := mediaRenameKnownVariables(mediaType)
	unknown := make([]string, 0)
	for key, value := range sample {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
			continue
		}
		target[key] = sanitizeMediaRenameContextValue(value)
	}
	sort.Strings(unknown)
	return unknown
}

func sanitizeMediaRenameContextValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return sanitizeMediaRenameString(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed >= math.MinInt64 && typed <= math.MaxInt64 && typed == math.Trunc(typed) {
			return int64(typed)
		}
		return typed
	case float32:
		value := float64(typed)
		if value >= math.MinInt64 && value <= math.MaxInt64 && value == math.Trunc(value) {
			return int64(value)
		}
		return typed
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, bool:
		return typed
	case []string:
		limit := len(typed)
		if limit > 5 {
			limit = 5
		}
		result := make([]string, 0, limit)
		for _, item := range typed[:limit] {
			result = append(result, sanitizeMediaRenameString(item))
		}
		return result
	case []any:
		limit := len(typed)
		if limit > 5 {
			limit = 5
		}
		result := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			result = append(result, sanitizeMediaRenameContextValue(item))
		}
		return result
	default:
		return sanitizeMediaRenameString(fmt.Sprint(typed))
	}
}

func sanitizeMediaRenameString(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.NewReplacer(
		"\\", "＼", "/", "／", ":", "：", "*", "＊", "?", "？",
		"\"", "＂", "<", "＜", ">", "＞", "|", "｜",
	).Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func mediaRenameWarnings(source, mediaType string) []string {
	warnings := make([]string, 0, 2)
	if !strings.Contains(source, "fileExt") {
		warnings = append(warnings, "模板未使用 fileExt，整理后的文件可能失去原扩展名")
	}
	if mediaType == "tv" && !strings.Contains(source, "season_episode") && !strings.Contains(source, "episode") {
		warnings = append(warnings, "电视剧模板未使用集号变量，多个剧集可能生成同名文件")
	}
	return warnings
}

func mediaRenameUsedVariables(source, mediaType string) []string {
	known := mediaRenameKnownVariables(mediaType)
	seen := make(map[string]struct{})
	for _, name := range mediaRenameIdentifierRE.FindAllString(source, -1) {
		if _, ok := known[name]; ok {
			seen[name] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(seen))
	for _, variable := range append(MediaRenameCommonVariables(), MediaRenameTVVariables()...) {
		if _, ok := seen[variable.Name]; ok {
			ordered = append(ordered, variable.Name)
		}
	}
	return ordered
}

func mediaRenameKnownVariables(mediaType string) map[string]struct{} {
	known := make(map[string]struct{})
	for _, variable := range MediaRenameCommonVariables() {
		known[variable.Name] = struct{}{}
	}
	if mediaType == "" || strings.EqualFold(mediaType, "tv") {
		for _, variable := range MediaRenameTVVariables() {
			known[variable.Name] = struct{}{}
		}
	}
	return known
}

func mediaRenameRawMaps(rawValues ...map[string]any) (mediaMaps, metaMaps []map[string]any) {
	for _, raw := range rawValues {
		if raw == nil {
			continue
		}
		if nested := mediaRenameNestedMap(raw, "media_info", "mediaInfo"); nested != nil {
			mediaMaps = append(mediaMaps, nested)
		}
		if nested := mediaRenameNestedMap(raw, "meta_info", "metaInfo"); nested != nil {
			metaMaps = append(metaMaps, nested)
		}
		mediaMaps = append(mediaMaps, raw)
		metaMaps = append(metaMaps, raw)
	}
	return mediaMaps, metaMaps
}

func mediaRenameNestedMap(data map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if nested, ok := data[key].(map[string]any); ok {
			return nested
		}
	}
	return nil
}

func mediaRenameString(maps []map[string]any, keys ...string) string {
	for _, data := range maps {
		if value := extractString(data, keys...); value != "" {
			return value
		}
	}
	return ""
}

func mediaRenameFloat(maps []map[string]any, keys ...string) float64 {
	for _, data := range maps {
		if value := extractFloat64(data, keys...); value != 0 {
			return value
		}
	}
	return 0
}

func mediaRenameInt(maps []map[string]any, keys ...string) (int, bool) {
	for _, data := range maps {
		for _, key := range keys {
			raw, ok := data[key]
			if !ok || raw == nil {
				continue
			}
			switch typed := raw.(type) {
			case float64:
				return int(typed), true
			case float32:
				return int(typed), true
			case int:
				return typed, true
			case int64:
				return int(typed), true
			case json.Number:
				if value, err := strconv.Atoi(typed.String()); err == nil {
					return value, true
				}
			case string:
				if value, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
					return value, true
				}
			}
		}
	}
	return 0, false
}

func mediaRenameValueInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := strconv.Atoi(typed.String())
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func mediaRenameVariableEmpty(value any) bool {
	return value == nil || strings.TrimSpace(fmt.Sprint(value)) == ""
}

func mediaRenameNamedValues(maps []map[string]any, keys ...string) []string {
	for _, data := range maps {
		if values := extractNamedStringSlice(data, keys...); len(values) > 0 {
			return values
		}
	}
	return nil
}

func mediaRenameJoinedValue(maps []map[string]any, keys ...string) string {
	if value := mediaRenameString(maps, keys...); value != "" {
		return value
	}
	for _, data := range maps {
		if values := extractStringSlice(data, keys...); len(values) > 0 {
			return strings.Join(values, " ")
		}
	}
	return ""
}

func mediaRenameJoinTerms(values ...string) string {
	parts := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Fields(strings.TrimSpace(value)) {
			key := strings.ToLower(part)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

func buildMediaRenameTitleYear(title, year string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	if strings.TrimSpace(year) == "" || strings.Contains(title, year) {
		return title
	}
	return fmt.Sprintf("%s (%s)", title, year)
}

func mediaRenameBaseName(value string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	return strings.TrimSuffix(name, path.Ext(name))
}

func parseMediaRenameSeasonEpisode(value string) (season, episode, endEpisode int, ok bool) {
	match := mediaRenameSeasonEpisodeRE.FindStringSubmatch(value)
	if len(match) < 2 {
		return 0, -1, -1, false
	}
	season, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, -1, -1, false
	}
	episode, endEpisode = -1, -1
	if len(match) > 2 && match[2] != "" {
		episode, _ = strconv.Atoi(match[2])
	}
	if len(match) > 3 && match[3] != "" {
		endEpisode, _ = strconv.Atoi(match[3])
	}
	return season, episode, endEpisode, true
}

func mediaRenameOptionalNumberString(value int, ok bool) string {
	if !ok {
		return ""
	}
	return strconv.Itoa(value)
}

func mediaRenameSeasonFormat(value int, ok bool) string {
	if !ok {
		return ""
	}
	return fmt.Sprintf("S%02d", value)
}
