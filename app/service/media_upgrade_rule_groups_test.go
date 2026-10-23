package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"film-fusion/app/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func upgradeGroupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "groups.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.MediaUpgradeRuleGroup{}, &model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := db.DB(); _ = sql.Close() })
	return db
}

func groupJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

func TestUpgradeRuleGroupsOwnershipReuseAndRevision(t *testing.T) {
	db := upgradeGroupDB(t)
	input := MediaUpgradeRuleGroupInput{Name: "客厅规则", Policy: MediaUpgradePresets()["quality"]}
	group, err := SaveMediaUpgradeRuleGroup(db, 1, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if !group.Policy.Enabled || group.Revision != 1 {
		t.Fatalf("invalid saved group: %+v", group)
	}
	ref := groupJSON(MediaUpgradePolicy{Enabled: true, GroupID: group.ID})
	if _, err := ResolveMediaUpgradePolicy(db, 2, ref); !errors.Is(err, ErrUpgradeRuleGroupNotFound) {
		t.Fatalf("cross-user reference: %v", err)
	}
	for _, name := range []string{"电影", "剧集"} {
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := LockMediaUpgradeRuleGroupReference(tx, 1, ref); err != nil {
				return err
			}
			return tx.Create(&model.CloudDirectory{UserID: 1, CloudStorageID: 1, DirectoryName: name, DirectoryID: name, UpgradePolicy: ref}).Error
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	groups, err := ListMediaUpgradeRuleGroups(db, 1)
	if err != nil || len(groups) != 1 || groups[0].DirectoryCount != 2 {
		t.Fatalf("list: %+v %v", groups, err)
	}
	if other, err := ListMediaUpgradeRuleGroups(db, 2); err != nil || len(other) != 0 {
		t.Fatalf("cross-user list: %+v %v", other, err)
	}
	if err := DeleteMediaUpgradeRuleGroup(db, 1, group.ID, 1); !errors.Is(err, ErrUpgradeRuleGroupInUse) {
		t.Fatalf("deleted live reference: %v", err)
	}
	frozen, err := ResolveMediaUpgradePolicy(db, 1, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMediaUpgradeRuleGroupSnapshot(db, 1, &frozen); err != nil {
		t.Fatal(err)
	}
	input.Policy, input.ExpectedRevision = MediaUpgradePresets()["compatible"], 1
	updated, err := SaveMediaUpgradeRuleGroup(db, 1, group.ID, input)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := SaveMediaUpgradeRuleGroup(db, 1, group.ID, input); !errors.Is(err, ErrUpgradeRuleGroupConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if err := ValidateMediaUpgradeRuleGroupSnapshot(db, 1, &frozen); err == nil {
		t.Fatal("old preview accepted after rule change")
	}
	current, err := ResolveMediaUpgradePolicy(db, 1, ref)
	if err != nil || current.GroupRevision != 2 || current.Rules[0].Name != input.Policy.Rules[0].Name {
		t.Fatalf("reference did not refresh: %+v %v", current, err)
	}
	if frozen.Rules[0].Name == current.Rules[0].Name {
		t.Fatal("snapshot mutated")
	}
	if _, err := SaveMediaUpgradeRuleGroup(db, 2, group.ID, input); !errors.Is(err, ErrUpgradeRuleGroupNotFound) {
		t.Fatalf("cross-user update: %v", err)
	}
	if err := DeleteMediaUpgradeRuleGroup(db, 2, group.ID, 2); !errors.Is(err, ErrUpgradeRuleGroupNotFound) {
		t.Fatalf("cross-user delete: %v", err)
	}
	if err := db.Where("user_id = ?", 1).Delete(&model.CloudDirectory{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteMediaUpgradeRuleGroup(db, 1, group.ID, 1); !errors.Is(err, ErrUpgradeRuleGroupConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := DeleteMediaUpgradeRuleGroup(db, 1, group.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := LockMediaUpgradeRuleGroupReference(db, 1, ref); !errors.Is(err, ErrUpgradeRuleGroupNotFound) {
		t.Fatalf("accepted missing group: %v", err)
	}
}

func TestUpgradeRuleGroupsRejectForgedSnapshotsAndPreserveIndependentPolicies(t *testing.T) {
	db := upgradeGroupDB(t)
	policy := MediaUpgradePresets()["quality"]
	policy.Enabled = true
	resolved, err := ResolveMediaUpgradePolicy(db, 1, groupJSON(policy))
	if err != nil || groupJSON(resolved) != groupJSON(policy) {
		t.Fatalf("legacy changed: %+v %v", resolved, err)
	}
	group, err := SaveMediaUpgradeRuleGroup(db, 1, 0, MediaUpgradeRuleGroupInput{Name: "默认", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	forged := policy
	forged.GroupID, forged.GroupRevision = group.ID, 1
	if _, err := ResolveMediaUpgradePolicy(db, 1, groupJSON(forged)); err == nil {
		t.Fatal("client snapshot accepted")
	}
	if _, err := SaveMediaUpgradeRuleGroup(db, 1, 0, MediaUpgradeRuleGroupInput{Name: "嵌套", Policy: forged}); err == nil {
		t.Fatal("nested group accepted")
	}
	if _, err := SaveMediaUpgradeRuleGroup(db, 1, 0, MediaUpgradeRuleGroupInput{Name: "默认", Policy: policy}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	disabled, err := ResolveMediaUpgradePolicy(db, 1, groupJSON(MediaUpgradePolicy{GroupID: group.ID}))
	if err != nil || disabled.Enabled || len(disabled.Rules) == 0 {
		t.Fatalf("directory switch not preserved: %+v %v", disabled, err)
	}
}
