package model

import (
	"path/filepath"
	"reflect"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

func TestNormalizeMatch302AccessMode(t *testing.T) {
	tests := map[string]string{
		"":              Match302AccessModeAuto,
		"AUTO":          Match302AccessModeAuto,
		"openapi_only":  Match302AccessModeOpenAPIOnly,
		" cookie_only ": Match302AccessModeCookieOnly,
		"unknown":       Match302AccessModeAuto,
	}
	for input, want := range tests {
		if got := NormalizeMatch302AccessMode(input); got != want {
			t.Fatalf("NormalizeMatch302AccessMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCloudStorageMigrationDefaultsExistingRowsToAuto(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Silent),
	})
	if err != nil {
		t.Fatalf("open migration database: %v", err)
	}
	if err := db.AutoMigrate(&CloudStorage{}); err != nil {
		t.Fatalf("create current table: %v", err)
	}
	if err := db.Migrator().DropColumn(&CloudStorage{}, "match302_access_mode"); err != nil {
		t.Fatalf("remove access mode from legacy table: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO cloud_storages (id, user_id, storage_type, storage_name)
		VALUES (1, 7, '115open', 'legacy')
	`).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := db.AutoMigrate(&CloudStorage{}); err != nil {
		t.Fatalf("migrate CloudStorage: %v", err)
	}

	var storage CloudStorage
	if err := db.First(&storage, 1).Error; err != nil {
		t.Fatalf("reload legacy row: %v", err)
	}
	if storage.Match302AccessMode != Match302AccessModeAuto {
		t.Fatalf("migrated access mode=%q", storage.Match302AccessMode)
	}
}

func TestMatch302AccessOrder(t *testing.T) {
	tests := map[string][]string{
		Match302AccessModeAuto:        {Match302AccessMethodOpenAPI, Match302AccessMethodCookie},
		Match302AccessModeOpenAPIOnly: {Match302AccessMethodOpenAPI},
		Match302AccessModeCookieOnly:  {Match302AccessMethodCookie},
	}
	for mode, want := range tests {
		if got := Match302AccessOrder(mode); !reflect.DeepEqual(got, want) {
			t.Fatalf("Match302AccessOrder(%q) = %#v, want %#v", mode, got, want)
		}
	}
}

func TestCloudStorageHasMatch302AccessCredential(t *testing.T) {
	storage := CloudStorage{AccessToken: "token", Cookie: "cookie"}
	for _, mode := range []string{
		Match302AccessModeAuto,
		Match302AccessModeOpenAPIOnly,
		Match302AccessModeCookieOnly,
	} {
		storage.Match302AccessMode = mode
		if !storage.HasMatch302AccessCredential() {
			t.Fatalf("mode %q should accept its configured credential", mode)
		}
	}

	storage.Match302AccessMode = Match302AccessModeCookieOnly
	storage.Cookie = ""
	if storage.HasMatch302AccessCredential() {
		t.Fatal("cookie-only storage must not fall back to AccessToken")
	}
	storage.Match302AccessMode = Match302AccessModeOpenAPIOnly
	storage.Cookie = "cookie"
	storage.AccessToken = ""
	if storage.HasMatch302AccessCredential() {
		t.Fatal("OpenAPI-only storage must not fall back to Cookie")
	}
}
