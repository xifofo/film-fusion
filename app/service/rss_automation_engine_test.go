package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRSSAutomationEngineRegexCaptureNumericIfAndBranch(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	service := &RSSAutomationService{
		db: db, ctx: ctx, cancel: cancel,
		sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		workers: make(chan struct{}, 2),
	}

	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger, Name: "收到条目"},
			{ID: "capture", Type: RSSAutomationNodeRegex, Name: "提取集数", Config: map[string]any{
				"input": "$item.title", "pattern": `(\d+)集`, "group": "1", "variable": "episode", "value_type": "integer",
			}},
			{ID: "if_episode", Type: RSSAutomationNodeIf, Name: "集数大于 1000", Config: map[string]any{
				"condition": map[string]any{"field": "$vars.episode", "operator": "gt", "value": 1000},
			}},
			{ID: "end_pass", Type: RSSAutomationNodeEnd, Name: "通过"},
			{ID: "end_reject", Type: RSSAutomationNodeEnd, Name: "忽略"},
			{ID: "end_no_match", Type: RSSAutomationNodeEnd, Name: "未匹配"},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "capture"},
			{ID: "e2", Source: "capture", SourcePort: "success", Target: "if_episode"},
			{ID: "e3", Source: "capture", SourcePort: "failure", Target: "end_no_match"},
			{ID: "e4", Source: "if_episode", SourcePort: "true", Target: "end_pass"},
			{ID: "e5", Source: "if_episode", SourcePort: "false", Target: "end_reject"},
		},
	}
	if validation := ValidateRSSAutomationDefinition(definition); !validation.Valid {
		t.Fatalf("test definition invalid: %#v", validation.Errors)
	}
	definitionJSON, _ := MarshalRSSAutomationDefinition(definition)
	source := model.RSSAutomationSource{
		Name: "动画更新", Enabled: true, FeedURL: "https://example.com/feed.xml",
		IntervalMinutes: 5, MappingJSON: DefaultRSSAutomationMappingJSON(),
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.RSSAutomationWorkflow{SourceID: source.ID, Name: "高集数流程", Enabled: true, Version: 1, DefinitionJSON: definitionJSON}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	fieldsJSON, _ := json.Marshal(map[string]any{"title": "示例动画 第1001集", "download_url": "magnet:?xt=urn:btih:ABC"})
	entry := model.RSSAutomationEntry{SourceID: source.ID, Fingerprint: "fingerprint", FieldsJSON: string(fieldsJSON), DiscoveredAt: time.Now()}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if created, err := service.createRSSAutomationRuns(entry); err != nil || created != 1 {
		t.Fatalf("createRSSAutomationRuns() = %d, %v", created, err)
	}
	service.Start()
	t.Cleanup(service.Stop)

	run := waitForRSSAutomationRun(t, db, model.RSSAutomationRunSucceeded)
	var nodeRuns []model.RSSAutomationNodeRun
	if err := db.Where("run_id = ?", run.ID).Find(&nodeRuns).Error; err != nil {
		t.Fatal(err)
	}
	byNode := make(map[string]model.RSSAutomationNodeRun)
	for _, nodeRun := range nodeRuns {
		byNode[nodeRun.NodeID] = nodeRun
	}
	if byNode["end_pass"].Status != model.RSSAutomationNodeSucceeded {
		t.Fatalf("pass branch did not execute: %#v", byNode)
	}
	if byNode["end_reject"].Status != model.RSSAutomationNodeSkipped || byNode["end_no_match"].Status != model.RSSAutomationNodeSkipped {
		t.Fatalf("inactive branches not skipped: %#v", byNode)
	}
	var captureOutput map[string]any
	if err := json.Unmarshal([]byte(byNode["capture"].OutputJSON), &captureOutput); err != nil {
		t.Fatal(err)
	}
	variables := captureOutput["variables"].(map[string]any)
	if got := variables["episode"]; got != float64(1001) {
		t.Fatalf("captured episode = %#v", got)
	}
}

func TestRSSAutomationSharedEndReadinessUsesActiveConditionalBranch(t *testing.T) {
	predecessor := &model.RSSAutomationNodeRun{
		NodeID:     "if_episode",
		Status:     model.RSSAutomationNodeSucceeded,
		OutputJSON: `{"selected_port":"true"}`,
	}
	ready, inactive := rssAutomationNodeReadiness(
		RSSAutomationNode{ID: "end", Type: RSSAutomationNodeEnd},
		[]RSSAutomationEdge{
			{Source: "if_episode", SourcePort: "true", Target: "end"},
			{Source: "if_episode", SourcePort: "false", Target: "end"},
		},
		map[string]*model.RSSAutomationNodeRun{"if_episode": predecessor},
	)
	if !ready || inactive {
		t.Fatalf("shared end readiness = %v, inactive = %v", ready, inactive)
	}
}

func TestExecuteRSSAutomationKeywordNodeModes(t *testing.T) {
	tests := []struct {
		name          string
		title         string
		keywords      []any
		mode          string
		caseSensitive bool
		wantPort      string
	}{
		{name: "contains any ignores case", title: "Show.WEB-DL.1080p", keywords: []any{"web-dl", "BluRay"}, mode: "contains_any", wantPort: "matched"},
		{name: "contains all", title: "Show 1080p HEVC", keywords: []any{"1080p", "HEVC"}, mode: "contains_all", wantPort: "matched"},
		{name: "contains none passes clean title", title: "Show 1080p WEB-DL", keywords: []any{"CAM", "TS"}, mode: "contains_none", wantPort: "matched"},
		{name: "contains none rejects forbidden keyword", title: "Show 1080p CAM", keywords: []any{"CAM", "TS"}, mode: "contains_none", wantPort: "unmatched"},
		{name: "case sensitive", title: "Show web-dl", keywords: []any{"WEB-DL"}, mode: "contains_any", caseSensitive: true, wantPort: "unmatched"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := RSSAutomationNode{Type: RSSAutomationNodeKeyword, Config: map[string]any{
				"input": "$item.title", "keywords": test.keywords, "match_mode": test.mode,
				"case_sensitive": test.caseSensitive,
			}}
			output, err := executeRSSAutomationKeywordNode(node, map[string]any{
				"item": map[string]any{"title": test.title},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := output["selected_port"]; got != test.wantPort {
				t.Fatalf("selected_port = %#v, want %q; output = %#v", got, test.wantPort, output)
			}
		})
	}
}

func newRSSAutomationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:rss-automation-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.SystemConfig{},
		&model.RSSAutomationSource{}, &model.RSSAutomationWorkflow{}, &model.RSSAutomationTarget{},
		&model.RSSAutomationEntry{}, &model.RSSAutomationRun{}, &model.RSSAutomationNodeRun{},
		&model.CloudStorage{},
	); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveRSSAutomationSettings(db, database.DefaultRSSAutomationUserAgent); err != nil {
		t.Fatal(err)
	}
	return db
}

func waitForRSSAutomationRun(t *testing.T, db *gorm.DB, wanted string) model.RSSAutomationRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var run model.RSSAutomationRun
		if err := db.Order("id DESC").First(&run).Error; err == nil && run.Status == wanted {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	var run model.RSSAutomationRun
	_ = db.Order("id DESC").First(&run).Error
	t.Fatalf("run did not reach %s: %#v", wanted, run)
	return model.RSSAutomationRun{}
}
