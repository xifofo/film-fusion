package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestLocalMonitorPersistsRecursiveEventAndExecutesConfiguredHTTPAfterRestart(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.mkv"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(body) != "created|season/episode.mkv" {
			t.Errorf("unexpected configured action: %s %s", r.Method, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	definition := AutomationDefinition{
		SchemaVersion: 1,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "http", Type: AutomationNodeHTTPRequest, Config: map[string]any{
				"url": server.URL, "method": "POST", "body": "{{item.event_type}}|{{item.relative_path}}", "allow_private_network": true,
			}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "a", Source: "trigger", SourcePort: "next", Target: "http"},
			{ID: "b", Source: "http", SourcePort: "success", Target: "end"},
		},
	}
	created, err := s.CreateAutomation(AutomationCreateInput{
		Source: AutomationSourceInput{
			Name: "NAS incoming", Enabled: true, TriggerType: model.AutomationTriggerLocalDirectory,
			DirectoryPath: root, Recursive: true, QuietSeconds: 1,
			LocalConfig: &model.LocalDirectoryConfig{Mode: "poll", IncludeExtensions: []string{"mkv"}},
		}, Workflow: AutomationCreateWorkflowInput{Definition: definition},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || !baseline.Baseline || baseline.CreatedRuns != 0 {
		t.Fatalf("baseline: %+v, %v", baseline, err)
	}
	if err := os.Mkdir(filepath.Join(root, "season"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "season", "episode.mkv"), []byte("movie"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "season", "ignored.txt"), []byte("sidecar"), 0600); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || pending.CreatedRuns != 0 || pending.PendingStable == 0 {
		t.Fatalf("pending write: %+v, %v", pending, err)
	}
	// Recreate the runtime to prove that the quiet candidate lives in SQLite.
	s.closeLocalMonitor()
	time.Sleep(1100 * time.Millisecond)
	ready, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || ready.CreatedRuns != 1 || ready.CreatedEntries != 1 {
		t.Fatalf("ready after restart: %+v, %v", ready, err)
	}
	duplicate, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || duplicate.CreatedRuns != 0 {
		t.Fatalf("duplicate: %+v, %v", duplicate, err)
	}
	var entry model.AutomationEntry
	if err := s.db.First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(entry.FieldsJSON), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["root_path"] != root || fields["source"] != model.AutomationTriggerLocalDirectory || fields["event_type"] != "created" {
		t.Fatalf("event fields: %#v", fields)
	}
	// Advance the durable workflow without starting unrelated source schedulers.
	for i := 0; i < 8; i++ {
		var run model.AutomationRun
		if err := s.db.First(&run).Error; err != nil {
			t.Fatal(err)
		}
		if run.Status == model.AutomationRunSucceeded {
			break
		}
		if err := s.advanceAutomationRun(run); err != nil {
			t.Fatal(err)
		}
		s.workerWG.Wait()
	}
	var run model.AutomationRun
	if err := s.db.First(&run).Error; err != nil || run.Status != model.AutomationRunSucceeded || requests.Load() != 1 {
		t.Fatalf("durable action: %+v requests=%d err=%v", run, requests.Load(), err)
	}
	var source model.AutomationSource
	if err := s.db.First(&source, created.Source.ID).Error; err != nil || !source.Initialized || source.LocalStatusJSON == "" {
		t.Fatalf("runtime status not persisted: %+v, %v", source, err)
	}
}

func TestLocalMonitorUnavailableRootKeepsHistoryAndRecovers(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	root := t.TempDir()
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "Disconnected NAS", TriggerType: model.AutomationTriggerLocalDirectory,
		DirectoryPath: root, Recursive: true, LocalConfig: &model.LocalDirectoryConfig{Mode: "poll"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), created.Source.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), created.Source.ID); err == nil {
		t.Fatal("missing mount was accepted as an empty directory")
	}
	var source model.AutomationSource
	if err := s.db.First(&source, created.Source.ID).Error; err != nil || !source.Initialized || source.LastError == "" {
		t.Fatalf("failure state: %+v, %v", source, err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), created.Source.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.First(&source, created.Source.ID).Error; err != nil || strings.TrimSpace(source.LastError) != "" {
		t.Fatalf("recovery state: %+v, %v", source, err)
	}
}

func TestLocalMonitorQuietCandidatesScheduleBeforePeriodicScan(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	last := time.Now().Add(-31 * time.Second)
	source := model.AutomationSource{
		ID: 99, LastCheckedAt: &last, QuietSeconds: 30,
		LocalStatusJSON: `{"pending_stable":2}`,
	}
	if !s.localSourceDue(source, time.Now(), 5*time.Minute) {
		t.Fatal("quiet candidates were left waiting for full reconciliation interval")
	}
	last = time.Now()
	if s.localSourceDue(source, time.Now(), 5*time.Minute) {
		t.Fatal("event storms bypassed scan coalescing")
	}
}

func TestLocalMonitorPartialScanResumesWithoutStormDelayButBackpressureWaits(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	now := time.Now()
	source := model.AutomationSource{ID: 99, LastCheckedAt: &now, LocalStatusJSON: `{"in_progress":true,"phase":"enumerate"}`}
	if !s.localSourceDue(source, now, time.Hour) {
		t.Fatal("large scan slice incurred event-storm delay")
	}
	source.LastError = "NAS unavailable"
	if s.localSourceDue(source, now, time.Hour) {
		t.Fatal("failed scan slice bypassed retry backoff")
	}
	source.LastError = ""
	source.LocalStatusJSON = `{"in_progress":true,"phase":"dispatch","backpressured":true}`
	if s.localSourceDue(source, now, time.Hour) {
		t.Fatal("full action queue busy-looped")
	}
	if !s.localSourceDue(source, now.Add(5*time.Second), time.Hour) {
		t.Fatal("full action queue was never retried")
	}
}

func TestLocalMonitorBackpressureKeepsCandidateAndResumesWithoutDuplicate(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	root := t.TempDir()
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "bounded queue", Enabled: true, TriggerType: model.AutomationTriggerLocalDirectory,
		DirectoryPath: root, Recursive: true, QuietSeconds: 1,
		LocalConfig: &model.LocalDirectoryConfig{Mode: "poll", MaxPendingRuns: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), created.Source.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.mkv", "b.mkv"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("stable"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Scan(context.Background(), created.Source.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	first, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || first.CreatedRuns != 1 || first.LocalMonitor == nil || !first.LocalMonitor.Backpressured || first.LocalMonitor.QueuedRuns != 1 {
		t.Fatalf("first pressure: %+v %v", first, err)
	}
	var candidates int64
	if err := s.db.Model(&model.LocalDirectoryItem{}).Where("source_id = ? AND pending_type <> ''", created.Source.ID).Count(&candidates).Error; err != nil || candidates != 1 {
		t.Fatalf("backpressure consumed candidate: %d %v", candidates, err)
	}
	blocked, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || blocked.CreatedEntries != 0 || blocked.CreatedRuns != 0 {
		t.Fatalf("queue exceeded limit: %+v %v", blocked, err)
	}
	now := time.Now()
	if err := s.db.Model(&model.AutomationRun{}).Where("status = ?", model.AutomationRunPending).
		Updates(map[string]any{"status": model.AutomationRunSucceeded, "completed_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&model.AutomationNodeRun{}).Where("status = ?", model.AutomationNodePending).
		Updates(map[string]any{"status": model.AutomationNodeSucceeded, "completed_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	resumed, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || resumed.CreatedEntries != 1 || resumed.CreatedRuns != 1 {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	var entries int64
	if err := s.db.Model(&model.AutomationEntry{}).Where("source_id = ?", created.Source.ID).Count(&entries).Error; err != nil || entries != 2 {
		t.Fatalf("event lost or repeated: %d %v", entries, err)
	}
	if err := s.db.Model(&model.AutomationRun{}).Where("status = ?", model.AutomationRunPending).
		Updates(map[string]any{"status": model.AutomationRunSucceeded, "completed_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&model.AutomationNodeRun{}).Where("status = ?", model.AutomationNodePending).
		Updates(map[string]any{"status": model.AutomationNodeSucceeded, "completed_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	options, _ := ParseLocalDirectoryConfig(created.Source.LocalConfigJSON)
	if _, err := s.cleanupLocalAutomationHistory(context.Background(), created.Source.ID, options, now.AddDate(0, 0, 40)); err != nil {
		t.Fatal(err)
	}
	afterRetention, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || afterRetention.CreatedEntries != 0 || afterRetention.CreatedRuns != 0 {
		t.Fatalf("history retention replayed an emitted filesystem revision: %+v %v", afterRetention, err)
	}
}

func TestLocalMonitorPausedManualSlicesPreserveEnumerationProgress(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	root := t.TempDir()
	for i := 0; i < 260; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.mkv", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "paused manual slices", TriggerType: model.AutomationTriggerLocalDirectory, DirectoryPath: root,
		LocalConfig: &model.LocalDirectoryConfig{Mode: "auto", ScanItemLimit: 128},
	}})
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for i := 0; i < 30; i++ {
		result, err := s.Scan(context.Background(), created.Source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.LocalMonitor.WatchedDirectories != 0 {
			t.Fatal("paused scan installed watches")
		}
		if result.LocalMonitor.CycleCompleted {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("paused slices restarted the wide directory instead of advancing")
	}
	var source model.AutomationSource
	if err := s.db.First(&source, created.Source.ID).Error; err != nil || source.Enabled || !source.Initialized {
		t.Fatalf("paused source lifecycle: %+v %v", source, err)
	}
}

func TestLocalMonitorPartialBaselineDoesNotReportSuccessfulInitialization(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	root := t.TempDir()
	for i := 0; i < 260; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.mkv", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "sliced baseline", Enabled: true, TriggerType: model.AutomationTriggerLocalDirectory, DirectoryPath: root,
		LocalConfig: &model.LocalDirectoryConfig{Mode: "poll", ScanBudgetMS: 100, ScanItemLimit: 128},
	}})
	if err != nil {
		t.Fatal(err)
	}
	partial, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || partial.LocalMonitor == nil || !partial.LocalMonitor.InProgress {
		t.Fatalf("not sliced: %+v %v", partial, err)
	}
	var source model.AutomationSource
	if err := s.db.First(&source, created.Source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.Initialized || source.LastSuccessAt != nil {
		t.Fatalf("incomplete baseline declared ready: %+v", source)
	}
	for i := 0; i < 30; i++ {
		result, err := s.Scan(context.Background(), created.Source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.LocalMonitor.CycleCompleted {
			break
		}
	}
	if err := s.db.First(&source, created.Source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !source.Initialized || source.LastSuccessAt == nil {
		t.Fatalf("finished baseline not ready: %+v", source)
	}
}

func TestLocalMonitorHistoryPressurePausesDispatchAndRecoversAtLimit(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	root := t.TempDir()
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "history flow control", Enabled: true, TriggerType: model.AutomationTriggerLocalDirectory,
		DirectoryPath: root, QuietSeconds: 1,
		LocalConfig: &model.LocalDirectoryConfig{Mode: "poll", HistoryMaxRuns: 100, MaxPendingRuns: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), created.Source.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ready.mkv"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), created.Source.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.db.Model(&model.LocalDirectoryItem{}).Where("source_id = ?", created.Source.ID).
		Updates(map[string]any{"observations": 2, "stable_since": now.Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		seedLocalHistoryRun(t, s, created, fmt.Sprintf("recent-%03d", i), now.Add(time.Duration(i-205)*time.Minute))
	}
	paused, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || paused.CreatedEntries != 0 || paused.LocalMonitor == nil || !paused.LocalMonitor.Backpressured ||
		paused.LocalMonitor.BackpressureReason != "history" || paused.LocalMonitor.QueuedRuns != 0 || !strings.Contains(paused.LocalMonitor.Warning, "暂缓派发") {
		t.Fatalf("history did not apply distinct backpressure: %+v %v", paused, err)
	}
	var pending int64
	if err := s.db.Model(&model.LocalDirectoryItem{}).Where("source_id = ? AND pending_type <> ''", created.Source.ID).Count(&pending).Error; err != nil || pending != 1 {
		t.Fatalf("history pressure lost candidate: %d %v", pending, err)
	}
	resumed, err := s.Scan(context.Background(), created.Source.ID)
	if err != nil || resumed.CreatedEntries != 1 || resumed.CreatedRuns != 1 || resumed.LocalMonitor.BackpressureReason == "history" {
		t.Fatalf("equal-to-limit cleanup deadlocked dispatch: %+v %v", resumed, err)
	}
}
