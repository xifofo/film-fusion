package directorymonitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

func finishTestCycle(t *testing.T, m *Monitor, config Config, emit Emit) Result {
	t.Helper()
	var total Result
	for slice := 0; slice < 1000; slice++ {
		result, err := m.Scan(context.Background(), config, emit)
		if err != nil {
			t.Fatal(err)
		}
		total.ScannedItems += result.ScannedItems
		total.ScannedFolders += result.ScannedFolders
		total.CreatedEntries += result.CreatedEntries
		total.PendingStable, total.Phase, total.InProgress = result.PendingStable, result.Phase, result.InProgress
		total.CycleCompleted, total.FullScanCompletedAt = result.CycleCompleted, result.FullScanCompletedAt
		if result.CycleCompleted || !result.InProgress {
			return total
		}
	}
	t.Fatal("cycle did not complete after 1000 slices")
	return total
}

func markTestChange(m *Monitor, sourceID uint, absoluteDirectory string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state := m.sources[sourceID]; state != nil {
		state.dirty = true
		m.recordChangeLocked(state, absoluteDirectory)
	}
}

func TestHugeDirectorySlicesResumeAfterRestartAndDoNotFinishBaselineEarly(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	config.Options.ScanItemLimit = 128
	for i := 0; i < 600; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("movie-%04d.mkv", i), "a")
	}
	first := scanTest(t, m, config)
	if !first.InProgress || !first.Baseline || first.CycleCompleted || first.FullScanCompletedAt != nil || first.ScannedItems > 128 {
		t.Fatalf("first slice = %+v", first)
	}
	var state model.LocalDirectoryState
	if err := m.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Initialized {
		t.Fatal("partial baseline initialized")
	}
	m.Close()
	restarted := New(m.db, nil)
	restarted.now = m.now
	t.Cleanup(restarted.Close)
	result := finishTestCycle(t, restarted, config, testEmit)
	if !result.CycleCompleted || result.CreatedEntries != 0 || result.FullScanCompletedAt == nil {
		t.Fatalf("restart = %+v", result)
	}
	var count int64
	if err := m.db.Model(&model.LocalDirectoryItem{}).Count(&count).Error; err != nil || count != 600 {
		t.Fatalf("snapshots = %d, %v", count, err)
	}
	if err := m.db.Model(&model.LocalDirectoryItem{}).Where("observations <> 1").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("same generation acquired extra observations: %d, %v", count, err)
	}
}

func TestMissingObjectsAreNotReconciledDuringPartialEnumeration(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	for i := 0; i < 300; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("movie-%04d.mkv", i), "a")
	}
	finishTestCycle(t, m, config, testEmit)
	if err := os.Remove(filepath.Join(config.Root, "movie-0000.mkv")); err != nil {
		t.Fatal(err)
	}
	config.Options.ScanItemLimit = 128
	if result := scanTest(t, m, config); !result.InProgress {
		t.Fatalf("expected partial scan = %+v", result)
	}
	var item model.LocalDirectoryItem
	if err := m.db.First(&item, "relative = ?", "movie-0000.mkv").Error; err != nil {
		t.Fatal(err)
	}
	if !item.Present || item.MissingSince != nil {
		t.Fatal("partial traversal marked unseen item missing")
	}
	finishTestCycle(t, m, config, testEmit)
	if err := m.db.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.Present || item.MissingSince == nil {
		t.Fatal("successful full observation did not mark missing")
	}
}

func TestWideDirectoryHashResumesAcrossSlicesAndRestart(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	config.Options.ItemType, config.Options.ScanItemLimit = "directory", 128
	for i := 0; i < 350; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("show/movie-%04d.mkv", i), "a")
	}
	restarted := false
	for slice := 0; slice < 100; slice++ {
		result := scanTest(t, m, config)
		var state model.LocalDirectoryState
		if err := m.db.First(&state).Error; err != nil {
			t.Fatal(err)
		}
		if !restarted && state.Phase == "aggregate" && state.AggregateChildCursor > 0 {
			if len(state.AggregateHash) == 0 {
				t.Fatal("wide hash cursor has no durable digest")
			}
			m.Close()
			next := New(m.db, nil)
			next.now = m.now
			t.Cleanup(next.Close)
			m = next
			restarted = true
		}
		if result.CycleCompleted {
			if !restarted {
				t.Fatal("test never exercised hash continuation")
			}
			return
		}
	}
	t.Fatal("wide directory hash did not finish")
}

func TestHotParentScanAvoidsUnchangedSubtreesAndPropagatesAncestors(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.ItemType, config.Options.Events = "directory", []string{"modified"}
	config.FullScanIntervalSeconds = 3600
	for i := 0; i < 30; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("old-%d/child/file.mkv", i), "old")
	}
	writeTestFile(t, config.Root, "a/b/movie.mkv", "old")
	finishTestCycle(t, m, config, testEmit)
	writeTestFile(t, config.Root, "a/b/movie.mkv", "changed")
	markTestChange(m, config.SourceID, filepath.Join(config.Root, "a/b"))
	first := finishTestCycle(t, m, config, testEmit)
	if first.ScannedFolders != 1 || first.PendingStable != 2 || first.CreatedEntries != 0 {
		t.Fatalf("hot path = %+v", first)
	}
	*now = now.Add(11 * time.Second)
	second := finishTestCycle(t, m, config, testEmit)
	if second.CreatedEntries != 2 {
		t.Fatalf("ancestor events = %+v", second)
	}
	writeTestFile(t, config.Root, "root-new.mkv", "new")
	markTestChange(m, config.SourceID, config.Root)
	if result := finishTestCycle(t, m, config, testEmit); result.ScannedFolders != 1 {
		t.Fatalf("hot root recursively rescanned existing tree: %+v", result)
	}
}

func TestStableFilesDispatchWhileColdTraversalRemainsIncomplete(t *testing.T) {
	m, config, now := newTestMonitor(t)
	for i := 0; i < 80; i++ {
		for file := 0; file < 4; file++ {
			writeTestFile(t, config.Root, fmt.Sprintf("cold-%02d/%d.mkv", i, file), "old")
		}
	}
	finishTestCycle(t, m, config, testEmit)
	config.Options.ScanItemLimit = 128
	writeTestFile(t, config.Root, "ready.mkv", "ready")
	first := scanTest(t, m, config)
	if !first.InProgress {
		t.Fatalf("cold traversal unexpectedly complete: %+v", first)
	}
	*now = now.Add(11 * time.Second)
	second := scanTest(t, m, config)
	if !second.InProgress || second.CreatedEntries != 1 {
		t.Fatalf("stable file starved behind cold traversal: %+v", second)
	}
}

func TestBackpressureKeepsCandidateAndResumesWithoutDuplicate(t *testing.T) {
	m, config, now := newTestMonitor(t)
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "ready.mkv", "ready")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	result, err := m.Scan(context.Background(), config, func(*gorm.DB, Event) error { return ErrBackpressure })
	if err != nil || !result.Backpressured || result.BackpressureReason != "runs" || !result.InProgress {
		t.Fatalf("backpressure = %+v, %v", result, err)
	}
	var item model.LocalDirectoryItem
	if err := m.db.First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.PendingType != "created" || item.EmittedRevision != 0 {
		t.Fatal("backpressure consumed pending event")
	}
	if result := finishTestCycle(t, m, config, testEmit); result.CreatedEntries != 1 {
		t.Fatalf("queue recovery = %+v", result)
	}
	if result := finishTestCycle(t, m, config, testEmit); result.CreatedEntries != 0 {
		t.Fatal("queue recovery duplicated event")
	}
}

func TestMissingSnapshotCleanupDoesNotReuseOldOutboxKey(t *testing.T) {
	m, config, now := newTestMonitor(t)
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "movie.mkv", "a")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	scanTest(t, m, config)
	if err := os.Remove(filepath.Join(config.Root, "movie.mkv")); err != nil {
		t.Fatal(err)
	}
	scanTest(t, m, config)
	*now = now.Add(8 * 24 * time.Hour)
	finishTestCycle(t, m, config, testEmit)
	var count int64
	m.db.Model(&model.LocalDirectoryItem{}).Count(&count)
	if count != 0 {
		t.Fatalf("expired missing snapshots = %d", count)
	}
	writeTestFile(t, config.Root, "movie.mkv", "a")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	scanTest(t, m, config)
	var events []testEvent
	m.db.Order("id ASC").Find(&events)
	if len(events) != 2 || events[0].Key == events[1].Key {
		t.Fatalf("GC reused outbox identity: %+v", events)
	}
}

func TestReaderCapWaitsWithoutEvictingActiveSourceAndReclaimsIdleManualReader(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	m.readerLimit = 2
	config.Options.ScanItemLimit = 128
	configs := make([]Config, 3)
	for index := range configs {
		configs[index] = config
		configs[index].SourceID = uint(index + 1)
		configs[index].Root = t.TempDir()
		for file := 0; file < 300; file++ {
			writeTestFile(t, configs[index].Root, fmt.Sprintf("%d.mkv", file), "a")
		}
	}
	scanTest(t, m, configs[0])
	scanTest(t, m, configs[1])
	m.mu.Lock()
	first := m.handles[1]
	m.mu.Unlock()
	third := scanTest(t, m, configs[2])
	if !third.InProgress || !third.Backpressured || third.BackpressureReason != "readers" {
		t.Fatalf("reader cap = %+v", third)
	}
	m.mu.Lock()
	if m.handles[1] != first {
		t.Fatal("active iterator was evicted")
	}
	m.handles[2].lastUsed = time.Now().Add(-6 * time.Minute)
	m.mu.Unlock()
	third = scanTest(t, m, configs[2])
	if third.ScannedItems == 0 || third.Backpressured {
		t.Fatalf("idle reader not reclaimed: %+v", third)
	}
	finishTestCycle(t, m, configs[0], testEmit)
	finishTestCycle(t, m, configs[2], testEmit)
	if result := finishTestCycle(t, m, configs[1], testEmit); !result.CycleCompleted {
		t.Fatal("reclaimed source failed to recover")
	}
}

func TestDirtyBoundOverflowAndUnrelatedSubtreeDoesNotBlockDirectory(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.ItemType = "directory"
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "a/file.mkv", "a")
	writeTestFile(t, config.Root, "b/file.mkv", "b")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	var state model.LocalDirectoryState
	m.db.First(&state)
	state.Phase, state.Cursor, state.PendingStable = "dispatch", 0, 0
	m.db.Save(&state)
	m.db.Model(&model.LocalDirectoryItem{}).Where("is_directory = ?", true).Update("observations", 2)
	markTestChange(m, config.SourceID, filepath.Join(config.Root, "a"))
	result := scanTest(t, m, config)
	if result.CreatedEntries != 1 || result.PendingStable != 1 {
		t.Fatalf("unrelated subtree blocked: %+v", result)
	}
	m.mu.Lock()
	for i := 0; i < maxDirtyDirectories+1; i++ {
		m.recordChangeLocked(m.sources[1], filepath.Join(config.Root, fmt.Sprintf("dirty-%d", i)))
	}
	if m.dirtyPaths > maxDirtyDirectories || !m.sources[1].fullDirty {
		t.Fatal("dirty directory overflow was not bounded and escalated")
	}
	m.mu.Unlock()
}

func TestLegacyEmptySnapshotEpochDoesNotReplayExistingItems(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.InitialScan = "process_existing"
	writeTestFile(t, config.Root, "movie.mkv", "old")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	scanTest(t, m, config)
	if err := m.db.Model(&model.LocalDirectoryItem{}).Where("source_id = ?", config.SourceID).Updates(map[string]any{"snapshot_epoch": "", "incarnation": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.db.Model(&model.LocalDirectoryState{}).Where("source_id = ?", config.SourceID).Updates(map[string]any{"root_identity": "", "phase": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if result := finishTestCycle(t, m, config, testEmit); result.CreatedEntries != 0 {
		t.Fatalf("legacy snapshot replayed: %+v", result)
	}
	var item model.LocalDirectoryItem
	m.db.First(&item)
	if item.SnapshotEpoch == "" || item.Incarnation == "" {
		t.Fatal("legacy snapshot was not lazily normalized")
	}
}

func TestDirtyQueueFailureRestoresRescanSignal(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	config.FullScanIntervalSeconds = 3600
	scanTest(t, m, config)
	markTestChange(m, config.SourceID, config.Root)
	failed := false
	if err := m.db.Callback().Create().Before("gorm:create").Register("test_queue_failure", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Model.(*model.LocalDirectoryScan); ok && !failed {
			failed = true
			tx.AddError(errors.New("queue unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Scan(context.Background(), config, testEmit)
	_ = m.db.Callback().Create().Remove("test_queue_failure")
	if err == nil || !m.NeedsScan(config.SourceID) {
		t.Fatalf("dirty signal lost on queue failure: %v", err)
	}
}

func TestNativeHotDirectoryEventsAndRestartWatchRestoration(t *testing.T) {
	m, config, now := newTestMonitor(t)
	if m.watcher == nil {
		t.Skip("native notifications disabled on this platform")
	}
	config.Options.Mode, config.FullScanIntervalSeconds = "auto", 3600
	writeTestFile(t, config.Root, "known/old.mkv", "old")
	finishTestCycle(t, m, config, testEmit)
	m.Close()
	restarted := New(m.db, nil)
	restarted.now = m.now
	t.Cleanup(restarted.Close)
	m = restarted
	if result := scanTest(t, m, config); result.WatchedDirectories < 2 || result.ScannedItems != 0 {
		t.Fatalf("restart did not restore watches directly: %+v", result)
	}
	writeTestFile(t, config.Root, "known/new.mkv", "new")
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		_, detected := m.sources[config.SourceID].changes["known"]
		m.mu.Unlock()
		if detected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native directory event not delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	first := finishTestCycle(t, m, config, testEmit)
	if first.ScannedFolders != 1 || first.CreatedEntries != 0 {
		t.Fatalf("native hot scan = %+v", first)
	}
	*now = now.Add(11 * time.Second)
	if result := finishTestCycle(t, m, config, testEmit); result.CreatedEntries != 1 {
		t.Fatalf("native stable event = %+v", result)
	}
}

func TestBatchWriteFailureRewindsReaderWithoutLosingItems(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	config.Options.ScanItemLimit = 128
	for i := 0; i < 250; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("item-%03d.mkv", i), "x")
	}
	failed := false
	if err := m.db.Callback().Create().Before("gorm:create").Register("fail_snapshot_batch", func(tx *gorm.DB) {
		if tx.Statement.Table == "local_directory_items" && !failed {
			failed = true
			tx.AddError(errors.New("snapshot write unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Scan(context.Background(), config, testEmit)
	_ = m.db.Callback().Create().Remove("fail_snapshot_batch")
	if err == nil || !failed {
		t.Fatalf("fault not injected: %v", err)
	}
	m.mu.Lock()
	open := len(m.handles)
	m.mu.Unlock()
	if open != 0 {
		t.Fatal("advanced reader retained after snapshot rollback")
	}
	finishTestCycle(t, m, config, testEmit)
	var count int64
	if err := m.db.Model(&model.LocalDirectoryItem{}).Count(&count).Error; err != nil || count != 250 {
		t.Fatalf("items = %d, %v", count, err)
	}
	if err := m.db.Model(&model.LocalDirectoryItem{}).Where("observations <> 1").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("duplicate observations = %d, %v", count, err)
	}
}

func TestCommittedEventSurvivesCheckpointFailureWithoutDuplicate(t *testing.T) {
	m, config, now := newTestMonitor(t)
	scanTest(t, m, config)
	writeTestFile(t, config.Root, "movie.mkv", "x")
	scanTest(t, m, config)
	*now = now.Add(11 * time.Second)
	emitted, failed := false, false
	if err := m.db.Callback().Update().Before("gorm:update").Register("fail_post_emit_checkpoint", func(tx *gorm.DB) {
		if tx.Statement.Table == "local_directory_states" && emitted && !failed {
			failed = true
			tx.AddError(errors.New("checkpoint unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	emit := func(tx *gorm.DB, event Event) error {
		if err := testEmit(tx, event); err != nil {
			return err
		}
		emitted = true
		return nil
	}
	_, err := m.Scan(context.Background(), config, emit)
	_ = m.db.Callback().Update().Remove("fail_post_emit_checkpoint")
	if err == nil || !failed || !emitted {
		t.Fatalf("fault not reached: emitted=%v failed=%v err=%v", emitted, failed, err)
	}
	finishTestCycle(t, m, config, testEmit)
	var count int64
	if err := m.db.Model(&testEvent{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("events = %d, %v", count, err)
	}
	var item model.LocalDirectoryItem
	if err := m.db.First(&item, "relative = ?", "movie.mkv").Error; err != nil {
		t.Fatal(err)
	}
	if item.PendingType != "" || item.EmittedRevision != item.Revision {
		t.Fatalf("candidate not atomically consumed: %+v", item)
	}
}

func TestMigrationAddsSnapshotIdentityWithoutReplayingBaseline(t *testing.T) {
	m, config, now := newTestMonitor(t)
	writeTestFile(t, config.Root, "old.mkv", "x")
	scanTest(t, m, config)
	for _, column := range []string{"SnapshotEpoch", "Incarnation"} {
		if err := m.db.Migrator().DropColumn(&model.LocalDirectoryItem{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	if result := finishTestCycle(t, m, config, testEmit); result.CreatedEntries != 0 || result.PendingStable != 0 {
		t.Fatalf("migration replayed baseline: %+v", result)
	}
	var item model.LocalDirectoryItem
	if err := m.db.First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Incarnation == "" || item.SnapshotEpoch == "" {
		t.Fatalf("migration identities not adopted: %+v", item)
	}
}

func TestCycleQueueReusesRowsAndCleansObsoleteJobsInSlices(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	for i := 0; i < 180; i++ {
		if err := os.Mkdir(filepath.Join(config.Root, fmt.Sprintf("dir-%03d", i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	finishTestCycle(t, m, config, testEmit)
	var rootJob model.LocalDirectoryScan
	if err := m.db.First(&rootJob, "relative = ?", ".").Error; err != nil {
		t.Fatal(err)
	}
	finishTestCycle(t, m, config, testEmit)
	var reused model.LocalDirectoryScan
	if err := m.db.First(&reused, "relative = ?", ".").Error; err != nil {
		t.Fatal(err)
	}
	if rootJob.ID != reused.ID || rootJob.Generation == reused.Generation {
		t.Fatalf("queue did not reuse row: before=%+v after=%+v", rootJob, reused)
	}
	for i := 0; i < 180; i++ {
		if err := os.Remove(filepath.Join(config.Root, fmt.Sprintf("dir-%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	config.Options.ScanItemLimit = 128
	cleanupSlices := 0
	for i := 0; i < 30; i++ {
		result := scanTest(t, m, config)
		if result.Phase == "cleanup" {
			cleanupSlices++
		}
		if result.CycleCompleted {
			break
		}
	}
	if cleanupSlices == 0 {
		t.Fatal("obsolete queue cleanup never yielded")
	}
	var count int64
	if err := m.db.Model(&model.LocalDirectoryScan{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("queue count=%d err=%v", count, err)
	}
}

func TestInFlightDirectoryChangeCoalescesOneRescanAndColdCycleFinishes(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	config.Options.Events = []string{"created", "modified"}
	for i := 0; i < 500; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("movie-%04d.mkv", i), "a")
	}
	finishTestCycle(t, m, config, testEmit)
	config.Options.ScanItemLimit = 128
	first := scanTest(t, m, config)
	if !first.InProgress {
		t.Fatal("expected partial directory")
	}
	var state model.LocalDirectoryState
	if err := m.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	var seen model.LocalDirectoryItem
	if err := m.db.First(&seen, "source_id = ? AND generation = ?", config.SourceID, state.Generation).Error; err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, config.Root, seen.Relative, "changed behind the active directory iterator")
	markTestChange(m, config.SourceID, config.Root)
	scanTest(t, m, config)
	var job model.LocalDirectoryScan
	if err := m.db.First(&job, "relative = ?", ".").Error; err != nil {
		t.Fatal(err)
	}
	if !job.RescanRequested || job.Priority {
		t.Fatalf("event not coalesced or cold lane lost: %+v", job)
	}
	// Persisted requests survive process exit while the unfinished reader resets.
	m.Close()
	restarted := New(m.db, nil)
	restarted.now = m.now
	t.Cleanup(restarted.Close)
	m = restarted
	completed := false
	for i := 0; i < 40; i++ {
		markTestChange(m, config.SourceID, config.Root)
		result := scanTest(t, m, config)
		if result.CycleCompleted {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("continuous hot notifications prevented the cold cycle from finishing")
	}
	if err := m.db.First(&seen, seen.ID).Error; err != nil {
		t.Fatal(err)
	}
	if seen.Size != int64(len("changed behind the active directory iterator")) {
		t.Fatalf("change behind iterator lost: %+v", seen)
	}
	if err := m.db.First(&job, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !job.Done || !job.Rescanned {
		t.Fatalf("coalesced rescan did not finish: %+v", job)
	}
	if err := m.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if !state.ForceFull {
		t.Fatal("changes during the single replay were not retained for the next cycle")
	}
}

func TestFileCandidateRotationRevisitsOldQuietCandidateUnderContinuousGrowth(t *testing.T) {
	m, config, now := newTestMonitor(t)
	config.Options.ScanItemLimit = 128
	config.Options, _ = NormalizeOptions(config.Options)
	scanTest(t, m, config)
	for i := 0; i < 50; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("old-%03d.mkv", i), "x")
	}
	finishTestCycle(t, m, config, testEmit)
	var state model.LocalDirectoryState
	if err := m.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	var oldest model.LocalDirectoryItem
	if err := m.db.Order("id ASC").First(&oldest).Error; err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(config.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	found := false
	for turn := 0; turn < 8; turn++ {
		budget := &sliceBudget{deadline: time.Now().Add(time.Second), remaining: 128}
		var result Result
		if err := m.dispatchReadyFiles(context.Background(), root, config, &state, budget, testEmit, &result); err != nil {
			t.Fatal(err)
		}
		if turn == 0 {
			*now = now.Add(11 * time.Second)
		}
		// Newer candidates arrive faster than the dispatch quota on every turn.
		for i := 0; i < 40; i++ {
			relative := fmt.Sprintf("new-%03d-%03d.mkv", turn, i)
			writeTestFile(t, config.Root, relative, "x")
			info, err := os.Stat(filepath.Join(config.Root, relative))
			if err != nil {
				t.Fatal(err)
			}
			if err := m.db.Transaction(func(tx *gorm.DB) error { return m.observe(tx, config, state, relative, info) }); err != nil {
				t.Fatal(err)
			}
		}
		var count int64
		if err := m.db.Model(&testEvent{}).Where("path = ?", oldest.Relative).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("old candidate starved while newer candidates kept arriving")
	}
}

func TestPendingDirectoryRotationUsesFixedRoundUnderContinuousGrowth(t *testing.T) {
	m, config, _ := newTestMonitor(t)
	config.FullScanIntervalSeconds = 3600
	config.Options, _ = NormalizeOptions(config.Options)
	scanTest(t, m, config)
	var state model.LocalDirectoryState
	if err := m.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	add := func(start, count int) {
		items := make([]model.LocalDirectoryItem, 0, count)
		for i := start; i < start+count; i++ {
			relative := fmt.Sprintf("parent-%05d/movie.mkv", i)
			items = append(items, model.LocalDirectoryItem{SourceID: config.SourceID, PathHash: digest(relative), Relative: relative, Name: "movie.mkv", Present: true, PendingType: "created", Generation: state.Generation, SnapshotEpoch: state.Epoch})
		}
		if err := m.db.Create(&items).Error; err != nil {
			t.Fatal(err)
		}
	}
	add(0, 150)
	for turn := 0; turn < 3; turn++ {
		state.Phase = "idle"
		if err := m.beginCycle(context.Background(), config, &state); err != nil {
			t.Fatal(err)
		}
		if turn == 2 {
			var count int64
			if err := m.db.Model(&model.LocalDirectoryScan{}).Where("source_id = ? AND generation = ? AND relative = ?", config.SourceID, state.Generation, "parent-00000").Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("old pending directory did not return after a bounded round")
			}
		}
		add(150+turn*200, 200)
	}
}

func TestCandidateIndexMigrationAndRealQueryPlansStayOnPendingRows(t *testing.T) {
	m, config, now := newTestMonitor(t)
	// Simulate a database that already has a legacy ordinary index of the old
	// name. AutoMigrate must add the new partial index, not trust that old name.
	if err := m.db.Migrator().DropIndex(&model.LocalDirectoryItem{}, "idx_local_candidates_v3"); err != nil {
		t.Fatal(err)
	}
	if err := m.db.Exec("CREATE INDEX idx_local_pending ON local_directory_items(source_id,id)").Error; err != nil {
		t.Fatal(err)
	}
	if err := m.db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	var indexSQL string
	if err := m.db.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_local_candidates_v3'").Scan(&indexSQL).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexSQL, "WHERE pending_type <> ''") || !strings.Contains(indexSQL, "(`source_id`,`present`,`id`)") {
		t.Fatalf("incorrect migrated candidate index: %s", indexSQL)
	}
	for i := 0; i < 1024; i++ {
		writeTestFile(t, config.Root, fmt.Sprintf("baseline-%04d.mkv", i), "x")
	}
	finishTestCycle(t, m, config, testEmit)
	var pending int64
	if err := m.db.Model(&model.LocalDirectoryItem{}).Where("pending_type <> ''").Count(&pending).Error; err != nil || pending != 0 {
		t.Fatalf("baseline candidates=%d err=%v", pending, err)
	}
	// Capture actual engine queries, including the empty-candidate fast path.
	// EXPLAIN them after callbacks finish; querying inside a callback would lock
	// the single-connection test database.
	captured := map[string]string{}
	if err := m.db.Callback().Query().After("gorm:query").Register("capture_pending_reads", func(tx *gorm.DB) {
		raw := tx.Statement.SQL.String()
		if strings.HasPrefix(raw, "SELECT") && strings.Contains(raw, "pending_type <> ''") {
			captured[raw] = tx.Dialector.Explain(raw, tx.Statement.Vars...)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.db.Callback().Query().Remove("capture_pending_reads") })
	finishTestCycle(t, m, config, testEmit)
	writeTestFile(t, config.Root, "new.mkv", "x")
	finishTestCycle(t, m, config, testEmit)
	config.FullScanIntervalSeconds = 3600
	finishTestCycle(t, m, config, testEmit)
	*now = now.Add(11 * time.Second)
	finishTestCycle(t, m, config, testEmit)
	if len(captured) < 5 {
		t.Fatalf("expected all five actual pending-read shapes, captured %d: %+v", len(captured), captured)
	}
	for raw, sql := range captured {
		if !strings.Contains(raw, "INDEXED BY idx_local_candidates_v3") {
			t.Fatalf("candidate read omitted bounded index: %s", raw)
		}
		var plan []struct{ Detail string }
		if err := m.db.Raw("EXPLAIN QUERY PLAN " + sql).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		if len(plan) != 1 || !strings.Contains(plan[0].Detail, "USING INDEX idx_local_candidates_v3") || !strings.Contains(plan[0].Detail, "source_id=? AND present=?") {
			t.Fatalf("candidate read scans snapshots: %s => %+v", sql, plan)
		}
	}
	var events int64
	if err := m.db.Model(&testEvent{}).Count(&events).Error; err != nil || events != 1 {
		t.Fatalf("indexed reads changed event behavior: count=%d err=%v", events, err)
	}
}
