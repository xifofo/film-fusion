package service

import (
	"fmt"
	"testing"

	"film-fusion/app/model"
)

func TestNextAutomationRunBatchRotatesPastOldWaitingRuns(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{db: db}
	for index := 1; index <= automationRunAdvanceBatchSize+1; index++ {
		run := model.AutomationRun{
			WorkflowID: uint(index), WorkflowName: fmt.Sprintf("流程 %d", index),
			WorkflowVersion: 1, EntryID: uint(index), DefinitionJSON: "{}",
			ContextJSON: "{}", Status: model.AutomationRunRunning,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
	}

	first, err := service.nextAutomationRunBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != automationRunAdvanceBatchSize || first[0].ID != 1 || first[len(first)-1].ID != automationRunAdvanceBatchSize {
		t.Fatalf("first batch = %d, IDs %d..%d", len(first), first[0].ID, first[len(first)-1].ID)
	}

	second, err := service.nextAutomationRunBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != automationRunAdvanceBatchSize+1 {
		t.Fatalf("second batch = %#v", second)
	}
	if service.executionCursorID != 0 {
		t.Fatalf("cursor = %d, want reset after final partial batch", service.executionCursorID)
	}
}
