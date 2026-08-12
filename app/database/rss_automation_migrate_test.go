package database

import (
	"fmt"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type legacyRSSAutomationWorkflow struct {
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

func (legacyRSSAutomationWorkflow) TableName() string {
	return "rss_automation_workflows"
}

func TestRSSAutomationOneToOneMigrationAddsConstraints(t *testing.T) {
	dsn := fmt.Sprintf("file:rss-automation-migration-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSAutomationSource{}, &legacyRSSAutomationWorkflow{}); err != nil {
		t.Fatal(err)
	}
	source := model.RSSAutomationSource{
		Name: "动画更新", FeedURL: "https://example.com/feed.xml",
		IntervalMinutes: 5, MappingJSON: `{}`,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	legacy := legacyRSSAutomationWorkflow{
		SourceID: source.ID, Name: "唯一流程", Version: 1, DefinitionJSON: `{}`,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}

	originalDB := DB
	DB = db
	t.Cleanup(func() { DB = originalDB })
	if err := validateRSSAutomationOneToOneData(); err != nil {
		t.Fatalf("valid legacy data rejected: %v", err)
	}
	if err := db.AutoMigrate(&model.RSSAutomationSource{}, &model.RSSAutomationWorkflow{}); err != nil {
		t.Fatal(err)
	}

	duplicate := model.RSSAutomationWorkflow{
		SourceID: source.ID, Name: "重复流程", Version: 1, DefinitionJSON: `{}`,
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("一对一迁移后仍允许同一个 RSS 源创建第二个流程")
	}
	unbound := model.RSSAutomationWorkflow{
		SourceID: 0, Name: "全局流程", Version: 1, DefinitionJSON: `{}`,
	}
	if err := db.Create(&unbound).Error; err == nil {
		t.Fatal("一对一迁移后仍允许 source_id=0 的全局流程")
	}
}

func TestRSSAutomationOneToOneMigrationRejectsAmbiguousData(t *testing.T) {
	dsn := fmt.Sprintf("file:rss-automation-invalid-migration-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSAutomationSource{}, &legacyRSSAutomationWorkflow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RSSAutomationSource{
		Name: "没有流程的源", FeedURL: "https://example.com/feed.xml",
		IntervalMinutes: 5, MappingJSON: `{}`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	originalDB := DB
	DB = db
	t.Cleanup(func() { DB = originalDB })
	if err := validateRSSAutomationOneToOneData(); err == nil {
		t.Fatal("存在没有流程的 RSS 源时迁移意外通过")
	}
}
