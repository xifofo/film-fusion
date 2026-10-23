package directorymonitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"
	"github.com/fsnotify/fsnotify"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testEvent struct {
	ID   uint   `gorm:"primaryKey"`
	Key  string `gorm:"uniqueIndex"`
	Path string
	Type string
}

func newTestMonitor(t *testing.T) (*Monitor, Config, *time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:directory-monitor-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	models := append(Models(), &testEvent{})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	m := New(db, nil)
	t.Cleanup(m.Close)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	return m, Config{SourceID: 1, Root: t.TempDir(), Recursive: true, QuietSeconds: 10, Options: model.LocalDirectoryConfig{Mode: "poll", BatchSize: 16}}, &now
}

func testEmit(tx *gorm.DB, event Event) error {
	return tx.Create(&testEvent{Key: event.Key, Path: event.RelativePath, Type: event.Type}).Error
}

func scanTest(t *testing.T, m *Monitor, config Config) Result {
	t.Helper()
	result, err := m.Scan(context.Background(), config, testEmit)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func writeTestFile(t *testing.T, root, relative, data string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestBaselineNewDeepDirectoriesAndStableOnce(t *testing.T) {
	m, config, now := newTestMonitor(t)
	writeTestFile(t, config.Root, "old/sub/old.mkv", "baseline")
	baseline := scanTest(t, m, config)
	if !baseline.Baseline || baseline.CreatedEntries != 0 || baseline.ScannedFolders != 3 {
		t.Fatalf("baseline = %+v", baseline)
	}
	writeTestFile(t, config.Root, "new/deep/movie.mkv", "new")
	first := scanTest(t, m, config)
	if first.ScannedFolders != 5 || first.PendingStable != 1 || first.CreatedEntries != 0 {
		t.Fatalf("first = %+v", first)
	}
	*now = now.Add(11 * time.Second)
	second := scanTest(t, m, config)
	if second.CreatedEntries != 1 || second.PendingStable != 0 {
		t.Fatalf("second = %+v", second)
	}
	if third := scanTest(t, m, config); third.CreatedEntries != 0 {
		t.Fatalf("duplicate event = %+v", third)
	}
}

func TestWritingRestartsQuietWindowAndModifiedEvent(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.Events = []string{"created", "modified"}
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "movie.mkv", "a")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	writeTestFile(t, config.Root, "movie.mkv", "still writing")
	if result := scanTest(t, m, config); result.CreatedEntries != 0 || result.PendingStable != 1 {
		t.Fatalf("writing = %+v", result)
	}
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 1 {
		t.Fatalf("settled = %+v", result)
	}
	writeTestFile(t, config.Root, "movie.mkv", "another revision")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	scanTest(t, m, config)
	var events []testEvent
	if err := m.db.Order("id ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "created" || events[1].Type != "modified" {
		t.Fatalf("events = %+v", events)
	}
}

func TestDirectoryQuietWindowIncludesNestedFileWrites(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.ItemType = "directory"
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "show/season/movie.mkv", "a")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	writeTestFile(t, config.Root, "show/season/movie.mkv", "nested file still writing")
	if result := scanTest(t, m, config); result.CreatedEntries != 0 || result.PendingStable != 2 {
		t.Fatalf("nested write = %+v", result)
	}
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 2 {
		t.Fatalf("stable directories = %+v", result)
	}
}

func TestRestartRecoveryRollbackAndReappearance(t *testing.T) {
	m, config, now := newTestMonitor(t)
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "movie.mkv", "same content")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	m.Close()
	restarted := New(m.db, nil)
	restarted.now = m.now
	t.Cleanup(restarted.Close)
	if !restarted.NeedsScan(config.SourceID) {
		t.Fatal("restart must request scan")
	}
	_, err := restarted.Scan(context.Background(), config, func(tx *gorm.DB, event Event) error {
		if err := testEmit(tx, event); err != nil {
			return err
		}
		return errors.New("injected outbox failure")
	})
	if err == nil {
		t.Fatal("expected emitter failure")
	}
	var count int64
	restarted.db.Model(&testEvent{}).Count(&count)
	if count != 0 {
		t.Fatal("outbox write escaped rollback")
	}
	if result := scanTest(t, restarted, config); result.CreatedEntries != 1 {
		t.Fatalf("retry = %+v", result)
	}
	if err := os.Remove(filepath.Join(config.Root, "movie.mkv")); err != nil {
		t.Fatal(err)
	}
	scanTest(t, restarted, config)
	writeTestFile(t, config.Root, "movie.mkv", "same content")
	scanTest(t, restarted, config)
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, restarted, config); result.CreatedEntries != 1 {
		t.Fatalf("reappearance = %+v", result)
	}
	var events []testEvent
	restarted.db.Order("id ASC").Find(&events)
	if len(events) != 2 || events[0].Key == events[1].Key {
		t.Fatalf("event identity = %+v", events)
	}
}

func TestFailedRootPreservesSnapshotAndPendingCandidate(t *testing.T) {
	m, config, now := newTestMonitor(t)
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "movie.mkv", "pending")
	scanTest(t, m, config)
	moved := config.Root + "-offline"
	if err := os.Rename(config.Root, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background(), config, testEmit); err == nil {
		t.Fatal("missing root must fail")
	}
	var item model.LocalDirectoryItem
	if err := m.db.First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if !item.Present || item.PendingType != "created" {
		t.Fatalf("failed scan changed candidate: %+v", item)
	}
	if err := os.Rename(moved, config.Root); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 1 {
		t.Fatalf("recovery = %+v", result)
	}
}

func TestFiltersNoSymlinksBatchingAndInitialProcessing(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.InitialScan = "process_existing"
	config.Options.IncludeExtensions = []string{"mkv"}
	config.Options.ExcludePatterns = []string{"ignored", "*.tmp"}
	for i := 0; i < 49; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("movie-%d.mkv", i), "a")
	}
	writeTestFile(t, config.Root, "ignored/deep/ignored.mkv", "a")
	writeTestFile(t, config.Root, "other.txt", "a")
	outside := t.TempDir()
	writeTestFile(t, outside, "outside.mkv", "a")
	if err := os.Symlink(outside, filepath.Join(config.Root, "symlink")); err != nil {
		t.Fatal(err)
	}
	first := scanTest(t, m, config)
	if first.PendingStable != 49 || first.ScannedFolders != 1 || first.CreatedEntries != 0 {
		t.Fatalf("first = %+v", first)
	}
	*now = now.Add(11 * time.Second)
	if second := scanTest(t, m, config); second.CreatedEntries != 49 {
		t.Fatalf("second = %+v", second)
	}
}

func TestBoundsRuntimeCleanupAndDarwinFallback(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	if _, err := NormalizeOptions(model.LocalDirectoryConfig{BatchSize: 513}); err == nil {
		t.Fatal("batch cap missing")
	}
	if _, err := NormalizeOptions(model.LocalDirectoryConfig{MaxWatches: MaxGlobalWatches + 1}); err == nil {
		t.Fatal("watch cap missing")
	}
	for id := uint(1); id <= 70; id++ {
		config.SourceID = id
		if err := m.startScan(config); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.sources) != 64 {
		t.Fatalf("runtime cap = %d", len(m.sources))
	}
	m.Forget(1)
	if len(m.sources) != 63 || !m.NeedsScan(1) {
		t.Fatal("forget must release runtime state")
	}
	config.SourceID, config.Options.Mode = 1, "auto"
	result := scanTest(t, m, config)
	if runtime.GOOS == "darwin" && (!result.Degraded || result.WatchedDirectories != 0 || result.Warning == "") {
		t.Fatalf("kqueue fallback = %+v", result)
	}
}

func TestCloseCancelsQueuedScansAndDirtyReadIsNonConsuming(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	if err := m.startScan(config); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sources[1].dirty = true
	m.mu.Unlock()
	if !m.NeedsScan(1) || !m.NeedsScan(1) {
		t.Fatal("NeedsScan consumed dirty flag")
	}
	m.scanGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Scan(ctx, config, testEmit); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled queue = %v", err)
	}
	<-m.scanGate
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.Close() }()
	go func() { defer wg.Done(); m.Close() }()
	wg.Wait()
	if _, err := m.Scan(context.Background(), config, testEmit); err == nil {
		t.Fatal("scan after close must fail")
	}
}

func TestDispatchRevalidatesFileWrittenAfterEnumeration(t *testing.T) {
	m, config, now := newTestMonitor(t)
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "movie.mkv", "first")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	if err := m.db.Model(&model.LocalDirectoryItem{}).Where("source_id = ?", config.SourceID).Update("observations", 2).Error; err != nil {
		t.Fatal(err)
	}
	var state model.LocalDirectoryState
	if err := m.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	config.Options, _ = NormalizeOptions(config.Options)
	writeTestFile(t, config.Root, "movie.mkv", "written after enumeration")
	root, err := os.OpenRoot(config.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var result Result
	state.Cursor = 0
	state.PendingStable = 0
	budget := &sliceBudget{deadline: time.Now().Add(time.Second), remaining: 128}
	if _, err := m.dispatchSlice(context.Background(), root, config, &state, budget, testEmit, &result); err != nil {
		t.Fatal(err)
	}
	result.PendingStable = state.PendingStable
	if result.CreatedEntries != 0 || result.PendingStable != 1 {
		t.Fatalf("stale dispatch = %+v", result)
	}
	var item model.LocalDirectoryItem
	if err := m.db.First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Observations != 1 || !item.StableSince.Equal(*now) {
		t.Fatalf("quiet window not restarted: %+v", item)
	}
}

func TestModifiedOnlyDoesNotReportNewDirectoryAsModification(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.ItemType, config.Options.Events = "directory", []string{"modified"}
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "new/child.mkv", "a")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 0 {
		t.Fatalf("new directory emitted modified: %+v", result)
	}
	writeTestFile(t, config.Root, "new/child.mkv", "changed")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 1 {
		t.Fatalf("changed directory = %+v", result)
	}
}

func TestNestedWriteAfterEnumerationDefersDirectoryEvent(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.ItemType = "directory"
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "show/season/movie.mkv", "first")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	written := false
	if err := m.db.Callback().Update().After("gorm:update").Register("test_late_write", func(tx *gorm.DB) {
		directory, ok := tx.Statement.Model.(*model.LocalDirectoryScan)
		if !ok || directory.Relative != "show/season" || written {
			return
		}
		written = true
		writeTestFile(t, config.Root, "show/season/movie.mkv", "late write after the directory was enumerated")
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.db.Callback().Update().Remove("test_late_write") })
	if result := scanTest(t, m, config); !written || result.CreatedEntries != 0 || result.PendingStable != 2 {
		t.Fatalf("late nested write = %+v; written=%v", result, written)
	}
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 2 {
		t.Fatalf("stable after late write = %+v", result)
	}
}

func TestRemovedSharedWatchReleasesOwnersButPreservesParent(t *testing.T) {
	m, _, _ := newTestMonitor(t)
	parent, child := filepath.Join(t.TempDir(), "parent"), ""
	child = filepath.Join(parent, "child")
	m.mu.Lock()
	m.sources[1] = &sourceRuntime{watches: map[string]uint64{parent: 1, child: 1}}
	m.sources[2] = &sourceRuntime{watches: map[string]uint64{child: 1}}
	m.owners[parent] = map[uint]struct{}{1: {}}
	m.owners[child] = map[uint]struct{}{1: {}, 2: {}}
	m.mu.Unlock()
	m.handleEvent(fsnotify.Event{Name: child, Op: fsnotify.Rename})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owners[child] != nil || len(m.sources[2].watches) != 0 || len(m.sources[1].watches) != 1 || m.owners[parent] == nil {
		t.Fatal("removed watch ownership was not cleaned precisely")
	}
	if !m.sources[1].dirty || !m.sources[2].dirty {
		t.Fatal("both sources must rescan")
	}
}

func TestInterruptedBaselineResumesWithoutProcessingExistingFiles(t *testing.T) {
	m, config, now := newTestMonitor(t)
	for i := 0; i < 40; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("old-%d.mkv", i), "old")
	}
	created := 0
	if err := m.db.Callback().Create().Before("gorm:create").Register("test_partial_failure", func(tx *gorm.DB) {
		switch items := tx.Statement.Model.(type) {
		case *model.LocalDirectoryItem:
			created++
		case *[]model.LocalDirectoryItem:
			created += len(*items)
		default:
			return
		}
		if created >= 20 {
			tx.AddError(errors.New("injected batch failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background(), config, testEmit); err == nil {
		t.Fatal("expected partial scan failure")
	}
	if err := m.db.Callback().Create().Remove("test_partial_failure"); err != nil {
		t.Fatal(err)
	}
	var state model.LocalDirectoryState
	if err := m.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Initialized {
		t.Fatal("partial baseline became initialized")
	}
	if result := scanTest(t, m, config); !result.Baseline || result.CreatedEntries != 0 || result.PendingStable != 0 {
		t.Fatalf("recovered baseline = %+v", result)
	}
	writeTestFile(t, config.Root, "new.mkv", "new")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 1 {
		t.Fatalf("new after recovered baseline = %+v", result)
	}
}

func TestConfigurationResetUsesNewDurableEventIdentity(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.InitialScan = "process_existing"
	writeTestFile(t, config.Root, "movie.mkv", "existing")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	scanTest(t, m, config)
	config.Options.IncludeExtensions = []string{"mkv"}
	if result := scanTest(t, m, config); !result.Baseline {
		t.Fatalf("config reset = %+v", result)
	}
	*now = now.Add(11 * time.Second)
	if result := scanTest(t, m, config); result.CreatedEntries != 1 {
		t.Fatalf("reset event = %+v", result)
	}
	var events []testEvent
	if err := m.db.Order("id ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Key == events[1].Key {
		t.Fatalf("reset reused event identity: %+v", events)
	}
}

func TestNativeWatchCapAndSilentBackendLossRecovery(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	if m.watcher == nil {
		t.Skip("native watcher deliberately disabled on this platform")
	}
	config.Options.Mode, config.Options.MaxWatches = "auto", 2
	for i := 0; i < 5; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("dir-%d/file.mkv", i), "a")
	}
	if result := scanTest(t, m, config); !result.Degraded || result.WatchedDirectories != 2 {
		t.Fatalf("native cap = %+v", result)
	}
	if err := m.watcher.Remove(config.Root); err != nil {
		t.Fatal(err)
	}
	// Simulate an OS watch disappearing without updating source ownership.
	scanTest(t, m, config)
	restored := false
	for _, watched := range m.watcher.WatchList() {
		if watched == config.Root {
			restored = true
		}
	}
	if !restored {
		t.Fatal("silently lost root watch was not rearmed")
	}
}
