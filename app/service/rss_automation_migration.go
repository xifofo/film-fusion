package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RSSAutomationLegacyMigrationStatus struct {
	Available           bool  `json:"available"`
	Complete            bool  `json:"complete"`
	SourceCount         int64 `json:"source_count"`
	EnabledSourceCount  int64 `json:"enabled_source_count"`
	MigratedSourceCount int64 `json:"migrated_source_count"`
	PendingSourceCount  int64 `json:"pending_source_count"`
	RuleCount           int64 `json:"rule_count"`
	EnabledRuleCount    int64 `json:"enabled_rule_count"`
	DisabledRuleCount   int64 `json:"disabled_rule_count"`
	ItemCount           int64 `json:"item_count"`
}

type RSSAutomationLegacyMigrationResult struct {
	SourcesMigrated      int                                `json:"sources_migrated"`
	WorkflowsCreated     int                                `json:"workflows_created"`
	EntriesMigrated      int                                `json:"entries_migrated"`
	LegacySourcesStopped int64                              `json:"legacy_sources_stopped"`
	EnabledRulesMigrated int                                `json:"enabled_rules_migrated"`
	DisabledRulesKept    int                                `json:"disabled_rules_kept"`
	Status               RSSAutomationLegacyMigrationStatus `json:"status"`
}

func (s *RSSAutomationService) LegacyMigrationStatus() (RSSAutomationLegacyMigrationStatus, error) {
	status := RSSAutomationLegacyMigrationStatus{}
	if s == nil || s.db == nil {
		return status, errors.New("RSS 自动化数据库未初始化")
	}
	sourceQuery := s.db.Model(&model.RSSMonitorSetting{}).Where("TRIM(feed_url) <> ''")
	if err := sourceQuery.Count(&status.SourceCount).Error; err != nil {
		return status, err
	}
	if err := s.db.Model(&model.RSSMonitorSetting{}).Where("enabled = ? AND TRIM(feed_url) <> ''", true).Count(&status.EnabledSourceCount).Error; err != nil {
		return status, err
	}
	if err := s.db.Table("rss_automation_legacy_migrations AS migration").
		Joins("JOIN rss_automation_sources AS source ON source.id = migration.source_id").
		Joins("JOIN rss_automation_workflows AS workflow ON workflow.id = migration.workflow_id AND workflow.source_id = source.id").
		Count(&status.MigratedSourceCount).Error; err != nil {
		return status, err
	}
	if status.MigratedSourceCount > status.SourceCount {
		status.MigratedSourceCount = status.SourceCount
	}
	status.PendingSourceCount = status.SourceCount - status.MigratedSourceCount
	if err := s.db.Model(&model.RSSNotificationRule{}).Count(&status.RuleCount).Error; err != nil {
		return status, err
	}
	if err := s.db.Model(&model.RSSNotificationRule{}).Where("enabled = ?", true).Count(&status.EnabledRuleCount).Error; err != nil {
		return status, err
	}
	status.DisabledRuleCount = status.RuleCount - status.EnabledRuleCount
	if err := s.db.Model(&model.RSSMonitorItem{}).Count(&status.ItemCount).Error; err != nil {
		return status, err
	}
	status.Available = status.PendingSourceCount > 0
	status.Complete = status.SourceCount > 0 && status.PendingSourceCount == 0
	return status, nil
}

func (s *RSSAutomationService) MigrateLegacyMonitor() (RSSAutomationLegacyMigrationResult, error) {
	result := RSSAutomationLegacyMigrationResult{}
	if s == nil || s.db == nil {
		return result, errors.New("RSS 自动化数据库未初始化")
	}
	if s.legacyMonitor != nil && !s.legacyMonitor.running.CompareAndSwap(false, true) {
		return result, errors.New("旧版 RSS 监控正在刷新，请稍后再迁移")
	}
	if s.legacyMonitor != nil {
		defer func() {
			s.legacyMonitor.running.Store(false)
			s.legacyMonitor.Wake()
		}()
	}

	var legacySources []model.RSSMonitorSetting
	if err := s.db.Where("TRIM(feed_url) <> ''").Order("id ASC").Find(&legacySources).Error; err != nil {
		return result, err
	}
	if len(legacySources) == 0 {
		return result, errors.New("没有可迁移的旧版 RSS 监控源")
	}
	var rules []model.RSSNotificationRule
	if err := s.db.Order("priority ASC, id ASC").Find(&rules).Error; err != nil {
		return result, err
	}
	activeRules := make([]model.RSSNotificationRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			activeRules = append(activeRules, rule)
		} else {
			result.DisabledRulesKept++
		}
	}
	definition, err := buildRSSAutomationLegacyDefinition(activeRules)
	if err != nil {
		return result, err
	}
	definitionJSON, err := MarshalRSSAutomationDefinition(definition)
	if err != nil {
		return result, err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existing []model.RSSAutomationLegacyMigration
		if err := tx.Find(&existing).Error; err != nil {
			return err
		}
		migrated := make(map[uint]struct{}, len(existing))
		for _, migration := range existing {
			var linked int64
			if err := tx.Table("rss_automation_sources AS source").
				Joins("JOIN rss_automation_workflows AS workflow ON workflow.source_id = source.id").
				Where("source.id = ? AND workflow.id = ?", migration.SourceID, migration.WorkflowID).
				Count(&linked).Error; err != nil {
				return err
			}
			if linked == 0 {
				if err := tx.Delete(&migration).Error; err != nil {
					return err
				}
				continue
			}
			migrated[migration.LegacySourceID] = struct{}{}
		}
		for _, legacySource := range legacySources {
			if _, exists := migrated[legacySource.ID]; exists {
				continue
			}
			source := model.RSSAutomationSource{
				Name: legacySource.FeedName, Enabled: legacySource.Enabled, FeedURL: legacySource.FeedURL,
				IntervalMinutes: legacySource.IntervalMinutes, MappingJSON: DefaultRSSAutomationMappingJSON(),
				Initialized: legacySource.Initialized, ETag: legacySource.ETag, LastModified: legacySource.LastModified,
				LastCheckedAt: legacySource.LastCheckedAt, LastSuccessAt: legacySource.LastSuccessAt, LastError: legacySource.LastError,
			}
			if strings.TrimSpace(source.Name) == "" {
				source.Name = fmt.Sprintf("旧版 RSS 源 %d", legacySource.ID)
			}
			if source.IntervalMinutes < 1 {
				source.IntervalMinutes = defaultRSSAutomationIntervalMinutes
			}
			if err := tx.Create(&source).Error; err != nil {
				return err
			}
			workflow := model.RSSAutomationWorkflow{
				SourceID: source.ID, Name: truncateRSSAutomationName(source.Name),
				Description: "由旧版 RSS 监控配置迁移；旧表和历史数据仍保留。",
				Enabled:     legacySource.Enabled, Version: 1, DefinitionJSON: definitionJSON,
			}
			if err := tx.Create(&workflow).Error; err != nil {
				return err
			}
			migratedEntries, err := migrateRSSAutomationLegacyEntries(tx, legacySource, source.ID)
			if err != nil {
				return err
			}
			migration := model.RSSAutomationLegacyMigration{
				LegacySourceID: legacySource.ID, SourceID: source.ID, WorkflowID: workflow.ID, MigratedAt: time.Now(),
			}
			if err := tx.Create(&migration).Error; err != nil {
				return err
			}
			result.SourcesMigrated++
			result.WorkflowsCreated++
			result.EntriesMigrated += migratedEntries
		}
		legacyIDs := make([]uint, 0, len(legacySources))
		for _, legacySource := range legacySources {
			legacyIDs = append(legacyIDs, legacySource.ID)
		}
		update := tx.Model(&model.RSSMonitorSetting{}).Where("id IN ? AND enabled = ?", legacyIDs, true).Update("enabled", false)
		result.LegacySourcesStopped = update.RowsAffected
		return update.Error
	})
	if err != nil {
		return result, err
	}
	result.EnabledRulesMigrated = len(activeRules)
	s.Wake()
	result.Status, err = s.LegacyMigrationStatus()
	return result, err
}

func buildRSSAutomationLegacyDefinition(rules []model.RSSNotificationRule) (RSSAutomationDefinition, error) {
	definition := RSSAutomationDefinition{SchemaVersion: RSSAutomationSchemaVersion, Viewport: RSSAutomationViewport{Zoom: 0.75}}
	definition.Nodes = append(definition.Nodes, RSSAutomationNode{
		ID: "trigger", Type: RSSAutomationNodeTrigger, Name: "收到 RSS 条目", Position: RSSAutomationPosition{X: 60, Y: 260}, Config: map[string]any{},
	})
	if len(rules) == 0 {
		definition.Nodes = append(definition.Nodes, RSSAutomationNode{
			ID: "unmatched_end", Type: RSSAutomationNodeEnd, Name: "未命中结束", Position: RSSAutomationPosition{X: 420, Y: 260}, Config: map[string]any{},
		})
		definition.Edges = append(definition.Edges, RSSAutomationEdge{ID: "trigger-unmatched", Source: "trigger", SourcePort: "next", Target: "unmatched_end"})
		return definition, nil
	}
	definition.Nodes = append(definition.Nodes,
		RSSAutomationNode{ID: "matched_end", Type: RSSAutomationNodeEnd, Name: "命中结束", Position: RSSAutomationPosition{X: float64(540 + len(rules)*300), Y: 80}, Config: map[string]any{}},
		RSSAutomationNode{ID: "unmatched_end", Type: RSSAutomationNodeEnd, Name: "未命中结束", Position: RSSAutomationPosition{X: float64(540 + len(rules)*300), Y: 360}, Config: map[string]any{}},
	)
	previousID := "trigger"
	previousPort := "next"
	for index, rule := range rules {
		suffix := fmt.Sprintf("%d", rule.ID)
		ifID := "legacy_rule_" + suffix
		conditionItems := []any{map[string]any{"field": "$item.title", "operator": "regex", "value": rule.TitlePattern}}
		if strings.TrimSpace(rule.CategoryPattern) != "" {
			conditionItems = append(conditionItems, map[string]any{"field": "$item.category", "operator": "regex", "value": rule.CategoryPattern})
		}
		condition := any(conditionItems[0])
		if len(conditionItems) > 1 {
			condition = map[string]any{"all": conditionItems}
		}
		definition.Nodes = append(definition.Nodes, RSSAutomationNode{
			ID: ifID, Type: RSSAutomationNodeIf, Name: truncateRSSAutomationName(rule.Name),
			Position: RSSAutomationPosition{X: float64(340 + index*300), Y: 260}, Config: map[string]any{"condition": condition},
		})
		definition.Edges = append(definition.Edges, RSSAutomationEdge{
			ID: "edge-" + previousID + "-" + ifID, Source: previousID, SourcePort: previousPort, Target: ifID,
		})

		if rssRuleUsesMP2Recognition(rule) {
			mpID := "legacy_mp_" + suffix
			notifySuccessID := "legacy_notify_" + suffix
			notifyFallbackID := "legacy_fallback_" + suffix
			definition.Nodes = append(definition.Nodes,
				RSSAutomationNode{ID: mpID, Type: RSSAutomationNodeMoviePilotTitle, Name: truncateRSSAutomationName("MP 标题识别 · " + rule.Name), Position: RSSAutomationPosition{X: float64(420 + index*300), Y: 40}, Config: map[string]any{"input": "$item.title", "tmdb_id": ""}, MaxAttempts: 3},
				legacyRSSAutomationNotificationNode(notifySuccessID, rule, mpID, float64(660+index*300), 20),
				legacyRSSAutomationNotificationNode(notifyFallbackID, rule, mpID, float64(660+index*300), 130),
			)
			definition.Edges = append(definition.Edges,
				RSSAutomationEdge{ID: "edge-" + ifID + "-mp", Source: ifID, SourcePort: "true", Target: mpID},
				RSSAutomationEdge{ID: "edge-" + mpID + "-success", Source: mpID, SourcePort: "success", Target: notifySuccessID},
				RSSAutomationEdge{ID: "edge-" + mpID + "-failure", Source: mpID, SourcePort: "failure", Target: notifyFallbackID},
			)
			appendRSSAutomationLegacyNotificationEndEdges(&definition, notifySuccessID)
			appendRSSAutomationLegacyNotificationEndEdges(&definition, notifyFallbackID)
		} else {
			notifyID := "legacy_notify_" + suffix
			definition.Nodes = append(definition.Nodes, legacyRSSAutomationNotificationNode(notifyID, rule, "", float64(560+index*300), 80))
			definition.Edges = append(definition.Edges, RSSAutomationEdge{ID: "edge-" + ifID + "-notify", Source: ifID, SourcePort: "true", Target: notifyID})
			appendRSSAutomationLegacyNotificationEndEdges(&definition, notifyID)
		}
		previousID = ifID
		previousPort = "false"
	}
	definition.Edges = append(definition.Edges, RSSAutomationEdge{ID: "edge-unmatched-end", Source: previousID, SourcePort: previousPort, Target: "unmatched_end"})
	validation := ValidateRSSAutomationDefinition(definition)
	if !validation.Valid {
		return definition, fmt.Errorf("旧版 RSS 规则无法转换为流程: %s", strings.Join(validation.Errors, "; "))
	}
	return definition, nil
}

func legacyRSSAutomationNotificationNode(id string, rule model.RSSNotificationRule, mpNodeID string, x, y float64) RSSAutomationNode {
	return RSSAutomationNode{
		ID: id, Type: RSSAutomationNodeNotification, Name: truncateRSSAutomationName("发送通知 · " + rule.Name),
		Position: RSSAutomationPosition{X: x, Y: y}, MaxAttempts: 3,
		Config: map[string]any{
			"title": "RSS 自动化命中", "message": translateRSSAutomationLegacyTemplate(rule.MessageTemplate, rule.Name, mpNodeID),
			"image_url": legacyRSSAutomationTemplateReference(mpNodeID, "poster_url", ""),
		},
	}
}

func appendRSSAutomationLegacyNotificationEndEdges(definition *RSSAutomationDefinition, notificationID string) {
	definition.Edges = append(definition.Edges,
		RSSAutomationEdge{ID: "edge-" + notificationID + "-success", Source: notificationID, SourcePort: "success", Target: "matched_end"},
		RSSAutomationEdge{ID: "edge-" + notificationID + "-failure", Source: notificationID, SourcePort: "failure", Target: "matched_end"},
	)
}

func translateRSSAutomationLegacyTemplate(template, ruleName, mpNodeID string) string {
	if strings.TrimSpace(template) == "" {
		template = defaultRSSMessageTemplate
	}
	replacements := map[string]string{
		"{{media_title}}":    legacyRSSAutomationTemplateReference(mpNodeID, "title", "{{item.title}}"),
		"{{media_year}}":     legacyRSSAutomationTemplateReference(mpNodeID, "year", ""),
		"{{media_type}}":     legacyRSSAutomationTemplateReference(mpNodeID, "media_type", ""),
		"{{media_category}}": legacyRSSAutomationTemplateReference(mpNodeID, "category", "{{item.category}}"),
		"{{season_episode}}": legacyRSSAutomationTemplateReference(mpNodeID, "season_episode", ""),
		"{{rating}}":         legacyRSSAutomationTemplateReference(mpNodeID, "rating", ""),
		"{{quality}}":        legacyRSSAutomationTemplateReference(mpNodeID, "quality", ""),
		"{{file_count}}":     "1", "{{tmdb_id}}": legacyRSSAutomationTemplateReference(mpNodeID, "tmdb_id", ""),
		"{{poster_url}}": legacyRSSAutomationTemplateReference(mpNodeID, "poster_url", ""),
		"{{title}}":      "{{item.title}}", "{{category}}": "{{item.category}}", "{{size}}": "{{item.size_bytes}}",
		"{{pub_date}}": "{{item.published_at}}", "{{link}}": "{{item.detail_url}}", "{{rule_name}}": ruleName,
	}
	for source, target := range replacements {
		template = strings.ReplaceAll(template, source, target)
	}
	return template
}

func legacyRSSAutomationTemplateReference(nodeID, field, fallback string) string {
	if strings.TrimSpace(nodeID) == "" {
		return fallback
	}
	return "{{nodes." + nodeID + ".output." + field + "}}"
}

func migrateRSSAutomationLegacyEntries(tx *gorm.DB, legacySource model.RSSMonitorSetting, sourceID uint) (int, error) {
	var items []model.RSSMonitorItem
	if err := tx.Where("source_id = ?", legacySource.ID).Order("id ASC").Find(&items).Error; err != nil {
		return 0, err
	}
	migrated := 0
	for _, item := range items {
		fields := map[string]any{
			"guid": item.GUID, "title": item.Title, "detail_url": item.Link,
			"category": item.Category, "size_bytes": item.SizeBytes,
		}
		if item.PublishedAt != nil {
			fields["published_at"] = item.PublishedAt.Format(time.RFC3339)
		}
		fieldsJSON, _ := json.Marshal(fields)
		metadataJSON, _ := json.Marshal(rssAutomationLegacyEntryMetadata{
			SourceName: item.SourceName, RuleName: item.RuleName,
			MediaTitle: item.MediaTitle, MediaYear: item.MediaYear, MediaType: item.MediaType,
			MediaCategory: item.MediaCategory, SeasonEpisode: item.SeasonEpisode, Rating: item.Rating,
			Quality: item.Quality, TMDBID: item.TmdbID, PosterURL: item.PosterURL,
			RecognitionError: item.RecognitionError, NotificationStatus: item.NotificationStatus,
			NotificationError: item.NotificationError,
		})
		discoveredAt := item.DiscoveredAt
		if discoveredAt.IsZero() {
			discoveredAt = item.CreatedAt
		}
		if discoveredAt.IsZero() {
			discoveredAt = time.Now()
		}
		entry := model.RSSAutomationEntry{
			SourceID: sourceID, Fingerprint: rssAutomationFingerprint(sourceID, fields),
			GUID: item.GUID, Title: item.Title, DetailURL: item.Link, PublishedAt: item.PublishedAt,
			FieldsJSON: string(fieldsJSON), Baseline: item.NotificationStatus == model.RSSNotificationBaseline,
			LegacyMatched: item.RuleID != nil, LegacyMetadataJSON: string(metadataJSON), DiscoveredAt: discoveredAt,
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)
		if created.Error != nil {
			return migrated, created.Error
		}
		if created.RowsAffected > 0 {
			migrated++
		}
	}
	return migrated, nil
}

func truncateRSSAutomationName(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRSSAutomationNameLength {
		return string(runes)
	}
	return string(runes[:maxRSSAutomationNameLength])
}
