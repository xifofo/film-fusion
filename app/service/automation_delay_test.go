package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"
)

func TestAutomationDelayDurationValidation(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		wantErr string
	}{
		{name: "one second", value: 1},
		{name: "thirty days", value: automationMaxDelaySeconds},
		{name: "missing", wantErr: "必须配置"},
		{name: "zero", value: 0, wantErr: "不能少于"},
		{name: "fractional", value: 1.5, wantErr: "整数秒"},
		{name: "too long", value: automationMaxDelaySeconds + 1, wantErr: "不能超过"},
		{name: "string", value: "60", wantErr: "整数秒"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]any{}
			if tt.value != nil {
				config["delay_seconds"] = tt.value
			}
			_, err := automationDelayDuration(config)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("automationDelayDuration() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("automationDelayDuration() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestExecuteAutomationDelayKeepsDurableResumeTime(t *testing.T) {
	startedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	node := AutomationNode{Type: AutomationNodeDelay, Config: map[string]any{"delay_seconds": 600}}
	output, err := executeAutomationDelay(model.AutomationNodeRun{}, node, startedAt)
	var deferred *automationNodeDeferred
	if !errors.As(err, &deferred) {
		t.Fatalf("first delay execution error = %v, want deferred", err)
	}
	if deferred.delay != 10*time.Minute {
		t.Fatalf("first delay = %v, want 10m", deferred.delay)
	}
	if output["resume_at"] != startedAt.Add(10*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("resume_at = %#v", output["resume_at"])
	}

	encoded, marshalErr := json.Marshal(output)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	nodeRun := model.AutomationNodeRun{OutputJSON: string(encoded)}
	output, err = executeAutomationDelay(nodeRun, node, startedAt.Add(4*time.Minute))
	if !errors.As(err, &deferred) || deferred.delay != 6*time.Minute {
		t.Fatalf("resumed delay = %v, %v, want 6m", deferred, err)
	}
	if output["resume_at"] != startedAt.Add(10*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("durable resume_at changed: %#v", output["resume_at"])
	}

	output, err = executeAutomationDelay(nodeRun, node, startedAt.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("completed delay error = %v", err)
	}
	if output["selected_port"] != "success" || output["resumed_at"] == nil {
		t.Fatalf("completed delay output = %#v", output)
	}
}

func TestAutomationPredecessorOutputSkipsSuccessfulDelayNodes(t *testing.T) {
	definition := AutomationDefinition{
		Nodes: []AutomationNode{
			{ID: "qb", Type: AutomationNodeQBittorrent},
			{ID: "delay", Type: AutomationNodeDelay},
			{ID: "wait", Type: AutomationNodeWaitQBittorrent},
		},
		Edges: []AutomationEdge{
			{Source: "qb", SourcePort: "success", Target: "delay"},
			{Source: "delay", SourcePort: "success", Target: "wait"},
		},
	}
	runContext := map[string]any{"nodes": map[string]any{
		"qb":    map[string]any{"output": map[string]any{"target_id": 7, "torrent_tag": "delay-test"}},
		"delay": map[string]any{"output": map[string]any{"delay_seconds": 600}},
	}}
	predecessorID, output, err := automationPredecessorOutput("wait", definition, runContext)
	if err != nil {
		t.Fatal(err)
	}
	if predecessorID != "qb" || output["target_id"] != 7 || output["torrent_tag"] != "delay-test" {
		t.Fatalf("predecessor = %s, output = %#v", predecessorID, output)
	}
}
