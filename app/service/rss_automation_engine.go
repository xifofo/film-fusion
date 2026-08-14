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

type RSSAutomationRunDetail struct {
	Run      model.RSSAutomationRun       `json:"run"`
	Entry    model.RSSAutomationEntry     `json:"entry"`
	NodeRuns []model.RSSAutomationNodeRun `json:"node_runs"`
}

type rssAutomationNodeDeferred struct {
	delay   time.Duration
	message string
}

func (d *rssAutomationNodeDeferred) Error() string {
	if d == nil || strings.TrimSpace(d.message) == "" {
		return "节点等待外部状态更新"
	}
	return d.message
}

func (s *RSSAutomationService) executionScheduleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.advanceRSSAutomationRuns()
		case <-s.executionWake:
			s.advanceRSSAutomationRuns()
		}
	}
}

func (s *RSSAutomationService) advanceRSSAutomationRuns() {
	var runs []model.RSSAutomationRun
	if err := s.db.Where("status IN ?", []string{model.RSSAutomationRunPending, model.RSSAutomationRunRunning}).Order("id ASC").Limit(100).Find(&runs).Error; err != nil {
		return
	}
	for _, run := range runs {
		if err := s.advanceRSSAutomationRun(run); err != nil && s.log != nil {
			s.log.Errorf("[RSS-AUTOMATION] 推进流程 run=%d 失败: %v", run.ID, err)
		}
	}
}

func (s *RSSAutomationService) advanceRSSAutomationRun(run model.RSSAutomationRun) error {
	definition, err := ParseRSSAutomationDefinition(run.DefinitionJSON)
	if err != nil {
		return s.failRSSAutomationRun(run.ID, err)
	}
	nodes := make(map[string]RSSAutomationNode, len(definition.Nodes))
	incoming := make(map[string][]RSSAutomationEdge, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range definition.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}

	var nodeRuns []model.RSSAutomationNodeRun
	if err := s.db.Where("run_id = ?", run.ID).Order("id ASC").Find(&nodeRuns).Error; err != nil {
		return err
	}
	runByNode := make(map[string]*model.RSSAutomationNodeRun, len(nodeRuns))
	for index := range nodeRuns {
		runByNode[nodeRuns[index].NodeID] = &nodeRuns[index]
	}

	changed := true
	for pass := 0; changed && pass <= len(definition.Nodes); pass++ {
		changed = false
		for _, node := range definition.Nodes {
			nodeRun := runByNode[node.ID]
			if nodeRun == nil || nodeRun.Status != model.RSSAutomationNodePending {
				continue
			}
			if nodeRun.NextAttemptAt != nil && nodeRun.NextAttemptAt.After(time.Now()) {
				continue
			}
			ready, inactive := rssAutomationNodeReadiness(node, incoming[node.ID], runByNode)
			if inactive {
				now := time.Now()
				outputJSON := `{"selected_ports":[]}`
				if err := s.db.Model(&model.RSSAutomationNodeRun{}).
					Where("id = ? AND status = ?", nodeRun.ID, model.RSSAutomationNodePending).
					Updates(map[string]any{"status": model.RSSAutomationNodeSkipped, "output_json": outputJSON, "completed_at": now}).Error; err != nil {
					return err
				}
				nodeRun.Status = model.RSSAutomationNodeSkipped
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
			if s.claimRSSAutomationNode(nodeRun) {
				nodeRun.Status = model.RSSAutomationNodeRunning
				nodeRun.Attempt++
				s.workerWG.Add(1)
				go func(runID, nodeRunID uint, definition RSSAutomationDefinition) {
					defer func() {
						<-s.workers
						s.workerWG.Done()
					}()
					s.executeRSSAutomationNodeRun(runID, nodeRunID, definition)
				}(run.ID, nodeRun.ID, definition)
				changed = true
			} else {
				<-s.workers
			}
		}
	}
	return s.finalizeRSSAutomationRun(run.ID)
}

func rssAutomationNodeReadiness(node RSSAutomationNode, incoming []RSSAutomationEdge, nodeRuns map[string]*model.RSSAutomationNodeRun) (ready bool, inactive bool) {
	if node.Type == RSSAutomationNodeTrigger {
		return true, false
	}
	if len(incoming) == 0 {
		return false, true
	}
	allResolved := true
	activeCount := 0
	for _, edge := range incoming {
		predecessor := nodeRuns[edge.Source]
		if predecessor == nil || !isRSSAutomationNodeTerminal(predecessor.Status) {
			allResolved = false
			continue
		}
		if rssAutomationEdgeActive(edge, predecessor) {
			activeCount++
		}
	}
	if node.Type == RSSAutomationNodeJoin {
		policy := rssAutomationConfigString(node.Config, "policy")
		switch policy {
		case "any_completed":
			if activeCount > 0 {
				return true, false
			}
		case "any_success":
			for _, edge := range incoming {
				predecessor := nodeRuns[edge.Source]
				if predecessor != nil && predecessor.Status == model.RSSAutomationNodeSucceeded && rssAutomationEdgeActive(edge, predecessor) {
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

func rssAutomationEdgeActive(edge RSSAutomationEdge, predecessor *model.RSSAutomationNodeRun) bool {
	if predecessor == nil || predecessor.Status == model.RSSAutomationNodeSkipped || predecessor.Status == model.RSSAutomationNodeCancelled {
		return false
	}
	if strings.EqualFold(edge.SourcePort, "always") {
		return isRSSAutomationNodeTerminal(predecessor.Status)
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

func (s *RSSAutomationService) claimRSSAutomationNode(nodeRun *model.RSSAutomationNodeRun) bool {
	if nodeRun == nil {
		return false
	}
	now := time.Now()
	result := s.db.Model(&model.RSSAutomationNodeRun{}).
		Where("id = ? AND status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", nodeRun.ID, model.RSSAutomationNodePending, now).
		Updates(map[string]any{
			"status": model.RSSAutomationNodeRunning, "attempt": gorm.Expr("attempt + 1"),
			"started_at": now, "completed_at": nil, "next_attempt_at": nil,
		})
	return result.Error == nil && result.RowsAffected == 1
}

func (s *RSSAutomationService) executeRSSAutomationNodeRun(runID, nodeRunID uint, definition RSSAutomationDefinition) {
	var run model.RSSAutomationRun
	var nodeRun model.RSSAutomationNodeRun
	loadErr := s.retryRSSAutomationDatabaseOperation(func() error {
		if err := s.db.First(&run, runID).Error; err != nil {
			return err
		}
		return s.db.First(&nodeRun, nodeRunID).Error
	})
	if loadErr != nil {
		s.requeueRSSAutomationNodeAfterDatabaseError(nodeRunID, loadErr)
		return
	}
	var node RSSAutomationNode
	found := false
	for _, candidate := range definition.Nodes {
		if candidate.ID == nodeRun.NodeID {
			node = candidate
			found = true
			break
		}
	}
	if !found {
		s.completeRSSAutomationNode(nodeRun, nil, errors.New("流程定义中找不到节点"))
		return
	}
	runContext, err := s.buildRSSAutomationRunContext(run)
	if err != nil {
		s.completeRSSAutomationNode(nodeRun, nil, err)
		return
	}
	inputJSON, _ := json.Marshal(map[string]any{"config": node.Config})
	s.db.Model(&nodeRun).Update("input_json", string(inputJSON))

	executionContext, cancel := context.WithTimeout(s.ctx, rssAutomationNodeTimeout(node))
	defer cancel()
	output, executeErr := s.executeRSSAutomationNode(executionContext, run, nodeRun, node, definition, runContext)
	s.completeRSSAutomationNode(nodeRun, output, executeErr)
	s.wakeExecution()
}

func (s *RSSAutomationService) executeRSSAutomationNode(ctx context.Context, run model.RSSAutomationRun, nodeRun model.RSSAutomationNodeRun, node RSSAutomationNode, definition RSSAutomationDefinition, runContext map[string]any) (map[string]any, error) {
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
	case RSSAutomationNodeJoin:
		return s.executeRSSAutomationJoinNode(run.ID, node, definition)
	case RSSAutomationNodeQBittorrent:
		output, err := s.executeRSSAutomationQBittorrent(ctx, node, runContext)
		return withRSSAutomationSelectedPort(output, err), err
	case RSSAutomationNodeWaitQBittorrent:
		return s.executeRSSAutomationWaitQBittorrent(ctx, nodeRun, node, definition, runContext)
	case RSSAutomationNodeOffline115:
		output, err := s.executeRSSAutomationOffline115(ctx, node, runContext)
		return withRSSAutomationSelectedPort(output, err), err
	case RSSAutomationNodeOffline115OpenAPI:
		output, err := s.executeRSSAutomationOffline115OpenAPI(ctx, node, runContext)
		return withRSSAutomationSelectedPort(output, err), err
	case RSSAutomationNodeWait115:
		return s.executeRSSAutomationWait115(ctx, nodeRun, node, definition, runContext)
	case RSSAutomationNodeMoviePilotTitle:
		return s.executeRSSAutomationMoviePilotTitleRecognize(ctx, node, runContext)
	case RSSAutomationNodeMediaExists:
		return s.executeRSSAutomationMediaExists(ctx, node, runContext)
	case RSSAutomationNodeHDHiveQuery:
		return s.executeRSSAutomationHDHiveQuery(ctx, node, runContext)
	case RSSAutomationNodeHDHiveUnlock:
		return s.executeRSSAutomationHDHiveUnlock(ctx, node, runContext)
	case RSSAutomationNodeMoviePilotRecognize:
		return s.executeRSSAutomationMoviePilotRecognize(ctx, node, definition, runContext)
	case RSSAutomationNodeOrganizeStrm:
		return s.executeRSSAutomationOrganizeStrm(ctx, node, definition, runContext)
	case RSSAutomationNodeStrmVerify:
		return s.executeRSSAutomationStrmVerify(ctx, node, definition, runContext)
	case RSSAutomationNodeStrmRegenerate:
		return s.executeRSSAutomationStrmRegenerate(ctx, node, definition, runContext)
	case RSSAutomationNodeEmbyRefreshWait:
		return s.executeRSSAutomationEmbyRefreshWait(ctx, nodeRun, node, runContext)
	case RSSAutomationNodeHTTPRequest:
		return s.executeRSSAutomationHTTPRequest(ctx, node, runContext)
	case RSSAutomationNodeNotification:
		output, err := s.executeRSSAutomationNotification(ctx, node, runContext)
		return withRSSAutomationSelectedPort(output, err), err
	case RSSAutomationNodeEnd:
		return map[string]any{"completed": true}, nil
	default:
		return nil, fmt.Errorf("不支持节点类型 %q", node.Type)
	}
}

func executeRSSAutomationRegexNode(node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	pattern, err := regexp.Compile(rssAutomationConfigString(node.Config, "pattern"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	matches := pattern.FindStringSubmatch(input)
	if matches == nil {
		return map[string]any{"selected_port": "failure", "matched": false, "reason": "正则表达式没有匹配到内容"}, nil
	}
	groupIndex := 0
	group := rssAutomationConfigString(node.Config, "group")
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
	valueType := rssAutomationConfigString(node.Config, "value_type")
	if valueType == "" {
		valueType = "string"
	}
	value, err := convertRSSAutomationScalar(matches[groupIndex], valueType)
	if err != nil {
		return map[string]any{"selected_port": "failure", "matched": true, "reason": err.Error()}, nil
	}
	variable := rssAutomationConfigString(node.Config, "variable")
	return map[string]any{
		"selected_port": "success", "matched": true, "captured": matches[groupIndex],
		"variables": map[string]any{variable: value},
	}, nil
}

func executeRSSAutomationKeywordNode(node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	keywords := rssAutomationConfigTextSlice(node.Config, "keywords")
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

	mode := rssAutomationConfigString(node.Config, "match_mode")
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

func executeRSSAutomationConvertNode(node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	input, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "input"))
	if err != nil {
		return map[string]any{"selected_port": "failure"}, err
	}
	converted, err := convertRSSAutomationScalar(input, rssAutomationConfigString(node.Config, "value_type"))
	if err != nil {
		return map[string]any{"selected_port": "failure", "reason": err.Error()}, nil
	}
	return map[string]any{
		"selected_port": "success",
		"variables":     map[string]any{rssAutomationConfigString(node.Config, "variable"): converted},
	}, nil
}

func (s *RSSAutomationService) executeRSSAutomationJoinNode(runID uint, node RSSAutomationNode, definition RSSAutomationDefinition) (map[string]any, error) {
	var nodeRuns []model.RSSAutomationNodeRun
	if err := s.db.Where("run_id = ?", runID).Find(&nodeRuns).Error; err != nil {
		return nil, err
	}
	byNode := make(map[string]*model.RSSAutomationNodeRun, len(nodeRuns))
	for index := range nodeRuns {
		byNode[nodeRuns[index].NodeID] = &nodeRuns[index]
	}
	active := make([]*model.RSSAutomationNodeRun, 0)
	for _, edge := range definition.Edges {
		if edge.Target != node.ID {
			continue
		}
		predecessor := byNode[edge.Source]
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

func withRSSAutomationSelectedPort(output map[string]any, err error) map[string]any {
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

func (s *RSSAutomationService) completeRSSAutomationNode(nodeRun model.RSSAutomationNodeRun, output map[string]any, executeErr error) {
	if output == nil {
		output = map[string]any{}
	}
	var deferred *rssAutomationNodeDeferred
	if errors.As(executeErr, &deferred) {
		s.deferRSSAutomationNode(nodeRun, output, deferred)
		return
	}
	if executeErr != nil {
		output["selected_port"] = "failure"
	}
	outputJSON, _ := json.Marshal(output)
	now := time.Now()
	updates := map[string]any{"output_json": string(outputJSON)}
	if executeErr == nil {
		updates["status"] = model.RSSAutomationNodeSucceeded
		updates["error_message"] = ""
		updates["completed_at"] = now
	} else if nodeRun.Attempt < nodeRun.MaxAttempts {
		delay := time.Duration(math.Min(math.Pow(2, float64(nodeRun.Attempt-1))*5, 300)) * time.Second
		next := now.Add(delay)
		updates["status"] = model.RSSAutomationNodePending
		updates["error_message"] = executeErr.Error()
		updates["next_attempt_at"] = next
		updates["completed_at"] = nil
	} else {
		updates["status"] = model.RSSAutomationNodeFailed
		updates["error_message"] = executeErr.Error()
		updates["completed_at"] = now
	}
	updated := false
	updateErr := s.retryRSSAutomationDatabaseOperation(func() error {
		result := s.db.Model(&model.RSSAutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRun.ID, model.RSSAutomationNodeRunning).
			Updates(updates)
		if result.Error == nil {
			updated = result.RowsAffected == 1
		}
		return result.Error
	})
	if updateErr != nil || !updated {
		return
	}
	_ = s.finalizeRSSAutomationRun(nodeRun.RunID)
}

func (s *RSSAutomationService) deferRSSAutomationNode(nodeRun model.RSSAutomationNodeRun, output map[string]any, deferred *rssAutomationNodeDeferred) {
	if output == nil {
		output = map[string]any{}
	}
	delete(output, "selected_port")
	outputJSON, _ := json.Marshal(output)
	delay := defaultRSSAutomation115PollSeconds * time.Second
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
	_ = s.retryRSSAutomationDatabaseOperation(func() error {
		return s.db.Model(&model.RSSAutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRun.ID, model.RSSAutomationNodeRunning).
			Updates(map[string]any{
				"status": model.RSSAutomationNodePending, "next_attempt_at": next,
				"started_at": nil, "completed_at": nil, "output_json": string(outputJSON),
				"error_message": message,
				"attempt":       gorm.Expr("CASE WHEN attempt > 0 THEN attempt - 1 ELSE 0 END"),
			}).Error
	})
}

func (s *RSSAutomationService) buildRSSAutomationRunContext(run model.RSSAutomationRun) (map[string]any, error) {
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
	var nodeRuns []model.RSSAutomationNodeRun
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

func evaluateRSSAutomationCondition(raw any, runContext map[string]any) (bool, error) {
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
			matched, err := evaluateRSSAutomationCondition(item, runContext)
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
			matched, err := evaluateRSSAutomationCondition(item, runContext)
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
		matched, err := evaluateRSSAutomationCondition(value, runContext)
		return !matched, err
	}
	leftExpression := firstRSSAutomationConditionValue(condition, "field", "left")
	leftRef := strings.TrimSpace(fmt.Sprint(leftExpression))
	operator := strings.ToLower(strings.TrimSpace(fmt.Sprint(firstRSSAutomationConditionValue(condition, "operator", "op"))))
	right := firstRSSAutomationConditionValue(condition, "value", "right")
	left, leftExists := resolveRSSAutomationConditionOperand(runContext, leftExpression)
	if resolved, exists := resolveRSSAutomationConditionOperand(runContext, right); exists {
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
		return rssAutomationValuesEqual(left, right), nil
	case "neq":
		return !rssAutomationValuesEqual(left, right), nil
	case "gt", "gte", "lt", "lte":
		comparison, err := compareRSSAutomationValues(left, right)
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
			if rssAutomationValuesEqual(left, item) {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("不支持的条件操作符 %q", operator)
	}
}

func resolveRSSAutomationConditionOperand(runContext map[string]any, raw any) (any, bool) {
	switch typed := raw.(type) {
	case string:
		expression := strings.TrimSpace(typed)
		if parts := rssAutomationTemplatePattern.FindStringSubmatch(expression); len(parts) == 2 && parts[0] == expression {
			return resolveRSSAutomationReference(runContext, parts[1])
		}
		trimmedReference := strings.TrimPrefix(expression, "$")
		if strings.HasPrefix(expression, "$") || strings.HasPrefix(trimmedReference, "item.") ||
			strings.HasPrefix(trimmedReference, "vars.") || strings.HasPrefix(trimmedReference, "nodes.") {
			return resolveRSSAutomationReference(runContext, expression)
		}
		if strings.Contains(expression, "{{") {
			return renderRSSAutomationTemplate(expression, runContext), true
		}
		return typed, true
	case []any:
		resolved := make([]any, 0, len(typed))
		for _, item := range typed {
			value, exists := resolveRSSAutomationConditionOperand(runContext, item)
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

func firstRSSAutomationConditionValue(condition map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := condition[key]; ok {
			return value
		}
	}
	return nil
}

func rssAutomationValuesEqual(left, right any) bool {
	if leftNumber, leftOK := rssAutomationNumber(left); leftOK {
		if rightNumber, rightOK := rssAutomationNumber(right); rightOK {
			return leftNumber == rightNumber
		}
	}
	return reflect.DeepEqual(fmt.Sprint(left), fmt.Sprint(right))
}

func compareRSSAutomationValues(left, right any) (int, error) {
	leftNumber, leftOK := rssAutomationNumber(left)
	rightNumber, rightOK := rssAutomationNumber(right)
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
	if leftTime := parseRSSAutomationTime(fmt.Sprint(left)); leftTime != nil {
		if rightTime := parseRSSAutomationTime(fmt.Sprint(right)); rightTime != nil {
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

func rssAutomationNumber(value any) (float64, bool) {
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

func (s *RSSAutomationService) finalizeRSSAutomationRun(runID uint) error {
	var nodeRuns []model.RSSAutomationNodeRun
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
		if !isRSSAutomationNodeTerminal(nodeRun.Status) {
			allTerminal = false
		}
		if nodeRun.NodeType == RSSAutomationNodeEnd && nodeRun.Status == model.RSSAutomationNodeSucceeded {
			endSucceeded = true
		}
		if nodeRun.Status == model.RSSAutomationNodeFailed {
			failed = append(failed, nodeRun.NodeName+": "+nodeRun.ErrorMessage)
		}
		if strings.Contains(nodeRun.OutputJSON, `"partial":true`) {
			partialOutput = true
		}
	}
	if !allTerminal {
		return s.db.Model(&model.RSSAutomationRun{}).
			Where("id = ? AND status = ?", runID, model.RSSAutomationRunPending).
			Update("status", model.RSSAutomationRunRunning).Error
	}
	status := model.RSSAutomationRunSucceeded
	if !endSucceeded {
		status = model.RSSAutomationRunFailed
	} else if len(failed) > 0 || partialOutput {
		status = model.RSSAutomationRunPartial
	}
	now := time.Now()
	return s.db.Model(&model.RSSAutomationRun{}).
		Where("id = ? AND status IN ?", runID, []string{model.RSSAutomationRunPending, model.RSSAutomationRunRunning}).
		Updates(map[string]any{
			"status": status, "error_message": strings.Join(failed, "; "), "completed_at": now,
		}).Error
}

func (s *RSSAutomationService) failRSSAutomationRun(runID uint, runErr error) error {
	now := time.Now()
	return s.db.Model(&model.RSSAutomationRun{}).
		Where("id = ? AND status IN ?", runID, []string{model.RSSAutomationRunPending, model.RSSAutomationRunRunning}).
		Updates(map[string]any{
			"status": model.RSSAutomationRunFailed, "error_message": runErr.Error(), "completed_at": now,
		}).Error
}

func (s *RSSAutomationService) ListRuns(workflowID uint, status string, limit, offset int) ([]model.RSSAutomationRun, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	query := s.db.Model(&model.RSSAutomationRun{})
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
	var runs []model.RSSAutomationRun
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

func (s *RSSAutomationService) GetRun(runID uint) (RSSAutomationRunDetail, error) {
	detail := RSSAutomationRunDetail{NodeRuns: []model.RSSAutomationNodeRun{}}
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

func (s *RSSAutomationService) RetryRun(runID uint) error {
	var run model.RSSAutomationRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return err
	}
	if run.Status != model.RSSAutomationRunFailed && run.Status != model.RSSAutomationRunPartial && run.Status != model.RSSAutomationRunCancelled {
		return errors.New("只有失败、部分成功或已取消的流程可以重试")
	}
	definition, err := ParseRSSAutomationDefinition(run.DefinitionJSON)
	if err != nil {
		return err
	}
	var nodeRuns []model.RSSAutomationNodeRun
	if err := s.db.Where("run_id = ?", runID).Find(&nodeRuns).Error; err != nil {
		return err
	}
	resetNodes := rssAutomationRetryNodeSet(definition, nodeRuns)
	if len(resetNodes) == 0 {
		return errors.New("流程中没有可重试的节点")
	}
	nodeIDs := make([]string, 0, len(resetNodes))
	for nodeID := range resetNodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.RSSAutomationNodeRun{}).Where("run_id = ? AND node_id IN ?", runID, nodeIDs).Updates(map[string]any{
			"status": model.RSSAutomationNodePending, "attempt": 0, "next_attempt_at": nil,
			"input_json": "", "output_json": "", "error_message": "", "started_at": nil, "completed_at": nil,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&run).Updates(map[string]any{
			"status": model.RSSAutomationRunPending, "error_message": "", "completed_at": nil,
		}).Error
	})
}

func rssAutomationRetryNodeSet(definition RSSAutomationDefinition, nodeRuns []model.RSSAutomationNodeRun) map[string]struct{} {
	reset := make(map[string]struct{})
	for _, nodeRun := range nodeRuns {
		if nodeRun.Status == model.RSSAutomationNodeFailed || nodeRun.Status == model.RSSAutomationNodeCancelled || strings.Contains(nodeRun.OutputJSON, `"partial":true`) {
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

func (s *RSSAutomationService) CancelRun(runID uint) error {
	var run model.RSSAutomationRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return err
	}
	if run.Status != model.RSSAutomationRunPending && run.Status != model.RSSAutomationRunRunning {
		return errors.New("当前流程不能取消")
	}
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.RSSAutomationNodeRun{}).Where("run_id = ? AND status IN ?", runID, []string{model.RSSAutomationNodePending, model.RSSAutomationNodeRunning}).Updates(map[string]any{
			"status": model.RSSAutomationNodeCancelled, "completed_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&run).Updates(map[string]any{"status": model.RSSAutomationRunCancelled, "completed_at": now}).Error
	})
}

func isRSSAutomationNodeTerminal(status string) bool {
	switch status {
	case model.RSSAutomationNodeSucceeded, model.RSSAutomationNodeFailed, model.RSSAutomationNodeSkipped, model.RSSAutomationNodeCancelled:
		return true
	default:
		return false
	}
}

func (s *RSSAutomationService) retryRSSAutomationDatabaseOperation(operation func() error) error {
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

func (s *RSSAutomationService) requeueRSSAutomationNodeAfterDatabaseError(nodeRunID uint, loadErr error) {
	next := time.Now().Add(time.Second)
	_ = s.retryRSSAutomationDatabaseOperation(func() error {
		return s.db.Model(&model.RSSAutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRunID, model.RSSAutomationNodeRunning).
			Updates(map[string]any{
				"status": model.RSSAutomationNodePending, "next_attempt_at": next,
				"started_at": nil, "error_message": "读取执行状态失败: " + loadErr.Error(),
				"attempt": gorm.Expr("CASE WHEN attempt > 0 THEN attempt - 1 ELSE 0 END"),
			}).Error
	})
}

func rssAutomationNodeTimeout(node RSSAutomationNode) time.Duration {
	seconds := 30
	if node.Type == RSSAutomationNodeOrganizeStrm {
		seconds = 600
	}
	if value := rssAutomationConfigUint(node.Config, "timeout_seconds"); value > 0 && value <= 600 {
		seconds = int(value)
	}
	return time.Duration(seconds) * time.Second
}
