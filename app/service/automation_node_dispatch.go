package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"film-fusion/app/model"
)

func isAutomationActionNode(nodeType string) bool {
	switch nodeType {
	case AutomationNodeQBittorrent, AutomationNodeWaitQBittorrent, AutomationNodeStopQBittorrent, AutomationNodeDeleteQBittorrent,
		AutomationNodeOffline115, AutomationNodeOffline115OpenAPI,
		AutomationNodeTorrentToMagnet, AutomationNodeTorrent115OpenAPI, AutomationNodeOffline115BTOpenAPI, AutomationNodeList115TasksOpenAPI,
		AutomationNodeQuota115OpenAPI, AutomationNodeDelete115TaskOpenAPI, AutomationNodeClear115TasksOpenAPI,
		AutomationNodeWait115, AutomationNodeRename115OpenAPI, AutomationNodeMoviePilotTitle,
		AutomationNodeFilmFusionRecognize, AutomationNodeMoviePilotTransfer, AutomationNodeMediaExists,
		AutomationNodeMoviePilotRecognize,
		AutomationNodeOrganizeStrm, AutomationNodeStrmVerify, AutomationNodeStrmRegenerate,
		AutomationNodeEmbyRefreshWait, AutomationNodeHTTPRequest, AutomationNodeNotification:
		return true
	default:
		return false
	}
}

func automationNodeMaxAttempts(node AutomationNode) int {
	if isAutomationNonRetryableNode(node.Type) {
		return 1
	}
	if node.MaxAttempts > 0 {
		return node.MaxAttempts
	}
	if isAutomationActionNode(node.Type) && !isAutomationNonRetryableNode(node.Type) {
		return 3
	}
	return 1
}

type automationNodeDeferred struct {
	delay   time.Duration
	message string
}

func (d *automationNodeDeferred) Error() string {
	if d == nil || strings.TrimSpace(d.message) == "" {
		return "节点等待外部状态更新"
	}
	return d.message
}

func (s *AutomationService) executeAutomationNodeRun(runID, nodeRunID uint, definition AutomationDefinition) {
	var run model.AutomationRun
	var nodeRun model.AutomationNodeRun
	loadErr := s.retryAutomationDatabaseOperation(func() error {
		if err := s.db.First(&run, runID).Error; err != nil {
			return err
		}
		return s.db.First(&nodeRun, nodeRunID).Error
	})
	if loadErr != nil {
		s.requeueAutomationNodeAfterDatabaseError(nodeRunID, loadErr)
		return
	}
	var node AutomationNode
	found := false
	for _, candidate := range definition.Nodes {
		if candidate.ID == nodeRun.NodeID {
			node = candidate
			found = true
			break
		}
	}
	if !found {
		s.completeAutomationNode(nodeRun, nil, errors.New("流程定义中找不到节点"))
		return
	}
	runContext, err := s.buildAutomationRunContext(run, nodeRun.NodeID)
	if err != nil {
		s.completeAutomationNode(nodeRun, nil, err)
		return
	}
	inputJSON, _ := json.Marshal(map[string]any{"config": node.Config})
	s.db.Model(&nodeRun).Update("input_json", string(inputJSON))

	executionContext, cancel := context.WithTimeout(s.ctx, automationNodeTimeout(node))
	defer cancel()
	output, executeErr := s.executeAutomationNode(executionContext, run, nodeRun, node, definition, runContext)
	s.completeAutomationNode(nodeRun, output, executeErr)
	s.wakeExecution()
}

func (s *AutomationService) executeAutomationNode(ctx context.Context, run model.AutomationRun, nodeRun model.AutomationNodeRun, node AutomationNode, definition AutomationDefinition, runContext map[string]any) (map[string]any, error) {
	switch node.Type {
	case AutomationNodeTrigger:
		output := map[string]any{"selected_port": "next"}
		if item, ok := runContext["item"].(map[string]any); ok {
			for key, value := range item {
				output[key] = value
			}
		}
		return output, nil
	case AutomationNodeDelay:
		return executeAutomationDelay(nodeRun, node, time.Now())
	case AutomationNodeRegex:
		return executeAutomationRegexNode(node, runContext)
	case AutomationNodeExtractBetween:
		return executeAutomationExtractBetweenNode(node, runContext)
	case AutomationNodeKeyword:
		return executeAutomationKeywordNode(node, runContext)
	case AutomationNodeKeywordReplace:
		return executeAutomationKeywordReplaceNode(node, runContext)
	case AutomationNodeRegexReplace:
		return executeAutomationRegexReplaceNode(node, runContext)
	case AutomationNodeConvert:
		return executeAutomationConvertNode(node, runContext)
	case AutomationNodeSetVariable:
		return executeAutomationSetVariableNode(node, runContext)
	case AutomationNodeTemplate:
		return executeAutomationTemplateNode(node, runContext)
	case AutomationNodeJSONExtract:
		return executeAutomationJSONExtractNode(node, runContext)
	case AutomationNodeMath:
		return executeAutomationMathNode(node, runContext)
	case AutomationNodeDatetimeOperation:
		return executeAutomationDatetimeOperationNode(node, runContext)
	case AutomationNodeListOperation:
		return executeAutomationListOperationNode(node, runContext)
	case AutomationNodeSwitch:
		return executeAutomationSwitchNode(node, runContext)
	case AutomationNodeCoalesce:
		return executeAutomationCoalesceNode(node, runContext)
	case AutomationNodeDeduplicate:
		return s.executeAutomationDeduplicateNode(run, nodeRun, node, runContext)
	case AutomationNodeRateLimit:
		return s.executeAutomationRateLimitNode(run, nodeRun, node, runContext)
	case AutomationNodeForeach:
		return executeAutomationForeachNode(node, runContext)
	case AutomationNodeIf:
		matched, err := evaluateAutomationCondition(node.Config["condition"], runContext)
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		port := "false"
		if matched {
			port = "true"
		}
		return map[string]any{"selected_port": port, "matched": matched}, nil
	case AutomationNodeParallel:
		return map[string]any{"selected_ports": []string{"*"}}, nil
	case AutomationNodeJoin:
		return s.executeAutomationJoinNode(run.ID, node, definition)
	case AutomationNodeQBittorrent:
		output, err := s.executeAutomationQBittorrent(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeWaitQBittorrent:
		return s.executeAutomationWaitQBittorrent(ctx, nodeRun, node, definition, runContext)
	case AutomationNodeMoviePilotTransfer:
		output, err := s.executeAutomationMoviePilotTransfer(ctx, node, definition, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeStopQBittorrent:
		output, err := s.executeAutomationStopQBittorrent(ctx, node, definition, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeDeleteQBittorrent:
		output, err := s.executeAutomationDeleteQBittorrent(ctx, node, definition, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeTorrentToMagnet:
		output, err := executeAutomationTorrentToMagnet(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeOffline115:
		output, err := s.executeAutomationOffline115(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeOffline115OpenAPI:
		output, err := s.executeAutomationOffline115OpenAPI(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeTorrent115OpenAPI, AutomationNodeOffline115BTOpenAPI, AutomationNodeList115TasksOpenAPI,
		AutomationNodeQuota115OpenAPI, AutomationNodeDelete115TaskOpenAPI, AutomationNodeClear115TasksOpenAPI:
		output, err := s.executeAutomation115OpenAPI(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeWait115:
		return s.executeAutomationWait115(ctx, nodeRun, node, definition, runContext)
	case AutomationNodeRename115OpenAPI:
		output, err := s.executeAutomationRename115OpenAPI(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeMoviePilotTitle:
		return s.executeAutomationMoviePilotTitleRecognize(ctx, node, runContext)
	case AutomationNodeFilmFusionRecognize:
		return s.executeAutomationFilmFusionRecognize(ctx, node, definition, runContext)
	case AutomationNodeMediaExists:
		return s.executeAutomationMediaExists(ctx, node, runContext)
	case AutomationNodeMoviePilotRecognize:
		return s.executeAutomationMoviePilotRecognize(ctx, node, definition, runContext)
	case AutomationNodeOrganizeStrm:
		return s.executeAutomationOrganizeStrm(ctx, node, definition, runContext)
	case AutomationNodeStrmVerify:
		return s.executeAutomationStrmVerify(ctx, node, definition, runContext)
	case AutomationNodeStrmRegenerate:
		return s.executeAutomationStrmRegenerate(ctx, node, definition, runContext)
	case AutomationNodeEmbyRefreshWait:
		return s.executeAutomationEmbyRefreshWait(ctx, nodeRun, node, runContext)
	case AutomationNodeHTTPRequest:
		return s.executeAutomationHTTPRequest(ctx, node, runContext)
	case AutomationNodeNotification:
		output, err := s.executeAutomationNotification(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeEnd:
		return map[string]any{"completed": true}, nil
	default:
		return nil, fmt.Errorf("不支持节点类型 %q", node.Type)
	}
}

func withAutomationSelectedPort(output map[string]any, err error) map[string]any {
	if output == nil {
		output = map[string]any{}
	}
	if err != nil {
		output["selected_port"] = "failure"
	} else {
		output["selected_port"] = "success"
	}
	return output
}

func automationNodeTimeout(node AutomationNode) time.Duration {
	seconds := 30
	if node.Type == AutomationNodeOrganizeStrm || node.Type == AutomationNodeMoviePilotTransfer {
		seconds = 600
	}
	if value := automationConfigUint(node.Config, "timeout_seconds"); value > 0 && value <= 600 {
		seconds = int(value)
	}
	return time.Duration(seconds) * time.Second
}
