package service

import (
	"strings"
	"testing"
)

func TestValidateRSSAutomationDefinitionRequiresExplicitParallelNode(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "notify_a", Type: RSSAutomationNodeNotification, Config: map[string]any{"message": "a"}},
			{ID: "notify_b", Type: RSSAutomationNodeNotification, Config: map[string]any{"message": "b"}},
			{ID: "end_a", Type: RSSAutomationNodeEnd},
			{ID: "end_b", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "notify_a"},
			{ID: "e2", Source: "trigger", SourcePort: "next", Target: "notify_b"},
			{ID: "e3", Source: "notify_a", SourcePort: "success", Target: "end_a"},
			{ID: "e4", Source: "notify_b", SourcePort: "success", Target: "end_b"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if result.Valid {
		t.Fatalf("definition unexpectedly valid: %#v", result)
	}
	if !containsRSSAutomationValidationError(result.Errors, "显式使用并行节点") {
		t.Fatalf("missing explicit parallel error: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionAcceptsIfParallelAndJoin(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "if_episode", Type: RSSAutomationNodeIf, Config: map[string]any{"condition": map[string]any{"field": "$vars.episode", "operator": "gt", "value": 1000}}},
			{ID: "parallel", Type: RSSAutomationNodeParallel},
			{ID: "notify", Type: RSSAutomationNodeNotification, Config: map[string]any{"message": "{{item.title}}"}},
			{ID: "qb", Type: RSSAutomationNodeQBittorrent, Config: map[string]any{"target_id": 1, "url": "$item.download_url"}},
			{ID: "join", Type: RSSAutomationNodeJoin, Config: map[string]any{"policy": "all_completed"}},
			{ID: "end_pass", Type: RSSAutomationNodeEnd},
			{ID: "end_reject", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "if_episode"},
			{ID: "e2", Source: "if_episode", SourcePort: "true", Target: "parallel"},
			{ID: "e3", Source: "if_episode", SourcePort: "false", Target: "end_reject"},
			{ID: "e4", Source: "parallel", SourcePort: "branch-notify", Target: "notify"},
			{ID: "e5", Source: "parallel", SourcePort: "branch-qb", Target: "qb"},
			{ID: "e6", Source: "notify", SourcePort: "success", Target: "join"},
			{ID: "e7", Source: "qb", SourcePort: "success", Target: "join"},
			{ID: "e8", Source: "join", SourcePort: "success", Target: "end_pass"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("definition invalid: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionRejectsMalformedCondition(t *testing.T) {
	definition := DefaultRSSAutomationDefinition()
	definition.Nodes = append(definition.Nodes[:1], RSSAutomationNode{
		ID: "if_bad", Type: RSSAutomationNodeIf,
		Config: map[string]any{"condition": map[string]any{"field": "$vars.episode", "operator": "wat", "value": 1}},
	})
	definition.Nodes = append(definition.Nodes, RSSAutomationNode{ID: "end", Type: RSSAutomationNodeEnd, Name: "结束"})
	definition.Edges = []RSSAutomationEdge{
		{ID: "e1", Source: "trigger", SourcePort: "next", Target: "if_bad"},
		{ID: "e2", Source: "if_bad", SourcePort: "true", Target: "end"},
		{ID: "e3", Source: "if_bad", SourcePort: "false", Target: "end"},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if result.Valid || !containsRSSAutomationValidationError(result.Errors, "不支持的 IF 条件操作符") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestValidateRSSAutomationDefinitionAcceptsSharedEnd(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "if_title", Type: RSSAutomationNodeIf, Config: map[string]any{
				"condition": map[string]any{"field": "$item.title", "operator": "eq", "value": "示例"},
			}},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "if_title"},
			{ID: "e2", Source: "if_title", SourcePort: "true", Target: "end"},
			{ID: "e3", Source: "if_title", SourcePort: "false", Target: "end"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("shared end definition invalid: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionAcceptsKeywordBranches(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "keywords", Type: RSSAutomationNodeKeyword, Config: map[string]any{
				"input": "$item.title", "keywords": []any{"CAM", "TS"},
				"match_mode": "contains_none", "case_sensitive": false,
			}},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "keywords"},
			{ID: "e2", Source: "keywords", SourcePort: "matched", Target: "end"},
			{ID: "e3", Source: "keywords", SourcePort: "unmatched", Target: "end"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("keyword definition invalid: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionRequiresConditionalJoinOutcomes(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "parallel", Type: RSSAutomationNodeParallel, Config: map[string]any{
				"branches": []any{"branch-a", "branch-b"},
			}},
			{ID: "join", Type: RSSAutomationNodeJoin, Config: map[string]any{"policy": "all_success"}},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "parallel"},
			{ID: "e2", Source: "parallel", SourcePort: "branch-a", Target: "join"},
			{ID: "e3", Source: "parallel", SourcePort: "branch-b", Target: "join"},
			{ID: "e4", Source: "join", SourcePort: "success", Target: "end"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if result.Valid || !containsRSSAutomationValidationError(result.Errors, "缺少“未满足”出口") {
		t.Fatalf("unexpected validation result: %#v", result)
	}

	definition.Edges = append(definition.Edges, RSSAutomationEdge{
		ID: "e5", Source: "join", SourcePort: "failure", Target: "end",
	})
	result = ValidateRSSAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("conditional join definition invalid: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionRequiresEveryConfiguredParallelBranch(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "parallel", Type: RSSAutomationNodeParallel, Config: map[string]any{"branches": []any{"branch-a", "branch-b", "branch-c"}}},
			{ID: "end_a", Type: RSSAutomationNodeEnd},
			{ID: "end_b", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "parallel"},
			{ID: "e2", Source: "parallel", SourcePort: "branch-a", Target: "end_a"},
			{ID: "e3", Source: "parallel", SourcePort: "branch-b", Target: "end_b"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if result.Valid || !containsRSSAutomationValidationError(result.Errors, "分支 branch-c 尚未连接") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestRSSAutomationSourcePortsAreNodeSpecific(t *testing.T) {
	if isRSSAutomationSourcePortValid(RSSAutomationNodeTrigger, "always") {
		t.Fatal("trigger must only use the next port")
	}
	if isRSSAutomationSourcePortValid(RSSAutomationNodeParallel, "always") {
		t.Fatal("parallel must only use configured branch ports")
	}
	if !isRSSAutomationSourcePortValid(RSSAutomationNodeNotification, "always") {
		t.Fatal("action nodes should support an unconditional continuation")
	}
	if !isRSSAutomationSourcePortValid(RSSAutomationNodeOffline115OpenAPI, "success") {
		t.Fatal("115 OpenAPI action should expose success and failure ports")
	}
	if err := validateRSSAutomationNodeConfig(RSSAutomationNode{
		Type: RSSAutomationNodeOffline115OpenAPI,
		Config: map[string]any{
			"cloud_storage_id": 1,
			"url":              "$item.download_url",
		},
	}); err != nil {
		t.Fatalf("115 OpenAPI action config rejected: %v", err)
	}
	if !isRSSAutomationSourcePortValid(RSSAutomationNodeKeyword, "matched") ||
		!isRSSAutomationSourcePortValid(RSSAutomationNodeKeyword, "unmatched") {
		t.Fatal("keyword nodes should expose matched and unmatched ports")
	}
}

func TestValidateRSSAutomationDefinitionRejectsUnknownTargetPort(t *testing.T) {
	definition := DefaultRSSAutomationDefinition()
	definition.Edges[0].TargetPort = "side-door"
	result := ValidateRSSAutomationDefinition(definition)
	if result.Valid || !containsRSSAutomationValidationError(result.Errors, "入口 \"side-door\" 无效") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func containsRSSAutomationValidationError(errors []string, fragment string) bool {
	for _, validationError := range errors {
		if strings.Contains(validationError, fragment) {
			return true
		}
	}
	return false
}
