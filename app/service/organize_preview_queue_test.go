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

func TestClampOrganizePreviewTaskLimit(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{name: "negative uses default", value: -1, want: 100},
		{name: "zero uses default", value: 0, want: 100},
		{name: "minimum", value: 1, want: 1},
		{name: "custom", value: 250, want: 250},
		{name: "maximum", value: 1000, want: 1000},
		{name: "above maximum", value: 1001, want: 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClampOrganizePreviewTaskLimit(test.value); got != test.want {
				t.Fatalf("ClampOrganizePreviewTaskLimit(%d)=%d want=%d", test.value, got, test.want)
			}
		})
	}
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

func TestOrganizePreviewQueuePublishesScopedEvents(t *testing.T) {
	db := newOrganizePreviewQueueTestDB(t)
	queue := &OrganizePreviewQueue{db: db}
	events, unsubscribe, err := queue.Subscribe(7, 8)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()
	otherEvents, unsubscribeOther, err := queue.Subscribe(7, 99)
	if err != nil {
		t.Fatalf("subscribe other directory: %v", err)
	}
	defer unsubscribeOther()

	tasks, err := queue.Enqueue([]OrganizePreviewTaskInput{{
		UserID:           7,
		CloudDirectoryID: 8,
		CloudStorageID:   9,
		FolderID:         "folder-event",
		FolderName:       "事件目录",
	}})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("enqueued tasks=%d want=1", len(tasks))
	}

	select {
	case event := <-events:
		if event.Type != "queued" || event.TaskID != tasks[0].ID || event.CloudDirectoryID != 8 {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue event")
	}
	select {
	case event := <-otherEvents:
		if event.Type != "queue_changed" || event.TaskID != 0 || event.CloudDirectoryID != 0 {
			t.Fatalf("unexpected anonymized queue event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for anonymized queue event")
	}
}

func TestOrganizePreviewQueuePendingPositionsFollowWorkerOrder(t *testing.T) {
	db := newOrganizePreviewQueueTestDB(t)
	now := time.Now()
	tasks := []model.OrganizePreviewTask{
		{
			UserID:           7,
			CloudDirectoryID: 8,
			CloudStorageID:   9,
			FolderID:         "folder-later",
			Status:           model.OrganizePreviewStatusPending,
			CreatedAt:        now,
		},
		{
			UserID:           7,
			CloudDirectoryID: 8,
			CloudStorageID:   9,
			FolderID:         "folder-first",
			Status:           model.OrganizePreviewStatusPending,
			CreatedAt:        now.Add(-time.Minute),
		},
		{
			UserID:           7,
			CloudDirectoryID: 8,
			CloudStorageID:   9,
			FolderID:         "folder-completed",
			Status:           model.OrganizePreviewStatusCompleted,
			CreatedAt:        now.Add(-time.Hour),
		},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	queue := &OrganizePreviewQueue{db: db}
	positions, err := queue.PendingPositions()
	if err != nil {
		t.Fatalf("pending positions: %v", err)
	}
	if positions[tasks[1].ID] != 1 || positions[tasks[0].ID] != 2 {
		t.Fatalf("unexpected positions: %+v", positions)
	}
	if _, ok := positions[tasks[2].ID]; ok {
		t.Fatalf("completed task should not have a queue position: %+v", positions)
	}
}
