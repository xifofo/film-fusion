package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

type AutomationRunDetail struct {
	Run      model.AutomationRun       `json:"run"`
	Entry    model.AutomationEntry     `json:"entry"`
	NodeRuns []model.AutomationNodeRun `json:"node_runs"`
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

func (s *AutomationService) executionScheduleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.advanceAutomationRuns()
		case <-s.executionWake:
			s.advanceAutomationRuns()
		}
	}
}

func (s *AutomationService) advanceAutomationRuns() {
	var runs []model.AutomationRun
	if err := s.db.Where("status IN ?", []string{model.AutomationRunPending, model.AutomationRunRunning}).Order("id ASC").Limit(100).Find(&runs).Error; err != nil {
		return
	}
	for _, run := range runs {
		if err := s.advanceAutomationRun(run); err != nil && s.log != nil {
			s.log.Errorf("[AUTOMATION] 推进流程 run=%d 失败: %v", run.ID, err)
		}
	}
}

func (s *AutomationService) advanceAutomationRun(run model.AutomationRun) error {
	definition, err := ParseAutomationDefinition(run.DefinitionJSON)
	if err != nil {
		return s.failAutomationRun(run.ID, err)
	}
	nodes := make(map[string]AutomationNode, len(definition.Nodes))
	incoming := make(map[string][]AutomationEdge, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range definition.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}

	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", run.ID).Order("id ASC").Find(&nodeRuns).Error; err != nil {
		return err
	}
	runByNode := make(map[string]*model.AutomationNodeRun, len(nodeRuns))
	for index := range nodeRuns {
		runByNode[nodeRuns[index].NodeID] = &nodeRuns[index]
	}

	changed := true
	for pass := 0; changed && pass <= len(definition.Nodes); pass++ {
		changed = false
		for _, node := range definition.Nodes {
			nodeRun := runByNode[node.ID]
			if nodeRun == nil || nodeRun.Status != model.AutomationNodePending {
				continue
			}
			if nodeRun.NextAttemptAt != nil && nodeRun.NextAttemptAt.After(time.Now()) {
				continue
			}
			ready, inactive := automationNodeReadiness(node, incoming[node.ID], runByNode)
			if inactive {
				now := time.Now()
				outputJSON := `{"selected_ports":[]}`
				if err := s.db.Model(&model.AutomationNodeRun{}).
					Where("id = ? AND status = ?", nodeRun.ID, model.AutomationNodePending).
					Updates(map[string]any{"status": model.AutomationNodeSkipped, "output_json": outputJSON, "completed_at": now}).Error; err != nil {
					return err
				}
				nodeRun.Status = model.AutomationNodeSkipped
				nodeRun.OutputJSON = outputJSON
				nodeRun.CompletedAt = &now
				changed = true
				continue
			}
			if !ready {
				continue
			}
			select {
			case s.workers <- struct{}{}:
			default:
				continue
			}
			if s.claimAutomationNode(nodeRun) {
				nodeRun.Status = model.AutomationNodeRunning
				nodeRun.Attempt++
				s.workerWG.Add(1)
				go func(runID, nodeRunID uint, definition AutomationDefinition) {
					defer func() {
						<-s.workers
						s.workerWG.Done()
					}()
					s.executeAutomationNodeRun(runID, nodeRunID, definition)
				}(run.ID, nodeRun.ID, definition)
				changed = true
			} else {
				<-s.workers
			}
		}
	}
	return s.finalizeAutomationRun(run.ID)
}

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

func (s *AutomationService) claimAutomationNode(nodeRun *model.AutomationNodeRun) bool {
	if nodeRun == nil {
		return false
	}
	now := time.Now()
	result := s.db.Model(&model.AutomationNodeRun{}).
		Where("id = ? AND status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", nodeRun.ID, model.AutomationNodePending, now).
		Updates(map[string]any{
			"status": model.AutomationNodeRunning, "attempt": gorm.Expr("attempt + 1"),
			"started_at": now, "completed_at": nil, "next_attempt_at": nil,
		})
	return result.Error == nil && result.RowsAffected == 1
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
	runContext, err := s.buildAutomationRunContext(run)
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
	case AutomationNodeRegex:
		return executeAutomationRegexNode(node, runContext)
	case AutomationNodeKeyword:
		return executeAutomationKeywordNode(node, runContext)
	case AutomationNodeKeywordReplace:
		return executeAutomationKeywordReplaceNode(node, runContext)
	case AutomationNodeRegexReplace:
		return executeAutomationRegexReplaceNode(node, runContext)
	case AutomationNodeConvert:
		return executeAutomationConvertNode(node, runContext)
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
	case AutomationNodeDeleteQBittorrent:
		output, err := s.executeAutomationDeleteQBittorrent(ctx, node, definition, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeOffline115:
		output, err := s.executeAutomationOffline115(ctx, node, runContext)
		return withAutomationSelectedPort(output, err), err
	case AutomationNodeOffline115OpenAPI:
		output, err := s.executeAutomationOffline115OpenAPI(ctx, node, runContext)
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
	case AutomationNodeHDHiveQuery:
		return s.executeAutomationHDHiveQuery(ctx, node, runContext)
	case AutomationNodeHDHiveUnlock:
		return s.executeAutomationHDHiveUnlock(ctx, node, runContext)
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

func executeAutomationRegexNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	pattern, err := regexp.Compile(automationConfigString(node.Config, "pattern"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	matches := pattern.FindStringSubmatch(input)
	if matches == nil {
		return map[string]any{"selected_port": "failure", "matched": false, "reason": "正则表达式没有匹配到内容"}, nil
	}
	groupIndex := 0
	group := automationConfigString(node.Config, "group")
	if group == "" && len(matches) > 1 {
		groupIndex = 1
	} else if group != "" {
		if parsed, parseErr := strconv.Atoi(group); parseErr == nil {
			groupIndex = parsed
		} else {
			groupIndex = pattern.SubexpIndex(group)
		}
	}
	if groupIndex < 0 || groupIndex >= len(matches) {
		return map[string]any{"selected_port": "failure", "matched": true}, fmt.Errorf("捕获组 %q 不存在", group)
	}
	valueType := automationConfigString(node.Config, "value_type")
	if valueType == "" {
		valueType = "string"
	}
	value, err := convertAutomationScalar(matches[groupIndex], valueType)
	if err != nil {
		return map[string]any{"selected_port": "failure", "matched": true, "reason": err.Error()}, nil
	}
	variable := automationConfigString(node.Config, "variable")
	return map[string]any{
		"selected_port": "success", "matched": true, "captured": matches[groupIndex],
		"variables": map[string]any{variable: value},
	}, nil
}

func executeAutomationKeywordNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	keywords := automationConfigTextSlice(node.Config, "keywords")
	if len(keywords) == 0 {
		return map[string]any{"selected_port": "failure"}, errors.New("关键词不能为空")
	}
	caseSensitive, _ := node.Config["case_sensitive"].(bool)
	haystack := input
	if !caseSensitive {
		haystack = strings.ToLower(haystack)
	}
	matchedKeywords := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		needle := keyword
		if !caseSensitive {
			needle = strings.ToLower(needle)
		}
		if strings.Contains(haystack, needle) {
			matchedKeywords = append(matchedKeywords, keyword)
		}
	}

	mode := automationConfigString(node.Config, "match_mode")
	matched := false
	switch mode {
	case "contains_any":
		matched = len(matchedKeywords) > 0
	case "contains_all":
		matched = len(matchedKeywords) == len(keywords)
	case "contains_none":
		matched = len(matchedKeywords) == 0
	default:
		return map[string]any{"selected_port": "failure"}, fmt.Errorf("不支持的关键词规则 %q", mode)
	}
	port := "unmatched"
	if matched {
		port = "matched"
	}
	return map[string]any{
		"selected_port":    port,
		"matched":          matched,
		"matched_keywords": matchedKeywords,
		"match_mode":       mode,
	}, nil
}

func executeAutomationKeywordReplaceNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	rules, err := automationConfigReplacementRules(node.Config, "replacements")
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	if len(rules) == 0 {
		return map[string]any{"selected_port": "failure"}, errors.New("关键词替换规则不能为空")
	}
	caseSensitive, _ := node.Config["case_sensitive"].(bool)
	result := input
	replacementCount := 0
	for _, rule := range rules {
		if caseSensitive {
			replacementCount += strings.Count(result, rule.Keyword)
			result = strings.ReplaceAll(result, rule.Keyword, rule.Replacement)
			continue
		}
		pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(rule.Keyword))
		replacementCount += len(pattern.FindAllStringIndex(result, -1))
		result = pattern.ReplaceAllStringFunc(result, func(string) string { return rule.Replacement })
	}
	variable := automationConfigString(node.Config, "variable")
	return map[string]any{
		"selected_port":     "success",
		"input":             input,
		"result":            result,
		"replacement_count": replacementCount,
		"variables":         map[string]any{variable: result},
	}, nil
}

func executeAutomationRegexReplaceNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	pattern, err := regexp.Compile(automationConfigString(node.Config, "pattern"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	replacement := automationConfigRawString(node.Config, "replacement")
	replacementCount := len(pattern.FindAllStringIndex(input, -1))
	result := pattern.ReplaceAllString(input, replacement)
	variable := automationConfigString(node.Config, "variable")
	return map[string]any{
		"selected_port":     "success",
		"input":             input,
		"result":            result,
		"replacement_count": replacementCount,
		"variables":         map[string]any{variable: result},
	}, nil
}

func executeAutomationConvertNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	converted, err := convertAutomationScalar(input, automationConfigString(node.Config, "value_type"))
	if err != nil {
		return map[string]any{"selected_port": "failure", "reason": err.Error()}, nil
	}
	return map[string]any{
		"selected_port": "success",
		"variables":     map[string]any{automationConfigString(node.Config, "variable"): converted},
	}, nil
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

func (s *AutomationService) completeAutomationNode(nodeRun model.AutomationNodeRun, output map[string]any, executeErr error) {
	if output == nil {
		output = map[string]any{}
	}
	var deferred *automationNodeDeferred
	if errors.As(executeErr, &deferred) {
		s.deferAutomationNode(nodeRun, output, deferred)
		return
	}
	if executeErr != nil {
		output["selected_port"] = "failure"
	}
	outputJSON, _ := json.Marshal(output)
	now := time.Now()
	updates := map[string]any{"output_json": string(outputJSON)}
	if executeErr == nil {
		updates["status"] = model.AutomationNodeSucceeded
		updates["error_message"] = ""
		updates["completed_at"] = now
	} else if nodeRun.Attempt < nodeRun.MaxAttempts {
		delay := time.Duration(math.Min(math.Pow(2, float64(nodeRun.Attempt-1))*5, 300)) * time.Second
		next := now.Add(delay)
		updates["status"] = model.AutomationNodePending
		updates["error_message"] = executeErr.Error()
		updates["next_attempt_at"] = next
		updates["completed_at"] = nil
	} else {
		updates["status"] = model.AutomationNodeFailed
		updates["error_message"] = executeErr.Error()
		updates["completed_at"] = now
	}
	updated := false
	updateErr := s.retryAutomationDatabaseOperation(func() error {
		result := s.db.Model(&model.AutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRun.ID, model.AutomationNodeRunning).
			Updates(updates)
		if result.Error == nil {
			updated = result.RowsAffected == 1
		}
		return result.Error
	})
	if updateErr != nil || !updated {
		return
	}
	_ = s.finalizeAutomationRun(nodeRun.RunID)
}

func (s *AutomationService) deferAutomationNode(nodeRun model.AutomationNodeRun, output map[string]any, deferred *automationNodeDeferred) {
	if output == nil {
		output = map[string]any{}
	}
	delete(output, "selected_port")
	outputJSON, _ := json.Marshal(output)
	delay := defaultAutomation115PollSeconds * time.Second
	message := "节点等待外部状态更新"
	if deferred != nil {
		if deferred.delay > 0 {
			delay = deferred.delay
		}
		if strings.TrimSpace(deferred.message) != "" {
			message = deferred.message
		}
	}
	next := time.Now().Add(delay)
	_ = s.retryAutomationDatabaseOperation(func() error {
		return s.db.Model(&model.AutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRun.ID, model.AutomationNodeRunning).
			Updates(map[string]any{
				"status": model.AutomationNodePending, "next_attempt_at": next,
				"started_at": nil, "completed_at": nil, "output_json": string(outputJSON),
				"error_message": message,
				"attempt":       gorm.Expr("CASE WHEN attempt > 0 THEN attempt - 1 ELSE 0 END"),
			}).Error
	})
}

func (s *AutomationService) buildAutomationRunContext(run model.AutomationRun) (map[string]any, error) {
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
	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", run.ID).Order("id ASC").Find(&nodeRuns).Error; err != nil {
		return nil, err
	}
	for _, nodeRun := range nodeRuns {
		output := map[string]any{}
		if nodeRun.OutputJSON != "" {
			outputDecoder := json.NewDecoder(strings.NewReader(nodeRun.OutputJSON))
			outputDecoder.UseNumber()
			_ = outputDecoder.Decode(&output)
		}
		if produced, ok := output["variables"].(map[string]any); ok {
			for key, value := range produced {
				variables[key] = value
			}
		}
		nodeValues[nodeRun.NodeID] = map[string]any{
			"status": nodeRun.Status, "attempt": nodeRun.Attempt, "output": output,
		}
	}
	return contextValue, nil
}

func evaluateAutomationCondition(raw any, runContext map[string]any) (bool, error) {
	condition, ok := raw.(map[string]any)
	if !ok {
		return false, errors.New("IF 条件格式无效")
	}
	if values, exists := condition["all"]; exists {
		items, ok := values.([]any)
		if !ok || len(items) == 0 {
			return false, errors.New("all 条件不能为空")
		}
		for _, item := range items {
			matched, err := evaluateAutomationCondition(item, runContext)
			if err != nil || !matched {
				return false, err
			}
		}
		return true, nil
	}
	if values, exists := condition["any"]; exists {
		items, ok := values.([]any)
		if !ok || len(items) == 0 {
			return false, errors.New("any 条件不能为空")
		}
		var lastErr error
		for _, item := range items {
			matched, err := evaluateAutomationCondition(item, runContext)
			if err == nil && matched {
				return true, nil
			}
			if err != nil {
				lastErr = err
			}
		}
		return false, lastErr
	}
	if value, exists := condition["not"]; exists {
		matched, err := evaluateAutomationCondition(value, runContext)
		return !matched, err
	}
	leftExpression := firstAutomationConditionValue(condition, "field", "left")
	leftRef := strings.TrimSpace(fmt.Sprint(leftExpression))
	operator := strings.ToLower(strings.TrimSpace(fmt.Sprint(firstAutomationConditionValue(condition, "operator", "op"))))
	right := firstAutomationConditionValue(condition, "value", "right")
	left, leftExists := resolveAutomationConditionOperand(runContext, leftExpression)
	if resolved, exists := resolveAutomationConditionOperand(runContext, right); exists {
		right = resolved
	}
	switch operator {
	case "exists":
		return leftExists && left != nil && strings.TrimSpace(fmt.Sprint(left)) != "", nil
	case "not_exists":
		return !leftExists || left == nil || strings.TrimSpace(fmt.Sprint(left)) == "", nil
	}
	if !leftExists {
		return false, fmt.Errorf("条件字段 %q 不存在", leftRef)
	}
	switch operator {
	case "eq":
		return automationValuesEqual(left, right), nil
	case "neq":
		return !automationValuesEqual(left, right), nil
	case "gt", "gte", "lt", "lte":
		comparison, err := compareAutomationValues(left, right)
		if err != nil {
			return false, err
		}
		switch operator {
		case "gt":
			return comparison > 0, nil
		case "gte":
			return comparison >= 0, nil
		case "lt":
			return comparison < 0, nil
		default:
			return comparison <= 0, nil
		}
	case "contains":
		return strings.Contains(fmt.Sprint(left), fmt.Sprint(right)), nil
	case "not_contains":
		return !strings.Contains(fmt.Sprint(left), fmt.Sprint(right)), nil
	case "starts_with":
		return strings.HasPrefix(fmt.Sprint(left), fmt.Sprint(right)), nil
	case "ends_with":
		return strings.HasSuffix(fmt.Sprint(left), fmt.Sprint(right)), nil
	case "regex":
		pattern, err := regexp.Compile(fmt.Sprint(right))
		if err != nil {
			return false, err
		}
		return pattern.MatchString(fmt.Sprint(left)), nil
	case "in":
		items, ok := right.([]any)
		if !ok {
			return false, errors.New("in 条件的比较值必须是数组")
		}
		for _, item := range items {
			if automationValuesEqual(left, item) {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("不支持的条件操作符 %q", operator)
	}
}

func resolveAutomationConditionOperand(runContext map[string]any, raw any) (any, bool) {
	switch typed := raw.(type) {
	case string:
		expression := strings.TrimSpace(typed)
		if parts := automationTemplatePattern.FindStringSubmatch(expression); len(parts) == 2 && parts[0] == expression {
			return resolveAutomationReference(runContext, parts[1])
		}
		trimmedReference := strings.TrimPrefix(expression, "$")
		if strings.HasPrefix(expression, "$") || strings.HasPrefix(trimmedReference, "item.") ||
			strings.HasPrefix(trimmedReference, "vars.") || strings.HasPrefix(trimmedReference, "nodes.") {
			return resolveAutomationReference(runContext, expression)
		}
		if strings.Contains(expression, "{{") {
			return renderAutomationTemplate(expression, runContext), true
		}
		return typed, true
	case []any:
		resolved := make([]any, 0, len(typed))
		for _, item := range typed {
			value, exists := resolveAutomationConditionOperand(runContext, item)
			if !exists {
				return nil, false
			}
			resolved = append(resolved, value)
		}
		return resolved, true
	default:
		return raw, raw != nil
	}
}

func firstAutomationConditionValue(condition map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := condition[key]; ok {
			return value
		}
	}
	return nil
}

func automationValuesEqual(left, right any) bool {
	if leftNumber, leftOK := automationNumber(left); leftOK {
		if rightNumber, rightOK := automationNumber(right); rightOK {
			return leftNumber == rightNumber
		}
	}
	return reflect.DeepEqual(fmt.Sprint(left), fmt.Sprint(right))
}

func compareAutomationValues(left, right any) (int, error) {
	leftNumber, leftOK := automationNumber(left)
	rightNumber, rightOK := automationNumber(right)
	if leftOK && rightOK {
		switch {
		case leftNumber < rightNumber:
			return -1, nil
		case leftNumber > rightNumber:
			return 1, nil
		default:
			return 0, nil
		}
	}
	if leftTime := parseAutomationTime(fmt.Sprint(left)); leftTime != nil {
		if rightTime := parseAutomationTime(fmt.Sprint(right)); rightTime != nil {
			switch {
			case leftTime.Before(*rightTime):
				return -1, nil
			case leftTime.After(*rightTime):
				return 1, nil
			default:
				return 0, nil
			}
		}
	}
	return strings.Compare(fmt.Sprint(left), fmt.Sprint(right)), nil
}

func automationNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (s *AutomationService) finalizeAutomationRun(runID uint) error {
	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", runID).Order("id ASC").Find(&nodeRuns).Error; err != nil {
		return err
	}
	if len(nodeRuns) == 0 {
		return nil
	}
	allTerminal := true
	endSucceeded := false
	failed := make([]string, 0)
	partialOutput := false
	for _, nodeRun := range nodeRuns {
		if !isAutomationNodeTerminal(nodeRun.Status) {
			allTerminal = false
		}
		if nodeRun.NodeType == AutomationNodeEnd && nodeRun.Status == model.AutomationNodeSucceeded {
			endSucceeded = true
		}
		if nodeRun.Status == model.AutomationNodeFailed {
			failed = append(failed, nodeRun.NodeName+": "+nodeRun.ErrorMessage)
		}
		if strings.Contains(nodeRun.OutputJSON, `"partial":true`) {
			partialOutput = true
		}
	}
	if !allTerminal {
		return s.db.Model(&model.AutomationRun{}).
			Where("id = ? AND status = ?", runID, model.AutomationRunPending).
			Update("status", model.AutomationRunRunning).Error
	}
	status := model.AutomationRunSucceeded
	if !endSucceeded {
		status = model.AutomationRunFailed
	} else if len(failed) > 0 || partialOutput {
		status = model.AutomationRunPartial
	}
	now := time.Now()
	return s.db.Model(&model.AutomationRun{}).
		Where("id = ? AND status IN ?", runID, []string{model.AutomationRunPending, model.AutomationRunRunning}).
		Updates(map[string]any{
			"status": status, "error_message": strings.Join(failed, "; "), "completed_at": now,
		}).Error
}

func (s *AutomationService) failAutomationRun(runID uint, runErr error) error {
	now := time.Now()
	return s.db.Model(&model.AutomationRun{}).
		Where("id = ? AND status IN ?", runID, []string{model.AutomationRunPending, model.AutomationRunRunning}).
		Updates(map[string]any{
			"status": model.AutomationRunFailed, "error_message": runErr.Error(), "completed_at": now,
		}).Error
}

func (s *AutomationService) ListRuns(workflowID uint, status string, limit, offset int) ([]model.AutomationRun, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	query := s.db.Model(&model.AutomationRun{})
	if workflowID != 0 {
		query = query.Where("workflow_id = ?", workflowID)
	}
	if strings.TrimSpace(status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(status))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var runs []model.AutomationRun
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

func (s *AutomationService) GetRun(runID uint) (AutomationRunDetail, error) {
	detail := AutomationRunDetail{NodeRuns: []model.AutomationNodeRun{}}
	if err := s.db.First(&detail.Run, runID).Error; err != nil {
		return detail, err
	}
	if err := s.db.First(&detail.Entry, detail.Run.EntryID).Error; err != nil {
		return detail, err
	}
	if err := s.db.Where("run_id = ?", runID).Order("id ASC").Find(&detail.NodeRuns).Error; err != nil {
		return detail, err
	}
	return detail, nil
}

func (s *AutomationService) RetryRun(runID uint) error {
	var run model.AutomationRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return err
	}
	if run.Status != model.AutomationRunFailed && run.Status != model.AutomationRunPartial && run.Status != model.AutomationRunCancelled {
		return errors.New("只有失败、部分成功或已取消的流程可以重试")
	}
	definition, err := ParseAutomationDefinition(run.DefinitionJSON)
	if err != nil {
		return err
	}
	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", runID).Find(&nodeRuns).Error; err != nil {
		return err
	}
	resetNodes := automationRetryNodeSet(definition, nodeRuns)
	if len(resetNodes) == 0 {
		return errors.New("流程中没有可重试的节点")
	}
	nodeIDs := make([]string, 0, len(resetNodes))
	for nodeID := range resetNodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.AutomationNodeRun{}).Where("run_id = ? AND node_id IN ?", runID, nodeIDs).Updates(map[string]any{
			"status": model.AutomationNodePending, "attempt": 0, "next_attempt_at": nil,
			"input_json": "", "output_json": "", "error_message": "", "started_at": nil, "completed_at": nil,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&run).Updates(map[string]any{
			"status": model.AutomationRunPending, "error_message": "", "completed_at": nil,
		}).Error
	})
}

func automationRetryNodeSet(definition AutomationDefinition, nodeRuns []model.AutomationNodeRun) map[string]struct{} {
	reset := make(map[string]struct{})
	for _, nodeRun := range nodeRuns {
		if nodeRun.Status == model.AutomationNodeFailed || nodeRun.Status == model.AutomationNodeCancelled || strings.Contains(nodeRun.OutputJSON, `"partial":true`) {
			reset[nodeRun.NodeID] = struct{}{}
		}
	}
	if len(reset) == 0 {
		return reset
	}
	outgoing := make(map[string][]string)
	for _, edge := range definition.Edges {
		outgoing[edge.Source] = append(outgoing[edge.Source], edge.Target)
	}
	queue := make([]string, 0, len(reset))
	for nodeID := range reset {
		queue = append(queue, nodeID)
	}
	for len(queue) > 0 {
		nodeID := queue[0]
		queue = queue[1:]
		for _, targetID := range outgoing[nodeID] {
			if _, exists := reset[targetID]; exists {
				continue
			}
			reset[targetID] = struct{}{}
			queue = append(queue, targetID)
		}
	}
	return reset
}

func (s *AutomationService) CancelRun(runID uint) error {
	var run model.AutomationRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return err
	}
	if run.Status != model.AutomationRunPending && run.Status != model.AutomationRunRunning {
		return errors.New("当前流程不能取消")
	}
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.AutomationNodeRun{}).Where("run_id = ? AND status IN ?", runID, []string{model.AutomationNodePending, model.AutomationNodeRunning}).Updates(map[string]any{
			"status": model.AutomationNodeCancelled, "completed_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&run).Updates(map[string]any{"status": model.AutomationRunCancelled, "completed_at": now}).Error
	})
}

func isAutomationNodeTerminal(status string) bool {
	switch status {
	case model.AutomationNodeSucceeded, model.AutomationNodeFailed, model.AutomationNodeSkipped, model.AutomationNodeCancelled:
		return true
	default:
		return false
	}
}

func (s *AutomationService) retryAutomationDatabaseOperation(operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < 7; attempt++ {
		if err := operation(); err == nil {
			return nil
		} else {
			lastErr = err
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, "database is locked") && !strings.Contains(message, "database table is locked") && !strings.Contains(message, "database is busy") {
				return err
			}
		}
		delay := time.Duration(10*(1<<attempt)) * time.Millisecond
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

func (s *AutomationService) requeueAutomationNodeAfterDatabaseError(nodeRunID uint, loadErr error) {
	next := time.Now().Add(time.Second)
	_ = s.retryAutomationDatabaseOperation(func() error {
		return s.db.Model(&model.AutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRunID, model.AutomationNodeRunning).
			Updates(map[string]any{
				"status": model.AutomationNodePending, "next_attempt_at": next,
				"started_at": nil, "error_message": "读取执行状态失败: " + loadErr.Error(),
				"attempt": gorm.Expr("CASE WHEN attempt > 0 THEN attempt - 1 ELSE 0 END"),
			}).Error
	})
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
