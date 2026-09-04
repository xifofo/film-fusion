package service

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestRunEntryCreatesFirstManualRunForBaseline(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger, Name: "收到条目"},
			{ID: "end", Type: AutomationNodeEnd, Name: "结束"},
		},
		Edges: []AutomationEdge{{ID: "edge", Source: "trigger", SourcePort: "next", Target: "end"}},
	}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	source := model.AutomationSource{
		Name: "已停用的 RSS", TriggerType: model.AutomationTriggerRSS,
		FeedURL: "https://example.com/feed.xml", IntervalMinutes: 5,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.AutomationWorkflow{
		SourceID: source.ID, Name: "当前流程", Enabled: false, Version: 5,
		DefinitionJSON: definitionJSON,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.AutomationEntry{
		SourceID: source.ID, Fingerprint: "baseline-fingerprint", EventKey: "rss:baseline",
		ItemID: "baseline", Title: "存量第一集", Baseline: true,
		FieldsJSON:   `{"title":"存量第一集","download_url":"magnet:?xt=urn:btih:baseline"}`,
		DiscoveredAt: time.Now(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}

	run, err := service.RunEntry(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.WorkflowID != workflow.ID || run.WorkflowVersion != workflow.Version || run.ExecutionNo != 1 || run.TriggerMode != model.AutomationRunTriggerManual {
		t.Fatalf("manual baseline run metadata = %#v", run)
	}
	if run.RepeatedFromRunID != nil || run.DefinitionJSON != workflow.DefinitionJSON {
		t.Fatalf("manual baseline run snapshot = %#v", run)
	}
	var runContext map[string]any
	if err := json.Unmarshal([]byte(run.ContextJSON), &runContext); err != nil {
		t.Fatal(err)
	}
	item, _ := runContext["item"].(map[string]any)
	if item["title"] != "存量第一集" || item["download_url"] != "magnet:?xt=urn:btih:baseline" {
		t.Fatalf("manual baseline context = %#v", runContext)
	}
	var nodeRuns []model.AutomationNodeRun
	if err := db.Where("run_id = ?", run.ID).Order("id ASC").Find(&nodeRuns).Error; err != nil {
		t.Fatal(err)
	}
	if len(nodeRuns) != len(definition.Nodes) {
		t.Fatalf("manual baseline node count = %d", len(nodeRuns))
	}
	for _, nodeRun := range nodeRuns {
		if nodeRun.Status != model.AutomationNodePending {
			t.Fatalf("manual baseline node was not pending: %#v", nodeRun)
		}
	}
	if _, err := service.RunEntry(entry.ID); err == nil {
		t.Fatal("baseline with a run should direct the user to run history")
	}
}

func TestRunEntryRejectsNonBaselineEntry(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{db: db}
	entry := model.AutomationEntry{
		SourceID: 1, Fingerprint: "incremental-fingerprint", EventKey: "rss:incremental",
		ItemID: "incremental", Title: "增量条目", Baseline: false,
		FieldsJSON: `{"title":"增量条目"}`, DiscoveredAt: time.Now(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunEntry(entry.ID); err == nil {
		t.Fatal("incremental entry should not use the baseline manual-run endpoint")
	}
}

func TestRepeatRunCreatesIndependentExecutionFromHistoricalSnapshot(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger, Name: "收到条目"},
			{ID: "end", Type: AutomationNodeEnd, Name: "结束"},
		},
		Edges: []AutomationEdge{
			{ID: "edge", Source: "trigger", SourcePort: "next", Target: "end"},
		},
	}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	source := model.AutomationSource{
		Name: "RSS", TriggerType: model.AutomationTriggerRSS,
		FeedURL: "https://example.com/feed.xml", IntervalMinutes: 5,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.AutomationWorkflow{
		SourceID: source.ID, Name: "下载新番", Version: 3, DefinitionJSON: definitionJSON,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.AutomationEntry{
		SourceID: source.ID, Fingerprint: "entry-fingerprint", EventKey: "rss:episode-09",
		ItemID: "episode-09", Title: "第 09 话", FieldsJSON: `{"title":"第 09 话"}`,
		DiscoveredAt: time.Now(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	original, created, err := service.createAutomationRun(workflow, entry, false)
	if err != nil || !created {
		t.Fatalf("create original run = %#v, %v, created=%v", original, err, created)
	}
	if original.ExecutionNo != 1 || original.TriggerMode != model.AutomationRunTriggerEvent {
		t.Fatalf("original execution metadata = %#v", original)
	}
	completedAt := time.Now()
	if err := db.Model(&model.AutomationRun{}).Where("id = ?", original.ID).Updates(map[string]any{
		"status": model.AutomationRunSucceeded, "completed_at": completedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AutomationNodeRun{}).Where("run_id = ?", original.ID).Updates(map[string]any{
		"status": model.AutomationNodeSucceeded, "output_json": `{"kept":true}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&workflow).Updates(map[string]any{
		"name": "已经编辑的新流程", "version": 4,
	}).Error; err != nil {
		t.Fatal(err)
	}

	repeated, err := service.RepeatRun(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID == original.ID || repeated.ExecutionNo != 2 || repeated.TriggerMode != model.AutomationRunTriggerManual {
		t.Fatalf("repeated run metadata = %#v", repeated)
	}
	if repeated.RepeatedFromRunID == nil || *repeated.RepeatedFromRunID != original.ID {
		t.Fatalf("repeated source run = %#v", repeated.RepeatedFromRunID)
	}
	if repeated.WorkflowName != original.WorkflowName || repeated.WorkflowVersion != original.WorkflowVersion || repeated.DefinitionJSON != original.DefinitionJSON || repeated.ContextJSON != original.ContextJSON {
		t.Fatalf("repeat did not use historical snapshot: original=%#v repeated=%#v", original, repeated)
	}
	var repeatedNodes []model.AutomationNodeRun
	if err := db.Where("run_id = ?", repeated.ID).Order("id ASC").Find(&repeatedNodes).Error; err != nil {
		t.Fatal(err)
	}
	if len(repeatedNodes) != len(definition.Nodes) {
		t.Fatalf("repeated node count = %d", len(repeatedNodes))
	}
	for _, nodeRun := range repeatedNodes {
		if nodeRun.Status != model.AutomationNodePending || nodeRun.OutputJSON != "" {
			t.Fatalf("repeated node was not reset: %#v", nodeRun)
		}
	}
	var originalNode model.AutomationNodeRun
	if err := db.Where("run_id = ? AND node_id = ?", original.ID, "trigger").First(&originalNode).Error; err != nil {
		t.Fatal(err)
	}
	if originalNode.Status != model.AutomationNodeSucceeded || originalNode.OutputJSON != `{"kept":true}` {
		t.Fatalf("original history was changed: %#v", originalNode)
	}

	if _, err := service.RepeatRun(original.ID); err == nil {
		t.Fatal("repeat should reject while the same event already has an active run")
	}
	if err := db.Model(&model.AutomationRun{}).Where("id = ?", repeated.ID).Updates(map[string]any{
		"status": model.AutomationRunSucceeded, "completed_at": time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	third, err := service.RepeatRun(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if third.ExecutionNo != 3 || third.ID == repeated.ID {
		t.Fatalf("third execution = %#v", third)
	}
}

func TestRepeatRunRejectsAnActiveHistoricalRun(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	run := model.AutomationRun{
		WorkflowID: 1, WorkflowName: "正在运行", WorkflowVersion: 1, EntryID: 1,
		ExecutionNo: 1, TriggerMode: model.AutomationRunTriggerEvent,
		DefinitionJSON: `{"schema_version":1,"nodes":[],"edges":[]}`,
		ContextJSON:    `{}`, Status: model.AutomationRunRunning,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RepeatRun(run.ID); err == nil {
		t.Fatal("active run should not be repeatable")
	}
}

func TestRepeatRunSerializesConcurrentRequests(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger, Name: "收到条目"},
			{ID: "end", Type: AutomationNodeEnd, Name: "结束"},
		},
		Edges: []AutomationEdge{{ID: "edge", Source: "trigger", SourcePort: "next", Target: "end"}},
	}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	original := model.AutomationRun{
		WorkflowID: 1, WorkflowName: "已完成", WorkflowVersion: 1, EntryID: 1,
		ExecutionNo: 1, TriggerMode: model.AutomationRunTriggerEvent,
		DefinitionJSON: definitionJSON, ContextJSON: `{"item":{},"vars":{},"nodes":{}}`,
		Status: model.AutomationRunSucceeded,
	}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, repeatErr := service.RepeatRun(original.ID)
			results <- repeatErr
		}()
	}
	group.Wait()
	close(results)

	succeeded := 0
	rejected := 0
	for repeatErr := range results {
		if repeatErr == nil {
			succeeded++
		} else {
			rejected++
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent repeats: succeeded=%d rejected=%d", succeeded, rejected)
	}
	var repeatCount int64
	if err := db.Model(&model.AutomationRun{}).Where("repeated_from_run_id = ?", original.ID).Count(&repeatCount).Error; err != nil {
		t.Fatal(err)
	}
	if repeatCount != 1 {
		t.Fatalf("created repeated runs = %d", repeatCount)
	}
}
