package service

import (
	"errors"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSourceFolderDeletionTestService(t *testing.T) (*Download115Service, *gorm.DB, model.CloudStorage) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "source-folder-deletion.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CloudStorage{},
		&model.OrganizeSourceFolderDeletionTask{},
		&model.Download115Queue{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	originalDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = originalDB })

	storage := model.CloudStorage{
		UserID:      1,
		StorageType: model.StorageType115Open,
		StorageName: "延迟删除测试 115",
		Status:      model.StatusActive,
		Cookie:      "UID=test; CID=test; SEID=test",
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatalf("create storage: %v", err)
	}
	return NewDownload115Service(nil, 1), db, storage
}

func TestSourceFolderDeletionWaitsForEveryDownload(t *testing.T) {
	service, db, storage := newSourceFolderDeletionTestService(t)
	deleteCalls := 0
	service.deleteSourceFolder = func(gotStorage model.CloudStorage, folderID string) error {
		deleteCalls++
		if gotStorage.ID != storage.ID || folderID != "source-100" {
			t.Fatalf("delete target storage=%d folder=%q", gotStorage.ID, folderID)
		}
		return nil
	}

	deletionID, err := service.BeginSourceFolderDeletion(storage.ID, "source-100")
	if err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, "subtitle-1", "/media/subtitle-1.ass", deletionID); err != nil {
		t.Fatalf("add first download: %v", err)
	}
	if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, "subtitle-2", "/media/subtitle-2.ass", deletionID); err != nil {
		t.Fatalf("add second download: %v", err)
	}

	schedule, err := service.ArmSourceFolderDeletion(deletionID)
	if err != nil {
		t.Fatalf("arm deletion: %v", err)
	}
	if !schedule.Pending || schedule.Deleted || schedule.TotalDownloads != 2 || deleteCalls != 0 {
		t.Fatalf("initial schedule=%+v deleteCalls=%d", schedule, deleteCalls)
	}

	var downloads []model.Download115Queue
	if err := db.Order("id ASC").Find(&downloads).Error; err != nil || len(downloads) != 2 {
		t.Fatalf("load downloads len=%d err=%v", len(downloads), err)
	}
	firstDeletionID, err := service.completeDownloadQueueTask(downloads[0].ID)
	if err != nil || firstDeletionID == nil || *firstDeletionID != deletionID {
		t.Fatalf("complete first deletionID=%v err=%v", firstDeletionID, err)
	}
	schedule, err = service.tryDeleteReadySourceFolder(deletionID)
	if err != nil {
		t.Fatalf("check after first download: %v", err)
	}
	if !schedule.Pending || schedule.CompletedDownloads != 1 || deleteCalls != 0 {
		t.Fatalf("after first schedule=%+v deleteCalls=%d", schedule, deleteCalls)
	}

	secondDeletionID, err := service.completeDownloadQueueTask(downloads[1].ID)
	if err != nil || secondDeletionID == nil || *secondDeletionID != deletionID {
		t.Fatalf("complete second deletionID=%v err=%v", secondDeletionID, err)
	}
	schedule, err = service.tryDeleteReadySourceFolder(deletionID)
	if err != nil {
		t.Fatalf("delete after second download: %v", err)
	}
	if !schedule.Deleted || schedule.Pending || schedule.CompletedDownloads != 2 || deleteCalls != 1 {
		t.Fatalf("final schedule=%+v deleteCalls=%d", schedule, deleteCalls)
	}
	if err := db.First(&model.OrganizeSourceFolderDeletionTask{}, deletionID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deletion task still exists: %v", err)
	}
}

func TestSourceFolderDeletionDoesNotDeleteBeforeOrganizeIsArmed(t *testing.T) {
	service, db, storage := newSourceFolderDeletionTestService(t)
	deleteCalls := 0
	service.deleteSourceFolder = func(model.CloudStorage, string) error {
		deleteCalls++
		return nil
	}

	deletionID, err := service.BeginSourceFolderDeletion(storage.ID, "source-before-arm")
	if err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, "subtitle-before-arm", "/media/before-arm.ass", deletionID); err != nil {
		t.Fatalf("add download: %v", err)
	}

	var download model.Download115Queue
	if err := db.Where("pick_code = ?", "subtitle-before-arm").First(&download).Error; err != nil {
		t.Fatalf("load download: %v", err)
	}
	linkedDeletionID, err := service.completeDownloadQueueTask(download.ID)
	if err != nil || linkedDeletionID == nil {
		t.Fatalf("complete download deletionID=%v err=%v", linkedDeletionID, err)
	}
	schedule, err := service.tryDeleteReadySourceFolder(deletionID)
	if err != nil {
		t.Fatalf("check collecting deletion: %v", err)
	}
	if !schedule.Pending || schedule.Deleted || deleteCalls != 0 {
		t.Fatalf("collecting schedule=%+v deleteCalls=%d", schedule, deleteCalls)
	}

	schedule, err = service.ArmSourceFolderDeletion(deletionID)
	if err != nil {
		t.Fatalf("arm deletion: %v", err)
	}
	if !schedule.Deleted || schedule.Pending || deleteCalls != 1 {
		t.Fatalf("armed schedule=%+v deleteCalls=%d", schedule, deleteCalls)
	}
}

func TestSourceFolderDeletionConcurrentCompletionsDeleteOnce(t *testing.T) {
	service, db, storage := newSourceFolderDeletionTestService(t)
	var deleteCalls atomic.Int32
	service.deleteSourceFolder = func(model.CloudStorage, string) error {
		deleteCalls.Add(1)
		return nil
	}

	deletionID, err := service.BeginSourceFolderDeletion(storage.ID, "source-concurrent")
	if err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	const downloadCount = 50
	for i := 0; i < downloadCount; i++ {
		pickCode := fmt.Sprintf("subtitle-concurrent-%d", i)
		if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, pickCode, "/media/"+pickCode+".ass", deletionID); err != nil {
			t.Fatalf("add download %d: %v", i, err)
		}
	}
	if _, err := service.ArmSourceFolderDeletion(deletionID); err != nil {
		t.Fatalf("arm deletion: %v", err)
	}

	var downloads []model.Download115Queue
	if err := db.Find(&downloads).Error; err != nil || len(downloads) != downloadCount {
		t.Fatalf("load downloads len=%d err=%v", len(downloads), err)
	}
	errCh := make(chan error, downloadCount)
	var wg sync.WaitGroup
	for _, download := range downloads {
		download := download
		wg.Add(1)
		go func() {
			defer wg.Done()
			linkedDeletionID, err := service.completeDownloadQueueTask(download.ID)
			if err != nil {
				errCh <- err
				return
			}
			if linkedDeletionID == nil {
				errCh <- errors.New("completed download lost deletion link")
				return
			}
			_, err = service.tryDeleteReadySourceFolder(*linkedDeletionID)
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent completion: %v", err)
	}
	if t.Failed() {
		return
	}

	service.processReadySourceFolderDeletions()
	if got := deleteCalls.Load(); got != 1 {
		t.Fatalf("delete calls=%d want 1", got)
	}
	if err := db.First(&model.OrganizeSourceFolderDeletionTask{}, deletionID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deletion task still exists: %v", err)
	}
}

func TestSourceFolderDeletionKeepsFolderWhenDownloadIsRemoved(t *testing.T) {
	service, db, storage := newSourceFolderDeletionTestService(t)
	deleteCalls := 0
	service.deleteSourceFolder = func(model.CloudStorage, string) error {
		deleteCalls++
		return nil
	}

	deletionID, err := service.BeginSourceFolderDeletion(storage.ID, "source-200")
	if err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, "subtitle-remove", "/media/remove.ass", deletionID); err != nil {
		t.Fatalf("add removed download: %v", err)
	}
	if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, "subtitle-keep", "/media/keep.ass", deletionID); err != nil {
		t.Fatalf("add remaining download: %v", err)
	}
	if _, err := service.ArmSourceFolderDeletion(deletionID); err != nil {
		t.Fatalf("arm deletion: %v", err)
	}

	var download model.Download115Queue
	if err := db.Where("pick_code = ?", "subtitle-remove").First(&download).Error; err != nil {
		t.Fatalf("load download: %v", err)
	}
	if err := service.RemoveQueueTask(download.ID); err != nil {
		t.Fatalf("remove download: %v", err)
	}
	service.processReadySourceFolderDeletions()

	if err := db.First(&model.OrganizeSourceFolderDeletionTask{}, deletionID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("source deletion was not cancelled: %v", err)
	}
	var remaining model.Download115Queue
	if err := db.Where("pick_code = ?", "subtitle-keep").First(&remaining).Error; err != nil {
		t.Fatalf("load remaining download: %v", err)
	}
	if remaining.SourceFolderDeletionID != nil || deleteCalls != 0 {
		t.Fatalf("remaining download still linked=%v deleteCalls=%d", remaining.SourceFolderDeletionID, deleteCalls)
	}
}

func TestSourceFolderDeletionRetriesDeleteFailure(t *testing.T) {
	service, db, storage := newSourceFolderDeletionTestService(t)
	deleteCalls := 0
	service.deleteSourceFolder = func(model.CloudStorage, string) error {
		deleteCalls++
		if deleteCalls == 1 {
			return errors.New("temporary 115 error")
		}
		return nil
	}

	deletionID, err := service.BeginSourceFolderDeletion(storage.ID, "source-300")
	if err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	schedule, err := service.ArmSourceFolderDeletion(deletionID)
	if err == nil || !schedule.Pending || schedule.Deleted || deleteCalls != 1 {
		t.Fatalf("first schedule=%+v err=%v deleteCalls=%d", schedule, err, deleteCalls)
	}
	if err := db.Model(&model.OrganizeSourceFolderDeletionTask{}).Where("id = ?", deletionID).
		Update("next_retry_at", nil).Error; err != nil {
		t.Fatalf("make retry ready: %v", err)
	}

	schedule, err = service.tryDeleteReadySourceFolder(deletionID)
	if err != nil {
		t.Fatalf("retry deletion: %v", err)
	}
	if !schedule.Deleted || deleteCalls != 2 {
		t.Fatalf("retry schedule=%+v deleteCalls=%d", schedule, deleteCalls)
	}
}

func TestRecoverInterruptedQueueStatePreservesArmedAndCancelsCollecting(t *testing.T) {
	service, db, storage := newSourceFolderDeletionTestService(t)

	armedID, err := service.BeginSourceFolderDeletion(storage.ID, "source-armed")
	if err != nil {
		t.Fatalf("begin armed deletion: %v", err)
	}
	if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, "subtitle-armed", "/media/armed.ass", armedID); err != nil {
		t.Fatalf("add armed download: %v", err)
	}
	if _, err := service.ArmSourceFolderDeletion(armedID); err != nil {
		t.Fatalf("arm deletion: %v", err)
	}
	if err := db.Model(&model.Download115Queue{}).Where("pick_code = ?", "subtitle-armed").Update("status", model.QueueStatusDownloading).Error; err != nil {
		t.Fatalf("mark download interrupted: %v", err)
	}
	if err := db.Model(&model.OrganizeSourceFolderDeletionTask{}).Where("id = ?", armedID).Update("status", model.SourceFolderDeletionStatusDeleting).Error; err != nil {
		t.Fatalf("mark deletion interrupted: %v", err)
	}

	collectingID, err := service.BeginSourceFolderDeletion(storage.ID, "source-collecting")
	if err != nil {
		t.Fatalf("begin collecting deletion: %v", err)
	}
	if err := service.AddDownloadTaskForSourceFolderDeletion(storage.ID, "subtitle-collecting", "/media/collecting.ass", collectingID); err != nil {
		t.Fatalf("add collecting download: %v", err)
	}

	if err := service.recoverInterruptedQueueState(); err != nil {
		t.Fatalf("recover state: %v", err)
	}

	var armedDownload model.Download115Queue
	if err := db.Where("pick_code = ?", "subtitle-armed").First(&armedDownload).Error; err != nil {
		t.Fatalf("load armed download: %v", err)
	}
	if armedDownload.Status != model.QueueStatusPending || armedDownload.SourceFolderDeletionID == nil || *armedDownload.SourceFolderDeletionID != armedID {
		t.Fatalf("armed download=%+v", armedDownload)
	}
	var armedDeletion model.OrganizeSourceFolderDeletionTask
	if err := db.First(&armedDeletion, armedID).Error; err != nil || armedDeletion.Status != model.SourceFolderDeletionStatusWaiting {
		t.Fatalf("armed deletion=%+v err=%v", armedDeletion, err)
	}

	if err := db.First(&model.OrganizeSourceFolderDeletionTask{}, collectingID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("collecting deletion still exists: %v", err)
	}
	var collectingDownload model.Download115Queue
	if err := db.Where("pick_code = ?", "subtitle-collecting").First(&collectingDownload).Error; err != nil {
		t.Fatalf("load collecting download: %v", err)
	}
	if collectingDownload.SourceFolderDeletionID != nil {
		t.Fatalf("collecting download still linked: %+v", collectingDownload)
	}
}
