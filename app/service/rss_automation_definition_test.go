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

func TestValidateRSSAutomationDefinitionAccepts115WaitAndMoviePilotChain(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "offline", Type: RSSAutomationNodeOffline115OpenAPI, Config: map[string]any{
				"cloud_storage_id": 1, "url": "$item.download_url",
			}},
			{ID: "wait", Type: RSSAutomationNodeWait115, Config: map[string]any{
				"poll_interval_seconds": 30, "max_wait_minutes": 10080,
			}},
			{ID: "mp", Type: RSSAutomationNodeMoviePilotRecognize, Config: map[string]any{"tmdb_id": "{{item.tmdb_id}}"}},
			{ID: "organize", Type: RSSAutomationNodeOrganizeStrm, Config: map[string]any{"cloud_directory_id": 1, "media_type": "auto"}},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "offline"},
			{ID: "e2", Source: "offline", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "mp"},
			{ID: "e4", Source: "mp", SourcePort: "success", Target: "organize"},
			{ID: "e5", Source: "organize", SourcePort: "success", Target: "end"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("115/MP chain invalid: %#v", result.Errors)
	}
	definition.Nodes[4].MaxAttempts = 2
	result = ValidateRSSAutomationDefinition(definition)
	if result.Valid || !containsRSSAutomationValidationError(result.Errors, "不能自动重试") {
		t.Fatalf("organize retry safeguard missing: %#v", result.Errors)
	}
}

func TestRSSAutomationNodeMaxAttemptsKeepsSideEffectsSingleAttempt(t *testing.T) {
	tests := []struct {
		name string
		node RSSAutomationNode
		want int
	}{
		{name: "organize", node: RSSAutomationNode{Type: RSSAutomationNodeOrganizeStrm}, want: 1},
		{name: "strm regenerate", node: RSSAutomationNode{Type: RSSAutomationNodeStrmRegenerate}, want: 1},
		{name: "http request", node: RSSAutomationNode{Type: RSSAutomationNodeHTTPRequest}, want: 1},
		{name: "MP2 transfer", node: RSSAutomationNode{Type: RSSAutomationNodeMoviePilotTransfer}, want: 1},
		{name: "qBittorrent delete", node: RSSAutomationNode{Type: RSSAutomationNodeDeleteQBittorrent}, want: 1},
		{name: "qBittorrent", node: RSSAutomationNode{Type: RSSAutomationNodeQBittorrent}, want: 3},
		{name: "explicit", node: RSSAutomationNode{Type: RSSAutomationNodeQBittorrent, MaxAttempts: 2}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rssAutomationNodeMaxAttempts(tt.node); got != tt.want {
				t.Fatalf("rssAutomationNodeMaxAttempts() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidateRSSAutomationWait115RejectsTooFrequentPolling(t *testing.T) {
	err := validateRSSAutomationNodeConfig(RSSAutomationNode{
		Type: RSSAutomationNodeWait115,
		Config: map[string]any{
			"poll_interval_seconds": 1,
			"max_wait_minutes":      60,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "不能少于 5 秒") {
		t.Fatalf("unexpected wait config result: %v", err)
	}
}

func TestValidateRSSAutomationDefinitionRequiresMediaNodePredecessors(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "notify", Type: RSSAutomationNodeNotification, Config: map[string]any{"message": "test"}},
			{ID: "wait", Type: RSSAutomationNodeWait115},
			{ID: "mp", Type: RSSAutomationNodeMoviePilotRecognize},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "notify"},
			{ID: "e2", Source: "notify", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "mp"},
			{ID: "e4", Source: "mp", SourcePort: "success", Target: "end"},
		},
	}
	result := ValidateRSSAutomationDefinition(definition)
	if result.Valid || !containsRSSAutomationValidationError(result.Errors, "必须直接连接在 115 离线节点之后") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestValidateRSSAutomationDefinitionAllowsMoviePilotTitleBeforeDownload(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "mp_title", Type: RSSAutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title", "tmdb_id": "{{item.tmdb_id}}"}},
			{ID: "end_success", Type: RSSAutomationNodeEnd},
			{ID: "end_failure", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "mp_title"},
			{ID: "e2", Source: "mp_title", SourcePort: "success", Target: "end_success"},
			{ID: "e3", Source: "mp_title", SourcePort: "failure", Target: "end_failure"},
		},
	}
	if result := ValidateRSSAutomationDefinition(definition); !result.Valid {
		t.Fatalf("title recognition definition invalid: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionAllowsFilmFusionTitleRecognitionAnywhere(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "local", Type: RSSAutomationNodeFilmFusionRecognize, Config: map[string]any{
				"recognition_mode": "title", "input": "$item.title", "lookup_tmdb": true,
			}},
			{ID: "end_success", Type: RSSAutomationNodeEnd},
			{ID: "end_failure", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "local"},
			{ID: "e2", Source: "local", SourcePort: "success", Target: "end_success"},
			{ID: "e3", Source: "local", SourcePort: "failure", Target: "end_failure"},
		},
	}
	if result := ValidateRSSAutomationDefinition(definition); !result.Valid {
		t.Fatalf("FilmFusion title recognition definition invalid: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionAcceptsFilmFusionFileRecognitionChain(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "offline", Type: RSSAutomationNodeOffline115OpenAPI, Config: map[string]any{"cloud_storage_id": 1, "url": "$item.download_url"}},
			{ID: "wait", Type: RSSAutomationNodeWait115},
			{ID: "local", Type: RSSAutomationNodeFilmFusionRecognize, Config: map[string]any{"recognition_mode": "file", "lookup_tmdb": false}},
			{ID: "organize", Type: RSSAutomationNodeOrganizeStrm, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "offline"},
			{ID: "e2", Source: "offline", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "local"},
			{ID: "e4", Source: "local", SourcePort: "success", Target: "organize"},
			{ID: "e5", Source: "organize", SourcePort: "success", Target: "end"},
		},
	}
	if result := ValidateRSSAutomationDefinition(definition); !result.Valid {
		t.Fatalf("FilmFusion file recognition chain invalid: %#v", result.Errors)
	}

	definition.Edges[2].Source = "offline"
	if result := ValidateRSSAutomationDefinition(definition); result.Valid || !containsRSSAutomationValidationError(result.Errors, "115 下载文件模式必须直接连接") {
		t.Fatalf("FilmFusion file predecessor safeguard missing: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionRejectsInvalidFilmFusionRecognitionConfig(t *testing.T) {
	base := RSSAutomationNode{ID: "local", Type: RSSAutomationNodeFilmFusionRecognize, Config: map[string]any{"recognition_mode": "title", "input": "$item.title"}}
	if err := validateRSSAutomationNodeConfig(base); err != nil {
		t.Fatalf("valid local recognition config rejected: %v", err)
	}

	invalidMode := base
	invalidMode.Config = map[string]any{"recognition_mode": "directory", "input": "$item.title"}
	if err := validateRSSAutomationNodeConfig(invalidMode); err == nil || !strings.Contains(err.Error(), "title/file") {
		t.Fatalf("invalid mode error = %v", err)
	}

	missingInput := base
	missingInput.Config = map[string]any{"recognition_mode": "title"}
	if err := validateRSSAutomationNodeConfig(missingInput); err == nil || !strings.Contains(err.Error(), "待识别标题") {
		t.Fatalf("missing title input error = %v", err)
	}

	invalidLookup := base
	invalidLookup.Config = map[string]any{"recognition_mode": "file", "lookup_tmdb": "yes"}
	if err := validateRSSAutomationNodeConfig(invalidLookup); err == nil || !strings.Contains(err.Error(), "布尔值") {
		t.Fatalf("invalid lookup_tmdb error = %v", err)
	}
}

func TestValidateRSSAutomationDefinitionAcceptsSuggestedQBitMediaChain(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "mp_title", Type: RSSAutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title"}},
			{ID: "dedupe", Type: RSSAutomationNodeMediaExists, Config: map[string]any{
				"cloud_directory_id": 1, "tmdb_id": "{{nodes.mp_title.output.tmdb_id}}",
			}},
			{ID: "query", Type: RSSAutomationNodeHDHiveQuery, Config: map[string]any{
				"tmdb_id": "{{nodes.mp_title.output.tmdb_id}}", "media_type": "{{nodes.mp_title.output.media_type}}",
			}},
			{ID: "unlock", Type: RSSAutomationNodeHDHiveUnlock, Config: map[string]any{"slug": "{{nodes.query.output.selected_slug}}"}},
			{ID: "qb", Type: RSSAutomationNodeQBittorrent, Config: map[string]any{
				"target_id": 1, "url": "{{nodes.unlock.output.download_url}}",
			}},
			{ID: "wait_qb", Type: RSSAutomationNodeWaitQBittorrent, Config: map[string]any{"poll_interval_seconds": 30}},
			{ID: "mp_transfer", Type: RSSAutomationNodeMoviePilotTransfer, Config: map[string]any{"file_type": "auto", "media_type": "auto"}},
			{ID: "delete_qb", Type: RSSAutomationNodeDeleteQBittorrent, Config: map[string]any{"delete_files": false}},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "mp_title"},
			{ID: "e2", Source: "mp_title", SourcePort: "success", Target: "dedupe"},
			{ID: "e3", Source: "dedupe", SourcePort: "missing", Target: "query"},
			{ID: "e4", Source: "query", SourcePort: "found", Target: "unlock"},
			{ID: "e5", Source: "unlock", SourcePort: "success", Target: "qb"},
			{ID: "e6", Source: "qb", SourcePort: "success", Target: "wait_qb"},
			{ID: "e7", Source: "wait_qb", SourcePort: "success", Target: "mp_transfer"},
			{ID: "e8", Source: "mp_transfer", SourcePort: "success", Target: "delete_qb"},
			{ID: "e9", Source: "delete_qb", SourcePort: "success", Target: "end"},
		},
	}
	if result := ValidateRSSAutomationDefinition(definition); !result.Valid {
		t.Fatalf("suggested qB media chain invalid: %#v", result.Errors)
	}
	unsafeDelete := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "qb", Type: RSSAutomationNodeQBittorrent, Config: map[string]any{"target_id": 1, "url": "$item.download_url"}},
			{ID: "wait", Type: RSSAutomationNodeWaitQBittorrent},
			{ID: "delete", Type: RSSAutomationNodeDeleteQBittorrent, Config: map[string]any{"delete_files": true}},
			{ID: "end", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "u1", Source: "trigger", SourcePort: "next", Target: "qb"},
			{ID: "u2", Source: "qb", SourcePort: "success", Target: "wait"},
			{ID: "u3", Source: "wait", SourcePort: "success", Target: "delete"},
			{ID: "u4", Source: "delete", SourcePort: "success", Target: "end"},
		},
	}
	if result := ValidateRSSAutomationDefinition(unsafeDelete); result.Valid || !containsRSSAutomationValidationError(result.Errors, "MP2 整理成功后") {
		t.Fatalf("qB file deletion safeguard missing: %#v", result.Errors)
	}
}

func TestValidateRSSAutomationDefinitionAcceptsStrmVerifyAndRegenerateBranches(t *testing.T) {
	definition := RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger},
			{ID: "offline", Type: RSSAutomationNodeOffline115OpenAPI, Config: map[string]any{"cloud_storage_id": 1, "url": "$item.download_url"}},
			{ID: "wait", Type: RSSAutomationNodeWait115},
			{ID: "organize", Type: RSSAutomationNodeOrganizeStrm, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "verify", Type: RSSAutomationNodeStrmVerify, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "regenerate", Type: RSSAutomationNodeStrmRegenerate, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "end_valid", Type: RSSAutomationNodeEnd},
			{ID: "end_regenerated", Type: RSSAutomationNodeEnd},
		},
		Edges: []RSSAutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "offline"},
			{ID: "e2", Source: "offline", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "organize"},
			{ID: "e4", Source: "organize", SourcePort: "success", Target: "verify"},
			{ID: "e5", Source: "verify", SourcePort: "valid", Target: "end_valid"},
			{ID: "e6", Source: "verify", SourcePort: "invalid", Target: "regenerate"},
			{ID: "e7", Source: "regenerate", SourcePort: "success", Target: "end_regenerated"},
		},
	}
	if result := ValidateRSSAutomationDefinition(definition); !result.Valid {
		t.Fatalf("STRM verify/regenerate definition invalid: %#v", result.Errors)
	}
	definition.Edges[5].SourcePort = "valid"
	if result := ValidateRSSAutomationDefinition(definition); result.Valid || !containsRSSAutomationValidationError(result.Errors, "“无效”出口") {
		t.Fatalf("STRM regenerate predecessor safeguard missing: %#v", result.Errors)
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
