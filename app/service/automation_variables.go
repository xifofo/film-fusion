package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	automationMaxVariableNameRunes   = 64
	automationMaxVariableJSONBytes   = 1 << 20
	automationMaxVariableTemplateLen = 64 << 10
	automationMaxTemplateReferences  = 200
	automationMaxJSONPointerLen      = 4096
	automationMaxJSONPointerSegments = 64
	automationMaxJSONNestingDepth    = 64
	automationJSONSafeInteger        = int64(1<<53 - 1)
)

var automationReservedVariableNames = map[string]struct{}{
	"__proto__":   {},
	"prototype":   {},
	"constructor": {},
}

var automationDecimalNumberPattern = regexp.MustCompile(`^[+-]?(?:(?:\d+(?:\.\d*)?)|(?:\.\d+))(?:[eE][+-]?\d+)?$`)

func automationVariableName(config map[string]any) (string, error) {
	raw, exists := config["variable"]
	if !exists {
		return "", errors.New("必须配置输出变量")
	}
	name, ok := raw.(string)
	if !ok {
		return "", errors.New("输出变量必须是文本")
	}
	name = strings.TrimSpace(name)
	if err := validateAutomationVariableName(name); err != nil {
		return "", err
	}
	return name, nil
}

func validateAutomationVariableName(name string) error {
	runes := []rune(name)
	if len(runes) == 0 || len(runes) > automationMaxVariableNameRunes {
		return fmt.Errorf("变量名长度必须在 1 到 %d 个字符之间", automationMaxVariableNameRunes)
	}
	if !unicode.IsLetter(runes[0]) && runes[0] != '_' {
		return errors.New("变量名必须以字母或下划线开头")
	}
	for _, char := range runes[1:] {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' {
			return errors.New("变量名只能包含字母、数字和下划线")
		}
	}
	if _, reserved := automationReservedVariableNames[strings.ToLower(name)]; reserved {
		return fmt.Errorf("变量名 %q 是保留名称", name)
	}
	return nil
}

func automationVariableOverwritePolicy(config map[string]any) string {
	policy := strings.ToLower(automationConfigString(config, "overwrite"))
	if policy == "" {
		return "overwrite"
	}
	return policy
}

func validateAutomationVariableWriterConfig(config map[string]any) error {
	if _, err := automationVariableName(config); err != nil {
		return err
	}
	switch automationVariableOverwritePolicy(config) {
	case "overwrite", "keep", "error":
		return nil
	default:
		return errors.New("变量覆盖策略必须是 overwrite/keep/error")
	}
}

func isAutomationScalarValueType(valueType string) bool {
	switch strings.ToLower(strings.TrimSpace(valueType)) {
	case "string", "integer", "number", "boolean", "datetime":
		return true
	default:
		return false
	}
}

func isAutomationVariableValueType(valueType string) bool {
	switch strings.ToLower(strings.TrimSpace(valueType)) {
	case "auto", "string", "integer", "number", "boolean", "datetime", "json":
		return true
	default:
		return false
	}
}

func validateAutomationSetVariableConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	if _, exists := config["value"]; !exists {
		return errors.New("必须配置变量值")
	}
	valueType := strings.ToLower(automationConfigString(config, "value_type"))
	if valueType == "" {
		valueType = "auto"
	}
	if !isAutomationVariableValueType(valueType) {
		return errors.New("变量类型必须是 auto/string/integer/number/boolean/datetime/json")
	}
	return nil
}

func validateAutomationTemplateConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	raw, exists := config["template"]
	if !exists {
		return errors.New("必须配置模板内容")
	}
	template, ok := raw.(string)
	if !ok {
		return errors.New("模板内容必须是文本")
	}
	if len(template) > automationMaxVariableTemplateLen {
		return fmt.Errorf("模板内容不能超过 %d 字节", automationMaxVariableTemplateLen)
	}
	missing := strings.ToLower(automationConfigString(config, "missing"))
	if missing == "" {
		missing = "error"
	}
	if missing != "error" && missing != "empty" {
		return errors.New("模板缺失变量策略必须是 error/empty")
	}
	if trim, exists := config["trim"]; exists {
		if _, ok := trim.(bool); !ok {
			return errors.New("模板去除首尾空白配置必须是布尔值")
		}
	}
	return nil
}

func validateAutomationJSONExtractConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	if _, exists := config["input"]; !exists {
		return errors.New("必须配置 JSON 输入")
	}
	pointerValue, exists := config["pointer"]
	if !exists {
		return errors.New("必须配置 JSON Pointer")
	}
	pointer, ok := pointerValue.(string)
	if !ok {
		return errors.New("JSON Pointer 必须是文本")
	}
	if _, err := decodeAutomationJSONPointer(pointer); err != nil {
		return err
	}
	missing := strings.ToLower(automationConfigString(config, "missing"))
	if missing == "" {
		missing = "failure"
	}
	if missing != "failure" && missing != "default" {
		return errors.New("JSON 路径缺失策略必须是 failure/default")
	}
	if missing == "default" {
		if _, exists := config["default_value"]; !exists {
			return errors.New("JSON 路径使用默认值时必须配置 default_value")
		}
	}
	valueType := strings.ToLower(automationConfigString(config, "value_type"))
	if valueType == "" {
		valueType = "auto"
	}
	if !isAutomationVariableValueType(valueType) {
		return errors.New("JSON 提取类型必须是 auto/string/integer/number/boolean/datetime/json")
	}
	return nil
}

func validateAutomationMathConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	operation := strings.ToLower(automationConfigString(config, "operation"))
	switch operation {
	case "add", "subtract", "multiply", "divide", "mod", "min", "max":
		if _, exists := config["right"]; !exists {
			return fmt.Errorf("数学操作 %s 必须配置 right", operation)
		}
	case "abs", "round", "floor", "ceil":
	default:
		return errors.New("数学操作必须是 add/subtract/multiply/divide/mod/min/max/abs/round/floor/ceil")
	}
	if _, exists := config["left"]; !exists {
		return errors.New("数学节点必须配置 left")
	}
	if precision, exists, err := automationConfigOptionalInt(config, "precision"); err != nil {
		return err
	} else if exists && (precision < 0 || precision > 12) {
		return errors.New("数学精度必须在 0 到 12 之间")
	}
	resultType := strings.ToLower(automationConfigString(config, "result_type"))
	if resultType == "" {
		resultType = "number"
	}
	if resultType != "number" && resultType != "integer" {
		return errors.New("数学结果类型必须是 number/integer")
	}
	return nil
}

func automationConfigOptionalInt(config map[string]any, key string) (int, bool, error) {
	value, exists := config[key]
	if !exists || value == nil {
		return 0, false, nil
	}
	var number float64
	switch typed := value.(type) {
	case int:
		return typed, true, nil
	case int64:
		if typed > math.MaxInt || typed < math.MinInt {
			return 0, true, fmt.Errorf("%s 超出整数范围", key)
		}
		return int(typed), true, nil
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, true, fmt.Errorf("%s 必须是整数", key)
		}
		number = parsed
	default:
		return 0, true, fmt.Errorf("%s 必须是整数", key)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number > math.MaxInt || number < math.MinInt {
		return 0, true, fmt.Errorf("%s 必须是整数", key)
	}
	return int(number), true, nil
}

func automationVariableProducerName(node AutomationNode) (string, bool) {
	switch node.Type {
	case AutomationNodeRegex, AutomationNodeKeywordReplace, AutomationNodeRegexReplace, AutomationNodeConvert,
		AutomationNodeSetVariable, AutomationNodeTemplate, AutomationNodeJSONExtract, AutomationNodeMath,
		AutomationNodeDatetimeOperation, AutomationNodeListOperation, AutomationNodeCoalesce, AutomationNodeForeach:
		name, err := automationVariableName(node.Config)
		return name, err == nil
	default:
		return "", false
	}
}

func validateAutomationVariableProducerConflicts(nodes map[string]AutomationNode, outgoing map[string][]AutomationEdge) []string {
	producers := make([]AutomationNode, 0)
	for _, node := range nodes {
		if _, ok := automationVariableProducerName(node); ok {
			producers = append(producers, node)
		}
	}
	sort.Slice(producers, func(i, j int) bool { return producers[i].ID < producers[j].ID })
	errorsFound := make([]string, 0)
	for leftIndex := 0; leftIndex < len(producers); leftIndex++ {
		leftName, _ := automationVariableProducerName(producers[leftIndex])
		for rightIndex := leftIndex + 1; rightIndex < len(producers); rightIndex++ {
			rightName, _ := automationVariableProducerName(producers[rightIndex])
			if leftName != rightName {
				continue
			}
			leftID := producers[leftIndex].ID
			rightID := producers[rightIndex].ID
			if automationGraphHasPath(leftID, rightID, outgoing) || automationGraphHasPath(rightID, leftID, outgoing) {
				continue
			}
			errorsFound = append(errorsFound, fmt.Sprintf("变量 %q 被互不可达的节点 %s 和 %s 同时写入；请使用不同变量或显式汇合后再覆盖", leftName, leftID, rightID))
		}
	}
	return errorsFound
}

func automationGraphHasPath(source, target string, outgoing map[string][]AutomationEdge) bool {
	visited := map[string]struct{}{}
	queue := []string{source}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == target {
			return true
		}
		if _, exists := visited[current]; exists {
			continue
		}
		visited[current] = struct{}{}
		for _, edge := range outgoing[current] {
			queue = append(queue, edge.Target)
		}
	}
	return false
}

func automationAncestorTopologicalOrder(definition AutomationDefinition, currentNodeID string) ([]string, error) {
	nodes := make(map[string]AutomationNode, len(definition.Nodes))
	incoming := make(map[string][]string, len(definition.Nodes))
	outgoing := make(map[string][]string, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodes[node.ID] = node
	}
	if _, exists := nodes[currentNodeID]; !exists {
		return nil, fmt.Errorf("流程定义中找不到节点 %s", currentNodeID)
	}
	for _, edge := range definition.Edges {
		if _, sourceExists := nodes[edge.Source]; !sourceExists {
			continue
		}
		if _, targetExists := nodes[edge.Target]; !targetExists {
			continue
		}
		incoming[edge.Target] = append(incoming[edge.Target], edge.Source)
		outgoing[edge.Source] = append(outgoing[edge.Source], edge.Target)
	}
	ancestors := map[string]struct{}{}
	queue := append([]string(nil), incoming[currentNodeID]...)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, exists := ancestors[current]; exists {
			continue
		}
		ancestors[current] = struct{}{}
		queue = append(queue, incoming[current]...)
	}
	indegree := make(map[string]int, len(ancestors))
	for nodeID := range ancestors {
		for _, predecessorID := range incoming[nodeID] {
			if _, exists := ancestors[predecessorID]; exists {
				indegree[nodeID]++
			}
		}
	}
	ready := make([]string, 0)
	for nodeID := range ancestors {
		if indegree[nodeID] == 0 {
			ready = append(ready, nodeID)
		}
	}
	sort.Strings(ready)
	ordered := make([]string, 0, len(ancestors))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		ordered = append(ordered, current)
		for _, targetID := range outgoing[current] {
			if _, exists := ancestors[targetID]; !exists {
				continue
			}
			indegree[targetID]--
			if indegree[targetID] == 0 {
				ready = append(ready, targetID)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(ancestors) {
		return nil, errors.New("流程祖先节点包含循环")
	}
	return ordered, nil
}

func resolveAutomationVariableValue(runContext map[string]any, raw any) (any, string, []string, error) {
	text, isText := raw.(string)
	if !isText {
		return raw, "literal", nil, nil
	}
	if strings.Contains(text, "{{") {
		result, missing, err := renderAutomationVariableTemplate(text, runContext, "error", false)
		return result, "template", missing, err
	}
	trimmed := strings.TrimSpace(text)
	if isAutomationVariableReference(trimmed) {
		value, exists := resolveAutomationReference(runContext, trimmed)
		if !exists {
			return nil, "reference", nil, fmt.Errorf("变量 %q 不存在", trimmed)
		}
		return value, "reference", nil, nil
	}
	return raw, "literal", nil, nil
}

func isAutomationVariableReference(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "$") {
		return false
	}
	path := strings.TrimPrefix(value, "$")
	for _, root := range []string{"item", "vars", "nodes", "each"} {
		if path == root || (strings.HasPrefix(path, root+".") && len(path) > len(root)+1) {
			return true
		}
	}
	return false
}

func renderAutomationVariableTemplate(template string, runContext map[string]any, missingPolicy string, trim bool) (string, []string, error) {
	matches := automationTemplatePattern.FindAllStringSubmatch(template, -1)
	if len(matches) > automationMaxTemplateReferences {
		return "", nil, fmt.Errorf("模板变量不能超过 %d 个", automationMaxTemplateReferences)
	}
	missingReferences := make([]string, 0)
	missingSet := map[string]struct{}{}
	result := automationTemplatePattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := automationTemplatePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		reference := strings.TrimSpace(parts[1])
		value, exists := resolveAutomationReference(runContext, reference)
		if !exists {
			if _, seen := missingSet[reference]; !seen {
				missingSet[reference] = struct{}{}
				missingReferences = append(missingReferences, reference)
			}
			return ""
		}
		return automationVariableString(value)
	})
	if missingPolicy == "error" && len(missingReferences) > 0 {
		return "", missingReferences, fmt.Errorf("模板变量 %s 不存在", strings.Join(missingReferences, ", "))
	}
	if trim {
		result = strings.TrimSpace(result)
	}
	return result, missingReferences, nil
}

func automationVariableString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case nil:
		return ""
	default:
		if encoded, err := automationJSONText(typed); err == nil {
			return encoded
		}
		return fmt.Sprint(typed)
	}
}

func automationJSONText(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

func convertAutomationVariableValue(value any, valueType string) (any, error) {
	valueType = strings.ToLower(strings.TrimSpace(valueType))
	if valueType == "" || valueType == "auto" {
		return value, nil
	}
	switch valueType {
	case "string":
		return automationVariableString(value), nil
	case "integer":
		return automationSafeInteger(value)
	case "number":
		return automationFiniteNumber(value)
	case "boolean":
		if boolean, ok := value.(bool); ok {
			return boolean, nil
		}
		parsed, err := strconv.ParseBool(strings.TrimSpace(automationVariableString(value)))
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效布尔值", automationVariableString(value))
		}
		return parsed, nil
	case "datetime":
		if parsed, ok := value.(time.Time); ok {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
		parsed := parseAutomationTime(automationVariableString(value))
		if parsed == nil {
			return nil, fmt.Errorf("%q 不是支持的日期时间", automationVariableString(value))
		}
		return parsed.UTC().Format(time.RFC3339Nano), nil
	case "json":
		if text, ok := value.(string); ok {
			if len(text) > automationMaxVariableJSONBytes {
				return nil, fmt.Errorf("JSON 输入不能超过 %d 字节", automationMaxVariableJSONBytes)
			}
			var decoded any
			decoder := json.NewDecoder(bytes.NewBufferString(text))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err != nil {
				return nil, fmt.Errorf("JSON 输入无效: %w", err)
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				return nil, errors.New("JSON 输入只能包含一个完整值")
			}
			return decoded, nil
		}
		return value, nil
	default:
		return nil, fmt.Errorf("不支持的变量类型 %q", valueType)
	}
}

func automationFiniteNumber(value any) (float64, error) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case uint:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, fmt.Errorf("%q 不是有效数字", typed.String())
		}
		number = parsed
	case string:
		trimmed := strings.TrimSpace(typed)
		if !automationDecimalNumberPattern.MatchString(trimmed) {
			return 0, fmt.Errorf("%q 不是有效数字", typed)
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, fmt.Errorf("%q 不是有效数字", typed)
		}
		number = parsed
	default:
		return 0, fmt.Errorf("%v 不是有效数字", value)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, errors.New("数字不能是 NaN 或 Infinity")
	}
	return number, nil
}

func automationSafeInteger(value any) (int64, error) {
	number, err := automationFiniteNumber(value)
	if err != nil {
		return 0, err
	}
	if math.Trunc(number) != number {
		return 0, fmt.Errorf("%v 不是整数", value)
	}
	if number < -float64(automationJSONSafeInteger) || number > float64(automationJSONSafeInteger) {
		return 0, fmt.Errorf("整数必须在 %d 到 %d 之间", -automationJSONSafeInteger, automationJSONSafeInteger)
	}
	return int64(number), nil
}

func automationVariableValueType(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "integer"
	case float32, float64:
		return "number"
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "json"
	}
}

func automationVariableFailure(output map[string]any, err error) map[string]any {
	if output == nil {
		output = map[string]any{}
	}
	output["selected_port"] = "failure"
	output["written"] = false
	output["variables"] = map[string]any{}
	if err != nil {
		output["reason"] = err.Error()
	}
	return output
}

func validateAutomationJSONSafe(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	return validateAutomationJSONSafeValue(value, 0)
}

func validateAutomationJSONSafeValue(value any, depth int) error {
	if depth > automationMaxJSONNestingDepth {
		return fmt.Errorf("变量 JSON 嵌套不能超过 %d 层", automationMaxJSONNestingDepth)
	}
	switch typed := value.(type) {
	case nil, string, bool:
		return nil
	case json.Number:
		number, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("变量数字 %q 无效", typed.String())
		}
		if math.Abs(number) > float64(automationJSONSafeInteger) {
			return fmt.Errorf("变量数字必须在 %d 到 %d 之间", -automationJSONSafeInteger, automationJSONSafeInteger)
		}
		return nil
	case []any:
		for _, item := range typed {
			if err := validateAutomationJSONSafeValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, item := range typed {
			if err := validateAutomationJSONSafeValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("变量包含不支持的 JSON 类型 %T", value)
	}
}

func automationVariableWrite(output map[string]any, runContext map[string]any, config map[string]any, value any) map[string]any {
	if output == nil {
		output = map[string]any{}
	}
	name, err := automationVariableName(config)
	if err != nil {
		return automationVariableFailure(output, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return automationVariableFailure(output, fmt.Errorf("变量 %s 不是可持久化的 JSON 值: %w", name, err))
	}
	if err := validateAutomationJSONSafe(encoded); err != nil {
		return automationVariableFailure(output, err)
	}
	if len(encoded) > automationMaxVariableJSONBytes {
		return automationVariableFailure(output, fmt.Errorf("变量 %s 的 JSON 不能超过 %d 字节", name, automationMaxVariableJSONBytes))
	}
	variables, _ := runContext["vars"].(map[string]any)
	_, exists := variables[name]
	policy := automationVariableOverwritePolicy(config)
	output["variable"] = name
	output["value"] = value
	output["value_type"] = automationVariableValueType(value)
	output["existing"] = exists
	output["overwritten"] = false
	output["written"] = false
	output["variables"] = map[string]any{}
	switch policy {
	case "keep":
		if exists {
			output["selected_port"] = "success"
			return output
		}
	case "error":
		if exists {
			return automationVariableFailure(output, fmt.Errorf("变量 %s 已存在", name))
		}
	case "overwrite":
	default:
		return automationVariableFailure(output, errors.New("变量覆盖策略必须是 overwrite/keep/error"))
	}
	output["selected_port"] = "success"
	output["written"] = true
	output["overwritten"] = exists
	output["variables"] = map[string]any{name: value}
	return output
}

func executeAutomationSetVariableNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	value, sourceKind, missing, err := resolveAutomationVariableValue(runContext, node.Config["value"])
	output := map[string]any{"source_kind": sourceKind, "missing_references": missing}
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	valueType := strings.ToLower(automationConfigString(node.Config, "value_type"))
	if valueType == "" {
		valueType = "auto"
	}
	value, err = convertAutomationVariableValue(value, valueType)
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	return automationVariableWrite(output, runContext, node.Config, value), nil
}

func executeAutomationTemplateNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	missingPolicy := strings.ToLower(automationConfigString(node.Config, "missing"))
	if missingPolicy == "" {
		missingPolicy = "error"
	}
	trim, _ := node.Config["trim"].(bool)
	result, missing, err := renderAutomationVariableTemplate(automationConfigRawString(node.Config, "template"), runContext, missingPolicy, trim)
	output := map[string]any{"result": result, "missing_references": missing}
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	return automationVariableWrite(output, runContext, node.Config, result), nil
}

func executeAutomationJSONExtractNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	input, _, _, err := resolveAutomationVariableValue(runContext, node.Config["input"])
	pointer := automationConfigRawString(node.Config, "pointer")
	output := map[string]any{"found": false, "pointer": pointer}
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	if text, ok := input.(string); ok {
		input, err = convertAutomationVariableValue(text, "json")
		if err != nil {
			return automationVariableFailure(output, err), nil
		}
	}
	segments, err := decodeAutomationJSONPointer(pointer)
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	result, found := extractAutomationJSONPointer(input, segments)
	output["found"] = found
	if !found {
		missing := strings.ToLower(automationConfigString(node.Config, "missing"))
		if missing == "" {
			missing = "failure"
		}
		if missing != "default" {
			return automationVariableFailure(output, fmt.Errorf("JSON Pointer %q 没有找到值", pointer)), nil
		}
		result, _, _, err = resolveAutomationVariableValue(runContext, node.Config["default_value"])
		if err != nil {
			return automationVariableFailure(output, err), nil
		}
	}
	valueType := strings.ToLower(automationConfigString(node.Config, "value_type"))
	if valueType == "" {
		valueType = "auto"
	}
	result, err = convertAutomationVariableValue(result, valueType)
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	output["result"] = result
	return automationVariableWrite(output, runContext, node.Config, result), nil
}

func decodeAutomationJSONPointer(pointer string) ([]string, error) {
	if len(pointer) > automationMaxJSONPointerLen {
		return nil, fmt.Errorf("JSON Pointer 不能超过 %d 字节", automationMaxJSONPointerLen)
	}
	if pointer == "" {
		return []string{}, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("JSON Pointer 必须为空或以 / 开头")
	}
	rawSegments := strings.Split(pointer[1:], "/")
	if len(rawSegments) > automationMaxJSONPointerSegments {
		return nil, fmt.Errorf("JSON Pointer 不能超过 %d 段", automationMaxJSONPointerSegments)
	}
	segments := make([]string, 0, len(rawSegments))
	for _, raw := range rawSegments {
		var builder strings.Builder
		for index := 0; index < len(raw); index++ {
			if raw[index] != '~' {
				builder.WriteByte(raw[index])
				continue
			}
			if index+1 >= len(raw) || (raw[index+1] != '0' && raw[index+1] != '1') {
				return nil, fmt.Errorf("JSON Pointer %q 包含无效转义", pointer)
			}
			index++
			if raw[index] == '0' {
				builder.WriteByte('~')
			} else {
				builder.WriteByte('/')
			}
		}
		segments = append(segments, builder.String())
	}
	return segments, nil
}

func extractAutomationJSONPointer(input any, segments []string) (any, bool) {
	current := input
	for _, segment := range segments {
		switch typed := current.(type) {
		case map[string]any:
			value, exists := typed[segment]
			if !exists {
				return nil, false
			}
			current = value
		case []any:
			if segment == "" || segment == "-" || (len(segment) > 1 && segment[0] == '0') {
				return nil, false
			}
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func executeAutomationMathNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	leftValue, _, _, err := resolveAutomationVariableValue(runContext, node.Config["left"])
	operation := strings.ToLower(automationConfigString(node.Config, "operation"))
	output := map[string]any{"operation": operation}
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	left, err := automationFiniteNumber(leftValue)
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	result := left
	if operation == "add" || operation == "subtract" || operation == "multiply" || operation == "divide" || operation == "mod" || operation == "min" || operation == "max" {
		rightValue, _, _, resolveErr := resolveAutomationVariableValue(runContext, node.Config["right"])
		if resolveErr != nil {
			return automationVariableFailure(output, resolveErr), nil
		}
		right, numberErr := automationFiniteNumber(rightValue)
		if numberErr != nil {
			return automationVariableFailure(output, numberErr), nil
		}
		switch operation {
		case "add":
			result = left + right
		case "subtract":
			result = left - right
		case "multiply":
			result = left * right
		case "divide":
			if right == 0 {
				return automationVariableFailure(output, errors.New("除数不能为 0")), nil
			}
			result = left / right
		case "mod":
			if right == 0 {
				return automationVariableFailure(output, errors.New("模运算除数不能为 0")), nil
			}
			result = math.Mod(left, right)
		case "min":
			result = math.Min(left, right)
		case "max":
			result = math.Max(left, right)
		}
	} else {
		switch operation {
		case "abs":
			result = math.Abs(left)
		case "round":
		case "floor":
			result = math.Floor(left)
		case "ceil":
			result = math.Ceil(left)
		default:
			return automationVariableFailure(output, fmt.Errorf("不支持的数学操作 %q", operation)), nil
		}
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return automationVariableFailure(output, errors.New("数学结果不能是 NaN 或 Infinity")), nil
	}
	precision, precisionSet, precisionErr := automationConfigOptionalInt(node.Config, "precision")
	if precisionErr != nil {
		return automationVariableFailure(output, precisionErr), nil
	}
	resultType := strings.ToLower(automationConfigString(node.Config, "result_type"))
	if resultType == "" {
		resultType = "number"
	}
	if operation == "round" || (resultType == "number" && precisionSet) {
		factor := math.Pow10(precision)
		result = math.Round(result*factor) / factor
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return automationVariableFailure(output, errors.New("数学舍入结果超出范围")), nil
		}
	}
	var storedResult any = result
	if resultType == "integer" {
		storedResult, err = automationSafeInteger(result)
		if err != nil {
			return automationVariableFailure(output, err), nil
		}
	}
	output["result"] = storedResult
	return automationVariableWrite(output, runContext, node.Config, storedResult), nil
}
