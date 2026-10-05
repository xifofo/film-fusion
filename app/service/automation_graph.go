package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"film-fusion/app/model"
)

func automationNodeReadiness(node AutomationNode, incoming []AutomationEdge, nodeRuns map[string]*model.AutomationNodeRun) (ready bool, inactive bool) {
	if node.Type == AutomationNodeTrigger {
		return true, false
	}
	if len(incoming) == 0 {
		return false, true
	}
	allResolved := true
	activeCount := 0
	for _, edge := range incoming {
		predecessor := nodeRuns[edge.Source]
		if predecessor == nil || !isAutomationNodeTerminal(predecessor.Status) {
			allResolved = false
			continue
		}
		if automationEdgeActive(edge, predecessor) {
			activeCount++
		}
	}
	if node.Type == AutomationNodeJoin {
		policy := automationConfigString(node.Config, "policy")
		switch policy {
		case "any_completed":
			if activeCount > 0 {
				return true, false
			}
		case "any_success":
			for _, edge := range incoming {
				predecessor := nodeRuns[edge.Source]
				if predecessor != nil && predecessor.Status == model.AutomationNodeSucceeded && automationEdgeActive(edge, predecessor) {
					return true, false
				}
			}
		}
		if allResolved {
			return activeCount > 0, activeCount == 0
		}
		return false, false
	}
	if !allResolved {
		return false, false
	}
	return activeCount > 0, activeCount == 0
}

func automationEdgeActive(edge AutomationEdge, predecessor *model.AutomationNodeRun) bool {
	if predecessor == nil || predecessor.Status == model.AutomationNodeSkipped || predecessor.Status == model.AutomationNodeCancelled {
		return false
	}
	if strings.EqualFold(edge.SourcePort, "always") {
		return isAutomationNodeTerminal(predecessor.Status)
	}
	var output map[string]any
	decoder := json.NewDecoder(strings.NewReader(predecessor.OutputJSON))
	decoder.UseNumber()
	if decoder.Decode(&output) != nil {
		return false
	}
	if selected, ok := output["selected_port"].(string); ok && strings.EqualFold(selected, edge.SourcePort) {
		return true
	}
	if selected, ok := output["selected_ports"].([]any); ok {
		for _, port := range selected {
			portName := fmt.Sprint(port)
			if portName == "*" || strings.EqualFold(portName, edge.SourcePort) {
				return true
			}
		}
	}
	return false
}

func (s *AutomationService) executeAutomationJoinNode(runID uint, node AutomationNode, definition AutomationDefinition) (map[string]any, error) {
	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", runID).Find(&nodeRuns).Error; err != nil {
		return nil, err
	}
	byNode := make(map[string]*model.AutomationNodeRun, len(nodeRuns))
	for index := range nodeRuns {
		byNode[nodeRuns[index].NodeID] = &nodeRuns[index]
	}
	active := make([]*model.AutomationNodeRun, 0)
	for _, edge := range definition.Edges {
		if edge.Target != node.ID {
			continue
		}
		predecessor := byNode[edge.Source]
		if automationEdgeActive(edge, predecessor) {
			active = append(active, predecessor)
		}
	}
	if len(active) == 0 {
		return map[string]any{"selected_port": "failure"}, errors.New("汇合节点没有激活的输入")
	}
	policy := automationConfigString(node.Config, "policy")
	if policy == "" {
		policy = "all_completed"
	}
	successes := 0
	for _, predecessor := range active {
		if predecessor.Status == model.AutomationNodeSucceeded {
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

func (s *AutomationService) buildAutomationRunContext(run model.AutomationRun, currentNodeID string) (map[string]any, error) {
	contextValue := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(run.ContextJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&contextValue); err != nil {
		return nil, err
	}
	variables, _ := contextValue["vars"].(map[string]any)
	if variables == nil {
		variables = map[string]any{}
		contextValue["vars"] = variables
	}
	nodeValues := map[string]any{}
	contextValue["nodes"] = nodeValues
	definition, err := ParseAutomationDefinition(run.DefinitionJSON)
	if err != nil {
		return nil, err
	}
	orderedAncestors, err := automationAncestorTopologicalOrder(definition, currentNodeID)
	contextValue["params"] = definition.Parameters
	if err != nil {
		return nil, err
	}
	if len(orderedAncestors) == 0 {
		return contextValue, nil
	}
	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ? AND node_id IN ?", run.ID, orderedAncestors).Find(&nodeRuns).Error; err != nil {
		return nil, err
	}
	runsByNodeID := make(map[string]model.AutomationNodeRun, len(nodeRuns))
	for _, nodeRun := range nodeRuns {
		runsByNodeID[nodeRun.NodeID] = nodeRun
	}
	for _, nodeID := range orderedAncestors {
		nodeRun, exists := runsByNodeID[nodeID]
		if !exists {
			continue
		}
		output := map[string]any{}
		if nodeRun.OutputJSON != "" {
			outputDecoder := json.NewDecoder(strings.NewReader(nodeRun.OutputJSON))
			outputDecoder.UseNumber()
			_ = outputDecoder.Decode(&output)
		}
		if nodeRun.Status == model.AutomationNodeSucceeded {
			if produced, ok := output["variables"].(map[string]any); ok {
				for key, value := range produced {
					variables[key] = value
				}
			}
		}
		nodeValues[nodeID] = map[string]any{
			"status": nodeRun.Status, "attempt": nodeRun.Attempt, "output": output,
		}
	}
	return contextValue, nil
}
