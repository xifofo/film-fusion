package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func validateAutomationListOperationConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	if _, exists := config["input"]; !exists {
		return errors.New("列表节点必须配置 input")
	}
	operation := strings.ToLower(automationConfigString(config, "operation"))
	if operation == "" {
		operation = "unique"
	}
	switch operation {
	case "split", "join", "unique", "sort", "reverse", "slice", "pluck", "length":
	default:
		return errors.New("列表操作必须是 split/join/unique/sort/reverse/slice/pluck/length")
	}
	for _, key := range []string{"trim_items", "omit_empty"} {
		if value, exists := config[key]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s 必须是布尔值", key)
			}
		}
	}
	if pointerValue, exists := config["pointer"]; exists {
		pointer, ok := pointerValue.(string)
		if !ok {
			return errors.New("列表 JSON Pointer 必须是文本")
		}
		if _, err := decodeAutomationJSONPointer(pointer); err != nil {
			return err
		}
	}
	missing := strings.ToLower(automationConfigString(config, "missing"))
	if missing == "" {
		missing = "failure"
	}
	if missing != "failure" && missing != "skip" && missing != "null" {
		return errors.New("列表字段缺失策略必须是 failure/skip/null")
	}
	direction := strings.ToLower(automationConfigString(config, "direction"))
	if direction == "" {
		direction = "asc"
	}
	if direction != "asc" && direction != "desc" {
		return errors.New("列表排序方向必须是 asc/desc")
	}
	compareAs := strings.ToLower(automationConfigString(config, "compare_as"))
	if compareAs == "" {
		compareAs = "auto"
	}
	if !isAutomationCompareAs(compareAs) {
		return errors.New("列表比较类型必须是 auto/string/number/boolean/datetime")
	}
	if offset, exists, err := automationConfigOptionalInt(config, "offset"); err != nil {
		return err
	} else if exists && (offset < -automationMaxListItems || offset > automationMaxListItems) {
		return fmt.Errorf("列表切片 offset 必须在 %d 到 %d 之间", -automationMaxListItems, automationMaxListItems)
	}
	if limit, exists, err := automationConfigOptionalInt(config, "limit"); err != nil {
		return err
	} else if exists && (limit < 0 || limit > automationMaxListItems) {
		return fmt.Errorf("列表切片 limit 必须在 0 到 %d 之间", automationMaxListItems)
	}
	return nil
}

func executeAutomationListOperationNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	result, output, empty, err := executeAutomationListOperationPure(node.Config, runContext)
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	written := automationVariableWrite(output, runContext, node.Config, result)
	if written["selected_port"] != "failure" && empty {
		written["selected_port"] = "empty"
	}
	return written, nil
}

func executeAutomationListOperationPure(config map[string]any, runContext map[string]any) (any, map[string]any, bool, error) {
	operation := strings.ToLower(automationConfigString(config, "operation"))
	if operation == "" {
		operation = "unique"
	}
	output := map[string]any{"operation": operation}
	input, _, _, err := resolveAutomationVariableValue(runContext, config["input"])
	if err != nil {
		return nil, output, false, err
	}
	if err := validateAutomationValueSize(input, "列表输入"); err != nil {
		return nil, output, false, err
	}
	separator := automationConfigRawString(config, "separator")
	if _, exists := config["separator"]; !exists {
		separator = ","
	}
	trimItems := true
	if value, exists := config["trim_items"]; exists {
		trimItems, _ = value.(bool)
	}
	omitEmpty := true
	if value, exists := config["omit_empty"]; exists {
		omitEmpty, _ = value.(bool)
	}
	var result any
	inputCount := 0
	outputCount := 0
	empty := false
	switch operation {
	case "split":
		text, ok := input.(string)
		if !ok {
			return nil, output, false, errors.New("split 输入必须是文本")
		}
		parts := strings.Split(text, separator)
		inputCount = len(parts)
		if inputCount > automationMaxListItems {
			return nil, output, false, fmt.Errorf("列表输入不能超过 %d 项", automationMaxListItems)
		}
		items := make([]any, 0, len(parts))
		for _, part := range parts {
			if trimItems {
				part = strings.TrimSpace(part)
			}
			if omitEmpty && part == "" {
				continue
			}
			items = append(items, part)
		}
		result = items
		outputCount = len(items)
		empty = len(items) == 0
	case "join":
		items, arrayErr := automationListInput(input)
		if arrayErr != nil {
			return nil, output, false, arrayErr
		}
		inputCount = len(items)
		parts := make([]string, 0, len(items))
		for _, item := range items {
			part := automationVariableString(item)
			if trimItems {
				part = strings.TrimSpace(part)
			}
			if omitEmpty && part == "" {
				continue
			}
			parts = append(parts, part)
		}
		joined := strings.Join(parts, separator)
		result = joined
		if joined != "" {
			outputCount = 1
		}
		empty = joined == ""
	case "unique", "sort", "reverse", "slice", "pluck", "length":
		items, arrayErr := automationListInput(input)
		if arrayErr != nil {
			return nil, output, false, arrayErr
		}
		inputCount = len(items)
		switch operation {
		case "unique":
			result, err = automationUniqueList(items, config)
		case "sort":
			result, err = automationSortList(items, config)
		case "reverse":
			reversed := append([]any(nil), items...)
			for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
				reversed[left], reversed[right] = reversed[right], reversed[left]
			}
			result = reversed
		case "slice":
			result = automationSliceList(items, config)
		case "pluck":
			result, err = automationPluckList(items, config)
		case "length":
			result = int64(len(items))
		}
		if err != nil {
			return nil, output, false, err
		}
		if arrayResult, ok := result.([]any); ok {
			outputCount = len(arrayResult)
			empty = len(arrayResult) == 0
		} else {
			if len(items) > 0 {
				outputCount = 1
			}
			empty = len(items) == 0
		}
	default:
		return nil, output, false, fmt.Errorf("不支持的列表操作 %q", operation)
	}
	if outputCount > automationMaxListItems {
		return nil, output, false, fmt.Errorf("列表结果不能超过 %d 项", automationMaxListItems)
	}
	if err := validateAutomationValueSize(result, "列表结果"); err != nil {
		return nil, output, false, err
	}
	output["result"] = result
	output["input_count"] = inputCount
	output["output_count"] = outputCount
	output["empty"] = empty
	return result, output, empty, nil
}

func automationListInput(input any) ([]any, error) {
	if items, ok := input.([]any); ok {
		if len(items) > automationMaxListItems {
			return nil, fmt.Errorf("列表输入不能超过 %d 项", automationMaxListItems)
		}
		return items, nil
	}
	if text, ok := input.(string); ok {
		decoded, err := convertAutomationVariableValue(text, "json")
		if err == nil {
			if items, ok := decoded.([]any); ok {
				if len(items) > automationMaxListItems {
					return nil, fmt.Errorf("列表输入不能超过 %d 项", automationMaxListItems)
				}
				return items, nil
			}
		}
	}
	return nil, errors.New("列表操作输入必须是数组")
}

func automationListPointerValues(items []any, config map[string]any) ([]any, []bool, error) {
	pointer := automationConfigRawString(config, "pointer")
	segments, err := decodeAutomationJSONPointer(pointer)
	if err != nil {
		return nil, nil, err
	}
	missingPolicy := strings.ToLower(automationConfigString(config, "missing"))
	if missingPolicy == "" {
		missingPolicy = "failure"
	}
	values := make([]any, len(items))
	included := make([]bool, len(items))
	for index, item := range items {
		value, found := extractAutomationJSONPointer(item, segments)
		if found {
			values[index] = value
			included[index] = true
			continue
		}
		switch missingPolicy {
		case "failure":
			return nil, nil, fmt.Errorf("列表第 %d 项没有找到 JSON Pointer %q", index, pointer)
		case "skip":
		case "null":
			values[index] = nil
			included[index] = true
		default:
			return nil, nil, fmt.Errorf("不支持的列表缺失策略 %q", missingPolicy)
		}
	}
	return values, included, nil
}

func automationUniqueList(items []any, config map[string]any) ([]any, error) {
	values, included, err := automationListPointerValues(items, config)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]any, 0, len(items))
	compareAs := strings.ToLower(automationConfigString(config, "compare_as"))
	if compareAs == "" {
		compareAs = "auto"
	}
	for index, item := range items {
		if !included[index] {
			continue
		}
		keyValue, err := automationUniqueComparable(values[index], compareAs)
		if err != nil && values[index] != nil {
			return nil, fmt.Errorf("列表第 %d 项无法按 %s 比较: %w", index, compareAs, err)
		}
		keyBytes, err := json.Marshal(keyValue)
		if err != nil {
			return nil, fmt.Errorf("列表第 %d 项无法比较: %w", index, err)
		}
		key := string(keyBytes)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result, nil
}

func automationUniqueComparable(value any, compareAs string) (any, error) {
	if compareAs != "auto" || value == nil {
		return convertAutomationComparable(value, compareAs)
	}
	switch typed := value.(type) {
	case int, int64, uint, uint64, float32, float64, json.Number:
		return automationFiniteNumber(value)
	case int8:
		return float64(typed), nil
	case int16:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case uint8:
		return float64(typed), nil
	case uint16:
		return float64(typed), nil
	case uint32:
		return float64(typed), nil
	default:
		return value, nil
	}
}

type automationSortableItem struct {
	item  any
	key   any
	index int
}

func automationSortList(items []any, config map[string]any) ([]any, error) {
	values, included, err := automationListPointerValues(items, config)
	if err != nil {
		return nil, err
	}
	compareAs := strings.ToLower(automationConfigString(config, "compare_as"))
	if compareAs == "" {
		compareAs = "auto"
	}
	sortable := make([]automationSortableItem, 0, len(items))
	for index, item := range items {
		if !included[index] {
			continue
		}
		key, err := convertAutomationComparable(values[index], compareAs)
		if err != nil && values[index] != nil {
			return nil, fmt.Errorf("列表第 %d 项无法按 %s 比较: %w", index, compareAs, err)
		}
		sortable = append(sortable, automationSortableItem{item: item, key: key, index: index})
	}
	direction := strings.ToLower(automationConfigString(config, "direction"))
	if direction == "" {
		direction = "asc"
	}
	sort.SliceStable(sortable, func(left, right int) bool {
		leftKey, rightKey := sortable[left].key, sortable[right].key
		if leftKey == nil || rightKey == nil {
			if leftKey == nil && rightKey == nil {
				return sortable[left].index < sortable[right].index
			}
			return rightKey == nil
		}
		comparison := compareAutomationComparable(leftKey, rightKey)
		if direction == "desc" {
			comparison = -comparison
		}
		if comparison == 0 {
			return sortable[left].index < sortable[right].index
		}
		return comparison < 0
	})
	result := make([]any, len(sortable))
	for index, item := range sortable {
		result[index] = item.item
	}
	return result, nil
}

func automationSliceList(items []any, config map[string]any) []any {
	offset, exists, _ := automationConfigOptionalInt(config, "offset")
	if !exists {
		offset = 0
	}
	if offset < 0 {
		offset = len(items) + offset
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	limit, exists, _ := automationConfigOptionalInt(config, "limit")
	if !exists {
		limit = 100
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return append([]any(nil), items[offset:end]...)
}

func automationPluckList(items []any, config map[string]any) ([]any, error) {
	values, included, err := automationListPointerValues(items, config)
	if err != nil {
		return nil, err
	}
	result := make([]any, 0, len(items))
	for index, value := range values {
		if included[index] {
			result = append(result, value)
		}
	}
	return result, nil
}
