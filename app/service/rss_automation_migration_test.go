package service

import (
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestRSSAutomationLegacyMigrationPreservesHistoryAndStopsOldSource(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	if err := db.AutoMigrate(
		&model.RSSMonitorSetting{}, &model.RSSNotificationRule{}, &model.RSSMonitorItem{},
		&model.RSSAutomationLegacyMigration{},
	); err != nil {
		t.Fatal(err)
	}
	legacySource := model.RSSMonitorSetting{
		Enabled: true, FeedName: "旧版 PT", FeedURL: "https://example.com/rss",
		IntervalMinutes: 2, Initialized: true,
	}
	if err := db.Create(&legacySource).Error; err != nil {
		t.Fatal(err)
	}
	useMP := true
	rule := model.RSSNotificationRule{
		Name: "首集", Enabled: true, Priority: 10,
		TitlePattern: `(?i)S01E01`, CategoryPattern: `剧集|TV`,
		MessageTemplate: "{{media_title}} {{tmdb_id}} {{link}}", UseMP2Recognition: &useMP,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	disabledRule := model.RSSNotificationRule{
		Name: "已停用", Enabled: false, Priority: 20, TitlePattern: `.*`,
		MessageTemplate: "disabled", UseMP2Recognition: &useMP,
	}
	if err := db.Create(&disabledRule).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	legacyItem := model.RSSMonitorItem{
		SourceID: legacySource.ID, SourceName: legacySource.FeedName, Fingerprint: "legacy-fingerprint",
		GUID: "item-1", Title: "示例剧.S01E01.2160p", Link: "https://example.com/item/1",
		Category: "剧集", RuleID: &rule.ID, RuleName: rule.Name, MediaTitle: "示例剧",
		MediaYear: "2026", TmdbID: "1396", PosterURL: "https://image.example/poster.jpg",
		NotificationStatus: model.RSSNotificationSent, DiscoveredAt: now,
	}
	if err := db.Create(&legacyItem).Error; err != nil {
		t.Fatal(err)
	}

	automation := &RSSAutomationService{db: db}
	result, err := automation.MigrateLegacyMonitor()
	if err != nil {
		t.Fatalf("MigrateLegacyMonitor() error = %v", err)
	}
	if result.SourcesMigrated != 1 || result.WorkflowsCreated != 1 || result.EntriesMigrated != 1 {
		t.Fatalf("unexpected migration result: %#v", result)
	}
	if result.EnabledRulesMigrated != 1 || result.DisabledRulesKept != 1 {
		t.Fatalf("unexpected rule result: %#v", result)
	}
	var stopped model.RSSMonitorSetting
	if err := db.First(&stopped, legacySource.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stopped.Enabled {
		t.Fatal("legacy source remains enabled after successful migration")
	}
	var workflow model.RSSAutomationWorkflow
	if err := db.First(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	definition, err := ParseRSSAutomationDefinition(workflow.DefinitionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if validation := ValidateRSSAutomationDefinition(definition); !validation.Valid {
		t.Fatalf("migrated definition invalid: %#v", validation.Errors)
	}
	var hasTitleRecognition, hasPosterNotification bool
	for _, node := range definition.Nodes {
		if node.Type == RSSAutomationNodeMoviePilotTitle {
			hasTitleRecognition = true
		}
		if node.Type == RSSAutomationNodeNotification && rssAutomationConfigString(node.Config, "image_url") != "" {
			hasPosterNotification = true
		}
	}
	if !hasTitleRecognition || !hasPosterNotification {
		t.Fatalf("migrated definition missing parity nodes: %#v", definition.Nodes)
	}

	history, err := automation.ListEntryHistory("matched", 0, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if history.Total != 1 || len(history.Items) != 1 {
		t.Fatalf("matched history = %#v", history)
	}
	item := history.Items[0]
	if !item.Legacy || !item.Matched || item.TMDBID != "1396" || item.NotificationStatus != model.RSSNotificationSent {
		t.Fatalf("legacy history metadata not preserved: %#v", item)
	}

	second, err := automation.MigrateLegacyMonitor()
	if err != nil {
		t.Fatalf("second MigrateLegacyMonitor() error = %v", err)
	}
	if second.SourcesMigrated != 0 || second.EntriesMigrated != 0 {
		t.Fatalf("migration is not idempotent: %#v", second)
	}
}
