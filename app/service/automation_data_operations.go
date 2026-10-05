package service

import (
	"encoding/json"
	"fmt"
)

const (
	automationMaxListItems    = 10000
	automationMaxSwitchCases  = 20
	automationMaxCoalesce     = 32
	automationMaxForeachItems = 1000
	automationMaxForeachItem  = 64 << 10
)

func validateAutomationTemplatePureConfig(config map[string]any) error {
	copyConfig := cloneAutomationConfig(config)
	copyConfig["variable"] = "_foreach_result"
	copyConfig["overwrite"] = "overwrite"
	return validateAutomationTemplateConfig(copyConfig)
}

func validateAutomationJSONExtractPureConfig(config map[string]any) error {
	copyConfig := cloneAutomationConfig(config)
	copyConfig["variable"] = "_foreach_result"
	copyConfig["overwrite"] = "overwrite"
	return validateAutomationJSONExtractConfig(copyConfig)
}

func validateAutomationMathPureConfig(config map[string]any) error {
	copyConfig := cloneAutomationConfig(config)
	copyConfig["variable"] = "_foreach_result"
	copyConfig["overwrite"] = "overwrite"
	return validateAutomationMathConfig(copyConfig)
}

func automationNestedConfig(parent map[string]any, key string) (map[string]any, error) {
	raw, exists := parent[key]
	if !exists || raw == nil {
		return map[string]any{}, nil
	}
	config, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 必须是对象", key)
	}
	return config, nil
}

func cloneAutomationConfig(config map[string]any) map[string]any {
	copyConfig := make(map[string]any, len(config)+2)
	for key, value := range config {
		copyConfig[key] = value
	}
	return copyConfig
}

func validateAutomationValueSize(value any, label string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s 不是有效 JSON: %w", label, err)
	}
	if len(encoded) > automationMaxVariableJSONBytes {
		return fmt.Errorf("%s JSON 不能超过 %d 字节", label, automationMaxVariableJSONBytes)
	}
	if err := validateAutomationJSONSafe(encoded); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
