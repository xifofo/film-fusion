package database

import (
	"fmt"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type legacyAutomationRunIdentity struct {
	ID              uint   `gorm:"primarykey"`
	WorkflowID      uint   `gorm:"uniqueIndex:idx_automation_run_identity,priority:1"`
	WorkflowName    string `gorm:"not null"`
	WorkflowVersion int    `gorm:"uniqueIndex:idx_automation_run_identity,priority:2"`
	EntryID         uint   `gorm:"uniqueIndex:idx_automation_run_identity,priority:3"`
	DefinitionJSON  string `gorm:"not null"`
	ContextJSON     string `gorm:"not null"`
	Status          string `gorm:"not null"`
}

func (legacyAutomationRunIdentity) TableName() string { return "automation_runs" }

func TestMigrateAutomationRunExecutionIndexAllowsRepeatedExecutions(t *testing.T) {
	dsn := fmt.Sprintf("file:automation-run-migration-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	if err := DB.AutoMigrate(&legacyAutomationRunIdentity{}); err != nil {
		t.Fatal(err)
	}
	legacy := legacyAutomationRunIdentity{
		WorkflowID: 7, WorkflowName: "旧流程", WorkflowVersion: 2, EntryID: 9,
		DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.AutomationRunSucceeded,
	}
	if err := DB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.AutoMigrate(&model.AutomationRun{}); err != nil {
		t.Fatal(err)
	}
	if !DB.Migrator().HasIndex(&model.AutomationRun{}, "idx_automation_run_identity") ||
		!DB.Migrator().HasIndex(&model.AutomationRun{}, "idx_automation_run_execution") {
		t.Fatal("expected both legacy and expanded indexes before cleanup")
	}
	if err := migrateAutomationRunExecutionIndex(); err != nil {
		t.Fatal(err)
	}
	if DB.Migrator().HasIndex(&model.AutomationRun{}, "idx_automation_run_identity") {
		t.Fatal("legacy identity index was not removed")
	}

	repeat := model.AutomationRun{
		WorkflowID: 7, WorkflowName: "旧流程", WorkflowVersion: 2, EntryID: 9,
		ExecutionNo: 2, TriggerMode: model.AutomationRunTriggerManual,
		DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.AutomationRunPending,
	}
	if err := DB.Create(&repeat).Error; err != nil {
		t.Fatalf("expanded identity rejected a repeat: %v", err)
	}
}
