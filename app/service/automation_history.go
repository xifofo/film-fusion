package service

import (
	"encoding/json"
	"errors"
	"strings"

	"film-fusion/app/model"
)

const (
	automationNotificationSent    = "sent"
	automationNotificationPartial = "partial"
	automationNotificationFailed  = "failed"
	automationNotificationSkipped = "skipped"
)

type AutomationEntryHistoryItem struct {
	Entry              model.AutomationEntry `json:"entry"`
	SourceName         string                `json:"source_name"`
	Matched            bool                  `json:"matched"`
	Legacy             bool                  `json:"legacy"`
	RuleName           string                `json:"rule_name,omitempty"`
	LatestRun          *model.AutomationRun  `json:"latest_run,omitempty"`
	MediaTitle         string                `json:"media_title,omitempty"`
	MediaYear          string                `json:"media_year,omitempty"`
	MediaType          string                `json:"media_type,omitempty"`
	MediaCategory      string                `json:"media_category,omitempty"`
	SeasonEpisode      string                `json:"season_episode,omitempty"`
	Rating             float64               `json:"rating,omitempty"`
	Quality            string                `json:"quality,omitempty"`
	TMDBID             string                `json:"tmdb_id,omitempty"`
	PosterURL          string                `json:"poster_url,omitempty"`
	RecognitionError   string                `json:"recognition_error,omitempty"`
	NotificationStatus string                `json:"notification_status,omitempty"`
	NotificationError  string                `json:"notification_error,omitempty"`
}

type AutomationEntryHistory struct {
	Items []AutomationEntryHistoryItem `json:"items"`
	Total int64                        `json:"total"`
}

type automationLegacyEntryMetadata struct {
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

func (s *AutomationService) ListEntryHistory(filter string, sourceID uint, limit, offset int) (AutomationEntryHistory, error) {
	result := AutomationEntryHistory{Items: []AutomationEntryHistoryItem{}}
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

	query := s.db.Model(&model.AutomationEntry{})
	if sourceID > 0 {
		query = query.Where("source_id = ?", sourceID)
	}
	if filter == "matched" {
		matchedEntries := s.db.Table("automation_runs AS history_run").
			Select("history_run.entry_id").
			Joins("JOIN automation_node_runs AS history_node ON history_node.run_id = history_run.id").
			Where("history_node.node_type IN ?", automationHistoryActionTypes()).
			Where("history_node.status IN ?", []string{model.AutomationNodeSucceeded, model.AutomationNodeFailed}).
			Group("history_run.entry_id")
		query = query.Where("legacy_matched = ? OR id IN (?)", true, matchedEntries)
	}
	if err := query.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var entries []model.AutomationEntry
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
	var sources []model.AutomationSource
	if err := s.db.Where("id IN ?", sourceIDs).Find(&sources).Error; err != nil {
		return result, err
	}
	sourceNames := make(map[uint]string, len(sources))
	for _, source := range sources {
		sourceNames[source.ID] = source.Name
	}

	var runs []model.AutomationRun
	if err := s.db.Where("entry_id IN ?", entryIDs).Order("id DESC").Find(&runs).Error; err != nil {
		return result, err
	}
	latestRuns := make(map[uint]model.AutomationRun, len(entries))
	runIDs := make([]uint, 0, len(entries))
	for _, run := range runs {
		if _, exists := latestRuns[run.EntryID]; exists {
			continue
		}
		latestRuns[run.EntryID] = run
		runIDs = append(runIDs, run.ID)
	}
	nodeRunsByRun := make(map[uint][]model.AutomationNodeRun, len(runIDs))
	if len(runIDs) > 0 {
		var nodeRuns []model.AutomationNodeRun
		if err := s.db.Where("run_id IN ?", runIDs).Order("id ASC").Find(&nodeRuns).Error; err != nil {
			return result, err
		}
		for _, nodeRun := range nodeRuns {
			nodeRunsByRun[nodeRun.RunID] = append(nodeRunsByRun[nodeRun.RunID], nodeRun)
		}
	}

	result.Items = make([]AutomationEntryHistoryItem, 0, len(entries))
	for _, entry := range entries {
		item := AutomationEntryHistoryItem{
			Entry: entry, SourceName: sourceNames[entry.SourceID], Matched: entry.LegacyMatched,
		}
		applyAutomationLegacyHistory(&item, entry.LegacyMetadataJSON)
		if run, exists := latestRuns[entry.ID]; exists {
			runCopy := run
			item.LatestRun = &runCopy
			applyAutomationNodeHistory(&item, nodeRunsByRun[run.ID])
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func applyAutomationLegacyHistory(item *AutomationEntryHistoryItem, raw string) {
	if item == nil || strings.TrimSpace(raw) == "" {
		return
	}
	var legacy automationLegacyEntryMetadata
	if json.Unmarshal([]byte(raw), &legacy) != nil {
		return
	}
	item.Legacy = true
	item.SourceName = firstAutomationHistoryString(item.SourceName, legacy.SourceName)
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

func automationHistoryActionTypes() []string {
	return []string{
		AutomationNodeQBittorrent, AutomationNodeWaitQBittorrent, AutomationNodeStopQBittorrent, AutomationNodeDeleteQBittorrent,
		AutomationNodeOffline115, AutomationNodeOffline115OpenAPI,
		AutomationNodeTorrentToMagnet, AutomationNodeTorrent115OpenAPI, AutomationNodeOffline115BTOpenAPI, AutomationNodeList115TasksOpenAPI,
		AutomationNodeQuota115OpenAPI, AutomationNodeDelete115TaskOpenAPI, AutomationNodeClear115TasksOpenAPI,
		AutomationNodeWait115, AutomationNodeRename115OpenAPI, AutomationNodeMoviePilotTitle, AutomationNodeFilmFusionRecognize, AutomationNodeMoviePilotTransfer, AutomationNodeMediaExists,
		AutomationNodeMoviePilotRecognize, AutomationNodeOrganizeStrm,
		AutomationNodeStrmVerify, AutomationNodeStrmRegenerate, AutomationNodeEmbyRefreshWait,
		AutomationNodeHTTPRequest, AutomationNodeNotification,
	}
}

func applyAutomationNodeHistory(item *AutomationEntryHistoryItem, nodeRuns []model.AutomationNodeRun) {
	if item == nil {
		return
	}
	actionTypes := make(map[string]struct{})
	for _, nodeType := range automationHistoryActionTypes() {
		actionTypes[nodeType] = struct{}{}
	}
	for _, nodeRun := range nodeRuns {
		if _, action := actionTypes[nodeRun.NodeType]; action && (nodeRun.Status == model.AutomationNodeSucceeded || nodeRun.Status == model.AutomationNodeFailed) {
			item.Matched = true
		}
		if nodeRun.NodeType == AutomationNodeMoviePilotTitle || nodeRun.NodeType == AutomationNodeMoviePilotRecognize || nodeRun.NodeType == AutomationNodeFilmFusionRecognize {
			applyAutomationRecognitionOutput(item, nodeRun)
		}
		if nodeRun.NodeType == AutomationNodeNotification {
			applyAutomationNotificationOutput(item, nodeRun)
		}
	}
}

func applyAutomationRecognitionOutput(item *AutomationEntryHistoryItem, nodeRun model.AutomationNodeRun) {
	var output map[string]any
	if json.Unmarshal([]byte(nodeRun.OutputJSON), &output) != nil {
		if item.RecognitionError == "" {
			item.RecognitionError = nodeRun.ErrorMessage
		}
		return
	}
	item.MediaTitle = firstAutomationHistoryString(automationAnyString(output["title"]), item.MediaTitle)
	item.MediaYear = firstAutomationHistoryString(automationAnyString(output["year"]), item.MediaYear)
	item.MediaType = firstAutomationHistoryString(automationAnyString(output["media_type"]), item.MediaType)
	item.MediaCategory = firstAutomationHistoryString(automationAnyString(output["category"]), item.MediaCategory)
	item.SeasonEpisode = firstAutomationHistoryString(automationAnyString(output["season_episode"]), item.SeasonEpisode)
	item.Quality = firstAutomationHistoryString(automationAnyString(output["quality"]), item.Quality)
	item.TMDBID = firstAutomationHistoryString(automationAnyString(output["tmdb_id"]), item.TMDBID)
	item.PosterURL = firstAutomationHistoryString(
		automationAnyString(output["poster_url"]),
		automationTMDBImageURL(firstAutomationHistoryString(automationAnyString(output["backdrop_path"]), automationAnyString(output["poster_path"]))),
		item.PosterURL,
	)
	if rating, ok := automationNumber(output["rating"]); ok && rating > 0 {
		item.Rating = rating
	}
	item.RecognitionError = firstAutomationHistoryString(nodeRun.ErrorMessage, automationAnyString(output["error"]), item.RecognitionError)
}

func applyAutomationNotificationOutput(item *AutomationEntryHistoryItem, nodeRun model.AutomationNodeRun) {
	var output map[string]any
	_ = json.Unmarshal([]byte(nodeRun.OutputJSON), &output)
	if nodeRun.Status == model.AutomationNodeFailed {
		item.NotificationStatus = automationNotificationFailed
		item.NotificationError = nodeRun.ErrorMessage
		return
	}
	if skipped, _ := output["skipped"].(bool); skipped {
		item.NotificationStatus = automationNotificationSkipped
		item.NotificationError = automationAnyString(output["skip_reason"])
		return
	}
	if partial, _ := output["partial"].(bool); partial {
		item.NotificationStatus = automationNotificationPartial
		item.NotificationError = automationAnyString(output["warning"])
		return
	}
	if nodeRun.Status == model.AutomationNodeSucceeded {
		item.NotificationStatus = automationNotificationSent
	}
}

func firstAutomationHistoryString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
