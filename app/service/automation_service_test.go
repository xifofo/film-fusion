package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestCreateAutomationValidatesOrganizeAccountAndCookie(t *testing.T) {
	db := newAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115 主账号",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{
		UserID: 1, CloudStorageID: storage.ID, DirectoryName: "媒体库", DirectoryID: "library",
	}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1)}
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "organize", Type: AutomationNodeOrganizeStrm, Config: map[string]any{"cloud_directory_id": directory.ID}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "organize"},
			{ID: "e2", Source: "organize", SourcePort: "success", Target: "end"},
		},
	}
	input := AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "入库", CloudStorageID: storage.ID, DirectoryID: "inbox",
			DirectoryPath: "/入库", IntervalSeconds: 300, QuietSeconds: 120,
		},
		Workflow: AutomationCreateWorkflowInput{Name: "入库", Definition: definition},
	}
	if _, err := automation.CreateAutomation(input); err == nil || !strings.Contains(err.Error(), "Cookie") {
		t.Fatalf("missing organize Cookie error = %v", err)
	}
	if err := db.Model(&storage).Update("cookie", "UID=1").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := automation.CreateAutomation(input); err != nil {
		t.Fatalf("valid organize monitor rejected: %v", err)
	}
}

func TestUpdateAutomationAtomicallyResetsMonitorAndVersionsWorkflow(t *testing.T) {
	db := newAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115 主账号",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	automation := &AutomationService{
		db: db, ctx: ctx, cancel: cancel,
		sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		scanning: make(map[uint]struct{}),
	}
	created, err := automation.CreateAutomation(AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "入库监控", Enabled: true, CloudStorageID: storage.ID,
			DirectoryID: "root-a", DirectoryPath: "/入库 A", Recursive: true,
			IntervalSeconds: 300, QuietSeconds: 120,
		},
		Workflow: AutomationCreateWorkflowInput{
			Name: "入库监控", Enabled: true, Definition: DefaultAutomationDefinition(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&created.Source).Update("initialized", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Automation115SeenItem{
		SourceID: created.Source.ID, FileID: "old-file", ParentID: "root-a",
		Name: "old.mkv", Path: "/入库 A/old.mkv", Baseline: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Automation115Candidate{
		SourceID: created.Source.ID, FileID: "pending", ParentID: "root-a",
		Name: "pending.mkv", Path: "/入库 A/pending.mkv", StateHash: "state",
	}).Error; err != nil {
		t.Fatal(err)
	}

	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "notify", Type: AutomationNodeNotification, Config: map[string]any{"message": "{{item.path}}"}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "notify"},
			{ID: "e2", Source: "notify", SourcePort: "success", Target: "end"},
		},
	}
	updated, err := automation.UpdateAutomation(created.Source.ID, AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "新入库监控", Enabled: false, CloudStorageID: storage.ID,
			DirectoryID: "root-b", DirectoryPath: "/入库 B", Recursive: false,
			IntervalSeconds: 600, QuietSeconds: 180,
		},
		Workflow: AutomationCreateWorkflowInput{
			Name: "新入库监控", Enabled: false, Definition: definition,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Source.DirectoryID != "root-b" || updated.Source.Initialized || updated.Source.Enabled {
		t.Fatalf("updated source = %#v", updated.Source)
	}
	if updated.Workflow.Version != 2 || updated.Workflow.Enabled {
		t.Fatalf("updated workflow = %#v", updated.Workflow)
	}
	var cursors []model.Automation115DirectoryCursor
	if err := db.Where("source_id = ?", created.Source.ID).Find(&cursors).Error; err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 1 || cursors[0].DirectoryID != "root-b" {
		t.Fatalf("reset cursors = %#v", cursors)
	}
	for _, value := range []any{&model.Automation115SeenItem{}, &model.Automation115Candidate{}} {
		var count int64
		if err := db.Model(value).Where("source_id = ?", created.Source.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("reset state %T count = %d", value, count)
		}
	}

	entry := model.AutomationEntry{
		SourceID: created.Source.ID, Fingerprint: "fingerprint", EventKey: "115:1:new-file",
		ItemID: "new-file", Title: "new.mkv", Path: "/入库 B/new.mkv",
		FieldsJSON: `{}`, DiscoveredAt: time.Now(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	run := model.AutomationRun{
		WorkflowID: updated.Workflow.ID, WorkflowName: updated.Workflow.Name,
		WorkflowVersion: updated.Workflow.Version, EntryID: entry.ID,
		DefinitionJSON: updated.Workflow.DefinitionJSON, ContextJSON: `{}`,
		Status: model.AutomationRunSucceeded,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AutomationNodeRun{
		RunID: run.ID, NodeID: "trigger", NodeType: AutomationNodeTrigger,
		Status: model.AutomationNodeSucceeded,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := automation.DeleteAutomation(created.Source.ID); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		&model.AutomationSource{}, &model.AutomationWorkflow{},
		&model.AutomationEntry{}, &model.AutomationRun{}, &model.AutomationNodeRun{},
		&model.Automation115DirectoryCursor{},
	} {
		var count int64
		if err := db.Model(value).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("deleted automation left %T count = %d", value, count)
		}
	}
}

func TestAutomationMonitorMutationRejectsConcurrentScan(t *testing.T) {
	db := newAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115 主账号",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		scanning: make(map[uint]struct{}),
	}
	created, err := automation.CreateAutomation(AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "入库监控", Enabled: true, CloudStorageID: storage.ID,
			DirectoryID: "root", DirectoryPath: "/入库", Recursive: true,
			IntervalSeconds: 300, QuietSeconds: 120,
		},
		Workflow: AutomationCreateWorkflowInput{
			Name: "入库监控", Enabled: true, Definition: DefaultAutomationDefinition(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !automation.beginAutomationScan(created.Source.ID) {
		t.Fatal("failed to hold scan lock")
	}
	defer automation.endAutomationScan(created.Source.ID)

	input := AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "不应写入", Enabled: false, CloudStorageID: storage.ID,
			DirectoryID: "other", DirectoryPath: "/其他", Recursive: false,
			IntervalSeconds: 600, QuietSeconds: 180,
		},
		Workflow: AutomationCreateWorkflowInput{
			Name: "不应写入", Enabled: false, Definition: DefaultAutomationDefinition(),
		},
	}
	if _, err := automation.UpdateAutomation(created.Source.ID, input); !errors.Is(err, ErrAutomationScanRunning) {
		t.Fatalf("concurrent update error = %v", err)
	}
	if err := automation.DeleteAutomation(created.Source.ID); !errors.Is(err, ErrAutomationScanRunning) {
		t.Fatalf("concurrent delete error = %v", err)
	}
	var unchanged model.AutomationSource
	if err := db.First(&unchanged, created.Source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Name != created.Source.Name || unchanged.DirectoryID != created.Source.DirectoryID {
		t.Fatalf("concurrent mutation changed source: %#v", unchanged)
	}
}
