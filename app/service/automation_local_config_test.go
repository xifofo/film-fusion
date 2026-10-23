package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"film-fusion/app/directorymonitor"
	"film-fusion/app/model"
	"gorm.io/gorm"
)

func TestLocalDirectorySourceDefaultsAndValidation(t *testing.T) {
	// No database or provider is required to validate an unavailable NAS mount.
	s := &AutomationService{}
	base := AutomationSourceInput{Name: "NAS 入库", TriggerType: model.AutomationTriggerLocalDirectory,
		DirectoryPath: "/mnt/unavailable/inbox", CloudStorageID: 9999, DirectoryID: "115-root", FeedURL: "https://example.com/rss"}
	normalized, mapping, err := s.validateAutomationSourceInput(base)
	if err != nil {
		t.Fatal(err)
	}
	config := normalized.LocalConfig
	if mapping != "" || normalized.CloudStorageID != 0 || normalized.DirectoryID != "" || normalized.FeedURL != "" ||
		normalized.IntervalSeconds != 300 || normalized.QuietSeconds != 30 || config == nil || config.Mode != "auto" ||
		config.InitialScan != "baseline" || config.ItemType != "file" || config.MaxWatches != 1024 || config.BatchSize != 128 ||
		config.ScanBudgetMS != 2000 || config.ScanItemLimit != 4096 || config.MaxPendingRuns != 1000 ||
		config.HistoryRetentionDays != 30 || config.HistoryMaxRuns != 10000 || config.MissingRetentionDays != 7 ||
		!reflect.DeepEqual(config.Events, []string{"created"}) {
		t.Fatalf("unexpected defaults: %#v / %#v", normalized, config)
	}
	tests := []struct {
		name string
		edit func(*AutomationSourceInput)
	}{
		{"relative path", func(i *AutomationSourceInput) { i.DirectoryPath = "inbox" }},
		{"root", func(i *AutomationSourceInput) { i.DirectoryPath = "/tmp/.." }},
		{"null path", func(i *AutomationSourceInput) { i.DirectoryPath = "/tmp/inbox\x00" }},
		{"long path", func(i *AutomationSourceInput) { i.DirectoryPath = "/" + strings.Repeat("a", 4096) }},
		{"interval", func(i *AutomationSourceInput) { i.IntervalSeconds = 29 }},
		{"quiet", func(i *AutomationSourceInput) { i.QuietSeconds = -1 }},
		{"watches", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{MaxWatches: 8193} }},
		{"batch", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{BatchSize: 15} }},
		{"scan budget", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{ScanBudgetMS: 99} }},
		{"scan item limit", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{ScanItemLimit: 127} }},
		{"pending high water", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{MaxPendingRuns: -1} }},
		{"history days", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{HistoryRetentionDays: 366} }},
		{"history count", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{HistoryMaxRuns: 99} }},
		{"missing days", func(i *AutomationSourceInput) { i.LocalConfig = &model.LocalDirectoryConfig{MissingRetentionDays: -1} }},
		{"event", func(i *AutomationSourceInput) {
			i.LocalConfig = &model.LocalDirectoryConfig{Events: []string{"deleted"}}
		}},
		{"duplicate event", func(i *AutomationSourceInput) {
			i.LocalConfig = &model.LocalDirectoryConfig{Events: []string{"created", "created"}}
		}},
		{"glob", func(i *AutomationSourceInput) {
			i.LocalConfig = &model.LocalDirectoryConfig{ExcludePatterns: []string{"["}}
		}},
		{"extension", func(i *AutomationSourceInput) {
			i.LocalConfig = &model.LocalDirectoryConfig{IncludeExtensions: []string{"*.mkv"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := base
			tt.edit(&input)
			if _, _, err := s.validateAutomationSourceInput(input); err == nil {
				t.Fatal("invalid local monitor accepted")
			}
		})
	}
}

func TestLocalDirectoryConfigurationChangesRetainOnlyCompatibleBaseline(t *testing.T) {
	s := &AutomationService{}
	input, _, err := s.validateAutomationSourceInput(AutomationSourceInput{Name: "本地", TriggerType: model.AutomationTriggerLocalDirectory,
		DirectoryPath: "/mnt/inbox", Recursive: true,
		LocalConfig: &model.LocalDirectoryConfig{Events: []string{"modified", "created"}, IncludeExtensions: []string{"MKV", ".mp4"}, ExcludePatterns: []string{"*.part", ".cache"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := model.AutomationSource{TriggerType: input.TriggerType, DirectoryPath: input.DirectoryPath, Recursive: true, LocalConfigJSON: automationLocalConfigJSON(input)}
	config, err := ParseLocalDirectoryConfig(source.LocalConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	config.Mode, config.MaxWatches, config.BatchSize = "poll", 5, 16
	config.ScanBudgetMS, config.ScanItemLimit, config.MaxPendingRuns = 100, 128, 10
	config.HistoryRetentionDays, config.HistoryMaxRuns, config.MissingRetentionDays = 10, 100, 2
	config.Events = []string{"modified", "created"}
	config.IncludeExtensions = []string{"mp4", "mkv"}
	config.ExcludePatterns = []string{".cache", "*.part"}
	input.LocalConfig, input.IntervalSeconds, input.QuietSeconds = &config, 900, 10
	if automationSourceConfigurationChanged(source, input, "") {
		t.Fatal("runtime settings and equivalent rules reset the baseline")
	}
	config.IncludeExtensions = []string{"mkv"}
	if !automationSourceConfigurationChanged(source, input, "") {
		t.Fatal("discovery rule change retained an incompatible baseline")
	}
	if _, err := ParseLocalDirectoryConfig(`{"events":["deleted"]}`); err == nil {
		t.Fatal("corrupt persisted config accepted")
	}
}

func TestLocalDirectoryConfigurationLifecycle(t *testing.T) {
	s := managementTestService(t)
	if err := s.db.AutoMigrate(directorymonitor.Models()...); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "本机媒体入库", TriggerType: model.AutomationTriggerLocalDirectory,
		DirectoryPath: filepath.Join(t.TempDir(), "not-mounted"), Enabled: true,
		LocalConfig: &model.LocalDirectoryConfig{IncludeExtensions: []string{"mkv"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := automationConfiguration(created.Source, created.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Source.LocalConfig == nil || configuration.Source.LocalConfig.IncludeExtensions[0] != ".mkv" ||
		len(configuration.Workflow.Definition.Nodes) != 3 || configuration.Workflow.Definition.Nodes[1].Type != AutomationNodeNotification {
		t.Fatalf("local configuration was lost: %#v", configuration)
	}
	if _, err := s.SaveAutomationTemplate(0, AutomationTemplateInput{Name: "本地模板", Configuration: configuration}); err != nil {
		t.Fatal(err)
	}
	state := model.LocalDirectoryState{SourceID: created.Source.ID, Configuration: "existing", Initialized: true}
	if err := s.db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&created.Source).Updates(map[string]any{"initialized": true, "local_status_json": `{"watched_directories":3}`}).Error; err != nil {
		t.Fatal(err)
	}
	configuration.Source.LocalConfig.MaxWatches = 10
	updated, err := s.UpdateAutomation(created.Source.ID, configuration)
	if err != nil || !updated.Source.Initialized {
		t.Fatalf("resource-only edit lost baseline: source=%#v error=%v", updated.Source, err)
	}
	var count int64
	if err := s.db.Model(&model.LocalDirectoryState{}).Where("source_id = ?", created.Source.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("resource-only edit removed state: count=%d error=%v", count, err)
	}
	configuration.Source.LocalConfig.IncludeExtensions = []string{"mp4"}
	updated, err = s.UpdateAutomation(created.Source.ID, configuration)
	if err != nil || updated.Source.Initialized || updated.Source.LocalStatusJSON != "" {
		t.Fatalf("filter edit retained baseline: source=%#v error=%v", updated.Source, err)
	}
	if err := s.db.First(&state, "source_id = ?", created.Source.ID).Error; err != nil || state.Configuration != "" {
		t.Fatalf("filter edit did not invalidate state identity: state=%+v error=%v", state, err)
	}
	if err := s.DeleteAutomation(created.Source.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&model.LocalDirectoryState{}).Where("source_id = ?", created.Source.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted monitor left state: count=%d error=%v", count, err)
	}
}

func TestLocalDirectoryScopeEditsInvalidateWithoutDeletingSnapshotOrQueue(t *testing.T) {
	for _, api := range []string{"automation", "source"} {
		t.Run(api, func(t *testing.T) {
			s := managementTestService(t)
			t.Cleanup(s.closeLocalMonitor)
			root := t.TempDir()
			for _, name := range []string{"old.mkv", "existing.mp4"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
				Name: "scope edit", TriggerType: model.AutomationTriggerLocalDirectory, DirectoryPath: root,
				LocalConfig: &model.LocalDirectoryConfig{Mode: "poll", IncludeExtensions: []string{"mkv"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := s.Scan(context.Background(), created.Source.ID)
			if err != nil || baseline.LocalMonitor == nil || !baseline.LocalMonitor.CycleCompleted {
				t.Fatalf("baseline: %+v %v", baseline, err)
			}
			var before model.LocalDirectoryState
			if err := s.db.First(&before, "source_id = ?", created.Source.ID).Error; err != nil {
				t.Fatal(err)
			}
			var beforeItems, beforeQueue int64
			if err := s.db.Model(&model.LocalDirectoryItem{}).Where("source_id = ?", created.Source.ID).Count(&beforeItems).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.db.Model(&model.LocalDirectoryScan{}).Where("source_id = ?", created.Source.ID).Count(&beforeQueue).Error; err != nil {
				t.Fatal(err)
			}
			if beforeItems == 0 || beforeQueue == 0 {
				t.Fatalf("baseline did not populate both durable tables: items=%d queue=%d", beforeItems, beforeQueue)
			}
			if err := s.db.Callback().Delete().Before("gorm:delete").Register("test:forbid_scope_delete", func(tx *gorm.DB) {
				if strings.HasPrefix(tx.Statement.Table, "local_directory_") {
					tx.AddError(errors.New("configuration changes must not delete the durable directory index"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			configuration, err := automationConfiguration(created.Source, created.Workflow)
			if err != nil {
				t.Fatal(err)
			}
			configuration.Source.LocalConfig.IncludeExtensions = []string{"mp4"}
			var updated model.AutomationSource
			if api == "automation" {
				result, updateErr := s.UpdateAutomation(created.Source.ID, configuration)
				updated, err = result.Source, updateErr
			} else {
				updated, err = s.UpdateSource(created.Source.ID, configuration.Source)
			}
			if err != nil || updated.Initialized {
				t.Fatalf("scope update: %+v %v", updated, err)
			}
			if err := s.db.Callback().Delete().Remove("test:forbid_scope_delete"); err != nil {
				t.Fatal(err)
			}
			var invalidated model.LocalDirectoryState
			if err := s.db.First(&invalidated, "source_id = ?", created.Source.ID).Error; err != nil {
				t.Fatal(err)
			}
			if invalidated.Configuration != "" || invalidated.Generation != before.Generation || invalidated.Epoch != before.Epoch {
				t.Fatalf("scope update replaced persistent scan progress: before=%+v after=%+v", before, invalidated)
			}
			var afterItems, afterQueue int64
			if err := s.db.Model(&model.LocalDirectoryItem{}).Where("source_id = ?", created.Source.ID).Count(&afterItems).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.db.Model(&model.LocalDirectoryScan{}).Where("source_id = ?", created.Source.ID).Count(&afterQueue).Error; err != nil {
				t.Fatal(err)
			}
			if beforeItems != afterItems || beforeQueue != afterQueue {
				t.Fatalf("scope update deleted rows: items %d/%d queue %d/%d", beforeItems, afterItems, beforeQueue, afterQueue)
			}
			finished := false
			for i := 0; i < 20; i++ {
				result, err := s.Scan(context.Background(), created.Source.ID)
				if err != nil || result.CreatedEntries != 0 {
					t.Fatalf("new baseline emitted existing files: %+v %v", result, err)
				}
				if result.LocalMonitor.CycleCompleted {
					finished = true
					break
				}
			}
			var rebuilt model.LocalDirectoryState
			if err := s.db.First(&rebuilt, "source_id = ?", created.Source.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !finished || !rebuilt.Initialized || rebuilt.Configuration == "" || rebuilt.Epoch == before.Epoch || rebuilt.Generation <= before.Generation {
				t.Fatalf("new scope did not rebuild baseline with a new epoch: %+v", rebuilt)
			}
		})
	}
}

func TestLocalDirectoryDisableCannotRaceScan(t *testing.T) {
	s := managementTestService(t)
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "本地", TriggerType: model.AutomationTriggerLocalDirectory, DirectoryPath: t.TempDir(), Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !s.beginAutomationScan(created.Source.ID) {
		t.Fatal("cannot reserve source")
	}
	if _, err := s.SetAutomationEnabled(created.Source.ID, false); !errors.Is(err, ErrAutomationScanRunning) {
		t.Fatalf("disable overlapped scan: %v", err)
	}
	if err := s.BatchAutomations(AutomationBatchInput{SourceIDs: []uint{created.Source.ID}, Action: "disable"}); !errors.Is(err, ErrAutomationScanRunning) {
		t.Fatalf("batch disable overlapped scan: %v", err)
	}
	s.endAutomationScan(created.Source.ID)
	if _, err := s.SetAutomationEnabled(created.Source.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestLocalDirectoryWorkflowRejectsOnlyInvalid115Input(t *testing.T) {
	for _, node := range []AutomationNode{
		{Type: AutomationNodeOrganizeStrm}, {Type: AutomationNodeMoviePilotRecognize},
		{Type: AutomationNodeFilmFusionRecognize, Config: map[string]any{"recognition_mode": "file"}},
	} {
		definition := AutomationDefinition{Nodes: []AutomationNode{{ID: "trigger", Type: AutomationNodeTrigger}, {ID: "delay", Type: AutomationNodeDelay}, {ID: "action", Type: node.Type, Config: node.Config}},
			Edges: []AutomationEdge{{Source: "trigger", SourcePort: "next", Target: "delay"}, {Source: "delay", SourcePort: "success", Target: "action"}}}
		if err := validateAutomationLocalSourceWorkflow(definition); err == nil {
			t.Fatalf("local trigger accepted by 115 file action %s", node.Type)
		}
		definition.Nodes[0].Type = AutomationNodeWait115
		if err := validateAutomationLocalSourceWorkflow(definition); err != nil {
			t.Fatalf("legitimate upstream 115 output rejected: %v", err)
		}
	}
	definition := DefaultLocalDirectoryAutomationDefinition()
	definition.Nodes[1] = AutomationNode{ID: "notify", Type: AutomationNodeFilmFusionRecognize, Config: map[string]any{"recognition_mode": "title", "input": "$item.name"}}
	if err := validateAutomationLocalSourceWorkflow(definition); err != nil {
		t.Fatalf("local title recognition rejected: %v", err)
	}
	if validation := ValidateAutomationDefinition(DefaultLocalDirectoryAutomationDefinition()); !validation.Valid {
		t.Fatalf("invalid local default workflow: %v", validation.Errors)
	}
}
