package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *RSSAutomationService) executeRSSAutomationMoviePilotTitleRecognize(ctx context.Context, node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.moviePilot == nil {
		return nil, errors.New("MoviePilot 媒体识别服务未初始化")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "input"))
	if err != nil {
		return nil, err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("待识别标题为空")
	}
	if len(input) > 4096 {
		return nil, errors.New("待识别标题超过 4096 字节")
	}
	tmdbID, err := resolveRSSAutomationOptionalTMDBID(runContext, rssAutomationConfigString(node.Config, "tmdb_id"))
	if err != nil {
		return nil, err
	}
	category := ""
	if value, ok := resolveRSSAutomationReference(runContext, "$item.category"); ok {
		category = strings.TrimSpace(fmt.Sprint(value))
	}

	media, recognizeInput, responded, recognized, recognizeErr := recognizeRSSAutomationTitle(s.moviePilot, input, category, tmdbID)
	output := rssAutomationTitleRecognitionOutput(input, recognizeInput, tmdbID, media)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return output, ctxErr
	}
	if recognized {
		output["selected_port"] = "success"
		return output, nil
	}

	output["selected_port"] = "failure"
	reason := "MoviePilot 未返回可识别的媒体信息"
	if tmdbID != "" && responded {
		reason = "MoviePilot 没有识别为指定的 TMDB ID " + tmdbID
	}
	if recognizeErr != nil {
		reason = recognizeErr.Error()
	}
	output["error"] = reason
	if !responded && recognizeErr != nil {
		return output, fmt.Errorf("MoviePilot 标题识别请求失败: %w", recognizeErr)
	}
	return output, nil
}

func recognizeRSSAutomationTitle(recognizer RSSAutomationMediaRecognizer, title, category, tmdbID string) (RSSAutomationMediaMetadata, string, bool, bool, error) {
	media := RSSAutomationMediaMetadata{
		Title: strings.TrimSpace(title), Category: strings.TrimSpace(category),
		SeasonEpisode: extractRSSAutomationSeasonEpisode(title), Quality: extractRSSAutomationQuality(title),
	}
	candidates := rssAutomationTitleRecognitionCandidates(title, tmdbID)
	requestErrors := make([]error, 0)
	responded := false
	lastInput := ""
	for _, candidate := range candidates {
		lastInput = candidate
		info, _, err := recognizer.RecognizeTitle(candidate)
		if err != nil {
			requestErrors = append(requestErrors, err)
			continue
		}
		responded = true
		recognizedID := strings.TrimSpace(info.TmdbID)
		if strings.TrimSpace(info.Title) == "" && recognizedID == "" {
			continue
		}
		if tmdbID != "" && recognizedID != tmdbID {
			continue
		}

		media.Title = firstRSSAutomationNonEmpty(info.Title, info.TitleYear, title)
		media.Year = strings.TrimSpace(info.Year)
		media.MediaType = normalizeRSSAutomationMediaType(info.MediaType)
		media.Category = firstRSSAutomationNonEmpty(info.Category, strings.Join(info.Genres, "、"), category)
		media.SeasonEpisode = firstRSSAutomationNonEmpty(media.SeasonEpisode, normalizeRSSAutomationSeasonEpisode(info.SeasonEpisode))
		if media.Quality == "" {
			media.Quality = extractRSSAutomationQuality(strings.TrimSpace(info.ResourceType + " " + info.ResourcePix))
		}
		media.Rating = info.Rating
		media.TmdbID = recognizedID
		media.PosterURL = rssAutomationTMDBImageURL(firstRSSAutomationNonEmpty(info.BackdropPath, info.PosterPath))

		if media.PosterURL == "" && media.Title != "" {
			if results, searchErr := recognizer.SearchMedia(media.Title, 8); searchErr == nil {
				if match := matchRSSAutomationMediaSearchResult(media, results); match != nil {
					media.Year = firstRSSAutomationNonEmpty(media.Year, match.Year)
					media.MediaType = firstRSSAutomationNonEmpty(media.MediaType, normalizeRSSAutomationMediaType(match.MediaType))
					media.Category = firstRSSAutomationNonEmpty(media.Category, match.Category, strings.Join(match.Genres, "、"), category)
					media.TmdbID = firstRSSAutomationNonEmpty(media.TmdbID, match.TmdbID)
					if media.Rating <= 0 {
						media.Rating = match.Rating
					}
					media.PosterURL = rssAutomationTMDBImageURL(firstRSSAutomationNonEmpty(match.BackdropPath, match.PosterPath))
				}
			}
		}
		return media, candidate, true, true, nil
	}
	if !responded && len(requestErrors) > 0 {
		return media, lastInput, false, false, errors.Join(requestErrors...)
	}
	return media, lastInput, responded, false, nil
}

func rssAutomationTitleRecognitionCandidates(title, tmdbID string) []string {
	title = strings.TrimSpace(title)
	candidates := make([]string, 0, 3)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		candidates = append(candidates, value)
	}
	if tmdbID != "" {
		cleaned := strings.TrimSpace(rssAutomationTMDBMarkerPattern.ReplaceAllString(title, ""))
		add(strings.TrimRight(cleaned, " ._-") + ".{tmdb-" + tmdbID + "}")
	}
	add(title)
	add(stripRSSAutomationTrailingMetadata(title))
	return candidates
}

func rssAutomationTitleRecognitionOutput(input, recognizeInput, requestedTMDBID string, media RSSAutomationMediaMetadata) map[string]any {
	return map[string]any{
		"input": input, "recognize_input": recognizeInput, "requested_tmdb_id": requestedTMDBID,
		"tmdb_id": strings.TrimSpace(media.TmdbID), "title": strings.TrimSpace(media.Title),
		"year": strings.TrimSpace(media.Year), "media_type": strings.TrimSpace(media.MediaType),
		"category": strings.TrimSpace(media.Category), "season_episode": strings.TrimSpace(media.SeasonEpisode),
		"rating": media.Rating, "quality": strings.TrimSpace(media.Quality),
		"poster_url": strings.TrimSpace(media.PosterURL), "file_count": 1,
	}
}
