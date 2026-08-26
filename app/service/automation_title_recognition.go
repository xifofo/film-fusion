package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *AutomationService) executeAutomationMoviePilotTitleRecognize(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.moviePilot == nil {
		return nil, errors.New("MoviePilot 媒体识别服务未初始化")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "input"))
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
	tmdbID, err := resolveAutomationOptionalTMDBID(runContext, automationConfigString(node.Config, "tmdb_id"))
	if err != nil {
		return nil, err
	}
	if tmdbID == "" {
		tmdbID = extractLocalTMDBMarkerID(input)
	}
	category := ""
	if value, ok := resolveAutomationReference(runContext, "$item.category"); ok {
		category = strings.TrimSpace(fmt.Sprint(value))
	}

	recognizer := automationRecognitionSourceAdapter{
		base: s.moviePilot, source: automationConfigString(node.Config, "recognition_source"),
	}
	media, recognizeInput, responded, recognized, recognizeErr := recognizeAutomationTitle(recognizer, input, category, tmdbID)
	if recognized {
		media = s.reclassifyAutomationMoviePilotTitle(ctx, input, media)
	}
	output := automationTitleRecognitionOutput(input, recognizeInput, tmdbID, media)
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

// reclassifyAutomationMoviePilotTitle keeps MoviePilot responsible for
// identifying the title, then uses the returned TMDB identity to load complete
// metadata and run FilmFusion's saved category rules. MoviePilot recognition
// responses do not always contain every field used by category.yaml (notably
// genre_ids), so classifying the partial response can select a later fallback
// category even though the identified media itself is correct.
func (s *AutomationService) reclassifyAutomationMoviePilotTitle(
	ctx context.Context,
	input string,
	media AutomationMediaMetadata,
) AutomationMediaMetadata {
	if s == nil || s.localMedia == nil || strings.TrimSpace(media.TmdbID) == "" {
		return media
	}
	recognizeInput := automationMoviePilotCategoryInput(input, media)
	result, err := s.localMedia.Recognize(ctx, recognizeInput, MediaRecognitionOptions{
		Mode: MediaRecognitionModeTitle, LookupTMDB: true,
	})
	if err != nil {
		if s.log != nil && ctx.Err() == nil {
			s.log.Warnf("[AUTOMATION] MP 标题识别分类补全失败，保留 MoviePilot 分类: %v", err)
		}
		return media
	}
	localTMDBID := strings.TrimSpace(firstAutomationNonEmpty(result.MediaInfo.TMDBID, result.MetaInfo.TMDBID))
	if result.TMDBStatus != "matched_by_id" || localTMDBID != strings.TrimSpace(media.TmdbID) {
		if s.log != nil && ctx.Err() == nil {
			s.log.Warnf(
				"[AUTOMATION] MP 标题识别未能按 TMDB %s 补全分类，保留 MoviePilot 分类（状态：%s）",
				media.TmdbID,
				firstAutomationNonEmpty(result.TMDBStatus, "unknown"),
			)
		}
		return media
	}
	if category := strings.TrimSpace(result.MediaInfo.Category); category != "" {
		media.Category = category
	}
	return media
}

func automationMoviePilotCategoryInput(input string, media AutomationMediaMetadata) string {
	parts := make([]string, 0, 4)
	if title := strings.TrimSpace(media.Title); title != "" {
		parts = append(parts, title)
	} else if input = strings.TrimSpace(input); input != "" {
		parts = append(parts, input)
	}
	if year := strings.TrimSpace(media.Year); year != "" {
		parts = append(parts, year)
	}
	if seasonEpisode := strings.TrimSpace(media.SeasonEpisode); seasonEpisode != "" {
		parts = append(parts, seasonEpisode)
	}
	marker := "tmdbid=" + strings.TrimSpace(media.TmdbID)
	if mediaType := normalizeLocalMediaType(media.MediaType); mediaType == "movie" || mediaType == "tv" {
		marker += ";type=" + mediaType
	}
	parts = append(parts, "{["+marker+"]}")
	return strings.Join(parts, ".")
}

func recognizeAutomationTitle(recognizer AutomationMediaRecognizer, title, category, tmdbID string) (AutomationMediaMetadata, string, bool, bool, error) {
	media := AutomationMediaMetadata{
		Title: strings.TrimSpace(title), Category: strings.TrimSpace(category),
		SeasonEpisode: extractAutomationSeasonEpisode(title), Quality: extractAutomationQuality(title),
	}
	candidates := automationTitleRecognitionCandidates(title, tmdbID)
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

		media.Title = firstAutomationNonEmpty(info.Title, info.TitleYear, title)
		media.Year = strings.TrimSpace(info.Year)
		media.MediaType = normalizeAutomationMediaType(info.MediaType)
		media.Category = firstAutomationNonEmpty(info.Category, strings.Join(info.Genres, "、"), category)
		media.SeasonEpisode = firstAutomationNonEmpty(media.SeasonEpisode, normalizeAutomationSeasonEpisode(info.SeasonEpisode))
		if media.Quality == "" {
			media.Quality = extractAutomationQuality(strings.TrimSpace(info.ResourceType + " " + info.ResourcePix))
		}
		media.Rating = info.Rating
		media.TmdbID = recognizedID
		media.PosterURL = automationTMDBImageURL(firstAutomationNonEmpty(info.BackdropPath, info.PosterPath))

		if media.PosterURL == "" && media.Title != "" {
			if results, searchErr := recognizer.SearchMedia(media.Title, 8); searchErr == nil {
				if match := matchAutomationMediaSearchResult(media, results); match != nil {
					media.Year = firstAutomationNonEmpty(media.Year, match.Year)
					media.MediaType = firstAutomationNonEmpty(media.MediaType, normalizeAutomationMediaType(match.MediaType))
					media.Category = firstAutomationNonEmpty(media.Category, match.Category, strings.Join(match.Genres, "、"), category)
					media.TmdbID = firstAutomationNonEmpty(media.TmdbID, match.TmdbID)
					if media.Rating <= 0 {
						media.Rating = match.Rating
					}
					media.PosterURL = automationTMDBImageURL(firstAutomationNonEmpty(match.BackdropPath, match.PosterPath))
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

func automationTitleRecognitionCandidates(title, tmdbID string) []string {
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
		cleaned := strings.TrimSpace(automationTMDBMarkerPattern.ReplaceAllString(title, ""))
		add(strings.TrimRight(cleaned, " ._-") + ".{tmdb-" + tmdbID + "}")
	}
	add(title)
	add(stripAutomationTrailingMetadata(title))
	return candidates
}

func automationTitleRecognitionOutput(input, recognizeInput, requestedTMDBID string, media AutomationMediaMetadata) map[string]any {
	return map[string]any{
		"input": input, "recognize_input": recognizeInput, "requested_tmdb_id": requestedTMDBID,
		"tmdb_id": strings.TrimSpace(media.TmdbID), "title": strings.TrimSpace(media.Title),
		"year": strings.TrimSpace(media.Year), "media_type": strings.TrimSpace(media.MediaType),
		"category": strings.TrimSpace(media.Category), "season_episode": strings.TrimSpace(media.SeasonEpisode),
		"rating": media.Rating, "quality": strings.TrimSpace(media.Quality),
		"poster_url": strings.TrimSpace(media.PosterURL), "file_count": 1,
	}
}
