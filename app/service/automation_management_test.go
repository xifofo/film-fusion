package service

import (
	"context"
	"encoding/json"
	"film-fusion/app/model"
	"strings"
	"testing"
	"time"
)

func managementTestService(t *testing.T) *AutomationService {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &AutomationService{db: newAutomationTestDB(t), ctx: ctx, cancel: cancel, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1), workers: make(chan struct{}, 4), scanWorkers: make(chan struct{}, 2)}
}

func createManagementAutomation(t *testing.T, s *AutomationService) AutomationCreateResult {
	t.Helper()
	result, err := s.CreateAutomation(AutomationCreateInput{Source: AutomationSourceInput{Name: "订阅", TriggerType: "rss", FeedURL: "https://example.com/rss.xml", Enabled: true}, Workflow: AutomationCreateWorkflowInput{Name: "流程", Definition: DefaultRSSAutomationDefinition()}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAutomationRevisionRestorePreservesHistoryAndStopsNewWork(t *testing.T) {
	s := managementTestService(t)
	created := createManagementAutomation(t, s)
	input, _ := automationConfiguration(created.Source, created.Workflow)
	input.Source.FeedURL = "https://example.com/other.xml"
	input.Workflow.Definition.Parameters = map[string]any{"category": "电影"}
	updated, err := s.UpdateAutomation(created.Source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := s.ListAutomationRevisions(created.Source.ID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("revisions=%v err=%v", revisions, err)
	}
	restored, err := s.RestoreAutomationRevision(created.Source.ID, revisions[1].ID, updated.Workflow.Version)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Source.FeedURL != created.Source.FeedURL || restored.Source.Enabled || restored.Workflow.Enabled || restored.Workflow.Version != 3 {
		t.Fatalf("unexpected restore: %#v", restored)
	}
	if _, err := s.RestoreAutomationRevision(created.Source.ID, revisions[1].ID, 2); err == nil {
		t.Fatal("stale restore succeeded")
	}
	revisions, _ = s.ListAutomationRevisions(created.Source.ID)
	if len(revisions) != 3 {
		t.Fatalf("history was rewritten: %d", len(revisions))
	}
	other := createManagementAutomation(t, s)
	if _, err := s.RestoreAutomationRevision(other.Source.ID, revisions[0].ID, other.Workflow.Version); err == nil {
		t.Fatal("cross-workflow restore succeeded")
	}
	if err := s.DeleteAutomation(created.Source.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	s.db.Model(&model.AutomationRevision{}).Where("source_id = ?", created.Source.ID).Count(&count)
	if count != 0 {
		t.Fatal("orphan revisions remain")
	}
}

func TestAutomationSourceAndWorkflowEditsBothKeepRevisions(t *testing.T) {
	s := managementTestService(t)
	created := createManagementAutomation(t, s)
	input := automationSourceInputFromModel(created.Source)
	input.Name = "修改来源"
	if _, err := s.UpdateSource(created.Source.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpdateWorkflow(created.Workflow.ID, AutomationWorkflowInput{SourceID: created.Source.ID, Name: "修改流程", Definition: DefaultRSSAutomationDefinition()}); err != nil {
		t.Fatal(err)
	}
	revisions, err := s.ListAutomationRevisions(created.Source.ID)
	if err != nil || len(revisions) != 3 {
		t.Fatalf("history=%v err=%v", revisions, err)
	}
	if !strings.Contains(revisions[1].ConfigurationJSON, "修改来源") || strings.Contains(revisions[2].ConfigurationJSON, "修改来源") {
		t.Fatal("source revision overwritten")
	}
}

func TestAutomationBatchIsAtomicAndPreservesAlertSettings(t *testing.T) {
	s := managementTestService(t)
	created := createManagementAutomation(t, s)
	settings := defaultAutomationSettings(created.Source.ID)
	settings.AlertsEnabled, settings.FailureThreshold = true, 5
	if _, err := s.SaveAutomationSettings(created.Source.ID, settings); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchAutomations(AutomationBatchInput{SourceIDs: []uint{created.Source.ID, 999999}, Action: "disable"}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	var source model.AutomationSource
	s.db.First(&source, created.Source.ID)
	if !source.Enabled {
		t.Fatal("failed batch partially applied")
	}
	if err := s.BatchAutomations(AutomationBatchInput{SourceIDs: []uint{created.Source.ID}, Action: "group", GroupName: "动画"}); err != nil {
		t.Fatal(err)
	}
	s.db.First(&settings, "source_id = ?", created.Source.ID)
	if settings.GroupName != "动画" || !settings.AlertsEnabled || settings.FailureThreshold != 5 {
		t.Fatalf("settings lost: %+v", settings)
	}
	if err := s.BatchAutomations(AutomationBatchInput{SourceIDs: []uint{created.Source.ID}, Action: "disable"}); err != nil {
		t.Fatal(err)
	}
	var workflow model.AutomationWorkflow
	s.db.First(&workflow, created.Workflow.ID)
	s.db.First(&source, created.Source.ID)
	if workflow.Enabled || source.Enabled {
		t.Fatal("batch did not disable source and workflow together")
	}
}

func TestAutomationTemplateLifecycleDoesNotCreateRuns(t *testing.T) {
	s := managementTestService(t)
	created := createManagementAutomation(t, s)
	configuration, _ := automationConfiguration(created.Source, created.Workflow)
	configuration.Workflow.Definition.Parameters = map[string]any{"category": "电视"}
	template, err := s.SaveAutomationTemplate(0, AutomationTemplateInput{Name: "电视剧下载", Configuration: configuration})
	if err != nil || template.ID == 0 {
		t.Fatalf("save=%+v err=%v", template, err)
	}
	var restored AutomationCreateInput
	if err := json.Unmarshal([]byte(template.ConfigurationJSON), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Source.Enabled || restored.Workflow.Enabled || restored.Workflow.Definition.Parameters["category"] != "电视" {
		t.Fatal("template state incorrect")
	}
	if _, err := s.SaveAutomationTemplate(template.ID, AutomationTemplateInput{Name: "更新名称", Configuration: restored}); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListAutomationTemplates()
	if err != nil || len(items) != 1 || items[0].Name != "更新名称" {
		t.Fatalf("list=%v err=%v", items, err)
	}
	var count int64
	s.db.Model(&model.AutomationRun{}).Count(&count)
	if count != 0 {
		t.Fatal("saving a template started work")
	}
	if err := s.DeleteAutomationTemplate(template.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationParametersResolveTypedBindingsWithoutChangingDefinition(t *testing.T) {
	definition := DefaultRSSAutomationDefinition()
	definition.Parameters = map[string]any{"account": float64(42), "text": "动画", "paused": false}
	definition.Nodes[0].Config = map[string]any{"nested": map[string]any{"value": "$params.account"}, "label": "分类 {{params.text}}", "enabled": "$params.paused"}
	definition.Nodes[0].ParameterBindings = map[string]string{"target_id": "account"}
	raw, _ := json.Marshal(definition)
	resolved, err := resolveAutomationParameters(definition)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Nodes[0].Config["target_id"] != float64(42) || resolved.Nodes[0].Config["label"] != "分类 动画" || resolved.Nodes[0].Config["enabled"] != false {
		t.Fatalf("resolution=%v", resolved.Nodes[0])
	}
	unchanged, _ := json.Marshal(definition)
	if string(raw) != string(unchanged) {
		t.Fatal("editable references were replaced")
	}
	definition.Nodes[0].ParameterBindings["target_id"] = "missing"
	if ValidateAutomationDefinition(definition).Valid {
		t.Fatal("undefined parameter accepted")
	}
}

func TestAutomationParameterRunSnapshotSurvivesLaterEdits(t *testing.T) {
	s := managementTestService(t)
	created := createManagementAutomation(t, s)
	input, _ := automationConfiguration(created.Source, created.Workflow)
	input.Source.Enabled = false
	input.Workflow.Definition.Parameters = map[string]any{"caption": "运行时的值"}
	input.Workflow.Definition.Nodes = []AutomationNode{{ID: "trigger", Type: AutomationNodeTrigger}, {ID: "variable", Type: AutomationNodeSetVariable, Config: map[string]any{"variable": "caption", "value": "$params.caption", "value_type": "string"}}, {ID: "end", Type: AutomationNodeEnd}}
	input.Workflow.Definition.Edges = []AutomationEdge{{ID: "a", Source: "trigger", SourcePort: "next", Target: "variable"}, {ID: "b", Source: "variable", SourcePort: "success", Target: "end"}}
	updated, err := s.UpdateAutomation(created.Source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	entry := model.AutomationEntry{SourceID: created.Source.ID, Fingerprint: "param-entry", EventKey: "param-entry", FieldsJSON: `{}`}
	if err := s.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	run, _, err := s.createAutomationRun(updated.Workflow, entry, false)
	if err != nil {
		t.Fatal(err)
	}
	input.Workflow.Definition.Parameters["caption"] = "后来改的值"
	if _, err := s.UpdateAutomation(created.Source.ID, input); err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(s.Stop)
	completed := waitForAutomationRun(t, s.db, model.AutomationRunSucceeded)
	if completed.ID != run.ID {
		t.Fatal("unexpected run")
	}
	var node model.AutomationNodeRun
	s.db.Where("run_id = ? AND node_id = ?", run.ID, "variable").First(&node)
	if !strings.Contains(node.OutputJSON, "运行时的值") || strings.Contains(node.OutputJSON, "后来改的值") {
		t.Fatalf("snapshot lost: %s", node.OutputJSON)
	}
}

type managementNotifier struct {
	events []NotificationEvent
	fail   bool
}

func (n *managementNotifier) Ready(NotificationEventType) bool { return true }
func (n *managementNotifier) Publish(_ context.Context, event NotificationEvent) NotificationReport {
	n.events = append(n.events, event)
	return NotificationReport{Deliveries: []NotificationDelivery{{Channel: "test", Success: !n.fail, Error: map[bool]string{true: "offline"}[n.fail]}}}
}

func TestAutomationAlertsCooldownRecoveryAndDisabledSources(t *testing.T) {
	s := managementTestService(t)
	notifier := &managementNotifier{}
	s.notifier = notifier
	created := createManagementAutomation(t, s)
	settings := defaultAutomationSettings(created.Source.ID)
	settings.AlertsEnabled = true
	if _, err := s.SaveAutomationSettings(created.Source.ID, settings); err != nil {
		t.Fatal(err)
	}
	s.db.Model(&created.Source).Update("last_error", "来源无法访问")
	now := time.Now()
	for _, at := range []time.Time{now, now.Add(time.Minute)} {
		if err := s.checkAutomationHealth(context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}
	if len(notifier.events) != 1 {
		t.Fatalf("alert spam: %d", len(notifier.events))
	}
	s.db.Model(&created.Source).Update("last_error", "")
	if err := s.checkAutomationHealth(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.db.Model(&created.Source).Update("last_error", "再次失败")
	if err := s.checkAutomationHealth(context.Background(), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 2 {
		t.Fatal("recurrence was suppressed")
	}
	if _, err := s.SetAutomationEnabled(created.Source.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.checkAutomationHealth(context.Background(), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var active int64
	s.db.Model(&model.AutomationAlert{}).Where("active = ?", true).Count(&active)
	if len(notifier.events) != 2 || active != 0 {
		t.Fatal("paused workflow still alerts")
	}
}

func TestAutomationAlertsConsecutiveFailuresAndOnlyRunningNodes(t *testing.T) {
	s := managementTestService(t)
	notifier := &managementNotifier{fail: true}
	s.notifier = notifier
	created := createManagementAutomation(t, s)
	settings := defaultAutomationSettings(created.Source.ID)
	settings.AlertsEnabled = true
	settings.FailureThreshold = 2
	if _, err := s.SaveAutomationSettings(created.Source.ID, settings); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := s.db.Create(&model.AutomationRun{WorkflowID: created.Workflow.ID, EntryID: uint(i), ExecutionNo: 1, Status: model.AutomationRunFailed}).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	future := now.Add(time.Hour)
	running := model.AutomationRun{WorkflowID: created.Workflow.ID, EntryID: 3, ExecutionNo: 1, Status: model.AutomationRunRunning}
	s.db.Create(&running)
	s.db.Create(&model.AutomationNodeRun{RunID: running.ID, NodeID: "delay", NodeType: "delay", Status: model.AutomationNodePending, StartedAt: &old, NextAttemptAt: &future})
	if err := s.checkAutomationHealth(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 1 || notifier.events[0].Metadata["kind"] != "failed_runs" {
		t.Fatalf("unexpected alerts: %v", notifier.events)
	}
	s.db.Create(&model.AutomationNodeRun{RunID: running.ID, NodeID: "slow", NodeName: "慢节点", Status: model.AutomationNodeRunning, StartedAt: &old})
	if err := s.checkAutomationHealth(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 2 || notifier.events[1].Metadata["kind"] != "stuck_node" {
		t.Fatalf("stuck alert absent: %v", notifier.events)
	}
	var alert model.AutomationAlert
	s.db.Where("kind = ?", "failed_runs").First(&alert)
	if alert.LastError != "test: offline" || alert.LastSentAt != nil {
		t.Fatalf("failed delivery incorrectly reported: %+v", alert)
	}
}
