package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

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
