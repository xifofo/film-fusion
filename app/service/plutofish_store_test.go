package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

func openPlutoFishTestDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	if err := db.AutoMigrate(&model.PlutoFishEvent{}, &model.PlutoFishCursor{}, &model.PlutoFishReceipt{}, &model.PlutoFishBusinessPlan{}, &model.SystemConfig{}, &model.CloudStorage{}, &model.CloudPath{}); err != nil {
		t.Fatal(err)
	}
	return db
}
func pfTestEvent(id, transfer, typ string) plutofish.Event {
	return plutofish.Event{ID: id, TransferID: transfer, Type: "transfer." + typ, Status: typ, SchemaVersion: 1, WorkTitle: "测试电影", FileName: "电影.mkv", OccurredAt: "2026-10-20T00:00:00Z", SizeBytes: 9223372036854775807}
}

func TestPlutoFishInboxRestartDedupeAndFailureThenSuccess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := openPlutoFishTestDB(t, path)
	store := NewPlutoFishStore(db, "https://plutofish.shop", "owner-a")
	failure, success := pfTestEvent("event-f", "transfer-1", "failed"), pfTestEvent("event-s", "transfer-1", "succeeded")
	if err := store.Commit(ctx, []plutofish.Event{failure}, "opaque cursor 1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, []plutofish.Event{success, success}, "opaque cursor 2"); err != nil {
		t.Fatal(err)
	}
	var rows []model.PlutoFishEvent
	db.Order("id").Find(&rows)
	if len(rows) != 2 || rows[0].State != "recorded" || rows[1].State != "pending" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	pool, _ := db.DB()
	pool.Close()
	db = openPlutoFishTestDB(t, path)
	store = NewPlutoFishStore(db, "https://plutofish.shop", "owner-a")
	if cursor, _ := store.Cursor(ctx); cursor != "opaque cursor 2" {
		t.Fatalf("lost cursor %q", cursor)
	}
	calls := 0
	handle := func(context.Context, string, plutofish.Event) (PlutoFishBusinessResult, error) {
		calls++
		return PlutoFishBusinessResult{LocalPath: "/test/file.strm", Message: "完成"}, nil
	}
	if ok, err := store.processOne(ctx, handle); !ok || err != nil {
		t.Fatal(ok, err)
	}
	success.ID = "event-second-success"
	if err := store.Commit(ctx, []plutofish.Event{success}, "cursor 3"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.processOne(ctx, handle); ok || err != nil {
		t.Fatal(ok, err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	var duplicate model.PlutoFishEvent
	db.Where("event_id = ?", success.ID).Take(&duplicate)
	if duplicate.State != "duplicate" {
		t.Fatal(duplicate.State)
	}
	if cursor, _ := NewPlutoFishStore(db, "https://plutofish.shop", "owner-b").Cursor(ctx); cursor != "" {
		t.Fatal("cross-user cursor reuse")
	}
	if cursor, _ := NewPlutoFishStore(db, "https://other.example", "owner-a").Cursor(ctx); cursor != "" {
		t.Fatal("cross-server cursor reuse")
	}
}

func TestPlutoFishInboxPageRollbackAndEmptyPageCursor(t *testing.T) {
	ctx := context.Background()
	db := openPlutoFishTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	store := NewPlutoFishStore(db, "https://plutofish.shop", "a")
	if err := store.Commit(ctx, nil, "before"); err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TRIGGER reject_second BEFORE INSERT ON pluto_fish_events WHEN NEW.event_id = 'bad' BEGIN SELECT RAISE(ABORT, 'test'); END")
	if err := store.Commit(ctx, []plutofish.Event{pfTestEvent("ok", "t1", "succeeded"), pfTestEvent("bad", "t2", "succeeded")}, "after"); err == nil {
		t.Fatal("expected transaction failure")
	}
	if cursor, _ := store.Cursor(ctx); cursor != "before" {
		t.Fatal("skipped failed page", cursor)
	}
	var n int64
	db.Model(&model.PlutoFishEvent{}).Count(&n)
	if n != 0 {
		t.Fatal("partially committed page", n)
	}
	if err := store.Commit(ctx, nil, "empty-scan-progress"); err != nil {
		t.Fatal(err)
	}
	if cursor, _ := store.Cursor(ctx); cursor != "empty-scan-progress" {
		t.Fatal(cursor)
	}
}

func TestPlutoFishReviewAndSideEffectRecovery(t *testing.T) {
	ctx := context.Background()
	db := openPlutoFishTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	store := NewPlutoFishStore(db, "https://plutofish.shop", "a")
	event := pfTestEvent("missing", "task", "succeeded")
	if err := store.Commit(ctx, []plutofish.Event{event}, "committed-durable"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.processOne(ctx, func(context.Context, string, plutofish.Event) (PlutoFishBusinessResult, error) {
		return PlutoFishBusinessResult{}, errors.New("缺少文件定位，保留待处理")
	}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	var row model.PlutoFishEvent
	db.First(&row)
	if row.State != "review" {
		t.Fatal(row.State)
	}
	var n int64
	db.Model(&model.PlutoFishReceipt{}).Count(&n)
	if n != 0 {
		t.Fatal("review incorrectly consumed transfer")
	}
	db.Model(&row).Update("state", "pending")
	db.Exec("CREATE TRIGGER fail_receipt BEFORE INSERT ON pluto_fish_receipts BEGIN SELECT RAISE(ABORT, 'test'); END")
	calls := 0
	handle := func(context.Context, string, plutofish.Event) (PlutoFishBusinessResult, error) {
		calls++
		return PlutoFishBusinessResult{LocalPath: "same-idempotent-path"}, nil
	}
	if _, err := store.processOne(ctx, handle); err == nil {
		t.Fatal("expected receipt failure")
	}
	db.First(&row)
	if row.State != "pending" {
		t.Fatal("lost pending side effect")
	}
	db.Exec("DROP TRIGGER fail_receipt")
	if _, err := store.processOne(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	unknown := pfTestEvent("future", "future-task", "succeeded")
	unknown.SchemaVersion = 2
	unknown.Raw = json.RawMessage(`{"id":"future","schemaVersion":2,"newFutureField":"preserve"}`)
	if err := store.Commit(ctx, []plutofish.Event{unknown}, "future-cursor"); err != nil {
		t.Fatal(err)
	}
	var future model.PlutoFishEvent
	db.Where("event_id = ?", "future").Take(&future)
	if future.State != "review" || !strings.Contains(future.Payload, "newFutureField") {
		t.Fatal("unknown event lost", future)
	}
}

func TestPlutoFishConfigWriteOnlyKeyAndIsolatedOwnerRotation(t *testing.T) {
	ctx := context.Background()
	db := openPlutoFishTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	svc := NewPlutoFishService(db)
	cfg := PlutoFishConfig{BaseURL: "https://plutofish.shop/", OwnerUID: "owner", APIKey: "dummy-test-secret", Mode: "sse", PollSeconds: 15}
	if err := svc.Save(ctx, cfg, 1); err != nil {
		t.Fatal(err)
	}
	var private model.SystemConfig
	db.Where("config_key = ?", plutoFishConfigKey).Take(&private)
	if private.IsVisible {
		t.Fatal("private credentials exposed as visible setting")
	}
	safe, err := svc.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(safe)
	if strings.Contains(string(data), cfg.APIKey) || safe.APIKey != "" || !safe.KeyConfigured {
		t.Fatal("key exposed")
	}
	store := NewPlutoFishStore(db, safe.BaseURL, safe.OwnerUID)
	store.Commit(ctx, nil, "opaque")
	safe.APIKey = "replacement-test-secret"
	if err := svc.Save(ctx, safe, 1); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(ctx)
	if err != nil || status.Cursor != "opaque" {
		t.Fatal(status, err)
	}
	safe.OwnerUID = "other"
	safe.APIKey = ""
	if err := svc.Save(ctx, safe, 1); err == nil {
		t.Fatal("silently reused prior user's key")
	}
	safe.APIKey = "new-owner-test-secret"
	if err := svc.Save(ctx, safe, 1); err != nil {
		t.Fatal(err)
	}
	status, _ = svc.Status(ctx)
	if status.Cursor != "" {
		t.Fatal("cross-user cursor")
	}
	if cursor, _ := store.Cursor(ctx); cursor != "opaque" {
		t.Fatal("old progress cleared")
	}
}
