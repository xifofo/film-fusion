package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	MediaRecognitionShadowMatched           = "matched"
	MediaRecognitionShadowDifferent         = "different"
	MediaRecognitionShadowMoviePilotError   = "moviepilot_error"
	MediaRecognitionShadowLocalError        = "local_error"
	MediaRecognitionShadowLocalUnavailable  = "local_unavailable"
	mediaRecognitionShadowRawKey            = "shadow_comparison"
	mediaTransferShadowRawKey               = "transfer_shadow_comparison"
	mediaRecognitionShadowPrimaryEngine     = "moviepilot"
	mediaRecognitionShadowEmptyWordsComment = "# FilmFusion empty word list"
)

// MediaTransferShadowComparison 记录同一输入的 MP2 与本地命名结果。
// 它只是观测数据，不会改变影子模式返回的 MP2 结果。
type MediaTransferShadowComparison struct {
	Status          string         `json:"status"`
	Matched         bool           `json:"matched"`
	MoviePilot      string         `json:"moviepilot,omitempty"`
	Local           string         `json:"local,omitempty"`
	MoviePilotError string         `json:"moviepilot_error,omitempty"`
	LocalError      string         `json:"local_error,omitempty"`
	LocalRaw        map[string]any `json:"local_raw,omitempty"`
}

// MediaRecognitionShadowSnapshot is the stable, engine-neutral subset used to
// compare MoviePilot's authoritative result with FilmFusion's local shadow.
// Artwork and descriptive TMDB metadata are deliberately omitted: the shadow
// is measuring recognition identity and release parsing, not provider payload
// completeness.
type MediaRecognitionShadowSnapshot struct {
	Engine        string `json:"engine"`
	MediaType     string `json:"media_type,omitempty"`
	Title         string `json:"title,omitempty"`
	OriginalTitle string `json:"original_title,omitempty"`
	Year          string `json:"year,omitempty"`
	TitleYear     string `json:"title_year,omitempty"`
	TMDBID        string `json:"tmdb_id,omitempty"`
	Category      string `json:"category,omitempty"`
	SeasonEpisode string `json:"season_episode,omitempty"`
	ResourceType  string `json:"resource_type,omitempty"`
	ResourcePix   string `json:"resource_pix,omitempty"`
	VideoEncode   string `json:"video_encode,omitempty"`
	BeginSeason   *int   `json:"begin_season,omitempty"`
}

// MediaRecognitionShadowDifference explains one mismatched field without
// requiring callers to understand either engine's raw response shape.
type MediaRecognitionShadowDifference struct {
	Field      string `json:"field"`
	Label      string `json:"label"`
	MoviePilot string `json:"moviepilot"`
	Local      string `json:"local"`
}

// MediaRecognitionShadowComparison is attached to recognition raw data and is
// also returned by the comparison workbench API. MoviePilot remains the
// authoritative result whenever it succeeds; local errors never replace it.
type MediaRecognitionShadowComparison struct {
	Medianame       *MedianameRecognition              `json:"medianame,omitempty"`
	Status          string                             `json:"status"`
	Matched         bool                               `json:"matched"`
	MoviePilot      *MediaRecognitionShadowSnapshot    `json:"moviepilot,omitempty"`
	Local           *MediaRecognitionShadowSnapshot    `json:"local,omitempty"`
	Differences     []MediaRecognitionShadowDifference `json:"differences"`
	MoviePilotError string                             `json:"moviepilot_error,omitempty"`
	LocalError      string                             `json:"local_error,omitempty"`
	localInfo       *MoviePilotMediaInfo
	localRaw        map[string]any
}

// MediaRecognitionShadowTestResult exposes both sides of a manual comparison.
// The rich local result is retained so the UI can still inspect recognition
// words, parsed release metadata and TMDB candidates.
type MediaRecognitionShadowTestResult struct {
	PrimaryEngine string                           `json:"primary_engine"`
	MoviePilotRaw map[string]any                   `json:"moviepilot_raw,omitempty"`
	Local         *MediaRecognitionResult          `json:"local,omitempty"`
	Comparison    MediaRecognitionShadowComparison `json:"comparison"`
}

type mediaRecognitionShadowRun struct {
	comparison  MediaRecognitionShadowComparison
	localResult *MediaRecognitionResult
	localInfo   MoviePilotMediaInfo
	localErr    error
}

// CompareRecognition runs MoviePilot first and FilmFusion second using the
// same input and draft options. It is intended for the media-recognition
// workbench; operational calls use finishRecognitionWithShadow below.
func (s *MoviePilotService) CompareRecognition(ctx context.Context, input string, options MediaRecognitionOptions) (MediaRecognitionShadowTestResult, error) {
	result := MediaRecognitionShadowTestResult{PrimaryEngine: mediaRecognitionShadowPrimaryEngine}
	input = strings.TrimSpace(input)
	if input == "" {
		return result, errors.New("识别输入不能为空")
	}
	if len(input) > maxMediaRecognitionInput {
		return result, fmt.Errorf("识别输入不能超过 %d KiB", maxMediaRecognitionInput>>10)
	}
	mode := normalizeMediaRecognitionMode(options.Mode)
	options.Mode = mode

	moviePilotInfo, moviePilotRaw, moviePilotErr := s.recognizeWithMoviePilotOptions(input, options)
	result.MoviePilotRaw = moviePilotRaw
	var moviePilotInfoPtr *MoviePilotMediaInfo
	if moviePilotErr == nil {
		s.clearLocalRecognitionFallback()
		moviePilotInfoPtr = &moviePilotInfo
	}

	shadow := s.runLocalRecognitionShadow(ctx, input, mode, options, moviePilotInfoPtr, moviePilotErr)
	result.Local = shadow.localResult
	result.Comparison = shadow.comparison
	return result, nil
}

// finishRecognitionWithShadow always runs the local engine after the MP2
// attempt, records the comparison, and returns only the MP2 outcome.
func (s *MoviePilotService) finishRecognitionWithShadow(
	input string,
	mode string,
	moviePilotInfo MoviePilotMediaInfo,
	moviePilotRaw map[string]any,
	moviePilotErr error,
) (MoviePilotMediaInfo, map[string]any, error) {
	var moviePilotInfoPtr *MoviePilotMediaInfo
	if moviePilotErr == nil {
		moviePilotInfoPtr = &moviePilotInfo
	}
	shadow := s.runLocalRecognitionShadow(
		context.Background(),
		input,
		mode,
		MediaRecognitionOptions{Mode: mode, LookupTMDB: true},
		moviePilotInfoPtr,
		moviePilotErr,
	)

	if moviePilotErr == nil {
		return moviePilotInfo, attachMediaRecognitionShadow(moviePilotRaw, shadow.comparison), nil
	}
	moviePilotRaw = attachMediaRecognitionShadow(moviePilotRaw, shadow.comparison)
	return MoviePilotMediaInfo{}, moviePilotRaw, moviePilotErr
}

func (s *MoviePilotService) runLocalRecognitionShadow(
	ctx context.Context,
	input string,
	mode string,
	options MediaRecognitionOptions,
	moviePilotInfo *MoviePilotMediaInfo,
	moviePilotErr error,
) mediaRecognitionShadowRun {
	run := mediaRecognitionShadowRun{}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.local == nil {
		run.localErr = errors.New("FilmFusion 本地识别未初始化")
		run.comparison = buildMediaRecognitionShadowComparison(moviePilotInfo, moviePilotErr, nil, run.localErr, true)
		s.logMediaRecognitionShadow(input, mode, run.comparison)
		return run
	}

	options.Mode = mode
	recognized := s.local.RecognizeMedianame(ctx, input, options)
	localResult := adaptMedianameRecognition(recognized)
	localErr := medianameCompatible(recognized)
	run.localResult = &localResult
	if localErr == nil && strings.TrimSpace(localResult.MediaInfo.Title) == "" {
		localErr = errors.New("本地识别未得到媒体标题")
	}
	if localErr == nil {
		run.localResult = &localResult
		run.localInfo = moviePilotInfoFromLocalResult(localResult)
	}
	run.localErr = localErr
	var localInfoPtr *MoviePilotMediaInfo
	if localErr == nil {
		localInfoPtr = &run.localInfo
	}
	run.comparison = buildMediaRecognitionShadowComparison(moviePilotInfo, moviePilotErr, localInfoPtr, localErr, false)
	run.comparison.Medianame = &recognized
	if localInfoPtr != nil {
		localInfoCopy := *localInfoPtr
		run.comparison.localInfo = &localInfoCopy
	}
	if run.localResult != nil {
		run.comparison.localRaw = run.localResult.Raw
	}
	s.logMediaRecognitionShadow(input, mode, run.comparison)
	return run
}

func (s *MoviePilotService) recognizeWithMoviePilotOptions(input string, options MediaRecognitionOptions) (MoviePilotMediaInfo, map[string]any, error) {
	mode := normalizeMediaRecognitionMode(options.Mode)
	var info MoviePilotMediaInfo
	var raw map[string]any
	var err error

	if options.UseProvidedWords {
		words := NormalizeMediaRecognitionWords(options.Words)
		if validateErr := ValidateMediaRecognitionWords(words); validateErr != nil {
			return MoviePilotMediaInfo{}, nil, validateErr
		}
		customWords := strings.Join(words, "\n")
		if customWords == "" {
			customWords = mediaRecognitionShadowEmptyWordsComment
		}
		values := url.Values{}
		values.Set("title", input)
		values.Set("custom_words", customWords)
		info, raw, err = s.recognizeMedia("/api/v1/media/recognize", values)
	} else if mode == MediaRecognitionModeTitle {
		info, raw, err = s.recognizeTitleWithMoviePilot(input)
	} else {
		info, raw, err = s.recognizeFileWithMoviePilot(input)
	}
	if err != nil {
		return MoviePilotMediaInfo{}, raw, err
	}
	if strings.TrimSpace(info.Title) == "" {
		return MoviePilotMediaInfo{}, raw, errors.New("MoviePilot 未识别到媒体信息")
	}
	if options.UseProvidedCategory {
		if s.local == nil {
			return MoviePilotMediaInfo{}, raw, errors.New("FilmFusion 本地识别未初始化")
		}
		config, configErr := s.local.moviePilotCategoryConfig(options.CategoryYAML, true)
		if configErr != nil {
			return MoviePilotMediaInfo{}, raw, fmt.Errorf("分类配置无效: %w", configErr)
		}
		info.Category = SelectMoviePilotCategory(info.MediaType, info, config)
	}
	return info, raw, nil
}

func (s *MoviePilotService) recognizeTitleWithMoviePilot(title string) (MoviePilotMediaInfo, map[string]any, error) {
	values := url.Values{}
	values.Set("title", title)
	if customWords, configured := s.localRecognitionWords(); configured {
		values.Set("custom_words", customWords)
	}
	info, raw, err := s.recognizeMedia("/api/v1/media/recognize", values)
	if err != nil {
		return MoviePilotMediaInfo{}, raw, err
	}
	if strings.TrimSpace(info.Title) == "" {
		return MoviePilotMediaInfo{}, raw, errors.New("MoviePilot 未识别到媒体信息")
	}
	return info, raw, nil
}

func buildMediaRecognitionShadowComparison(
	moviePilotInfo *MoviePilotMediaInfo,
	moviePilotErr error,
	localInfo *MoviePilotMediaInfo,
	localErr error,
	localUnavailable bool,
) MediaRecognitionShadowComparison {
	comparison := MediaRecognitionShadowComparison{
		Differences: make([]MediaRecognitionShadowDifference, 0),
	}
	if moviePilotInfo != nil {
		comparison.MoviePilot = mediaRecognitionShadowSnapshot("moviepilot", *moviePilotInfo)
	}
	if localInfo != nil {
		comparison.Local = mediaRecognitionShadowSnapshot("medianame", *localInfo)
	}
	if moviePilotErr != nil {
		comparison.Status = MediaRecognitionShadowMoviePilotError
		comparison.MoviePilotError = moviePilotErr.Error()
		if localErr != nil {
			comparison.LocalError = localErr.Error()
		}
		return comparison
	}
	if localErr != nil {
		comparison.Status = MediaRecognitionShadowLocalError
		if localUnavailable {
			comparison.Status = MediaRecognitionShadowLocalUnavailable
		}
		comparison.LocalError = localErr.Error()
		return comparison
	}

	fields := []struct {
		name  string
		label string
		mp    string
		local string
	}{
		{name: "media_type", label: "媒体类型", mp: comparison.MoviePilot.MediaType, local: comparison.Local.MediaType},
		{name: "title", label: "标题", mp: comparison.MoviePilot.Title, local: comparison.Local.Title},
		{name: "original_title", label: "原始标题", mp: comparison.MoviePilot.OriginalTitle, local: comparison.Local.OriginalTitle},
		{name: "year", label: "年份", mp: comparison.MoviePilot.Year, local: comparison.Local.Year},
		{name: "title_year", label: "标题年份", mp: comparison.MoviePilot.TitleYear, local: comparison.Local.TitleYear},
		{name: "tmdb_id", label: "TMDB ID", mp: comparison.MoviePilot.TMDBID, local: comparison.Local.TMDBID},
		{name: "category", label: "媒体分类", mp: comparison.MoviePilot.Category, local: comparison.Local.Category},
		{name: "season_episode", label: "季集", mp: comparison.MoviePilot.SeasonEpisode, local: comparison.Local.SeasonEpisode},
		{name: "resource_type", label: "资源类型", mp: comparison.MoviePilot.ResourceType, local: comparison.Local.ResourceType},
		{name: "resource_pix", label: "分辨率", mp: comparison.MoviePilot.ResourcePix, local: comparison.Local.ResourcePix},
		{name: "video_encode", label: "视频编码", mp: comparison.MoviePilot.VideoEncode, local: comparison.Local.VideoEncode},
		{name: "begin_season", label: "起始季", mp: shadowSeasonValue(comparison.MoviePilot.BeginSeason), local: shadowSeasonValue(comparison.Local.BeginSeason)},
	}
	for _, field := range fields {
		if equalMediaRecognitionShadowValue(field.name, field.mp, field.local) {
			continue
		}
		comparison.Differences = append(comparison.Differences, MediaRecognitionShadowDifference{
			Field: field.name, Label: field.label, MoviePilot: field.mp, Local: field.local,
		})
	}
	comparison.Matched = len(comparison.Differences) == 0
	comparison.Status = MediaRecognitionShadowDifferent
	if comparison.Matched {
		comparison.Status = MediaRecognitionShadowMatched
	}
	return comparison
}

func mediaRecognitionShadowSnapshot(engine string, info MoviePilotMediaInfo) *MediaRecognitionShadowSnapshot {
	mediaType := normalizeLocalMediaType(info.MediaType)
	videoEncode := normalizeLocalVideoCodec(info.VideoEncode)
	snapshot := &MediaRecognitionShadowSnapshot{
		Engine: engine, MediaType: mediaType, Title: info.Title,
		OriginalTitle: info.OriginalTitle, Year: info.Year, TitleYear: info.TitleYear,
		TMDBID: info.TmdbID, Category: mediaRecognitionCategoryPath(mediaType, info.Category), SeasonEpisode: info.SeasonEpisode,
		ResourceType: normalizeLocalResourceType(info.ResourceType), ResourcePix: normalizeLocalResolution(info.ResourcePix), VideoEncode: videoEncode,
	}
	if info.HasBeginSeason && mediaType != "movie" {
		season := info.BeginSeason
		snapshot.BeginSeason = &season
	}
	return snapshot
}

// mediaRecognitionCategoryPath 使用 MP2 的“电影/分类”与“电视剧/分类”语义
// 展示分类，同时保留 MoviePilotMediaInfo.Category 的叶子值供整理目录使用。
func mediaRecognitionCategoryPath(mediaType, category string) string {
	category = strings.Trim(strings.TrimSpace(strings.ReplaceAll(category, "\\", "/")), "/")
	if category == "" {
		return ""
	}
	group := normalizeLocalMediaType(mediaType)
	root := mediaRecognitionCategoryGroupLabel(group)
	if root != "电影" && root != "电视剧" {
		root = ""
	}

	parts := strings.Split(category, "/")
	if len(parts) > 1 {
		if categoryGroup, ok := canonicalMediaRecognitionCategoryGroup(parts[0]); ok {
			root = mediaRecognitionCategoryGroupLabel(categoryGroup)
			parts = parts[1:]
		}
	}
	leaf := strings.Trim(strings.Join(parts, "/"), "/")
	if root == "" || leaf == "" {
		return firstNonEmptyMoviePilotString(root, leaf)
	}
	return root + "/" + leaf
}

func attachMediaRecognitionShadow(raw map[string]any, comparison MediaRecognitionShadowComparison) map[string]any {
	if raw == nil {
		raw = map[string]any{}
	}
	raw[mediaRecognitionShadowRawKey] = comparison
	return raw
}

// MediaRecognitionShadowFromRaw 读取一次业务识别调用附带的影子结果。
// localInfo 与 localRaw 只供同进程中的整理预览计算本地目标路径使用，
// 不会随着 comparison 序列化到 API 响应。
func MediaRecognitionShadowFromRaw(raw map[string]any) (
	comparison MediaRecognitionShadowComparison,
	localInfo *MoviePilotMediaInfo,
	localRaw map[string]any,
	ok bool,
) {
	if raw == nil {
		return comparison, nil, nil, false
	}
	value, exists := raw[mediaRecognitionShadowRawKey]
	if !exists {
		return comparison, nil, nil, false
	}
	switch typed := value.(type) {
	case MediaRecognitionShadowComparison:
		comparison = typed
	case *MediaRecognitionShadowComparison:
		if typed == nil {
			return comparison, nil, nil, false
		}
		comparison = *typed
	default:
		return comparison, nil, nil, false
	}
	if comparison.localInfo != nil {
		localInfoCopy := *comparison.localInfo
		localInfo = &localInfoCopy
	}
	return comparison, localInfo, comparison.localRaw, true
}

// MediaTransferShadowFromRaw 读取一次业务命名调用附带的影子结果。
func MediaTransferShadowFromRaw(raw map[string]any) (MediaTransferShadowComparison, bool) {
	if raw == nil {
		return MediaTransferShadowComparison{}, false
	}
	value, exists := raw[mediaTransferShadowRawKey]
	if !exists {
		return MediaTransferShadowComparison{}, false
	}
	switch typed := value.(type) {
	case MediaTransferShadowComparison:
		return typed, true
	case *MediaTransferShadowComparison:
		if typed != nil {
			return *typed, true
		}
	}
	return MediaTransferShadowComparison{}, false
}

// finishTransferNameWithShadow 在 MP2 命名完成后运行本地命名影子，
// 并无条件保留 MP2 的返回值与错误。
func (s *MoviePilotService) finishTransferNameWithShadow(
	input string,
	moviePilotName string,
	moviePilotRaw map[string]any,
	moviePilotErr error,
) (string, map[string]any, error) {
	comparison := MediaTransferShadowComparison{
		Status:     MediaRecognitionShadowDifferent,
		MoviePilot: strings.TrimSpace(moviePilotName),
	}
	if moviePilotErr != nil {
		comparison.Status = MediaRecognitionShadowMoviePilotError
		comparison.MoviePilotError = moviePilotErr.Error()
	}
	if s == nil || s.local == nil {
		if moviePilotErr == nil {
			comparison.Status = MediaRecognitionShadowLocalUnavailable
		}
		comparison.LocalError = "FilmFusion 本地识别未初始化"
	} else {
		localName, localRaw, localErr := s.TransferNameWithSource(input, "", MediaRecognitionSourceMedianame)
		comparison.Local = strings.TrimSpace(localName)
		comparison.LocalRaw = localRaw
		if localErr != nil {
			comparison.LocalError = localErr.Error()
			if moviePilotErr == nil {
				comparison.Status = MediaRecognitionShadowLocalError
			}
		} else if moviePilotErr == nil && comparison.MoviePilot == comparison.Local {
			comparison.Status = MediaRecognitionShadowMatched
			comparison.Matched = true
		}
	}
	if moviePilotRaw == nil {
		moviePilotRaw = map[string]any{}
	}
	moviePilotRaw[mediaTransferShadowRawKey] = comparison
	s.logMediaTransferShadow(input, comparison)
	return moviePilotName, moviePilotRaw, moviePilotErr
}

func (s *MoviePilotService) logMediaTransferShadow(input string, comparison MediaTransferShadowComparison) {
	if s == nil || s.logger == nil {
		return
	}
	switch comparison.Status {
	case MediaRecognitionShadowMatched:
		s.logger.Infof("[media-transfer-shadow] MP2 与本地命名一致 input=%q name=%q", input, comparison.MoviePilot)
	case MediaRecognitionShadowDifferent:
		s.logger.Warnf(
			"[media-transfer-shadow] MP2 与本地命名不一致 input=%q moviepilot=%q local=%q",
			input, comparison.MoviePilot, comparison.Local,
		)
	default:
		s.logger.Warnf(
			"[media-transfer-shadow] 命名影子对比未完成 input=%q status=%s moviepilot_error=%q local_error=%q",
			input, comparison.Status, comparison.MoviePilotError, comparison.LocalError,
		)
	}
}

func equalMediaRecognitionShadowValue(field, left, right string) bool {
	normalize := func(value string) string {
		value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if field == "season_episode" {
			value = strings.ReplaceAll(value, " ", "")
		}
		return strings.ToLower(value)
	}
	return normalize(left) == normalize(right)
}

func shadowSeasonValue(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func (s *MoviePilotService) logMediaRecognitionShadow(input, mode string, comparison MediaRecognitionShadowComparison) {
	if s == nil || s.logger == nil {
		return
	}
	diffFields := make([]string, 0, len(comparison.Differences))
	for _, difference := range comparison.Differences {
		diffFields = append(diffFields, difference.Field)
	}
	switch comparison.Status {
	case MediaRecognitionShadowMatched:
		s.logger.Debugf(
			"[media-recognition-shadow] MP2 与本地影子一致 mode=%s input=%q",
			mode,
			input,
		)
	case MediaRecognitionShadowDifferent:
		s.logger.Warnf(
			"[media-recognition-shadow] MP2 与本地影子不一致 mode=%s input=%q fields=%s moviepilot=%+v local=%+v",
			mode,
			input,
			strings.Join(diffFields, ","),
			comparison.MoviePilot,
			comparison.Local,
		)
	default:
		s.logger.Warnf(
			"[media-recognition-shadow] 影子对比未完成 mode=%s input=%q status=%s moviepilot_error=%q local_error=%q",
			mode,
			input,
			comparison.Status,
			comparison.MoviePilotError,
			comparison.LocalError,
		)
	}
}
