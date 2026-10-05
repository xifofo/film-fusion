package database

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/logger"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// One connection serializes writers inside this process and avoids deferred
// read/write transactions racing to upgrade SQLite locks. All transactions must
// continue to use their supplied tx handle, never acquire a second connection.
// Keep the database's journal mode unless the operator explicitly selects one:
// existing installations can place the database on network filesystems.
func openSQLite(path string, log *logger.Logger) (*gorm.DB, error) {
	busyMS := 5000
	if raw := strings.TrimSpace(os.Getenv("FILMFUSION_SQLITE_BUSY_TIMEOUT_MS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 60000 {
			return nil, fmt.Errorf("FILMFUSION_SQLITE_BUSY_TIMEOUT_MS must be between 1 and 60000")
		}
		busyMS = value
	}
	query := url.Values{"_busy_timeout": {strconv.Itoa(busyMS)}}
	mode := strings.ToUpper(strings.TrimSpace(os.Getenv("FILMFUSION_SQLITE_JOURNAL_MODE")))
	if mode != "" {
		if mode != "WAL" && mode != "DELETE" {
			return nil, fmt.Errorf("FILMFUSION_SQLITE_JOURNAL_MODE must be WAL or DELETE")
		}
		query.Set("_journal_mode", mode)
		// WAL must not silently lower the previous FULL durability guarantee.
		query.Set("_synchronous", "FULL")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: query.Encode()}).String()
	queryLog := newQueryLogger(log, 500*time.Millisecond)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: queryLog})
	if err != nil {
		return nil, err
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	queryLog.pool = pool
	var journal string
	if err := db.Raw("PRAGMA journal_mode").Scan(&journal).Error; err != nil {
		_ = pool.Close()
		return nil, err
	}
	if log != nil {
		log.Infof("SQLite initialized: journal_mode=%s max_open_conns=1 busy_timeout_ms=%d slow_query_ms=500", journal, busyMS)
	}
	return db, nil
}
