package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	automationRSSSampleLimit      = 20
	automationRSSDefaultUserAgent = "FilmFusion-RSS-Automation/1.0"
)

func (s *AutomationService) SampleRSSSource(ctx context.Context, input AutomationSourceInput) (RSSAutomationParsedFeed, error) {
	input.TriggerType = model.AutomationTriggerRSS
	input, _, err := s.validateAutomationSourceInput(input)
	if err != nil {
		return RSSAutomationParsedFeed{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.FeedURL, nil)
	if err != nil {
		return RSSAutomationParsedFeed{}, errors.New("创建 RSS 请求失败")
	}
	s.setAutomationRSSRequestHeaders(req)
	resp, err := s.automationHTTPClient().Do(req)
	if err != nil {
		return RSSAutomationParsedFeed{}, fmt.Errorf("请求 RSS 源失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RSSAutomationParsedFeed{}, fmt.Errorf("RSS 源返回 HTTP %d", resp.StatusCode)
	}
	return ParseRSSAutomationFeed(resp.Body, input.Mapping, automationRSSSampleLimit)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.FeedURL, nil)
	if err != nil {
		return result, err
	}
	s.setAutomationRSSRequestHeaders(req)
	if source.Initialized && source.ETag != "" {
		req.Header.Set("If-None-Match", source.ETag)
	}
	if source.Initialized && source.LastModified != "" {
		req.Header.Set("If-Modified-Since", source.LastModified)
	}
	hasConditionalHeaders := req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != ""
	resp, err := s.automationHTTPClient().Do(req)
	if err != nil {
		s.recordAutomationRSSFailure(source.ID, err)
		return result, fmt.Errorf("请求 RSS 源失败: %w", err)
	}
	conditionalFallback := false
	if hasConditionalHeaders && resp.StatusCode >= http.StatusInternalServerError {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		fallbackReq, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, source.FeedURL, nil)
		if requestErr != nil {
			s.recordAutomationRSSFailure(source.ID, requestErr)
			return result, requestErr
		}
		s.setAutomationRSSRequestHeaders(fallbackReq)
		resp, err = s.automationHTTPClient().Do(fallbackReq)
		if err != nil {
			s.recordAutomationRSSFailure(source.ID, err)
			return result, fmt.Errorf("无条件重试 RSS 源失败: %w", err)
		}
		conditionalFallback = true
	}
	defer resp.Body.Close()
	checkedAt := time.Now()
	if resp.StatusCode == http.StatusNotModified {
		result.NotModified = true
		s.recordAutomationRSSSuccess(source.ID, checkedAt, source.Initialized, source.ETag, source.LastModified)
		return result, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("RSS 源返回 HTTP %d", resp.StatusCode)
		s.recordAutomationRSSFailure(source.ID, err)
		return result, err
	}
	feed, err := ParseRSSAutomationFeed(io.LimitReader(resp.Body, maxRSSAutomationBodyBytes+1), mapping, 0)
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
	newETag := strings.TrimSpace(resp.Header.Get("ETag"))
	newLastModified := strings.TrimSpace(resp.Header.Get("Last-Modified"))
	if conditionalFallback {
		newETag = ""
		newLastModified = ""
	}
	s.recordAutomationRSSSuccess(source.ID, checkedAt, true, newETag, newLastModified)
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

func (s *AutomationService) recordAutomationRSSSuccess(sourceID uint, checkedAt time.Time, initialized bool, etag, lastModified string) {
	_ = s.db.Model(&model.AutomationSource{}).Where("id = ?", sourceID).Updates(map[string]any{
		"last_checked_at": checkedAt, "last_success_at": checkedAt, "last_error": "",
		"initialized": initialized, "etag": etag, "last_modified": lastModified,
	}).Error
}

func (s *AutomationService) automationHTTPClient() *http.Client {
	if s != nil && s.httpClient != nil {
		return s.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *AutomationService) setAutomationRSSRequestHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", s.automationRSSUserAgent())
}

func (s *AutomationService) automationRSSUserAgent() string {
	if s != nil && s.db != nil && s.db.Migrator().HasTable(&model.SystemConfig{}) {
		var setting model.SystemConfig
		if err := s.db.Where("config_key IN ?", []string{"rss_automation.user_agent", "automation.rss_user_agent"}).
			Order("CASE config_key WHEN 'automation.rss_user_agent' THEN 0 ELSE 1 END").First(&setting).Error; err == nil {
			if value := strings.TrimSpace(setting.ConfigValue); value != "" && !strings.ContainsAny(value, "\r\n") {
				return value
			}
		}
	}
	return automationRSSDefaultUserAgent
}
