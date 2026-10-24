package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
)

// 用用户提供的片名和正确播放内容验证云路径约定：本地保留挂载/作品/季目录，
// 同一季共用目录，已有相同 STRM 不被重写，目录中不残留任务清单或临时文件。
func TestPlutoFishPathsFollowExistingCloudDriveMapping(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			b, mapping, event := plutoFishBusinessFixture(t)
			mapping.SourceType = model.SourceTypeCloudDrive2
			mapping.SourcePath = "/115LongTerm/影视中心"
			mapping.ContentPrefix = "/CloudNAS/CloudDrive"
			mapping.ContentEncodeURI = false
			if err := b.db.Save(&mapping).Error; err != nil {
				t.Fatal(err)
			}

			event.TargetDirectoryPath = "/影视中心/国产剧集/尚公主 (2026) {tmdb-286171}/Season 01"
			event.TargetPickCode = "not-a-path"
			localDir := filepath.Join(mapping.LocalPath, "115LongTerm/影视中心/国产剧集/尚公主 (2026) {tmdb-286171}/Season 01")
			for _, episode := range []string{"03", "04"} {
				name := "Tigers.Sniff.the.Rose.S01E" + episode + ".2026.2160p.TX.WEB-DL.DDP5.1.Atmos.HFR.H.265-SalmonWeb"
				event.ID, event.TransferID, event.FileName = "event-"+episode, "transfer-"+episode, name+".mkv"
				wantPath := filepath.Join(localDir, name+".strm")
				wantContent := "/CloudNAS/CloudDrive/115LongTerm/影视中心/国产剧集/尚公主 (2026) {tmdb-286171}/Season 01/" + name + ".mkv"
				var before os.FileInfo
				if existing {
					if err := os.MkdirAll(localDir, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(wantPath, []byte(wantContent), 0644); err != nil {
						t.Fatal(err)
					}
					var err error
					before, err = os.Stat(wantPath)
					if err != nil {
						t.Fatal(err)
					}
				}

				result, err := b.Handle(context.Background(), "service|owner", event)
				if err != nil || result.LocalPath != wantPath {
					t.Fatalf("result=%+v want=%q err=%v", result, wantPath, err)
				}
				if data, err := os.ReadFile(wantPath); err != nil || string(data) != wantContent {
					t.Fatalf("content=%q want=%q err=%v", data, wantContent, err)
				}
				if existing {
					after, err := os.Stat(wantPath)
					if err != nil || !os.SameFile(before, after) {
						t.Fatalf("replaced existing STRM: %v", err)
					}
				}
			}

			files := 0
			if err := filepath.WalkDir(mapping.LocalPath, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					files++
					if filepath.Dir(path) != localDir || filepath.Ext(path) != ".strm" {
						t.Errorf("unexpected media directory artifact: %s", path)
					}
				}
				return nil
			}); err != nil || files != 2 {
				t.Fatalf("files=%d err=%v", files, err)
			}
		})
	}
}

// 写文件成功、成功回执落库失败后，重启消费仍使用原路径；不依赖旁路清单文件。
func TestPlutoFishMappedSTRMRecoversWithoutManifest(t *testing.T) {
	b, mapping, event := plutoFishBusinessFixture(t)
	if err := b.db.AutoMigrate(&model.PlutoFishEvent{}, &model.PlutoFishCursor{}, &model.PlutoFishReceipt{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := NewPlutoFishStore(b.db, "https://plutofish.example", "owner")
	if err := store.Commit(ctx, []plutofish.Event{event}, "cursor"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec("CREATE TRIGGER fail_receipt BEFORE INSERT ON pluto_fish_receipts BEGIN SELECT RAISE(ABORT, 'test'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.processOne(ctx, b.Handle); err == nil {
		t.Fatal("expected receipt failure after writing STRM")
	}
	wantPath := filepath.Join(mapping.LocalPath, "Movies/Title/影片 #1.strm")
	before, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec("DROP TRIGGER fail_receipt").Error; err != nil {
		t.Fatal(err)
	}
	restarted := NewPlutoFishBusiness(b.db, b.options)
	if ok, err := store.processOne(ctx, restarted.Handle); !ok || err != nil {
		t.Fatalf("recovery: processed=%t err=%v", ok, err)
	}
	var receipt model.PlutoFishReceipt
	if err := b.db.First(&receipt).Error; err != nil || receipt.OutputPath != wantPath {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	after, err := os.Stat(wantPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("recovery replaced STRM: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Dir(wantPath)); err != nil || len(entries) != 1 || entries[0].Name() != "影片 #1.strm" {
		t.Fatalf("unexpected recovery outputs: %v %v", entries, err)
	}
}

// 旧计划可能已经写过文件；升级不能忽略计划直接生成另一份、覆盖历史或假报完成。
func TestPlutoFishLegacyPlanRemainsForReview(t *testing.T) {
	b, mapping, event := plutoFishBusinessFixture(t)
	scope := "service|owner"
	legacy := model.PlutoFishBusinessPlan{
		Scope: scope, TransferID: event.TransferID, RootPath: mapping.LocalPath,
		RelativePath: fmt.Sprintf("PlutoFish/%x/%x/影片 #1.strm", sha256.Sum256([]byte(scope)), sha256.Sum256([]byte(event.TransferID))),
		Content:      "https://existing-player.example/play/Movies/Title/%E5%BD%B1%E7%89%87%20%231.mkv",
	}
	if err := b.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(context.Background(), scope, event); err == nil || !strings.Contains(err.Error(), "历史文件") {
		t.Fatalf("legacy plan was not kept for review: %v", err)
	}
	if entries, err := os.ReadDir(mapping.LocalPath); err != nil || len(entries) != 0 {
		t.Fatalf("unexpected second output: %v %v", entries, err)
	}
	var saved model.PlutoFishBusinessPlan
	if err := b.db.First(&saved).Error; err != nil || saved.RelativePath != legacy.RelativePath || saved.Content != legacy.Content {
		t.Fatalf("legacy plan changed: %+v err=%v", saved, err)
	}
}
