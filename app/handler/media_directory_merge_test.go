package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func mergeDirectoryFixture(t *testing.T) (*gorm.DB, *gin.Engine, model.MediaDirectory, model.MediaDirectory) {
	t.Helper()
	db, router := mediaDirectoryFixture(t)
	h := NewMediaDirectoryHandler()
	router.POST("/media-directories/:id/merge-preview", h.MergePreview)
	router.POST("/media-directories/:id/merge", h.Merge)
	if err := db.AutoMigrate(&model.OrganizeLog{}, &model.OrganizePlan{}, &model.OrganizeUpgradeJob{}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := model.MediaDirectory{ID: 8, UserID: 1, CloudStorageID: 1, DirectoryID: "3409190130230626116", DirectoryName: "影视中心", OrganizeEnabled: true, SavePath: filepath.Join(root, "115LongTerm"), ContentPrefix: "/CloudNAS/CloudDrive/115LongTerm", IncludeExtensions: `["mkv","mp4"]`, ExcludeExtensions: `["avi"]`, ExcludeSmallerThanMB: 1, ClassifyByCategory: true}
	mapping := model.MediaDirectory{ID: 9, UserID: 1, CloudStorageID: 1, MappingEnabled: true, DirectoryName: "/115LongTerm/影视中心", SourcePath: "/115LongTerm/影视中心", SourceType: model.SourceTypeCloudDrive2, SavePath: root, ContentPrefix: "/CloudNAS/CloudDrive", EmbyPathPrefix: "/MediaSTRM_115LongTerm", FilterRules: `{"include":[".mkv",".mp4"],"download":["ass","srt"]}`, LinkType: model.LinkTypeStrm, StrmContentType: model.StrmContentTypePath}
	for _, d := range []*model.MediaDirectory{&target, &mapping} {
		if err := db.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, router, target, mapping
}

func mergePreviewRequest(t *testing.T, router *gin.Engine, target, mapping uint) service.MediaDirectoryMergePreview {
	t.Helper()
	w := directoryRequest(router, "POST", fmt.Sprintf("/media-directories/%d/merge-preview", target), fmt.Sprintf(`{"mapping_id":%d}`, mapping))
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	var response struct {
		Data service.MediaDirectoryMergePreview `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

func TestMediaDirectoryMergePreservesPathsFiltersReferencesAndSourceSnapshot(t *testing.T) {
	db, router, target, mapping := mergeDirectoryFixture(t)
	setting := model.EmbyVersionCheckSetting{UserID: 1, CloudPathIDs: []uint{mapping.ID, target.ID}}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	config := model.SystemConfig{ConfigKey: "plutofish.settings", ConfigValue: `{"userID":1,"unknown":9007199254740993,"config":{"apiKey":"keep-private","mappings":[{"targetAccountID":"account","cloudPathID":9,"future":"keep"}]}}`}
	if err := db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	log := model.OrganizeLog{CloudPathID: mapping.ID, CloudStorageID: 1, Source: "source", Target: "unchanged"}
	if err := db.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.OrganizePlan{ID: "completed-plan", UserID: 1, CloudDirectoryID: target.ID, Status: "completed", DirectoryJSON: `{"immutable":"snapshot"}`}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(target.SavePath, "影视中心", "影片.strm")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("existing content"), 0600); err != nil {
		t.Fatal(err)
	}
	p := mergePreviewRequest(t, router, target.ID, mapping.ID)
	if p.References["emby_version_check"] != 1 || p.References["plutofish"] != 1 || p.References["organize_logs"] != 1 {
		t.Fatalf("references=%v", p.References)
	}
	var before model.MediaDirectory
	if err := db.First(&before, mapping.ID).Error; err != nil || !before.MappingEnabled {
		t.Fatal("preview mutated source")
	}
	data, _ := json.Marshal(p)
	if strings.Contains(string(data), "keep-private") {
		t.Fatal("preview leaked service credentials")
	}
	w := directoryRequest(router, "POST", "/media-directories/8/merge", fmt.Sprintf(`{"mapping_id":9,"revision":%q}`, p.Revision))
	if w.Code != 200 {
		t.Fatalf("merge: %d %s", w.Code, w.Body)
	}
	var merged model.MediaDirectory
	if err := db.First(&merged, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !merged.MappingEnabled || !merged.OrganizeEnabled || merged.SavePath != target.SavePath || merged.ContentPrefix != target.ContentPrefix || merged.DirectoryName != target.DirectoryName || merged.DirectoryID != target.DirectoryID || merged.IncludeExtensions != target.IncludeExtensions || merged.ExcludeExtensions != target.ExcludeExtensions || merged.ExcludeSmallerThanMB != target.ExcludeSmallerThanMB || merged.FilterRules != mapping.FilterRules || merged.EmbyPathPrefix != mapping.EmbyPathPrefix {
		t.Fatalf("merge lost original settings: %+v", merged)
	}
	runtime, err := merged.MappingRuntimeConfig()
	if err != nil || runtime.SavePath != mapping.SavePath || runtime.ContentPrefix != mapping.ContentPrefix || runtime.ContentEncodeURI != mapping.ContentEncodeURI || runtime.SourcePath != mapping.SourcePath {
		t.Fatalf("mapping outputs changed: %+v %v", runtime, err)
	}
	var absorbed model.MediaDirectory
	if err := db.Unscoped().First(&absorbed, mapping.ID).Error; err != nil || !absorbed.DeletedAt.Valid || absorbed.MergedIntoID != target.ID || absorbed.SavePath != mapping.SavePath || absorbed.FilterRules != mapping.FilterRules {
		t.Fatalf("source not retained: %+v %v", absorbed, err)
	}
	if id, err := model.ResolveMergedMediaDirectoryID(db, 1, mapping.ID); err != nil || id != target.ID {
		t.Fatalf("old running consumer reference lost: %d %v", id, err)
	}
	if _, err := model.ResolveMergedMediaDirectoryID(db, 2, mapping.ID); err == nil {
		t.Fatal("alias escaped owner")
	}
	db.First(&setting, setting.ID)
	if len(setting.CloudPathIDs) != 1 || setting.CloudPathIDs[0] != target.ID {
		t.Fatalf("setting=%+v", setting)
	}
	db.First(&config, config.ID)
	if !strings.Contains(config.ConfigValue, `"cloudPathID":8`) || !strings.Contains(config.ConfigValue, "keep-private") || !strings.Contains(config.ConfigValue, "9007199254740993") || !strings.Contains(config.ConfigValue, `"future":"keep"`) {
		t.Fatal("PlutoFish configuration changed unexpectedly")
	}
	db.First(&log, log.ID)
	if log.CloudPathID != target.ID || log.Target != "unchanged" {
		t.Fatal("log association lost")
	}
	db.First(&plan, "id = ?", plan.ID)
	if plan.CloudDirectoryID != target.ID || plan.DirectoryJSON != `{"immutable":"snapshot"}` {
		t.Fatal("organizing snapshot rewritten")
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "existing content" {
		t.Fatal("merge changed STRM files")
	}
}

func TestMediaDirectoryMergeRejectsStaleUnsafeAndBusyConfigurations(t *testing.T) {
	for _, kind := range []string{"stale", "owner", "storage", "path", "content", "encoding", "source", "busy", "missing-preview"} {
		t.Run(kind, func(t *testing.T) {
			db, router, target, mapping := mergeDirectoryFixture(t)
			p := mergePreviewRequest(t, router, target.ID, mapping.ID)
			switch kind {
			case "stale":
				db.Model(&target).Update("exclude_smaller_than_mb", 99)
			case "owner":
				db.Model(&mapping).Update("user_id", 2)
			case "storage":
				db.Model(&mapping).Update("cloud_storage_id", 2)
			case "path":
				db.Model(&mapping).Update("save_path", "/different")
			case "content":
				db.Model(&mapping).Update("content_prefix", "/different")
			case "encoding":
				db.Model(&mapping).Update("content_encode_uri", true)
			case "source":
				db.Model(&mapping).Update("source_path", "/115LongTerm/other")
			case "busy":
				db.Create(&model.OrganizePlan{ID: "active", UserID: 1, CloudDirectoryID: target.ID, Status: "executing"})
			case "missing-preview":
				p.Revision = ""
			}
			w := directoryRequest(router, "POST", "/media-directories/8/merge", fmt.Sprintf(`{"mapping_id":9,"revision":%q}`, p.Revision))
			if w.Code == 200 {
				t.Fatalf("unsafe merge accepted: %s", w.Body)
			}
			var count int64
			db.Model(&model.MediaDirectory{}).Where("id IN ?", []uint{target.ID, mapping.ID}).Count(&count)
			if count != 2 {
				t.Fatal("failed merge removed a configuration")
			}
		})
	}
}

func TestMediaDirectoryMergeRollsBackReferencesOnSaveFailure(t *testing.T) {
	db, router, target, mapping := mergeDirectoryFixture(t)
	setting := model.EmbyVersionCheckSetting{UserID: 1, CloudPathIDs: []uint{mapping.ID}}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	p := mergePreviewRequest(t, router, target.ID, mapping.ID)
	plan := model.OrganizePlan{ID: "ready-plan", UserID: 1, CloudDirectoryID: target.ID, Status: "ready"}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_directory_merge BEFORE UPDATE ON media_directories WHEN NEW.id = 8 AND NEW.mapping_enabled = 1 BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	w := directoryRequest(router, "POST", "/media-directories/8/merge", fmt.Sprintf(`{"mapping_id":9,"revision":%q}`, p.Revision))
	if w.Code != 500 {
		t.Fatalf("status=%d %s", w.Code, w.Body)
	}
	db.First(&setting, setting.ID)
	if len(setting.CloudPathIDs) != 1 || setting.CloudPathIDs[0] != mapping.ID {
		t.Fatal("references partially committed")
	}
	var source model.MediaDirectory
	if err := db.First(&source, mapping.ID).Error; err != nil || source.MergedIntoID != 0 {
		t.Fatal("source deletion partially committed")
	}
	if err := db.First(&plan, "id = ?", plan.ID).Error; err != nil || plan.Status != "ready" {
		t.Fatalf("draft cancellation partially committed: %+v err=%v", plan, err)
	}
}

func TestMergedDirectoryLocalVersionScanKeepsOriginalRootAndEmbyPaths(t *testing.T) {
	db, router, target, mapping := mergeDirectoryFixture(t)
	writeTestVersionFile(t, mapping.SavePath, "115LongTerm/影视中心/Movies/The Matrix (1999) {tmdb-603}/The.Matrix.2160p.strm")
	writeTestVersionFile(t, mapping.SavePath, "115LongTerm/影视中心/Movies/The Matrix (1999) {tmdb-603}/The.Matrix.1080p.strm")
	before := scanEmbyVersionCloudPaths([]model.MediaDirectory{mapping}, "movie")
	p := mergePreviewRequest(t, router, target.ID, mapping.ID)
	w := directoryRequest(router, "POST", "/media-directories/8/merge", fmt.Sprintf(`{"mapping_id":9,"revision":%q}`, p.Revision))
	if w.Code != 200 {
		t.Fatalf("merge: %s", w.Body)
	}
	var merged model.MediaDirectory
	if err := db.First(&merged, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	after := scanEmbyVersionCloudPaths([]model.MediaDirectory{merged}, "movie")
	if before.TotalFiles != 2 || after.TotalFiles != before.TotalFiles || after.DuplicateMovieCount != before.DuplicateMovieCount || len(after.ScannedPaths) != 1 || after.ScannedPaths[0].SavePath != mapping.SavePath || after.ScannedPaths[0].EmbyPathPrefix != mapping.EmbyPathPrefix {
		t.Fatalf("scan changed: before=%+v after=%+v", before, after)
	}
}
