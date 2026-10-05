package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func automationConfigString(config map[string]any, key string) string {
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func automationConfigUint(config map[string]any, key string) uint {
	value, ok := config[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		if typed > 0 {
			return uint(typed)
		}
	case int:
		if typed > 0 {
			return uint(typed)
		}
	case uint:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		if parsed > 0 {
			return uint(parsed)
		}
	}
	return 0
}

func automationConfigStringSlice(config map[string]any, key string) []string {
	value, ok := config[key]
	if !ok || value == nil {
		return nil
	}
	values, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			values = make([]any, len(typed))
			for index := range typed {
				values[index] = typed[index]
			}
		} else {
			return nil
		}
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		if text := strings.ToLower(strings.TrimSpace(fmt.Sprint(item))); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func automationConfigTextSlice(config map[string]any, key string) []string {
	value, ok := config[key]
	if !ok || value == nil {
		return nil
	}
	values, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			values = make([]any, len(typed))
			for index := range typed {
				values[index] = typed[index]
			}
		} else {
			return nil
		}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		text := strings.TrimSpace(fmt.Sprint(item))
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
	}
	return result
}

type automationReplacementRule struct {
	Keyword     string
	Replacement string
}

func automationConfigReplacementRules(config map[string]any, key string) ([]automationReplacementRule, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("关键词替换规则格式无效")
	}
	rules := make([]automationReplacementRule, 0, len(values))
	for index, value := range values {
		raw, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("第 %d 条关键词替换规则格式无效", index+1)
		}
		keywordValue, keywordOK := raw["keyword"].(string)
		replacementValue, replacementOK := raw["replacement"].(string)
		keyword := strings.TrimSpace(keywordValue)
		if !keywordOK || keyword == "" {
			return nil, fmt.Errorf("第 %d 条关键词替换规则的关键词不能为空", index+1)
		}
		if !replacementOK {
			return nil, fmt.Errorf("第 %d 条关键词替换规则的替换内容必须是文本", index+1)
		}
		rules = append(rules, automationReplacementRule{
			Keyword: keyword, Replacement: replacementValue,
		})
	}
	return rules, nil
}

func automationConfigRawString(config map[string]any, key string) string {
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
