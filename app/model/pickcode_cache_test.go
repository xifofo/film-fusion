package model

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUpsertPickcodeCacheOverwritesExistingPath(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:pickcode-cache-upsert?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&PickcodeCache{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	first, created, err := UpsertPickcodeCache(db, "/media/movie.mkv", "old-pickcode")
	if err != nil {
		t.Fatalf("insert cache: %v", err)
	}
	if !created {
		t.Fatal("first upsert should create a record")
	}

	updated, created, err := UpsertPickcodeCache(db, "/media/movie.mkv", "new-pickcode")
	if err != nil {
		t.Fatalf("overwrite cache: %v", err)
	}
	if created {
		t.Fatal("second upsert should update the existing record")
	}
	if updated.ID != first.ID {
		t.Fatalf("expected record ID %d to remain unchanged, got %d", first.ID, updated.ID)
	}
	if updated.Pickcode != "new-pickcode" {
		t.Fatalf("expected overwritten pickcode, got %q", updated.Pickcode)
	}
}
