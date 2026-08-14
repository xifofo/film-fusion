package service

import (
	"encoding/json"
	"errors"
	"strings"

	"film-fusion/app/model"
)

type RSSAutomationEntryHistoryItem struct {
	Entry              model.RSSAutomationEntry `json:"entry"`
	SourceName         string                   `json:"source_name"`
	Matched            bool                     `json:"matched"`
	Legacy             bool                     `json:"legacy"`
	RuleName           string                   `json:"rule_name,omitempty"`
	LatestRun          *model.RSSAutomationRun  `json:"latest_run,omitempty"`
	MediaTitle         string                   `json:"media_title,omitempty"`
	MediaYear          string                   `json:"media_year,omitempty"`
	MediaType          string                   `json:"media_type,omitempty"`
	MediaCategory      string                   `json:"media_category,omitempty"`
	SeasonEpisode      string                   `json:"season_episode,omitempty"`
	Rating             float64                  `json:"rating,omitempty"`
	Quality            string                   `json:"quality,omitempty"`
	TMDBID             string                   `json:"tmdb_id,omitempty"`
	PosterURL          string                   `json:"poster_url,omitempty"`
	RecognitionError   string                   `json:"recognition_error,omitempty"`
	NotificationStatus string                   `json:"notification_status,omitempty"`
	NotificationError  string                   `json:"notification_error,omitempty"`
}

type RSSAutomationEntryHistory struct {
	Items []RSSAutomationEntryHistoryItem `json:"items"`
	Total int64                           `json:"total"`
}

type rssAutomationLegacyEntryMetadata struct {
	SourceName         string  `json:"source_name,omitempty"`
	RuleName           string  `json:"rule_name,omitempty"`
	MediaTitle         string  `json:"media_title,omitempty"`
	MediaYear          string  `json:"media_year,omitempty"`
	MediaType          string  `json:"media_type,omitempty"`
	MediaCategory      string  `json:"media_category,omitempty"`
	SeasonEpisode      string  `json:"season_episode,omitempty"`
	Rating             float64 `json:"rating,omitempty"`
	Quality            string  `json:"quality,omitempty"`
	TMDBID             string  `json:"tmdb_id,omitempty"`
	PosterURL          string  `json:"poster_url,omitempty"`
	RecognitionError   string  `json:"recognition_error,omitempty"`
	NotificationStatus string  `json:"notification_status,omitempty"`
	NotificationError  string  `json:"notification_error,omitempty"`
}

func (s *RSSAutomationService) ListEntryHistory(filter string, sourceID uint, limit, offset int) (RSSAutomationEntryHistory, error) {
	result := RSSAutomationEntryHistory{Items: []RSSAutomationEntryHistoryItem{}}
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && filter != "matched" {
		return result, errors.New("条目筛选只支持 all 或 matched")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := s.db.Model(&model.RSSAutomationEntry{})
	if sourceID > 0 {
		query = query.Where("source_id = ?", sourceID)
	}
	if filter == "matched" {
		matchedEntries := s.db.Table("rss_automation_runs AS history_run").
			Select("history_run.entry_id").
			Joins("JOIN rss_automation_node_runs AS history_node ON history_node.run_id = history_run.id").
			Where("history_node.node_type IN ?", rssAutomationHistoryActionTypes()).
			Where("history_node.status IN ?", []string{model.RSSAutomationNodeSucceeded, model.RSSAutomationNodeFailed}).
			Group("history_run.entry_id")
		query = query.Where("legacy_matched = ? OR id IN (?)", true, matchedEntries)
	}
	if err := query.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var entries []model.RSSAutomationEntry
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&entries).Error; err != nil {
		return result, err
	}
	if len(entries) == 0 {
		return result, nil
	}

	sourceIDs := make([]uint, 0, len(entries))
	entryIDs := make([]uint, 0, len(entries))
	for _, entry := range entries {
		sourceIDs = append(sourceIDs, entry.SourceID)
		entryIDs = append(entryIDs, entry.ID)
	}
	var sources []model.RSSAutomationSource
	if err := s.db.Where("id IN ?", sourceIDs).Find(&sources).Error; err != nil {
		return result, err
	}
	sourceNames := make(map[uint]string, len(sources))
	for _, source := range sources {
		sourceNames[source.ID] = source.Name
	}

	var runs []model.RSSAutomationRun
	if err := s.db.Where("entry_id IN ?", entryIDs).Order("id DESC").Find(&runs).Error; err != nil {
		return result, err
	}
	latestRuns := make(map[uint]model.RSSAutomationRun, len(entries))
	runIDs := make([]uint, 0, len(entries))
	for _, run := range runs {
		if _, exists := latestRuns[run.EntryID]; exists {
			continue
		}
		latestRuns[run.EntryID] = run
		runIDs = append(runIDs, run.ID)
	}
	nodeRunsByRun := make(map[uint][]model.RSSAutomationNodeRun, len(runIDs))
	if len(runIDs) > 0 {
		var nodeRuns []model.RSSAutomationNodeRun
		if err := s.db.Where("run_id IN ?", runIDs).Order("id ASC").Find(&nodeRuns).Error; err != nil {
			return result, err
		}
		for _, nodeRun := range nodeRuns {
			nodeRunsByRun[nodeRun.RunID] = append(nodeRunsByRun[nodeRun.RunID], nodeRun)
		}
	}

	result.Items = make([]RSSAutomationEntryHistoryItem, 0, len(entries))
	for _, entry := range entries {
		item := RSSAutomationEntryHistoryItem{
			Entry: entry, SourceName: sourceNames[entry.SourceID], Matched: entry.LegacyMatched,
		}
		applyRSSAutomationLegacyHistory(&item, entry.LegacyMetadataJSON)
		if run, exists := latestRuns[entry.ID]; exists {
			runCopy := run
			item.LatestRun = &runCopy
			applyRSSAutomationNodeHistory(&item, nodeRunsByRun[run.ID])
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func rssAutomationHistoryActionTypes() []string {
	return []string{
		RSSAutomationNodeQBittorrent, RSSAutomationNodeWaitQBittorrent,
		RSSAutomationNodeOffline115, RSSAutomationNodeOffline115OpenAPI,
		RSSAutomationNodeWait115, RSSAutomationNodeMoviePilotTitle, RSSAutomationNodeMediaExists,
		RSSAutomationNodeHDHiveQuery, RSSAutomationNodeHDHiveUnlock,
		RSSAutomationNodeMoviePilotRecognize, RSSAutomationNodeOrganizeStrm,
		RSSAutomationNodeStrmVerify, RSSAutomationNodeStrmRegenerate, RSSAutomationNodeEmbyRefreshWait,
		RSSAutomationNodeHTTPRequest, RSSAutomationNodeNotification,
	}
}

func applyRSSAutomationLegacyHistory(item *RSSAutomationEntryHistoryItem, raw string) {
	if item == nil || strings.TrimSpace(raw) == "" {
		return
	}
	var legacy rssAutomationLegacyEntryMetadata
	if json.Unmarshal([]byte(raw), &legacy) != nil {
		return
	}
	item.Legacy = true
	item.SourceName = firstRSSAutomationHistoryString(item.SourceName, legacy.SourceName)
	item.RuleName = legacy.RuleName
	item.MediaTitle = legacy.MediaTitle
	item.MediaYear = legacy.MediaYear
	item.MediaType = legacy.MediaType
	item.MediaCategory = legacy.MediaCategory
	item.SeasonEpisode = legacy.SeasonEpisode
	item.Rating = legacy.Rating
	item.Quality = legacy.Quality
	item.TMDBID = legacy.TMDBID
	item.PosterURL = legacy.PosterURL
	item.RecognitionError = legacy.RecognitionError
	item.NotificationStatus = legacy.NotificationStatus
	item.NotificationError = legacy.NotificationError
}

func applyRSSAutomationNodeHistory(item *RSSAutomationEntryHistoryItem, nodeRuns []model.RSSAutomationNodeRun) {
	if item == nil {
		return
	}
	actionTypes := make(map[string]struct{})
	for _, nodeType := range rssAutomationHistoryActionTypes() {
		actionTypes[nodeType] = struct{}{}
	}
	for _, nodeRun := range nodeRuns {
		if _, action := actionTypes[nodeRun.NodeType]; action && (nodeRun.Status == model.RSSAutomationNodeSucceeded || nodeRun.Status == model.RSSAutomationNodeFailed) {
			item.Matched = true
		}
		if nodeRun.NodeType == RSSAutomationNodeMoviePilotTitle || nodeRun.NodeType == RSSAutomationNodeMoviePilotRecognize {
			applyRSSAutomationRecognitionOutput(item, nodeRun)
		}
		if nodeRun.NodeType == RSSAutomationNodeNotification {
			applyRSSAutomationNotificationOutput(item, nodeRun)
		}
	}
}

func applyRSSAutomationRecognitionOutput(item *RSSAutomationEntryHistoryItem, nodeRun model.RSSAutomationNodeRun) {
	var output map[string]any
	if json.Unmarshal([]byte(nodeRun.OutputJSON), &output) != nil {
		if item.RecognitionError == "" {
			item.RecognitionError = nodeRun.ErrorMessage
		}
		return
	}
	item.MediaTitle = firstRSSAutomationHistoryString(rssAutomationAnyString(output["title"]), item.MediaTitle)
	item.MediaYear = firstRSSAutomationHistoryString(rssAutomationAnyString(output["year"]), item.MediaYear)
	item.MediaType = firstRSSAutomationHistoryString(rssAutomationAnyString(output["media_type"]), item.MediaType)
	item.MediaCategory = firstRSSAutomationHistoryString(rssAutomationAnyString(output["category"]), item.MediaCategory)
	item.SeasonEpisode = firstRSSAutomationHistoryString(rssAutomationAnyString(output["season_episode"]), item.SeasonEpisode)
	item.Quality = firstRSSAutomationHistoryString(rssAutomationAnyString(output["quality"]), item.Quality)
	item.TMDBID = firstRSSAutomationHistoryString(rssAutomationAnyString(output["tmdb_id"]), item.TMDBID)
	item.PosterURL = firstRSSAutomationHistoryString(
		rssAutomationAnyString(output["poster_url"]),
		rssTMDBImageURL(firstRSSAutomationHistoryString(rssAutomationAnyString(output["backdrop_path"]), rssAutomationAnyString(output["poster_path"]))),
		item.PosterURL,
	)
	if rating, ok := rssAutomationNumber(output["rating"]); ok && rating > 0 {
		item.Rating = rating
	}
	item.RecognitionError = firstRSSAutomationHistoryString(nodeRun.ErrorMessage, rssAutomationAnyString(output["error"]), item.RecognitionError)
}

func applyRSSAutomationNotificationOutput(item *RSSAutomationEntryHistoryItem, nodeRun model.RSSAutomationNodeRun) {
	var output map[string]any
	_ = json.Unmarshal([]byte(nodeRun.OutputJSON), &output)
	if nodeRun.Status == model.RSSAutomationNodeFailed {
		item.NotificationStatus = model.RSSNotificationFailed
		item.NotificationError = nodeRun.ErrorMessage
		return
	}
	if skipped, _ := output["skipped"].(bool); skipped {
		item.NotificationStatus = model.RSSNotificationSkipped
		item.NotificationError = rssAutomationAnyString(output["skip_reason"])
		return
	}
	if partial, _ := output["partial"].(bool); partial {
		item.NotificationStatus = model.RSSNotificationPartial
		item.NotificationError = rssAutomationAnyString(output["warning"])
		return
	}
	if nodeRun.Status == model.RSSAutomationNodeSucceeded {
		item.NotificationStatus = model.RSSNotificationSent
	}
}

func firstRSSAutomationHistoryString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
