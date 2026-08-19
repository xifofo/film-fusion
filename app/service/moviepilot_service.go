package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	moviePilotTokenCheckInterval     = 30 * time.Minute
	moviePilotTokenSkew              = 2 * time.Minute
	moviePilotRequestTimeout         = 8 * time.Second
	moviePilotLocalFallbackTTL       = 2 * time.Minute
	MediaRecognitionSourceMoviePilot = "moviepilot"
	MediaRecognitionSourceLocal      = "local"
)

// NormalizeMediaRecognitionSource returns the canonical recognition engine
// used by organize requests. Empty values keep the historical MoviePilot
// default for API compatibility.
func NormalizeMediaRecognitionSource(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "moviepilot", "movie-pilot", "mp", "mp2":
		return MediaRecognitionSourceMoviePilot, nil
	case "local", "filmfusion", "film-fusion":
		return MediaRecognitionSourceLocal, nil
	default:
		return "", errors.New("识别方式无效")
	}
}

type MoviePilotService struct {
	logger *logger.Logger
	cfg    *config.Config
	client *http.Client
	local  *MediaRecognitionService

	mu             sync.RWMutex
	accessToken    string
	tokenExpiresAt time.Time
	fallbackUntil  time.Time
	fallbackReason string

	stopChan chan struct{}
	wg       sync.WaitGroup
	ticker   *time.Ticker
}

func NewMoviePilotService(cfg *config.Config, log *logger.Logger) *MoviePilotService {
	return &MoviePilotService{
		logger:   log,
		cfg:      cfg,
		client:   &http.Client{Timeout: moviePilotRequestTimeout},
		stopChan: make(chan struct{}),
	}
}

// SetLocalMediaRecognition 为现有 MoviePilot 调用链接入 FilmFusion 本地降级识别。
func (s *MoviePilotService) SetLocalMediaRecognition(local *MediaRecognitionService) {
	s.local = local
}

func (s *MoviePilotService) Start() {
	if !s.isConfigured() {
		s.logger.Warn("MoviePilot 未配置，跳过令牌定时刷新")
		return
	}

	s.ticker = time.NewTicker(moviePilotTokenCheckInterval)
	s.wg.Add(1)
	go s.run()
	s.logger.Info("MoviePilot 令牌刷新服务已启动")
}

func (s *MoviePilotService) Stop() {
	if s.ticker == nil {
		return
	}
	close(s.stopChan)
	s.ticker.Stop()
	s.wg.Wait()
	s.logger.Info("MoviePilot 令牌刷新服务已停止")
}

func (s *MoviePilotService) run() {
	defer s.wg.Done()

	_, _ = s.refreshToken()

	for {
		select {
		case <-s.ticker.C:
			_, _ = s.refreshToken()
		case <-s.stopChan:
			return
		}
	}
}

func (s *MoviePilotService) isConfigured() bool {
	return strings.TrimSpace(s.cfg.MoviePilot.API) != "" &&
		strings.TrimSpace(s.cfg.MoviePilot.Username) != "" &&
		strings.TrimSpace(s.cfg.MoviePilot.Password) != ""
}

func (s *MoviePilotService) baseURL() string {
	return strings.TrimRight(strings.TrimSpace(s.cfg.MoviePilot.API), "/")
}

func (s *MoviePilotService) GetAccessToken() (string, error) {
	if !s.isConfigured() {
		return "", errors.New("moviepilot 未配置")
	}

	s.mu.RLock()
	token := s.accessToken
	expiresAt := s.tokenExpiresAt
	s.mu.RUnlock()

	if token != "" && time.Now().Before(expiresAt.Add(-moviePilotTokenSkew)) {
		return token, nil
	}

	return s.refreshToken()
}

func (s *MoviePilotService) refreshToken() (string, error) {
	if !s.isConfigured() {
		return "", errors.New("moviepilot 未配置")
	}

	loginURL := s.baseURL() + "/api/v1/login/access-token"
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", s.cfg.MoviePilot.Username)
	form.Set("password", s.cfg.MoviePilot.Password)

	req, err := http.NewRequest("POST", loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("创建 MoviePilot 登录请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 MoviePilot 登录失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MoviePilot 登录失败: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	token, expiresIn, err := parseAccessToken(body)
	if err != nil {
		return "", err
	}

	if token == "" {
		return "", errors.New("MoviePilot 登录未返回 access_token")
	}

	expireAt := time.Now().Add(1 * time.Hour)
	if expiresIn > 0 {
		expireAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	}

	s.mu.Lock()
	s.accessToken = token
	s.tokenExpiresAt = expireAt
	s.mu.Unlock()

	return token, nil
}

func (s *MoviePilotService) doGet(endpointPath string, query url.Values) ([]byte, error) {
	token, err := s.GetAccessToken()
	if err != nil {
		return nil, err
	}

	endpoint := s.baseURL() + endpointPath
	if len(query) > 0 {
		endpoint = endpoint + "?" + query.Encode()
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		if _, refreshErr := s.refreshToken(); refreshErr == nil {
			return s.doGet(endpointPath, query)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MoviePilot 请求失败: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

type MoviePilotManualTransferRequest struct {
	SourcePath   string
	FileType     string
	TmdbID       string
	MediaType    string
	TransferType string
	Scrape       bool
}

type MoviePilotManualTransferResult struct {
	Message string
	Data    any
}

// ManualTransfer asks MoviePilot to synchronously organize one local file or
// directory. A successful response means the transfer call has returned, so a
// later automation node may safely remove the corresponding downloader task.
func (s *MoviePilotService) ManualTransfer(ctx context.Context, request MoviePilotManualTransferRequest) (MoviePilotManualTransferResult, error) {
	result := MoviePilotManualTransferResult{}
	sourcePath := strings.TrimSpace(request.SourcePath)
	if sourcePath == "" {
		return result, errors.New("MoviePilot 整理源路径不能为空")
	}
	fileType := strings.ToLower(strings.TrimSpace(request.FileType))
	if fileType != "file" && fileType != "dir" {
		return result, errors.New("MoviePilot 整理源类型必须是 file 或 dir")
	}

	normalizedPath := strings.ReplaceAll(sourcePath, "\\", "/")
	name := path.Base(strings.TrimRight(normalizedPath, "/"))
	fileItem := map[string]any{
		"storage": "local",
		"path":    sourcePath,
		"type":    fileType,
		"name":    name,
	}
	if fileType == "file" {
		fileItem["extension"] = strings.TrimPrefix(path.Ext(name), ".")
	}
	payload := map[string]any{
		"fileitem": fileItem,
		"scrape":   request.Scrape,
	}
	if tmdbID := strings.TrimSpace(request.TmdbID); tmdbID != "" {
		parsed, err := strconv.ParseUint(tmdbID, 10, 64)
		if err != nil || parsed == 0 {
			return result, errors.New("MoviePilot 整理使用的 TMDB ID 无效")
		}
		payload["tmdbid"] = parsed
	}
	switch strings.ToLower(strings.TrimSpace(request.MediaType)) {
	case "", "auto":
	case "movie":
		payload["type_name"] = "电影"
	case "tv":
		payload["type_name"] = "电视剧"
	default:
		return result, errors.New("MoviePilot 整理媒体类型必须是 auto/movie/tv")
	}
	if transferType := strings.TrimSpace(request.TransferType); transferType != "" {
		payload["transfer_type"] = transferType
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return result, fmt.Errorf("序列化 MoviePilot 整理请求失败: %w", err)
	}
	requestURL := s.baseURL() + "/api/v1/transfer/manual?background=false"
	perform := func(token string) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(encoded))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		client := *s.client
		client.Timeout = 0
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return body, resp.StatusCode, readErr
	}

	token, err := s.GetAccessToken()
	if err != nil {
		return result, err
	}
	body, statusCode, err := perform(token)
	if err != nil {
		return result, fmt.Errorf("请求 MoviePilot 整理失败: %w", err)
	}
	if statusCode == http.StatusUnauthorized {
		token, err = s.refreshToken()
		if err == nil {
			body, statusCode, err = perform(token)
		}
		if err != nil {
			return result, fmt.Errorf("刷新凭据后请求 MoviePilot 整理失败: %w", err)
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return result, fmt.Errorf("MoviePilot 整理请求失败: HTTP %d %s", statusCode, strings.TrimSpace(string(body)))
	}
	if err := validateMoviePilotSuccess(body); err != nil {
		return result, err
	}
	var response struct {
		Message string `json:"message"`
		Data    any    `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return result, fmt.Errorf("解析 MoviePilot 整理响应失败: %w", err)
	}
	result.Message = strings.TrimSpace(response.Message)
	result.Data = response.Data
	return result, nil
}

type MoviePilotCategoryRule struct {
	GenreIDs            string            `json:"genre_ids"`
	OriginalLanguage    string            `json:"original_language"`
	OriginCountry       string            `json:"origin_country"`
	ProductionCountries string            `json:"production_countries"`
	ReleaseYear         string            `json:"release_year"`
	Extra               map[string]string `json:"-"`
}

type MoviePilotCategoryConfig struct {
	Movie      map[string]*MoviePilotCategoryRule `json:"movie"`
	TV         map[string]*MoviePilotCategoryRule `json:"tv"`
	MovieOrder []string                           `json:"-"`
	TVOrder    []string                           `json:"-"`
}

// UnmarshalJSON 保留 MoviePilot 分类对象在 JSON 中的书写顺序。
func (config *MoviePilotCategoryConfig) UnmarshalJSON(data []byte) error {
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(data, &groups); err != nil {
		return err
	}
	var err error
	config.Movie, config.MovieOrder, err = decodeMoviePilotCategoryGroup(groups["movie"])
	if err != nil {
		return fmt.Errorf("解析 movie 分类失败: %w", err)
	}
	config.TV, config.TVOrder, err = decodeMoviePilotCategoryGroup(groups["tv"])
	if err != nil {
		return fmt.Errorf("解析 tv 分类失败: %w", err)
	}
	return nil
}

func decodeMoviePilotCategoryGroup(data json.RawMessage) (map[string]*MoviePilotCategoryRule, []string, error) {
	result := make(map[string]*MoviePilotCategoryRule)
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return result, nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil, errors.New("分类分组必须是对象")
	}
	order := make([]string, 0)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, nil, errors.New("分类名必须是字符串")
		}
		var rule *MoviePilotCategoryRule
		if err := decoder.Decode(&rule); err != nil {
			return nil, nil, err
		}
		result[name] = rule
		order = append(order, name)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	return result, order, nil
}

// UnmarshalJSON 接收 MoviePilot 允许的动态 TMDB 一级字段。
func (rule *MoviePilotCategoryRule) UnmarshalJSON(data []byte) error {
	type knownRule MoviePilotCategoryRule
	var known knownRule
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	*rule = MoviePilotCategoryRule(known)
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	for _, field := range []string{"genre_ids", "original_language", "origin_country", "production_countries", "release_year"} {
		delete(values, field)
	}
	if len(values) == 0 {
		return nil
	}
	rule.Extra = make(map[string]string, len(values))
	for field, raw := range values {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			var scalar any
			if err := json.Unmarshal(raw, &scalar); err != nil {
				return err
			}
			value = fmt.Sprint(scalar)
		}
		rule.Extra[field] = value
	}
	return nil
}

func (s *MoviePilotService) GetCategoryConfig() (MoviePilotCategoryConfig, error) {
	if s.local != nil {
		if localConfig, configured, err := s.local.LoadMoviePilotCategoryConfig(); err != nil {
			if s.logger != nil {
				s.logger.Warnf("[media-recognition] 读取本地分类配置失败，将继续尝试 MoviePilot: %v", err)
			}
		} else if configured {
			return localConfig, nil
		}
	}
	if fallbackErr, active := s.activeLocalRecognitionFallback(); active {
		s.logLocalRecognitionFallback("分类配置", fallbackErr)
		return s.localMediaRecognitionCategoryConfig(), nil
	}
	body, err := s.doGet("/api/v1/media/category/config", nil)
	if err != nil {
		if s.local != nil {
			s.logLocalRecognitionFallback("分类配置", err)
			return s.localMediaRecognitionCategoryConfig(), nil
		}
		return MoviePilotCategoryConfig{}, err
	}
	if err := validateMoviePilotSuccess(body); err != nil {
		if s.local != nil {
			s.logLocalRecognitionFallback("分类配置", err)
			return s.localMediaRecognitionCategoryConfig(), nil
		}
		return MoviePilotCategoryConfig{}, err
	}

	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	var cfg MoviePilotCategoryConfig
	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Data) > 0 {
		if err := json.Unmarshal(wrapper.Data, &cfg); err == nil {
			s.clearLocalRecognitionFallback()
			return cfg, nil
		}
	}

	if err := json.Unmarshal(body, &cfg); err != nil {
		if s.local != nil {
			s.logLocalRecognitionFallback("分类配置", err)
			return s.localMediaRecognitionCategoryConfig(), nil
		}
		return MoviePilotCategoryConfig{}, fmt.Errorf("解析 MoviePilot 分类配置失败: %w", err)
	}

	s.clearLocalRecognitionFallback()
	return cfg, nil
}

// GetCategoryConfigWithSource avoids contacting MoviePilot when an organize
// request explicitly selects FilmFusion's local recognition engine.
func (s *MoviePilotService) GetCategoryConfigWithSource(source string) (MoviePilotCategoryConfig, error) {
	normalized, err := NormalizeMediaRecognitionSource(source)
	if err != nil {
		return MoviePilotCategoryConfig{}, err
	}
	if normalized != MediaRecognitionSourceLocal {
		return s.GetCategoryConfig()
	}
	if s.local == nil {
		return MoviePilotCategoryConfig{}, errors.New("FilmFusion 本地识别未初始化")
	}
	config, configured, err := s.local.LoadMoviePilotCategoryConfig()
	if err != nil {
		return MoviePilotCategoryConfig{}, fmt.Errorf("读取 FilmFusion 本地分类配置失败: %w", err)
	}
	if configured {
		return config, nil
	}
	return s.localMediaRecognitionCategoryConfig(), nil
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
	if fallbackErr, active := s.activeLocalRecognitionFallback(); active {
		return s.recognizeMediaLocally(filePath, MediaRecognitionModeFile, fallbackErr)
	}
	info, raw, err := s.recognizeFileWithMoviePilot(filePath)
	if err == nil {
		s.clearLocalRecognitionFallback()
		return info, raw, nil
	}
	return s.recognizeMediaLocally(filePath, MediaRecognitionModeFile, err)
}

// RecognizeFileWithSource uses exactly the requested recognition engine. This
// is intentionally strict so the organize-page switch has deterministic
// semantics; legacy callers can keep using RecognizeFile for automatic local
// fallback.
func (s *MoviePilotService) RecognizeFileWithSource(filePath, source string) (MoviePilotMediaInfo, map[string]any, error) {
	normalized, err := NormalizeMediaRecognitionSource(source)
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}
	if normalized == MediaRecognitionSourceLocal {
		if s.local == nil {
			return MoviePilotMediaInfo{}, nil, errors.New("FilmFusion 本地识别未初始化")
		}
		return s.local.RecognizeFallback(context.Background(), filePath, MediaRecognitionModeFile)
	}
	info, raw, err := s.recognizeFileWithMoviePilot(filePath)
	if err == nil {
		s.clearLocalRecognitionFallback()
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
	title = strings.TrimSpace(title)
	if title == "" {
		return MoviePilotMediaInfo{}, nil, errors.New("识别标题不能为空")
	}
	if fallbackErr, active := s.activeLocalRecognitionFallback(); active {
		return s.recognizeMediaLocally(title, MediaRecognitionModeTitle, fallbackErr)
	}
	values := url.Values{}
	values.Set("title", title)
	if customWords, configured := s.localRecognitionWords(); configured {
		values.Set("custom_words", customWords)
	}
	info, raw, err := s.recognizeMedia("/api/v1/media/recognize", values)
	if err == nil && strings.TrimSpace(info.Title) != "" {
		s.clearLocalRecognitionFallback()
		return info, raw, nil
	}
	if err == nil {
		err = errors.New("MoviePilot 未识别到媒体信息")
	}
	return s.recognizeMediaLocally(title, MediaRecognitionModeTitle, err)
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

func (s *MoviePilotService) TransferName(filePath, fileType string) (string, map[string]any, error) {
	if fallbackErr, active := s.activeLocalRecognitionFallback(); active {
		return s.transferNameLocally(filePath, fallbackErr)
	}
	name, raw, err := s.transferNameWithMoviePilot(filePath, fileType)
	if err == nil {
		s.clearLocalRecognitionFallback()
		return name, raw, nil
	}
	return s.transferNameLocally(filePath, err)
}

// TransferNameWithSource mirrors RecognizeFileWithSource for the naming stage.
func (s *MoviePilotService) TransferNameWithSource(filePath, fileType, source string) (string, map[string]any, error) {
	normalized, err := NormalizeMediaRecognitionSource(source)
	if err != nil {
		return "", nil, err
	}
	if normalized == MediaRecognitionSourceLocal {
		if s.local == nil {
			return "", nil, errors.New("FilmFusion 本地识别未初始化")
		}
		return s.local.BuildTransferName(filePath)
	}
	name, raw, err := s.transferNameWithMoviePilot(filePath, fileType)
	if err == nil {
		s.clearLocalRecognitionFallback()
	}
	return name, raw, err
}

func (s *MoviePilotService) transferNameWithMoviePilot(filePath, fileType string) (string, map[string]any, error) {
	values := url.Values{}
	values.Set("path", filePath)
	if fileType != "" {
		values.Set("filetype", fileType)
	}

	body, err := s.doGet("/api/v1/transfer/name", values)
	if err != nil {
		return "", nil, err
	}
	if err := validateMoviePilotSuccess(body); err != nil {
		return "", nil, err
	}

	dataMap := unwrapDataMap(body)
	name := extractString(dataMap, "name", "new_name", "file_name", "filename", "title")
	if name == "" {
		if rawName, ok := dataMap["data"]; ok {
			if str, ok := rawName.(string); ok {
				name = str
			}
		}
	}

	if strings.TrimSpace(name) == "" {
		return "", dataMap, errors.New("MoviePilot 未返回重命名结果")
	}
	return name, dataMap, nil
}

func (s *MoviePilotService) localRecognitionWords() (string, bool) {
	if s.local == nil {
		return "", false
	}
	words, configured, err := s.local.LoadWords()
	if err != nil {
		if s.logger != nil {
			s.logger.Warnf("[media-recognition] 读取本地识别词失败，继续使用 MoviePilot 配置: %v", err)
		}
		return "", false
	}
	if !configured {
		return "", false
	}
	if len(words) == 0 {
		// MoviePilot 将空 custom_words 当作未传入并回退到自己的全局词表；
		// 用注释占位才能表达 FilmFusion 已显式保存空词表。
		return "# FilmFusion empty word list", true
	}
	return strings.Join(words, "\n"), true
}

func (s *MoviePilotService) recognizeMediaLocally(input, mode string, upstreamErr error) (MoviePilotMediaInfo, map[string]any, error) {
	if s.local == nil {
		return MoviePilotMediaInfo{}, nil, upstreamErr
	}
	info, raw, localErr := s.local.RecognizeFallback(context.Background(), input, mode)
	if localErr != nil {
		return MoviePilotMediaInfo{}, raw, fmt.Errorf("MoviePilot 识别失败: %v；本地识别也失败: %w", upstreamErr, localErr)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	raw["fallback_reason"] = upstreamErr.Error()
	s.logLocalRecognitionFallback("媒体识别", upstreamErr)
	return info, raw, nil
}

func (s *MoviePilotService) searchMediaLocally(keyword string, count int, upstreamErr error) ([]MoviePilotSearchResult, error) {
	if s.local == nil {
		return nil, upstreamErr
	}
	results, localErr := s.local.SearchMedia(context.Background(), keyword, count)
	if localErr != nil {
		return nil, fmt.Errorf("MoviePilot 搜索失败: %v；本地搜索也失败: %w", upstreamErr, localErr)
	}
	s.logLocalRecognitionFallback("媒体搜索", upstreamErr)
	return results, nil
}

func (s *MoviePilotService) transferNameLocally(filePath string, upstreamErr error) (string, map[string]any, error) {
	if s.local == nil {
		return "", nil, upstreamErr
	}
	name, raw, localErr := s.local.BuildTransferName(filePath)
	if localErr != nil {
		return "", raw, fmt.Errorf("MoviePilot 重命名失败: %v；本地重命名也失败: %w", upstreamErr, localErr)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	raw["fallback_reason"] = upstreamErr.Error()
	s.logLocalRecognitionFallback("文件命名", upstreamErr)
	return name, raw, nil
}

func (s *MoviePilotService) logLocalRecognitionFallback(action string, err error) {
	s.markLocalRecognitionFallback(err)
	if s.logger != nil {
		s.logger.Warnf("[media-recognition] MoviePilot %s不可用，已切换 FilmFusion 本地能力: %v", action, err)
	}
}

func (s *MoviePilotService) activeLocalRecognitionFallback() (error, bool) {
	if s.local == nil {
		return nil, false
	}
	s.mu.RLock()
	until := s.fallbackUntil
	reason := s.fallbackReason
	s.mu.RUnlock()
	if until.IsZero() || !time.Now().Before(until) {
		return nil, false
	}
	return fmt.Errorf("MoviePilot 暂停重试至 %s: %s", until.Format(time.RFC3339), reason), true
}

func (s *MoviePilotService) markLocalRecognitionFallback(err error) {
	if err == nil || !shouldOpenMoviePilotFallbackCircuit(err) {
		return
	}
	s.mu.Lock()
	s.fallbackUntil = time.Now().Add(moviePilotLocalFallbackTTL)
	s.fallbackReason = err.Error()
	s.mu.Unlock()
}

func (s *MoviePilotService) clearLocalRecognitionFallback() {
	s.mu.Lock()
	s.fallbackUntil = time.Time{}
	s.fallbackReason = ""
	s.mu.Unlock()
}

func shouldOpenMoviePilotFallbackCircuit(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "moviepilot 暂停重试至") {
		return false
	}
	for _, businessMessage := range []string{"未识别到媒体", "未返回媒体搜索结果", "未返回重命名结果"} {
		if strings.Contains(message, strings.ToLower(businessMessage)) {
			return false
		}
	}
	return true
}

func (s *MoviePilotService) localMediaRecognitionCategoryConfig() MoviePilotCategoryConfig {
	if s != nil && s.local != nil {
		if config, _, err := s.local.LoadMoviePilotCategoryConfig(); err == nil {
			return config
		} else if s.logger != nil {
			s.logger.Warnf("[media-recognition] 加载本地分类配置失败，使用内置默认值: %v", err)
		}
	}
	config, err := mediaRecognitionCategoryConfigFromYAML(DefaultMediaRecognitionCategoryYAML)
	if err == nil {
		return config
	}
	return MoviePilotCategoryConfig{
		Movie: map[string]*MoviePilotCategoryRule{"电影": nil}, MovieOrder: []string{"电影"},
		TV: map[string]*MoviePilotCategoryRule{"电视剧": nil}, TVOrder: []string{"电视剧"},
	}
}

func BuildMoviePilotTargetPath(category string, info MoviePilotMediaInfo, transferName, originalName string) string {
	folderName := strings.TrimSpace(info.TitleYear)
	if folderName == "" {
		folderName = strings.TrimSpace(transferName)
	}
	if folderName == "" {
		folderName = strings.TrimSpace(info.Title)
		if folderName == "" {
			folderName = strings.TrimSuffix(originalName, path.Ext(originalName))
		}
		if info.Year != "" && !strings.Contains(folderName, info.Year) {
			folderName = fmt.Sprintf("%s (%s)", folderName, info.Year)
		}
	} else if folderName == strings.TrimSpace(transferName) {
		folderName = strings.TrimSuffix(folderName, path.Ext(folderName))
	}

	if tmdbID := strings.TrimSpace(info.TmdbID); tmdbID != "" && !strings.Contains(folderName, "{tmdb-") {
		folderName = strings.TrimRight(folderName, " ") + " {tmdb-" + tmdbID + "}"
	}

	fileName := strings.TrimSpace(transferName)
	if fileName == "" {
		fileName = originalName
	} else {
		originalExt := path.Ext(strings.TrimSpace(originalName))
		if originalExt != "" && !strings.EqualFold(path.Ext(fileName), originalExt) {
			// MoviePilot 可能返回以 .10bit、.x265 等发布标签结尾的名称。
			// path.Ext 会把这些标签误认为扩展名；整理只重命名、不转码，因此始终保留源文件真实后缀。
			fileName = strings.TrimRight(fileName, " ") + originalExt
		}
	}

	basePath := path.Join("/", folderName)
	if strings.TrimSpace(category) != "" {
		basePath = path.Join("/", category, folderName)
	}
	if shouldAddSeasonFolder(info) {
		basePath = path.Join(basePath, fmt.Sprintf("Season %02d", info.BeginSeason))
	}

	return path.Join(basePath, fileName)
}

func SelectMoviePilotCategory(mediaType string, info MoviePilotMediaInfo, cfg MoviePilotCategoryConfig) string {
	normalizedType := strings.ToLower(strings.TrimSpace(mediaType))
	if normalizedType == "" {
		normalizedType = "movie"
	}

	var categories map[string]*MoviePilotCategoryRule
	var order []string
	if normalizedType == "tv" {
		categories = cfg.TV
		order = cfg.TVOrder
	} else {
		categories = cfg.Movie
		order = cfg.MovieOrder
	}

	if len(categories) == 0 {
		return ""
	}

	keys := orderedMoviePilotCategoryNames(categories, order)
	for _, name := range keys {
		rule := categories[name]
		if rule == nil {
			return name
		}
		if matchCategoryRule(info, *rule) {
			return name
		}
	}
	return ""
}

func orderedMoviePilotCategoryNames(categories map[string]*MoviePilotCategoryRule, preferred []string) []string {
	result := make([]string, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, name := range preferred {
		if _, exists := categories[name]; !exists {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	rest := make([]string, 0, len(categories)-len(result))
	for name := range categories {
		if _, exists := seen[name]; !exists {
			rest = append(rest, name)
		}
	}
	sortStrings(rest)
	return append(result, rest...)
}

func matchCategoryRule(info MoviePilotMediaInfo, rule MoviePilotCategoryRule) bool {
	if rule.GenreIDs != "" {
		if !matchMoviePilotCategoryCondition(rule.GenreIDs, info.GenreIDs) {
			return false
		}
	}
	if rule.OriginalLanguage != "" {
		if !matchMoviePilotCategoryCondition(rule.OriginalLanguage, info.OriginalLanguages) {
			return false
		}
	}
	if rule.OriginCountry != "" {
		if !matchMoviePilotCategoryCondition(rule.OriginCountry, info.OriginCountries) {
			return false
		}
	}
	if rule.ProductionCountries != "" {
		if !matchMoviePilotCategoryCondition(rule.ProductionCountries, info.ProductionCountries) {
			return false
		}
	}
	if rule.ReleaseYear != "" {
		if !matchMoviePilotCategoryCondition(rule.ReleaseYear, compactMediaRecognitionValues(info.Year)) {
			return false
		}
	}
	for field, value := range rule.Extra {
		dataValues := info.CategoryFields[field]
		if len(dataValues) == 0 {
			dataValues = info.CategoryFields[strings.ToLower(field)]
		}
		if !matchMoviePilotCategoryCondition(value, dataValues) {
			return false
		}
	}
	return true
}

func matchMoviePilotCategoryCondition(rule string, dataValues []string) bool {
	if strings.TrimSpace(rule) == "" {
		return true
	}
	if len(dataValues) == 0 {
		return false
	}
	data := make(map[string]struct{}, len(dataValues))
	for _, value := range dataValues {
		if value = strings.ToUpper(strings.TrimSpace(value)); value != "" {
			data[value] = struct{}{}
		}
	}
	if len(data) == 0 {
		return false
	}

	positive := make([]string, 0)
	negative := make([]string, 0)
	for _, raw := range strings.Split(rule, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		inverted := strings.HasPrefix(raw, "!")
		raw = strings.TrimPrefix(raw, "!")
		values := expandMoviePilotCategoryValue(raw)
		if inverted {
			negative = append(negative, values...)
		} else {
			positive = append(positive, values...)
		}
	}
	if len(positive) > 0 && !categoryValueIntersects(positive, data) {
		return false
	}
	if len(negative) > 0 && categoryValueIntersects(negative, data) {
		return false
	}
	return true
}

func expandMoviePilotCategoryValue(value string) []string {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "-") {
		return compactMediaRecognitionValues(strings.ToUpper(value))
	}
	parts := strings.SplitN(value, "-", 2)
	startText, endText := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	start, startErr := strconv.Atoi(startText)
	end, endErr := strconv.Atoi(endText)
	if startErr != nil || endErr != nil {
		return compactMediaRecognitionValues(strings.ToUpper(startText), strings.ToUpper(endText))
	}
	if start > end || end-start > 10000 {
		return nil
	}
	result := make([]string, 0, end-start+1)
	for current := start; current <= end; current++ {
		result = append(result, strconv.Itoa(current))
	}
	return result
}

func categoryValueIntersects(values []string, data map[string]struct{}) bool {
	for _, value := range values {
		if _, exists := data[strings.ToUpper(strings.TrimSpace(value))]; exists {
			return true
		}
	}
	return false
}

func normalizeList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseAccessToken(body []byte) (string, int64, error) {
	dataMap := unwrapDataMap(body)
	token := extractString(dataMap, "access_token", "token", "accessToken")
	expires := extractInt64(dataMap, "expires_in", "expires", "expire_in")
	if token == "" {
		if raw, ok := dataMap["data"]; ok {
			if m, ok := raw.(map[string]any); ok {
				token = extractString(m, "access_token", "token", "accessToken")
				expires = extractInt64(m, "expires_in", "expires", "expire_in")
			}
		}
	}
	return token, expires, nil
}

func unwrapDataMap(body []byte) map[string]any {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return map[string]any{}
	}

	if data, ok := root["data"]; ok {
		if dataMap, ok := data.(map[string]any); ok {
			return dataMap
		}
	}
	return root
}

func extractSearchResultMaps(body []byte) []map[string]any {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	return findSearchResultMaps(root)
}

func findSearchResultMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				if looksLikeSearchResult(m) {
					out = append(out, m)
					continue
				}
				if nested := findSearchResultMaps(m); len(nested) > 0 {
					out = append(out, nested...)
				}
			}
		}
		return out
	case map[string]any:
		for _, key := range []string{"data", "items", "list", "results", "medias", "records"} {
			if nested := findSearchResultMaps(typed[key]); len(nested) > 0 {
				return nested
			}
		}
		if looksLikeSearchResult(typed) {
			return []map[string]any{typed}
		}
	}
	return nil
}

func looksLikeSearchResult(data map[string]any) bool {
	if data == nil {
		return false
	}
	if raw, ok := data["media_info"]; ok {
		if m, ok := raw.(map[string]any); ok {
			if looksLikeSearchResult(m) {
				return true
			}
		}
	}
	return extractString(data, "tmdb_id", "tmdbId", "mediaid") != "" &&
		extractString(data, "title", "name", "original_title", "originalTitle") != ""
}

func parseSearchResult(data map[string]any) MoviePilotSearchResult {
	base := data
	if raw, ok := data["media_info"]; ok {
		if m, ok := raw.(map[string]any); ok {
			base = m
		}
	}

	info := parseMediaInfo(data)
	mediaType := extractString(base, "media_type", "mediaType", "type", "type_name")
	if mediaType == "" {
		mediaType = info.MediaType
	}
	tmdbID := normalizeTmdbID(extractString(base, "tmdb_id", "tmdbId", "mediaid"))
	if tmdbID == "" {
		tmdbID = normalizeTmdbID(extractString(data, "tmdb_id", "tmdbId", "mediaid"))
	}

	return MoviePilotSearchResult{
		MediaType:     mediaType,
		Title:         info.Title,
		OriginalTitle: extractString(base, "original_title", "originalTitle", "original_name", "originalName"),
		Year:          info.Year,
		TitleYear:     info.TitleYear,
		TmdbID:        tmdbID,
		Category:      info.Category,
		PosterPath:    firstNonEmptyMoviePilotString(extractString(base, "poster_path", "posterPath", "poster", "image", "cover", "cover_url"), info.PosterPath),
		BackdropPath:  firstNonEmptyMoviePilotString(extractString(base, "backdrop_path", "backdropPath", "backdrop", "background", "background_image"), info.BackdropPath),
		Rating:        firstNonZeroMoviePilotFloat(extractFloat64(base, "vote_average", "voteAverage", "rating", "score"), info.Rating),
		Genres:        firstNonEmptyMoviePilotSlice(extractNamedStringSlice(base, "genre_names", "genreNames", "genres"), info.Genres),
		Overview:      extractString(base, "overview", "description", "desc"),
	}
}

func normalizeTmdbID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.LastIndex(value, ":"); idx >= 0 && idx < len(value)-1 {
		value = value[idx+1:]
	}
	var digits strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	return digits.String()
}

func parseMediaInfo(data map[string]any) MoviePilotMediaInfo {
	info := MoviePilotMediaInfo{}

	base := data
	if raw, ok := data["media_info"]; ok {
		if m, ok := raw.(map[string]any); ok {
			base = m
		}
	}

	info.MediaType = strings.ToLower(extractString(base, "media_type", "mediaType", "type", "category"))
	if info.MediaType == "" {
		info.MediaType = strings.ToLower(extractString(data, "media_type", "mediaType", "type", "category"))
	}

	info.Category = extractString(base, "category", "category_name")
	if info.Category == "" {
		info.Category = extractString(data, "category", "category_name")
	}

	info.Title = extractString(base, "title", "name", "original_title", "originalTitle")
	if info.Title == "" {
		info.Title = extractString(data, "title", "name", "original_title", "originalTitle")
	}
	info.OriginalTitle = extractString(base, "original_title", "originalTitle", "original_name", "originalName")
	if info.OriginalTitle == "" {
		info.OriginalTitle = extractString(data, "original_title", "originalTitle", "original_name", "originalName")
	}

	info.TitleYear = extractString(base, "title_year", "titleYear")
	if info.TitleYear == "" {
		info.TitleYear = extractString(data, "title_year", "titleYear")
	}
	info.TmdbID = extractString(base, "tmdb_id", "tmdbId")
	if info.TmdbID == "" {
		info.TmdbID = extractString(data, "tmdb_id", "tmdbId")
	}
	info.PosterPath = extractString(base, "poster_path", "posterPath", "poster", "image", "cover", "cover_url")
	if info.PosterPath == "" {
		info.PosterPath = extractString(data, "poster_path", "posterPath", "poster", "image", "cover", "cover_url")
	}
	info.BackdropPath = extractString(base, "backdrop_path", "backdropPath", "backdrop", "background", "background_image")
	if info.BackdropPath == "" {
		info.BackdropPath = extractString(data, "backdrop_path", "backdropPath", "backdrop", "background", "background_image")
	}
	info.Rating = extractFloat64(base, "vote_average", "voteAverage", "rating", "score")
	if info.Rating == 0 {
		info.Rating = extractFloat64(data, "vote_average", "voteAverage", "rating", "score")
	}
	info.Genres = extractNamedStringSlice(base, "genre_names", "genreNames", "genres")
	if len(info.Genres) == 0 {
		info.Genres = extractNamedStringSlice(data, "genre_names", "genreNames", "genres")
	}
	info.SeasonEpisode = extractSeasonEpisode(data)
	if raw, ok := data["meta_info"].(map[string]any); ok {
		info.ResourceType = extractString(raw, "resource_type", "resourceType")
		info.ResourcePix = extractString(raw, "resource_pix", "resourcePix")
		info.VideoEncode = extractString(raw, "video_encode", "videoEncode")
	}

	info.Year = extractYear(base)
	if info.Year == "" {
		info.Year = extractYear(data)
	}

	info.GenreIDs = extractStringSlice(base, "genre_ids", "genreIds", "genres")
	if len(info.GenreIDs) == 0 {
		info.GenreIDs = extractStringSlice(data, "genre_ids", "genreIds", "genres")
	}
	info.OriginalLanguages = extractStringSlice(base, "original_language", "originalLanguage", "languages")
	if len(info.OriginalLanguages) == 0 {
		info.OriginalLanguages = extractStringSlice(data, "original_language", "originalLanguage", "languages")
	}
	info.OriginCountries = extractStringSlice(base, "origin_country", "originCountry", "origin_countries")
	if len(info.OriginCountries) == 0 {
		info.OriginCountries = extractStringSlice(data, "origin_country", "originCountry", "origin_countries")
	}
	info.ProductionCountries = extractStringSlice(base, "production_countries", "productionCountries")
	if len(info.ProductionCountries) == 0 {
		info.ProductionCountries = extractStringSlice(data, "production_countries", "productionCountries")
	}
	info.CategoryFields = extractMoviePilotCategoryFields(base)
	return info
}

func extractMoviePilotCategoryFields(data map[string]any) map[string][]string {
	fields := make(map[string][]string, len(data))
	for field, raw := range data {
		values := moviePilotCategoryScalarValues(raw)
		if len(values) == 0 {
			continue
		}
		fields[field] = values
		fields[strings.ToLower(field)] = values
	}
	return fields
}

func moviePilotCategoryScalarValues(raw any) []string {
	switch value := raw.(type) {
	case string:
		return compactMediaRecognitionValues(value)
	case json.Number:
		return compactMediaRecognitionValues(value.String())
	case float64:
		return compactMediaRecognitionValues(strconv.FormatFloat(value, 'f', -1, 64))
	case bool:
		return []string{strconv.FormatBool(value)}
	case []any:
		values := make([]string, 0, len(value))
		for _, item := range value {
			values = append(values, moviePilotCategoryScalarValues(item)...)
		}
		return values
	case map[string]any:
		for _, key := range []string{"iso_3166_1", "id", "name", "value"} {
			if nested := moviePilotCategoryScalarValues(value[key]); len(nested) > 0 {
				return nested
			}
		}
	}
	return nil
}

func extractYear(data map[string]any) string {
	if year := extractString(data, "year", "release_year", "releaseYear"); year != "" {
		return year
	}
	if date := extractString(data, "release_date", "first_air_date", "air_date"); date != "" {
		if len(date) >= 4 {
			return date[:4]
		}
	}
	return ""
}

func extractString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if val, ok := data[key]; ok {
			switch typed := val.(type) {
			case string:
				return typed
			case float64:
				if typed == float64(int64(typed)) {
					return strconv.FormatInt(int64(typed), 10)
				}
				return fmt.Sprintf("%v", typed)
			case json.Number:
				return typed.String()
			}
		}
	}
	return ""
}

func extractInt64(data map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if val, ok := data[key]; ok {
			switch typed := val.(type) {
			case float64:
				return int64(typed)
			case json.Number:
				if n, err := typed.Int64(); err == nil {
					return n
				}
			case string:
				if n, err := strconv.ParseInt(typed, 10, 64); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func extractFloat64(data map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if val, ok := data[key]; ok {
			switch typed := val.(type) {
			case float64:
				return typed
			case json.Number:
				if n, err := typed.Float64(); err == nil {
					return n
				}
			case string:
				if n, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func extractBeginSeason(data map[string]any) (int, bool) {
	if data == nil {
		return 0, false
	}
	if seasonEpisode := extractSeasonEpisode(data); seasonEpisode != "" {
		if season, ok := parseSeasonFromEpisode(seasonEpisode); ok {
			return season, true
		}
	}
	if raw, ok := data["meta_info"]; ok {
		if meta, ok := raw.(map[string]any); ok {
			if val := extractInt64(meta, "begin_season", "beginSeason"); val >= 0 {
				return int(val), true
			}
		}
	}
	if val := extractInt64(data, "begin_season", "beginSeason"); val >= 0 {
		return int(val), true
	}
	return 0, false
}

func shouldAddSeasonFolder(info MoviePilotMediaInfo) bool {
	if !info.HasBeginSeason {
		return false
	}
	if isTVMediaType(info.MediaType) {
		return true
	}
	if isTVCategory(info.Category) {
		return true
	}
	return false
}

func isTVMediaType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tv", "series", "tvshow", "show", "电视剧":
		return true
	default:
		return false
	}
}

func isTVCategory(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	if strings.Contains(value, "剧集") {
		return true
	}
	return value == "tv" || value == "series"
}

func extractSeasonEpisode(data map[string]any) string {
	if data == nil {
		return ""
	}
	if raw, ok := data["media_info"]; ok {
		if m, ok := raw.(map[string]any); ok {
			if val := extractString(m, "season_episode", "seasonEpisode"); val != "" {
				return val
			}
		}
	}
	if val := extractString(data, "season_episode", "seasonEpisode"); val != "" {
		return val
	}
	if raw, ok := data["meta_info"]; ok {
		if m, ok := raw.(map[string]any); ok {
			if val := extractString(m, "season_episode", "seasonEpisode"); val != "" {
				return val
			}
		}
	}
	return ""
}

func parseSeasonFromEpisode(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] != 'S' && value[i] != 's' {
			continue
		}
		j := i + 1
		for j < len(value) && value[j] >= '0' && value[j] <= '9' {
			j++
		}
		if j == i+1 {
			continue
		}
		season, err := strconv.Atoi(value[i+1 : j])
		if err == nil {
			return season, true
		}
	}
	return 0, false
}

func extractStringSlice(data map[string]any, keys ...string) []string {
	for _, key := range keys {
		if val, ok := data[key]; ok {
			switch typed := val.(type) {
			case []string:
				return typed
			case []any:
				return flattenToStrings(typed)
			case string:
				return normalizeList(typed)
			}
		}
	}
	return nil
}

func extractNamedStringSlice(data map[string]any, keys ...string) []string {
	for _, key := range keys {
		if val, ok := data[key]; ok {
			switch typed := val.(type) {
			case []string:
				return typed
			case []any:
				out := make([]string, 0, len(typed))
				for _, item := range typed {
					switch value := item.(type) {
					case string:
						if value = strings.TrimSpace(value); value != "" {
							out = append(out, value)
						}
					case map[string]any:
						if name := strings.TrimSpace(extractString(value, "name", "title", "value")); name != "" {
							out = append(out, name)
						}
					}
				}
				return out
			case string:
				return normalizeList(typed)
			}
		}
	}
	return nil
}

func firstNonEmptyMoviePilotString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroMoviePilotFloat(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonEmptyMoviePilotSlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func validateMoviePilotSuccess(body []byte) error {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		// Some older endpoints return an unwrapped array. Their existing parsers
		// remain authoritative when no business-status object is present.
		return nil
	}
	rawSuccess, exists := root["success"]
	if !exists {
		return nil
	}

	success, recognized := moviePilotSuccessValue(rawSuccess)
	if !recognized || success {
		return nil
	}

	message := strings.TrimSpace(extractString(root, "message", "msg", "detail", "error"))
	if message == "" {
		if data, ok := root["data"].(string); ok {
			message = strings.TrimSpace(data)
		}
	}
	if message == "" {
		message = "未返回错误详情"
	}
	return fmt.Errorf("MoviePilot 业务请求失败: %s", message)
}

func moviePilotSuccessValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case float64:
		return typed != 0, true
	case json.Number:
		n, err := typed.Float64()
		return n != 0, err == nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}

func flattenToStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, val := range values {
		switch typed := val.(type) {
		case string:
			out = append(out, typed)
		case float64:
			out = append(out, strconv.FormatInt(int64(typed), 10))
		case map[string]any:
			if id := extractString(typed, "id", "value", "code"); id != "" {
				out = append(out, id)
			} else if name := extractString(typed, "name", "title"); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func sortStrings(values []string) {
	if len(values) < 2 {
		return
	}
	for i := 0; i < len(values)-1; i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
