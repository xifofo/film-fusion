package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"gorm.io/gorm"
)

func TestMergedDirectorySyncCreateRenameDeleteKeepsPathsContentAndCacheKeys(t *testing.T) {
	db := setupBalanceServiceTestDB(t)
	t.Cleanup(WaitOrganizeLogs)
	if err := db.AutoMigrate(&model.PickcodeCache{}, &model.OrganizeLog{}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	storage := &model.CloudStorage{ID: 1, StorageType: model.StorageType115Open}
	legacy := model.MediaDirectory{MappingEnabled: true, ID: 9, CloudStorageID: 1, SourceType: model.SourceTypeCloudDrive2, SourcePath: "/115LongTerm/影视中心", SavePath: root, ContentPrefix: "/CloudNAS/CloudDrive", FilterRules: `{"include":[".mkv"],"download":[".srt"]}`, CloudStorage: storage}
	merged := legacy
	merged.ID = 8
	merged.OrganizeEnabled = true
	merged.SavePath = filepath.Join(root, "115LongTerm")
	merged.ContentPrefix += "/115LongTerm"
	merged.MappingPathPrefix = "/115LongTerm"
	log := logger.New(config.LogConfig{Output: "stdout", Level: "error"})
	defer log.Close()
	s := NewStrmService(log, nil)
	event := "/115LongTerm/影视中心/电影/样本 #1.mkv"
	file := filepath.Join(root, "115LongTerm/影视中心/电影/样本 #1.strm")
	s.CreateStrmOrDownloadWith115OpenAPI(event, legacy, "sample-pick")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s.CreateStrmOrDownloadWith115OpenAPI(event, merged, "sample-pick")
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(before) {
		t.Fatalf("content changed: %q %q %v", before, after, err)
	}
	var cached model.PickcodeCache
	if err := database.DB.Where("file_path = ?", "/CloudNAS/CloudDrive/115LongTerm/影视中心/电影/样本 #1.mkv").First(&cached).Error; err != nil || cached.Pickcode != "sample-pick" {
		t.Fatal("cache key changed")
	}
	if _, err := os.Stat(filepath.Join(root, "115LongTerm/115LongTerm")); !os.IsNotExist(err) {
		t.Fatal("mount prefix duplicated")
	}
	s.RenameFile(event, "/115LongTerm/影视中心/电影/新名.mkv", merged, true, true)
	newFile := filepath.Join(root, "115LongTerm/影视中心/电影/新名.strm")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("rename retained old output")
	}
	data, err := os.ReadFile(newFile)
	if err != nil || string(data) != "/CloudNAS/CloudDrive/115LongTerm/影视中心/电影/新名.mkv" {
		t.Fatalf("renamed content=%q err=%v", data, err)
	}
	s.DeleteStrm("/115LongTerm/影视中心/电影/新名.mkv", merged, false)
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatal("delete missed rebased output")
	}
	// 目录移出监控范围时只清理原目录，不能多剥一层或扩大删除范围。
	s.CreateStrmOrDownloadWith115OpenAPI(event, merged, "")
	outside := filepath.Join(root, "keep.strm")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s.RenameDir("/115LongTerm/影视中心/电影", "/outside", merged, true, false)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("directory rename missed output")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("directory cleanup escaped original scope")
	}
}

func TestRunningPlutoFishConsumerFollowsMergedMappingWithoutRestart(t *testing.T) {
	b, mapping, event := plutoFishBusinessFixture(t)
	mapping.SourceType = model.SourceTypeCloudDrive2
	mapping.SourcePath = "/115LongTerm/Movies"
	if err := b.db.Save(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	first, err := b.Handle(context.Background(), "scope", event)
	if err != nil {
		t.Fatal(err)
	}
	target := model.MediaDirectory{ID: 3, UserID: mapping.UserID, CloudStorageID: mapping.CloudStorageID, DirectoryName: "Movies", DirectoryID: "cloud-root", OrganizeEnabled: true, SavePath: filepath.Join(mapping.SavePath, "115LongTerm"), ContentPrefix: mapping.ContentPrefix + "/115LongTerm", ContentEncodeURI: true}
	if err := b.db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Transaction(func(tx *gorm.DB) error {
		p, err := PreviewMediaDirectoryMerge(tx, mapping.UserID, target.ID, mapping.ID)
		if err != nil {
			return err
		}
		return ApplyMediaDirectoryMerge(tx, mapping.UserID, p)
	}); err != nil {
		t.Fatal(err)
	}
	// b.options 仍持有被吸收的旧 ID，模拟已运行的接收线程。
	event.ID = "owner:next"
	event.TransferID = "transfer-next"
	second, err := b.Handle(context.Background(), "scope", event)
	if err != nil || first.SavePath != second.SavePath {
		t.Fatalf("old consumer lost reference: %+v %+v %v", first, second, err)
	}
}
