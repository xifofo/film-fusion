package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

func TestLocalMonitorIdleSourceUpdateDoesNotWaitForAnotherScan(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(s.closeLocalMonitor)
	createSource := func(name string) AutomationCreateResult {
		t.Helper()
		created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
			Name: name, TriggerType: model.AutomationTriggerLocalDirectory, DirectoryPath: t.TempDir(),
			LocalConfig: &model.LocalDirectoryConfig{Mode: "poll"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	a, b := createSource("正在扫描的目录"), createSource("空闲目录")
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	releaseScan := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseScan)
	// Hold A at a standalone checkpoint after its DB transaction has released.
	// B must remain editable while another monitor owns the global scan slot.
	if err := s.db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("test:hold_local_scan", func(tx *gorm.DB) {
		if tx.Statement.Table != "local_directory_states" {
			return
		}
		// Enumeration checkpoints share the queue transaction; later phases save
		// independently. Pause after a standalone save releases SQLite's single
		// connection, without coupling this barrier to a specific scan phase.
		if _, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter); inTransaction {
			return
		}
		state, ok := tx.Statement.Model.(*model.LocalDirectoryState)
		if !ok || state.SourceID != a.Source.ID || state.Generation == 0 {
			return
		}
		enterOnce.Do(func() {
			close(entered)
			<-release
		})
	}); err != nil {
		t.Fatal(err)
	}
	scanDone := make(chan error, 1)
	go func() {
		_, err := s.Scan(context.Background(), a.Source.ID)
		scanDone <- err
	}()
	select {
	case <-entered:
	case err := <-scanDone:
		t.Fatalf("scan finished before reaching the test barrier: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not reach the test barrier")
	}
	updated := make(chan error, 1)
	go func() {
		input := automationSourceInputFromModel(b.Source)
		input.LocalConfig.MaxWatches = 10
		_, err := s.UpdateSource(b.Source.ID, input)
		updated <- err
	}()
	select {
	case err := <-updated:
		if err != nil {
			t.Errorf("updating the idle monitor failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("updating idle source B waited for unrelated source A to finish scanning")
		releaseScan()
		if err := <-updated; err != nil {
			t.Errorf("update after releasing the scan: %v", err)
		}
	}
	releaseScan()
	if err := <-scanDone; err != nil {
		t.Fatal(err)
	}
}

func TestLocalMonitorSchedulerResumesSlicesBeforePeriodicTickAndFinishesOtherRoot(t *testing.T) {
	s := managementTestService(t)
	t.Cleanup(func() {
		s.cancel()
		s.wg.Wait()
		s.scanWorkerWG.Wait()
		s.closeLocalMonitor()
	})
	root := t.TempDir()
	for i := 0; i < 260; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.mkv", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var sources []uint
	for index, directory := range []string{root, t.TempDir()} {
		created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
			Name: fmt.Sprintf("scheduled root %d", index), Enabled: true, TriggerType: model.AutomationTriggerLocalDirectory,
			DirectoryPath: directory, LocalConfig: &model.LocalDirectoryConfig{Mode: "poll", ScanItemLimit: 128},
		}})
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, created.Source.ID)
	}
	s.wg.Add(1)
	go s.sourceScheduleLoop()
	s.Wake()
	// The normal ticker is five seconds. Completing both roots before it
	// fires proves the post-release wake resumes slices and allows rotation.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var initialized int64
		if err := s.db.Model(&model.AutomationSource{}).Where("id IN ? AND initialized = ?", sources, true).Count(&initialized).Error; err != nil {
			t.Fatal(err)
		}
		if initialized == int64(len(sources)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bounded scan continuation waited for the five-second periodic tick or starved another root")
}

func TestLocalMonitorPausedManualScanReleasesRuntimeResources(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "available"
		if unavailable {
			name = "unavailable"
		}
		t.Run(name, func(t *testing.T) {
			s := managementTestService(t)
			t.Cleanup(s.closeLocalMonitor)
			root := t.TempDir()
			if unavailable {
				root = filepath.Join(root, "disconnected-nas")
			}
			created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
				Name: "已停用目录", TriggerType: model.AutomationTriggerLocalDirectory, DirectoryPath: root,
				LocalConfig: &model.LocalDirectoryConfig{Mode: "auto"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.Scan(context.Background(), created.Source.ID)
			if (err != nil) != unavailable {
				t.Fatalf("manual scan error = %v, unavailable = %v", err, unavailable)
			}
			if result.CreatedRuns != 0 || (result.LocalMonitor != nil && result.LocalMonitor.WatchedDirectories != 0) {
				t.Fatalf("paused manual scan installed background work: %+v", result)
			}
			// An unseen source is due immediately. A retained clean runtime slot
			// would instead report false here, even on platforms without fsnotify.
			if !s.localDirectoryMonitor().NeedsScan(created.Source.ID) {
				t.Fatal("paused manual scan retained a runtime source slot")
			}
			var source model.AutomationSource
			if err := s.db.First(&source, created.Source.ID).Error; err != nil {
				t.Fatal(err)
			}
			config, err := ParseLocalDirectoryConfig(source.LocalConfigJSON)
			if err != nil || config.Mode != "auto" || source.Enabled {
				t.Fatalf("manual scan changed saved configuration: source=%+v config=%+v err=%v", source, config, err)
			}
		})
	}
}
