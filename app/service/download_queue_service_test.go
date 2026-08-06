package service

import (
	"errors"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClaimQueueTaskSerializesWorkerAndRemoval(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:download-queue-claim?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}, &model.Download115Queue{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	originalDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = originalDB })

	storage := model.CloudStorage{
		UserID:      1,
		StorageType: model.StorageType115Open,
		StorageName: "领取测试 115",
		Status:      model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatalf("create storage: %v", err)
	}

	downloadService := NewDownload115Service(nil, 1)
	claimedTask := model.Download115Queue{
		CloudStorageID: storage.ID,
		PickCode:       "claimed-task",
		SavePath:       "/media/claimed.mkv",
		Status:         model.QueueStatusPending,
		MaxRetryCount:  3,
	}
	if err := db.Create(&claimedTask).Error; err != nil {
		t.Fatalf("create claimed task: %v", err)
	}

	claimed, err := downloadService.claimQueueTask(claimedTask.ID)
	if err != nil || !claimed {
		t.Fatalf("claim task: claimed=%v err=%v", claimed, err)
	}
	if err := downloadService.RemoveQueueTask(claimedTask.ID); !errors.Is(err, ErrDownloadTaskRunning) {
		t.Fatalf("remove claimed task error=%v", err)
	}

	var storedClaimedTask model.Download115Queue
	if err := db.First(&storedClaimedTask, claimedTask.ID).Error; err != nil {
		t.Fatalf("load claimed task: %v", err)
	}
	if storedClaimedTask.Status != model.QueueStatusDownloading {
		t.Fatalf("claimed task status=%q", storedClaimedTask.Status)
	}

	removedTask := model.Download115Queue{
		CloudStorageID: storage.ID,
		PickCode:       "removed-task",
		SavePath:       "/media/removed.mkv",
		Status:         model.QueueStatusPending,
		MaxRetryCount:  3,
	}
	if err := db.Create(&removedTask).Error; err != nil {
		t.Fatalf("create removed task: %v", err)
	}
	if err := downloadService.RemoveQueueTask(removedTask.ID); err != nil {
		t.Fatalf("remove pending task: %v", err)
	}
	claimed, err = downloadService.claimQueueTask(removedTask.ID)
	if err != nil {
		t.Fatalf("claim removed task: %v", err)
	}
	if claimed {
		t.Fatal("removed task was claimed by worker")
	}
}
