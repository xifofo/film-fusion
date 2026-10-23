package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func plutoFishBusinessFixture(t *testing.T) (*PlutoFishBusiness, model.CloudPath, plutofish.Event) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}, &model.CloudPath{}, &model.PlutoFishBusinessPlan{}); err != nil {
		t.Fatal(err)
	}
	// No provider credentials: notification handling must never need them.
	if err := db.Create(&model.CloudStorage{ID: 1, UserID: 7, StorageType: model.StorageType115Open, Status: model.StatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	mapping := model.CloudPath{ID: 2, UserID: 7, CloudStorageID: 1, SourcePath: "/Movies", SourceType: model.SourceTypeMoviePilot2, ContentPrefix: "https://existing-player.example/play", ContentEncodeURI: true, LocalPath: t.TempDir(), LinkType: model.LinkTypeStrm, FilterRules: `{"include":["mkv"],"download":["srt"]}`}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	b := NewPlutoFishBusiness(db, PlutoFishBusinessOptions{UserID: 7, Mappings: []PlutoFishAccountMapping{{TargetAccountID: "account", CloudPathID: 2}}})
	event := plutofish.Event{ID: "owner:1", Type: "transfer.succeeded", Status: "succeeded", SchemaVersion: 1, TransferID: "transfer", Provider: "115", TargetAccountID: "account", TargetDirectoryPath: "/Movies/Title", FileName: "影片 #1.mkv"}
	return b, mapping, event
}

func TestPlutoFishBusinessCreatesRealMappedSTRMAndReplaysAfterRestart(t *testing.T) {
	for _, cd2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "MP2", true: "CD2"}[cd2], func(t *testing.T) {
			b, mapping, event := plutoFishBusinessFixture(t)
			mount := ""
			if cd2 {
				mapping.SourceType, mapping.SourcePath = model.SourceTypeCloudDrive2, "/115挂载/Movies"
				mount = "/115%E6%8C%82%E8%BD%BD"
				if err := b.db.Save(&mapping).Error; err != nil {
					t.Fatal(err)
				}
			}
			first, err := b.Handle(context.Background(), "https://plutofish.example|owner", event)
			if err != nil {
				t.Fatal(err)
			}
			want := "https://existing-player.example/play" + mount + "/Movies/Title/%E5%BD%B1%E7%89%87%20%231.mkv"
			data, err := os.ReadFile(first.LocalPath)
			if err != nil || string(data) != want {
				t.Fatalf("content=%q want=%q err=%v", data, want, err)
			}
			if filepath.Base(first.LocalPath) != "影片 #1.strm" || !strings.HasPrefix(first.LocalPath, filepath.Join(mapping.LocalPath, "PlutoFish")+string(filepath.Separator)) {
				t.Fatalf("unexpected path %q", first.LocalPath)
			}
			// An independent adapter instance models process restart before inbox commit.
			second, err := NewPlutoFishBusiness(b.db, b.options).Handle(context.Background(), "https://plutofish.example|owner", event)
			if err != nil || first != second {
				t.Fatalf("replay=%+v err=%v", second, err)
			}
			if entries, err := os.ReadDir(filepath.Dir(first.LocalPath)); err != nil || len(entries) != 2 {
				t.Fatalf("unexpected outputs %v %v", entries, err)
			}
		})
	}
}

func TestPlutoFishBusinessMissingMetadataAndFailedStatusesStayPending(t *testing.T) {
	for _, mode := range []string{"missing directory", "missing name", "failed", "unknown", "new schema", "unknown type", "no transfer", "unmapped", "other provider", "outside", "traversal directory", "traversal filename", "control", "download", "excluded"} {
		t.Run(mode, func(t *testing.T) {
			b, mapping, event := plutoFishBusinessFixture(t)
			switch mode {
			case "missing directory":
				event.TargetDirectoryPath, event.TargetPickCode, event.TargetFileID = "", "still-not-a-path", "123"
			case "missing name":
				event.FileName = ""
			case "failed":
				event.Type, event.Status = "transfer.failed", "failed"
			case "unknown":
				event.Type, event.Status = "transfer.unknown", "unknown"
			case "new schema":
				event.SchemaVersion = 2
			case "unknown type":
				event.Type = "transfer.new"
			case "no transfer":
				event.TransferID = ""
			case "unmapped":
				event.TargetAccountID = "other"
			case "other provider":
				event.Provider = "other"
			case "outside":
				event.TargetDirectoryPath = "/Movies2"
			case "traversal directory":
				event.TargetDirectoryPath = "/Movies/../outside"
			case "traversal filename":
				event.FileName = "../outside.mkv"
			case "control":
				event.FileName = "bad\n.mkv"
			case "download":
				event.FileName = "subtitle.srt"
			case "excluded":
				event.FileName = "movie.mp4"
			}
			if result, err := b.Handle(context.Background(), "scope", event); err == nil {
				t.Fatalf("expected pending, got %+v", result)
			}
			if entries, err := os.ReadDir(mapping.LocalPath); err != nil || len(entries) != 0 {
				t.Fatalf("unexpected local effect %v %v", entries, err)
			}
			var plans int64
			if err := b.db.Model(&model.PlutoFishBusinessPlan{}).Count(&plans).Error; err != nil || plans != 0 {
				t.Fatalf("invalid event claimed a business plan: count=%d err=%v", plans, err)
			}
		})
	}
}

func TestPlutoFishBusinessPlanPinsRootAcrossCrashAndMappingChange(t *testing.T) {
	b, mapping, event := plutoFishBusinessFixture(t)
	first, err := b.Handle(context.Background(), "service|owner", event)
	if err != nil {
		t.Fatal(err)
	}
	// Model a crash after file publication but before the inbox's success
	// receipt, followed by an operator changing the CloudPath local directory.
	originalRoot := mapping.LocalPath
	mapping.LocalPath = t.TempDir()
	if err := b.db.Save(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	restarted := NewPlutoFishBusiness(b.db, b.options)
	if _, err := restarted.Handle(context.Background(), "service|owner", event); err == nil || !strings.Contains(err.Error(), "原计划") {
		t.Fatalf("changed root did not remain for review: %v", err)
	}
	if entries, err := os.ReadDir(mapping.LocalPath); err != nil || len(entries) != 0 {
		t.Fatalf("same transfer published into a second root: %v %v", entries, err)
	}
	var plan model.PlutoFishBusinessPlan
	if err := b.db.Where("scope = ? AND transfer_id = ?", "service|owner", event.TransferID).Take(&plan).Error; err != nil || plan.RootPath != originalRoot {
		t.Fatalf("original plan was changed: %+v %v", plan, err)
	}
	mapping.LocalPath = originalRoot
	if err := b.db.Save(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	if replay, err := restarted.Handle(context.Background(), "service|owner", event); err != nil || replay.LocalPath != first.LocalPath {
		t.Fatalf("same plan cannot resume: %+v %v", replay, err)
	}
}

func TestPlutoFishBusinessOwnerAndMappingValidation(t *testing.T) {
	for _, mode := range []string{"mapping owner", "storage owner", "disabled", "missing mapping", "duplicate mapping", "invalid filter", "no prefix", "relative prefix", "secret URL", "relative root", "no encoding"} {
		t.Run(mode, func(t *testing.T) {
			b, mapping, event := plutoFishBusinessFixture(t)
			switch mode {
			case "mapping owner":
				mapping.UserID = 8
			case "storage owner":
				if err := b.db.Model(&model.CloudStorage{}).Where("id = 1").Update("user_id", 8).Error; err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if err := b.db.Model(&model.CloudStorage{}).Where("id = 1").Update("status", model.StatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			case "missing mapping":
				b.options.Mappings[0].CloudPathID = 999
			case "duplicate mapping":
				b.options.Mappings = append(b.options.Mappings, b.options.Mappings[0])
			case "invalid filter":
				mapping.FilterRules = "{"
			case "no prefix":
				mapping.ContentPrefix = ""
			case "relative prefix":
				mapping.ContentPrefix = "relative"
			case "secret URL":
				mapping.ContentPrefix = "https://existing-player.example/?api_key=secret"
			case "relative root":
				mapping.LocalPath = "relative"
			case "no encoding":
				mapping.ContentEncodeURI = false
			}
			if err := b.db.Save(&mapping).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := b.Handle(context.Background(), "scope", event); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected sanitized pending error: %v", err)
			}
		})
	}
}

func TestPlutoFishBusinessIdempotencyNamespaceAndConflicts(t *testing.T) {
	b, _, event := plutoFishBusinessFixture(t)
	first, err := b.Handle(context.Background(), "service|owner-1", event)
	if err != nil {
		t.Fatal(err)
	}
	other, err := b.Handle(context.Background(), "service|owner-2", event)
	if err != nil || other.LocalPath == first.LocalPath {
		t.Fatalf("scope isolation lost: %+v %v", other, err)
	}
	changed := event
	changed.FileName = "changed.mkv"
	if result, err := b.Handle(context.Background(), "service|owner-1", changed); err == nil {
		t.Fatalf("renamed replay created a duplicate: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(first.LocalPath), "changed.strm")); !os.IsNotExist(err) {
		t.Fatalf("duplicate path exists: %v", err)
	}
	if err := os.WriteFile(first.LocalPath, []byte("unrelated content"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(context.Background(), "service|owner-1", event); err == nil {
		t.Fatal("conflicting STRM accepted")
	}
	if data, err := os.ReadFile(first.LocalPath); err != nil || string(data) != "unrelated content" {
		t.Fatalf("overwrote unrelated content: %q %v", data, err)
	}
}

func TestPlutoFishBusinessFailedThenSucceededAndCancellation(t *testing.T) {
	b, mapping, event := plutoFishBusinessFixture(t)
	failed := event
	failed.Type, failed.Status = "transfer.failed", "failed"
	if _, err := b.Handle(context.Background(), "scope", failed); err == nil {
		t.Fatal("failure handled as success")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Handle(cancelled, "scope", event); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
	if entries, err := os.ReadDir(mapping.LocalPath); err != nil || len(entries) != 0 {
		t.Fatalf("cancelled operation wrote files: %v %v", entries, err)
	}
	if _, err := b.Handle(context.Background(), "scope", event); err != nil {
		t.Fatalf("prior failure blocked later success: %v", err)
	}
}

func TestPlutoFishSTRMAtomicPublishAndSymlinkConfinement(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same", false: "different"}[same], func(t *testing.T) {
			root := t.TempDir()
			contents := []string{"one", "two"}
			if same {
				contents[1] = contents[0]
			}
			start, done := make(chan struct{}), make(chan error, 2)
			for _, content := range contents {
				go func() { <-start; done <- plutoFishWriteOnce(context.Background(), root, "a/b/movie.strm", content) }()
			}
			close(start)
			conflicts := 0
			for range 2 {
				if err := <-done; err != nil {
					if !errors.Is(err, errPlutoFishSTRMConflict) {
						t.Fatal(err)
					}
					conflicts++
				}
			}
			if (same && conflicts != 0) || (!same && conflicts != 1) {
				t.Fatalf("conflicts=%d same=%v", conflicts, same)
			}
			if entries, err := os.ReadDir(filepath.Join(root, "a/b")); err != nil || len(entries) != 1 {
				t.Fatalf("temporary files leaked: %v %v", entries, err)
			}
		})
	}
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := plutoFishWriteOnce(context.Background(), root, "escape/movie.strm", "content"); err == nil {
		t.Fatal("symlink escape permitted")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("escaped root %v %v", entries, err)
	}
}
