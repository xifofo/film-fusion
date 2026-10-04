package service

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var automationParameterName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var automationParameterTemplate = regexp.MustCompile(`\{\{\s*params\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// Resolve a fresh definition so validation and execution share typed parameter
// values without replacing the editable references in the persisted snapshot.
func resolveAutomationParameters(definition AutomationDefinition) (AutomationDefinition, error) {
	if len(definition.Parameters) > 100 {
		return definition, fmt.Errorf("流程参数不能超过 100 个")
	}
	for name, value := range definition.Parameters {
		if !automationParameterName.MatchString(name) || validateAutomationVariableName(name) != nil {
			return definition, fmt.Errorf("流程参数名 %q 无效，请使用字母、数字和下划线", name)
		}
		switch v := value.(type) {
		case string:
			if len(v) > 16384 {
				return definition, fmt.Errorf("参数 %s 的文本过长", name)
			}
		case bool, int, int64, uint, uint64, json.Number:
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return definition, fmt.Errorf("参数 %s 必须是有限数值", name)
			}
		default:
			return definition, fmt.Errorf("参数 %s 只支持文本、数字或布尔值", name)
		}
	}
	lookup := func(name string) (any, error) {
		value, ok := definition.Parameters[name]
		if !ok {
			return nil, fmt.Errorf("流程参数 %s 未定义", name)
		}
		return value, nil
	}
	var resolve func(any) (any, error)
	resolve = func(raw any) (any, error) {
		switch value := raw.(type) {
		case string:
			if strings.HasPrefix(value, "$params.") && automationParameterName.MatchString(strings.TrimPrefix(value, "$params.")) {
				return lookup(strings.TrimPrefix(value, "$params."))
			}
			matches := automationParameterTemplate.FindAllStringSubmatch(value, -1)
			for _, match := range matches {
				parameter, err := lookup(match[1])
				if err != nil {
					return nil, err
				}
				value = strings.ReplaceAll(value, match[0], fmt.Sprint(parameter))
			}
			return value, nil
		case map[string]any:
			result := make(map[string]any, len(value))
			for key, item := range value {
				next, err := resolve(item)
				if err != nil {
					return nil, err
				}
				result[key] = next
			}
			return result, nil
		case []any:
			result := make([]any, len(value))
			for i, item := range value {
				next, err := resolve(item)
				if err != nil {
					return nil, err
				}
				result[i] = next
			}
			return result, nil
		default:
			return raw, nil
		}
	}
	result := definition
	result.Nodes = append([]AutomationNode(nil), definition.Nodes...)
	for i, node := range result.Nodes {
		config, err := resolve(node.Config)
		if err != nil {
			return definition, fmt.Errorf("节点 %s: %w", node.ID, err)
		}
		result.Nodes[i].Config, _ = config.(map[string]any)
		if result.Nodes[i].Config == nil {
			result.Nodes[i].Config = map[string]any{}
		}
		for field, name := range node.ParameterBindings {
			if !automationParameterName.MatchString(field) || validateAutomationVariableName(field) != nil {
				return definition, fmt.Errorf("节点 %s 的参数绑定字段无效", node.ID)
			}
			value, err := lookup(name)
			if err != nil {
				return definition, fmt.Errorf("节点 %s: %w", node.ID, err)
			}
			result.Nodes[i].Config[field] = value
		}
	}
	return result, nil
}
