package service

// 验证失败记录清理的范围、任务保护及后续状态更新的重新展示。

import (
	"context"
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestClearFailedTransferQueue(t *testing.T) {
	db := setupBalanceServiceTestDB(t)
	old := time.Now().Add(-time.Hour)
	items := []model.Match302BalanceAssignment{
		{Match302ID: 1, SourceFilePath: "/failed", Status: model.BalanceAssignmentStatusFailed, TargetFileID: "preserved", UpdatedAt: old},
		{Match302ID: 1, SourceFilePath: "/pending", Status: model.BalanceAssignmentStatusPending, UpdatedAt: old},
		{Match302ID: 1, SourceFilePath: "/running", Status: model.BalanceAssignmentStatusTransferring, UpdatedAt: old},
		{Match302ID: 1, SourceFilePath: "/ready", Status: model.BalanceAssignmentStatusReady, UpdatedAt: old},
		{Match302ID: 1, SourceFilePath: "/source", Status: model.BalanceAssignmentStatusFailed, IsSourcePlayback: true, UpdatedAt: old},
		{Match302ID: 2, SourceFilePath: "/other-failed", Status: model.BalanceAssignmentStatusFailed, UpdatedAt: old},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewBalanceStatusService()
	count, err := svc.ClearFailedTransferQueue(context.Background(), []uint{items[0].ID, items[1].ID, items[2].ID, items[3].ID, items[4].ID, 999})
	if err != nil || count != 1 {
		t.Fatalf("selected clear: count=%d err=%v", count, err)
	}
	summary := svc.transferSummary()
	if summary.Failed != 1 || summary.Pending != 1 || summary.Transferring != 1 || len(summary.Queue) != 3 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	var preserved model.Match302BalanceAssignment
	if err := db.First(&preserved, items[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if preserved.TargetFileID != "preserved" || preserved.Status != model.BalanceAssignmentStatusFailed || !preserved.UpdatedAt.Equal(old) {
		t.Fatalf("assignment changed: %+v", preserved)
	}
	count, err = svc.ClearFailedTransferQueue(context.Background(), []uint{items[0].ID})
	if err != nil || count != 0 {
		t.Fatalf("repeated clear: count=%d err=%v", count, err)
	}

	// 后续转存产生新失败时，旧的清理标记不能隐藏新的错误。
	if err := db.Model(&preserved).UpdateColumn("updated_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if summary := svc.transferSummary(); summary.Failed != 2 {
		t.Fatalf("new failure remained hidden: %+v", summary)
	}
	count, err = svc.ClearFailedTransferQueue(context.Background(), nil)
	if err != nil || count != 2 {
		t.Fatalf("clear all: count=%d err=%v", count, err)
	}

	if summary := svc.transferSummary(); summary.Failed != 0 || len(summary.Queue) != 2 {
		t.Fatalf("cleared records remained visible: %+v", summary)
	}

	var total int64
	db.Model(&model.Match302BalanceAssignment{}).Count(&total)
	if total != int64(len(items)) {
		t.Fatalf("assignments deleted: total=%d", total)
	}
}
