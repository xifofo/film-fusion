package database

import (
	"path/filepath"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSQLitePreservesJournalAndSerializesConcurrentWrites(t *testing.T) {
	t.Setenv("FILMFUSION_SQLITE_JOURNAL_MODE", "")
	t.Setenv("FILMFUSION_SQLITE_BUSY_TIMEOUT_MS", "2500")
	path := filepath.Join(t.TempDir(), "database #1.db")
	seed, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		t.Fatal(err)
	}
	conn, _ := seed.DB()
	conn.Close()
	db, err := openSQLite(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	var mode string
	var busy int
	db.Raw("PRAGMA journal_mode").Scan(&mode)
	db.Raw("PRAGMA busy_timeout").Scan(&busy)
	if mode != "wal" || busy != 2500 || pool.Stats().MaxOpenConnections != 1 {
		t.Fatalf("mode=%s busy=%d pool=%+v", mode, busy, pool.Stats())
	}
	if err := db.Exec("CREATE TABLE counters (value INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO counters VALUES (0)").Error; err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				if err := db.Transaction(func(tx *gorm.DB) error {
					var value int
					if err := tx.Raw("SELECT value FROM counters").Scan(&value).Error; err != nil {
						return err
					}
					return tx.Exec("UPDATE counters SET value = ?", value+1).Error
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	workers.Wait()
	var value int
	if err := db.Raw("SELECT value FROM counters").Scan(&value).Error; err != nil || value != 160 {
		t.Fatalf("value=%d err=%v", value, err)
	}
}

func TestSQLiteExplicitModesKeepFullDurability(t *testing.T) {
	for _, mode := range []string{"WAL", "DELETE"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("FILMFUSION_SQLITE_JOURNAL_MODE", mode)
			db, err := openSQLite(filepath.Join(t.TempDir(), "db.sqlite"), nil)
			if err != nil {
				t.Fatal(err)
			}
			pool, _ := db.DB()
			defer pool.Close()
			var synchronous int
			if err := db.Raw("PRAGMA synchronous").Scan(&synchronous).Error; err != nil || synchronous != 2 {
				t.Fatalf("synchronous=%d err=%v", synchronous, err)
			}
		})
	}
}

func TestSQLiteRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("FILMFUSION_SQLITE_JOURNAL_MODE", "OFF")
	if _, err := openSQLite(filepath.Join(t.TempDir(), "db"), nil); err == nil {
		t.Fatal("unsafe journal mode accepted")
	}
	t.Setenv("FILMFUSION_SQLITE_JOURNAL_MODE", "")
	t.Setenv("FILMFUSION_SQLITE_BUSY_TIMEOUT_MS", "bad")
	if _, err := openSQLite(filepath.Join(t.TempDir(), "db"), nil); err == nil {
		t.Fatal("invalid wait accepted")
	}
}
