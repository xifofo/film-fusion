package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newAutomationGuardTestService(t *testing.T) (*AutomationService, *gorm.DB) {
	t.Helper()
	db := newAutomationTestDB(t)
	db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	if err := db.AutoMigrate(
		&model.AutomationDedupRecord{},
		&model.AutomationRateLimitBucket{},
		&model.AutomationRateLimitClaim{},
	); err != nil {
		t.Fatal(err)
	}
	return &AutomationService{db: db, ctx: context.Background()}, db
}

func createAutomationGuardRun(t *testing.T, db *gorm.DB, identity uint) model.AutomationRun {
	t.Helper()
	run := model.AutomationRun{
		WorkflowID: identity, WorkflowName: "guard", WorkflowVersion: 1, EntryID: identity,
		DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.AutomationRunRunning,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	return run
}

func automationGuardTestContext(sourceID uint, key string) map[string]any {
	return map[string]any{
		"source_id": sourceID,
		"item":      map[string]any{"key": key},
		"vars":      map[string]any{},
		"nodes":     map[string]any{},
	}
}

type automationDedupPartialRetryFixture struct {
	service        *AutomationService
	db             *gorm.DB
	run            model.AutomationRun
	node           AutomationNode
	definition     AutomationDefinition
	nodeRuns       []model.AutomationNodeRun
	originalExpiry time.Time
	now            time.Time
}

func newAutomationDedupPartialRetryFixture(t *testing.T, namespace string) automationDedupPartialRetryFixture {
	t.Helper()
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	node := AutomationNode{ID: "dedupe", Type: AutomationNodeDeduplicate, Name: "条目去重", Config: map[string]any{
		"key": "media:partial", "scope": "workflow", "namespace": namespace, "ttl_seconds": 60,
	}}
	definition := AutomationDefinition{SchemaVersion: AutomationSchemaVersion, Nodes: []AutomationNode{node}}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	run := createAutomationGuardRun(t, db, 99)
	run.DefinitionJSON = definitionJSON
	if err := db.Model(&run).Update("definition_json", definitionJSON).Error; err != nil {
		t.Fatal(err)
	}
	output, err := service.executeAutomationDeduplicateNodeAt(run, model.AutomationNodeRun{RunID: run.ID, NodeID: node.ID}, node, automationGuardTestContext(42, "unused"), now)
	if err != nil || output["selected_port"] != "new" {
		t.Fatalf("initial partial dedup = %#v, %v", output, err)
	}
	outputJSON, _ := json.Marshal(output)
	nodeRuns := []model.AutomationNodeRun{{
		RunID: run.ID, NodeID: node.ID, NodeType: AutomationNodeDeduplicate, NodeName: node.Name,
		Status: model.AutomationNodeSucceeded, OutputJSON: string(outputJSON),
	}}
	if err := db.Create(&nodeRuns).Error; err != nil {
		t.Fatal(err)
	}
	partialAt := now.Add(time.Second)
	if err := db.Model(&run).Update("status", model.AutomationRunPartial).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.finalizeAutomationGuardRunAt(run.ID, model.AutomationRunPartial, partialAt); err != nil {
		t.Fatal(err)
	}
	var record model.AutomationDedupRecord
	if err := db.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.State != model.AutomationDedupCommitted || record.ExpiresAt == nil {
		t.Fatalf("partial record = %#v", record)
	}
	return automationDedupPartialRetryFixture{
		service: service, db: db, run: run, node: node, definition: definition, nodeRuns: nodeRuns,
		originalExpiry: *record.ExpiresAt, now: now,
	}
}

func restoreAutomationDedupPartialRetryForTest(t *testing.T, fixture automationDedupPartialRetryFixture, retryAt time.Time) {
	t.Helper()
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.AutomationRun{}).
			Where("id = ? AND status = ?", fixture.run.ID, model.AutomationRunPartial).
			Update("status", model.AutomationRunPending)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("partial run was not transitioned for retry")
		}
		return restoreAutomationDedupReservationsForRetryWithDB(tx, fixture.run, fixture.definition, fixture.nodeRuns, map[string]struct{}{}, retryAt)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationDeduplicateReservationIsAtomicAndNodeRunIdempotent(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	node := AutomationNode{ID: "dedupe", Config: map[string]any{
		"key": "$item.key", "scope": "global", "namespace": "media-season",
		"ttl_seconds": 60, "normalize": "trim_lower",
	}}
	run1 := createAutomationGuardRun(t, db, 1)
	nodeRun1 := model.AutomationNodeRun{RunID: run1.ID, NodeID: node.ID}
	contextValue := automationGuardTestContext(9, "  TMDB:1396:S01  ")

	first, err := service.executeAutomationDeduplicateNodeAt(run1, nodeRun1, node, contextValue, now)
	if err != nil || first["selected_port"] != "new" || first["duplicate"] != false {
		t.Fatalf("first reservation = %#v, %v", first, err)
	}
	if encoded, _ := json.Marshal(first); strings.Contains(string(encoded), "TMDB:1396") {
		t.Fatalf("dedup output leaked raw key: %s", encoded)
	}

	repeated, err := service.executeAutomationDeduplicateNodeAt(run1, nodeRun1, node, contextValue, now.Add(time.Second))
	if err != nil || repeated["selected_port"] != "new" {
		t.Fatalf("same node run must retain new ownership: %#v, %v", repeated, err)
	}
	var count int64
	if err := db.Model(&model.AutomationDedupRecord{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("dedup count = %d, %v", count, err)
	}

	run2 := createAutomationGuardRun(t, db, 2)
	second, err := service.executeAutomationDeduplicateNodeAt(run2, model.AutomationNodeRun{RunID: run2.ID, NodeID: node.ID}, node, contextValue, now.Add(2*time.Second))
	if err != nil || second["selected_port"] != "duplicate" || second["duplicate"] != true {
		t.Fatalf("competing reservation = %#v, %v", second, err)
	}
}

func TestAutomationDeduplicateConcurrentReservationsHaveOneOwner(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	node := AutomationNode{ID: "dedupe", Config: map[string]any{
		"key": "$item.key", "scope": "global", "namespace": "concurrent-test", "ttl_seconds": 60,
	}}
	contextValue := automationGuardTestContext(1, "one-key")
	runs := make([]model.AutomationRun, 8)
	for index := range runs {
		runs[index] = createAutomationGuardRun(t, db, uint(index+1))
	}

	type result struct {
		port string
		err  error
	}
	results := make(chan result, len(runs))
	var group sync.WaitGroup
	for index := range runs {
		group.Add(1)
		go func(run model.AutomationRun) {
			defer group.Done()
			output, err := service.executeAutomationDeduplicateNodeAt(run, model.AutomationNodeRun{RunID: run.ID, NodeID: node.ID}, node, contextValue, now)
			results <- result{port: fmt.Sprint(output["selected_port"]), err: err}
		}(runs[index])
	}
	group.Wait()
	close(results)

	newCount := 0
	duplicateCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent reservation failed: %v", result.err)
		}
		switch result.port {
		case "new":
			newCount++
		case "duplicate":
			duplicateCount++
		default:
			t.Fatalf("unexpected port %q", result.port)
		}
	}
	if newCount != 1 || duplicateCount != len(runs)-1 {
		t.Fatalf("concurrent results: new=%d duplicate=%d", newCount, duplicateCount)
	}
}

func TestAutomationDeduplicateFinalizationCommitReleaseAndExpiry(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	node := AutomationNode{ID: "dedupe", Config: map[string]any{
		"key": "$item.key", "scope": "source", "ttl_seconds": 60,
	}}
	contextValue := automationGuardTestContext(42, "same-media")

	run1 := createAutomationGuardRun(t, db, 1)
	if output, err := service.executeAutomationDeduplicateNodeAt(run1, model.AutomationNodeRun{RunID: run1.ID, NodeID: node.ID}, node, contextValue, now); err != nil || output["selected_port"] != "new" {
		t.Fatalf("reserve = %#v, %v", output, err)
	}
	committedAt := now.Add(10 * time.Second)
	if err := service.finalizeAutomationGuardRunAt(run1.ID, model.AutomationRunSucceeded, committedAt); err != nil {
		t.Fatal(err)
	}
	var record model.AutomationDedupRecord
	if err := db.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.State != model.AutomationDedupCommitted || record.ExpiresAt == nil || !record.ExpiresAt.Equal(committedAt.Add(60*time.Second)) {
		t.Fatalf("committed record = %#v", record)
	}

	run2 := createAutomationGuardRun(t, db, 2)
	duplicate, err := service.executeAutomationDeduplicateNodeAt(run2, model.AutomationNodeRun{RunID: run2.ID, NodeID: node.ID}, node, contextValue, committedAt.Add(time.Second))
	if err != nil || duplicate["selected_port"] != "duplicate" {
		t.Fatalf("committed duplicate = %#v, %v", duplicate, err)
	}

	run3 := createAutomationGuardRun(t, db, 3)
	reclaimed, err := service.executeAutomationDeduplicateNodeAt(run3, model.AutomationNodeRun{RunID: run3.ID, NodeID: node.ID}, node, contextValue, committedAt.Add(61*time.Second))
	if err != nil || reclaimed["selected_port"] != "new" {
		t.Fatalf("expired reservation = %#v, %v", reclaimed, err)
	}
	if err := service.finalizeAutomationGuardRunAt(run3.ID, model.AutomationRunFailed, committedAt.Add(62*time.Second)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.AutomationDedupRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed owner reservation count = %d, %v", count, err)
	}

	run4 := createAutomationGuardRun(t, db, 4)
	afterRelease, err := service.executeAutomationDeduplicateNodeAt(run4, model.AutomationNodeRun{RunID: run4.ID, NodeID: node.ID}, node, contextValue, committedAt.Add(63*time.Second))
	if err != nil || afterRelease["selected_port"] != "new" {
		t.Fatalf("reservation after release = %#v, %v", afterRelease, err)
	}
}

func TestAutomationDeduplicateReclaimsTerminalReservationWithoutFinalizeHook(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	node := AutomationNode{ID: "dedupe", Config: map[string]any{
		"key": "$item.key", "scope": "workflow", "ttl_seconds": 60,
	}}
	contextValue := automationGuardTestContext(1, "media")
	run1 := createAutomationGuardRun(t, db, 1)
	if _, err := service.executeAutomationDeduplicateNodeAt(run1, model.AutomationNodeRun{RunID: run1.ID, NodeID: node.ID}, node, contextValue, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&run1).Update("status", model.AutomationRunFailed).Error; err != nil {
		t.Fatal(err)
	}

	// Keep the same workflow scope while using a distinct run identity.
	run2 := model.AutomationRun{
		WorkflowID: run1.WorkflowID, WorkflowName: "guard", WorkflowVersion: 2, EntryID: 2,
		DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.AutomationRunRunning,
	}
	if err := db.Create(&run2).Error; err != nil {
		t.Fatal(err)
	}
	output, err := service.executeAutomationDeduplicateNodeAt(run2, model.AutomationNodeRun{RunID: run2.ID, NodeID: node.ID}, node, contextValue, now.Add(time.Second))
	if err != nil || output["selected_port"] != "new" || output["owner_run_id"] != run2.ID {
		t.Fatalf("stale reservation reclaim = %#v, %v", output, err)
	}
}

func TestAutomationDeduplicateRetryRestoresReservationAndKeepsSuccessfulNodes(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	dedupNode := AutomationNode{ID: "dedupe", Type: AutomationNodeDeduplicate, Name: "条目去重", Config: map[string]any{
		"key": "media:one", "scope": "workflow", "ttl_seconds": 60,
	}}
	definition := AutomationDefinition{SchemaVersion: AutomationSchemaVersion, Nodes: []AutomationNode{
		dedupNode,
		{ID: "successful_action", Type: AutomationNodeNotification, Name: "已完成的副作用"},
		{ID: "failed_action", Type: AutomationNodeNotification, Name: "失败节点"},
	}}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	run := createAutomationGuardRun(t, db, 77)
	run.DefinitionJSON = definitionJSON
	if err := db.Model(&run).Update("definition_json", definitionJSON).Error; err != nil {
		t.Fatal(err)
	}
	output, err := service.executeAutomationDeduplicateNodeAt(run, model.AutomationNodeRun{RunID: run.ID, NodeID: dedupNode.ID}, dedupNode, automationGuardTestContext(42, "unused"), now)
	if err != nil || output["selected_port"] != "new" {
		t.Fatalf("initial dedup = %#v, %v", output, err)
	}
	outputJSON, _ := json.Marshal(output)
	nodeRuns := []model.AutomationNodeRun{
		{RunID: run.ID, NodeID: dedupNode.ID, NodeType: AutomationNodeDeduplicate, NodeName: dedupNode.Name, Status: model.AutomationNodeSucceeded, OutputJSON: string(outputJSON)},
		{RunID: run.ID, NodeID: "successful_action", NodeType: AutomationNodeNotification, NodeName: "已完成的副作用", Status: model.AutomationNodeSucceeded, OutputJSON: `{"external_id":"sent-once"}`},
		{RunID: run.ID, NodeID: "failed_action", NodeType: AutomationNodeNotification, NodeName: "失败节点", Status: model.AutomationNodeFailed, ErrorMessage: "temporary failure"},
	}
	if err := db.Create(&nodeRuns).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&run).Update("status", model.AutomationRunFailed).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.finalizeAutomationGuardRunAt(run.ID, model.AutomationRunFailed, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var beforeRetry int64
	if err := db.Model(&model.AutomationDedupRecord{}).Count(&beforeRetry).Error; err != nil || beforeRetry != 0 {
		t.Fatalf("failed run reservation count = %d, %v", beforeRetry, err)
	}

	if err := service.RetryRun(run.ID); err != nil {
		t.Fatal(err)
	}

	var record model.AutomationDedupRecord
	if err := db.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.State != model.AutomationDedupReserved || record.OwnerRunID != run.ID || record.OwnerNodeID != dedupNode.ID {
		t.Fatalf("restored reservation = %#v", record)
	}
	var successful model.AutomationNodeRun
	if err := db.Where("run_id = ? AND node_id = ?", run.ID, "successful_action").First(&successful).Error; err != nil {
		t.Fatal(err)
	}
	if successful.Status != model.AutomationNodeSucceeded || successful.OutputJSON != `{"external_id":"sent-once"}` {
		t.Fatalf("successful side effect node was changed: %#v", successful)
	}
	var retriedRun model.AutomationRun
	if err := db.First(&retriedRun, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	var retriedFailure model.AutomationNodeRun
	if err := db.Where("run_id = ? AND node_id = ?", run.ID, "failed_action").First(&retriedFailure).Error; err != nil {
		t.Fatal(err)
	}
	if retriedRun.Status != model.AutomationRunPending || retriedFailure.Status != model.AutomationNodePending || retriedFailure.ErrorMessage != "" {
		t.Fatalf("RetryRun integration state = run:%q failed-node:%#v", retriedRun.Status, retriedFailure)
	}

	// A failed retry releases the restored reservation just like the original
	// failed run, while a later successful retry commits it again.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.AutomationRun{}).Where("id = ?", run.ID).Update("status", model.AutomationRunFailed).Error; err != nil {
			return err
		}
		return tx.Model(&model.AutomationNodeRun{}).Where("run_id = ? AND node_id = ?", run.ID, "failed_action").Updates(map[string]any{
			"status": model.AutomationNodeFailed, "error_message": "failed again",
		}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.finalizeAutomationGuardRunAt(run.ID, model.AutomationRunFailed, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var releasedCount int64
	if err := db.Model(&model.AutomationDedupRecord{}).Count(&releasedCount).Error; err != nil || releasedCount != 0 {
		t.Fatalf("failed retry reservation count = %d, %v", releasedCount, err)
	}
	if err := service.RetryRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).Update("status", model.AutomationRunSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	if err := service.finalizeAutomationGuardRunAt(run.ID, model.AutomationRunSucceeded, completedAt); err != nil {
		t.Fatal(err)
	}
	record = model.AutomationDedupRecord{}
	if err := db.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.State != model.AutomationDedupCommitted || record.ExpiresAt == nil || !record.ExpiresAt.Equal(completedAt.Add(60*time.Second)) {
		t.Fatalf("successful retry did not commit reservation: %#v", record)
	}
}

func TestAutomationDeduplicateRetryConflictRollsBackAllRestoredReservations(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	nodes := []AutomationNode{
		{ID: "dedupe_one", Type: AutomationNodeDeduplicate, Name: "第一道去重", Config: map[string]any{
			"key": "media:one", "scope": "workflow", "namespace": "retry-one", "ttl_seconds": 60,
		}},
		{ID: "dedupe_two", Type: AutomationNodeDeduplicate, Name: "第二道去重", Config: map[string]any{
			"key": "media:two", "scope": "workflow", "namespace": "retry-two", "ttl_seconds": 60,
		}},
	}
	definition := AutomationDefinition{SchemaVersion: AutomationSchemaVersion, Nodes: nodes}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	run := createAutomationGuardRun(t, db, 88)
	run.DefinitionJSON = definitionJSON
	if err := db.Model(&run).Update("definition_json", definitionJSON).Error; err != nil {
		t.Fatal(err)
	}
	contextValue := automationGuardTestContext(42, "unused")
	nodeRuns := make([]model.AutomationNodeRun, 0, len(nodes))
	for _, node := range nodes {
		output, executeErr := service.executeAutomationDeduplicateNodeAt(run, model.AutomationNodeRun{RunID: run.ID, NodeID: node.ID}, node, contextValue, now)
		if executeErr != nil || output["selected_port"] != "new" {
			t.Fatalf("initial %s dedup = %#v, %v", node.ID, output, executeErr)
		}
		outputJSON, _ := json.Marshal(output)
		nodeRuns = append(nodeRuns, model.AutomationNodeRun{
			RunID: run.ID, NodeID: node.ID, NodeType: AutomationNodeDeduplicate, NodeName: node.Name,
			Status: model.AutomationNodeSucceeded, OutputJSON: string(outputJSON),
		})
	}
	if err := db.Create(&nodeRuns).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&run).Update("status", model.AutomationRunFailed).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.finalizeAutomationGuardRunAt(run.ID, model.AutomationRunFailed, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	competitor := model.AutomationRun{
		WorkflowID: run.WorkflowID, WorkflowName: "guard", WorkflowVersion: 2, EntryID: run.EntryID + 1,
		DefinitionJSON: definitionJSON, ContextJSON: `{}`, Status: model.AutomationRunRunning,
	}
	if err := db.Create(&competitor).Error; err != nil {
		t.Fatal(err)
	}
	competingOutput, err := service.executeAutomationDeduplicateNodeAt(competitor, model.AutomationNodeRun{RunID: competitor.ID, NodeID: nodes[1].ID}, nodes[1], contextValue, now.Add(2*time.Second))
	if err != nil || competingOutput["selected_port"] != "new" {
		t.Fatalf("competing reservation = %#v, %v", competingOutput, err)
	}

	retryErr := db.Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.AutomationRun{}).
			Where("id = ? AND status IN ?", run.ID, []string{model.AutomationRunFailed, model.AutomationRunPartial, model.AutomationRunCancelled}).
			Update("status", model.AutomationRunPending)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("run was not transitioned for retry")
		}
		return restoreAutomationDedupReservationsForRetryWithDB(tx, run, definition, nodeRuns, map[string]struct{}{}, now.Add(3*time.Second))
	})
	var conflict *automationDedupRetryConflictError
	if !errors.As(retryErr, &conflict) || conflict.OwnerRunID != competitor.ID {
		t.Fatalf("retry conflict = %T %#v", retryErr, retryErr)
	}

	var refreshedRun model.AutomationRun
	if err := db.First(&refreshedRun, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshedRun.Status != model.AutomationRunFailed {
		t.Fatalf("conflicted retry changed run status to %q", refreshedRun.Status)
	}
	var records []model.AutomationDedupRecord
	if err := db.Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].OwnerRunID != competitor.ID || records[0].Namespace != "retry-two" {
		t.Fatalf("partial retry reservations were not rolled back: %#v", records)
	}
}

func TestAutomationDeduplicatePartialRetryProtectsExpiredCommitAndRestoresItOnFailure(t *testing.T) {
	for _, terminalStatus := range []string{model.AutomationRunFailed, model.AutomationRunCancelled} {
		t.Run(terminalStatus, func(t *testing.T) {
			fixture := newAutomationDedupPartialRetryFixture(t, "partial-"+terminalStatus)
			retryAt := fixture.originalExpiry.Add(time.Second)
			restoreAutomationDedupPartialRetryForTest(t, fixture, retryAt)

			var reopened model.AutomationDedupRecord
			if err := fixture.db.First(&reopened).Error; err != nil {
				t.Fatal(err)
			}
			if reopened.State != automationDedupRetryCommitted || reopened.ExpiresAt == nil || !reopened.ExpiresAt.Equal(fixture.originalExpiry) {
				t.Fatalf("expired partial commit was not safely reopened: %#v", reopened)
			}

			competitor := model.AutomationRun{
				WorkflowID: fixture.run.WorkflowID, WorkflowName: "guard", WorkflowVersion: 2, EntryID: fixture.run.EntryID + 1,
				DefinitionJSON: fixture.run.DefinitionJSON, ContextJSON: `{}`, Status: model.AutomationRunRunning,
			}
			if err := fixture.db.Create(&competitor).Error; err != nil {
				t.Fatal(err)
			}
			competing, err := fixture.service.executeAutomationDeduplicateNodeAt(competitor, model.AutomationNodeRun{RunID: competitor.ID, NodeID: fixture.node.ID}, fixture.node, automationGuardTestContext(42, "unused"), retryAt.Add(time.Second))
			if err != nil || competing["selected_port"] != "duplicate" || competing["owner_run_id"] != fixture.run.ID {
				t.Fatalf("active partial retry lost expired protection = %#v, %v", competing, err)
			}

			if err := fixture.db.Model(&model.AutomationRun{}).Where("id = ?", fixture.run.ID).Update("status", terminalStatus).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.finalizeAutomationGuardRunAt(fixture.run.ID, terminalStatus, retryAt.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			var restored model.AutomationDedupRecord
			if err := fixture.db.First(&restored).Error; err != nil {
				t.Fatal(err)
			}
			if restored.State != model.AutomationDedupCommitted || restored.OwnerRunID != fixture.run.ID || restored.ExpiresAt == nil || !restored.ExpiresAt.Equal(fixture.originalExpiry) {
				t.Fatalf("%s retry did not preserve prior partial commit: %#v", terminalStatus, restored)
			}

			newRun := model.AutomationRun{
				WorkflowID: fixture.run.WorkflowID, WorkflowName: "guard", WorkflowVersion: 3, EntryID: fixture.run.EntryID + 2,
				DefinitionJSON: fixture.run.DefinitionJSON, ContextJSON: `{}`, Status: model.AutomationRunRunning,
			}
			if err := fixture.db.Create(&newRun).Error; err != nil {
				t.Fatal(err)
			}
			reclaimed, err := fixture.service.executeAutomationDeduplicateNodeAt(newRun, model.AutomationNodeRun{RunID: newRun.ID, NodeID: fixture.node.ID}, fixture.node, automationGuardTestContext(42, "unused"), retryAt.Add(3*time.Second))
			if err != nil || reclaimed["selected_port"] != "new" || reclaimed["owner_run_id"] != newRun.ID {
				t.Fatalf("expired prior commit was not reclaimable after %s retry = %#v, %v", terminalStatus, reclaimed, err)
			}
		})
	}
}

func TestAutomationDeduplicatePartialRetrySuccessRefreshesCommitTTL(t *testing.T) {
	fixture := newAutomationDedupPartialRetryFixture(t, "partial-success")
	// Reopen while the old commit is still valid. retry_committed prevents it
	// from expiring mid-retry and success starts a fresh TTL at completion.
	retryAt := fixture.now.Add(10 * time.Second)
	restoreAutomationDedupPartialRetryForTest(t, fixture, retryAt)
	if err := fixture.db.Model(&model.AutomationRun{}).Where("id = ?", fixture.run.ID).Update("status", model.AutomationRunSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	completedAt := fixture.originalExpiry.Add(30 * time.Second)
	if err := fixture.service.finalizeAutomationGuardRunAt(fixture.run.ID, model.AutomationRunSucceeded, completedAt); err != nil {
		t.Fatal(err)
	}
	var record model.AutomationDedupRecord
	if err := fixture.db.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	wantedExpiry := completedAt.Add(60 * time.Second)
	if record.State != model.AutomationDedupCommitted || record.ExpiresAt == nil || !record.ExpiresAt.Equal(wantedExpiry) {
		t.Fatalf("successful partial retry commit = %#v, want expiry %s", record, wantedExpiry)
	}
}

func TestAutomationRateLimitFixedWindowAndClaimIdempotency(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 15, 0, time.UTC)
	node := AutomationNode{ID: "rate", Config: map[string]any{
		"key": "$item.key", "scope": "global", "namespace": "115-submit",
		"limit": 1, "window_seconds": 60, "behavior": "branch",
	}}
	contextValue := automationGuardTestContext(1, "account-1")
	run1 := createAutomationGuardRun(t, db, 1)
	nodeRun1 := model.AutomationNodeRun{RunID: run1.ID, NodeID: node.ID}

	allowed, err := service.executeAutomationRateLimitNodeAt(run1, nodeRun1, node, contextValue, now)
	if err != nil || allowed["selected_port"] != "allowed" || allowed["used"] != 1 {
		t.Fatalf("first rate claim = %#v, %v", allowed, err)
	}
	repeated, err := service.executeAutomationRateLimitNodeAt(run1, nodeRun1, node, contextValue, now.Add(time.Second))
	if err != nil || repeated["selected_port"] != "allowed" || repeated["claim_id"] != allowed["claim_id"] {
		t.Fatalf("idempotent claim = %#v, %v", repeated, err)
	}
	var bucket model.AutomationRateLimitBucket
	if err := db.First(&bucket).Error; err != nil || bucket.Used != 1 {
		t.Fatalf("bucket after retry = %#v, %v", bucket, err)
	}

	run2 := createAutomationGuardRun(t, db, 2)
	throttled, err := (&AutomationService{db: db, ctx: context.Background()}).executeAutomationRateLimitNodeAt(run2, model.AutomationNodeRun{RunID: run2.ID, NodeID: node.ID}, node, contextValue, now.Add(2*time.Second))
	if err != nil || throttled["selected_port"] != "throttled" || throttled["allowed"] != false {
		t.Fatalf("restarted service throttle = %#v, %v", throttled, err)
	}

	run3 := createAutomationGuardRun(t, db, 3)
	nextWindow, err := service.executeAutomationRateLimitNodeAt(run3, model.AutomationNodeRun{RunID: run3.ID, NodeID: node.ID}, node, contextValue, now.Add(46*time.Second))
	if err != nil || nextWindow["selected_port"] != "allowed" || nextWindow["used"] != 1 {
		t.Fatalf("next window claim = %#v, %v", nextWindow, err)
	}
	if err := db.First(&bucket, bucket.ID).Error; err != nil || !bucket.WindowStart.Equal(time.Date(2026, 8, 30, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("reset bucket = %#v, %v", bucket, err)
	}
}

func TestAutomationRateLimitConcurrentClaimsRespectLimit(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 15, 0, time.UTC)
	node := AutomationNode{ID: "rate", Config: map[string]any{
		"key": "$item.key", "scope": "global", "namespace": "concurrent-rate",
		"limit": 3, "window_seconds": 60, "behavior": "branch",
	}}
	contextValue := automationGuardTestContext(1, "account-1")
	runs := make([]model.AutomationRun, 10)
	for index := range runs {
		runs[index] = createAutomationGuardRun(t, db, uint(index+1))
	}

	type result struct {
		port string
		err  error
	}
	results := make(chan result, len(runs))
	var group sync.WaitGroup
	for index := range runs {
		group.Add(1)
		go func(run model.AutomationRun) {
			defer group.Done()
			output, err := service.executeAutomationRateLimitNodeAt(run, model.AutomationNodeRun{RunID: run.ID, NodeID: node.ID}, node, contextValue, now)
			results <- result{port: fmt.Sprint(output["selected_port"]), err: err}
		}(runs[index])
	}
	group.Wait()
	close(results)

	allowedCount := 0
	throttledCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent rate claim failed: %v", result.err)
		}
		switch result.port {
		case "allowed":
			allowedCount++
		case "throttled":
			throttledCount++
		default:
			t.Fatalf("unexpected port %q", result.port)
		}
	}
	if allowedCount != 3 || throttledCount != len(runs)-3 {
		t.Fatalf("concurrent results: allowed=%d throttled=%d", allowedCount, throttledCount)
	}
	var bucket model.AutomationRateLimitBucket
	if err := db.First(&bucket).Error; err != nil || bucket.Used != 3 {
		t.Fatalf("bucket after concurrency = %#v, %v", bucket, err)
	}
	var claims int64
	if err := db.Model(&model.AutomationRateLimitClaim{}).Count(&claims).Error; err != nil || claims != 3 {
		t.Fatalf("claims after concurrency = %d, %v", claims, err)
	}
}

func TestAutomationRateLimitSeparatesDifferentPoliciesForSameKey(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 15, 0, time.UTC)
	contextValue := automationGuardTestContext(1, "account-1")
	firstPolicy := AutomationNode{ID: "rate-first", Config: map[string]any{
		"key": "$item.key", "scope": "global", "namespace": "shared-account-limit",
		"limit": 1, "window_seconds": 60, "behavior": "branch",
	}}
	secondPolicy := AutomationNode{ID: "rate-second", Config: map[string]any{
		"key": "$item.key", "scope": "global", "namespace": "shared-account-limit",
		"limit": 2, "window_seconds": 120, "behavior": "branch",
	}}

	run1 := createAutomationGuardRun(t, db, 1)
	first, err := service.executeAutomationRateLimitNodeAt(run1, model.AutomationNodeRun{RunID: run1.ID, NodeID: firstPolicy.ID}, firstPolicy, contextValue, now)
	if err != nil || first["selected_port"] != "allowed" {
		t.Fatalf("first policy claim = %#v, %v", first, err)
	}
	driftedPolicy := secondPolicy
	driftedPolicy.ID = firstPolicy.ID
	drifted, driftErr := service.executeAutomationRateLimitNodeAt(run1, model.AutomationNodeRun{RunID: run1.ID, NodeID: firstPolicy.ID}, driftedPolicy, contextValue, now.Add(time.Second))
	if driftErr == nil || drifted["selected_port"] != "failure" {
		t.Fatalf("same node run accepted policy drift = %#v, %v", drifted, driftErr)
	}
	run2 := createAutomationGuardRun(t, db, 2)
	second, err := service.executeAutomationRateLimitNodeAt(run2, model.AutomationNodeRun{RunID: run2.ID, NodeID: secondPolicy.ID}, secondPolicy, contextValue, now)
	if err != nil || second["selected_port"] != "allowed" || second["limit"] != 2 || second["window_seconds"] != 120 {
		t.Fatalf("second policy claim = %#v, %v", second, err)
	}
	if first["policy_hash"] == second["policy_hash"] {
		t.Fatalf("different policies shared hash: %#v / %#v", first, second)
	}

	var buckets []model.AutomationRateLimitBucket
	if err := db.Order("id ASC").Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("different policies must use independent buckets, got %d", len(buckets))
	}
	if buckets[0].PolicyHash == buckets[1].PolicyHash || buckets[0].Algorithm != automationRateAlgorithm || buckets[1].Algorithm != automationRateAlgorithm {
		t.Fatalf("persisted bucket policies = %#v", buckets)
	}
}

func TestAutomationRateLimitDeferredWaitSurvivesRestart(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 15, 0, time.UTC)
	node := AutomationNode{ID: "rate", Config: map[string]any{
		"key": "$item.key", "scope": "workflow", "limit": 1,
		"window_seconds": 60, "behavior": "defer", "max_wait_seconds": 120,
	}}
	contextValue := automationGuardTestContext(1, "same-key")
	run1 := createAutomationGuardRun(t, db, 1)
	if _, err := service.executeAutomationRateLimitNodeAt(run1, model.AutomationNodeRun{RunID: run1.ID, NodeID: node.ID}, node, contextValue, now); err != nil {
		t.Fatal(err)
	}

	// Use the same workflow scope for a second run.
	run2 := model.AutomationRun{
		WorkflowID: run1.WorkflowID, WorkflowName: "guard", WorkflowVersion: 2, EntryID: 2,
		DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.AutomationRunRunning,
	}
	if err := db.Create(&run2).Error; err != nil {
		t.Fatal(err)
	}
	nodeRun2 := model.AutomationNodeRun{RunID: run2.ID, NodeID: node.ID}
	waiting, waitErr := service.executeAutomationRateLimitNodeAt(run2, nodeRun2, node, contextValue, now.Add(time.Second))
	var deferred *automationNodeDeferred
	if !errors.As(waitErr, &deferred) || waiting["first_wait_at"] == nil || waiting["selected_port"] != nil {
		t.Fatalf("deferred output = %#v, %T %v", waiting, waitErr, waitErr)
	}
	encoded, _ := json.Marshal(waiting)
	nodeRun2.OutputJSON = string(encoded)

	restarted := &AutomationService{db: db, ctx: context.Background()}
	resumed, err := restarted.executeAutomationRateLimitNodeAt(run2, nodeRun2, node, contextValue, time.Date(2026, 8, 30, 10, 1, 1, 0, time.UTC))
	if err != nil || resumed["selected_port"] != "allowed" {
		t.Fatalf("resumed rate node = %#v, %v", resumed, err)
	}
}

func TestAutomationRateLimitDeferredWaitHonorsMaximum(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 15, 0, time.UTC)
	node := AutomationNode{ID: "rate", Config: map[string]any{
		"key": "$item.key", "scope": "workflow", "limit": 1,
		"window_seconds": 60, "behavior": "defer", "max_wait_seconds": 10,
	}}
	contextValue := automationGuardTestContext(1, "same-key")
	run1 := createAutomationGuardRun(t, db, 1)
	if _, err := service.executeAutomationRateLimitNodeAt(run1, model.AutomationNodeRun{RunID: run1.ID, NodeID: node.ID}, node, contextValue, now); err != nil {
		t.Fatal(err)
	}
	run2 := model.AutomationRun{
		WorkflowID: run1.WorkflowID, WorkflowName: "guard", WorkflowVersion: 2, EntryID: 2,
		DefinitionJSON: `{}`, ContextJSON: `{}`, Status: model.AutomationRunRunning,
	}
	if err := db.Create(&run2).Error; err != nil {
		t.Fatal(err)
	}
	firstWait := now.Add(time.Second)
	nodeRun := model.AutomationNodeRun{RunID: run2.ID, NodeID: node.ID, OutputJSON: `{"first_wait_at":"` + firstWait.Format(time.RFC3339Nano) + `"}`}
	failed, err := service.executeAutomationRateLimitNodeAt(run2, nodeRun, node, contextValue, firstWait.Add(10*time.Second))
	var deferred *automationNodeDeferred
	if err == nil || errors.As(err, &deferred) || failed["selected_port"] != "failure" {
		t.Fatalf("max wait failure = %#v, %T %v", failed, err, err)
	}
}

func TestAutomationGuardDeletionCleansLocalScopesAndPreservesGlobalPolicyState(t *testing.T) {
	service, db := newAutomationGuardTestService(t)
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	const sourceID uint = 42
	const workflowID uint = 77
	run := createAutomationGuardRun(t, db, workflowID)
	contextValue := automationGuardTestContext(sourceID, "same-media")

	for _, scope := range []string{"source", "workflow", "global"} {
		namespace := "cleanup-" + scope
		dedupNode := AutomationNode{ID: "dedup-" + scope, Config: map[string]any{
			"key": "$item.key", "scope": scope, "namespace": namespace, "ttl_seconds": 60,
		}}
		if output, err := service.executeAutomationDeduplicateNodeAt(run, model.AutomationNodeRun{RunID: run.ID, NodeID: dedupNode.ID}, dedupNode, contextValue, now); err != nil || output["selected_port"] != "new" {
			t.Fatalf("%s dedup reservation = %#v, %v", scope, output, err)
		}
		rateNode := AutomationNode{ID: "rate-" + scope, Config: map[string]any{
			"key": "$item.key", "scope": scope, "namespace": namespace,
			"limit": 2, "window_seconds": 60, "behavior": "branch",
		}}
		if output, err := service.executeAutomationRateLimitNodeAt(run, model.AutomationNodeRun{RunID: run.ID, NodeID: rateNode.ID}, rateNode, contextValue, now); err != nil || output["selected_port"] != "allowed" {
			t.Fatalf("%s rate claim = %#v, %v", scope, output, err)
		}
	}
	if err := service.finalizeAutomationGuardRunAt(run.ID, model.AutomationRunSucceeded, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		runIDs := tx.Model(&model.AutomationRun{}).Select("id").Where("workflow_id = ?", workflowID)
		if err := deleteAutomationGuardStateForRuns(tx, runIDs); err != nil {
			return err
		}
		if err := deleteAutomationGuardsForWorkflow(tx, workflowID); err != nil {
			return err
		}
		return deleteAutomationGuardsForSource(tx, sourceID)
	}); err != nil {
		t.Fatal(err)
	}

	var dedupRecords []model.AutomationDedupRecord
	if err := db.Find(&dedupRecords).Error; err != nil {
		t.Fatal(err)
	}
	if len(dedupRecords) != 1 || dedupRecords[0].ScopeType != "global" || dedupRecords[0].State != model.AutomationDedupCommitted {
		t.Fatalf("remaining dedup records = %#v", dedupRecords)
	}
	var buckets []model.AutomationRateLimitBucket
	if err := db.Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].ScopeType != "global" || buckets[0].Used != 1 {
		t.Fatalf("remaining rate buckets = %#v", buckets)
	}
	var claimCount int64
	if err := db.Model(&model.AutomationRateLimitClaim{}).Count(&claimCount).Error; err != nil || claimCount != 0 {
		t.Fatalf("remaining rate claims = %d, %v", claimCount, err)
	}
	if err := deleteAutomationGuardsForScope(db, "global", 1); err == nil {
		t.Fatal("global scope cleanup must be rejected")
	}
}

func TestAutomationGuardConfigValidation(t *testing.T) {
	ports := map[string][]string{
		AutomationNodeDeduplicate: {"new", "duplicate", "failure", "always"},
		AutomationNodeRateLimit:   {"allowed", "throttled", "failure", "always"},
	}
	for nodeType, expectedPorts := range ports {
		if !isAutomationNodeType(nodeType) {
			t.Errorf("%s is not registered", nodeType)
		}
		for _, port := range expectedPorts {
			if !isAutomationSourcePortValid(nodeType, port) {
				t.Errorf("%s missing %s port", nodeType, port)
			}
		}
	}
	if isAutomationSourcePortValid(AutomationNodeDeduplicate, "success") || isAutomationSourcePortValid(AutomationNodeRateLimit, "duplicate") {
		t.Fatal("guard node accepted another node type's port")
	}
	if err := validateAutomationDeduplicateConfig(map[string]any{"key": "x", "scope": "global"}); err == nil {
		t.Fatal("global dedup without namespace accepted")
	}
	if err := validateAutomationDeduplicateConfig(map[string]any{"key": "x", "ttl_seconds": 59}); err == nil {
		t.Fatal("short dedup TTL accepted")
	}
	if err := validateAutomationRateLimitConfig(map[string]any{"key": "x", "limit": 0}); err == nil {
		t.Fatal("zero rate limit accepted")
	}
	if err := validateAutomationRateLimitConfig(map[string]any{"key": "x", "behavior": "sleep"}); err == nil {
		t.Fatal("blocking rate behavior accepted")
	}
}
