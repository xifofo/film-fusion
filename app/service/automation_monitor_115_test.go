package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"
)

type automation115MonitorStub struct {
	mu      sync.Mutex
	entries map[string][]Web115File
	failAt  map[string]int
}

func (s *automation115MonitorStub) ListDirectory(
	_ context.Context,
	_ model.CloudStorage,
	directoryID string,
	offset int,
	limit int,
) (Web115ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if failOffset, ok := s.failAt[directoryID]; ok && offset >= failOffset {
		return Web115ListResult{}, errors.New("injected page failure")
	}
	items := s.entries[directoryID]
	if offset >= len(items) {
		return Web115ListResult{Items: []Web115File{}, Total: int64(len(items))}, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	page := append([]Web115File(nil), items[offset:end]...)
	return Web115ListResult{Items: page, Total: int64(len(items))}, nil
}

func TestAutomation115MonitorBuildsBaselineThenDispatchesStableDirectoryOnce(t *testing.T) {
	db := newAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115 主账号",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	definitionJSON, err := MarshalAutomationDefinition(DefaultAutomationDefinition())
	if err != nil {
		t.Fatal(err)
	}
	source := model.AutomationSource{
		Name: "新增媒体", Enabled: true, TriggerType: model.AutomationTrigger115Directory,
		CloudStorageID: storage.ID, DirectoryID: "root", DirectoryPath: "/入库",
		Recursive: true, IntervalSeconds: 300, QuietSeconds: 1,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.AutomationWorkflow{
		SourceID: source.ID, Name: "入库流程", Enabled: true, Version: 1, DefinitionJSON: definitionJSON,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	monitor := &automation115MonitorStub{entries: map[string][]Web115File{
		"root":    {{FileID: "old-dir", ParentID: "root", Name: "旧剧集", IsFile: false, UpdatedAt: 100, Complete: true}},
		"old-dir": {{FileID: "old-file", ParentID: "old-dir", Name: "Old.S01E01.mkv", IsFile: true, Size: 100, UpdatedAt: 100, Complete: true}},
	}, failAt: map[string]int{}}
	automation := &AutomationService{
		db: db, monitor115: monitor, executionWake: make(chan struct{}, 1),
		scanning: make(map[uint]struct{}),
	}

	baseline, err := automation.scanAutomation115(context.Background(), source)
	if err != nil {
		t.Fatalf("baseline scan error = %v", err)
	}
	if !baseline.Baseline || baseline.BaselineItems != 2 || baseline.CreatedEntries != 0 {
		t.Fatalf("unexpected baseline result: %#v", baseline)
	}
	var entryCount int64
	if err := db.Model(&model.AutomationEntry{}).Count(&entryCount).Error; err != nil || entryCount != 0 {
		t.Fatalf("baseline entries = %d, err = %v", entryCount, err)
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}

	monitor.mu.Lock()
	monitor.entries["root"] = []Web115File{
		{FileID: "new-dir", ParentID: "root", Name: "New.Show.S01", IsFile: false, UpdatedAt: 200, Complete: true},
		{FileID: "old-dir", ParentID: "root", Name: "旧剧集", IsFile: false, UpdatedAt: 100, Complete: true},
	}
	monitor.entries["new-dir"] = []Web115File{
		{FileID: "new-file", ParentID: "new-dir", Name: "New.Show.S01E01.mkv", IsFile: true, Size: 200, UpdatedAt: 200, Complete: true},
	}
	monitor.mu.Unlock()

	second, err := automation.scanAutomation115(context.Background(), source)
	if err != nil {
		t.Fatalf("second scan error = %v", err)
	}
	if second.NewCandidates != 1 || second.CreatedEntries != 0 || second.PendingStable != 1 {
		t.Fatalf("unexpected second scan: %#v", second)
	}
	var candidate model.Automation115Candidate
	if err := db.Where("source_id = ? AND file_id = ?", source.ID, "new-dir").First(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	monitor.mu.Lock()
	monitor.entries["new-dir"] = append(monitor.entries["new-dir"], Web115File{
		FileID: "late-file", ParentID: "new-dir", Name: "New.Show.S01E02.mkv",
		IsFile: true, Size: 220, UpdatedAt: 210, Complete: true,
	})
	monitor.mu.Unlock()

	third, err := automation.scanAutomation115(context.Background(), source)
	if err != nil {
		t.Fatalf("late-file scan error = %v", err)
	}
	if third.CreatedEntries != 0 || third.PendingStable != 1 {
		t.Fatalf("late file should only reset the directory quiet window: %#v", third)
	}
	var candidateCount int64
	if err := db.Model(&model.Automation115Candidate{}).Where("source_id = ?", source.ID).Count(&candidateCount).Error; err != nil {
		t.Fatal(err)
	}
	if candidateCount != 1 {
		t.Fatalf("pending directory subtree created %d candidates", candidateCount)
	}
	if err := db.Where("source_id = ? AND file_id = ?", source.ID, "new-dir").First(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&candidate).Update("stable_since", time.Now().Add(-2*time.Second)).Error; err != nil {
		t.Fatal(err)
	}

	fourth, err := automation.scanAutomation115(context.Background(), source)
	if err != nil {
		t.Fatalf("fourth scan error = %v", err)
	}
	if fourth.CreatedEntries != 1 || fourth.CreatedRuns != 1 || fourth.PendingStable != 0 {
		t.Fatalf("unexpected fourth scan: %#v", fourth)
	}
	if err := db.Model(&model.AutomationEntry{}).Count(&entryCount).Error; err != nil || entryCount != 1 {
		t.Fatalf("entries = %d, err = %v", entryCount, err)
	}
	var runCount int64
	if err := db.Model(&model.AutomationRun{}).Count(&runCount).Error; err != nil || runCount != 1 {
		t.Fatalf("runs = %d, err = %v", runCount, err)
	}
	var entry model.AutomationEntry
	if err := db.First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.EventKey != fmt.Sprintf("115:%d:new-dir", storage.ID) || !entry.IsDirectory {
		t.Fatalf("unexpected event: %#v", entry)
	}
}

func TestAutomation115MonitorDoesNotAdvanceCursorWhenLaterPageFails(t *testing.T) {
	db := newAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	source := model.AutomationSource{
		Name: "分页失败", Enabled: true, TriggerType: model.AutomationTrigger115Directory,
		CloudStorageID: storage.ID, DirectoryID: "root", DirectoryPath: "/",
		IntervalSeconds: 300, QuietSeconds: 60,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	items := make([]Web115File, 0, automation115ScanPageSize+1)
	for index := 0; index <= automation115ScanPageSize; index++ {
		items = append(items, Web115File{
			FileID: fmt.Sprintf("file-%04d", index), ParentID: "root",
			Name: fmt.Sprintf("Video.%04d.mkv", index), IsFile: true,
			UpdatedAt: int64(1000 - index), Complete: true,
		})
	}
	monitor := &automation115MonitorStub{
		entries: map[string][]Web115File{"root": items}, failAt: map[string]int{"root": automation115ScanPageSize},
	}
	automation := &AutomationService{db: db, monitor115: monitor}
	if _, err := automation.scanAutomation115(context.Background(), source); err == nil {
		t.Fatal("scan unexpectedly succeeded")
	}
	var cursor model.Automation115DirectoryCursor
	if err := db.Where("source_id = ? AND directory_id = ?", source.ID, "root").First(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	if cursor.Initialized || cursor.CursorUpdatedAt != 0 {
		t.Fatalf("failed scan advanced cursor: %#v", cursor)
	}
}

func TestAutomation115MonitorDropsCandidateThatDisappearsBeforeQuietWindow(t *testing.T) {
	db := newAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	source := model.AutomationSource{
		Name: "临时文件", Enabled: true, Initialized: true,
		TriggerType:    model.AutomationTrigger115Directory,
		CloudStorageID: storage.ID, DirectoryID: "root", DirectoryPath: "/",
		IntervalSeconds: 300, QuietSeconds: 60,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	definitionJSON, err := MarshalAutomationDefinition(DefaultAutomationDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AutomationWorkflow{
		SourceID: source.ID, Name: source.Name, Enabled: true,
		Version: 1, DefinitionJSON: definitionJSON,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Automation115DirectoryCursor{
		SourceID: source.ID, DirectoryID: "root", DirectoryPath: "/", Initialized: true,
		CursorUpdatedAt: 100,
	}).Error; err != nil {
		t.Fatal(err)
	}
	monitor := &automation115MonitorStub{entries: map[string][]Web115File{
		"root": {{
			FileID: "temporary", ParentID: "root", Name: "Temporary.mkv",
			IsFile: true, Size: 100, UpdatedAt: 200, Complete: true,
		}},
	}, failAt: map[string]int{}}
	automation := &AutomationService{db: db, monitor115: monitor}
	first, err := automation.scanAutomation115(context.Background(), source)
	if err != nil || first.PendingStable != 1 {
		t.Fatalf("candidate scan = %#v, %v", first, err)
	}

	monitor.mu.Lock()
	monitor.entries["root"] = nil
	monitor.mu.Unlock()
	second, err := automation.scanAutomation115(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedEntries != 0 || second.PendingStable != 0 {
		t.Fatalf("disappeared candidate scan = %#v", second)
	}
	for _, value := range []any{&model.Automation115Candidate{}, &model.Automation115SeenItem{}, &model.AutomationEntry{}} {
		var count int64
		if err := db.Model(value).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("disappeared file left %T count = %d", value, count)
		}
	}
}

func TestAutomationScanLockIsScopedPerSource(t *testing.T) {
	automation := &AutomationService{scanning: make(map[uint]struct{})}
	if !automation.beginAutomationScan(1) || automation.beginAutomationScan(1) {
		t.Fatal("same source lock was not exclusive")
	}
	if !automation.beginAutomationScan(2) {
		t.Fatal("one source blocked a different source")
	}
	automation.endAutomationScan(1)
	automation.endAutomationScan(2)
}
