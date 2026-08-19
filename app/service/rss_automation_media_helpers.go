package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var rssAutomationSeasonEpisodeRegexp = regexp.MustCompile(`(?i)S([0-9]{1,2})[ ._-]*E([0-9]{1,3})`)

type RSSAutomationMediaRecognizer interface {
	RecognizeTitle(title string) (MoviePilotMediaInfo, map[string]any, error)
	SearchMedia(keyword string, count int) ([]MoviePilotSearchResult, error)
}

type RSSAutomationMediaMetadata struct {
	Title         string
	Year          string
	MediaType     string
	Category      string
	SeasonEpisode string
	Rating        float64
	Quality       string
	TmdbID        string
	PosterURL     string
}

func stripRSSAutomationTrailingMetadata(title string) string {
	original := strings.TrimSpace(title)
	candidate := original
	for {
		runes := []rune(candidate)
		if len(runes) == 0 || runes[len(runes)-1] != ']' {
			break
		}
		depth := 0
		openingIndex := -1
	findOpeningBracket:
		for index := len(runes) - 1; index >= 0; index-- {
			switch runes[index] {
			case ']':
				depth++
			case '[':
				depth--
				if depth == 0 {
					openingIndex = index
					break findOpeningBracket
				}
			}
		}
		if openingIndex < 0 {
			break
		}
		stripped := strings.TrimSpace(string(runes[:openingIndex]))
		if stripped == "" {
			return original
		}
		candidate = stripped
	}
	return candidate
}

func extractRSSAutomationSeasonEpisode(value string) string {
	match := rssAutomationSeasonEpisodeRegexp.FindStringSubmatch(value)
	if len(match) != 3 {
		return ""
	}
	season, seasonErr := strconv.Atoi(match[1])
	episode, episodeErr := strconv.Atoi(match[2])
	if seasonErr != nil || episodeErr != nil {
		return ""
	}
	return fmt.Sprintf("S%02dE%02d", season, episode)
}

func normalizeRSSAutomationSeasonEpisode(value string) string {
	if normalized := extractRSSAutomationSeasonEpisode(value); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(value)
}

func extractRSSAutomationQuality(title string) string {
	sources := []struct {
		pattern *regexp.Regexp
		label   string
	}{
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])WEB[ ._-]?DL(?:[^A-Z0-9]|$)`), "WEB-DL"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])WEB[ ._-]?RIP(?:[^A-Z0-9]|$)`), "WEBRip"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])BLU[ ._-]?RAY(?:[^A-Z0-9]|$)`), "BluRay"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])BDRIP(?:[^A-Z0-9]|$)`), "BDRip"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])REMUX(?:[^A-Z0-9]|$)`), "REMUX"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])HDTV(?:[^A-Z0-9]|$)`), "HDTV"},
	}
	qualityParts := make([]string, 0, 2)
	for _, source := range sources {
		if source.pattern.MatchString(title) {
			qualityParts = append(qualityParts, source.label)
			break
		}
	}

	resolutions := []struct {
		pattern *regexp.Regexp
		label   string
	}{
		{regexp.MustCompile(`(?i)(?:^|[^0-9])4320P(?:[^0-9]|$)|(?:^|[^A-Z0-9])8K(?:[^A-Z0-9]|$)`), "4320p"},
		{regexp.MustCompile(`(?i)(?:^|[^0-9])2160P(?:[^0-9]|$)|(?:^|[^A-Z0-9])4K(?:[^A-Z0-9]|$)`), "2160p"},
		{regexp.MustCompile(`(?i)(?:^|[^0-9])1080[PI](?:[^0-9]|$)`), "1080p"},
		{regexp.MustCompile(`(?i)(?:^|[^0-9])720P(?:[^0-9]|$)`), "720p"},
	}
	for _, resolution := range resolutions {
		if resolution.pattern.MatchString(title) {
			qualityParts = append(qualityParts, resolution.label)
			break
		}
	}
	return strings.Join(qualityParts, " ")
}

func normalizeRSSAutomationMediaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tv", "television", "series", "电视剧", "剧集":
		return "电视剧"
	case "movie", "film", "电影":
		return "电影"
	default:
		return strings.TrimSpace(value)
	}
}

func matchRSSAutomationMediaSearchResult(media RSSAutomationMediaMetadata, results []MoviePilotSearchResult) *MoviePilotSearchResult {
	if media.TmdbID != "" {
		for index := range results {
			if strings.TrimSpace(results[index].TmdbID) == media.TmdbID {
				return &results[index]
			}
		}
		return nil
	}
	for index := range results {
		if !strings.EqualFold(strings.TrimSpace(results[index].Title), strings.TrimSpace(media.Title)) {
			continue
		}
		if media.Year == "" || strings.TrimSpace(results[index].Year) == media.Year {
			return &results[index]
		}
	}
	if len(results) == 1 {
		return &results[0]
	}
	return nil
}

func rssAutomationTMDBImageURL(imagePath string) string {
	imagePath = strings.TrimSpace(imagePath)
	if imagePath == "" {
		return ""
	}
	lower := strings.ToLower(imagePath)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return imagePath
	}
	if strings.HasPrefix(imagePath, "//") {
		return "https:" + imagePath
	}
	return "https://image.tmdb.org/t/p/w780/" + strings.TrimLeft(imagePath, "/")
}

func firstRSSAutomationNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
