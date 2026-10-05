package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var automationSwitchCaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)

func validateAutomationSwitchConfig(config map[string]any) error {
	if _, exists := config["input"]; !exists {
		return errors.New("Switch 必须配置 input")
	}
	compareAs := strings.ToLower(automationConfigString(config, "compare_as"))
	if compareAs == "" {
		compareAs = "auto"
	}
	if !isAutomationCompareAs(compareAs) {
		return errors.New("Switch 比较类型必须是 auto/string/number/boolean/datetime")
	}
	if value, exists := config["case_sensitive"]; exists {
		if _, ok := value.(bool); !ok {
			return errors.New("Switch 区分大小写配置必须是布尔值")
		}
	}
	cases, err := automationSwitchCases(config)
	if err != nil {
		return err
	}
	if len(cases) == 0 || len(cases) > automationMaxSwitchCases {
		return fmt.Errorf("Switch 分支数量必须在 1 到 %d 之间", automationMaxSwitchCases)
	}
	seen := make(map[string]struct{}, len(cases))
	for index, item := range cases {
		id := automationConfigString(item, "id")
		if !automationSwitchCaseIDPattern.MatchString(id) {
			return fmt.Errorf("Switch 第 %d 个分支 ID 必须是 1 到 40 位 ASCII 字母、数字、下划线或连字符", index+1)
		}
		normalizedID := strings.ToLower(id)
		if _, exists := seen[normalizedID]; exists {
			return fmt.Errorf("Switch 分支 ID %q 重复", id)
		}
		seen[normalizedID] = struct{}{}
		label := strings.TrimSpace(automationConfigRawString(item, "label"))
		if label == "" {
			return fmt.Errorf("Switch 分支 %s 必须配置标签", id)
		}
		if len([]rune(label)) > 120 {
			return fmt.Errorf("Switch 分支 %s 标签不能超过 120 个字符", id)
		}
		operator := strings.ToLower(automationConfigString(item, "operator"))
		if err := validateAutomationSwitchOperator(operator, item); err != nil {
			return fmt.Errorf("Switch 分支 %s: %w", id, err)
		}
	}
	return nil
}

func validateAutomationSwitchOperator(operator string, item map[string]any) error {
	switch operator {
	case "exists", "not_exists":
		return nil
	case "eq", "neq", "gt", "gte", "lt", "lte", "contains", "not_contains", "starts_with", "ends_with":
		if _, exists := item["value"]; !exists {
			return errors.New("缺少比较值")
		}
		return nil
	case "regex":
		value, exists := item["value"]
		if !exists {
			return errors.New("缺少正则表达式")
		}
		if pattern, ok := value.(string); ok && !strings.Contains(pattern, "{{") && !isAutomationVariableReference(strings.TrimSpace(pattern)) {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("正则表达式无效: %w", err)
			}
		}
		return nil
	case "in":
		value, exists := item["value"]
		if !exists {
			return errors.New("缺少比较值")
		}
		if _, ok := value.([]any); !ok {
			reference, isText := value.(string)
			if !isText || (!isAutomationVariableReference(reference) && !strings.Contains(reference, "{{")) {
				return errors.New("in 的比较值必须是数组或数组变量")
			}
		}
		return nil
	default:
		return fmt.Errorf("不支持的操作符 %q", operator)
	}
}

func automationSwitchCases(config map[string]any) ([]map[string]any, error) {
	raw, exists := config["cases"]
	if !exists {
		return nil, errors.New("Switch 必须配置 cases")
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("Switch cases 必须是数组")
	}
	result := make([]map[string]any, 0, len(items))
	for index, item := range items {
		caseConfig, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Switch 第 %d 个分支格式无效", index+1)
		}
		result = append(result, caseConfig)
	}
	return result, nil
}

func automationSwitchCasePortSet(config map[string]any) map[string]struct{} {
	ports := map[string]struct{}{}
	cases, err := automationSwitchCases(config)
	if err != nil {
		return ports
	}
	for _, item := range cases {
		ports["case-"+strings.ToLower(automationConfigString(item, "id"))] = struct{}{}
	}
	return ports
}

func executeAutomationSwitchNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	input, inputExists, err := resolveAutomationSwitchInput(runContext, node.Config["input"])
	output := map[string]any{"matched": false, "value": input, "selected_port": "failure"}
	if err != nil {
		output["reason"] = err.Error()
		return output, nil
	}
	cases, err := automationSwitchCases(node.Config)
	if err != nil {
		output["reason"] = err.Error()
		return output, nil
	}
	for index, item := range cases {
		matched, matchErr := evaluateAutomationSwitchCase(input, inputExists, item, node.Config, runContext)
		if matchErr != nil {
			output["reason"] = fmt.Sprintf("Switch 分支 %s: %v", automationConfigString(item, "id"), matchErr)
			return output, nil
		}
		if !matched {
			continue
		}
		id := automationConfigString(item, "id")
		output["matched"] = true
		output["case_id"] = id
		output["case_label"] = automationConfigRawString(item, "label")
		output["case_index"] = index
		output["selected_port"] = "case-" + strings.ToLower(id)
		return output, nil
	}
	output["case_id"] = ""
	output["case_label"] = ""
	output["case_index"] = -1
	output["selected_port"] = "default"
	return output, nil
}

func resolveAutomationSwitchInput(runContext map[string]any, raw any) (any, bool, error) {
	if text, ok := raw.(string); ok {
		trimmed := strings.TrimSpace(text)
		if isAutomationVariableReference(trimmed) {
			value, exists := resolveAutomationReference(runContext, trimmed)
			return value, exists, nil
		}
	}
	value, _, _, err := resolveAutomationVariableValue(runContext, raw)
	return value, err == nil, err
}

func evaluateAutomationSwitchCase(input any, inputExists bool, item, nodeConfig map[string]any, runContext map[string]any) (bool, error) {
	operator := strings.ToLower(automationConfigString(item, "operator"))
	switch operator {
	case "exists":
		return inputExists && input != nil && strings.TrimSpace(automationVariableString(input)) != "", nil
	case "not_exists":
		return !inputExists || input == nil || strings.TrimSpace(automationVariableString(input)) == "", nil
	}
	if !inputExists {
		return false, nil
	}
	right, _, _, err := resolveAutomationVariableValue(runContext, item["value"])
	if err != nil {
		return false, err
	}
	compareAs := strings.ToLower(automationConfigString(nodeConfig, "compare_as"))
	if compareAs == "" {
		compareAs = "auto"
	}
	caseSensitive, _ := nodeConfig["case_sensitive"].(bool)
	leftComparable, err := convertAutomationComparable(input, compareAs)
	if err != nil {
		return false, err
	}
	if operator == "in" {
		items, ok := right.([]any)
		if !ok {
			return false, errors.New("in 的比较值必须是数组")
		}
		for _, candidate := range items {
			candidateComparable, convertErr := convertAutomationComparable(candidate, compareAs)
			if convertErr != nil {
				return false, convertErr
			}
			if automationSwitchEqual(leftComparable, candidateComparable, caseSensitive) {
				return true, nil
			}
		}
		return false, nil
	}
	rightComparable, err := convertAutomationComparable(right, compareAs)
	if err != nil {
		return false, err
	}
	switch operator {
	case "eq":
		return automationSwitchEqual(leftComparable, rightComparable, caseSensitive), nil
	case "neq":
		return !automationSwitchEqual(leftComparable, rightComparable, caseSensitive), nil
	case "gt", "gte", "lt", "lte":
		comparison := compareAutomationComparable(leftComparable, rightComparable)
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
	case "contains", "not_contains", "starts_with", "ends_with", "regex":
		leftText := automationVariableString(leftComparable)
		rightText := automationVariableString(rightComparable)
		if !caseSensitive && operator != "regex" {
			leftText = strings.ToLower(leftText)
			rightText = strings.ToLower(rightText)
		}
		switch operator {
		case "contains":
			return strings.Contains(leftText, rightText), nil
		case "not_contains":
			return !strings.Contains(leftText, rightText), nil
		case "starts_with":
			return strings.HasPrefix(leftText, rightText), nil
		case "ends_with":
			return strings.HasSuffix(leftText, rightText), nil
		case "regex":
			if !caseSensitive {
				rightText = "(?i:" + rightText + ")"
			}
			pattern, compileErr := regexp.Compile(rightText)
			if compileErr != nil {
				return false, compileErr
			}
			return pattern.MatchString(leftText), nil
		}
	}
	return false, fmt.Errorf("不支持的 Switch 操作符 %q", operator)
}

func automationSwitchEqual(left, right any, caseSensitive bool) bool {
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	if leftIsText && rightIsText {
		if !caseSensitive {
			return strings.EqualFold(leftText, rightText)
		}
		return leftText == rightText
	}
	return automationValuesEqual(left, right)
}
