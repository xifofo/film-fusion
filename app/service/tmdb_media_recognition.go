package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type tmdbMediaRecognitionRecord struct {
	ID               int              `json:"id"`
	MediaType        string           `json:"media_type"`
	Title            string           `json:"title"`
	Name             string           `json:"name"`
	OriginalTitle    string           `json:"original_title"`
	OriginalName     string           `json:"original_name"`
	ReleaseDate      string           `json:"release_date"`
	FirstAirDate     string           `json:"first_air_date"`
	PosterPath       string           `json:"poster_path"`
	BackdropPath     string           `json:"backdrop_path"`
	VoteAverage      float64          `json:"vote_average"`
	Overview         string           `json:"overview"`
	Popularity       float64          `json:"popularity"`
	GenreIDs         []int            `json:"genre_ids"`
	Genres           []tmdbNamedValue `json:"genres"`
	OriginalLanguage string           `json:"original_language"`
	OriginCountry    []string         `json:"origin_country"`
	Countries        []tmdbCountry    `json:"production_countries"`
}

type tmdbNamedValue struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type tmdbCountry struct {
	ISO3166 string `json:"iso_3166_1"`
	Name    string `json:"name"`
}

type tmdbMediaRecognitionSearchResponse struct {
	Results []tmdbMediaRecognitionRecord `json:"results"`
}

type tmdbMediaRecognitionHTTPError struct {
	Status int
	Body   string
}

func (e *tmdbMediaRecognitionHTTPError) Error() string {
	return fmt.Sprintf("TMDB 请求失败: HTTP %d %s", e.Status, e.Body)
}

func (s *TMDBService) mediaRecognitionConfigured() bool {
	return s != nil && s.cfg != nil && s.cfg.TMDB.Enabled &&
		(strings.TrimSpace(s.cfg.TMDB.APIKey) != "" || strings.TrimSpace(s.cfg.TMDB.AccessToken) != "")
}

func (s *TMDBService) matchMediaRecognition(ctx context.Context, meta MediaRecognitionMetaInfo) (*MediaRecognitionCandidate, []MediaRecognitionCandidate, string, error) {
	if !s.mediaRecognitionConfigured() {
		return nil, nil, "not_configured", errors.New("TMDB 未配置")
	}
	if meta.TMDBID != "" {
		candidates, err := s.lookupMediaRecognitionByID(ctx, meta)
		if err != nil {
			return nil, candidates, "error", err
		}
		if len(candidates) == 0 {
			return nil, candidates, "not_found", nil
		}
		selected := candidates[0]
		selected.Score = 200
		selected.Confidence = 1
		return &selected, candidates, "matched_by_id", nil
	}

	candidates := make([]MediaRecognitionCandidate, 0)
	for _, searchTitle := range mediaRecognitionSearchTitles(meta.Name) {
		var err error
		candidates, err = s.searchMediaRecognitionCandidates(ctx, searchTitle, meta.MediaType, meta.Year, 8)
		if err != nil {
			return nil, nil, "error", err
		}
		if len(candidates) > 0 {
			break
		}
	}
	for index := range candidates {
		candidates[index].Score = scoreMediaRecognitionCandidate(meta, candidates[index])
		candidates[index].Confidence = math.Min(1, math.Max(0, float64(candidates[index].Score)/150))
	}
	sortMediaRecognitionCandidates(candidates)
	if len(candidates) == 0 || candidates[0].Score < 90 {
		return nil, candidates, "not_found", nil
	}
	if len(candidates) > 1 && candidates[0].Score-candidates[1].Score < 10 &&
		!mediaRecognitionCandidateHasExactYear(meta, candidates[0]) {
		return nil, candidates, "ambiguous", nil
	}
	selected := candidates[0]
	return &selected, candidates, "matched", nil
}

func (s *TMDBService) lookupMediaRecognitionByID(ctx context.Context, meta MediaRecognitionMetaInfo) ([]MediaRecognitionCandidate, error) {
	mediaTypes := []string{meta.MediaType}
	if meta.MediaType != "movie" && meta.MediaType != "tv" {
		if meta.BeginEpisode != nil || meta.BeginSeason != nil {
			mediaTypes = []string{"tv", "movie"}
		} else {
			mediaTypes = []string{"movie", "tv"}
		}
	}
	candidates := make([]MediaRecognitionCandidate, 0, len(mediaTypes))
	for _, mediaType := range mediaTypes {
		var record tmdbMediaRecognitionRecord
		err := s.doMediaRecognitionTMDBRequest(ctx, "/3/"+mediaType+"/"+url.PathEscape(meta.TMDBID), url.Values{
			"language": []string{"zh-CN"},
		}, &record)
		if err != nil {
			var httpErr *tmdbMediaRecognitionHTTPError
			if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
				continue
			}
			return candidates, err
		}
		record.MediaType = mediaType
		candidate := mediaRecognitionCandidateFromTMDB(record)
		candidate.Score = scoreMediaRecognitionCandidate(meta, candidate)
		candidate.Confidence = 1
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	return candidates, nil
}

func (s *TMDBService) searchMediaRecognitionCandidates(ctx context.Context, title, mediaType, year string, limit int) ([]MediaRecognitionCandidate, error) {
	if !s.mediaRecognitionConfigured() {
		return nil, errors.New("TMDB 未配置")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("TMDB 搜索标题不能为空")
	}
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	mediaType = normalizeLocalMediaType(mediaType)
	endpoint := "/3/search/multi"
	if mediaType == "movie" || mediaType == "tv" {
		endpoint = "/3/search/" + mediaType
	}
	query := url.Values{
		"query":         []string{title},
		"language":      []string{"zh-CN"},
		"include_adult": []string{"false"},
		"page":          []string{"1"},
	}
	if year != "" {
		if mediaType == "movie" {
			query.Set("year", year)
		} else if mediaType == "tv" {
			query.Set("first_air_date_year", year)
		}
	}
	var response tmdbMediaRecognitionSearchResponse
	if err := s.doMediaRecognitionTMDBRequest(ctx, endpoint, query, &response); err != nil {
		return nil, err
	}
	candidates := make([]MediaRecognitionCandidate, 0, min(limit, len(response.Results)))
	for _, record := range response.Results {
		if record.MediaType == "person" {
			continue
		}
		if record.MediaType == "" && mediaType != "unknown" {
			record.MediaType = mediaType
		}
		if record.MediaType != "movie" && record.MediaType != "tv" {
			continue
		}
		candidates = append(candidates, mediaRecognitionCandidateFromTMDB(record))
		if len(candidates) >= limit {
			break
		}
	}
	return candidates, nil
}

func (s *TMDBService) doMediaRecognitionTMDBRequest(ctx context.Context, endpoint string, query url.Values, target any) error {
	baseURL := strings.TrimRight(strings.TrimSpace(s.cfg.TMDB.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.themoviedb.org"
	}
	if query == nil {
		query = url.Values{}
	}
	if strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		query.Set("api_key", strings.TrimSpace(s.cfg.TMDB.APIKey))
	}
	requestURL := baseURL + endpoint + "?" + query.Encode()
	timeout := s.cfg.TMDB.TimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	requestContext, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(s.cfg.TMDB.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 TMDB 失败: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode != http.StatusOK {
		return &tmdbMediaRecognitionHTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("解析 TMDB 响应失败: %w", err)
	}
	return nil
}

func mediaRecognitionCandidateFromTMDB(record tmdbMediaRecognitionRecord) MediaRecognitionCandidate {
	mediaType := normalizeLocalMediaType(record.MediaType)
	title := strings.TrimSpace(record.Title)
	originalTitle := strings.TrimSpace(record.OriginalTitle)
	date := record.ReleaseDate
	if mediaType == "tv" {
		title = strings.TrimSpace(record.Name)
		originalTitle = strings.TrimSpace(record.OriginalName)
		date = record.FirstAirDate
	}
	if title == "" {
		title = originalTitle
	}
	year := ""
	if len(date) >= 4 {
		year = date[:4]
	}
	titleYear := title
	if year != "" {
		titleYear = fmt.Sprintf("%s (%s)", title, year)
	}
	genreIDs := make([]string, 0, len(record.GenreIDs)+len(record.Genres))
	genres := make([]string, 0, len(record.GenreIDs)+len(record.Genres))
	seenGenre := make(map[int]struct{})
	for _, item := range record.Genres {
		if item.ID <= 0 {
			continue
		}
		seenGenre[item.ID] = struct{}{}
		genreIDs = append(genreIDs, strconv.Itoa(item.ID))
		if strings.TrimSpace(item.Name) != "" {
			genres = append(genres, item.Name)
		}
	}
	for _, id := range record.GenreIDs {
		if _, ok := seenGenre[id]; ok {
			continue
		}
		genreIDs = append(genreIDs, strconv.Itoa(id))
		if name := localTMDBGenreName(mediaType, id); name != "" {
			genres = append(genres, name)
		}
	}
	productionCountries := make([]string, 0, len(record.Countries))
	for _, country := range record.Countries {
		if country.ISO3166 != "" {
			productionCountries = append(productionCountries, country.ISO3166)
		}
	}
	media := MediaRecognitionMediaInfo{
		Source: "themoviedb", MediaType: mediaType, Title: title,
		OriginalTitle: originalTitle, Year: year, TitleYear: titleYear,
		TMDBID:     strconv.Itoa(record.ID),
		PosterPath: record.PosterPath, BackdropPath: record.BackdropPath,
		Rating: record.VoteAverage, Genres: genres, GenreIDs: genreIDs,
		Overview: record.Overview, OriginalLanguage: record.OriginalLanguage,
		OriginCountries: record.OriginCountry, ProductionCountries: productionCountries,
	}
	return MediaRecognitionCandidate{MediaRecognitionMediaInfo: media}
}

func scoreMediaRecognitionCandidate(meta MediaRecognitionMetaInfo, candidate MediaRecognitionCandidate) int {
	query := normalizeLocalTitleForMatch(meta.Name)
	title := normalizeLocalTitleForMatch(candidate.Title)
	original := normalizeLocalTitleForMatch(candidate.OriginalTitle)
	score := 0
	switch {
	case query != "" && (query == title || query == original):
		score += 110
	case query != "" && ((title != "" && strings.Contains(title, query)) || (original != "" && strings.Contains(original, query))):
		score += 70
	case query != "" && ((title != "" && strings.Contains(query, title)) || (original != "" && strings.Contains(query, original))):
		score += 60
	default:
		score += mediaRecognitionTokenOverlapScore(meta.Name, candidate.Title)
	}
	if meta.Year != "" {
		if candidate.Year == meta.Year {
			score += 30
		} else if candidate.Year != "" {
			score -= 25
		}
	}
	if meta.MediaType == "movie" || meta.MediaType == "tv" {
		if candidate.MediaType == meta.MediaType {
			score += 20
		} else {
			score -= 35
		}
	}
	if candidate.Rating > 0 {
		score += min(5, int(candidate.Rating/2))
	}
	return score
}

func mediaRecognitionTokenOverlapScore(left, right string) int {
	tokens := func(value string) map[string]struct{} {
		fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && !(r > 127)
		})
		result := make(map[string]struct{}, len(fields))
		for _, field := range fields {
			if field != "" {
				result[field] = struct{}{}
			}
		}
		return result
	}
	leftTokens, rightTokens := tokens(left), tokens(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	matches := 0
	for token := range leftTokens {
		if _, ok := rightTokens[token]; ok {
			matches++
		}
	}
	return int(math.Round(70 * float64(matches) / float64(max(len(leftTokens), len(rightTokens)))))
}

func mediaRecognitionCandidateHasExactYear(meta MediaRecognitionMetaInfo, candidate MediaRecognitionCandidate) bool {
	return meta.Year != "" && candidate.Year == meta.Year
}

func localTMDBGenreName(mediaType string, id int) string {
	movie := map[int]string{
		12: "冒险", 14: "奇幻", 16: "动画", 18: "剧情", 27: "恐怖", 28: "动作",
		35: "喜剧", 36: "历史", 37: "西部", 53: "惊悚", 80: "犯罪", 99: "纪录",
		878: "科幻", 9648: "悬疑", 10402: "音乐", 10749: "爱情", 10751: "家庭",
		10752: "战争", 10770: "电视电影",
	}
	tv := map[int]string{
		16: "动画", 18: "剧情", 35: "喜剧", 37: "西部", 80: "犯罪", 99: "纪录",
		9648: "悬疑", 10751: "家庭", 10759: "动作冒险", 10762: "儿童",
		10763: "新闻", 10764: "真人秀", 10765: "科幻奇幻", 10766: "肥皂剧",
		10767: "脱口秀", 10768: "战争政治",
	}
	if mediaType == "tv" {
		return tv[id]
	}
	return movie[id]
}
