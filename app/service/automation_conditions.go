package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

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

func isAutomationCompareAs(value string) bool {
	switch value {
	case "auto", "string", "number", "boolean", "datetime":
		return true
	default:
		return false
	}
}

func convertAutomationComparable(value any, compareAs string) (any, error) {
	if value == nil || compareAs == "auto" {
		return value, nil
	}
	switch compareAs {
	case "string":
		return automationVariableString(value), nil
	case "number":
		return automationFiniteNumber(value)
	case "boolean":
		return convertAutomationVariableValue(value, "boolean")
	case "datetime":
		parsed := parseAutomationTime(automationVariableString(value))
		if parsed == nil {
			return nil, fmt.Errorf("%q 不是支持的日期时间", automationVariableString(value))
		}
		return *parsed, nil
	default:
		return nil, fmt.Errorf("不支持的比较类型 %q", compareAs)
	}
}

func compareAutomationComparable(left, right any) int {
	if leftNumber, ok := automationNumber(left); ok {
		if rightNumber, rightOK := automationNumber(right); rightOK {
			switch {
			case leftNumber < rightNumber:
				return -1
			case leftNumber > rightNumber:
				return 1
			default:
				return 0
			}
		}
	}
	if leftTime, ok := left.(time.Time); ok {
		if rightTime, rightOK := right.(time.Time); rightOK {
			switch {
			case leftTime.Before(rightTime):
				return -1
			case leftTime.After(rightTime):
				return 1
			default:
				return 0
			}
		}
	}
	if leftTime := parseAutomationTime(automationVariableString(left)); leftTime != nil {
		if rightTime := parseAutomationTime(automationVariableString(right)); rightTime != nil {
			switch {
			case leftTime.Before(*rightTime):
				return -1
			case leftTime.After(*rightTime):
				return 1
			default:
				return 0
			}
		}
	}
	return strings.Compare(automationVariableString(left), automationVariableString(right))
}

func validateAutomationConditionShape(raw any) error {
	condition, ok := raw.(map[string]any)
	if !ok {
		return errors.New("IF 条件格式无效")
	}
	for _, logicKey := range []string{"all", "any"} {
		if rawItems, exists := condition[logicKey]; exists {
			items, ok := rawItems.([]any)
			if !ok || len(items) == 0 {
				return fmt.Errorf("%s 条件不能为空", logicKey)
			}
			for _, item := range items {
				if err := validateAutomationConditionShape(item); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if nested, exists := condition["not"]; exists {
		return validateAutomationConditionShape(nested)
	}
	field := strings.TrimSpace(fmt.Sprint(firstAutomationConditionValue(condition, "field", "left")))
	operator := strings.ToLower(strings.TrimSpace(fmt.Sprint(firstAutomationConditionValue(condition, "operator", "op"))))
	if field == "" {
		return errors.New("IF 条件字段不能为空")
	}
	switch operator {
	case "exists", "not_exists":
		return nil
	case "eq", "neq", "gt", "gte", "lt", "lte", "contains", "not_contains", "starts_with", "ends_with":
		if _, exists := condition["value"]; !exists {
			if _, exists = condition["right"]; !exists {
				return errors.New("IF 条件缺少比较值")
			}
		}
		return nil
	case "regex":
		pattern := fmt.Sprint(firstAutomationConditionValue(condition, "value", "right"))
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("IF 正则表达式无效: %w", err)
		}
		return nil
	case "in":
		if _, ok := firstAutomationConditionValue(condition, "value", "right").([]any); !ok {
			return errors.New("in 条件的比较值必须是数组")
		}
		return nil
	default:
		return fmt.Errorf("不支持的 IF 条件操作符 %q", operator)
	}
}
