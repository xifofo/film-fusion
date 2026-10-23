package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"
)

func createLocalHistoryAutomation(t *testing.T, s *AutomationService) AutomationCreateResult {
	t.Helper()
	created, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{
		Name: "local history", TriggerType: model.AutomationTriggerLocalDirectory, DirectoryPath: t.TempDir(),
		LocalConfig: &model.LocalDirectoryConfig{Mode: "poll"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func seedLocalHistoryRun(t *testing.T, s *AutomationService, source AutomationCreateResult, key string, completed time.Time) model.AutomationRun {
	t.Helper()
	entry := model.AutomationEntry{SourceID: source.Source.ID, Fingerprint: key, EventKey: "local:" + key,
		ItemID: key, FieldsJSON: "{}", DiscoveredAt: completed, CreatedAt: completed}
	if err := s.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	run := model.AutomationRun{WorkflowID: source.Workflow.ID, WorkflowVersion: 1, EntryID: entry.ID,
		DefinitionJSON: source.Workflow.DefinitionJSON, ContextJSON: "{}", Status: model.AutomationRunSucceeded,
		CompletedAt: &completed, CreatedAt: completed}
	if err := s.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	node := model.AutomationNodeRun{RunID: run.ID, NodeID: "end", NodeType: AutomationNodeEnd, Status: model.AutomationNodeSucceeded, CompletedAt: &completed}
	if err := s.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	return run
}

func TestLocalHistoryRetentionBoundsBatchAndRemovesDependentRecords(t *testing.T) {
	s := managementTestService(t)
	source := createLocalHistoryAutomation(t, s)
	now := time.Now()
	old := now.AddDate(0, 0, -40)
	var first model.AutomationRun
	for i := 0; i < 105; i++ {
		run := seedLocalHistoryRun(t, s, source, fmt.Sprintf("old-%03d", i), old)
		if i == 0 {
			first = run
		}
	}
	bucket := model.AutomationRateLimitBucket{ScopeType: "global", Namespace: "retained", KeyHash: "key", PolicyHash: "policy", Used: 1}
	if err := s.db.Create(&bucket).Error; err != nil {
		t.Fatal(err)
	}
	claim := model.AutomationRateLimitClaim{RunID: first.ID, NodeID: "end", BucketID: bucket.ID}
	if err := s.db.Create(&claim).Error; err != nil {
		t.Fatal(err)
	}
	options, err := ParseLocalDirectoryConfig(source.Source.LocalConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := s.cleanupLocalAutomationHistory(context.Background(), source.Source.ID, options, now)
	if err != nil || cleaned.DeletedRuns != localAutomationHistoryBatch {
		t.Fatalf("bounded cleanup: %+v %v", cleaned, err)
	}
	for _, record := range []any{&model.AutomationRun{}, &model.AutomationNodeRun{}, &model.AutomationEntry{}} {
		var count int64
		if err := s.db.Model(record).Count(&count).Error; err != nil || count != 5 {
			t.Fatalf("remaining %T: %d %v", record, count, err)
		}
	}
	var claims, buckets int64
	s.db.Model(&model.AutomationRateLimitClaim{}).Count(&claims)
	s.db.Model(&model.AutomationRateLimitBucket{}).Count(&buckets)
	if claims != 0 || buckets != 1 {
		t.Fatalf("claim/bucket lifecycle: %d/%d", claims, buckets)
	}
}

func TestLocalHistoryRetentionProtectsActiveReferencesAndOtherSources(t *testing.T) {
	s := managementTestService(t)
	local := createLocalHistoryAutomation(t, s)
	remote := createManagementAutomation(t, s)
	now := time.Now()
	old := now.AddDate(0, 0, -40)
	removed := seedLocalHistoryRun(t, s, local, "remove", old)
	active := seedLocalHistoryRun(t, s, local, "active", old)
	delayed := seedLocalHistoryRun(t, s, local, "delay", old)
	dedup := seedLocalHistoryRun(t, s, local, "dedup", old)
	original := seedLocalHistoryRun(t, s, local, "original", old)
	replay := seedLocalHistoryRun(t, s, local, "replay", now)
	other := seedLocalHistoryRun(t, s, remote, "other", old)
	if err := s.db.Model(&active).Update("status", model.AutomationRunRunning).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&model.AutomationNodeRun{}).Where("run_id = ?", delayed.ID).
		Updates(map[string]any{"status": model.AutomationNodePending, "next_attempt_at": now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&replay).Update("repeated_from_run_id", original.ID).Error; err != nil {
		t.Fatal(err)
	}
	ledger := model.AutomationDedupRecord{ScopeType: "global", Namespace: "forever", KeyHash: "key", State: model.AutomationDedupCommitted,
		OwnerRunID: dedup.ID, OwnerNodeID: "end", FirstSeenAt: old, LastSeenAt: old}
	if err := s.db.Create(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	options, _ := ParseLocalDirectoryConfig(local.Source.LocalConfigJSON)
	cleaned, err := s.cleanupLocalAutomationHistory(context.Background(), local.Source.ID, options, now)
	if err != nil || cleaned.DeletedRuns != 1 {
		t.Fatalf("cleanup=%+v %v", cleaned, err)
	}
	for _, run := range []model.AutomationRun{active, delayed, dedup, original, replay, other} {
		var count int64
		if err := s.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("protected run %d removed", run.ID)
		}
	}
	var count int64
	s.db.Model(&model.AutomationRun{}).Where("id = ?", removed.ID).Count(&count)
	if count != 0 {
		t.Fatal("unreferenced old run retained")
	}
	if _, err := s.cleanupLocalAutomationHistory(context.Background(), remote.Source.ID, options, now); err != nil {
		t.Fatal(err)
	}
	s.db.Model(&model.AutomationRun{}).Where("id = ?", other.ID).Count(&count)
	if count != 1 {
		t.Fatal("non-local history modified")
	}
	queued, err := countLocalAutomationPendingRuns(s.db, local.Source.ID)
	if err != nil || queued != 1 {
		t.Fatalf("queued=%d %v", queued, err)
	}
}

func TestLocalHistoryCountLimitPrunesOldestBeforeTTLAndWarnsForProtectedRuns(t *testing.T) {
	s := managementTestService(t)
	local := createLocalHistoryAutomation(t, s)
	now := time.Now()
	for i := 0; i < 105; i++ {
		seedLocalHistoryRun(t, s, local, fmt.Sprintf("recent-%03d", i), now.Add(time.Duration(i-105)*time.Minute))
	}
	options, _ := ParseLocalDirectoryConfig(local.Source.LocalConfigJSON)
	options.HistoryMaxRuns = 100
	cleaned, err := s.cleanupLocalAutomationHistory(context.Background(), local.Source.ID, options, now)
	if err != nil || cleaned.DeletedRuns != 5 || cleaned.Warning != "" {
		t.Fatalf("count cap: %+v %v", cleaned, err)
	}
	for i := 0; i < 105; i++ {
		run := seedLocalHistoryRun(t, s, local, fmt.Sprintf("protected-%03d", i), now)
		ledger := model.AutomationDedupRecord{ScopeType: "source", ScopeID: local.Source.ID, Namespace: "keep", KeyHash: fmt.Sprint(i),
			State: model.AutomationDedupCommitted, OwnerRunID: run.ID, OwnerNodeID: "end", FirstSeenAt: now, LastSeenAt: now}
		if err := s.db.Create(&ledger).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.cleanupLocalAutomationHistory(context.Background(), local.Source.ID, options, now); err != nil {
		t.Fatal(err)
	}
	cleaned, err = s.cleanupLocalAutomationHistory(context.Background(), local.Source.ID, options, now)
	if err != nil || cleaned.DeletedRuns != 0 || !strings.Contains(cleaned.Warning, "保护") {
		t.Fatalf("protected overflow: %+v %v", cleaned, err)
	}
}
