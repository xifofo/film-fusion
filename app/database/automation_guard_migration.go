package database

import "film-fusion/app/model"

// migrateAutomationGuardTables is kept separate so automation node work can be
// wired into the main migration without expanding its already large model list.
func migrateAutomationGuardTables() error {
	return DB.AutoMigrate(
		&model.AutomationDedupRecord{},
		&model.AutomationRateLimitBucket{},
		&model.AutomationRateLimitClaim{},
	)
}
