package database

import "film-fusion/app/model"

// migrateAutomationRunExecutionIndex removes the legacy three-column unique
// index after AutoMigrate has added execution_no and created the expanded
// four-column index. The old index would otherwise reject a manual repeat of
// the same workflow snapshot and entry.
func migrateAutomationRunExecutionIndex() error {
	if !DB.Migrator().HasTable(&model.AutomationRun{}) ||
		!DB.Migrator().HasIndex(&model.AutomationRun{}, "idx_automation_run_identity") {
		return nil
	}
	return DB.Migrator().DropIndex(&model.AutomationRun{}, "idx_automation_run_identity")
}
