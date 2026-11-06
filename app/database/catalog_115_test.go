package database

import (
	"film-fusion/app/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
)

func catalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&model.SystemConfig{}, &model.Dictionary{}, &model.DictionaryItem{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func Test115AppCatalogSeedAndNoReseed(t *testing.T) {
	db := catalogTestDB(t)
	if err := Init115AppCatalog(db); err != nil {
		t.Fatal(err)
	}
	items, err := Load115AppCatalog(db)
	if err != nil || len(items) != 1075 || items[0].Value != "100195123" {
		t.Fatalf("seed count=%d, error=%v", len(items), err)
	}
	if err := db.Where("1 = 1").Delete(&model.DictionaryItem{}).Error; err != nil {
		t.Fatal(err)
	}
	// 通用目录已经存在时，即使旧配置残留，也不覆盖管理员的删除和启停。
	if err := db.Create(&model.SystemConfig{ConfigKey: Catalog115Key, ConfigValue: `[{"value":"999","label":"legacy","enabled":true}]`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := Init115AppCatalog(db); err != nil {
		t.Fatal(err)
	}
	items, err = Load115AppCatalog(db)
	if err != nil || len(items) != 0 {
		t.Fatalf("deleted items were restored: %v", err)
	}
	var count int64
	db.Unscoped().Model(&model.SystemConfig{}).Where("config_key = ?", Catalog115Key).Count(&count)
	if count != 0 {
		t.Fatal("legacy config was not retired")
	}
}

func Test115AppCatalogMigratesLegacyAtomically(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		deleted     bool
		count       int
	}{
		{"maintained", `[{"value":"999","label":"自定义应用","enabled":false,"sort_order":-1}]`, false, 1},
		{"empty", `[]`, false, 0},
		{"soft-deleted", `[]`, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := catalogTestDB(t)
			legacy := model.SystemConfig{ConfigKey: Catalog115Key, ConfigValue: tc.value}
			if err := db.Create(&legacy).Error; err != nil {
				t.Fatal(err)
			}
			if tc.deleted {
				if err := db.Delete(&legacy).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := Init115AppCatalog(db); err != nil {
				t.Fatal(err)
			}
			var parent model.Dictionary
			if err := db.Where("code = ?", Catalog115Key).First(&parent).Error; err != nil {
				t.Fatal(err)
			}
			if parent.Enabled == tc.deleted || parent.Version != 1 {
				t.Fatalf("parent state=%+v", parent)
			}
			var items []model.DictionaryItem
			db.Where("dictionary_id = ?", parent.ID).Find(&items)
			if len(items) != tc.count {
				t.Fatalf("count=%d", len(items))
			}
			if len(items) > 0 && (items[0].Label != "自定义应用" || items[0].Enabled || items[0].SortOrder != -1 || items[0].Value != "999") {
				t.Fatalf("lost legacy metadata: %+v", items[0])
			}
			if err := Init115AppCatalog(db); err != nil {
				t.Fatal(err)
			}
			var count int64
			db.Unscoped().Model(&model.SystemConfig{}).Where("config_key = ?", Catalog115Key).Count(&count)
			if count != 0 {
				t.Fatal("legacy config remains")
			}
		})
	}
	db := catalogTestDB(t)
	db.Create(&model.SystemConfig{ConfigKey: Catalog115Key, ConfigValue: `null`})
	if err := Init115AppCatalog(db); err == nil {
		t.Fatal("corrupt data accepted")
	}
	var parents, legacy int64
	db.Model(&model.Dictionary{}).Count(&parents)
	db.Model(&model.SystemConfig{}).Count(&legacy)
	if parents != 0 || legacy != 1 {
		t.Fatal("failed migration must keep legacy data and leave no partial dictionary")
	}
}

func Test115AppCatalogRejectsCorruptData(t *testing.T) {
	for _, value := range []string{`null`, `{}`, `broken`, `[{"value":"001","label":"invalid"}]`, `[{"value":"123","label":"  "}]`, `[{"value":"123","label":"one"},{"value":"123","label":"two"}]`} {
		if _, err := decode115AppCatalog(value); err == nil {
			t.Errorf("accepted invalid directory: %s", value)
		}
	}
	if !Valid115ClientID("12345678901234567890") || Valid115ClientID("123456789012345678901") {
		t.Fatal("client ID length boundary is incorrect")
	}
}
