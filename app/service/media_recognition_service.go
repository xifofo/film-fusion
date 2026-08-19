package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

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
	ResourceEffect []string `json:"resource_effect,omitempty"`
	ResourceTeam   string   `json:"resource_team,omitempty"`
	AppliedWords   []string `json:"applied_words"`
}

// MediaRecognitionMediaInfo 是本地解析与 TMDB 匹配后的统一媒体实体。
type MediaRecognitionMediaInfo struct {
	Source              string   `json:"source"`
	MediaType           string   `json:"media_type"`
	Title               string   `json:"title"`
	OriginalTitle       string   `json:"original_title,omitempty"`
	Year                string   `json:"year,omitempty"`
	TitleYear           string   `json:"title_year,omitempty"`
	TMDBID              string   `json:"tmdb_id,omitempty"`
	Category            string   `json:"category,omitempty"`
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

// MediaRecognitionCandidate 是本地 TMDB 匹配时用于解释选择结果的候选项。
type MediaRecognitionCandidate struct {
	MediaRecognitionMediaInfo
	Score      int     `json:"score"`
	Confidence float64 `json:"confidence"`
}

// MediaRecognitionResult 汇总识别词轨迹、本地解析和可选 TMDB 匹配结果。
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

// MediaRecognitionOptions 控制一次本地识别是否使用临时词表以及是否查询 TMDB。
type MediaRecognitionOptions struct {
	Mode                string
	Words               []string
	UseProvidedWords    bool
	CategoryYAML        string
	UseProvidedCategory bool
	LookupTMDB          bool
}

// MediaRecognitionService 提供 FilmFusion 自己的识别词、标题解析和 TMDB 匹配能力。
type MediaRecognitionService struct {
	db     *gorm.DB
	tmdb   *TMDBService
	logger *logger.Logger
}

// NewMediaRecognitionService 创建本地媒体识别服务。
func NewMediaRecognitionService(db *gorm.DB, tmdb *TMDBService, log *logger.Logger) *MediaRecognitionService {
	return &MediaRecognitionService{db: db, tmdb: tmdb, logger: log}
}

// LoadWords 读取 FilmFusion 自己保存的识别词；configured 用于区分尚未接管和已保存空词表。
func (s *MediaRecognitionService) LoadWords() (words []string, configured bool, err error) {
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

// SaveWords 覆盖保存完整识别词列表，保存后立即对本地识别和 MP2 请求生效。
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

// TMDBConfigured 返回本地识别是否具备可用的 TMDB 凭据。
func (s *MediaRecognitionService) TMDBConfigured() bool {
	return s != nil && s.tmdb != nil && s.tmdb.mediaRecognitionConfigured()
}

// Recognize 执行不经过 MoviePilot 的完整本地识别。
func (s *MediaRecognitionService) Recognize(ctx context.Context, input string, options MediaRecognitionOptions) (MediaRecognitionResult, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return MediaRecognitionResult{}, errors.New("识别输入不能为空")
	}
	if len(input) > maxMediaRecognitionInput {
		return MediaRecognitionResult{}, fmt.Errorf("识别输入不能超过 %d KiB", maxMediaRecognitionInput>>10)
	}
	mode := normalizeMediaRecognitionMode(options.Mode)
	words := options.Words
	if !options.UseProvidedWords {
		stored, _, err := s.LoadWords()
		if err != nil {
			return MediaRecognitionResult{}, err
		}
		words = stored
	}
	words = NormalizeMediaRecognitionWords(words)
	if err := ValidateMediaRecognitionWords(words); err != nil {
		return MediaRecognitionResult{}, err
	}

	wordResult := ApplyMediaRecognitionWords(input, words)
	for _, step := range wordResult.Steps {
		if step.Error != "" {
			return MediaRecognitionResult{}, fmt.Errorf("第 %d 行识别词执行失败: %s", step.Line, step.Error)
		}
	}
	meta, err := parseLocalMediaMeta(input, wordResult, mode)
	if err != nil {
		return MediaRecognitionResult{}, err
	}
	result := MediaRecognitionResult{
		Engine:     "local",
		Mode:       mode,
		WordResult: wordResult,
		MetaInfo:   meta,
		MediaInfo:  localMediaInfoFromMeta(meta),
		Candidates: make([]MediaRecognitionCandidate, 0),
		TMDBStatus: "skipped",
	}

	if options.LookupTMDB {
		s.enrichWithTMDB(ctx, &result)
	}
	categoryConfig, err := s.moviePilotCategoryConfig(options.CategoryYAML, options.UseProvidedCategory)
	if err != nil {
		return MediaRecognitionResult{}, fmt.Errorf("分类配置无效: %w", err)
	}
	applyMediaRecognitionCategories(&result, categoryConfig)
	result.Raw = buildLocalMediaRecognitionRaw(result)
	return result, nil
}

// RecognizeFallback 将本地识别结果转换成现有 MoviePilot 调用链使用的兼容结构。
func (s *MediaRecognitionService) RecognizeFallback(ctx context.Context, input, mode string) (MoviePilotMediaInfo, map[string]any, error) {
	result, err := s.Recognize(ctx, input, MediaRecognitionOptions{Mode: mode, LookupTMDB: true})
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}
	info := moviePilotInfoFromLocalResult(result)
	if strings.TrimSpace(info.Title) == "" {
		return MoviePilotMediaInfo{}, result.Raw, errors.New("本地识别未得到媒体标题")
	}
	return info, result.Raw, nil
}

// SearchMedia 使用 TMDB 完成本地媒体搜索，供 MP2 不可访问时复用。
func (s *MediaRecognitionService) SearchMedia(ctx context.Context, keyword string, count int) ([]MoviePilotSearchResult, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, errors.New("搜索关键词不能为空")
	}
	if !s.TMDBConfigured() {
		return nil, errors.New("本地媒体搜索需要先配置 TMDB")
	}
	if count <= 0 || count > 20 {
		count = 8
	}
	candidates, err := s.tmdb.searchMediaRecognitionCandidates(ctx, keyword, "", "", count)
	if err != nil {
		return nil, err
	}
	categoryConfig, err := s.moviePilotCategoryConfig("", false)
	if err != nil {
		return nil, err
	}
	results := make([]MoviePilotSearchResult, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Category = classifyMediaRecognitionMediaInfo(candidate.MediaRecognitionMediaInfo, categoryConfig)
		results = append(results, MoviePilotSearchResult{
			MediaType: candidate.MediaType, Title: candidate.Title,
			OriginalTitle: candidate.OriginalTitle, Year: candidate.Year,
			TitleYear: candidate.TitleYear, TmdbID: candidate.TMDBID,
			Category: candidate.Category, PosterPath: candidate.PosterPath,
			BackdropPath: candidate.BackdropPath, Rating: candidate.Rating,
			Genres: candidate.Genres, Overview: candidate.Overview,
		})
	}
	return results, nil
}

// BuildTransferName 在 MP2 不可访问时保留应用识别词后的发布名，避免整理链路直接中断。
func (s *MediaRecognitionService) BuildTransferName(input string) (string, map[string]any, error) {
	words, _, err := s.LoadWords()
	if err != nil {
		return "", nil, err
	}
	result := ApplyMediaRecognitionWords(strings.TrimSpace(input), words)
	for _, step := range result.Steps {
		if step.Error != "" {
			return "", nil, fmt.Errorf("第 %d 行识别词执行失败: %s", step.Line, step.Error)
		}
	}
	originalName := path.Base(strings.ReplaceAll(strings.TrimSpace(input), "\\", "/"))
	processedName := path.Base(strings.ReplaceAll(strings.TrimSpace(result.Processed), "\\", "/"))
	if processedName == "" || processedName == "." || processedName == "/" {
		return "", nil, errors.New("本地重命名未得到有效文件名")
	}
	originalExt := path.Ext(originalName)
	if originalExt != "" && path.Ext(processedName) == "" {
		processedName += originalExt
	}
	return processedName, map[string]any{
		"engine": "local", "name": processedName,
		"meta_info": map[string]any{"apply_words": result.AppliedWords, "org_string": result.Processed},
	}, nil
}

func (s *MediaRecognitionService) enrichWithTMDB(ctx context.Context, result *MediaRecognitionResult) {
	if !s.TMDBConfigured() {
		result.TMDBStatus = "not_configured"
		result.Warning = "TMDB 未启用或未配置凭据；已返回纯本地解析结果"
		return
	}
	matched, candidates, status, err := s.tmdb.matchMediaRecognition(ctx, result.MetaInfo)
	result.Candidates = candidates
	result.TMDBStatus = status
	if err != nil {
		result.Warning = "TMDB 匹配失败；已返回纯本地解析结果: " + err.Error()
		if s.logger != nil {
			s.logger.Warnf("[media-recognition] TMDB 匹配失败: %v", err)
		}
		return
	}
	if matched != nil {
		result.MediaInfo = matched.MediaRecognitionMediaInfo
	}
}

func applyMediaRecognitionCategories(result *MediaRecognitionResult, config MoviePilotCategoryConfig) {
	if result == nil {
		return
	}
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		candidate.Category = classifyMediaRecognitionMediaInfo(candidate.MediaRecognitionMediaInfo, config)
	}
	mediaType := normalizeLocalMediaType(result.MediaInfo.MediaType)
	if mediaType != "movie" && mediaType != "tv" {
		return
	}
	result.MediaInfo.Category = classifyMediaRecognitionMediaInfo(result.MediaInfo, config)
}

func normalizeMediaRecognitionMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), MediaRecognitionModeTitle) {
		return MediaRecognitionModeTitle
	}
	return MediaRecognitionModeFile
}

var (
	localMediaExtensionRE = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|mov|wmv|flv|m4v|ts|m2ts|iso|rmvb|strm)$`)
	localTMDBMarkerRE     = regexp.MustCompile(`(?i)[\[{](?:tmdb(?:id)?)[-_=](\d+)[\]}]`)
	localRichMarkerRE     = regexp.MustCompile(`(?i)\{\[([^\]]*(?:tmdbid|tmdb_id)\s*=\s*\d+[^\]]*)\]\}`)
	localYearRE           = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`)
	localSeasonEpisodeRE  = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])S(\d{1,3})(?:[ ._-]*E(?:P)?(\d{1,4})(?:[ ._-]*(?:E|-)(\d{1,4}))?)?`)
	localChineseSeasonRE  = regexp.MustCompile(`第\s*([0-9零一二两三四五六七八九十百]+)\s*季`)
	localChineseEpisodeRE = regexp.MustCompile(`第\s*([0-9零一二两三四五六七八九十百]+)\s*[集话話](?:\s*[-~至]\s*([0-9零一二两三四五六七八九十百]+))?`)
	localEpisodeOnlyRE    = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])E(?:P)?\s*(\d{1,4})(?:\s*[-~]\s*(\d{1,4}))?`)
	localResolutionRE     = regexp.MustCompile(`(?i)(4320p|2160p|1080[pi]|720p|576p|480p|8k|4k|uhd)`)
	localResourceTypeRE   = regexp.MustCompile(`(?i)(blu[ ._-]?ray|bdrip|remux|web[ ._-]?dl|web[ ._-]?rip|hdtv|hdrip|dvdrip|cam|telesync|ts)`)
	localVideoCodecRE     = regexp.MustCompile(`(?i)(h[ ._-]?26[45]|x26[45]|hevc|avc|av1|mpeg[ ._-]?2)`)
	localVideoBitRE       = regexp.MustCompile(`(?i)(8|10|12)[ ._-]?bit`)
	localAudioCodecRE     = regexp.MustCompile(`(?i)(truehd(?:[ ._-]?atmos)?|dts[ ._-]?hd(?:[ ._-]?ma)?|dts:x|ddp|e[ ._-]?ac[ ._-]?3|ac[ ._-]?3|aac|flac|opus|mp3)(?:[ ._-]?\d(?:\.\d)?(?:ch)?)?`)
	localEffectRE         = regexp.MustCompile(`(?i)(dolby[ ._-]?vision|dovi|hdr10\+|hdr10|hdr|sdr)`)
	localReleaseTeamRE    = regexp.MustCompile(`-([A-Za-z0-9][A-Za-z0-9._-]{1,40})$`)
	localLeadingGroupRE   = regexp.MustCompile(`^\s*(?:\[[^\]]{1,80}\]\s*)+`)
	localBracketRE        = regexp.MustCompile(`[\[\]{}()]`)
	localSpaceRE          = regexp.MustCompile(`\s+`)
)

func parseLocalMediaMeta(original string, words MediaRecognitionWordResult, mode string) (MediaRecognitionMetaInfo, error) {
	processed := strings.TrimSpace(words.Processed)
	normalizedPath := strings.ReplaceAll(processed, "\\", "/")
	fileName := path.Base(normalizedPath)
	if mode == MediaRecognitionModeTitle {
		fileName = processed
	}
	extension := ""
	if match := localMediaExtensionRE.FindStringSubmatch(fileName); len(match) > 1 {
		extension = strings.ToLower(match[1])
		fileName = localMediaExtensionRE.ReplaceAllString(fileName, "")
	}

	meta := MediaRecognitionMetaInfo{
		OriginalInput: original, ProcessedInput: processed, FileName: path.Base(normalizedPath),
		Extension: extension, MediaType: "unknown", AppliedWords: words.AppliedWords,
		ResourceEffect: make([]string, 0),
	}
	markerSource := processed
	meta.TMDBID, meta.MediaType, meta.BeginSeason, meta.BeginEpisode = parseLocalMediaMarkers(markerSource)
	cleanSource := localRichMarkerRE.ReplaceAllString(fileName, " ")
	cleanSource = localTMDBMarkerRE.ReplaceAllString(cleanSource, " ")

	parseLocalSeasonEpisode(cleanSource, &meta)
	if meta.BeginEpisode != nil || meta.BeginSeason != nil {
		if meta.MediaType == "unknown" {
			meta.MediaType = "tv"
		}
	}
	if year := localYearRE.FindStringSubmatch(cleanSource); len(year) > 1 {
		meta.Year = year[1]
	}
	meta.ResourcePix = normalizeLocalResolution(firstLocalMatch(localResolutionRE, cleanSource))
	meta.ResourceType = normalizeLocalResourceType(firstLocalMatch(localResourceTypeRE, cleanSource))
	meta.VideoEncode = normalizeLocalVideoCodec(firstLocalMatch(localVideoCodecRE, cleanSource))
	if bit := firstLocalMatch(localVideoBitRE, cleanSource); bit != "" {
		meta.VideoBit = strings.ToLower(bit) + "bit"
	}
	meta.AudioEncode = normalizeLocalAudioCodec(firstLocalMatch(localAudioCodecRE, cleanSource))
	meta.ResourceEffect = collectLocalEffects(cleanSource)
	if team := localReleaseTeamRE.FindStringSubmatch(cleanSource); len(team) > 1 {
		meta.ResourceTeam = team[1]
	}
	meta.Name = cleanLocalMediaTitle(cleanSource)

	if mode == MediaRecognitionModeFile {
		parent := path.Base(path.Dir(normalizedPath))
		parent = localRichMarkerRE.ReplaceAllString(parent, " ")
		parent = localTMDBMarkerRE.ReplaceAllString(parent, " ")
		if meta.Year == "" {
			if year := localYearRE.FindStringSubmatch(parent); len(year) > 1 {
				meta.Year = year[1]
			}
		}
		if shouldUseParentMediaTitle(meta.Name, cleanSource) {
			parentName := cleanLocalMediaTitle(parent)
			if parentName != "" && !isGenericLocalMediaTitle(parentName) {
				meta.Name = parentName
			}
		}
	}
	if meta.Name == "" {
		return MediaRecognitionMetaInfo{}, errors.New("无法从输入中解析媒体标题")
	}
	if meta.MediaType == "unknown" && meta.Year != "" {
		meta.MediaType = "movie"
	}
	meta.SeasonEpisode = formatLocalSeasonEpisode(meta)
	return meta, nil
}

func parseLocalMediaMarkers(input string) (tmdbID, mediaType string, season, episode *int) {
	mediaType = "unknown"
	if rich := localRichMarkerRE.FindStringSubmatch(input); len(rich) > 1 {
		for _, pair := range strings.Split(rich[1], ";") {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(parts[0]))
			value := strings.TrimSpace(parts[1])
			switch key {
			case "tmdbid", "tmdb_id", "tmdb":
				tmdbID = normalizeTmdbID(value)
			case "type":
				mediaType = normalizeLocalMediaType(value)
			case "s", "season":
				season = localIntPointer(value)
			case "e", "episode":
				episode = localIntPointer(value)
			}
		}
	}
	if tmdbID == "" {
		if simple := localTMDBMarkerRE.FindStringSubmatch(input); len(simple) > 1 {
			tmdbID = simple[1]
		}
	}
	return tmdbID, mediaType, season, episode
}

func parseLocalSeasonEpisode(input string, meta *MediaRecognitionMetaInfo) {
	if match := localSeasonEpisodeRE.FindStringSubmatch(input); len(match) > 1 {
		meta.BeginSeason = localIntPointer(match[1])
		if len(match) > 2 && match[2] != "" {
			meta.BeginEpisode = localIntPointer(match[2])
		}
		if len(match) > 3 && match[3] != "" {
			meta.EndEpisode = localIntPointer(match[3])
		}
	}
	if meta.BeginSeason == nil {
		if match := localChineseSeasonRE.FindStringSubmatch(input); len(match) > 1 {
			meta.BeginSeason = localEpisodePointer(match[1])
		}
	}
	if meta.BeginEpisode == nil {
		if match := localChineseEpisodeRE.FindStringSubmatch(input); len(match) > 1 {
			meta.BeginEpisode = localEpisodePointer(match[1])
			if len(match) > 2 && match[2] != "" {
				meta.EndEpisode = localEpisodePointer(match[2])
			}
		}
	}
	if meta.BeginEpisode == nil {
		if match := localEpisodeOnlyRE.FindStringSubmatch(input); len(match) > 1 {
			meta.BeginEpisode = localIntPointer(match[1])
			if len(match) > 2 && match[2] != "" {
				meta.EndEpisode = localIntPointer(match[2])
			}
		}
	}
	if meta.BeginEpisode != nil && meta.BeginSeason == nil {
		season := 1
		meta.BeginSeason = &season
	}
}

func cleanLocalMediaTitle(input string) string {
	value := localLeadingGroupRE.ReplaceAllString(input, " ")
	patterns := []*regexp.Regexp{
		localSeasonEpisodeRE, localChineseSeasonRE, localChineseEpisodeRE, localEpisodeOnlyRE,
		localYearRE, localResolutionRE, localResourceTypeRE, localVideoCodecRE,
		localVideoBitRE, localAudioCodecRE, localEffectRE, localReleaseTeamRE,
	}
	for _, pattern := range patterns {
		value = pattern.ReplaceAllString(value, " ")
	}
	value = localBracketRE.ReplaceAllString(value, " ")
	value = strings.NewReplacer(".", " ", "_", " ", " - ", " ", "+", " ").Replace(value)
	value = strings.Trim(value, " -_.")
	return localSpaceRE.ReplaceAllString(value, " ")
}

func shouldUseParentMediaTitle(name, source string) bool {
	if name == "" || isGenericLocalMediaTitle(name) {
		return true
	}
	trimmed := strings.TrimSpace(source)
	return localSeasonEpisodeRE.MatchString(trimmed) && strings.HasPrefix(strings.ToUpper(trimmed), "S")
}

func isGenericLocalMediaTitle(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return true
	}
	generic := map[string]struct{}{
		"season": {}, "episode": {}, "complete": {}, "全集": {}, "完结": {}, "movie": {}, "tv": {},
	}
	_, ok := generic[value]
	return ok
}

func firstLocalMatch(pattern *regexp.Regexp, input string) string {
	match := pattern.FindStringSubmatch(input)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

func collectLocalEffects(input string) []string {
	matches := localEffectRE.FindAllStringSubmatch(input, -1)
	seen := make(map[string]struct{})
	effects := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		value := normalizeLocalEffect(match[1])
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		effects = append(effects, value)
	}
	return effects
}

func normalizeLocalResolution(value string) string {
	value = strings.ToLower(value)
	switch value {
	case "4k", "uhd":
		return "2160p"
	case "8k":
		return "4320p"
	default:
		return value
	}
}

func normalizeLocalResourceType(value string) string {
	compact := strings.ToLower(strings.NewReplacer(" ", "", ".", "", "_", "", "-", "").Replace(value))
	switch compact {
	case "bluray", "bdrip":
		return "BluRay"
	case "remux":
		return "Remux"
	case "webdl":
		return "WEB-DL"
	case "webrip":
		return "WEBRip"
	case "hdtv":
		return "HDTV"
	case "hdrip":
		return "HDRip"
	case "dvdrip":
		return "DVDRip"
	case "cam":
		return "CAM"
	case "telesync", "ts":
		return "TS"
	default:
		return value
	}
}

func normalizeLocalVideoCodec(value string) string {
	compact := strings.ToLower(strings.NewReplacer(" ", "", ".", "", "_", "", "-", "").Replace(value))
	switch compact {
	case "h265", "x265", "hevc":
		return "H.265"
	case "h264", "x264", "avc":
		return "H.264"
	case "av1":
		return "AV1"
	case "mpeg2":
		return "MPEG-2"
	default:
		return value
	}
}

func normalizeLocalAudioCodec(value string) string {
	compact := strings.ToLower(strings.NewReplacer(" ", "", ".", "", "_", "", "-", "").Replace(value))
	switch {
	case strings.HasPrefix(compact, "truehdatmos"):
		return "TrueHD Atmos"
	case strings.HasPrefix(compact, "truehd"):
		return "TrueHD"
	case strings.HasPrefix(compact, "dtshdma"):
		return "DTS-HD MA"
	case strings.HasPrefix(compact, "dtshd"):
		return "DTS-HD"
	case compact == "dtsx":
		return "DTS:X"
	case compact == "ddp", compact == "eac3":
		return "DDP"
	case compact == "ac3":
		return "AC3"
	default:
		return strings.ToUpper(value)
	}
}

func normalizeLocalEffect(value string) string {
	compact := strings.ToLower(strings.NewReplacer(" ", "", ".", "", "_", "", "-", "").Replace(value))
	switch compact {
	case "dolbyvision", "dovi":
		return "DoVi"
	case "hdr10+":
		return "HDR10+"
	case "hdr10":
		return "HDR10"
	case "hdr":
		return "HDR"
	case "sdr":
		return "SDR"
	default:
		return value
	}
}

func normalizeLocalMediaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tv", "series", "show", "电视剧", "剧集", "anime", "动漫", "动画":
		return "tv"
	case "movie", "电影":
		return "movie"
	default:
		return "unknown"
	}
}

func localIntPointer(value string) *int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number < 0 {
		return nil
	}
	return &number
}

func localEpisodePointer(value string) *int {
	number, _, err := parseMediaRecognitionEpisode(strings.TrimSpace(value))
	if err != nil || number < 0 {
		return nil
	}
	return &number
}

func formatLocalSeasonEpisode(meta MediaRecognitionMetaInfo) string {
	if meta.BeginSeason == nil {
		return ""
	}
	season := fmt.Sprintf("S%02d", *meta.BeginSeason)
	if meta.BeginEpisode == nil {
		return season
	}
	episode := fmt.Sprintf("E%02d", *meta.BeginEpisode)
	if meta.EndEpisode != nil && *meta.EndEpisode != *meta.BeginEpisode {
		episode += fmt.Sprintf("-E%02d", *meta.EndEpisode)
	}
	return season + episode
}

func localMediaInfoFromMeta(meta MediaRecognitionMetaInfo) MediaRecognitionMediaInfo {
	titleYear := meta.Name
	if meta.Year != "" {
		titleYear = fmt.Sprintf("%s (%s)", meta.Name, meta.Year)
	}
	return MediaRecognitionMediaInfo{
		Source: "local", MediaType: meta.MediaType, Title: meta.Name,
		Year: meta.Year, TitleYear: titleYear, TMDBID: meta.TMDBID,
		Genres: make([]string, 0), GenreIDs: make([]string, 0),
		OriginCountries: make([]string, 0), ProductionCountries: make([]string, 0),
	}
}

func moviePilotInfoFromLocalResult(result MediaRecognitionResult) MoviePilotMediaInfo {
	media := result.MediaInfo
	meta := result.MetaInfo
	info := MoviePilotMediaInfo{
		MediaType: media.MediaType, Title: media.Title, Year: media.Year,
		Category: media.Category, TitleYear: media.TitleYear, TmdbID: media.TMDBID,
		PosterPath: media.PosterPath, BackdropPath: media.BackdropPath,
		Rating: media.Rating, Genres: media.Genres, GenreIDs: media.GenreIDs,
		OriginalLanguages: []string{media.OriginalLanguage},
		OriginCountries:   media.OriginCountries, ProductionCountries: media.ProductionCountries,
		SeasonEpisode: meta.SeasonEpisode, ResourceType: meta.ResourceType,
		ResourcePix: meta.ResourcePix, VideoEncode: meta.VideoEncode,
	}
	if media.OriginalLanguage == "" {
		info.OriginalLanguages = nil
	}
	if meta.BeginSeason != nil {
		info.BeginSeason = *meta.BeginSeason
		info.HasBeginSeason = true
	}
	return info
}

func buildLocalMediaRecognitionRaw(result MediaRecognitionResult) map[string]any {
	media := result.MediaInfo
	meta := result.MetaInfo
	mediaMap := map[string]any{
		"source": media.Source, "media_type": media.MediaType, "title": media.Title,
		"original_title": media.OriginalTitle, "year": media.Year, "title_year": media.TitleYear,
		"tmdb_id": media.TMDBID, "category": media.Category, "poster_path": media.PosterPath,
		"backdrop_path": media.BackdropPath, "vote_average": media.Rating,
		"genres": media.Genres, "genre_ids": media.GenreIDs, "overview": media.Overview,
		"original_language": media.OriginalLanguage, "origin_country": media.OriginCountries,
		"production_countries": media.ProductionCountries,
	}
	metaMap := map[string]any{
		"org_string": meta.ProcessedInput, "title": meta.OriginalInput, "name": meta.Name,
		"year": meta.Year, "type": meta.MediaType, "tmdbid": meta.TMDBID,
		"season_episode": meta.SeasonEpisode, "resource_type": meta.ResourceType,
		"resource_pix": meta.ResourcePix, "video_encode": meta.VideoEncode,
		"video_bit": meta.VideoBit, "audio_encode": meta.AudioEncode,
		"resource_effect": meta.ResourceEffect, "resource_team": meta.ResourceTeam,
		"apply_words": meta.AppliedWords,
	}
	if meta.BeginSeason != nil {
		metaMap["begin_season"] = *meta.BeginSeason
	}
	if meta.EndSeason != nil {
		metaMap["end_season"] = *meta.EndSeason
	}
	if meta.BeginEpisode != nil {
		metaMap["begin_episode"] = *meta.BeginEpisode
	}
	if meta.EndEpisode != nil {
		metaMap["end_episode"] = *meta.EndEpisode
	}
	return map[string]any{
		"engine": "local", "tmdb_status": result.TMDBStatus,
		"media_info": mediaMap, "meta_info": metaMap,
	}
}

func sortMediaRecognitionCandidates(candidates []MediaRecognitionCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Rating > candidates[j].Rating
		}
		return candidates[i].Score > candidates[j].Score
	})
}

func normalizeLocalTitleForMatch(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func mediaRecognitionSearchTitles(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	han := strings.Builder{}
	latin := strings.Builder{}
	for _, char := range value {
		switch {
		case unicode.Is(unicode.Han, char):
			han.WriteRune(char)
			latin.WriteRune(' ')
		case unicode.IsLetter(char) || unicode.IsDigit(char) || unicode.IsSpace(char):
			latin.WriteRune(char)
		default:
			latin.WriteRune(' ')
		}
	}
	candidates := []string{value, strings.TrimSpace(han.String()), localSpaceRE.ReplaceAllString(strings.TrimSpace(latin.String()), " ")}
	seen := make(map[string]struct{}, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	return result
}
