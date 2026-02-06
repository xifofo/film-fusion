package service

import (
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
	moviePilotTokenCheckInterval = 30 * time.Minute
	moviePilotTokenSkew          = 2 * time.Minute
)

type MoviePilotService struct {
	logger *logger.Logger
	cfg    *config.Config
	client *http.Client

	mu             sync.RWMutex
	accessToken    string
	tokenExpiresAt time.Time

	stopChan chan struct{}
	wg       sync.WaitGroup
	ticker   *time.Ticker
}

func NewMoviePilotService(cfg *config.Config, log *logger.Logger) *MoviePilotService {
	return &MoviePilotService{
		logger:   log,
		cfg:      cfg,
		client:   &http.Client{Timeout: 30 * time.Second},
		stopChan: make(chan struct{}),
	}
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

type MoviePilotCategoryRule struct {
	GenreIDs            string `json:"genre_ids"`
	OriginalLanguage    string `json:"original_language"`
	OriginCountry       string `json:"origin_country"`
	ProductionCountries string `json:"production_countries"`
	ReleaseYear         string `json:"release_year"`
}

type MoviePilotCategoryConfig struct {
	Movie map[string]*MoviePilotCategoryRule `json:"movie"`
	TV    map[string]*MoviePilotCategoryRule `json:"tv"`
}

func (s *MoviePilotService) GetCategoryConfig() (MoviePilotCategoryConfig, error) {
	body, err := s.doGet("/api/v1/media/category/config", nil)
	if err != nil {
		return MoviePilotCategoryConfig{}, err
	}

	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	var cfg MoviePilotCategoryConfig
	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Data) > 0 {
		if err := json.Unmarshal(wrapper.Data, &cfg); err == nil {
			return cfg, nil
		}
	}

	if err := json.Unmarshal(body, &cfg); err != nil {
		return MoviePilotCategoryConfig{}, fmt.Errorf("解析 MoviePilot 分类配置失败: %w", err)
	}

	return cfg, nil
}

type MoviePilotMediaInfo struct {
	MediaType           string
	Title               string
	Year                string
	Category            string
	TitleYear           string
	TmdbID              string
	GenreIDs            []string
	OriginalLanguages   []string
	OriginCountries     []string
	ProductionCountries []string
	BeginSeason         int
	HasBeginSeason      bool
}

func (s *MoviePilotService) RecognizeFile(filePath string) (MoviePilotMediaInfo, map[string]any, error) {
	values := url.Values{}
	values.Set("path", filePath)

	body, err := s.doGet("/api/v1/media/recognize_file", values)
	if err != nil {
		return MoviePilotMediaInfo{}, nil, err
	}

	dataMap := unwrapDataMap(body)
	info := parseMediaInfo(dataMap)
	info.BeginSeason, info.HasBeginSeason = extractBeginSeason(dataMap)
	return info, dataMap, nil
}

func (s *MoviePilotService) TransferName(filePath, fileType string) (string, map[string]any, error) {
	values := url.Values{}
	values.Set("path", filePath)
	if fileType != "" {
		values.Set("filetype", fileType)
	}

	body, err := s.doGet("/api/v1/transfer/name", values)
	if err != nil {
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

	return name, dataMap, nil
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

	fileName := strings.TrimSpace(transferName)
	if fileName == "" {
		fileName = originalName
	} else if path.Ext(fileName) == "" && path.Ext(originalName) != "" {
		fileName = fileName + path.Ext(originalName)
	}

	basePath := path.Join("/", folderName)
	if strings.TrimSpace(category) != "" {
		basePath = path.Join("/", category, folderName)
	}
	if info.HasBeginSeason {
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
	if normalizedType == "tv" {
		categories = cfg.TV
	} else {
		categories = cfg.Movie
	}

	if len(categories) == 0 {
		return ""
	}

	keys := make([]string, 0, len(categories))
	for k := range categories {
		keys = append(keys, k)
	}
	sortStrings(keys)

	bestName := ""
	bestScore := -1
	fallback := ""

	for _, name := range keys {
		rule := categories[name]
		if rule == nil {
			if fallback == "" {
				fallback = name
			}
			continue
		}

		match, score := matchCategoryRule(info, *rule)
		if match && score > bestScore {
			bestScore = score
			bestName = name
		}
	}

	if bestName != "" {
		return bestName
	}
	if fallback != "" {
		return fallback
	}
	return keys[0]
}

func matchCategoryRule(info MoviePilotMediaInfo, rule MoviePilotCategoryRule) (bool, int) {
	score := 0
	if rule.GenreIDs != "" {
		score++
		if !hasAny(normalizeList(rule.GenreIDs), info.GenreIDs) {
			return false, 0
		}
	}
	if rule.OriginalLanguage != "" {
		score++
		if !hasAny(normalizeList(rule.OriginalLanguage), info.OriginalLanguages) {
			return false, 0
		}
	}
	if rule.OriginCountry != "" {
		score++
		if !hasAny(normalizeList(rule.OriginCountry), info.OriginCountries) {
			return false, 0
		}
	}
	if rule.ProductionCountries != "" {
		score++
		if !hasAny(normalizeList(rule.ProductionCountries), info.ProductionCountries) {
			return false, 0
		}
	}
	if rule.ReleaseYear != "" {
		score++
		if !matchReleaseYear(rule.ReleaseYear, info.Year) {
			return false, 0
		}
	}
	return true, score
}

func matchReleaseYear(rule, year string) bool {
	rule = strings.TrimSpace(rule)
	year = strings.TrimSpace(year)
	if rule == "" || year == "" {
		return false
	}

	if strings.Contains(rule, "-") {
		parts := strings.Split(rule, "-")
		if len(parts) != 2 {
			return false
		}
		start, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		y, _ := strconv.Atoi(year)
		if start == 0 || end == 0 || y == 0 {
			return false
		}
		return y >= start && y <= end
	}

	for _, val := range normalizeList(rule) {
		if val == year {
			return true
		}
	}
	return false
}

func hasAny(ruleValues []string, dataValues []string) bool {
	if len(ruleValues) == 0 || len(dataValues) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(dataValues))
	for _, v := range dataValues {
		set[strings.ToLower(strings.TrimSpace(v))] = struct{}{}
	}
	for _, r := range ruleValues {
		if _, ok := set[strings.ToLower(strings.TrimSpace(r))]; ok {
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

	info.TitleYear = extractString(base, "title_year", "titleYear")
	if info.TitleYear == "" {
		info.TitleYear = extractString(data, "title_year", "titleYear")
	}
	info.TmdbID = extractString(base, "tmdb_id", "tmdbId")
	if info.TmdbID == "" {
		info.TmdbID = extractString(data, "tmdb_id", "tmdbId")
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
	return info
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
