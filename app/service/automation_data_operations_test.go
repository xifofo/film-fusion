package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func automationDataTestContext() map[string]any {
	return map[string]any{
		"item":  map[string]any{"title": "Example", "published_at": "2026-08-30T12:00:00Z"},
		"vars":  map[string]any{},
		"nodes": map[string]any{},
	}
}

func TestAutomationDataNodesAreRegisteredNonRetryableAndExposePorts(t *testing.T) {
	ports := map[string][]string{
		AutomationNodeDatetimeOperation: {"success", "failure", "always"},
		AutomationNodeListOperation:     {"success", "empty", "failure", "always"},
		AutomationNodeSwitch:            {"case-uhd", "default", "failure", "always"},
		AutomationNodeCoalesce:          {"success", "failure", "always"},
		AutomationNodeForeach:           {"success", "partial", "empty", "failure", "always"},
	}
	for nodeType, expectedPorts := range ports {
		if !isAutomationNodeType(nodeType) {
			t.Errorf("%s is not registered", nodeType)
		}
		if !isAutomationNonRetryableNode(nodeType) || automationNodeMaxAttempts(AutomationNode{Type: nodeType}) != 1 {
			t.Errorf("%s should be non-retryable", nodeType)
		}
		for _, port := range expectedPorts {
			if !isAutomationSourcePortValid(nodeType, port) {
				t.Errorf("%s missing %s port", nodeType, port)
			}
		}
	}
	if isAutomationSourcePortValid(AutomationNodeSwitch, "case-") || isAutomationSourcePortValid(AutomationNodeForeach, "matched") {
		t.Fatal("data node accepted an invalid dynamic port")
	}
}

func TestExecuteAutomationDatetimeOperationFormatDiffAndStartOf(t *testing.T) {
	contextValue := automationDataTestContext()
	formatNode := AutomationNode{Type: AutomationNodeDatetimeOperation, Config: map[string]any{
		"operation": "format", "input": "$item.published_at", "input_format": "auto",
		"output_format": "rfc3339", "timezone": "Asia/Shanghai", "variable": "localized", "overwrite": "overwrite",
	}}
	formatted, _ := executeAutomationDatetimeOperationNode(formatNode, contextValue)
	if formatted["selected_port"] != "success" || formatted["result"] != "2026-08-30T20:00:00+08:00" {
		t.Fatalf("format output = %#v", formatted)
	}

	diffNode := AutomationNode{Type: AutomationNodeDatetimeOperation, Config: map[string]any{
		"operation": "diff", "input": "2026-08-30T12:01:30Z", "right": "2026-08-30T12:00:00Z",
		"input_format": "rfc3339", "timezone": "UTC", "unit": "second", "precision": 0,
		"variable": "elapsed", "overwrite": "overwrite",
	}}
	difference, _ := executeAutomationDatetimeOperationNode(diffNode, contextValue)
	if difference["selected_port"] != "success" || difference["result"] != float64(90) {
		t.Fatalf("diff output = %#v", difference)
	}

	startNode := AutomationNode{Type: AutomationNodeDatetimeOperation, Config: map[string]any{
		"operation": "start_of", "input": "2026-08-30 20:01:02", "input_format": "datetime",
		"output_format": "datetime", "timezone": "Asia/Shanghai", "unit": "week",
		"variable": "week_start", "overwrite": "overwrite",
	}}
	started, _ := executeAutomationDatetimeOperationNode(startNode, contextValue)
	if started["selected_port"] != "success" || started["result"] != "2026-08-24 00:00:00" {
		t.Fatalf("start_of output = %#v", started)
	}
}

func TestExecuteAutomationDatetimeOperationAutoUnixAndFailures(t *testing.T) {
	contextValue := automationDataTestContext()
	secondsNode := AutomationNode{Type: AutomationNodeDatetimeOperation, Config: map[string]any{
		"operation": "parse", "input": "1725019200", "input_format": "auto", "output_format": "unix_seconds",
		"timezone": "UTC", "variable": "timestamp", "overwrite": "overwrite",
	}}
	seconds, _ := executeAutomationDatetimeOperationNode(secondsNode, contextValue)
	if seconds["selected_port"] != "success" || seconds["result"] != int64(1725019200) {
		t.Fatalf("unix seconds output = %#v", seconds)
	}

	badZone := secondsNode
	badZone.Config = cloneAutomationConfig(secondsNode.Config)
	badZone.Config["timezone"] = "Mars/Olympus"
	failed, _ := executeAutomationDatetimeOperationNode(badZone, contextValue)
	if failed["selected_port"] != "failure" || len(failed["variables"].(map[string]any)) != 0 {
		t.Fatalf("invalid timezone output = %#v", failed)
	}

	if err := validateAutomationDatetimeOperationConfig(map[string]any{
		"operation": "start_of", "input": "2026-08-30", "unit": "hour", "variable": "x",
	}); err == nil {
		t.Fatal("start_of accepted unsupported hour unit")
	}

	secondsNode.Config["input"] = "2026-08-30T20:00:00"
	secondsNode.Config["input_format"] = "datetime"
	secondsNode.Config["output_format"] = "datetime"
	secondsNode.Config["timezone"] = "Asia/Shanghai"
	localWithT, _ := executeAutomationDatetimeOperationNode(secondsNode, contextValue)
	if localWithT["selected_port"] != "success" || localWithT["result"] != "2026-08-30 20:00:00" {
		t.Fatalf("datetime T local input output = %#v", localWithT)
	}
}

func TestExecuteAutomationDatetimeOperationUsesCalendarDSTMonthAndYearSemantics(t *testing.T) {
	contextValue := automationDataTestContext()
	tests := []struct {
		name   string
		input  string
		amount int
		unit   string
		zone   string
		want   string
	}{
		{name: "dst day keeps wall clock", input: "2026-03-07 12:00:00", amount: 1, unit: "day", zone: "America/New_York", want: "2026-03-08T12:00:00-04:00"},
		{name: "month clamps leap day", input: "2024-01-31 12:00:00", amount: 1, unit: "month", zone: "Asia/Shanghai", want: "2024-02-29T12:00:00+08:00"},
		{name: "year clamps leap day", input: "2024-02-29 12:00:00", amount: 1, unit: "year", zone: "Asia/Shanghai", want: "2025-02-28T12:00:00+08:00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := AutomationNode{Type: AutomationNodeDatetimeOperation, Config: map[string]any{
				"operation": "add", "input": test.input, "input_format": "datetime", "output_format": "rfc3339",
				"timezone": test.zone, "amount": test.amount, "unit": test.unit, "variable": "added", "overwrite": "overwrite",
			}}
			output, _ := executeAutomationDatetimeOperationNode(node, contextValue)
			if output["selected_port"] != "success" || output["result"] != test.want {
				t.Fatalf("datetime add output = %#v, want %s", output, test.want)
			}
		})
	}
}

func TestExecuteAutomationDatetimeOperationRejectsNonexistentDSTWallClock(t *testing.T) {
	contextValue := automationDataTestContext()
	node := AutomationNode{Type: AutomationNodeDatetimeOperation, Config: map[string]any{
		"operation": "parse", "input": "2026-03-08 02:30:00", "input_format": "datetime",
		"output_format": "rfc3339", "timezone": "America/New_York", "variable": "parsed", "overwrite": "overwrite",
	}}
	output, _ := executeAutomationDatetimeOperationNode(node, contextValue)
	if output["selected_port"] != "failure" || !strings.Contains(output["reason"].(string), "不存在") || len(output["variables"].(map[string]any)) != 0 {
		t.Fatalf("nonexistent DST input output = %#v", output)
	}

	node.Config["operation"] = "add"
	node.Config["input"] = "2026-03-07 02:30:00"
	node.Config["amount"] = 1
	node.Config["unit"] = "day"
	added, _ := executeAutomationDatetimeOperationNode(node, contextValue)
	if added["selected_port"] != "failure" || !strings.Contains(added["reason"].(string), "不存在") {
		t.Fatalf("nonexistent DST add output = %#v", added)
	}
}

func TestExecuteAutomationListOperations(t *testing.T) {
	contextValue := automationDataTestContext()
	tests := []struct {
		name       string
		config     map[string]any
		wantResult any
		wantPort   string
	}{
		{name: "split", config: map[string]any{"operation": "split", "input": " a, ,b,a ", "separator": ",", "trim_items": true, "omit_empty": true}, wantResult: []any{"a", "b", "a"}, wantPort: "success"},
		{name: "join", config: map[string]any{"operation": "join", "input": []any{"a", "", "b"}, "separator": "-", "omit_empty": true}, wantResult: "a-b", wantPort: "success"},
		{name: "reverse", config: map[string]any{"operation": "reverse", "input": []any{1, 2, 3}}, wantResult: []any{3, 2, 1}, wantPort: "success"},
		{name: "slice", config: map[string]any{"operation": "slice", "input": []any{1, 2, 3, 4}, "offset": -2, "limit": 1}, wantResult: []any{3}, wantPort: "success"},
		{name: "length empty", config: map[string]any{"operation": "length", "input": []any{}}, wantResult: int64(0), wantPort: "empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.config["variable"] = "list_result"
			test.config["overwrite"] = "overwrite"
			output, _ := executeAutomationListOperationNode(AutomationNode{Type: AutomationNodeListOperation, Config: test.config}, contextValue)
			if output["selected_port"] != test.wantPort || !reflect.DeepEqual(output["result"], test.wantResult) {
				t.Fatalf("list output = %#v, want result %#v port %s", output, test.wantResult, test.wantPort)
			}
		})
	}
}

func TestExecuteAutomationListPointerMissingAndSortNullLast(t *testing.T) {
	contextValue := automationDataTestContext()
	items := []any{
		map[string]any{"id": "a", "score": 2},
		map[string]any{"id": "missing"},
		map[string]any{"id": "b", "score": 1},
		map[string]any{"id": "duplicate", "score": 2},
	}
	uniqueNode := AutomationNode{Type: AutomationNodeListOperation, Config: map[string]any{
		"operation": "unique", "input": items, "pointer": "/score", "missing": "null",
		"variable": "unique_scores", "overwrite": "overwrite",
	}}
	unique, _ := executeAutomationListOperationNode(uniqueNode, contextValue)
	wantUnique := []any{items[0], items[1], items[2]}
	if unique["selected_port"] != "success" || !reflect.DeepEqual(unique["result"], wantUnique) {
		t.Fatalf("unique output = %#v", unique)
	}

	sortNode := AutomationNode{Type: AutomationNodeListOperation, Config: map[string]any{
		"operation": "sort", "input": items, "pointer": "/score", "missing": "null", "direction": "desc", "compare_as": "number",
		"variable": "sorted_scores", "overwrite": "overwrite",
	}}
	sorted, _ := executeAutomationListOperationNode(sortNode, contextValue)
	wantSorted := []any{items[0], items[3], items[2], items[1]}
	if sorted["selected_port"] != "success" || !reflect.DeepEqual(sorted["result"], wantSorted) {
		t.Fatalf("sort output = %#v", sorted)
	}

	pluckNode := AutomationNode{Type: AutomationNodeListOperation, Config: map[string]any{
		"operation": "pluck", "input": items, "pointer": "/score", "missing": "skip",
		"variable": "scores", "overwrite": "overwrite",
	}}
	plucked, _ := executeAutomationListOperationNode(pluckNode, contextValue)
	if !reflect.DeepEqual(plucked["result"], []any{2, 1, 2}) {
		t.Fatalf("pluck output = %#v", plucked)
	}

	pluckNode.Config["missing"] = "failure"
	failed, _ := executeAutomationListOperationNode(pluckNode, contextValue)
	if failed["selected_port"] != "failure" || failed["written"] != false {
		t.Fatalf("pluck failure output = %#v", failed)
	}
}

func TestExecuteAutomationListEnforcesRawSplitLimitAndNormalizesJSONNumbers(t *testing.T) {
	contextValue := automationDataTestContext()
	tooMany := AutomationNode{Type: AutomationNodeListOperation, Config: map[string]any{
		"operation": "split", "input": strings.Repeat(",", automationMaxListItems), "separator": ",",
		"omit_empty": true, "variable": "parts", "overwrite": "overwrite",
	}}
	failed, _ := executeAutomationListOperationNode(tooMany, contextValue)
	if failed["selected_port"] != "failure" || !strings.Contains(failed["reason"].(string), "10000") {
		t.Fatalf("split raw item limit output = %#v", failed)
	}

	unique := AutomationNode{Type: AutomationNodeListOperation, Config: map[string]any{
		"operation": "unique", "input": []any{json.Number("1"), json.Number("1.0"), "1"},
		"pointer": "", "missing": "failure", "compare_as": "auto", "variable": "unique", "overwrite": "overwrite",
	}}
	output, _ := executeAutomationListOperationNode(unique, contextValue)
	if output["selected_port"] != "success" || !reflect.DeepEqual(output["result"], []any{json.Number("1"), "1"}) {
		t.Fatalf("unique numeric normalization output = %#v", output)
	}
}

func TestExecuteAutomationSwitchUsesFirstMatchingDynamicCase(t *testing.T) {
	contextValue := automationDataTestContext()
	node := AutomationNode{Type: AutomationNodeSwitch, Config: map[string]any{
		"input": "2160P", "compare_as": "string", "case_sensitive": false,
		"cases": []any{
			map[string]any{"id": "contains", "label": "包含", "operator": "contains", "value": "2160"},
			map[string]any{"id": "exact", "label": "精确", "operator": "eq", "value": "2160p"},
		},
	}}
	output, _ := executeAutomationSwitchNode(node, contextValue)
	if output["selected_port"] != "case-contains" || output["case_index"] != 0 || output["case_id"] != "contains" {
		t.Fatalf("switch output = %#v", output)
	}

	node.Config["input"] = "1080p"
	defaulted, _ := executeAutomationSwitchNode(node, contextValue)
	if defaulted["selected_port"] != "default" || defaulted["matched"] != false || defaulted["case_index"] != -1 {
		t.Fatalf("switch default output = %#v", defaulted)
	}

	node.Config["cases"] = []any{map[string]any{"id": "bad", "operator": "regex", "value": "["}}
	failed, _ := executeAutomationSwitchNode(node, contextValue)
	if failed["selected_port"] != "failure" || !strings.Contains(failed["reason"].(string), "bad") {
		t.Fatalf("switch failure output = %#v", failed)
	}
}

func TestValidateAutomationSwitchRejectsUnknownConnectedCase(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "switch", Type: AutomationNodeSwitch, Config: map[string]any{
				"input": "$item.title", "cases": []any{map[string]any{"id": "known", "operator": "eq", "value": "x"}},
			}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{ID: "a", Source: "trigger", SourcePort: "next", Target: "switch"},
			{ID: "b", Source: "switch", SourcePort: "case-missing", Target: "end"},
		},
	}
	validation := ValidateAutomationDefinition(definition)
	if validation.Valid || !strings.Contains(strings.Join(validation.Errors, "\n"), "没有对应 case") {
		t.Fatalf("switch validation = %#v", validation)
	}
}

func TestValidateAutomationSwitchRequiresCaseLabel(t *testing.T) {
	err := validateAutomationSwitchConfig(map[string]any{
		"input": "$item.title",
		"cases": []any{map[string]any{"id": "known", "operator": "eq", "value": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "标签") {
		t.Fatalf("switch label validation error = %v", err)
	}
}

func TestExecuteAutomationCoalescePreservesFalseZeroAndDefaults(t *testing.T) {
	contextValue := automationDataTestContext()
	contextValue["vars"].(map[string]any)["empty"] = "   "
	contextValue["vars"].(map[string]any)["zero"] = 0
	node := AutomationNode{Type: AutomationNodeCoalesce, Config: map[string]any{
		"candidates": []any{"$vars.missing", "$vars.empty", "$vars.zero", false},
		"missing":    "skip", "skip_null": true, "skip_empty_string": true, "trim_strings": true,
		"on_empty": "failure", "value_type": "auto", "variable": "picked", "overwrite": "overwrite",
	}}
	output, _ := executeAutomationCoalesceNode(node, contextValue)
	if output["selected_port"] != "success" || output["result"] != 0 || output["selected_index"] != 2 || output["used_default"] != false {
		t.Fatalf("coalesce output = %#v", output)
	}

	node.Config["candidates"] = []any{"$vars.missing"}
	node.Config["on_empty"] = "default"
	node.Config["default_value"] = false
	defaulted, _ := executeAutomationCoalesceNode(node, contextValue)
	if defaulted["selected_port"] != "success" || defaulted["result"] != false || defaulted["used_default"] != true || defaulted["selected_index"] != -1 {
		t.Fatalf("coalesce default output = %#v", defaulted)
	}

	node.Config["missing"] = "failure"
	failed, _ := executeAutomationCoalesceNode(node, contextValue)
	if failed["selected_port"] != "failure" || len(failed["variables"].(map[string]any)) != 0 {
		t.Fatalf("coalesce missing output = %#v", failed)
	}
}

func TestExecuteAutomationForeachTemplateAndJSONExtract(t *testing.T) {
	contextValue := automationDataTestContext()
	templateNode := AutomationNode{Type: AutomationNodeForeach, Config: map[string]any{
		"input":     []any{"a", "b"},
		"transform": map[string]any{"type": "template", "config": map[string]any{"template": "{{each.index}}/{{each.count}}:{{each.item}}"}},
		"on_error":  "fail_fast", "max_items": 100, "variable": "mapped", "overwrite": "overwrite",
	}}
	output, _ := executeAutomationForeachNode(templateNode, contextValue)
	want := []any{"0/2:a", "1/2:b"}
	if output["selected_port"] != "success" || !reflect.DeepEqual(output["results"], want) || output["succeeded_count"] != 2 {
		t.Fatalf("foreach template output = %#v", output)
	}

	jsonNode := AutomationNode{Type: AutomationNodeForeach, Config: map[string]any{
		"input": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}},
		"transform": map[string]any{"type": "json_extract", "config": map[string]any{
			"input": "$each.item", "pointer": "/name", "missing": "failure", "value_type": "string",
		}},
		"on_error": "fail_fast", "max_items": 100, "variable": "names", "overwrite": "overwrite",
	}}
	extracted, _ := executeAutomationForeachNode(jsonNode, contextValue)
	if extracted["selected_port"] != "success" || !reflect.DeepEqual(extracted["results"], []any{"a", "b"}) {
		t.Fatalf("foreach json output = %#v", extracted)
	}
}

func TestExecuteAutomationForeachCollectPartialFailFastAndEmpty(t *testing.T) {
	contextValue := automationDataTestContext()
	baseConfig := map[string]any{
		"input": []any{2, 0, 5},
		"transform": map[string]any{"type": "math", "config": map[string]any{
			"operation": "divide", "left": 10, "right": "$each.item", "result_type": "number",
		}},
		"max_items": 100, "variable": "mapped", "overwrite": "overwrite",
	}
	collectConfig := cloneAutomationConfig(baseConfig)
	collectConfig["on_error"] = "collect"
	partial, _ := executeAutomationForeachNode(AutomationNode{Type: AutomationNodeForeach, Config: collectConfig}, contextValue)
	if partial["selected_port"] != "partial" || partial["partial"] != true || partial["succeeded_count"] != 2 || partial["failed_count"] != 1 {
		t.Fatalf("foreach partial output = %#v", partial)
	}
	if !reflect.DeepEqual(partial["results"], []any{float64(5), nil, float64(2)}) || len(partial["variables"].(map[string]any)) != 1 {
		t.Fatalf("foreach partial results = %#v", partial)
	}

	failFastConfig := cloneAutomationConfig(baseConfig)
	failFastConfig["on_error"] = "fail_fast"
	failed, _ := executeAutomationForeachNode(AutomationNode{Type: AutomationNodeForeach, Config: failFastConfig}, contextValue)
	if failed["selected_port"] != "failure" || failed["failed_count"] != 1 || len(failed["variables"].(map[string]any)) != 0 {
		t.Fatalf("foreach fail-fast output = %#v", failed)
	}

	emptyConfig := cloneAutomationConfig(baseConfig)
	emptyConfig["input"] = []any{}
	empty, _ := executeAutomationForeachNode(AutomationNode{Type: AutomationNodeForeach, Config: emptyConfig}, contextValue)
	if empty["selected_port"] != "empty" || !reflect.DeepEqual(empty["results"], []any{}) || len(empty["variables"].(map[string]any)) != 1 {
		t.Fatalf("foreach empty output = %#v", empty)
	}
}

func TestValidateAutomationForeachBoundsWhitelistAndNestedWriter(t *testing.T) {
	valid := map[string]any{
		"input": "$vars.items", "transform": map[string]any{"type": "template", "config": map[string]any{"template": "{{each.item}}"}},
		"on_error": "collect", "max_items": 1000, "variable": "mapped", "overwrite": "overwrite",
	}
	if err := validateAutomationForeachConfig(valid); err != nil {
		t.Fatalf("valid foreach rejected: %v", err)
	}

	invalid := cloneAutomationConfig(valid)
	invalid["max_items"] = 1001
	if err := validateAutomationForeachConfig(invalid); err == nil {
		t.Fatal("foreach accepted max_items above bound")
	}

	invalid = cloneAutomationConfig(valid)
	invalid["transform"] = map[string]any{"type": "http_request", "config": map[string]any{}}
	if err := validateAutomationForeachConfig(invalid); err == nil {
		t.Fatal("foreach accepted side-effect transform")
	}

	invalid = cloneAutomationConfig(valid)
	invalid["transform"] = map[string]any{"type": "template", "config": map[string]any{"template": "x", "variable": "nested"}}
	if err := validateAutomationForeachConfig(invalid); err == nil {
		t.Fatal("foreach accepted nested variable writer")
	}
}

func TestNewDataNodeValidationRequiresInputField(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "datetime", err: validateAutomationDatetimeOperationConfig(map[string]any{"variable": "result"})},
		{name: "list", err: validateAutomationListOperationConfig(map[string]any{"variable": "result"})},
		{name: "switch", err: validateAutomationSwitchConfig(map[string]any{"cases": []any{map[string]any{"id": "a", "label": "A", "operator": "eq", "value": "a"}}})},
		{name: "foreach", err: validateAutomationForeachConfig(map[string]any{
			"variable": "result", "transform": map[string]any{"type": "template", "config": map[string]any{"template": "x"}},
		})},
	}
	for _, test := range tests {
		if test.err == nil || !strings.Contains(test.err.Error(), "input") {
			t.Errorf("%s missing input error = %v", test.name, test.err)
		}
	}
}

func TestExecuteAutomationForeachRejectsOversizedInputAndItemLimit(t *testing.T) {
	contextValue := automationDataTestContext()
	node := AutomationNode{Type: AutomationNodeForeach, Config: map[string]any{
		"input": []any{1, 2}, "transform": map[string]any{"type": "template", "config": map[string]any{"template": "{{each.item}}"}},
		"max_items": 1, "variable": "mapped", "overwrite": "overwrite",
	}}
	limited, _ := executeAutomationForeachNode(node, contextValue)
	if limited["selected_port"] != "failure" || !strings.Contains(limited["reason"].(string), "max_items") {
		t.Fatalf("foreach item limit output = %#v", limited)
	}

	node.Config["max_items"] = 100
	node.Config["input"] = []any{strings.Repeat("x", automationMaxVariableJSONBytes)}
	oversized, _ := executeAutomationForeachNode(node, contextValue)
	if oversized["selected_port"] != "failure" || !strings.Contains(oversized["reason"].(string), "1") {
		t.Fatalf("foreach oversized output = %#v", oversized)
	}
}

func TestExecuteAutomationForeachEnforcesPerItemAndTotalOutputBounds(t *testing.T) {
	contextValue := automationDataTestContext()
	baseTransform := map[string]any{"type": "template", "config": map[string]any{"template": "{{each.item}}"}}
	inputTooLarge := AutomationNode{Type: AutomationNodeForeach, Config: map[string]any{
		"input": []any{strings.Repeat("x", automationMaxForeachItem)}, "transform": baseTransform,
		"on_error": "fail_fast", "max_items": 100, "variable": "mapped", "overwrite": "overwrite",
	}}
	failedInput, _ := executeAutomationForeachNode(inputTooLarge, contextValue)
	if failedInput["selected_port"] != "failure" || !strings.Contains(failedInput["reason"].(string), "65536") || len(failedInput["variables"].(map[string]any)) != 0 {
		t.Fatalf("foreach oversized item output = %#v", failedInput)
	}

	resultTooLarge := inputTooLarge
	resultTooLarge.Config = cloneAutomationConfig(inputTooLarge.Config)
	resultTooLarge.Config["input"] = []any{"small"}
	resultTooLarge.Config["transform"] = map[string]any{"type": "template", "config": map[string]any{"template": strings.Repeat("x", automationMaxForeachItem)}}
	failedResult, _ := executeAutomationForeachNode(resultTooLarge, contextValue)
	if failedResult["selected_port"] != "failure" || !strings.Contains(failedResult["reason"].(string), "65536") || len(failedResult["variables"].(map[string]any)) != 0 {
		t.Fatalf("foreach oversized result output = %#v", failedResult)
	}

	totalTooLarge := inputTooLarge
	totalTooLarge.Config = cloneAutomationConfig(inputTooLarge.Config)
	totalTooLarge.Config["input"] = make([]any, 20)
	for index := range totalTooLarge.Config["input"].([]any) {
		totalTooLarge.Config["input"].([]any)[index] = index
	}
	totalTooLarge.Config["transform"] = map[string]any{"type": "template", "config": map[string]any{"template": strings.Repeat("x", 60<<10)}}
	failedTotal, _ := executeAutomationForeachNode(totalTooLarge, contextValue)
	if failedTotal["selected_port"] != "failure" || !strings.Contains(failedTotal["reason"].(string), "1048576") || len(failedTotal["variables"].(map[string]any)) != 0 {
		t.Fatalf("foreach oversized total output = %#v", failedTotal)
	}
	if encoded, err := json.Marshal(failedTotal); err != nil || len(encoded) > automationMaxVariableJSONBytes {
		t.Fatalf("compacted failure output size = %d, err = %v", len(encoded), err)
	}
}

func TestNewDataNodeVariableProducersParticipateInConflictValidation(t *testing.T) {
	nodes := map[string]AutomationNode{
		"left":  {ID: "left", Type: AutomationNodeCoalesce, Config: map[string]any{"variable": "shared"}},
		"right": {ID: "right", Type: AutomationNodeListOperation, Config: map[string]any{"variable": "shared"}},
	}
	errorsFound := validateAutomationVariableProducerConflicts(nodes, map[string][]AutomationEdge{})
	if len(errorsFound) != 1 || !strings.Contains(errorsFound[0], "shared") {
		t.Fatalf("producer conflict errors = %#v", errorsFound)
	}
}

func TestForeachResultsRemainJSONSafe(t *testing.T) {
	contextValue := automationDataTestContext()
	node := AutomationNode{Type: AutomationNodeForeach, Config: map[string]any{
		"input": []any{json.Number("9007199254740992")},
		"transform": map[string]any{"type": "json_extract", "config": map[string]any{
			"input": "$each.item", "pointer": "", "missing": "failure", "value_type": "auto",
		}},
		"on_error": "collect", "max_items": 100, "variable": "mapped", "overwrite": "overwrite",
	}}
	output, _ := executeAutomationForeachNode(node, contextValue)
	if output["selected_port"] != "failure" || !strings.Contains(output["reason"].(string), "9007199254740991") {
		t.Fatalf("unsafe foreach output = %#v", output)
	}
}
