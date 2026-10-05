package service

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

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
