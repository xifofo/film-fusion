package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"
)

const (
	defaultRSSAutomationManualCandidateLimit = 50
	maxRSSAutomationManualCandidateLimit     = 200
	maxRSSAutomationManualCandidateScan      = 5000
	maxRSSAutomationManualRunEntries         = 100
)

type RSSAutomationManualCandidate struct {
	EntryID      uint       `json:"entry_id"`
	Title        string     `json:"title"`
	DetailURL    string     `json:"detail_url,omitempty"`
	DownloadURL  string     `json:"download_url,omitempty"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
	DiscoveredAt time.Time  `json:"discovered_at"`
	ActionNames  []string   `json:"action_names"`
	ActionTypes  []string   `json:"action_types"`
}

type RSSAutomationManualCandidateList struct {
	WorkflowID      uint                           `json:"workflow_id"`
	WorkflowVersion int                            `json:"workflow_version"`
	Items           []RSSAutomationManualCandidate `json:"items"`
	ScannedEntries  int                            `json:"scanned_entries"`
	HasMore         bool                           `json:"has_more"`
}

type RSSAutomationManualRunInput struct {
	EntryIDs []uint `json:"entry_ids"`
}

type RSSAutomationManualRunSkip struct {
	EntryID uint   `json:"entry_id"`
	Reason  string `json:"reason"`
}

type RSSAutomationManualRunResult struct {
	Requested int                          `json:"requested"`
	Created   int                          `json:"created"`
	RunIDs    []uint                       `json:"run_ids"`
	Skipped   []RSSAutomationManualRunSkip `json:"skipped"`
}

type rssAutomationManualAction struct {
	ID   string
	Name string
	Type string
}

func (s *RSSAutomationService) ListManualCandidates(workflowID uint, limit int) (RSSAutomationManualCandidateList, error) {
	result := RSSAutomationManualCandidateList{WorkflowID: workflowID, Items: []RSSAutomationManualCandidate{}}
	workflow, definition, err := s.loadRSSAutomationWorkflowDefinition(workflowID)
	if err != nil {
		return result, err
	}
	result.WorkflowVersion = workflow.Version
	if limit <= 0 {
		limit = defaultRSSAutomationManualCandidateLimit
	}
	if limit > maxRSSAutomationManualCandidateLimit {
		limit = maxRSSAutomationManualCandidateLimit
	}

	var entries []model.RSSAutomationEntry
	err = s.db.Where("source_id = ?", workflow.SourceID).
		Where(`NOT EXISTS (
			SELECT 1
			FROM rss_automation_runs
			JOIN rss_automation_entries AS run_entry ON run_entry.id = rss_automation_runs.entry_id
			WHERE rss_automation_runs.workflow_id = ?
			AND (
				rss_automation_runs.entry_id = rss_automation_entries.id
				OR (rss_automation_entries.content_key <> '' AND run_entry.content_key = rss_automation_entries.content_key)
			)
		)`, workflow.ID).
		Order("published_at DESC").Order("discovered_at DESC").Order("id DESC").
		Limit(maxRSSAutomationManualCandidateScan).
		Find(&entries).Error
	if err != nil {
		return result, err
	}

	seenContentKeys := make(map[string]struct{})
	for _, entry := range entries {
		result.ScannedEntries++
		actions, previewErr := previewRSSAutomationEntry(definition, entry)
		if previewErr != nil || len(actions) == 0 {
			continue
		}
		if entry.ContentKey != "" {
			if _, exists := seenContentKeys[entry.ContentKey]; exists {
				continue
			}
			seenContentKeys[entry.ContentKey] = struct{}{}
		}
		candidate := RSSAutomationManualCandidate{
			EntryID: entry.ID, Title: entry.Title, DetailURL: entry.DetailURL,
			DownloadURL: entry.DownloadURL, PublishedAt: entry.PublishedAt,
			DiscoveredAt: entry.DiscoveredAt, ActionNames: []string{}, ActionTypes: []string{},
		}
		for _, action := range actions {
			candidate.ActionNames = append(candidate.ActionNames, action.Name)
			candidate.ActionTypes = append(candidate.ActionTypes, action.Type)
		}
		result.Items = append(result.Items, candidate)
		if len(result.Items) > limit {
			result.Items = result.Items[:limit]
			result.HasMore = true
			break
		}
	}
	if len(entries) == maxRSSAutomationManualCandidateScan && result.ScannedEntries == len(entries) {
		result.HasMore = true
	}
	return result, nil
}

func (s *RSSAutomationService) CreateManualRuns(workflowID uint, input RSSAutomationManualRunInput) (RSSAutomationManualRunResult, error) {
	result := RSSAutomationManualRunResult{RunIDs: []uint{}, Skipped: []RSSAutomationManualRunSkip{}}
	entryIDs := uniqueRSSAutomationEntryIDs(input.EntryIDs)
	result.Requested = len(entryIDs)
	if len(entryIDs) == 0 {
		return result, errors.New("请至少选择一个条目")
	}
	if len(entryIDs) > maxRSSAutomationManualRunEntries {
		return result, fmt.Errorf("一次最多手动运行 %d 个条目", maxRSSAutomationManualRunEntries)
	}

	workflow, definition, err := s.loadRSSAutomationWorkflowDefinition(workflowID)
	if err != nil {
		return result, err
	}
	var entries []model.RSSAutomationEntry
	if err := s.db.Where("id IN ? AND source_id = ?", entryIDs, workflow.SourceID).Find(&entries).Error; err != nil {
		return result, err
	}
	entryByID := make(map[uint]model.RSSAutomationEntry, len(entries))
	for _, entry := range entries {
		entryByID[entry.ID] = entry
	}

	for _, entryID := range entryIDs {
		entry, exists := entryByID[entryID]
		if !exists {
			result.Skipped = append(result.Skipped, RSSAutomationManualRunSkip{EntryID: entryID, Reason: "条目不存在或不属于该 RSS 源"})
			continue
		}
		actions, previewErr := previewRSSAutomationEntry(definition, entry)
		if previewErr != nil {
			result.Skipped = append(result.Skipped, RSSAutomationManualRunSkip{EntryID: entryID, Reason: "流程预演失败: " + previewErr.Error()})
			continue
		}
		if len(actions) == 0 {
			result.Skipped = append(result.Skipped, RSSAutomationManualRunSkip{EntryID: entryID, Reason: "当前流程不会到达任何动作节点"})
			continue
		}
		run, created, createErr := s.createRSSAutomationRun(workflow, entry, true)
		if createErr != nil {
			return result, createErr
		}
		if !created {
			result.Skipped = append(result.Skipped, RSSAutomationManualRunSkip{EntryID: entryID, Reason: "该流程已运行过此条目或相同下载内容"})
			continue
		}
		result.Created++
		result.RunIDs = append(result.RunIDs, run.ID)
	}
	if result.Created > 0 {
		s.wakeExecution()
	}
	return result, nil
}

func (s *RSSAutomationService) loadRSSAutomationWorkflowDefinition(workflowID uint) (model.RSSAutomationWorkflow, RSSAutomationDefinition, error) {
	var workflow model.RSSAutomationWorkflow
	if workflowID == 0 {
		return workflow, RSSAutomationDefinition{}, errors.New("自动化流程 ID 无效")
	}
	if err := s.db.First(&workflow, workflowID).Error; err != nil {
		return workflow, RSSAutomationDefinition{}, err
	}
	definition, err := ParseRSSAutomationDefinition(workflow.DefinitionJSON)
	if err != nil {
		return workflow, definition, err
	}
	validation := ValidateRSSAutomationDefinition(definition)
	if !validation.Valid {
		return workflow, definition, fmt.Errorf("流程 %s 无效: %s", workflow.Name, strings.Join(validation.Errors, "; "))
	}
	return workflow, definition, nil
}

func uniqueRSSAutomationEntryIDs(entryIDs []uint) []uint {
	unique := make([]uint, 0, len(entryIDs))
	seen := make(map[uint]struct{}, len(entryIDs))
	for _, entryID := range entryIDs {
		if entryID == 0 {
			continue
		}
		if _, exists := seen[entryID]; exists {
			continue
		}
		seen[entryID] = struct{}{}
		unique = append(unique, entryID)
	}
	return unique
}

func previewRSSAutomationEntry(definition RSSAutomationDefinition, entry model.RSSAutomationEntry) ([]rssAutomationManualAction, error) {
	fields := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(entry.FieldsJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf("读取条目字段失败: %w", err)
	}
	runContext := map[string]any{
		"item": fields, "vars": map[string]any{}, "nodes": map[string]any{},
		"entry_id": entry.ID, "source_id": entry.SourceID,
	}
	incoming := make(map[string][]RSSAutomationEdge, len(definition.Nodes))
	for _, edge := range definition.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}
	nodeRuns := make(map[string]*model.RSSAutomationNodeRun, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodeRuns[node.ID] = &model.RSSAutomationNodeRun{
			NodeID: node.ID, NodeType: node.Type, NodeName: node.Name, Status: model.RSSAutomationNodePending,
		}
	}
	actions := make(map[string]rssAutomationManualAction)
	for pass := 0; pass <= len(definition.Nodes); pass++ {
		changed := false
		for _, node := range definition.Nodes {
			nodeRun := nodeRuns[node.ID]
			if nodeRun == nil || nodeRun.Status != model.RSSAutomationNodePending {
				continue
			}
			ready, inactive := rssAutomationNodeReadiness(node, incoming[node.ID], nodeRuns)
			if inactive {
				nodeRun.Status = model.RSSAutomationNodeSkipped
				nodeRun.OutputJSON = `{"selected_ports":[]}`
				changed = true
				continue
			}
			if !ready {
				continue
			}

			var output map[string]any
			var executeErr error
			if isRSSAutomationActionNode(node.Type) {
				output, executeErr = previewRSSAutomationActionNode(node, runContext)
				if executeErr == nil {
					name := strings.TrimSpace(node.Name)
					if name == "" {
						name = node.Type
					}
					actions[node.ID] = rssAutomationManualAction{ID: node.ID, Name: name, Type: node.Type}
				}
			} else if node.Type == RSSAutomationNodeJoin {
				output, executeErr = previewRSSAutomationJoinNode(node, definition, nodeRuns)
			} else {
				output, executeErr = executeRSSAutomationPreviewNode(node, runContext)
			}
			if output == nil {
				output = map[string]any{}
			}
			if executeErr != nil {
				output["selected_port"] = "failure"
				nodeRun.Status = model.RSSAutomationNodeFailed
				nodeRun.ErrorMessage = executeErr.Error()
			} else {
				nodeRun.Status = model.RSSAutomationNodeSucceeded
			}
			encoded, _ := json.Marshal(output)
			nodeRun.OutputJSON = string(encoded)
			mergeRSSAutomationPreviewOutput(runContext, nodeRun, output)
			changed = true
		}
		if !changed {
			break
		}
	}

	result := make([]rssAutomationManualAction, 0, len(actions))
	for _, action := range actions {
		result = append(result, action)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func executeRSSAutomationPreviewNode(node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	switch node.Type {
	case RSSAutomationNodeTrigger:
		return map[string]any{"selected_port": "next"}, nil
	case RSSAutomationNodeRegex:
		return executeRSSAutomationRegexNode(node, runContext)
	case RSSAutomationNodeKeyword:
		return executeRSSAutomationKeywordNode(node, runContext)
	case RSSAutomationNodeConvert:
		return executeRSSAutomationConvertNode(node, runContext)
	case RSSAutomationNodeIf:
		matched, err := evaluateRSSAutomationCondition(node.Config["condition"], runContext)
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		port := "false"
		if matched {
			port = "true"
		}
		return map[string]any{"selected_port": port, "matched": matched}, nil
	case RSSAutomationNodeParallel:
		return map[string]any{"selected_ports": []string{"*"}}, nil
	case RSSAutomationNodeEnd:
		return map[string]any{"completed": true}, nil
	default:
		return nil, fmt.Errorf("预演不支持节点类型 %q", node.Type)
	}
}

func previewRSSAutomationActionNode(node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	switch node.Type {
	case RSSAutomationNodeQBittorrent, RSSAutomationNodeOffline115, RSSAutomationNodeOffline115OpenAPI:
		downloadURL, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "url"))
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		if err := validateRSSAutomationDownloadURL(downloadURL); err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		return map[string]any{
			"selected_port": "success", "content_key": rssAutomationContentKey(downloadURL), "preview": true,
		}, nil
	case RSSAutomationNodeWaitQBittorrent:
		return map[string]any{
			"selected_port": "success", "preview": true, "completed": true, "progress": 100,
			"name": "运行时读取 qBittorrent 任务", "content_path": "/downloads/运行时任务",
		}, nil
	case RSSAutomationNodeWait115:
		return map[string]any{
			"selected_port": "success", "preview": true, "completed": true,
			"tasks": []map[string]any{{"status": 2, "file_id": "preview-file", "name": "运行时读取 115 下载结果"}},
		}, nil
	case RSSAutomationNodeMoviePilotTitle:
		input, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "input"))
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		return map[string]any{
			"selected_port": "success", "preview": true, "input": input,
			"tmdb_id": "运行时识别", "title": "运行时识别媒体信息", "poster_url": "运行时识别",
		}, nil
	case RSSAutomationNodeMediaExists:
		return map[string]any{
			"selected_port": "missing", "preview": true, "exists": false, "local_exists": false,
		}, nil
	case RSSAutomationNodeHDHiveQuery:
		return map[string]any{
			"selected_port": "found", "preview": true, "resource_count": 1,
			"selected_slug": "运行时选择资源", "selected_title": "运行时 HDHive 资源",
		}, nil
	case RSSAutomationNodeHDHiveUnlock:
		return map[string]any{
			"selected_port": "success", "preview": true,
			"download_url": "https://example.invalid/runtime-resource",
		}, nil
	case RSSAutomationNodeMoviePilotRecognize:
		return map[string]any{
			"selected_port": "success", "preview": true,
			"tmdb_id": "运行时识别", "title": "运行时识别媒体信息",
		}, nil
	case RSSAutomationNodeOrganizeStrm:
		return map[string]any{
			"selected_port": "success", "preview": true,
			"organized_count": 1, "strm_count": 1,
			"strm_path": "运行时按目录配置生成",
		}, nil
	case RSSAutomationNodeStrmVerify:
		return map[string]any{
			"selected_port": "valid", "preview": true, "valid": true,
			"checked_count": 1, "valid_count": 1, "invalid_count": 0,
			"strm_path": "运行时校验 STRM 路径",
		}, nil
	case RSSAutomationNodeStrmRegenerate:
		return map[string]any{
			"selected_port": "success", "preview": true,
			"regenerated_count": 1, "failed_count": 0,
			"strm_path": "运行时按上游整理结果重写",
		}, nil
	case RSSAutomationNodeEmbyRefreshWait:
		return map[string]any{
			"selected_port": "success", "preview": true, "found": true,
			"emby_item_id": "运行时等待入库", "refresh_requested": true,
		}, nil
	case RSSAutomationNodeHTTPRequest:
		requestURL, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "url"))
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		return map[string]any{
			"selected_port": "success", "preview": true, "status_code": 200,
			"request_host": rssAutomationHTTPRequestHost(requestURL), "content_type": "application/json",
			"body": "{\"preview\":true}", "json": map[string]any{"preview": true},
		}, nil
	case RSSAutomationNodeNotification:
		message := strings.TrimSpace(renderRSSAutomationTemplate(rssAutomationConfigString(node.Config, "message"), runContext))
		if message == "" {
			return map[string]any{"selected_port": "failure"}, errors.New("通知内容不能为空")
		}
		return map[string]any{"selected_port": "success", "preview": true}, nil
	default:
		return nil, fmt.Errorf("不支持预演动作节点 %q", node.Type)
	}
}

func previewRSSAutomationJoinNode(node RSSAutomationNode, definition RSSAutomationDefinition, nodeRuns map[string]*model.RSSAutomationNodeRun) (map[string]any, error) {
	active := make([]*model.RSSAutomationNodeRun, 0)
	for _, edge := range definition.Edges {
		if edge.Target != node.ID {
			continue
		}
		predecessor := nodeRuns[edge.Source]
		if rssAutomationEdgeActive(edge, predecessor) {
			active = append(active, predecessor)
		}
	}
	if len(active) == 0 {
		return map[string]any{"selected_port": "failure"}, errors.New("汇合节点没有激活的输入")
	}
	policy := rssAutomationConfigString(node.Config, "policy")
	if policy == "" {
		policy = "all_completed"
	}
	successes := 0
	for _, predecessor := range active {
		if predecessor.Status == model.RSSAutomationNodeSucceeded {
			successes++
		}
	}
	matched := false
	switch policy {
	case "all_completed", "any_completed":
		matched = true
	case "all_success":
		matched = successes == len(active)
	case "any_success":
		matched = successes > 0
	}
	output := map[string]any{"policy": policy, "active_inputs": len(active), "successful_inputs": successes}
	if !matched {
		output["selected_port"] = "failure"
		output["reason"] = fmt.Sprintf("汇合策略 %s 未满足", policy)
		return output, nil
	}
	output["selected_port"] = "success"
	return output, nil
}

func mergeRSSAutomationPreviewOutput(runContext map[string]any, nodeRun *model.RSSAutomationNodeRun, output map[string]any) {
	variables, _ := runContext["vars"].(map[string]any)
	if produced, ok := output["variables"].(map[string]any); ok {
		for key, value := range produced {
			variables[key] = value
		}
	}
	nodes, _ := runContext["nodes"].(map[string]any)
	nodes[nodeRun.NodeID] = map[string]any{
		"status": nodeRun.Status, "attempt": 1, "output": output,
	}
}
