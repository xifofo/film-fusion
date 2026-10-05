package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func validateAutomationForeachConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	if _, exists := config["input"]; !exists {
		return errors.New("Foreach 必须配置 input")
	}
	transform, err := automationForeachTransform(config)
	if err != nil {
		return err
	}
	transformType := strings.ToLower(automationConfigString(transform, "type"))
	transformConfig, err := automationNestedConfig(transform, "config")
	if err != nil {
		return err
	}
	if _, exists := transformConfig["variable"]; exists {
		return errors.New("Foreach transform.config 不能配置 variable")
	}
	if _, exists := transformConfig["overwrite"]; exists {
		return errors.New("Foreach transform.config 不能配置 overwrite")
	}
	var transformErr error
	switch transformType {
	case AutomationNodeTemplate:
		transformErr = validateAutomationTemplatePureConfig(transformConfig)
	case AutomationNodeJSONExtract:
		transformErr = validateAutomationJSONExtractPureConfig(transformConfig)
	case AutomationNodeMath:
		transformErr = validateAutomationMathPureConfig(transformConfig)
	case AutomationNodeCoalesce:
		transformErr = validateAutomationCoalescePureConfig(transformConfig)
	case AutomationNodeDatetimeOperation:
		transformErr = validateAutomationDatetimeOperationPureConfig(transformConfig)
	default:
		return errors.New("Foreach transform.type 必须是 template/json_extract/math/coalesce/datetime_operation")
	}
	if transformErr != nil {
		return transformErr
	}
	onError := strings.ToLower(automationConfigString(config, "on_error"))
	if onError == "" {
		onError = "fail_fast"
	}
	if onError != "fail_fast" && onError != "collect" {
		return errors.New("Foreach 错误策略必须是 fail_fast/collect")
	}
	maxItems, exists, err := automationConfigOptionalInt(config, "max_items")
	if err != nil {
		return err
	}
	if !exists {
		maxItems = 100
	}
	if maxItems < 1 || maxItems > automationMaxForeachItems {
		return fmt.Errorf("Foreach max_items 必须在 1 到 %d 之间", automationMaxForeachItems)
	}
	return nil
}

func automationForeachTransform(config map[string]any) (map[string]any, error) {
	raw, exists := config["transform"]
	if !exists {
		return nil, errors.New("Foreach 必须配置 transform")
	}
	transform, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("Foreach transform 格式无效")
	}
	return transform, nil
}

func executeAutomationForeachNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	baseOutput := map[string]any{
		"results": []any{}, "count": 0, "succeeded_count": 0, "failed_count": 0,
		"errors": []any{}, "selected_port": "failure",
	}
	input, _, _, err := resolveAutomationVariableValue(runContext, node.Config["input"])
	if err != nil {
		return automationVariableFailure(baseOutput, err), nil
	}
	if err := validateAutomationValueSize(input, "Foreach 输入"); err != nil {
		return automationVariableFailure(baseOutput, err), nil
	}
	items, err := automationListInput(input)
	if err != nil {
		return automationVariableFailure(baseOutput, errors.New("Foreach 输入必须是数组")), nil
	}
	maxItems, exists, _ := automationConfigOptionalInt(node.Config, "max_items")
	if !exists {
		maxItems = 100
	}
	if len(items) > maxItems || len(items) > automationMaxForeachItems {
		return automationVariableFailure(baseOutput, fmt.Errorf("Foreach 输入 %d 项超过 max_items=%d", len(items), maxItems)), nil
	}
	transform, err := automationForeachTransform(node.Config)
	if err != nil {
		return automationVariableFailure(baseOutput, err), nil
	}
	results := make([]any, len(items))
	errorsFound := make([]any, 0)
	succeeded := 0
	failed := 0
	onError := strings.ToLower(automationConfigString(node.Config, "on_error"))
	if onError == "" {
		onError = "fail_fast"
	}
	for index, item := range items {
		itemContext := cloneAutomationConfig(runContext)
		itemContext["each"] = map[string]any{
			"item": item, "index": index, "count": len(items),
			"first": index == 0, "last": index == len(items)-1,
		}
		var result any
		transformErr := validateAutomationForeachItemSize(item, "输入项")
		if transformErr == nil {
			result, transformErr = executeAutomationForeachTransform(transform, itemContext)
		}
		if transformErr == nil {
			transformErr = validateAutomationForeachItemSize(result, "结果项")
		}
		if transformErr != nil {
			failed++
			errorsFound = append(errorsFound, map[string]any{"index": index, "reason": transformErr.Error()})
			if onError == "fail_fast" {
				baseOutput["results"] = results
				baseOutput["count"] = len(items)
				baseOutput["succeeded_count"] = succeeded
				baseOutput["failed_count"] = failed
				baseOutput["errors"] = errorsFound
				return automationVariableFailure(baseOutput, transformErr), nil
			}
			continue
		}
		results[index] = result
		succeeded++
	}
	baseOutput["results"] = results
	baseOutput["count"] = len(items)
	baseOutput["succeeded_count"] = succeeded
	baseOutput["failed_count"] = failed
	baseOutput["errors"] = errorsFound
	if err := validateAutomationValueSize(baseOutput, "Foreach 输出"); err != nil {
		return automationVariableFailure(compactAutomationForeachOutput(baseOutput), err), nil
	}
	if len(items) == 0 {
		written := automationVariableWrite(baseOutput, runContext, node.Config, results)
		if written["selected_port"] != "failure" {
			written["selected_port"] = "empty"
		}
		return written, nil
	}
	if succeeded == 0 {
		return automationVariableFailure(baseOutput, errors.New("Foreach 所有项目转换失败")), nil
	}
	written := automationVariableWrite(baseOutput, runContext, node.Config, results)
	if written["selected_port"] == "failure" {
		return written, nil
	}
	if failed > 0 {
		written["partial"] = true
		written["selected_port"] = "partial"
	}
	if err := validateAutomationValueSize(written, "Foreach 输出"); err != nil {
		return automationVariableFailure(compactAutomationForeachOutput(baseOutput), err), nil
	}
	return written, nil
}

func validateAutomationForeachItemSize(value any, label string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("Foreach %s 不是有效 JSON: %w", label, err)
	}
	if len(encoded) > automationMaxForeachItem {
		return fmt.Errorf("Foreach %s JSON 不能超过 %d 字节", label, automationMaxForeachItem)
	}
	if err := validateAutomationJSONSafe(encoded); err != nil {
		return fmt.Errorf("Foreach %s: %w", label, err)
	}
	return nil
}

func compactAutomationForeachOutput(output map[string]any) map[string]any {
	compact := map[string]any{"results": []any{}, "errors": []any{}, "selected_port": "failure"}
	for _, key := range []string{"count", "succeeded_count", "failed_count"} {
		if value, exists := output[key]; exists {
			compact[key] = value
		}
	}
	return compact
}

func executeAutomationForeachTransform(transform map[string]any, runContext map[string]any) (any, error) {
	transformType := strings.ToLower(automationConfigString(transform, "type"))
	config, err := automationNestedConfig(transform, "config")
	if err != nil {
		return nil, err
	}
	config = cloneAutomationConfig(config)
	config["variable"] = "_foreach_result"
	config["overwrite"] = "overwrite"
	node := AutomationNode{Type: transformType, Config: config}
	var output map[string]any
	switch transformType {
	case AutomationNodeTemplate:
		output, _ = executeAutomationTemplateNode(node, runContext)
	case AutomationNodeJSONExtract:
		output, _ = executeAutomationJSONExtractNode(node, runContext)
	case AutomationNodeMath:
		output, _ = executeAutomationMathNode(node, runContext)
	case AutomationNodeCoalesce:
		output, _ = executeAutomationCoalesceNode(node, runContext)
	case AutomationNodeDatetimeOperation:
		output, _ = executeAutomationDatetimeOperationNode(node, runContext)
	default:
		return nil, fmt.Errorf("不支持的 Foreach transform.type %q", transformType)
	}
	if output == nil || output["selected_port"] == "failure" {
		reason := "转换失败"
		if output != nil && strings.TrimSpace(fmt.Sprint(output["reason"])) != "" {
			reason = fmt.Sprint(output["reason"])
		}
		return nil, errors.New(reason)
	}
	result, exists := output["result"]
	if !exists {
		return nil, errors.New("转换节点没有返回 result")
	}
	return result, nil
}
