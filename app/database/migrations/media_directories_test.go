package migrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"film-fusion/app/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func migrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func seedLegacyDirectories(t *testing.T, db *gorm.DB) (legacyDirectory, legacyPath) {
	t.Helper()
	must(t, db.Table("cloud_directories").AutoMigrate(&legacyDirectory{}))
	must(t, db.Table("cloud_paths").AutoMigrate(&legacyPath{}))
	must(t, db.AutoMigrate(&model.SystemConfig{}, &model.EmbyVersionCheckSetting{}, &model.OrganizeLog{}, &model.OrganizePlan{}, &model.OrganizeUpgradeJob{}))
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	dir := legacyDirectory{ID: 7, UserID: 11, CloudStorageID: 3, DirectoryName: "影片", DirectoryID: "1234567890123456789", SavePath: "/library", ContentPrefix: "https://play.example", ContentEncodeURI: true, IncludeExtensions: `["mkv"]`, ExcludeExtensions: `["txt"]`, ExcludeSmallerThanMB: 123, ClassifyByCategory: false, RecognitionSource: "medianame", UpgradePolicy: `{"enabled":false}`, CreatedAt: now, UpdatedAt: now}
	path := legacyPath{ID: 7, UserID: 11, CloudStorageID: 3, SourcePath: "/CD2/影片", SourceType: "clouddrive2", LocalPath: "/mapped", ContentPrefix: "https://other.example", ContentEncodeURI: false, EmbyPathPrefix: "/emby", LinkType: "strm", FilterRules: `{"include":["mkv"],"download":["srt"]}`, StrmContentType: "openlist", CreatedAt: now, UpdatedAt: now}
	must(t, db.Table("cloud_directories").Create(&dir).Error)
	must(t, db.Table("cloud_paths").Create(&path).Error)
	// Soft-deleted configurations still own historical references and must retain their state.
	deleted := path
	deleted.ID = 9
	deleted.DeletedAt = gorm.DeletedAt{Time: now, Valid: true}
	must(t, db.Table("cloud_paths").Create(&deleted).Error)
	cfg := model.SystemConfig{ConfigKey: "plutofish.settings", ConfigValue: `{"userID":11,"extra":1234567890123456789,"config":{"enabled":true,"apiKey":"test-secret","mappings":[{"targetAccountID":"account","cloudPathID":7}]}}`}
	must(t, db.Create(&cfg).Error)
	must(t, db.Create(&model.EmbyVersionCheckSetting{UserID: 11, CloudPathIDs: []uint{7, 9}, ScheduleEnabled: true, Cron: "0 0 * * *"}).Error)
	must(t, db.Create(&model.OrganizeLog{CloudPathID: 7, Action: "strm_create"}).Error)
	// Exact legacy serialization including zero timestamps is used by immutable plan comparison.
	snapshot := dir
	snapshot.CreatedAt = time.Time{}
	snapshot.UpdatedAt = time.Time{}
	data, _ := json.Marshal(snapshot)
	must(t, db.Create(&model.OrganizePlan{ID: "pending-plan", UserID: 11, CloudDirectoryID: 7, DirectoryJSON: string(data), RequestJSON: `{"cloud_directory_id":7}`, ResultJSON: `{"cloud_directory_id":7}`, Status: "ready"}).Error)
	job, _ := json.Marshal(map[string]any{"directory": dir, "other": json.RawMessage(`1234567890123456789`)})
	must(t, db.Create(&model.OrganizeUpgradeJob{ID: "pending-upgrade", CloudDirectoryID: 7, PayloadJSON: string(job), Status: "failed"}).Error)
	return dir, path
}

func TestMergeMediaDirectoriesPreservesConfigurationsReferencesAndRetry(t *testing.T) {
	db := migrationDB(t)
	dir, path := seedLegacyDirectories(t, db)
	must(t, MergeMediaDirectories(db))
	if db.Migrator().HasTable("cloud_paths") || db.Migrator().HasTable("cloud_directories") {
		t.Fatal("legacy business tables remain")
	}
	var rows []model.MediaDirectory
	must(t, db.Unscoped().Order("id").Find(&rows).Error)
	if len(rows) != 3 {
		t.Fatalf("rows=%d", len(rows))
	}
	got := rows[0]
	if got.ID != dir.ID || !got.OrganizeEnabled || got.MappingEnabled || got.DirectoryID != dir.DirectoryID || got.SavePath != dir.SavePath || got.ContentPrefix != dir.ContentPrefix || got.ContentEncodeURI != dir.ContentEncodeURI || got.IncludeExtensions != dir.IncludeExtensions || got.ExcludeExtensions != dir.ExcludeExtensions || got.ExcludeSmallerThanMB != dir.ExcludeSmallerThanMB || got.ClassifyByCategory != dir.ClassifyByCategory || got.RecognitionSource != dir.RecognitionSource || got.UpgradePolicy != dir.UpgradePolicy || !got.CreatedAt.Equal(dir.CreatedAt) {
		t.Fatalf("organizing configuration changed: %+v", got)
	}
	mapped := rows[1]
	if mapped.ID != 14 || !mapped.MappingEnabled || mapped.OrganizeEnabled || mapped.SavePath != path.LocalPath || mapped.SourcePath != path.SourcePath || mapped.SourceType != path.SourceType || mapped.FilterRules != path.FilterRules || mapped.EmbyPathPrefix != path.EmbyPathPrefix || mapped.StrmContentType != path.StrmContentType || mapped.ContentPrefix != path.ContentPrefix || !mapped.UpdatedAt.Equal(path.UpdatedAt) {
		t.Fatalf("mapping changed: %+v", mapped)
	}
	if rows[2].ID != 16 || !rows[2].DeletedAt.Valid {
		t.Fatal("soft delete lost")
	}
	var setting model.EmbyVersionCheckSetting
	must(t, db.First(&setting).Error)
	if !reflect.DeepEqual(setting.CloudPathIDs, []uint{14, 16}) || !setting.ScheduleEnabled {
		t.Fatal("Emby references changed incorrectly")
	}
	var log model.OrganizeLog
	must(t, db.First(&log).Error)
	if log.CloudPathID != 14 {
		t.Fatal("log reference not migrated")
	}
	var cfg model.SystemConfig
	must(t, db.First(&cfg).Error)
	var saved struct {
		Extra  json.Number
		Config struct {
			APIKey   string
			Mappings []struct{ CloudPathID uint }
		}
	}
	must(t, json.Unmarshal([]byte(cfg.ConfigValue), &saved))
	if saved.Extra != "1234567890123456789" || saved.Config.APIKey != "test-secret" || saved.Config.Mappings[0].CloudPathID != 14 {
		t.Fatal("PlutoFish configuration not preserved")
	}
	var plan model.OrganizePlan
	must(t, db.First(&plan).Error)
	var snapshot model.MediaDirectory
	must(t, json.Unmarshal([]byte(plan.DirectoryJSON), &snapshot))
	expected := got
	expected.CreatedAt = time.Time{}
	expected.UpdatedAt = time.Time{}
	expectedJSON, _ := json.Marshal(expected)
	if plan.DirectoryJSON != string(expectedJSON) || plan.RequestJSON != `{"cloud_directory_id":7}` || plan.CloudDirectoryID != 7 {
		t.Fatal("pending plan is no longer usable")
	}
	var job model.OrganizeUpgradeJob
	must(t, db.First(&job).Error)
	var payload struct {
		Directory model.MediaDirectory
		Other     json.Number
	}
	must(t, json.Unmarshal([]byte(job.PayloadJSON), &payload))
	if !payload.Directory.OrganizeEnabled || payload.Directory.ID != 7 || payload.Other != "1234567890123456789" {
		t.Fatal("recovery snapshot changed")
	}
	var marker schemaMigration
	must(t, db.First(&marker).Error)
	stat, err := os.Stat(marker.BackupPath)
	must(t, err)
	if stat.Mode().Perm() != 0600 {
		t.Fatal("backup must be private")
	}
	backup, err := gorm.Open(sqlite.Open(marker.BackupPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	must(t, err)
	pool, _ := backup.DB()
	defer pool.Close()
	if !backup.Migrator().HasTable("cloud_paths") || !backup.Migrator().HasTable("cloud_directories") {
		t.Fatal("backup missing original schema")
	}
	must(t, MergeMediaDirectories(db))
	var after []model.MediaDirectory
	must(t, db.Unscoped().Order("id").Find(&after).Error)
	if !reflect.DeepEqual(rows, after) {
		t.Fatal("second startup changed data")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(marker.BackupPath), "*.db"))
	must(t, err)
	if len(files) != 1 {
		t.Fatal("repeated startup created extra backup")
	}
}

func TestMergeMediaDirectoriesRollsBackInvalidReferences(t *testing.T) {
	db := migrationDB(t)
	seedLegacyDirectories(t, db)
	must(t, db.Model(&model.SystemConfig{}).Where("config_key = ?", "plutofish.settings").Update("config_value", `{"config":{"mappings":[{"cloudPathID":999}]}}`).Error)
	if err := MergeMediaDirectories(db); err == nil {
		t.Fatal("dangling reference accepted")
	}
	if !db.Migrator().HasTable("cloud_paths") || !db.Migrator().HasTable("cloud_directories") {
		t.Fatal("failed migration dropped data")
	}
	var count int64
	must(t, db.Model(&schemaMigration{}).Count(&count).Error)
	if count != 0 {
		t.Fatal("failed migration marked complete")
	}
	var setting model.EmbyVersionCheckSetting
	must(t, db.First(&setting).Error)
	if !reflect.DeepEqual(setting.CloudPathIDs, []uint{7, 9}) {
		t.Fatal("partial reference update committed")
	}
	if db.Migrator().HasTable("media_directories") {
		t.Fatal("partial schema committed")
	}
	must(t, db.Model(&model.SystemConfig{}).Where("config_key = ?", "plutofish.settings").Update("config_value", `{"config":{"mappings":[{"cloudPathID":7}]}}`).Error)
	must(t, MergeMediaDirectories(db))
}

func TestMergeMediaDirectoriesFreshInstall(t *testing.T) {
	db := migrationDB(t)
	must(t, MergeMediaDirectories(db))
	must(t, MergeMediaDirectories(db))
	var marker schemaMigration
	must(t, db.First(&marker).Error)
	if marker.BackupPath != "" {
		t.Fatal("empty installation backed up unnecessarily")
	}
	var count int64
	must(t, db.Model(&model.MediaDirectory{}).Count(&count).Error)
	if count != 0 {
		t.Fatal("empty installation seeded data")
	}
}

func TestMergeMediaDirectoriesPreservesNullAndRetiresKnownUnusedColumn(t *testing.T) {
	db := migrationDB(t)
	seedLegacyDirectories(t, db)
	must(t, db.Exec("ALTER TABLE cloud_paths ADD COLUMN is_windows_path numeric").Error)
	must(t, db.Exec("UPDATE cloud_paths SET is_windows_path = 1").Error)
	must(t, db.Exec("UPDATE cloud_directories SET upgrade_policy = NULL, recognition_source = NULL, classify_by_category = 0").Error)
	must(t, MergeMediaDirectories(db))
	// 在 SQL 层比较原值，避免驱动将布尔列的 0 扫描为 Go bool 后产生类型误判。
	var preserved int64
	must(t, db.Table("media_directories").Where("id = 7 AND upgrade_policy IS NULL AND recognition_source IS NULL AND classify_by_category = 0").Count(&preserved).Error)
	if preserved != 1 {
		t.Fatal("original SQL values were normalized")
	}
	if db.Migrator().HasColumn(&model.MediaDirectory{}, "is_windows_path") {
		t.Fatal("retired business field reintroduced")
	}
	var marker schemaMigration
	must(t, db.First(&marker).Error)
	backup, err := gorm.Open(sqlite.Open(marker.BackupPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	must(t, err)
	pool, _ := backup.DB()
	defer pool.Close()
	var value int
	must(t, backup.Raw("SELECT is_windows_path FROM cloud_paths WHERE id = 7").Scan(&value).Error)
	if value != 1 {
		t.Fatal("backup did not preserve retired setting")
	}
}

func TestMergeMediaDirectoriesUnknownColumnBlocksDestructiveCleanup(t *testing.T) {
	db := migrationDB(t)
	seedLegacyDirectories(t, db)
	must(t, db.Exec("ALTER TABLE cloud_paths ADD COLUMN future_setting TEXT").Error)
	must(t, db.Exec("UPDATE cloud_paths SET future_setting = 'preserve'").Error)
	if err := MergeMediaDirectories(db); err == nil {
		t.Fatal("unknown data was silently dropped")
	}
	if !db.Migrator().HasTable("cloud_paths") {
		t.Fatal("original table removed")
	}
	var value string
	must(t, db.Raw("SELECT future_setting FROM cloud_paths WHERE id = 7").Scan(&value).Error)
	if value != "preserve" {
		t.Fatal("original future setting changed")
	}
}

func TestMergeMediaDirectoriesReservesHistoricalIDsWithoutConfigurations(t *testing.T) {
	db := migrationDB(t)
	must(t, db.Table("cloud_paths").AutoMigrate(&legacyPath{}))
	must(t, db.AutoMigrate(&model.OrganizeLog{}))
	must(t, db.Create(&model.OrganizeLog{CloudPathID: 300, Action: "strm_create"}).Error)
	must(t, MergeMediaDirectories(db))
	row := model.MediaDirectory{DirectoryName: "New", MappingEnabled: true}
	must(t, db.Create(&row).Error)
	if row.ID <= 300 {
		t.Fatalf("new directory ID %d can collide with historical reference", row.ID)
	}
}
