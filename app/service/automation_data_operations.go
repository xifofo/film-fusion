package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	automationMaxListItems    = 10000
	automationMaxSwitchCases  = 20
	automationMaxCoalesce     = 32
	automationMaxForeachItems = 1000
	automationMaxForeachItem  = 64 << 10
)

var automationSwitchCaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)

func validateAutomationDatetimeOperationConfig(config map[string]any) error {
	if err := validateAutomationVariableWriterConfig(config); err != nil {
		return err
	}
	return validateAutomationDatetimeOperationPureConfig(config)
}

func validateAutomationDatetimeOperationPureConfig(config map[string]any) error {
	if _, exists := config["input"]; !exists {
		return errors.New("日期时间节点必须配置 input")
	}
	operation := strings.ToLower(automationConfigString(config, "operation"))
	if operation == "" {
		operation = "parse"
	}
	switch operation {
	case "parse", "format", "add", "diff", "start_of":
	default:
		return errors.New("日期时间操作必须是 parse/format/add/diff/start_of")
	}
	if operation == "diff" {
		if _, exists := config["right"]; !exists {
			return errors.New("日期时间 diff 必须配置 right")
		}
		if precision, exists, err := automationConfigOptionalInt(config, "precision"); err != nil {
			return err
		} else if exists && (precision < 0 || precision > 6) {
			return errors.New("日期时间差精度必须在 0 到 6 之间")
		}
	}
	inputFormat := strings.ToLower(automationConfigString(config, "input_format"))
	if inputFormat == "" {
		inputFormat = "auto"
	}
	if !isAutomationDatetimeFormat(inputFormat) {
		return errors.New("日期时间输入格式必须是 auto/rfc3339/rfc1123/date/datetime/unix_seconds/unix_milliseconds")
	}
	outputFormat := strings.ToLower(automationConfigString(config, "output_format"))
	if outputFormat == "" {
		outputFormat = "rfc3339"
	}
	if outputFormat == "auto" || !isAutomationDatetimeFormat(outputFormat) {
		return errors.New("日期时间输出格式必须是 rfc3339/rfc1123/date/datetime/unix_seconds/unix_milliseconds")
	}
	timezone := automationConfigString(config, "timezone")
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("无效时区 %q", timezone)
	}
	unit := strings.ToLower(automationConfigString(config, "unit"))
	if unit == "" {
		unit = "second"
	}
	switch operation {
	case "add":
		if !automationDatetimeAddUnit(unit) {
			return errors.New("日期时间增加单位必须是 millisecond/second/minute/hour/day/week/month/year")
		}
	case "diff":
		if !automationDatetimeDiffUnit(unit) {
			return errors.New("日期时间差单位必须是 millisecond/second/minute/hour/day/week")
		}
	case "start_of":
		switch unit {
		case "day", "week", "month", "year":
		default:
			return errors.New("日期时间 start_of 单位必须是 day/week/month/year")
		}
	}
	return nil
}

func isAutomationDatetimeFormat(value string) bool {
	switch value {
	case "auto", "rfc3339", "rfc1123", "date", "datetime", "unix_seconds", "unix_milliseconds":
		return true
	default:
		return false
	}
}

func automationDatetimeAddUnit(unit string) bool {
	switch unit {
	case "millisecond", "second", "minute", "hour", "day", "week", "month", "year":
		return true
	default:
		return false
	}
}

func automationDatetimeDiffUnit(unit string) bool {
	switch unit {
	case "millisecond", "second", "minute", "hour", "day", "week":
		return true
	default:
		return false
	}
}

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

func isAutomationCompareAs(value string) bool {
	switch value {
	case "auto", "string", "number", "boolean", "datetime":
		return true
	default:
		return false
	}
}

func executeAutomationDatetimeOperationNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	result, output, err := executeAutomationDatetimeOperationPure(node.Config, runContext)
	if err != nil {
		return automationVariableFailure(output, err), nil
	}
	return automationVariableWrite(output, runContext, node.Config, result), nil
}

func executeAutomationDatetimeOperationPure(config map[string]any, runContext map[string]any) (any, map[string]any, error) {
	operation := strings.ToLower(automationConfigString(config, "operation"))
	if operation == "" {
		operation = "parse"
	}
	timezone := automationConfigString(config, "timezone")
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	unit := strings.ToLower(automationConfigString(config, "unit"))
	if unit == "" {
		unit = "second"
	}
	output := map[string]any{"operation": operation, "timezone": timezone, "unit": unit}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, output, fmt.Errorf("无效时区 %q", timezone)
	}
	input, _, _, err := resolveAutomationVariableValue(runContext, config["input"])
	if err != nil {
		return nil, output, err
	}
	output["input"] = input
	inputTime, err := parseAutomationDatetimeValue(input, automationConfigString(config, "input_format"), location)
	if err != nil {
		return nil, output, err
	}
	inputTime = inputTime.In(location)
	var result any
	switch operation {
	case "parse", "format":
		result, err = formatAutomationDatetimeValue(inputTime, automationConfigString(config, "output_format"), location)
	case "add":
		amountValue := config["amount"]
		if amountValue == nil {
			amountValue = 0
		}
		amountValue, _, _, err = resolveAutomationVariableValue(runContext, amountValue)
		if err != nil {
			return nil, output, err
		}
		amount, numberErr := automationFiniteNumber(amountValue)
		if numberErr != nil {
			return nil, output, numberErr
		}
		inputTime, err = addAutomationDatetime(inputTime, amount, unit)
		if err == nil {
			result, err = formatAutomationDatetimeValue(inputTime, automationConfigString(config, "output_format"), location)
		}
	case "diff":
		rightValue, _, _, resolveErr := resolveAutomationVariableValue(runContext, config["right"])
		if resolveErr != nil {
			return nil, output, resolveErr
		}
		rightTime, parseErr := parseAutomationDatetimeValue(rightValue, automationConfigString(config, "input_format"), location)
		if parseErr != nil {
			return nil, output, fmt.Errorf("right: %w", parseErr)
		}
		result, err = automationDatetimeDifference(inputTime, rightTime, unit, config)
	case "start_of":
		inputTime, err = startOfAutomationDatetime(inputTime, unit, location)
		if err == nil {
			result, err = formatAutomationDatetimeValue(inputTime, automationConfigString(config, "output_format"), location)
		}
	default:
		err = fmt.Errorf("不支持的日期时间操作 %q", operation)
	}
	if err != nil {
		return nil, output, err
	}
	output["result"] = result
	return result, output, nil
}

func parseAutomationDatetimeValue(value any, inputFormat string, location *time.Location) (time.Time, error) {
	inputFormat = strings.ToLower(strings.TrimSpace(inputFormat))
	if inputFormat == "" {
		inputFormat = "auto"
	}
	text := strings.TrimSpace(automationVariableString(value))
	if text == "" {
		return time.Time{}, errors.New("日期时间输入不能为空")
	}
	parseUnix := func(milliseconds bool) (time.Time, error) {
		number, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("%q 不是有效 Unix 时间戳", text)
		}
		if milliseconds {
			return time.UnixMilli(number), nil
		}
		return time.Unix(number, 0), nil
	}
	parseNamed := func(format string) (time.Time, error) {
		switch format {
		case "rfc3339":
			return time.Parse(time.RFC3339Nano, text)
		case "rfc1123":
			if parsed, err := time.Parse(time.RFC1123Z, text); err == nil {
				return parsed, nil
			}
			return time.Parse(time.RFC1123, text)
		case "date":
			return parseAutomationLocalDatetime("2006-01-02", text, location)
		case "datetime":
			layout := "2006-01-02 15:04:05"
			if strings.Contains(text, "T") {
				layout = "2006-01-02T15:04:05"
			}
			return parseAutomationLocalDatetime(layout, text, location)
		case "unix_seconds":
			return parseUnix(false)
		case "unix_milliseconds":
			return parseUnix(true)
		default:
			return time.Time{}, fmt.Errorf("不支持的日期时间格式 %q", format)
		}
	}
	if inputFormat != "auto" {
		parsed, err := parseNamed(inputFormat)
		if err != nil {
			return time.Time{}, fmt.Errorf("无法按 %s 解析日期时间 %q: %w", inputFormat, text, err)
		}
		return validateAutomationDatetimeRange(parsed)
	}
	for _, format := range []string{"rfc3339", "rfc1123", "date", "datetime"} {
		if parsed, err := parseNamed(format); err == nil {
			return validateAutomationDatetimeRange(parsed)
		}
	}
	digits := strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	if digits != "" {
		if _, err := strconv.ParseInt(digits, 10, 64); err == nil {
			format := "unix_milliseconds"
			if len(digits) <= 10 {
				format = "unix_seconds"
			}
			if parsed, err := parseNamed(format); err == nil {
				return validateAutomationDatetimeRange(parsed)
			}
		}
	}
	return time.Time{}, fmt.Errorf("无法自动解析日期时间 %q", text)
}

func parseAutomationLocalDatetime(layout, text string, location *time.Location) (time.Time, error) {
	parsed, err := time.ParseInLocation(layout, text, location)
	if err != nil {
		return time.Time{}, err
	}
	// time.ParseInLocation normalizes local wall-clock values that fall in a
	// daylight-saving gap. Treat that normalization as invalid input so the
	// preview and real execution do not silently move the requested time.
	if parsed.In(location).Format(layout) != text {
		return time.Time{}, errors.New("日期时间落在时区不存在的本地时间")
	}
	return parsed, nil
}

func validateAutomationDatetimeRange(value time.Time) (time.Time, error) {
	year := value.Year()
	if year < 1 || year > 9999 {
		return time.Time{}, errors.New("日期时间年份必须在 1 到 9999 之间")
	}
	return value, nil
}

func formatAutomationDatetimeValue(value time.Time, outputFormat string, location *time.Location) (any, error) {
	outputFormat = strings.ToLower(strings.TrimSpace(outputFormat))
	if outputFormat == "" {
		outputFormat = "rfc3339"
	}
	localized, err := validateAutomationDatetimeRange(value.In(location))
	if err != nil {
		return nil, err
	}
	switch outputFormat {
	case "rfc3339":
		return localized.Format(time.RFC3339Nano), nil
	case "rfc1123":
		return localized.Format(time.RFC1123), nil
	case "date":
		return localized.Format("2006-01-02"), nil
	case "datetime":
		return localized.Format("2006-01-02 15:04:05"), nil
	case "unix_seconds":
		return automationSafeInteger(localized.Unix())
	case "unix_milliseconds":
		return automationSafeInteger(localized.UnixMilli())
	default:
		return nil, fmt.Errorf("不支持的日期时间输出格式 %q", outputFormat)
	}
}

func addAutomationDatetime(value time.Time, amount float64, unit string) (time.Time, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return time.Time{}, errors.New("日期时间增加量必须是有限数字")
	}
	var result time.Time
	var err error
	switch unit {
	case "millisecond", "second", "minute", "hour":
		multiplier := float64(time.Millisecond)
		switch unit {
		case "second":
			multiplier = float64(time.Second)
		case "minute":
			multiplier = float64(time.Minute)
		case "hour":
			multiplier = float64(time.Hour)
		}
		nanoseconds := amount * multiplier
		if nanoseconds > math.MaxInt64 || nanoseconds < math.MinInt64 {
			return time.Time{}, errors.New("日期时间增加量超出范围")
		}
		result = value.Add(time.Duration(math.Round(nanoseconds)))
	case "day", "week", "month", "year":
		if math.Trunc(amount) != amount || amount > math.MaxInt32 || amount < math.MinInt32 {
			return time.Time{}, fmt.Errorf("%s 增加量必须是有效整数", unit)
		}
		count := int(amount)
		switch unit {
		case "day":
			result, err = addAutomationCalendarDays(value, count)
		case "week":
			result, err = addAutomationCalendarDays(value, count*7)
		case "month":
			result, err = addAutomationCalendarDate(value, 0, count)
		case "year":
			result, err = addAutomationCalendarDate(value, count, 0)
		}
	default:
		return time.Time{}, fmt.Errorf("不支持的日期时间单位 %q", unit)
	}
	if err != nil {
		return time.Time{}, err
	}
	return validateAutomationDatetimeRange(result)
}

func addAutomationCalendarDays(value time.Time, days int) (time.Time, error) {
	year, month, day := value.Date()
	target := time.Date(year, month, day+days, 0, 0, 0, 0, time.UTC)
	hour, minute, second := value.Clock()
	return strictAutomationLocalDatetime(
		target.Year(), target.Month(), target.Day(), hour, minute, second, value.Nanosecond(), value.Location(),
	)
}

func addAutomationCalendarDate(value time.Time, years, months int) (time.Time, error) {
	year, month, day := value.Date()
	hour, minute, second := value.Clock()
	nanosecond := value.Nanosecond()
	firstOfTarget := time.Date(year+years, month+time.Month(months), 1, hour, minute, second, nanosecond, value.Location())
	lastDay := time.Date(firstOfTarget.Year(), firstOfTarget.Month()+1, 0, hour, minute, second, nanosecond, value.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return strictAutomationLocalDatetime(
		firstOfTarget.Year(), firstOfTarget.Month(), day, hour, minute, second, nanosecond, value.Location(),
	)
}

func strictAutomationLocalDatetime(year int, month time.Month, day, hour, minute, second, nanosecond int, location *time.Location) (time.Time, error) {
	value := time.Date(year, month, day, hour, minute, second, nanosecond, location)
	localized := value.In(location)
	actualYear, actualMonth, actualDay := localized.Date()
	actualHour, actualMinute, actualSecond := localized.Clock()
	if actualYear != year || actualMonth != month || actualDay != day || actualHour != hour || actualMinute != minute || actualSecond != second || localized.Nanosecond() != nanosecond {
		return time.Time{}, errors.New("日期时间落在时区不存在的本地时间")
	}
	return value, nil
}

func automationDatetimeDifference(left, right time.Time, unit string, config map[string]any) (float64, error) {
	divisorSeconds := 1.0
	switch unit {
	case "millisecond":
		divisorSeconds = 0.001
	case "second":
	case "minute":
		divisorSeconds = 60
	case "hour":
		divisorSeconds = 60 * 60
	case "day":
		divisorSeconds = 24 * 60 * 60
	case "week":
		divisorSeconds = 7 * 24 * 60 * 60
	default:
		return 0, fmt.Errorf("不支持的日期时间差单位 %q", unit)
	}
	deltaSeconds := float64(left.Unix()-right.Unix()) + float64(left.Nanosecond()-right.Nanosecond())/float64(time.Second)
	result := deltaSeconds / divisorSeconds
	precision, exists, err := automationConfigOptionalInt(config, "precision")
	if err != nil {
		return 0, err
	}
	if !exists {
		precision = 0
	}
	factor := math.Pow10(precision)
	result = math.Round(result*factor) / factor
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, errors.New("日期时间差超出范围")
	}
	return result, nil
}

func startOfAutomationDatetime(value time.Time, unit string, location *time.Location) (time.Time, error) {
	value = value.In(location)
	year, month, day := value.Date()
	switch unit {
	case "day":
		return strictAutomationLocalDatetime(year, month, day, 0, 0, 0, 0, location)
	case "week":
		weekday := (int(value.Weekday()) + 6) % 7
		target := time.Date(year, month, day-weekday, 0, 0, 0, 0, time.UTC)
		return strictAutomationLocalDatetime(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, location)
	case "month":
		return strictAutomationLocalDatetime(year, month, 1, 0, 0, 0, 0, location)
	case "year":
		return strictAutomationLocalDatetime(year, time.January, 1, 0, 0, 0, 0, location)
	default:
		return time.Time{}, fmt.Errorf("不支持的 start_of 单位 %q", unit)
	}
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
