package database

import (
	"fmt"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type legacyAutomationWorkflow struct {
	ID             uint   `gorm:"primarykey"`
	SourceID       uint   `gorm:"not null;default:0;index"`
	Name           string `gorm:"size:120;not null"`
	Description    string `gorm:"type:text"`
	Enabled        bool   `gorm:"not null;default:false;index"`
	Version        int    `gorm:"not null;default:1"`
	DefinitionJSON string `gorm:"type:text;not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (legacyAutomationWorkflow) TableName() string {
	return "automation_workflows"
}

func TestAutomationOneToOneMigrationAddsConstraints(t *testing.T) {
	dsn := fmt.Sprintf("file:automations-migration-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.AutomationSource{}, &legacyAutomationWorkflow{}); err != nil {
		t.Fatal(err)
	}
	source := model.AutomationSource{
		Name: "115 目录更新", TriggerType: model.AutomationTrigger115Directory,
		CloudStorageID: 1, DirectoryID: "0", DirectoryPath: "/", IntervalSeconds: 300,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	legacy := legacyAutomationWorkflow{
		SourceID: source.ID, Name: "唯一流程", Version: 1, DefinitionJSON: `{}`,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}

	originalDB := DB
	DB = db
	t.Cleanup(func() { DB = originalDB })
	if err := validateAutomationOneToOneData(); err != nil {
		t.Fatalf("valid legacy data rejected: %v", err)
	}
	if err := db.AutoMigrate(&model.AutomationSource{}, &model.AutomationWorkflow{}); err != nil {
		t.Fatal(err)
	}

	duplicate := model.AutomationWorkflow{
		SourceID: source.ID, Name: "重复流程", Version: 1, DefinitionJSON: `{}`,
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("一对一迁移后仍允许同一个触发源创建第二个流程")
	}
	unbound := model.AutomationWorkflow{
		SourceID: 0, Name: "全局流程", Version: 1, DefinitionJSON: `{}`,
	}
	if err := db.Create(&unbound).Error; err == nil {
		t.Fatal("一对一迁移后仍允许 source_id=0 的全局流程")
	}
}

func TestAutomationOneToOneMigrationRejectsAmbiguousData(t *testing.T) {
	dsn := fmt.Sprintf("file:automations-invalid-migration-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.AutomationSource{}, &legacyAutomationWorkflow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AutomationSource{
		Name: "没有流程的源", TriggerType: model.AutomationTrigger115Directory,
		CloudStorageID: 1, DirectoryID: "0", DirectoryPath: "/", IntervalSeconds: 300,
	}).Error; err != nil {
		t.Fatal(err)
	}

	originalDB := DB
	DB = db
	t.Cleanup(func() { DB = originalDB })
	if err := validateAutomationOneToOneData(); err == nil {
		t.Fatal("存在没有流程的触发源时迁移意外通过")
	}
}

func TestLegacyRSSAutomationMigratesWithoutDeletingSourceData(t *testing.T) {
	dsn := fmt.Sprintf("file:legacy-rss-migration-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.SystemConfig{}, &legacyRSSAutomationSource{}, &legacyRSSAutomationWorkflow{},
		&legacyRSSAutomationEntry{},
		&model.AutomationSource{}, &model.AutomationWorkflow{}, &model.AutomationEntry{},
		&model.AutomationRun{}, &model.AutomationNodeRun{},
	); err != nil {
		t.Fatal(err)
	}
	legacySource := legacyRSSAutomationSource{
		Name: "动画更新", Enabled: true, FeedURL: "https://example.com/rss.xml",
		IntervalMinutes: 5, MappingJSON: `{"item_selector":"channel/item","fields":[]}`,
	}
	if err := db.Create(&legacySource).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&legacyRSSAutomationWorkflow{
		SourceID: legacySource.ID, Name: "下载新条目", Enabled: true,
		Version: 1, DefinitionJSON: `{"schema_version":1,"nodes":[],"edges":[]}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&legacyRSSAutomationEntry{
		SourceID: legacySource.ID, Fingerprint: "legacy-entry", GUID: "legacy-guid",
		Title: "旧条目", FieldsJSON: `{"title":"旧条目"}`, LegacyMatched: true,
		LegacyMetadataJSON: `{"rule_name":"旧规则","media_title":"旧媒体"}`,
		DiscoveredAt:       time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		CREATE TABLE rss_automation_targets (
			id integer primary key,
			name text not null,
			type text not null,
			enabled numeric not null,
			config_json text not null,
			created_at datetime,
			updated_at datetime
		)
	`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		INSERT INTO rss_automation_targets
			(id, name, type, enabled, config_json, created_at, updated_at)
		VALUES (7, '家庭 NAS', 'qbittorrent', 1, '{"base_url":"http://nas:8080"}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SystemConfig{
		ConfigKey: "rss_automation.user_agent", ConfigValue: "legacy", ConfigType: model.TypeString,
	}).Error; err != nil {
		t.Fatal(err)
	}

	originalDB := DB
	DB = db
	t.Cleanup(func() { DB = originalDB })
	if err := preserveLegacyAutomationTargets(); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyRSSAutomations(); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&legacyRSSAutomationSource{}) || !db.Migrator().HasTable(&legacyRSSAutomationWorkflow{}) {
		t.Fatal("旧 RSS 表不应在迁移后被删除")
	}
	var source model.AutomationSource
	if err := db.Where("trigger_type = ?", model.AutomationTriggerRSS).First(&source).Error; err != nil {
		t.Fatalf("RSS 触发器没有迁移到自动化中心: %v", err)
	}
	if source.Name != legacySource.Name || source.FeedURL != legacySource.FeedURL {
		t.Fatalf("migrated source = %#v", source)
	}
	var entry model.AutomationEntry
	if err := db.Where("source_id = ?", source.ID).First(&entry).Error; err != nil {
		t.Fatalf("RSS 条目没有迁移: %v", err)
	}
	if !entry.LegacyMatched || entry.LegacyMetadataJSON == "" {
		t.Fatalf("RSS 旧历史元数据没有保留: %#v", entry)
	}
	if entry.Fingerprint == "legacy-entry" {
		t.Fatal("RSS 去重指纹没有重绑到新的自动化 source ID")
	}
	var workflow model.AutomationWorkflow
	if err := db.Where("source_id = ?", source.ID).First(&workflow).Error; err != nil {
		t.Fatalf("RSS 流程没有迁移: %v", err)
	}
	var count int64
	if err := db.Unscoped().Model(&model.SystemConfig{}).
		Where("config_key = ?", "rss_automation.user_agent").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("RSS User-Agent 设置没有保留，count = %d", count)
	}
	var target model.AutomationTarget
	if err := db.First(&target, 7).Error; err != nil {
		t.Fatalf("independent downloader target was not preserved: %v", err)
	}
	if target.Name != "家庭 NAS" || target.Type != model.AutomationTargetQBittorrent {
		t.Fatalf("preserved downloader target = %#v", target)
	}
}
