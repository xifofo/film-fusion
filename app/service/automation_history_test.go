package service

import (
	"fmt"
	"strings"
	"testing"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

func TestEntryHistoryLoadsOnlyLatestRunsAndPreservesResponse(t *testing.T) {
	db := newAutomationTestDB(t)
	pool, _ := db.DB()
	t.Cleanup(func() { _ = pool.Close() })
	source := model.AutomationSource{Name: "history"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	latest := map[uint]model.AutomationRun{}
	for i := 0; i < 50; i++ {
		entry := model.AutomationEntry{SourceID: source.ID, Fingerprint: fmt.Sprint(i), EventKey: fmt.Sprint(i), FieldsJSON: "{}"}
		if err := db.Create(&entry).Error; err != nil {
			t.Fatal(err)
		}
		runs := make([]model.AutomationRun, 100)
		for j := range runs {
			runs[j] = model.AutomationRun{EntryID: entry.ID, WorkflowID: 1, WorkflowVersion: j/50 + 1, ExecutionNo: j + 1,
				WorkflowName: "workflow", DefinitionJSON: `{"payload":"` + strings.Repeat("x", 4096) + `"}`, ContextJSON: `{"title":"latest"}`, Status: model.AutomationRunSucceeded}
		}
		if err := db.CreateInBatches(&runs, 100).Error; err != nil {
			t.Fatal(err)
		}
		latest[entry.ID] = runs[len(runs)-1]
	}
	var loaded int64
	if err := db.Callback().Query().After("gorm:query").Register("history:count-runs", func(tx *gorm.DB) {
		if tx.Statement.Table == "automation_runs" {
			loaded += tx.RowsAffected
		}
	}); err != nil {
		t.Fatal(err)
	}
	history, err := (&AutomationService{db: db}).ListEntryHistory("all", source.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if history.Total != 50 || len(history.Items) != 50 || loaded != 50 {
		t.Fatalf("total=%d items=%d loaded_runs=%d", history.Total, len(history.Items), loaded)
	}
	for i, item := range history.Items {
		want := latest[item.Entry.ID]
		if item.SourceName != source.Name || item.LatestRun == nil || item.LatestRun.ID != want.ID || item.LatestRun.DefinitionJSON != want.DefinitionJSON || item.LatestRun.ContextJSON != want.ContextJSON || item.LatestRun.WorkflowVersion != want.WorkflowVersion {
			t.Fatalf("latest run contract changed: %#v", item)
		}
		if i > 0 && item.Entry.ID >= history.Items[i-1].Entry.ID {
			t.Fatal("entry ordering changed")
		}
	}
	t.Logf("50 entries / 5000 historic runs: loaded %d full run records", loaded)
}

func TestEntryHistoryMatchedFilterStillUsesOlderRuns(t *testing.T) {
	db := newAutomationTestDB(t)
	pool, _ := db.DB()
	defer pool.Close()
	entry := model.AutomationEntry{SourceID: 1, Fingerprint: "old-match", FieldsJSON: "{}"}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	first := model.AutomationRun{EntryID: entry.ID, WorkflowID: 1, WorkflowVersion: 1, ExecutionNo: 1, DefinitionJSON: "{}", ContextJSON: "{}"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	node := model.AutomationNodeRun{RunID: first.ID, NodeID: "download", NodeType: AutomationNodeQBittorrent, Status: model.AutomationNodeSucceeded}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	last := first
	last.ID = 0
	last.ExecutionNo = 2
	last.Status = model.AutomationRunPending
	if err := db.Create(&last).Error; err != nil {
		t.Fatal(err)
	}
	history, err := (&AutomationService{db: db}).ListEntryHistory("matched", 1, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if history.Total != 1 || len(history.Items) != 1 || history.Items[0].LatestRun.ID != last.ID {
		t.Fatalf("old match disappeared: %#v", history)
	}
}
