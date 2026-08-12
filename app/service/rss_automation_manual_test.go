package service

import (
	"encoding/json"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

func TestRSSAutomationManualCandidatesMatchWithoutExecutingActions(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	automation := &RSSAutomationService{db: db, executionWake: make(chan struct{}, 1)}
	definition := manualCandidateTestDefinition()
	definitionJSON, err := MarshalRSSAutomationDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	source := model.RSSAutomationSource{
		Name: "柯南更新", Enabled: true, FeedURL: "https://example.com/feed.xml",
		IntervalMinutes: 5, MappingJSON: DefaultRSSAutomationMappingJSON(), Initialized: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.RSSAutomationWorkflow{
		SourceID: source.ID, Name: "下载 MKV 新集", Enabled: false, Version: 1, DefinitionJSON: definitionJSON,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}

	matching := createRSSAutomationManualTestEntry(t, db, source.ID, "matching", "[第1209集][简繁日多语MKV]", "magnet:?xt=urn:btih:AAA1209", time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	alreadyRun := createRSSAutomationManualTestEntry(t, db, source.ID, "already-run", "[第1208集][简繁日多语MKV]", "magnet:?xt=urn:btih:AAA1208", time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC))
	createRSSAutomationManualTestEntry(t, db, source.ID, "below-threshold", "[第1207集][简繁日多语MKV]", "magnet:?xt=urn:btih:AAA1207", time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))
	createRSSAutomationManualTestEntry(t, db, source.ID, "wrong-keyword", "[第1210集][简日双语MP4]", "magnet:?xt=urn:btih:AAA1210", time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	createRSSAutomationManualTestEntry(t, db, source.ID, "range", "[第124-125集][简繁日多语MKV]", "magnet:?xt=urn:btih:AAA0124", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))

	if _, created, err := automation.createRSSAutomationRun(workflow, alreadyRun, true); err != nil || !created {
		t.Fatalf("seed existing run: created=%v err=%v", created, err)
	}
	if err := db.Model(&workflow).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}

	list, err := automation.ListManualCandidates(workflow.ID, 50)
	if err != nil {
		t.Fatalf("ListManualCandidates() error = %v", err)
	}
	if list.WorkflowVersion != 2 || len(list.Items) != 1 {
		t.Fatalf("unexpected candidates: %#v", list)
	}
	if list.Items[0].EntryID != matching.ID {
		t.Fatalf("candidate entry = %d, want %d", list.Items[0].EntryID, matching.ID)
	}
	if len(list.Items[0].ActionTypes) != 1 || list.Items[0].ActionTypes[0] != RSSAutomationNodeOffline115OpenAPI {
		t.Fatalf("candidate actions = %#v", list.Items[0].ActionTypes)
	}
	var runCount int64
	if err := db.Model(&model.RSSAutomationRun{}).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("preview created runs: %d", runCount)
	}
}

func TestRSSAutomationManualRunsRequireSelectionAndPreventDuplicates(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	automation := &RSSAutomationService{db: db, executionWake: make(chan struct{}, 1)}
	definitionJSON, err := MarshalRSSAutomationDefinition(manualCandidateTestDefinition())
	if err != nil {
		t.Fatal(err)
	}
	source := model.RSSAutomationSource{
		Name: "柯南更新", FeedURL: "https://example.com/feed.xml", IntervalMinutes: 5,
		MappingJSON: DefaultRSSAutomationMappingJSON(), Initialized: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.RSSAutomationWorkflow{SourceID: source.ID, Name: "手动下载", Version: 1, DefinitionJSON: definitionJSON}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	matching := createRSSAutomationManualTestEntry(t, db, source.ID, "matching", "[第1209集][简繁日多语MKV]", "magnet:?xt=urn:btih:BBB1209", time.Now())
	sameContent := createRSSAutomationManualTestEntry(t, db, source.ID, "same-content", "[第1209集][简繁日多语MKV][补发]", "magnet:?xt=urn:btih:BBB1209", time.Now().Add(-time.Minute))
	unmatched := createRSSAutomationManualTestEntry(t, db, source.ID, "unmatched", "[第1207集][简繁日多语MKV]", "magnet:?xt=urn:btih:BBB1207", time.Now().Add(-time.Hour))

	if _, err := automation.CreateManualRuns(workflow.ID, RSSAutomationManualRunInput{}); err == nil {
		t.Fatal("empty manual selection unexpectedly succeeded")
	}
	result, err := automation.CreateManualRuns(workflow.ID, RSSAutomationManualRunInput{EntryIDs: []uint{matching.ID, matching.ID, sameContent.ID, unmatched.ID}})
	if err != nil {
		t.Fatalf("CreateManualRuns() error = %v", err)
	}
	if result.Requested != 3 || result.Created != 1 || len(result.RunIDs) != 1 || len(result.Skipped) != 2 {
		t.Fatalf("unexpected manual run result: %#v", result)
	}
	var run model.RSSAutomationRun
	if err := db.First(&run, result.RunIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if run.EntryID != matching.ID || run.Status != model.RSSAutomationRunPending {
		t.Fatalf("unexpected queued run: %#v", run)
	}

	again, err := automation.CreateManualRuns(workflow.ID, RSSAutomationManualRunInput{EntryIDs: []uint{matching.ID}})
	if err != nil {
		t.Fatalf("second CreateManualRuns() error = %v", err)
	}
	if again.Created != 0 || len(again.Skipped) != 1 || again.Skipped[0].Reason != "该流程已运行过此条目或相同下载内容" {
		t.Fatalf("duplicate manual run was not skipped: %#v", again)
	}
	var runCount int64
	if err := db.Model(&model.RSSAutomationRun{}).Where("workflow_id = ? AND entry_id = ?", workflow.ID, matching.ID).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("duplicate runs = %d, want 1", runCount)
	}
}

func TestRSSAutomationListRunsFiltersByWorkflow(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	automation := &RSSAutomationService{db: db}
	for _, run := range []model.RSSAutomationRun{
		{WorkflowID: 11, WorkflowName: "流程一", WorkflowVersion: 1, EntryID: 101, DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.RSSAutomationRunSucceeded},
		{WorkflowID: 22, WorkflowName: "流程二", WorkflowVersion: 1, EntryID: 202, DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.RSSAutomationRunFailed},
	} {
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
	}

	runs, total, err := automation.ListRuns(11, "", 50, 0)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if total != 1 || len(runs) != 1 || runs[0].WorkflowID != 11 {
		t.Fatalf("workflow filtered runs = %#v, total = %d", runs, total)
	}
}

func manualCandidateTestDefinition() RSSAutomationDefinition {
	return RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger, Name: "收到条目"},
			{ID: "keyword", Type: RSSAutomationNodeKeyword, Name: "匹配版本", Config: map[string]any{
				"input": "$item.title", "keywords": []any{"简繁日多语MKV"}, "match_mode": "contains_any", "case_sensitive": false,
			}},
			{ID: "episode", Type: RSSAutomationNodeRegex, Name: "提取集数", Config: map[string]any{
				"input": "$item.title", "pattern": `第(\d+)集`, "group": "1", "variable": "episode", "value_type": "integer",
			}},
			{ID: "threshold", Type: RSSAutomationNodeIf, Name: "大于 1207", Config: map[string]any{
				"condition": map[string]any{"field": "$vars.episode", "operator": "gt", "value": 1207},
			}},
			{ID: "download", Type: RSSAutomationNodeOffline115OpenAPI, Name: "115 OpenAPI 离线", Config: map[string]any{
				"cloud_storage_id": 2, "directory_id": "100", "url": "$item.download_url",
			}},
			{ID: "end", Type: RSSAutomationNodeEnd, Name: "结束"},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "keyword"},
			{ID: "e2", Source: "keyword", SourcePort: "matched", Target: "episode"},
			{ID: "e3", Source: "keyword", SourcePort: "unmatched", Target: "end"},
			{ID: "e4", Source: "keyword", SourcePort: "failure", Target: "end"},
			{ID: "e5", Source: "episode", SourcePort: "success", Target: "threshold"},
			{ID: "e6", Source: "episode", SourcePort: "failure", Target: "end"},
			{ID: "e7", Source: "threshold", SourcePort: "true", Target: "download"},
			{ID: "e8", Source: "threshold", SourcePort: "false", Target: "end"},
			{ID: "e9", Source: "download", SourcePort: "success", Target: "end"},
			{ID: "e10", Source: "download", SourcePort: "failure", Target: "end"},
		},
	}
}

func createRSSAutomationManualTestEntry(t *testing.T, db *gorm.DB, sourceID uint, fingerprint, title, downloadURL string, publishedAt time.Time) model.RSSAutomationEntry {
	t.Helper()
	fieldsJSON, err := json.Marshal(map[string]any{
		"guid": fingerprint, "title": title, "download_url": downloadURL, "published_at": publishedAt.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := model.RSSAutomationEntry{
		SourceID: sourceID, Fingerprint: fingerprint, GUID: fingerprint, Title: title,
		DownloadURL: downloadURL, ContentKey: rssAutomationContentKey(downloadURL),
		PublishedAt: &publishedAt, FieldsJSON: string(fieldsJSON), DiscoveredAt: time.Now(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return entry
}
