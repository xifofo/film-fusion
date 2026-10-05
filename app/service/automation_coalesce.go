package service

import (
	"errors"
	"fmt"
	"strings"
)

func validateAutomationCoalesceConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	return validateAutomationCoalescePureConfig(config)
}

func validateAutomationCoalescePureConfig(config map[string]any) error {
	raw, exists := config["candidates"]
	if !exists {
		raw = []any{}
	}
	candidates, ok := raw.([]any)
	if !ok {
		return errors.New("Coalesce candidates 必须是数组")
	}
	if len(candidates) > automationMaxCoalesce {
		return fmt.Errorf("Coalesce 候选值不能超过 %d 个", automationMaxCoalesce)
	}
	missing := strings.ToLower(automationConfigString(config, "missing"))
	if missing == "" {
		missing = "skip"
	}
	if missing != "skip" && missing != "failure" {
		return errors.New("Coalesce 缺失值策略必须是 skip/failure")
	}
	for _, key := range []string{"skip_null", "skip_empty_string", "skip_empty_array", "skip_empty_object", "trim_strings"} {
		if value, exists := config[key]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s 必须是布尔值", key)
			}
		}
	}
	onEmpty := strings.ToLower(automationConfigString(config, "on_empty"))
	if onEmpty == "" {
		onEmpty = "failure"
	}
	if onEmpty != "failure" && onEmpty != "default" {
		return errors.New("Coalesce 空结果策略必须是 failure/default")
	}
	valueType := strings.ToLower(automationConfigString(config, "value_type"))
	if valueType == "" {
		valueType = "auto"
	}
	if !isAutomationVariableValueType(valueType) {
		return errors.New("Coalesce 结果类型必须是 auto/string/integer/number/boolean/datetime/json")
	}
	return nil
}

func executeAutomationCoalesceNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	result, output, err := executeAutomationCoalescePure(node.Config, runContext)
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	return automationVariableWrite(output, runContext, node.Config, result), nil
}

func executeAutomationCoalescePure(config map[string]any, runContext map[string]any) (any, map[string]any, error) {
	output := map[string]any{"selected_index": -1, "used_default": false}
	candidates, _ := config["candidates"].([]any)
	missingPolicy := strings.ToLower(automationConfigString(config, "missing"))
	if missingPolicy == "" {
		missingPolicy = "skip"
	}
	for index, raw := range candidates {
		value, missing, err := resolveAutomationCoalesceCandidate(runContext, raw)
		if err != nil {
			return nil, output, err
		}
		if missing {
			if missingPolicy == "failure" {
				return nil, output, fmt.Errorf("Coalesce 第 %d 个候选值不存在", index)
			}
			continue
		}
		if text, ok := value.(string); ok && automationConfigBool(config, "trim_strings") {
			value = strings.TrimSpace(text)
		}
		if automationCoalesceSkips(value, config) {
			continue
		}
		converted, err := automationCoalesceConvert(value, config)
		if err != nil {
			return nil, output, err
		}
		output["selected_index"] = index
		output["result"] = converted
		return converted, output, nil
	}
	onEmpty := strings.ToLower(automationConfigString(config, "on_empty"))
	if onEmpty == "" {
		onEmpty = "failure"
	}
	if onEmpty != "default" {
		return nil, output, errors.New("Coalesce 没有可用候选值")
	}
	defaultValue := config["default_value"]
	if defaultValue == nil {
		defaultValue = ""
	}
	value, missing, err := resolveAutomationCoalesceCandidate(runContext, defaultValue)
	if err != nil {
		return nil, output, err
	}
	if missing {
		return nil, output, errors.New("Coalesce 默认值不存在")
	}
	if text, ok := value.(string); ok && automationConfigBool(config, "trim_strings") {
		value = strings.TrimSpace(text)
	}
	converted, err := automationCoalesceConvert(value, config)
	if err != nil {
		return nil, output, err
	}
	output["used_default"] = true
	output["result"] = converted
	return converted, output, nil
}

func resolveAutomationCoalesceCandidate(runContext map[string]any, raw any) (any, bool, error) {
	if text, ok := raw.(string); ok {
		trimmed := strings.TrimSpace(text)
		if isAutomationVariableReference(trimmed) {
			value, exists := resolveAutomationReference(runContext, trimmed)
			return value, !exists, nil
		}
		if strings.Contains(text, "{{") {
			result, missing, err := renderAutomationVariableTemplate(text, runContext, "error", false)
			if len(missing) > 0 {
				return nil, true, nil
			}
			return result, false, err
		}
	}
	value, _, _, err := resolveAutomationVariableValue(runContext, raw)
	return value, false, err
}

func automationCoalesceSkips(value any, config map[string]any) bool {
	skipNull := true
	if configured, exists := config["skip_null"]; exists {
		skipNull, _ = configured.(bool)
	}
	skipEmptyString := true
	if configured, exists := config["skip_empty_string"]; exists {
		skipEmptyString, _ = configured.(bool)
	}
	if value == nil {
		return skipNull
	}
	switch typed := value.(type) {
	case string:
		return skipEmptyString && typed == ""
	case []any:
		return automationConfigBool(config, "skip_empty_array") && len(typed) == 0
	case map[string]any:
		return automationConfigBool(config, "skip_empty_object") && len(typed) == 0
	default:
		return false
	}
}

func automationCoalesceConvert(value any, config map[string]any) (any, error) {
	valueType := strings.ToLower(automationConfigString(config, "value_type"))
	if valueType == "" {
		valueType = "auto"
	}
	return convertAutomationVariableValue(value, valueType)
}
