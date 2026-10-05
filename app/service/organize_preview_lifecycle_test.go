package service

import (
	"context"
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestPreviewShutdownCancelsWorkerAndKeepsTaskPending(t *testing.T) {
	db := newOrganizePreviewQueueTestDB(t)
	pool, _ := db.DB()
	defer pool.Close()
	entered := make(chan struct{})
	queue := NewOrganizePreviewQueueWithContext(nil, func(ctx context.Context, task model.OrganizePreviewTask) (OrganizePreviewProcessResult, error) {
		close(entered)
		<-ctx.Done()
		return OrganizePreviewProcessResult{}, ctx.Err()
	})
	queue.db = db
	task := model.OrganizePreviewTask{UserID: 1, CloudDirectoryID: 1, CloudStorageID: 1, FolderID: "folder", Status: model.OrganizePreviewStatusPending}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	queue.Start()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		queue.Stop()
		t.Fatal("preview did not start")
	}
	queue.Stop()
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != model.OrganizePreviewStatusPending || task.StartedAt != nil || task.Error != "" {
		t.Fatalf("shutdown changed task outcome: %#v", task)
	}
}
