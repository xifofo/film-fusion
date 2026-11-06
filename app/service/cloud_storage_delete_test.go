package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func storageDeleteFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delete.db")+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.CloudStorage{}, &model.MediaDirectory{}, &model.Match302{}, &model.Match302BalanceMember{},
		&model.Match302BalanceAssignment{}, &model.EmbyAccountBinding{}, &model.AutomationSource{},
		&model.AutomationWorkflow{}, &model.AutomationTemplate{}, &model.AutomationRun{}, &model.AutomationNodeRun{}, &model.AutomationRevision{},
		&model.Download115Queue{}, &model.OrganizePreviewTask{}, &model.OrganizeUpgradeJob{},
		&model.OrganizeSourceFolderDeletionTask{}, &model.OrganizeStorageLock{}, &model.OrganizeManagedStrm{},
		&model.OrganizePlan{}, &model.OrganizeLog{}, &model.OrganizeMediaFile{},
	); err != nil {
		t.Fatal(err)
	}
	storageDeleteCreate(t, db, &model.CloudStorage{ID: 7, UserID: 1, StorageName: "电影主盘", Cookie: "private-cookie", AccessToken: "private-token"})
	storageDeleteCreate(t, db, &model.CloudStorage{ID: 70, UserID: 2, StorageName: "其他账号"})
	storageDeleteCreate(t, db, &model.AutomationSource{ID: 1, Name: "无存储绑定的来源"})
	return db
}

func storageDeleteCreate(t *testing.T, db *gorm.DB, row any) {
	t.Helper()
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
}

func assertStorageNotDeleted(t *testing.T, db *gorm.DB) {
	t.Helper()
	var row model.CloudStorage
	if err := db.First(&row, 7).Error; err != nil || row.Cookie != "private-cookie" || row.AccessToken != "private-token" {
		t.Fatalf("storage or credentials changed: %v", err)
	}
}

func TestDeleteCloudStorageBlocksReferences(t *testing.T) {
	tests := []struct {
		name string
		row  any
		want string
	}{
		{"disabled directory", &model.MediaDirectory{CloudStorageID: 7, DirectoryName: "电影目录"}, "电影目录"},
		{"match source", &model.Match302{CloudStorageID: 7, SourcePath: "/Movies"}, "/Movies"},
		{"pool member", &model.Match302BalanceMember{CloudStorageID: 7, Match302ID: 12, TargetRootPath: "/pool"}, "规则 #12"},
		{"emby binding", &model.EmbyAccountBinding{CloudStorageID: 7, EmbyUserName: "客厅用户", EmbyUserID: "emby-1"}, "客厅用户"},
		{"disabled source", &model.AutomationSource{CloudStorageID: 7, Name: "目录入库"}, "目录入库"},
		{"workflow numeric", &model.AutomationWorkflow{SourceID: 1, Name: "离线下载", DefinitionJSON: `{"nodes":[{"config":{"cloud_storage_id":7}}]}`}, "离线下载"},
		{"workflow string", &model.AutomationWorkflow{SourceID: 1, Name: "字符串绑定", DefinitionJSON: `{"nodes":[{"config":{"cloud_storage_id":"7"}}]}`}, "字符串绑定"},
		{"template", &model.AutomationTemplate{Name: "下载模板", ConfigurationJSON: `{"source":{"cloud_storage_id":7}}`}, "下载模板"},
		{"running snapshot", &model.AutomationRun{Status: model.AutomationRunRunning, WorkflowName: "旧版流程运行", DefinitionJSON: `{"nodes":[{"config":{"cloud_storage_id":7}}]}`, ContextJSON: `{}`}, "旧版流程运行"},
		{"pending context", &model.AutomationRun{Status: model.AutomationRunPending, WorkflowName: "输入引用", DefinitionJSON: `{}`, ContextJSON: `{"source":{"cloud_storage_id":7}}`}, "输入引用"},
		{"pending download", &model.Download115Queue{CloudStorageID: 7, SavePath: "/download/pending.srt", PickCode: "pending", Status: model.QueueStatusPending}, "/download/pending.srt"},
		{"running download", &model.Download115Queue{CloudStorageID: 7, SavePath: "/download/running.srt", PickCode: "running", Status: model.QueueStatusDownloading}, "/download/running.srt"},
		{"failed download", &model.Download115Queue{CloudStorageID: 7, SavePath: "/download/failed.srt", PickCode: "failed", Status: model.QueueStatusFailed}, "/download/failed.srt"},
		{"pending preview", &model.OrganizePreviewTask{CloudStorageID: 7, FolderName: "待整理影片", Status: model.OrganizePreviewStatusPending}, "待整理影片"},
		{"failed preview", &model.OrganizePreviewTask{CloudStorageID: 7, FolderName: "失败预览", Status: model.OrganizePreviewStatusFailed}, "失败预览"},
		{"failed recovery", &model.OrganizeUpgradeJob{ID: "upgrade-1", CloudStorageID: 7, Status: "failed", RecoveryPath: "/recovery"}, "/recovery"},
		{"delayed deletion", &model.OrganizeSourceFolderDeletionTask{CloudStorageID: 7, FolderID: "folder-9"}, "folder-9"},
		{"retained lock", &model.OrganizeStorageLock{CloudStorageID: 7, PlanID: "plan-locked"}, "plan-locked"},
		{"live strm", &model.OrganizeManagedStrm{CloudStorageID: 7, Path: "/local/movie.strm", CloudPath: "/cloud/movie.mkv"}, "/local/movie.strm"},
		{"assignment source", &model.Match302BalanceAssignment{SourceStorageID: 7, PlaybackStorageID: 70, SourceFilePath: "/cached-source.mkv", Status: model.BalanceAssignmentStatusReady}, "/cached-source.mkv"},
		{"assignment playback", &model.Match302BalanceAssignment{SourceStorageID: 70, PlaybackStorageID: 7, SourceFilePath: "/cached-playback.mkv", Status: model.BalanceAssignmentStatusReady}, "/cached-playback.mkv"},
		{"retired cache waiting cleanup", &model.Match302BalanceAssignment{PlaybackStorageID: 7, SourceFilePath: "/retired.mkv", Retired: true, CleanupStatus: model.BalanceCleanupStatusFailed}, "/retired.mkv"},
		{"executing plan", &model.OrganizePlan{ID: "executing-1", Status: "executing", DirectoryJSON: `{"cloud_storage_id":7}`}, "executing-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := storageDeleteFixture(t)
			storageDeleteCreate(t, db, tt.row)
			err := DeleteCloudStorage(context.Background(), db, 1, 7)
			var inUse *CloudStorageInUseError
			if !errors.As(err, &inUse) || !strings.Contains(err.Error(), tt.want) || len(inUse.References) != 1 || inUse.References[0].Count != 1 {
				t.Fatalf("expected %q reference, got %v", tt.want, err)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("reference message leaked credentials")
			}
			assertStorageNotDeleted(t, db)
		})
	}
}

func TestDeleteCloudStorageAllowsHistoryAndUnrelatedReferences(t *testing.T) {
	db := storageDeleteFixture(t)
	expired := time.Now().Add(-time.Minute)
	for _, row := range []any{
		&model.MediaDirectory{CloudStorageID: 70, DirectoryName: "其他账号目录"},
		&model.OrganizeLog{CloudStorageID: 7, Source: "历史日志"},
		&model.OrganizeMediaFile{CloudStorageID: 7, FileID: "historical-facts"},
		&model.AutomationRevision{ConfigurationJSON: `{"cloud_storage_id":7}`},
		&model.AutomationRun{Status: model.AutomationRunSucceeded, DefinitionJSON: `{"cloud_storage_id":7}`},
		&model.Download115Queue{CloudStorageID: 7, Status: model.QueueStatusCompleted},
		&model.OrganizePreviewTask{CloudStorageID: 7, Status: model.OrganizePreviewStatusCompleted},
		&model.OrganizeUpgradeJob{ID: "complete", CloudStorageID: 7, Status: "completed"},
		&model.OrganizeUpgradeJob{ID: "rollback", CloudStorageID: 7, Status: "rolled_back"},
		&model.OrganizeManagedStrm{CloudStorageID: 7, Path: "/old.strm", Retired: true},
		&model.OrganizeStorageLock{CloudStorageID: 7, PlanID: "expired", LeaseUntil: &expired},
		&model.Match302BalanceAssignment{PlaybackStorageID: 7, CleanupStatus: model.BalanceCleanupStatusCleaned, Status: model.BalanceAssignmentStatusReady},
		&model.OrganizePlan{ID: "done", Status: "executed", DirectoryJSON: `{"cloud_storage_id":7}`},
		&model.AutomationWorkflow{SourceID: 1, Name: "非引用文字", DefinitionJSON: `{"nodes":[{"config":{"cloud_storage_id":70,"text":"cloud_storage_id:7","id":7}}]}`},
	} {
		storageDeleteCreate(t, db, row)
	}
	deletedDirectory := model.MediaDirectory{CloudStorageID: 7, DirectoryName: "已删除目录"}
	storageDeleteCreate(t, db, &deletedDirectory)
	if err := db.Delete(&deletedDirectory).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteCloudStorage(context.Background(), db, 1, 7); err != nil {
		t.Fatal(err)
	}
	var row model.CloudStorage
	if err := db.First(&row, 7).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("storage not deleted: %v", err)
	}
	if err := db.Unscoped().First(&row, 7).Error; err != nil || !row.DeletedAt.Valid {
		t.Fatalf("expected soft delete: %v", err)
	}
}

func TestDeleteCloudStorageAggregatesAndPreservesBindings(t *testing.T) {
	db := storageDeleteFixture(t)
	for i := 0; i < 12; i++ {
		storageDeleteCreate(t, db, &model.MediaDirectory{CloudStorageID: 7, DirectoryName: "电影目录"})
	}
	binding := model.EmbyAccountBinding{CloudStorageID: 7, EmbyUserName: "Emby 用户"}
	storageDeleteCreate(t, db, &binding)
	var inUse *CloudStorageInUseError
	err := DeleteCloudStorage(context.Background(), db, 1, 7)
	if !errors.As(err, &inUse) || len(inUse.References) != 2 || inUse.References[0].Count != 12 || len(inUse.References[0].Items) != 10 || !strings.Contains(err.Error(), "另有 2 处占用") {
		t.Fatalf("missing reference summary: %v", err)
	}
	if err := db.First(&model.EmbyAccountBinding{}, binding.ID).Error; err != nil {
		t.Fatalf("binding removed: %v", err)
	}
	if err := db.Where("cloud_storage_id = ?", 7).Delete(&model.MediaDirectory{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteCloudStorage(context.Background(), db, 1, 7); err != nil {
		t.Fatalf("still blocked after references removed: %v", err)
	}
}

func TestDeleteCloudStorageChecksRunningNodeOutputs(t *testing.T) {
	db := storageDeleteFixture(t)
	run := model.AutomationRun{Status: model.AutomationRunRunning, DefinitionJSON: `{}`, ContextJSON: `{}`}
	storageDeleteCreate(t, db, &run)
	storageDeleteCreate(t, db, &model.AutomationNodeRun{
		RunID: run.ID, NodeID: "download", NodeName: "下载输出", Status: model.AutomationNodeSucceeded,
		InputJSON: `{}`, OutputJSON: `{"cloud_storage_id":7}`,
	})
	var inUse *CloudStorageInUseError
	if err := DeleteCloudStorage(context.Background(), db, 1, 7); !errors.As(err, &inUse) || !strings.Contains(err.Error(), "下载输出") {
		t.Fatalf("running workflow output not protected: %v", err)
	}
	if err := db.Model(&run).Update("status", model.AutomationRunSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteCloudStorage(context.Background(), db, 1, 7); err != nil {
		t.Fatalf("completed workflow output blocked deletion: %v", err)
	}
}

func TestDeleteCloudStorageReferenceSamplesExcludeDeletedDirectories(t *testing.T) {
	db := storageDeleteFixture(t)
	old := model.MediaDirectory{CloudStorageID: 7, DirectoryName: "已删除，不应展示"}
	storageDeleteCreate(t, db, &old)
	if err := db.Delete(&old).Error; err != nil {
		t.Fatal(err)
	}
	storageDeleteCreate(t, db, &model.MediaDirectory{CloudStorageID: 7, DirectoryName: "当前目录"})
	var inUse *CloudStorageInUseError
	err := DeleteCloudStorage(context.Background(), db, 1, 7)
	if !errors.As(err, &inUse) || strings.Contains(err.Error(), "已删除") || !strings.Contains(err.Error(), "当前目录") || inUse.References[0].Count != 1 || len(inUse.References[0].Items) != 1 {
		t.Fatalf("incorrect directory samples: %v", err)
	}
}

func TestDeleteCloudStorageFailsClosed(t *testing.T) {
	for _, kind := range []string{"foreign owner", "missing", "unauthenticated", "broken table", "invalid JSON"} {
		t.Run(kind, func(t *testing.T) {
			db := storageDeleteFixture(t)
			owner, storageID := uint(1), uint(7)
			switch kind {
			case "foreign owner":
				owner = 2
			case "missing":
				storageID = 700
			case "unauthenticated":
				owner = 0
			case "broken table":
				if err := db.Migrator().DropTable(&model.AutomationTemplate{}); err != nil {
					t.Fatal(err)
				}
			case "invalid JSON":
				storageDeleteCreate(t, db, &model.AutomationWorkflow{SourceID: 1, Name: "损坏配置", DefinitionJSON: `{broken`})
			}
			if err := DeleteCloudStorage(context.Background(), db, owner, storageID); err == nil {
				t.Fatal("expected failure")
			}
			assertStorageNotDeleted(t, db)
		})
	}
}
