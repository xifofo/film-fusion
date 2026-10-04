package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestCreateAutomationSupportsRSSAsIndependentTriggerType(t *testing.T) {
	db := newAutomationTestDB(t)
	automation := &AutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	created, err := automation.CreateAutomation(AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "新番下载", Enabled: true, TriggerType: model.AutomationTriggerRSS,
			FeedURL: "https://example.com/feed.xml", IntervalMinutes: 5,
			Mapping: DefaultRSSAutomationMapping(),
		},
		Workflow: AutomationCreateWorkflowInput{
			Name: "下载新条目", Enabled: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Source.TriggerType != model.AutomationTriggerRSS || created.Source.FeedURL == "" {
		t.Fatalf("created source = %#v", created.Source)
	}
	definition, err := ParseAutomationDefinition(created.Workflow.DefinitionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Nodes) == 0 || definition.Nodes[0].Name != "收到 RSS 条目" {
		t.Fatalf("default RSS definition = %#v", definition)
	}
	var cursors int64
	if err := db.Model(&model.Automation115DirectoryCursor{}).
		Where("source_id = ?", created.Source.ID).Count(&cursors).Error; err != nil {
		t.Fatal(err)
	}
	if cursors != 0 {
		t.Fatalf("RSS trigger unexpectedly created %d 115 cursors", cursors)
	}
}

func TestRSSAutomationCheckBaselinesThenQueuesOnlyNewItems(t *testing.T) {
	now := time.Now()
	feed := `<rss><channel><title>新番</title><item><guid>one</guid><title>第一集</title><enclosure url="magnet:?xt=urn:btih:one" /></item></channel></rss>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, feed)
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	automation := &AutomationService{
		db: db, httpClient: &http.Client{Timeout: time.Second}, rssClock: func() time.Time { return now },
		sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		scanning: make(map[uint]struct{}),
	}
	created, err := automation.CreateAutomation(AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "新番", Enabled: true, TriggerType: model.AutomationTriggerRSS,
			FeedURL: server.URL, IntervalMinutes: 5, Mapping: DefaultRSSAutomationMapping(),
		},
		Workflow: AutomationCreateWorkflowInput{
			Name: "新番下载", Enabled: true, Definition: DefaultAutomationDefinition(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	baseline, err := automation.Scan(context.Background(), created.Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.Baseline || baseline.TriggerType != model.AutomationTriggerRSS ||
		baseline.NewEntries != 1 || baseline.CreatedRuns != 0 {
		t.Fatalf("baseline result = %#v", baseline)
	}

	feed = strings.Replace(feed, "</channel>", `<item><guid>two</guid><title>第二集</title><enclosure url="magnet:?xt=urn:btih:two" /></item></channel>`, 1)
	now = now.Add(181 * time.Second)
	increment, err := automation.Scan(context.Background(), created.Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if increment.Baseline || increment.NewEntries != 1 || increment.CreatedRuns != 1 {
		t.Fatalf("increment result = %#v", increment)
	}
	var entries []model.AutomationEntry
	if err := db.Order("id ASC").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !entries[0].Baseline || entries[1].Baseline {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestRSSAutomationCheckRollsBackEntryWhenRunCreationFails(t *testing.T) {
	now := time.Now()
	feed := `<rss><channel><title>新番</title><item><guid>one</guid><title>第一集</title><enclosure url="magnet:?xt=urn:btih:one" /></item></channel></rss>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, feed)
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	automation := &AutomationService{
		db: db, httpClient: &http.Client{Timeout: time.Second}, rssClock: func() time.Time { return now },
		sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		scanning: make(map[uint]struct{}),
	}
	created, err := automation.CreateAutomation(AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "新番", Enabled: true, TriggerType: model.AutomationTriggerRSS,
			FeedURL: server.URL, IntervalMinutes: 5, Mapping: DefaultRSSAutomationMapping(),
		},
		Workflow: AutomationCreateWorkflowInput{
			Name: "新番下载", Enabled: true, Definition: DefaultAutomationDefinition(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := automation.Scan(context.Background(), created.Source.ID); err != nil {
		t.Fatal(err)
	}

	originalDefinition := created.Workflow.DefinitionJSON
	if err := db.Model(&model.AutomationWorkflow{}).Where("id = ?", created.Workflow.ID).
		Update("definition_json", `{"schema_version":1,"nodes":[],"edges":[]}`).Error; err != nil {
		t.Fatal(err)
	}
	feed = strings.Replace(feed, "</channel>", `<item><guid>two</guid><title>第二集</title><enclosure url="magnet:?xt=urn:btih:two" /></item></channel>`, 1)
	now = now.Add(181 * time.Second)
	if _, err := automation.Scan(context.Background(), created.Source.ID); err == nil {
		t.Fatal("expected invalid workflow to fail run creation")
	}
	var entryCount int64
	if err := db.Model(&model.AutomationEntry{}).Where("source_id = ?", created.Source.ID).Count(&entryCount).Error; err != nil {
		t.Fatal(err)
	}
	if entryCount != 1 {
		t.Fatalf("entry count after failed run creation = %d, want 1", entryCount)
	}

	if err := db.Model(&model.AutomationWorkflow{}).Where("id = ?", created.Workflow.ID).
		Update("definition_json", originalDefinition).Error; err != nil {
		t.Fatal(err)
	}
	result, err := automation.Scan(context.Background(), created.Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewEntries != 1 || result.CreatedRuns != 1 {
		t.Fatalf("retry result = %#v", result)
	}
}

func TestAutomationEntryHistoryKeepsLegacyRSSMetadata(t *testing.T) {
	db := newAutomationTestDB(t)
	source := model.AutomationSource{
		Name: "旧 RSS", TriggerType: model.AutomationTriggerRSS,
		FeedURL: "https://example.com/feed.xml", IntervalMinutes: 5,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AutomationEntry{
		SourceID: source.ID, Fingerprint: "legacy", EventKey: "rss:legacy", ItemID: "legacy",
		Title: "旧条目", FieldsJSON: `{"title":"旧条目"}`, LegacyMatched: true,
		LegacyMetadataJSON: `{"rule_name":"旧规则","media_title":"旧媒体"}`,
		DiscoveredAt:       time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	history, err := (&AutomationService{db: db}).ListEntryHistory("matched", source.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if history.Total != 1 || len(history.Items) != 1 {
		t.Fatalf("history = %#v", history)
	}
	item := history.Items[0]
	if !item.Matched || !item.Legacy || item.RuleName != "旧规则" || item.MediaTitle != "旧媒体" {
		t.Fatalf("legacy history item = %#v", item)
	}
}
