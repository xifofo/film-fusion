package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/gorm"
)

type pfRepairFixture struct {
	svc          *PlutoFishService
	db           *gorm.DB
	cfg          plutoFishSavedConfig
	mapping      model.MediaDirectory
	event        plutofish.Event
	row          model.PlutoFishEvent
	old, desired model.PlutoFishBusinessPlan
}

func newPFRepairFixture(t *testing.T) *pfRepairFixture {
	t.Helper()
	b, mapping, event := plutoFishBusinessFixture(t)
	pool, _ := b.db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	if err := b.db.AutoMigrate(&model.SystemConfig{}, &model.PlutoFishEvent{}, &model.PlutoFishReceipt{}, &model.PlutoFishCursor{}, &model.PlutoFishSTRMRepair{}); err != nil {
		t.Fatal(err)
	}
	svc := NewPlutoFishService(b.db)
	cfg := PlutoFishConfig{BaseURL: "https://plutofish.example", OwnerUID: "owner", Mode: "sse", PollSeconds: 15, APIKey: "offline-only-key", Mappings: b.options.Mappings}
	if err := svc.Save(context.Background(), cfg, 7); err != nil {
		t.Fatal(err)
	}
	f := &pfRepairFixture{svc: svc, db: b.db, cfg: plutoFishSavedConfig{Config: cfg, UserID: 7}, mapping: mapping, event: event}
	f.addLegacy(t, event, true)
	return f
}

func (f *pfRepairFixture) addLegacy(t *testing.T, event plutofish.Event, receipt bool) {
	t.Helper()
	ctx := context.Background()
	scope := plutoFishScope(f.cfg.Config.BaseURL, f.cfg.Config.OwnerUID)
	b := NewPlutoFishBusiness(f.db, PlutoFishBusinessOptions{UserID: 7, Mappings: f.cfg.Config.Mappings})
	desired, err := b.plan(ctx, scope, event)
	if err != nil {
		t.Fatal(err)
	}
	old := model.PlutoFishBusinessPlan{Scope: scope, TransferID: event.TransferID, RootPath: f.mapping.SavePath, RelativePath: plutoFishLegacyPath(scope, event.TransferID, strings.TrimSuffix(event.FileName, filepath.Ext(event.FileName))+".strm"), Content: "/old/wrong-playback.mkv"}
	if err := f.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := plutoFishWriteOnce(ctx, old.RootPath, old.RelativePath, old.Content); err != nil {
		t.Fatal(err)
	}
	manifest, content := plutoFishRepairManifest(model.PlutoFishSTRMRepair{OldRelative: old.RelativePath, OldContent: old.Content})
	if err := plutoFishWriteOnce(ctx, old.RootPath, manifest, content); err != nil {
		t.Fatal(err)
	}
	store := NewPlutoFishStore(f.db, f.cfg.Config.BaseURL, f.cfg.Config.OwnerUID)
	if err := store.Commit(ctx, []plutofish.Event{event}, "preserve-opaque-cursor"); err != nil {
		t.Fatal(err)
	}
	var row model.PlutoFishEvent
	if err := f.db.Where("scope = ? AND event_id = ?", scope, event.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	row.State, row.OutputPath = "completed", filepath.Join(old.RootPath, old.RelativePath)
	if !receipt {
		row.State, row.OutputPath = "review", ""
	}
	if err := f.db.Save(&row).Error; err != nil {
		t.Fatal(err)
	}
	if receipt {
		if err := f.db.Create(&model.PlutoFishReceipt{Scope: scope, TransferID: event.TransferID, EventID: event.ID, OutputPath: row.OutputPath}).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.row, f.old, f.desired = row, old, desired
}

func (f *pfRepairFixture) preview(t *testing.T) PlutoFishRepairItem {
	t.Helper()
	p, err := f.svc.PreviewSTRMRepairs(context.Background(), 7, 0, 100)
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("preview=%+v err=%v", p, err)
	}
	return p.Items[0]
}

func (f *pfRepairFixture) apply(t *testing.T, item PlutoFishRepairItem) PlutoFishRepairItem {
	t.Helper()
	items, err := f.svc.RepairSTRMs(context.Background(), 7, []PlutoFishRepairSelection{{EventID: item.EventID, Snapshot: item.Snapshot}})
	if err != nil || len(items) != 1 {
		t.Fatalf("apply=%+v err=%v", items, err)
	}
	return items[0]
}

func pfAssertFile(t *testing.T, path, content string) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != content {
		t.Fatalf("%s content=%q err=%v", path, actual, err)
	}
}

func (f *pfRepairFixture) assertDone(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.desired.RootPath, f.desired.RelativePath)
	pfAssertFile(t, path, f.desired.Content)
	if _, err := os.Stat(filepath.Join(f.old.RootPath, f.old.RelativePath)); !os.IsNotExist(err) {
		t.Fatalf("old file remains: %v", err)
	}
	var plan model.PlutoFishBusinessPlan
	if err := f.db.Where("scope = ? AND transfer_id = ?", f.old.Scope, f.old.TransferID).Take(&plan).Error; err != nil || !repairPlansEqual(plan, f.desired) {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	var receipt model.PlutoFishReceipt
	if err := f.db.Where("scope = ? AND transfer_id = ?", f.old.Scope, f.old.TransferID).Take(&receipt).Error; err != nil || receipt.OutputPath != path {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	var rows []model.PlutoFishEvent
	f.db.Where("scope = ? AND transfer_id = ?", f.old.Scope, f.old.TransferID).Find(&rows)
	for _, row := range rows {
		if row.OutputPath != path || row.State != "completed" && row.State != "duplicate" {
			t.Fatalf("event=%+v", row)
		}
	}
	var journal model.PlutoFishSTRMRepair
	f.db.Where("scope = ? AND transfer_id = ?", f.old.Scope, f.old.TransferID).Take(&journal)
	if journal.State != "completed" {
		t.Fatalf("journal=%+v", journal)
	}
	cursor, err := NewPlutoFishStore(f.db, f.cfg.Config.BaseURL, f.cfg.Config.OwnerUID).Cursor(context.Background())
	if err != nil || cursor != "preserve-opaque-cursor" {
		t.Fatalf("cursor=%q err=%v", cursor, err)
	}
}

type pfNoNetwork struct{ calls atomic.Int64 }

func (n *pfNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.calls.Add(1)
	return nil, errors.New("network forbidden")
}

func TestPlutoFishRepairPreviewApplyAndIdempotencyOffline(t *testing.T) {
	transport := &pfNoNetwork{}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	f := newPFRepairFixture(t)
	duplicate := f.event
	duplicate.ID = "owner:duplicate"
	if err := NewPlutoFishStore(f.db, f.cfg.Config.BaseURL, f.cfg.Config.OwnerUID).Commit(context.Background(), []plutofish.Event{duplicate}, "preserve-opaque-cursor"); err != nil {
		t.Fatal(err)
	}
	var beforePlan model.PlutoFishBusinessPlan
	f.db.First(&beforePlan)
	item := f.preview(t)
	if !item.CanRepair || item.State != "ready" || item.NewPath != filepath.Join(f.mapping.SavePath, "Movies/Title/影片 #1.strm") {
		t.Fatalf("item=%+v", item)
	}
	if item.Content == item.OldContent {
		t.Fatal("playback content not recalculated")
	}
	var count int64
	f.db.Model(&model.PlutoFishSTRMRepair{}).Count(&count)
	if count != 0 {
		t.Fatal("preview wrote journal")
	}
	if _, err := os.Stat(item.NewPath); !os.IsNotExist(err) {
		t.Fatal("preview wrote file")
	}
	pfAssertFile(t, item.OldPath, item.OldContent)
	var afterPlan model.PlutoFishBusinessPlan
	f.db.First(&afterPlan)
	if beforePlan != afterPlan {
		t.Fatal("preview changed plan")
	}
	if result := f.apply(t, item); result.State != "repaired" {
		t.Fatalf("result=%+v", result)
	}
	f.assertDone(t)
	if _, err := os.Stat(filepath.Join(f.mapping.SavePath, "PlutoFish")); !os.IsNotExist(err) {
		t.Fatal("empty hash tree remains")
	}
	if result := f.apply(t, item); result.State != "repaired" {
		t.Fatalf("repeat result=%+v", result)
	}
	if p := f.preview(t); p.CanRepair || p.State != "repaired" {
		t.Fatalf("preview=%+v", p)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("repair attempted network")
	}
}

func TestPlutoFishRepairConflictAndStalePreviewPreserveOld(t *testing.T) {
	for _, kind := range []string{"target", "old", "manifest", "symlink", "mapping", "receipt", "other-user", "missing-plan"} {
		t.Run(kind, func(t *testing.T) {
			f := newPFRepairFixture(t)
			item := f.preview(t)
			switch kind {
			case "target":
				if err := plutoFishWriteOnce(context.Background(), f.desired.RootPath, f.desired.RelativePath, "foreign content"); err != nil {
					t.Fatal(err)
				}
			case "old":
				if err := os.WriteFile(item.OldPath, []byte("foreign content"), 0644); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				if err := os.WriteFile(filepath.Join(filepath.Dir(item.OldPath), ".transfer.json"), []byte("foreign manifest"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				external := t.TempDir()
				if err := os.Symlink(external, filepath.Join(f.desired.RootPath, "Movies")); err != nil {
					t.Fatal(err)
				}
			case "mapping":
				if err := f.db.Model(&f.mapping).Update("content_prefix", "/new/playback").Error; err != nil {
					t.Fatal(err)
				}
			case "receipt":
				if err := f.db.Model(&model.PlutoFishReceipt{}).Where("scope = ?", f.old.Scope).Update("output_path", "/other.strm").Error; err != nil {
					t.Fatal(err)
				}
			case "other-user":
				if _, err := f.svc.PreviewSTRMRepairs(context.Background(), 8, 0, 100); err == nil {
					t.Fatal("cross-user preview")
				}
				if _, err := f.svc.RepairSTRMs(context.Background(), 8, []PlutoFishRepairSelection{{EventID: item.EventID, Snapshot: item.Snapshot}}); err == nil {
					t.Fatal("cross-user apply")
				}
				return
			case "missing-plan":
				if err := f.db.Where("scope = ?", f.old.Scope).Delete(&model.PlutoFishBusinessPlan{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if result := f.apply(t, item); result.State != "blocked" {
				t.Fatalf("result=%+v", result)
			}
			var count int64
			f.db.Model(&model.PlutoFishSTRMRepair{}).Count(&count)
			if count != 0 {
				t.Fatal("blocked repair wrote journal")
			}
			want := item.OldContent
			if kind == "old" {
				want = "foreign content"
			}
			pfAssertFile(t, item.OldPath, want)
		})
	}
}

func TestPlutoFishRepairDatabaseFailureResumesAfterRestart(t *testing.T) {
	f := newPFRepairFixture(t)
	item := f.preview(t)
	if err := f.db.Exec("CREATE TRIGGER reject_pf_repair BEFORE UPDATE ON pluto_fish_business_plans BEGIN SELECT RAISE(ABORT, 'injected'); END;").Error; err != nil {
		t.Fatal(err)
	}
	result := f.apply(t, item)
	if result.State != "failed" || !strings.Contains(result.Reason, "数据库同步未完成") {
		t.Fatalf("result=%+v", result)
	}
	pfAssertFile(t, item.OldPath, item.OldContent)
	pfAssertFile(t, item.NewPath, item.Content)
	var plan model.PlutoFishBusinessPlan
	f.db.First(&plan)
	if !repairPlansEqual(plan, f.old) {
		t.Fatal("failed transaction changed plan")
	}
	if err := f.db.Exec("DROP TRIGGER reject_pf_repair").Error; err != nil {
		t.Fatal(err)
	}
	f.svc = NewPlutoFishService(f.db)
	resume := f.preview(t)
	if resume.State != "resume" || resume.Snapshot != item.Snapshot {
		t.Fatalf("resume=%+v", resume)
	}
	if result = f.apply(t, resume); result.State != "repaired" {
		t.Fatalf("result=%+v", result)
	}
	f.assertDone(t)
}

func TestPlutoFishRepairResumesCleanupAndPreservesUnrelatedFiles(t *testing.T) {
	f := newPFRepairFixture(t)
	_, repair := f.svc.repairCandidate(context.Background(), f.cfg, f.row)
	if err := f.db.Create(&repair).Error; err != nil {
		t.Fatal(err)
	}
	if err := plutoFishWriteOnce(context.Background(), repair.NewRoot, repair.NewRelative, repair.NewContent); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.commitSTRMRepair(context.Background(), f.cfg, f.row, repair); err != nil {
		t.Fatal(err)
	}
	// Simulate a process stopping after the DB commit and a partial cleanup.
	if err := os.Remove(filepath.Join(repair.OldRoot, repair.OldRelative)); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(repair.OldRoot, filepath.Dir(repair.OldRelative), "keep.nfo")
	if err := os.WriteFile(keep, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	f.svc = NewPlutoFishService(f.db)
	resume := f.preview(t)
	if resume.State != "resume" {
		t.Fatalf("resume=%+v", resume)
	}
	if result := f.apply(t, resume); result.State != "repaired" {
		t.Fatalf("result=%+v", result)
	}
	f.assertDone(t)
	pfAssertFile(t, keep, "keep")
	if _, err := os.Stat(filepath.Join(filepath.Dir(keep), ".transfer.json")); !os.IsNotExist(err) {
		t.Fatal("manifest remains")
	}
}

func TestPlutoFishRepairBatchPaginationAndPartialFailures(t *testing.T) {
	f := newPFRepairFixture(t)
	first := f.preview(t)
	event := f.event
	event.ID = "owner:2"
	event.TransferID = "transfer2"
	event.FileName = "episode2.mkv"
	f.addLegacy(t, event, false)
	preview, err := f.svc.PreviewSTRMRepairs(context.Background(), 7, 0, 1)
	if err != nil || len(preview.Items) != 1 || !preview.HasMore || preview.NextAfter != first.EventID {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	next, err := f.svc.PreviewSTRMRepairs(context.Background(), 7, preview.NextAfter, 1)
	if err != nil || len(next.Items) != 1 || next.HasMore || !next.Items[0].CanRepair {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	// A collision for one item must not prevent the rest of the batch from being repaired.
	if err := os.WriteFile(first.OldPath, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	results, err := f.svc.RepairSTRMs(context.Background(), 7, []PlutoFishRepairSelection{{first.EventID, first.Snapshot}, {next.Items[0].EventID, next.Items[0].Snapshot}})
	if err != nil || results[0].State != "blocked" || results[1].State != "repaired" {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	f.assertDone(t)
	pfAssertFile(t, first.OldPath, "changed")
}

func TestPlutoFishRepairUsesCompletedEventAndRejectsUnknownPaths(t *testing.T) {
	f := newPFRepairFixture(t)
	// Oldest event could have insufficient metadata; the completed receipt event has the actual path.
	duplicate := f.row
	duplicate.ID = 0
	duplicate.EventID = "owner:completed"
	var event plutofish.Event
	json.Unmarshal([]byte(duplicate.Payload), &event)
	event.ID = duplicate.EventID
	data, _ := json.Marshal(event)
	duplicate.Payload = string(data)
	if err := f.db.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&f.row).Updates(map[string]any{"state": "review", "payload": "{}", "output_path": ""}).Error; err != nil {
		t.Fatal(err)
	}
	item := f.preview(t)
	if item.EventID != duplicate.ID || !item.CanRepair {
		t.Fatalf("item=%+v", item)
	}
	if err := f.db.Model(&model.PlutoFishBusinessPlan{}).Where("scope = ?", f.old.Scope).Update("relative_path", "unrelated.strm").Error; err != nil {
		t.Fatal(err)
	}
	if item = f.preview(t); item.CanRepair || !strings.Contains(item.Reason, "不是该任务") {
		t.Fatalf("item=%+v", item)
	}
}
