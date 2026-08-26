package service

import (
	"strings"
	"testing"
)

func TestValidateAutomationDefinitionRequiresExplicitParallelNode(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "notify_a", Type: AutomationNodeNotification, Config: map[string]any{"message": "a"}},
			{ID: "notify_b", Type: AutomationNodeNotification, Config: map[string]any{"message": "b"}},
			{ID: "end_a", Type: AutomationNodeEnd},
			{ID: "end_b", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "notify_a"},
			{ID: "e2", Source: "trigger", SourcePort: "next", Target: "notify_b"},
			{ID: "e3", Source: "notify_a", SourcePort: "success", Target: "end_a"},
			{ID: "e4", Source: "notify_b", SourcePort: "success", Target: "end_b"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if result.Valid {
		t.Fatalf("definition unexpectedly valid: %#v", result)
	}
	if !containsAutomationValidationError(result.Errors, "显式使用并行节点") {
		t.Fatalf("missing explicit parallel error: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionAcceptsIfParallelAndJoin(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "if_episode", Type: AutomationNodeIf, Config: map[string]any{"condition": map[string]any{"field": "$vars.episode", "operator": "gt", "value": 1000}}},
			{ID: "parallel", Type: AutomationNodeParallel},
			{ID: "notify", Type: AutomationNodeNotification, Config: map[string]any{"message": "{{item.title}}"}},
			{ID: "qb", Type: AutomationNodeQBittorrent, Config: map[string]any{"target_id": 1, "url": "$item.download_url"}},
			{ID: "join", Type: AutomationNodeJoin, Config: map[string]any{"policy": "all_completed"}},
			{ID: "end_pass", Type: AutomationNodeEnd},
			{ID: "end_reject", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
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
	result := ValidateAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("definition invalid: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionRejectsMalformedCondition(t *testing.T) {
	definition := DefaultAutomationDefinition()
	definition.Nodes = append(definition.Nodes[:1], AutomationNode{
		ID: "if_bad", Type: AutomationNodeIf,
		Config: map[string]any{"condition": map[string]any{"field": "$vars.episode", "operator": "wat", "value": 1}},
	})
	definition.Nodes = append(definition.Nodes, AutomationNode{ID: "end", Type: AutomationNodeEnd, Name: "结束"})
	definition.Edges = []AutomationEdge{
		{ID: "e1", Source: "trigger", SourcePort: "next", Target: "if_bad"},
		{ID: "e2", Source: "if_bad", SourcePort: "true", Target: "end"},
		{ID: "e3", Source: "if_bad", SourcePort: "false", Target: "end"},
	}
	result := ValidateAutomationDefinition(definition)
	if result.Valid || !containsAutomationValidationError(result.Errors, "不支持的 IF 条件操作符") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestValidateAutomationDefinitionAcceptsSharedEnd(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "if_title", Type: AutomationNodeIf, Config: map[string]any{
				"condition": map[string]any{"field": "$item.title", "operator": "eq", "value": "示例"},
			}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "if_title"},
			{ID: "e2", Source: "if_title", SourcePort: "true", Target: "end"},
			{ID: "e3", Source: "if_title", SourcePort: "false", Target: "end"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("shared end definition invalid: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionAcceptsKeywordBranches(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "keywords", Type: AutomationNodeKeyword, Config: map[string]any{
				"input": "$item.title", "keywords": []any{"CAM", "TS"},
				"match_mode": "contains_none", "case_sensitive": false,
			}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "keywords"},
			{ID: "e2", Source: "keywords", SourcePort: "matched", Target: "end"},
			{ID: "e3", Source: "keywords", SourcePort: "unmatched", Target: "end"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("keyword definition invalid: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionRequiresConditionalJoinOutcomes(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "parallel", Type: AutomationNodeParallel, Config: map[string]any{
				"branches": []any{"branch-a", "branch-b"},
			}},
			{ID: "join", Type: AutomationNodeJoin, Config: map[string]any{"policy": "all_success"}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "parallel"},
			{ID: "e2", Source: "parallel", SourcePort: "branch-a", Target: "join"},
			{ID: "e3", Source: "parallel", SourcePort: "branch-b", Target: "join"},
			{ID: "e4", Source: "join", SourcePort: "success", Target: "end"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if result.Valid || !containsAutomationValidationError(result.Errors, "缺少“未满足”出口") {
		t.Fatalf("unexpected validation result: %#v", result)
	}

	definition.Edges = append(definition.Edges, AutomationEdge{
		ID: "e5", Source: "join", SourcePort: "failure", Target: "end",
	})
	result = ValidateAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("conditional join definition invalid: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionRequiresEveryConfiguredParallelBranch(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "parallel", Type: AutomationNodeParallel, Config: map[string]any{"branches": []any{"branch-a", "branch-b", "branch-c"}}},
			{ID: "end_a", Type: AutomationNodeEnd},
			{ID: "end_b", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "parallel"},
			{ID: "e2", Source: "parallel", SourcePort: "branch-a", Target: "end_a"},
			{ID: "e3", Source: "parallel", SourcePort: "branch-b", Target: "end_b"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if result.Valid || !containsAutomationValidationError(result.Errors, "分支 branch-c 尚未连接") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestAutomationSourcePortsAreNodeSpecific(t *testing.T) {
	if isAutomationSourcePortValid(AutomationNodeTrigger, "always") {
		t.Fatal("trigger must only use the next port")
	}
	if isAutomationSourcePortValid(AutomationNodeParallel, "always") {
		t.Fatal("parallel must only use configured branch ports")
	}
	if !isAutomationSourcePortValid(AutomationNodeNotification, "always") {
		t.Fatal("action nodes should support an unconditional continuation")
	}
	if !isAutomationSourcePortValid(AutomationNodeOffline115OpenAPI, "success") {
		t.Fatal("115 OpenAPI action should expose success and failure ports")
	}
	if err := validateAutomationNodeConfig(AutomationNode{
		Type: AutomationNodeOffline115OpenAPI,
		Config: map[string]any{
			"cloud_storage_id": 1,
			"url":              "$item.download_url",
		},
	}); err != nil {
		t.Fatalf("115 OpenAPI action config rejected: %v", err)
	}
	if !isAutomationSourcePortValid(AutomationNodeRename115OpenAPI, "success") ||
		!isAutomationSourcePortValid(AutomationNodeRename115OpenAPI, "failure") {
		t.Fatal("115 OpenAPI rename action should expose success and failure ports")
	}
	if err := validateAutomationNodeConfig(AutomationNode{
		Type: AutomationNodeRename115OpenAPI,
		Config: map[string]any{
			"cloud_storage_id": 1,
			"file_id":          "$nodes.wait.output.file_id",
			"new_name":         "{{item.title}}.mkv",
		},
	}); err != nil {
		t.Fatalf("115 OpenAPI rename config rejected: %v", err)
	}
	if err := validateAutomationNodeConfig(AutomationNode{
		Type: AutomationNodeRename115OpenAPI,
		Config: map[string]any{
			"cloud_storage_id": 1,
			"file_id":          "123456",
			"new_name":         "剧集/第一集.mkv",
		},
	}); err == nil || !strings.Contains(err.Error(), "路径分隔符") {
		t.Fatalf("invalid 115 rename name was accepted: %v", err)
	}
	if !isAutomationSourcePortValid(AutomationNodeKeyword, "matched") ||
		!isAutomationSourcePortValid(AutomationNodeKeyword, "unmatched") {
		t.Fatal("keyword nodes should expose matched and unmatched ports")
	}
}

func TestValidateAutomationDefinitionAccepts115WaitAndMoviePilotChain(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "offline", Type: AutomationNodeOffline115OpenAPI, Config: map[string]any{
				"cloud_storage_id": 1, "url": "$item.download_url",
			}},
			{ID: "wait", Type: AutomationNodeWait115, Config: map[string]any{
				"poll_interval_seconds": 30, "max_wait_minutes": 10080,
			}},
			{ID: "mp", Type: AutomationNodeMoviePilotRecognize, Config: map[string]any{"tmdb_id": "{{item.tmdb_id}}"}},
			{ID: "organize", Type: AutomationNodeOrganizeStrm, Config: map[string]any{"cloud_directory_id": 1, "media_type": "auto"}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "offline"},
			{ID: "e2", Source: "offline", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "mp"},
			{ID: "e4", Source: "mp", SourcePort: "success", Target: "organize"},
			{ID: "e5", Source: "organize", SourcePort: "success", Target: "end"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if !result.Valid {
		t.Fatalf("115/MP chain invalid: %#v", result.Errors)
	}
	definition.Nodes[4].MaxAttempts = 2
	result = ValidateAutomationDefinition(definition)
	if result.Valid || !containsAutomationValidationError(result.Errors, "不能自动重试") {
		t.Fatalf("organize retry safeguard missing: %#v", result.Errors)
	}
}

func TestAutomationNodeMaxAttemptsKeepsSideEffectsSingleAttempt(t *testing.T) {
	tests := []struct {
		name string
		node AutomationNode
		want int
	}{
		{name: "organize", node: AutomationNode{Type: AutomationNodeOrganizeStrm}, want: 1},
		{name: "strm regenerate", node: AutomationNode{Type: AutomationNodeStrmRegenerate}, want: 1},
		{name: "http request", node: AutomationNode{Type: AutomationNodeHTTPRequest}, want: 1},
		{name: "MP2 transfer", node: AutomationNode{Type: AutomationNodeMoviePilotTransfer}, want: 1},
		{name: "qBittorrent delete", node: AutomationNode{Type: AutomationNodeDeleteQBittorrent}, want: 1},
		{name: "qBittorrent", node: AutomationNode{Type: AutomationNodeQBittorrent}, want: 3},
		{name: "explicit", node: AutomationNode{Type: AutomationNodeQBittorrent, MaxAttempts: 2}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := automationNodeMaxAttempts(tt.node); got != tt.want {
				t.Fatalf("automationNodeMaxAttempts() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidateAutomationWait115RejectsTooFrequentPolling(t *testing.T) {
	err := validateAutomationNodeConfig(AutomationNode{
		Type: AutomationNodeWait115,
		Config: map[string]any{
			"poll_interval_seconds": 1,
			"max_wait_minutes":      60,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "不能少于 5 秒") {
		t.Fatalf("unexpected wait config result: %v", err)
	}
}

func TestValidateAutomationDefinitionRequiresMediaNodePredecessors(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "notify", Type: AutomationNodeNotification, Config: map[string]any{"message": "test"}},
			{ID: "wait", Type: AutomationNodeWait115},
			{ID: "mp", Type: AutomationNodeMoviePilotRecognize},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "notify"},
			{ID: "e2", Source: "notify", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "mp"},
			{ID: "e4", Source: "mp", SourcePort: "success", Target: "end"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if result.Valid || !containsAutomationValidationError(result.Errors, "必须直接连接在 115 离线节点之后") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestValidateAutomationDefinitionAllowsMoviePilotTitleBeforeDownload(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "mp_title", Type: AutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title", "tmdb_id": "{{item.tmdb_id}}"}},
			{ID: "end_success", Type: AutomationNodeEnd},
			{ID: "end_failure", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "mp_title"},
			{ID: "e2", Source: "mp_title", SourcePort: "success", Target: "end_success"},
			{ID: "e3", Source: "mp_title", SourcePort: "failure", Target: "end_failure"},
		},
	}
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("title recognition definition invalid: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionAllowsFilmFusionTitleRecognitionAnywhere(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "local", Type: AutomationNodeFilmFusionRecognize, Config: map[string]any{
				"recognition_mode": "title", "input": "$item.title", "lookup_tmdb": true,
			}},
			{ID: "end_success", Type: AutomationNodeEnd},
			{ID: "end_failure", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "local"},
			{ID: "e2", Source: "local", SourcePort: "success", Target: "end_success"},
			{ID: "e3", Source: "local", SourcePort: "failure", Target: "end_failure"},
		},
	}
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("FilmFusion title recognition definition invalid: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionAcceptsFilmFusionFileRecognitionChain(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "offline", Type: AutomationNodeOffline115OpenAPI, Config: map[string]any{"cloud_storage_id": 1, "url": "$item.download_url"}},
			{ID: "wait", Type: AutomationNodeWait115},
			{ID: "local", Type: AutomationNodeFilmFusionRecognize, Config: map[string]any{"recognition_mode": "file", "lookup_tmdb": false}},
			{ID: "organize", Type: AutomationNodeOrganizeStrm, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "offline"},
			{ID: "e2", Source: "offline", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "local"},
			{ID: "e4", Source: "local", SourcePort: "success", Target: "organize"},
			{ID: "e5", Source: "organize", SourcePort: "success", Target: "end"},
		},
	}
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("FilmFusion file recognition chain invalid: %#v", result.Errors)
	}

	definition.Edges[2].Source = "offline"
	if result := ValidateAutomationDefinition(definition); result.Valid || !containsAutomationValidationError(result.Errors, "115 文件模式必须连接在 115 目录触发器或等待节点之后") {
		t.Fatalf("FilmFusion file predecessor safeguard missing: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionAcceptsDirectoryMonitorActionChain(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "recognize", Type: AutomationNodeFilmFusionRecognize, Config: map[string]any{
				"recognition_mode": "file", "lookup_tmdb": true,
			}},
			{ID: "organize", Type: AutomationNodeOrganizeStrm, Config: map[string]any{
				"cloud_directory_id": 9, "media_type": "auto",
			}},
			{ID: "notification", Type: AutomationNodeNotification, Config: map[string]any{
				"title": "115 目录发现新媒体", "message": "{{item.path}}",
			}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "recognize"},
			{ID: "e2", Source: "recognize", SourcePort: "success", Target: "organize"},
			{ID: "e3", Source: "organize", SourcePort: "success", Target: "notification"},
			{ID: "e4", Source: "notification", SourcePort: "success", Target: "end"},
		},
	}
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("115 monitor action chain invalid: %#v", result.Errors)
	}

	definition.Nodes[1] = AutomationNode{ID: "recognize", Type: AutomationNodeMoviePilotRecognize}
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("MoviePilot monitor action chain invalid: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionRejectsInvalidFilmFusionRecognitionConfig(t *testing.T) {
	base := AutomationNode{ID: "local", Type: AutomationNodeFilmFusionRecognize, Config: map[string]any{"recognition_mode": "title", "input": "$item.title"}}
	if err := validateAutomationNodeConfig(base); err != nil {
		t.Fatalf("valid local recognition config rejected: %v", err)
	}

	invalidMode := base
	invalidMode.Config = map[string]any{"recognition_mode": "directory", "input": "$item.title"}
	if err := validateAutomationNodeConfig(invalidMode); err == nil || !strings.Contains(err.Error(), "title/file") {
		t.Fatalf("invalid mode error = %v", err)
	}

	missingInput := base
	missingInput.Config = map[string]any{"recognition_mode": "title"}
	if err := validateAutomationNodeConfig(missingInput); err == nil || !strings.Contains(err.Error(), "待识别标题") {
		t.Fatalf("missing title input error = %v", err)
	}

	invalidLookup := base
	invalidLookup.Config = map[string]any{"recognition_mode": "file", "lookup_tmdb": "yes"}
	if err := validateAutomationNodeConfig(invalidLookup); err == nil || !strings.Contains(err.Error(), "布尔值") {
		t.Fatalf("invalid lookup_tmdb error = %v", err)
	}
}

func TestValidateAutomationDefinitionAcceptsSuggestedQBitMediaChain(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "mp_title", Type: AutomationNodeMoviePilotTitle, Config: map[string]any{"input": "$item.title"}},
			{ID: "dedupe", Type: AutomationNodeMediaExists, Config: map[string]any{
				"cloud_directory_id": 1, "tmdb_id": "{{nodes.mp_title.output.tmdb_id}}",
			}},
			{ID: "query", Type: AutomationNodeHDHiveQuery, Config: map[string]any{
				"tmdb_id": "{{nodes.mp_title.output.tmdb_id}}", "media_type": "{{nodes.mp_title.output.media_type}}",
			}},
			{ID: "unlock", Type: AutomationNodeHDHiveUnlock, Config: map[string]any{"slug": "{{nodes.query.output.selected_slug}}"}},
			{ID: "qb", Type: AutomationNodeQBittorrent, Config: map[string]any{
				"target_id": 1, "url": "{{nodes.unlock.output.download_url}}",
			}},
			{ID: "wait_qb", Type: AutomationNodeWaitQBittorrent, Config: map[string]any{"poll_interval_seconds": 30}},
			{ID: "mp_transfer", Type: AutomationNodeMoviePilotTransfer, Config: map[string]any{"file_type": "auto", "media_type": "auto"}},
			{ID: "delete_qb", Type: AutomationNodeDeleteQBittorrent, Config: map[string]any{"delete_files": false}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
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
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("suggested qB media chain invalid: %#v", result.Errors)
	}
	unsafeDelete := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "qb", Type: AutomationNodeQBittorrent, Config: map[string]any{"target_id": 1, "url": "$item.download_url"}},
			{ID: "wait", Type: AutomationNodeWaitQBittorrent},
			{ID: "delete", Type: AutomationNodeDeleteQBittorrent, Config: map[string]any{"delete_files": true}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "u1", Source: "trigger", SourcePort: "next", Target: "qb"},
			{ID: "u2", Source: "qb", SourcePort: "success", Target: "wait"},
			{ID: "u3", Source: "wait", SourcePort: "success", Target: "delete"},
			{ID: "u4", Source: "delete", SourcePort: "success", Target: "end"},
		},
	}
	if result := ValidateAutomationDefinition(unsafeDelete); result.Valid || !containsAutomationValidationError(result.Errors, "MP2 整理成功后") {
		t.Fatalf("qB file deletion safeguard missing: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionAcceptsStrmVerifyAndRegenerateBranches(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "offline", Type: AutomationNodeOffline115OpenAPI, Config: map[string]any{"cloud_storage_id": 1, "url": "$item.download_url"}},
			{ID: "wait", Type: AutomationNodeWait115},
			{ID: "organize", Type: AutomationNodeOrganizeStrm, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "verify", Type: AutomationNodeStrmVerify, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "regenerate", Type: AutomationNodeStrmRegenerate, Config: map[string]any{"cloud_directory_id": 1}},
			{ID: "end_valid", Type: AutomationNodeEnd},
			{ID: "end_regenerated", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "e1", Source: "trigger", SourcePort: "next", Target: "offline"},
			{ID: "e2", Source: "offline", SourcePort: "success", Target: "wait"},
			{ID: "e3", Source: "wait", SourcePort: "success", Target: "organize"},
			{ID: "e4", Source: "organize", SourcePort: "success", Target: "verify"},
			{ID: "e5", Source: "verify", SourcePort: "valid", Target: "end_valid"},
			{ID: "e6", Source: "verify", SourcePort: "invalid", Target: "regenerate"},
			{ID: "e7", Source: "regenerate", SourcePort: "success", Target: "end_regenerated"},
		},
	}
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("STRM verify/regenerate definition invalid: %#v", result.Errors)
	}
	definition.Edges[5].SourcePort = "valid"
	if result := ValidateAutomationDefinition(definition); result.Valid || !containsAutomationValidationError(result.Errors, "“无效”出口") {
		t.Fatalf("STRM regenerate predecessor safeguard missing: %#v", result.Errors)
	}
}

func TestValidateAutomationDefinitionRejectsUnknownTargetPort(t *testing.T) {
	definition := DefaultAutomationDefinition()
	definition.Edges[0].TargetPort = "side-door"
	result := ValidateAutomationDefinition(definition)
	if result.Valid || !containsAutomationValidationError(result.Errors, "入口 \"side-door\" 无效") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
}

func TestValidateAutomationReplacementNodeConfigs(t *testing.T) {
	valid := []AutomationNode{
		{Type: AutomationNodeKeywordReplace, Config: map[string]any{
			"input": "$item.title", "variable": "normalized_title", "case_sensitive": false,
			"replacements": []any{map[string]any{"keyword": "WEB-DL", "replacement": "WEB"}},
		}},
		{Type: AutomationNodeRegexReplace, Config: map[string]any{
			"input": "$item.title", "pattern": `\[[^]]+\]`, "replacement": "", "variable": "clean_title",
		}},
	}
	for _, node := range valid {
		if err := validateAutomationNodeConfig(node); err != nil {
			t.Errorf("valid %s config rejected: %v", node.Type, err)
		}
		if !isAutomationSourcePortValid(node.Type, "success") || !isAutomationSourcePortValid(node.Type, "failure") {
			t.Errorf("%s should expose success and failure ports", node.Type)
		}
	}

	invalid := []AutomationNode{
		{Type: AutomationNodeKeywordReplace, Config: map[string]any{
			"input": "$item.title", "variable": "normalized_title",
			"replacements": []any{map[string]any{"keyword": "", "replacement": "WEB"}},
		}},
		{Type: AutomationNodeRegexReplace, Config: map[string]any{
			"input": "$item.title", "pattern": "(", "replacement": "", "variable": "clean_title",
		}},
	}
	for _, node := range invalid {
		if err := validateAutomationNodeConfig(node); err == nil {
			t.Errorf("invalid %s config was accepted", node.Type)
		}
	}
}

func containsAutomationValidationError(errors []string, fragment string) bool {
	for _, validationError := range errors {
		if strings.Contains(validationError, fragment) {
			return true
		}
	}
	return false
}
