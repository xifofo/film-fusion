package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newStorageOrderDB(t *testing.T) (*gorm.DB, []model.CloudStorage) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "order.db")+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []model.CloudStorage{
		{UserID: 7, StorageName: "A", SortOrder: 10, CreatedAt: created, AccessToken: "keep-a", Cookie: "keep-cookie"},
		{UserID: 7, StorageName: "B", SortOrder: 10, CreatedAt: created.Add(time.Second)},
		{UserID: 7, StorageName: "C", SortOrder: 30, CreatedAt: created},
		{UserID: 8, StorageName: "Other owner", SortOrder: 99, CreatedAt: created},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	return db, rows
}

func storageOrderSnapshot(t *testing.T, db *gorm.DB) []StorageOrderVersion {
	t.Helper()
	var rows []model.CloudStorage
	if err := db.Where("user_id = ?", 7).Order("sort_order ASC, created_at DESC, id DESC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	result := make([]StorageOrderVersion, len(rows))
	for index, row := range rows {
		result[index] = StorageOrderVersion{ID: row.ID, SortOrder: row.SortOrder}
	}
	return result
}

func TestCloudStorageOrderSaveAndUndo(t *testing.T) {
	db, rows := newStorageOrderDB(t)
	original := storageOrderSnapshot(t, db)
	order := []uint{rows[2].ID, rows[1].ID, rows[0].ID}
	result, err := ReorderCloudStorages(t.Context(), db, 7, StorageOrderInput{Expected: original, Order: order})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(order) {
		t.Fatalf("unexpected result count: %d", len(result))
	}
	expected := make([]StorageOrderVersion, len(result))
	for index, row := range result {
		if row.ID != order[index] || row.SortOrder != index+1 || row.UpdatedAt.IsZero() {
			t.Fatalf("invalid saved order: %+v", row)
		}
		expected[index] = row.StorageOrderVersion
	}
	var stored, foreign model.CloudStorage
	db.First(&stored, rows[0].ID)
	db.First(&foreign, rows[3].ID)
	if stored.AccessToken != "keep-a" || stored.Cookie != "keep-cookie" || foreign.SortOrder != 99 {
		t.Fatal("sorting changed credentials or another owner's record")
	}

	before := []uint{original[0].ID, original[1].ID, original[2].ID}
	if _, err := ReorderCloudStorages(t.Context(), db, 7, StorageOrderInput{Expected: expected, Order: before}); err != nil {
		t.Fatal(err)
	}
	restored := storageOrderSnapshot(t, db)
	for index, row := range restored {
		if row.ID != before[index] {
			t.Fatalf("undo did not restore order: %+v", restored)
		}
	}
}

func TestCloudStorageOrderRejectsInvalidOrStaleScope(t *testing.T) {
	for _, kind := range []string{"duplicate", "missing", "foreign", "stale", "deleted", "added"} {
		t.Run(kind, func(t *testing.T) {
			db, rows := newStorageOrderDB(t)
			input := StorageOrderInput{
				Expected: storageOrderSnapshot(t, db),
				Order:    []uint{rows[2].ID, rows[1].ID, rows[0].ID},
			}
			want := ErrStorageOrderChanged
			switch kind {
			case "duplicate":
				input.Order[1] = input.Order[0]
				want = ErrStorageOrderInvalid
			case "missing":
				input.Expected = input.Expected[:2]
				input.Order = []uint{rows[0].ID, rows[1].ID}
			case "foreign":
				input.Expected[2].ID = rows[3].ID
				input.Order[0] = rows[3].ID
			case "stale":
				db.Model(&model.CloudStorage{}).Where("id = ?", rows[0].ID).Update("sort_order", 5)
			case "deleted":
				db.Delete(&model.CloudStorage{}, rows[0].ID)
			case "added":
				db.Create(&model.CloudStorage{UserID: 7, StorageName: "new"})
			}
			before := storageOrderSnapshot(t, db)
			_, err := ReorderCloudStorages(t.Context(), db, 7, input)
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if !reflect.DeepEqual(before, storageOrderSnapshot(t, db)) {
				t.Fatal("rejected reorder changed records")
			}
		})
	}
}

func TestCloudStorageOrderRollsBackPartialFailure(t *testing.T) {
	db, rows := newStorageOrderDB(t)
	before := storageOrderSnapshot(t, db)
	// 第二次写入失败时，第一次已经完成的 UPDATE 也必须回滚。
	trigger := fmt.Sprintf("CREATE TRIGGER reject_order BEFORE UPDATE OF sort_order ON cloud_storages WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'reject'); END", rows[1].ID)
	if err := db.Exec(trigger).Error; err != nil {
		t.Fatal(err)
	}
	_, err := ReorderCloudStorages(t.Context(), db, 7, StorageOrderInput{
		Expected: before, Order: []uint{rows[2].ID, rows[1].ID, rows[0].ID},
	})
	if err == nil || !reflect.DeepEqual(before, storageOrderSnapshot(t, db)) {
		t.Fatal("failed reorder must roll back every update")
	}
}

func TestCloudStorageOrderConcurrentWriters(t *testing.T) {
	db, rows := newStorageOrderDB(t)
	expected := storageOrderSnapshot(t, db)
	orders := [][]uint{{rows[2].ID, rows[0].ID, rows[1].ID}, {rows[0].ID, rows[2].ID, rows[1].ID}}
	start := make(chan struct{})
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for _, order := range orders {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			_, err := ReorderCloudStorages(context.Background(), db, 7, StorageOrderInput{Expected: expected, Order: order})
			results <- err
		}()
	}
	close(start)
	writers.Wait()
	close(results)

	saved, conflicts := 0, 0
	for err := range results {
		if err == nil {
			saved++
		} else if errors.Is(err, ErrStorageOrderChanged) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if saved != 1 || conflicts != 1 {
		t.Fatalf("expected one save and one conflict; saved=%d conflicts=%d", saved, conflicts)
	}
}
