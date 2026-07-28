package service

import (
	"path/filepath"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newOrganizePreviewQueueTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "organize-preview.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.OrganizePreviewTask{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}

func TestUpdateFolderAndRequeueResetsPreviewResult(t *testing.T) {
	db := newOrganizePreviewQueueTestDB(t)
	completedAt := time.Now()
	task := model.OrganizePreviewTask{
		UserID:                7,
		CloudDirectoryID:      8,
		CloudStorageID:        9,
		FolderID:              "folder-1",
		FolderName:            "示例电影 (2024)",
		FolderPath:            "下载 / 示例电影 (2024)",
		Status:                model.OrganizePreviewStatusCompleted,
		Total:                 3,
		ResultJSON:            `{"total":3}`,
		Error:                 "old error",
		BestVersionCount:      1,
		AlternateVersionCount: 2,
		CompletedAt:           &completedAt,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	queue := &OrganizePreviewQueue{db: db}
	claimed, err := queue.ClaimForFolderUpdate(task.UserID, task.ID)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimed.Status != model.OrganizePreviewStatusCompleted {
		t.Fatalf("claimed original status=%q want=%q", claimed.Status, model.OrganizePreviewStatusCompleted)
	}
	updated, err := queue.UpdateFolderAndRequeue(
		task.UserID,
		task.ID,
		"示例电影 (2024) {tmdb-12345}",
		"下载 / 示例电影 (2024) {tmdb-12345}",
	)
	if err != nil {
		t.Fatalf("update and requeue task: %v", err)
	}
	if updated.Status != model.OrganizePreviewStatusPending {
		t.Fatalf("updated status=%q want=%q", updated.Status, model.OrganizePreviewStatusPending)
	}
	if updated.FolderName != "示例电影 (2024) {tmdb-12345}" ||
		updated.FolderPath != "下载 / 示例电影 (2024) {tmdb-12345}" {
		t.Fatalf("folder metadata not updated: %+v", updated)
	}
	if updated.Total != 0 || updated.ResultJSON != "" || updated.Error != "" ||
		updated.BestVersionCount != 0 || updated.AlternateVersionCount != 0 ||
		updated.StartedAt != nil || updated.CompletedAt != nil {
		t.Fatalf("old preview result not reset: %+v", updated)
	}
}

func TestClaimForFolderUpdateRejectsProcessingTask(t *testing.T) {
	db := newOrganizePreviewQueueTestDB(t)
	task := model.OrganizePreviewTask{
		UserID:           7,
		CloudDirectoryID: 8,
		CloudStorageID:   9,
		FolderID:         "folder-2",
		Status:           model.OrganizePreviewStatusProcessing,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	queue := &OrganizePreviewQueue{db: db}
	if _, err := queue.ClaimForFolderUpdate(task.UserID, task.ID); err == nil {
		t.Fatal("processing task should not be claimed for folder update")
	}
}
