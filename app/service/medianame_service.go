package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/model"
	"github.com/xifofo/medianame"
	"github.com/xifofo/medianame/category"
	"github.com/xifofo/medianame/rename"
	mtmdb "github.com/xifofo/medianame/tmdb"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MedianameVersion = "v0.1.2"
const medianameConfigKey = "media_recognition.medianame.v1"

// Stored independently of MP2. Defaults are inserted once; upgrades never rewrite a saved row.
type MedianameConfig struct {
	CategoryYAML  string `json:"category_yaml"`
	MovieTemplate string `json:"movie_template"`
	TVTemplate    string `json:"tv_template"`
	Version       string `json:"version"`
}

func DefaultMedianameConfig() MedianameConfig {
	return MedianameConfig{category.DefaultYAML, rename.DefaultMovieTemplate, rename.DefaultTVTemplate, MedianameVersion}
}
func (s *MediaRecognitionService) LoadMedianameConfig() (MedianameConfig, error) {
	if s != nil && s.frozen != nil {
		return s.frozen.Medianame, s.frozen.medianameError
	}
	if s == nil || s.db == nil {
		return MedianameConfig{}, errors.New("数据库未初始化")
	}
	var row model.SystemConfig
	err := s.db.Where("config_key = ?", medianameConfigKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		value, _ := json.Marshal(DefaultMedianameConfig())
		row = model.SystemConfig{ConfigKey: medianameConfigKey, ConfigValue: string(value), ConfigType: model.TypeJSON, Category: model.CategoryMediaRecognition, Description: "medianame 分类与 Go 命名模板", IsSystem: true, IsVisible: true, SortOrder: 40}
		if err = s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "config_key"}}, DoNothing: true}).Create(&row).Error; err != nil {
			return MedianameConfig{}, err
		}
		err = s.db.Where("config_key = ?", medianameConfigKey).First(&row).Error
	}
	if err != nil {
		return MedianameConfig{}, err
	}
	var cfg MedianameConfig
	if err = json.Unmarshal([]byte(row.ConfigValue), &cfg); err != nil {
		return cfg, err
	}
	return cfg, ValidateMedianameConfig(cfg)
}
func (s *MediaRecognitionService) SaveMedianameConfig(cfg MedianameConfig) (MedianameConfig, error) {
	if err := ValidateMedianameConfig(cfg); err != nil {
		return cfg, err
	}
	if s == nil || s.db == nil {
		return cfg, errors.New("数据库未初始化")
	}
	cfg.Version = MedianameVersion
	data, _ := json.Marshal(cfg)
	row := model.SystemConfig{ConfigKey: medianameConfigKey, ConfigValue: string(data), ConfigType: model.TypeJSON, Category: model.CategoryMediaRecognition, Description: "medianame 分类与 Go 命名模板", IsSystem: true, IsVisible: true, SortOrder: 40}
	err := s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "config_key"}}, DoUpdates: clause.AssignmentColumns([]string{"config_value", "updated_at", "deleted_at"})}).Create(&row).Error
	return cfg, err
}
func ValidateMedianameConfig(cfg MedianameConfig) error {
	if len(cfg.CategoryYAML) > maxMediaRecognitionCategoryYAML {
		return errors.New("分类配置超过 128 KiB")
	}
	if _, err := category.Parse([]byte(cfg.CategoryYAML)); err != nil {
		return fmt.Errorf("medianame 分类: %w", err)
	}
	if _, err := medianameCategoryNames(cfg.CategoryYAML); err != nil {
		return fmt.Errorf("medianame 分类目录: %w", err)
	}
	for _, entry := range []struct{ kind, source string }{{"movie", cfg.MovieTemplate}, {"tv", cfg.TVTemplate}} {
		if _, err := PreviewMedianameTemplate(entry.kind, entry.source, nil); err != nil {
			return fmt.Errorf("medianame %s 模板: %w", entry.kind, err)
		}
	}
	return nil
}

// Template owns the media/season folders. FilmFusion alone prepends library root and category.
// category is intentionally unavailable to prevent templates from bypassing the classification switch.
func PreviewMedianameTemplate(kind, source string, sample map[string]any) (MediaRenamePreviewResult, error) {
	ctx := MedianameSampleContext(kind)
	for k, v := range sample {
		if _, ok := ctx[k]; !ok {
			return MediaRenamePreviewResult{}, fmt.Errorf("未知变量 %s", k)
		}
		ctx[k] = v
	}
	result, err := renderMedianame(source, ctx)
	if err != nil {
		return MediaRenamePreviewResult{}, err
	}
	return MediaRenamePreviewResult{Path: result.Path, Template: source, Variables: ctx, Warnings: []string{}}, nil
}
func MedianameSampleContext(kind string) rename.Context {
	input := "Example.2024.1080p.WEB-DL.H264.mkv"
	if kind == "tv" {
		input = "Example.2024.S00E01.1080p.WEB-DL.H264.mkv"
	}
	info := medianame.Parse(input)
	media := mtmdb.MediaInfo{Title: "示例", EnglishTitle: "Example", OriginalTitle: "Example", Year: 2024, Type: info.Type, TMDBID: "123"}
	ctx := media.RenameContext(info)
	delete(ctx, "category")
	ctx["episode_title"] = "示例集"
	ctx["episode_date"] = "2024-01-01"
	ctx["season_year"] = "2024"
	ctx["total_episodes"] = 12
	return ctx
}
func renderMedianame(source string, ctx rename.Context) (*rename.Result, error) {
	if len(source) > maxMediaRenameTemplateBytes {
		return nil, errors.New("模板超过 16 KiB")
	}
	// Do not permit category through conditional/index expressions either.
	if strings.Contains(source, "category") {
		return nil, errors.New("分类目录由 FilmFusion 按开关统一拼接，模板不能引用 category")
	}
	result, err := rename.Render(source, ctx)
	if err != nil {
		return nil, err
	}
	if len(result.Path) > maxMediaRenameOutputBytes {
		return nil, errors.New("路径超过 4 KiB")
	}
	for _, segment := range strings.Split(result.Path, "/") {
		if len(segment) > 255 {
			return nil, errors.New("路径段超过 255 字节")
		}
	}
	return result, nil
}

// Each immutable client owns a bounded, shared response cache. A configuration change
// replaces the client, while in-flight tasks retain their original client and credentials.
type medianameTMDBClient struct {
	key         string
	client      *mtmdb.Client
	supplements *TMDBService
	err         error
}

func fingerprint(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func tmdbAPIBase(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		base = "https://api.themoviedb.org"
	}
	if !strings.HasSuffix(base, "/3") {
		base += "/3"
	}
	return base
}
func (s *TMDBService) medianameAPI() *medianameTMDBClient {
	if s == nil || s.cfg == nil {
		return &medianameTMDBClient{err: errors.New("TMDB 未配置")}
	}
	cfg := s.cfg.TMDB
	key := fingerprint(cfg)
	s.medianameMu.Lock()
	defer s.medianameMu.Unlock()
	if s.medianameClient != nil && s.medianameClient.key == key {
		return s.medianameClient
	}
	entry := &medianameTMDBClient{key: key}
	if !cfg.Enabled {
		entry.err = errors.New("TMDB 未启用")
	} else {
		timeout := cfg.TimeoutSeconds
		if timeout <= 0 {
			timeout = 10
		}
		ttl := cfg.CacheMinutes
		if ttl <= 0 {
			ttl = 60
		}
		entry.client, entry.err = mtmdb.NewClient(mtmdb.Config{BaseURL: tmdbAPIBase(cfg.BaseURL), APIKey: cfg.APIKey, Token: cfg.AccessToken, HTTPClient: &http.Client{Timeout: time.Duration(timeout) * time.Second, Transport: s.client.Transport}, Cache: mtmdb.CacheConfig{TTL: time.Duration(ttl) * time.Minute}})
		fixed := *s.cfg
		fixed.TMDB = cfg
		fixed.TMDB.BaseURL = strings.TrimSuffix(tmdbAPIBase(cfg.BaseURL), "/3")
		entry.supplements = NewTMDBService(&fixed, s.logger)
		entry.supplements.client = &http.Client{Timeout: time.Duration(timeout) * time.Second, Transport: s.client.Transport}
	}
	s.medianameClient = entry
	return entry
}

type MedianameRecognition struct {
	Engine         string                     `json:"engine"`
	PackageVersion string                     `json:"package_version"`
	WordResult     MediaRecognitionWordResult `json:"word_result"`
	Result         *mtmdb.Result              `json:"result"`
	Error          string                     `json:"error,omitempty"`
}

func (s *MediaRecognitionService) RecognizeMedianame(ctx context.Context, input string, options MediaRecognitionOptions) MedianameRecognition {
	out := MedianameRecognition{Engine: MediaRecognitionSourceMedianame, PackageVersion: MedianameVersion}
	fail := func(err error) MedianameRecognition {
		out.Error = err.Error()
		if out.Result != nil {
			out.Result.Status = mtmdb.StatusRequestError
		}
		if out.Result == nil {
			out.Result = &mtmdb.Result{Status: mtmdb.StatusRequestError}
		}
		return out
	}
	if strings.TrimSpace(input) == "" || len(input) > maxMediaRecognitionInput {
		return fail(errors.New("识别输入为空或超过 16 KiB"))
	}
	words := options.Words
	if !options.UseProvidedWords {
		var err error
		words, _, err = s.LoadWords()
		if err != nil {
			return fail(err)
		}
	}
	if err := ValidateMediaRecognitionWords(words); err != nil {
		return fail(err)
	}
	out.WordResult = ApplyMediaRecognitionWords(input, words)
	for _, step := range out.WordResult.Steps {
		if step.Error != "" {
			return fail(errors.New(step.Error))
		}
	}
	var parsed medianame.Info
	if normalizeMediaRecognitionMode(options.Mode) == MediaRecognitionModeFile {
		parsed = medianame.ParsePath(out.WordResult.Processed)
	} else {
		parsed = medianame.Parse(out.WordResult.Processed)
	}
	out.Result = &mtmdb.Result{Status: mtmdb.StatusNotIdentified, MetaInfo: parsed}
	if !options.LookupTMDB {
		return out
	}
	api := s.medianameClient
	if api == nil {
		api = s.tmdb.medianameAPI()
	}
	if api.err != nil {
		out.Result.Status = mtmdb.StatusRequestError
		return fail(api.err)
	}
	result, err := api.client.RecognizeInfo(ctx, parsed)
	out.Result = result
	if err != nil {
		return fail(err)
	}
	cfg, err := s.LoadMedianameConfig()
	if err != nil {
		return fail(err)
	}
	policy, err := category.Parse([]byte(cfg.CategoryYAML))
	if err != nil {
		return fail(err)
	}
	// Reclassify cached details against this task's policy without invalidating TMDB response caches.
	for _, candidate := range result.Candidates {
		if candidate.MediaInfo != nil {
			name, e := candidate.MediaInfo.Classify(policy)
			if e != nil {
				return fail(e)
			}
			candidate.MediaInfo.Category = name
		}
	}
	if result.Status == mtmdb.StatusCompatible && result.MediaInfo != nil {
		name, e := result.MediaInfo.Classify(policy)
		if e != nil {
			return fail(e)
		}
		result.MediaInfo.Category = name
	}
	return out
}
func medianameCompatible(r MedianameRecognition) error {
	if r.Error != "" {
		return errors.New(r.Error)
	}
	if r.Result == nil {
		return errors.New("medianame 未返回识别结果")
	}
	if r.Result.Status != mtmdb.StatusCompatible || r.Result.MediaInfo == nil {
		return fmt.Errorf("medianame: %s，需重新核对后预览", r.Result.Status)
	}
	return nil
}
func medianameInfo(r MedianameRecognition) MoviePilotMediaInfo {
	info := MoviePilotMediaInfo{}
	if medianameCompatible(r) != nil {
		return info
	}
	m, p := r.Result.MediaInfo, r.Result.MetaInfo
	info.MediaType = string(m.Type)
	info.Title = m.Title
	info.OriginalTitle = m.OriginalTitle
	info.Year = strconv.Itoa(m.Year)
	info.TitleYear = fmt.Sprintf("%s (%d)", m.Title, m.Year)
	info.TmdbID = m.TMDBID
	info.Category = m.Category
	info.PosterPath = m.PosterPath
	info.BackdropPath = m.BackdropPath
	info.Rating = m.VoteAverage
	info.ResourceType = p.Source
	info.ResourcePix = p.Resolution
	info.VideoEncode = p.VideoCodec
	info.SeasonEpisode = fmt.Sprint(m.RenameContext(p)["season_episode"])
	if p.Season != nil {
		info.BeginSeason = *p.Season
		info.HasBeginSeason = true
	}
	return info
}
func (s *MediaRecognitionService) MedianameTarget(ctx context.Context, r MedianameRecognition, root string, classify bool, override string) (string, rename.Context, error) {
	if err := medianameCompatible(r); err != nil {
		return "", nil, err
	}
	cfg, err := s.LoadMedianameConfig()
	if err != nil {
		return "", nil, err
	}
	m, p := *r.Result.MediaInfo, r.Result.MetaInfo
	if override != "" {
		m.Category = override
	}
	format := cfg.MovieTemplate
	isDefault := format == rename.DefaultMovieTemplate
	if m.Type == medianame.TypeTV {
		format = cfg.TVTemplate
		isDefault = format == rename.DefaultTVTemplate
	}
	if isDefault {
		if _, err := m.Rename(p, ""); err != nil {
			return "", nil, err
		}
	}
	vars := m.RenameContext(p)
	delete(vars, "category")
	if m.Type == medianame.TypeTV && p.Season != nil {
		api := s.medianameClient
		if api == nil {
			api = s.tmdb.medianameAPI()
		}
		needsEpisode := strings.Contains(format, "episode_title") || strings.Contains(format, "episode_date")
		if needsEpisode && (p.Episode == nil || p.EpisodeEnd != nil || len(p.Episodes) > 1) {
			return "", vars, errors.New("单集标题或日期模板不能用于多集或未确定集号的文件")
		}
		needsYear := strings.Contains(format, "season_year") && fmt.Sprint(vars["season_year"]) == ""
		if needsEpisode || needsYear {
			if api.err != nil {
				return "", vars, api.err
			}
			episode := 0
			if p.Episode != nil {
				episode = *p.Episode
			}
			details, e := api.supplements.GetTVEpisodeRenameDetails(ctx, m.TMDBID, *p.Season, episode)
			if e != nil {
				return "", vars, e
			}
			vars["episode_title"] = details.EpisodeTitle
			vars["episode_date"] = details.EpisodeDate
			vars["season_year"] = details.SeasonYear
		}
		if strings.Contains(format, "total_episodes") && vars["total_episodes"] == 0 {
			if api.err != nil {
				return "", vars, api.err
			}
			count, e := api.supplements.GetTVSeasonEpisodeCount(ctx, m.TMDBID, *p.Season)
			if e != nil {
				return "", vars, e
			}
			vars["total_episodes"] = count
		}
	}
	rendered, err := renderMedianame(format, vars)
	if err != nil {
		return "", vars, err
	}
	prefix := ""
	if classify && m.Category != "" {
		if err := validateMediaRecognitionCategoryName(m.Category); err != nil {
			return "", vars, err
		}
		prefix = m.Category
	}
	return path.Join("/", strings.Trim(root, "/"), prefix, rendered.Path), vars, nil
}

// No credentials are serialized. ConfigVersion describes the exact words and both branch configs.
type MediaPipelineSnapshot struct {
	Schema                 int                                  `json:"schema"`
	Source                 string                               `json:"source"`
	PackageVersion         string                               `json:"package_version"`
	ConfigVersion          string                               `json:"config_version"`
	Words                  []string                             `json:"words"`
	WordsConfigured        bool                                 `json:"words_configured"`
	MP2Categories          MediaRecognitionCategoryConfigResult `json:"mp2_categories"`
	MP2EffectiveCategories *MoviePilotCategoryConfig            `json:"mp2_effective_categories,omitempty"`
	MP2Rename              MediaRenameConfigResult              `json:"mp2_rename"`
	Medianame              MedianameConfig                      `json:"medianame"`
	MedianameConfigError   string                               `json:"medianame_config_error,omitempty"`
	medianameError         error
}

func (s *MoviePilotService) FreezeMediaPipeline(source string) (*MoviePilotService, *MediaPipelineSnapshot, error) {
	source, err := s.ResolveMediaRecognitionSource(source)
	if err != nil {
		return nil, nil, err
	}
	snapshot := &MediaPipelineSnapshot{Schema: 1, Source: source, PackageVersion: MedianameVersion}
	if s.local != nil {
		snapshot.Words, snapshot.WordsConfigured, err = s.local.LoadWords()
		if err != nil {
			return nil, nil, err
		}
		snapshot.MP2Categories, err = s.local.LoadCategoryConfig()
		if err != nil && source != MediaRecognitionSourceMedianame {
			return nil, nil, err
		}
		snapshot.MP2Rename, err = s.local.LoadMediaRenameConfig()
		if err != nil && source != MediaRecognitionSourceMedianame {
			return nil, nil, err
		}
		snapshot.Medianame, snapshot.medianameError = s.local.LoadMedianameConfig()
		if snapshot.medianameError != nil {
			snapshot.MedianameConfigError = snapshot.medianameError.Error()
			if source == MediaRecognitionSourceMedianame {
				return nil, nil, snapshot.medianameError
			}
		}
	}
	snapshot.ConfigVersion = fingerprint(snapshot)
	cfg := config.Config{}
	if s.cfg != nil {
		cfg = *s.cfg
	}
	cfg.MediaRecognition.Source = source
	frozen := NewMoviePilotService(&cfg, s.logger)
	frozen.client = s.client
	frozen.operationCtx = s.requestContext()
	state := s.sharedState()
	state.mu.RLock()
	frozen.accessToken = state.accessToken
	frozen.tokenExpiresAt = state.tokenExpiresAt
	state.mu.RUnlock()
	if s.local != nil {
		local := *s.local
		local.frozen = snapshot
		local.medianameClient = s.local.tmdb.medianameAPI()
		if local.medianameClient.supplements != nil {
			local.tmdb = local.medianameClient.supplements
		}
		frozen.local = &local
	}
	return frozen, snapshot, nil
}
func (s *MoviePilotService) RecognizeMedianame(ctx context.Context, input string, options MediaRecognitionOptions) MedianameRecognition {
	if s == nil || s.local == nil {
		return MedianameRecognition{Engine: MediaRecognitionSourceMedianame, PackageVersion: MedianameVersion, Error: "medianame 服务未初始化", Result: &mtmdb.Result{Status: mtmdb.StatusRequestError}}
	}
	return s.local.RecognizeMedianame(ctx, input, options)
}
func (s *MoviePilotService) MedianameTarget(ctx context.Context, r MedianameRecognition, root string, classify bool, override string) (string, rename.Context, error) {
	if s == nil || s.local == nil {
		return "", nil, errors.New("medianame 服务未初始化")
	}
	return s.local.MedianameTarget(ctx, r, root, classify, override)
}

func MedianameMoviePilotInfo(r MedianameRecognition) (MoviePilotMediaInfo, error) {
	return medianameInfo(r), medianameCompatible(r)
}
func (s *MoviePilotService) recognizeMedianameCompat(input, mode string) (MoviePilotMediaInfo, map[string]any, error) {
	r := s.RecognizeMedianame(s.requestContext(), input, MediaRecognitionOptions{Mode: mode, LookupTMDB: true})
	return medianameInfo(r), map[string]any{"engine": "medianame", "medianame": r}, medianameCompatible(r)
}

// Draft changes are confined to an already-frozen workbench session.
func (s *MoviePilotService) WithMedianameDraft(draft *MedianameConfig) error {
	if draft == nil {
		return nil
	}
	if s.local == nil || s.local.frozen == nil {
		return errors.New("识别会话未初始化")
	}
	if err := ValidateMedianameConfig(*draft); err != nil {
		return err
	}
	s.local.frozen.Medianame = *draft
	s.local.frozen.medianameError = nil
	s.local.frozen.MedianameConfigError = ""
	s.local.frozen.ConfigVersion = ""
	s.local.frozen.ConfigVersion = fingerprint(s.local.frozen)
	return nil
}
func adaptMedianameRecognition(r MedianameRecognition) MediaRecognitionResult {
	out := MediaRecognitionResult{Engine: "medianame", TMDBStatus: "request_error", WordResult: r.WordResult, Warning: r.Error, Candidates: []MediaRecognitionCandidate{}, Raw: map[string]any{"engine": "medianame", "medianame": r}}
	if r.Result == nil {
		return out
	}
	p := r.Result.MetaInfo
	out.TMDBStatus = string(r.Result.Status)
	out.MetaInfo = MediaRecognitionMetaInfo{OriginalInput: r.WordResult.Original, ProcessedInput: r.WordResult.Processed, Name: p.Title, MediaType: string(p.Type), TMDBID: p.IDs.TMDB, BeginSeason: p.Season, EndSeason: p.SeasonEnd, BeginEpisode: p.Episode, EndEpisode: p.EpisodeEnd, Extension: p.Extension, ResourceType: p.Source, ResourcePix: p.Resolution, VideoEncode: p.VideoCodec, AudioEncode: p.AudioCodec, ResourceEffect: p.Effects, ResourceTeam: p.ReleaseGroup, AppliedWords: r.WordResult.AppliedWords}
	if p.Year != 0 {
		out.MetaInfo.Year = strconv.Itoa(p.Year)
	}
	asMedia := func(m *mtmdb.MediaInfo) MediaRecognitionMediaInfo {
		return MediaRecognitionMediaInfo{Source: "medianame", MediaType: string(m.Type), Title: m.Title, OriginalTitle: m.OriginalTitle, Year: strconv.Itoa(m.Year), TMDBID: m.TMDBID, Category: m.Category, PosterPath: m.PosterPath, BackdropPath: m.BackdropPath, Rating: m.VoteAverage, Overview: m.Overview}
	}
	if r.Result.MediaInfo != nil && r.Result.Status == mtmdb.StatusCompatible && r.Error == "" {
		out.MediaInfo = asMedia(r.Result.MediaInfo)
	}
	for _, candidate := range r.Result.Candidates {
		if candidate.MediaInfo != nil {
			out.Candidates = append(out.Candidates, MediaRecognitionCandidate{MediaRecognitionMediaInfo: asMedia(candidate.MediaInfo)})
		}
	}
	out.MetaInfo.SeasonEpisode = fmt.Sprint(rename.BuildContext(p, rename.Media{})["season_episode"])
	out.Raw["meta_info"] = out.MetaInfo
	out.Raw["media_info"] = out.MediaInfo
	return out
}

// The library validates and evaluates rules; this projection is only the category selector's label list.
func (s *MediaRecognitionService) MedianameCategoryNames() (MoviePilotCategoryConfig, error) {
	cfg, err := s.LoadMedianameConfig()
	if err != nil {
		return MoviePilotCategoryConfig{}, err
	}
	return medianameCategoryNames(cfg.CategoryYAML)
}

func medianameCategoryNames(source string) (MoviePilotCategoryConfig, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(source), &node); err != nil {
		return MoviePilotCategoryConfig{}, err
	}
	result := MoviePilotCategoryConfig{Movie: map[string]*MoviePilotCategoryRule{}, TV: map[string]*MoviePilotCategoryRule{}}
	if len(node.Content) == 0 {
		return result, nil
	}
	root := node.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		group := root.Content[i].Value
		values := root.Content[i+1]
		for j := 0; j < len(values.Content); j += 2 {
			name := values.Content[j].Value
			if err := validateMediaRecognitionCategoryName(name); err != nil {
				return result, err
			}
			if group == "movie" || group == "电影" {
				result.Movie[name] = nil
				result.MovieOrder = append(result.MovieOrder, name)
			} else {
				result.TV[name] = nil
				result.TVOrder = append(result.TVOrder, name)
			}
		}
	}
	return result, nil
}

func (s *MoviePilotService) PipelineTMDB() *TMDBService {
	if s == nil || s.local == nil {
		return nil
	}
	return s.local.tmdb
}
func (s *MoviePilotService) RecordPipelineCategories(cfg MoviePilotCategoryConfig) {
	if s == nil || s.local == nil || s.local.frozen == nil {
		return
	}
	snapshot := s.local.frozen
	if snapshot.Source != MediaRecognitionSourceMedianame {
		snapshot.MP2EffectiveCategories = &cfg
	}
	snapshot.ConfigVersion = ""
	snapshot.ConfigVersion = fingerprint(snapshot)
}
