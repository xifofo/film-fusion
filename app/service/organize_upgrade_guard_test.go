package service

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

type upgradeGuardTransport func(*http.Request) (*http.Response, error)

func (f upgradeGuardTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpgradeGuardProtectsCurrentStrmFromDelayedEvents(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "guards.db")), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&model.CloudStorage{}, &model.OrganizeManagedStrm{}, &model.OrganizeUpgradeJob{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous; sql, _ := db.DB(); _ = sql.Close() })
	log := logger.New(config.LogConfig{Output: "stdout", Level: "error", Format: "json"})
	t.Cleanup(func() { _ = log.Close() })
	s := NewStrmService(log, nil)
	root := t.TempDir()
	target := filepath.Join(root, "media", "title.strm")
	if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, []byte("current"), 0644); err != nil {
		t.Fatal(err)
	}
	storage := model.CloudStorage{ID: 3, UserID: 1, Cookie: "mock"}
	if err = db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	job := model.OrganizeUpgradeJob{ID: "job", UserID: 1, CloudStorageID: 3, Status: "failed"}
	if err = db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	binding := model.OrganizeManagedStrm{JobID: "job", Path: target, Root: root, UserID: 1, CloudStorageID: 3, ParentID: "900", FileID: "new-file", CloudPath: "/media/title.mkv"}
	if err = db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	present, reads := true, 0
	client := driver.New()
	client.Client.SetTransport(upgradeGuardTransport(func(r *http.Request) (*http.Response, error) {
		reads++
		body := `{"state":true,"count":0,"data":[]}`
		if present {
			body = `{"state":true,"count":1,"data":[{"fid":"new-file","cid":"900","n":"title.mkv","ico":"mkv","s":"20","pc":"new-pick"}]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	s.newUpgradeClient = func(string) (*driver.Pan115Client, error) { return client, nil }
	s.DeleteAction(root, "/media/title.mkv", false)
	if reads != 0 {
		t.Fatal("unfinished journal should protect without cloud reads")
	}
	if err = db.Model(&job).Update("status", "completed").Error; err != nil {
		t.Fatal(err)
	}
	s.DeleteAction(root, "/media/title.mkv", false)
	s.DeleteAction(root, "/media", true)
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "current" {
		t.Fatalf("late delete removed new STRM: %q %v", data, err)
	}
	mapping := model.CloudPath{LocalPath: root, ContentPrefix: "wrong prefix", SourceType: model.SourceTypeMoviePilot2, CloudStorage: &storage}
	s.CreateStrmOrDownloadWith115OpenAPI("/media/title.mkv", mapping, "old-pick")
	data, _ = os.ReadFile(target)
	if string(data) != "current" {
		t.Fatal("late create overwrote new STRM")
	}
	if reads == 0 {
		t.Fatal("completed journal did not validate cloud ownership")
	}
	present = false
	s.DeleteAction(root, "/media/title.mkv", false)
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("actual cloud removal did not clean STRM")
	}
	s.CreateStrmOrDownloadWith115OpenAPI("/media/title.mkv", mapping, "old-pick")
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("delayed create resurrected removed STRM")
	}
	queue := &Download115Service{db: db}
	if err = queue.performDownload(&model.Download115Queue{CloudStorageID: 3, SavePath: target}); err == nil {
		t.Fatal("old download passed managed ownership guard")
	}
	if err = CheckUpgradeDownloadOwnership(db, 3, filepath.Join(root, "unrelated.srt")); err != nil {
		t.Fatal("unrelated download blocked")
	}
}

func TestUpgradeRecoveryAndRetiredCacheGuards(t *testing.T) {
	if !upgradeRecoveryEvent("/挂载/FilmFusion-Recovery/job/file.mkv", model.CloudPath{SourceType: model.SourceTypeCloudDrive2}) {
		t.Fatal("CD2 recovery event not excluded")
	}
	if upgradeRecoveryEvent("/挂载/FilmFusion-Recovery-other/file.mkv", model.CloudPath{SourceType: model.SourceTypeCloudDrive2}) {
		t.Fatal("unrelated recovery prefix excluded")
	}
	expires := time.Now().Add(time.Hour)
	if assignmentCacheReusable(&model.Match302BalanceAssignment{Retired: true, Status: model.BalanceAssignmentStatusReady, TargetPickcode: "old", ExpiresAt: &expires}, time.Now()) {
		t.Fatal("retired ready assignment reused")
	}
}
