package service

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

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
	return automationVariableWrite(map[string]any{
		"matched": true, "captured": matches[groupIndex],
	}, runContext, node.Config, value), nil
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
	return automationVariableWrite(map[string]any{
		"input":             input,
		"result":            result,
		"replacement_count": replacementCount,
	}, runContext, node.Config, result), nil
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
	return automationVariableWrite(map[string]any{
		"input":             input,
		"result":            result,
		"replacement_count": replacementCount,
	}, runContext, node.Config, result), nil
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
	return automationVariableWrite(nil, runContext, node.Config, converted), nil
}
