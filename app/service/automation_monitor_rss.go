package service

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const automationRSSSampleLimit = 20

func (s *AutomationService) SampleRSSSource(ctx context.Context, input AutomationSourceInput) (RSSAutomationParsedFeed, error) {
	input.TriggerType = model.AutomationTriggerRSS
	input, _, err := s.validateAutomationSourceInput(input)
	if err != nil {
		return RSSAutomationParsedFeed{}, err
	}
	cached, err := s.rssFeedService().readRSSFeed(ctx, input.FeedURL, true)
	if err != nil {
		return RSSAutomationParsedFeed{}, err
	}
	return parseCachedRSSFeed(cached, input.Mapping, automationRSSSampleLimit)
}

func (s *AutomationService) scanAutomationRSS(ctx context.Context, source model.AutomationSource) (result AutomationScanResult, returnErr error) {
	result.SourceID = source.ID
	result.SourceName = source.Name
	result.TriggerType = source.TriggerType
	result.Baseline = !source.Initialized
	defer func() { result.CompletedAt = time.Now() }()

	mapping, err := ParseRSSAutomationMapping(source.MappingJSON)
	if err != nil {
		s.recordAutomationRSSFailure(source.ID, err)
		return result, err
	}
	cached, err := s.rssFeedService().readRSSFeed(ctx, source.FeedURL, true)
	if err != nil {
		s.recordAutomationRSSFailure(source.ID, err)
		return result, err
	}
	result.RSSCacheMetadata = cached.RSSCacheMetadata
	checkedAt := time.Now()
	feed, err := parseCachedRSSFeed(cached, mapping, 0)
	if err != nil {
		s.recordAutomationRSSFailure(source.ID, err)
		return result, err
	}
	result.Fetched = len(feed.Items)
	for _, parsedItem := range feed.Items {
		result.ParseWarnings += len(parsedItem.Errors)
		if len(parsedItem.Errors) > 0 {
			continue
		}
		_, created, createdRuns, createErr := s.persistAutomationRSSEntry(
			source.ID,
			parsedItem.Fields,
			checkedAt,
			result.Baseline,
		)
		if createErr != nil {
			s.recordAutomationRSSFailure(source.ID, createErr)
			return result, createErr
		}
		if !created {
			continue
		}
		result.NewEntries++
		result.CreatedEntries++
		result.CreatedRuns += createdRuns
	}
	updates := map[string]any{
		"last_checked_at": checkedAt, "initialized": true,
		"last_success_at": cached.CacheFetchedAt, "last_error": cached.CacheWarning,
	}
	if err := s.db.Model(&model.AutomationSource{}).Where("id = ?", source.ID).Updates(updates).Error; err != nil {
		return result, err
	}
	s.wakeExecution()
	return result, nil
}

func (s *AutomationService) persistAutomationRSSEntry(
	sourceID uint,
	fields map[string]any,
	discoveredAt time.Time,
	baseline bool,
) (model.AutomationEntry, bool, int, error) {
	fields = cloneAutomationRSSFields(fields)
	fields["source"] = "rss"
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return model.AutomationEntry{}, false, 0, err
	}
	fingerprint := rssAutomationFingerprint(sourceID, fields)
	guid := firstRSSAutomationString(fields, "guid")
	title := firstRSSAutomationString(fields, "title")
	detailURL := firstRSSAutomationString(fields, "detail_url")
	downloadURL := firstRSSAutomationString(fields, "download_url")
	entry := model.AutomationEntry{
		SourceID: sourceID, Fingerprint: fingerprint, EventKey: "rss:" + fingerprint,
		ItemID: fingerprint, Title: title, Path: detailURL,
		GUID: guid, DetailURL: detailURL, DownloadURL: downloadURL,
		ContentKey:  rssAutomationContentKey(downloadURL),
		PublishedAt: parseRSSAutomationTime(firstRSSAutomationString(fields, "published_at")),
		Baseline:    baseline, FieldsJSON: string(fieldsJSON), DiscoveredAt: discoveredAt,
	}
	created := false
	createdRuns := 0
	err = s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		created = true
		if baseline {
			return nil
		}
		txService := &AutomationService{db: tx, executionWake: s.executionWake}
		createdRuns, err = txService.createAutomationRuns(entry)
		return err
	})
	if err != nil {
		return entry, false, 0, err
	}
	return entry, created, createdRuns, nil
}

func cloneAutomationRSSFields(fields map[string]any) map[string]any {
	result := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		result[key] = value
	}
	return result
}

func (s *AutomationService) recordAutomationRSSFailure(sourceID uint, refreshErr error) {
	now := time.Now()
	_ = s.db.Model(&model.AutomationSource{}).Where("id = ?", sourceID).Updates(map[string]any{
		"last_checked_at": now, "last_error": refreshErr.Error(),
	}).Error
}

func (s *AutomationService) automationHTTPClient() *http.Client {
	if s != nil && s.httpClient != nil {
		return s.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
