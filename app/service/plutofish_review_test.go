package service

import (
	"context"
	"path/filepath"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/gorm"
)

func plutoFishReviewFixture(t *testing.T) (*PlutoFishService, *PlutoFishStore, *gorm.DB) {
	t.Helper()
	db := openPlutoFishTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	svc := NewPlutoFishService(db)
	cfg := PlutoFishConfig{BaseURL: "https://plutofish.shop", OwnerUID: "review-owner", APIKey: "local-test-key", Mode: "sse", PollSeconds: 15}
	if err := svc.Save(context.Background(), cfg, 7); err != nil {
		t.Fatal(err)
	}
	return svc, NewPlutoFishStore(db, cfg.BaseURL, cfg.OwnerUID), db
}

func TestPlutoFishReviewClosesReminderPreservesHistoryAndLaterSuccess(t *testing.T) {
	ctx := context.Background()
	svc, store, db := plutoFishReviewFixture(t)
	unknown := pfTestEvent("unknown-event", "same-transfer", "unknown")
	if err := store.Commit(ctx, []plutofish.Event{unknown}, "opaque-committed"); err != nil {
		t.Fatal(err)
	}
	var row model.PlutoFishEvent
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	before := row
	status, err := svc.Status(ctx)
	if err != nil || status.ReviewCount != 1 {
		t.Fatal(status, err)
	}
	if err := svc.Review(ctx, row.ID, 7); err != nil {
		t.Fatal(err)
	}
	status, err = svc.Status(ctx)
	if err != nil || status.ReviewCount != 0 || status.Cursor != "opaque-committed" {
		t.Fatal(status, err)
	}
	db.First(&row, row.ID)
	if row.State != "reviewed" || row.ReviewedBy != 7 || row.ReviewedAt == nil || row.Status != "unknown" || row.Type != before.Type || row.Reason != before.Reason || row.Payload != before.Payload {
		t.Fatalf("review modified outcome/history or lost audit: %+v", row)
	}
	firstReviewedAt := *row.ReviewedAt
	if err := svc.Review(ctx, row.ID, 7); err != nil {
		t.Fatal(err)
	}
	db.First(&row, row.ID)
	if !row.ReviewedAt.Equal(firstReviewedAt) {
		t.Fatal("repeat confirmation overwrote audit timestamp")
	}
	for _, table := range []any{&model.PlutoFishReceipt{}, &model.PlutoFishBusinessPlan{}} {
		var count int64
		db.Model(table).Count(&count)
		if count != 0 {
			t.Fatal("manual review claimed a successful business side effect")
		}
	}
	if err := store.Commit(ctx, []plutofish.Event{unknown}, "opaque-committed"); err != nil {
		t.Fatal(err)
	}
	status, _ = svc.Status(ctx)
	if status.ReviewCount != 0 {
		t.Fatal("duplicate delivery reopened a reviewed reminder")
	}
	success := pfTestEvent("success-event", unknown.TransferID, "succeeded")
	if err := store.Commit(ctx, []plutofish.Event{success}, "opaque-next"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	processed, err := store.processOne(ctx, func(context.Context, string, plutofish.Event) (PlutoFishBusinessResult, error) {
		calls++
		return PlutoFishBusinessResult{Message: "local test callback"}, nil
	})
	if err != nil || !processed || calls != 1 {
		t.Fatal("review suppressed a later successful event", processed, err, calls)
	}
	db.First(&row, row.ID)
	if row.State != "reviewed" || row.Status != "unknown" {
		t.Fatal("later success rewrote the original unknown history")
	}
}

func TestPlutoFishReviewScopeAndStateRestrictions(t *testing.T) {
	ctx := context.Background()
	svc, store, db := plutoFishReviewFixture(t)
	for _, scope := range []string{store.scope, plutoFishScope("https://other.example", "review-owner"), plutoFishScope("https://plutofish.shop", "other-owner")} {
		row := model.PlutoFishEvent{Scope: scope, EventID: "event", State: "review", Status: "unknown"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if scope != store.scope && svc.Review(ctx, row.ID, 7) == nil {
			t.Fatal("reviewed another service/user's event")
		}
		if scope == store.scope {
			for _, actor := range []uint{0, 8} {
				if err := svc.Review(ctx, row.ID, actor); err == nil {
					t.Fatal("another local administrator changed review state")
				}
			}
		}
	}
	for _, state := range []string{"pending", "completed", "recorded", "duplicate"} {
		row := model.PlutoFishEvent{Scope: store.scope, EventID: state, State: state}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.Review(ctx, row.ID, 7); err == nil {
			t.Fatalf("unexpected review of %s", state)
		}
		db.First(&row, row.ID)
		if row.State != state || row.ReviewedAt != nil {
			t.Fatal("rejected request changed event")
		}
	}
	if err := svc.Review(ctx, 999999, 7); err == nil {
		t.Fatal("missing event accepted")
	}
}

func TestPlutoFishReviewFutureSchemaAndWriteFailure(t *testing.T) {
	ctx := context.Background()
	svc, store, db := plutoFishReviewFixture(t)
	future := pfTestEvent("future-event", "future-transfer", "succeeded")
	future.SchemaVersion = 2
	if err := store.Commit(ctx, []plutofish.Event{future}, "future-cursor"); err != nil {
		t.Fatal(err)
	}
	var row model.PlutoFishEvent
	db.First(&row)
	if err := db.Exec("CREATE TRIGGER reject_review BEFORE UPDATE ON pluto_fish_events WHEN NEW.state = 'reviewed' BEGIN SELECT RAISE(ABORT, 'test'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(ctx, row.ID, 7); err == nil {
		t.Fatal("failed write reported success")
	}
	db.First(&row, row.ID)
	if row.State != "review" || row.ReviewedAt != nil || row.ReviewedBy != 0 {
		t.Fatal("failed write changed state/audit")
	}
	db.Exec("DROP TRIGGER reject_review")
	if err := svc.Review(ctx, row.ID, 7); err != nil {
		t.Fatal("future schema could not be manually acknowledged", err)
	}
	db.First(&row, row.ID)
	if row.State != "reviewed" || row.SchemaVersion != 2 {
		t.Fatal("schema or review state corrupted")
	}
}
