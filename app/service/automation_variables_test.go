package service

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestAutomationVariableNameValidation(t *testing.T) {
	for _, name := range []string{"title", "媒体标题_2", " _trimmed ", "_私有"} {
		config := map[string]any{"variable": name}
		if _, err := automationVariableName(config); err != nil {
			t.Errorf("valid variable %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", "1title", "media.title", "two words", "__proto__", "Prototype", "constructor"} {
		if err := validateAutomationVariableName(name); err == nil {
			t.Errorf("invalid variable %q accepted", name)
		}
	}
}

func TestAutomationVariableNodesAreNonRetryableAndExposeStandardPorts(t *testing.T) {
	for _, nodeType := range []string{
		AutomationNodeExtractBetween, AutomationNodeSetVariable, AutomationNodeTemplate, AutomationNodeJSONExtract, AutomationNodeMath,
	} {
		if !isAutomationNonRetryableNode(nodeType) || automationNodeMaxAttempts(AutomationNode{Type: nodeType}) != 1 {
			t.Errorf("%s should be non-retryable", nodeType)
		}
		for _, port := range []string{"success", "failure", "always"} {
			if !isAutomationSourcePortValid(nodeType, port) {
				t.Errorf("%s missing %s port", nodeType, port)
			}
		}
	}
}

func TestExecuteAutomationExtractBetweenConvertsAndWritesValues(t *testing.T) {
	runContext := map[string]any{
		"item": map[string]any{"title": "动画[ 42 ]集"},
		"vars": map[string]any{},
	}
	node := AutomationNode{Type: AutomationNodeExtractBetween, Config: map[string]any{
		"input": "$item.title", "start_marker": "[", "end_marker": "]",
		"value_type": "integer", "variable": "episode", "overwrite": "overwrite",
	}}
	output, err := (&AutomationService{}).executeAutomationNode(
		context.Background(), model.AutomationRun{}, model.AutomationNodeRun{}, node, AutomationDefinition{}, runContext,
	)
	if err != nil || output["selected_port"] != "success" || output["found"] != true {
		t.Fatalf("extract between output = %#v, err = %v", output, err)
	}
	if output["raw_result"] != " 42 " || output["result"] != int64(42) {
		t.Fatalf("extract between values = %#v", output)
	}
	if output["variables"].(map[string]any)["episode"] != int64(42) {
		t.Fatalf("extract between variable = %#v", output["variables"])
	}

	node.Config = map[string]any{
		"input": `before[ ["first", 2] ]after`, "start_marker": "[ ", "end_marker": " ]",
		"value_type": "array", "variable": "items", "overwrite": "overwrite",
	}
	arrayOutput, _ := executeAutomationExtractBetweenNode(node, runContext)
	if arrayOutput["selected_port"] != "success" || !reflect.DeepEqual(
		arrayOutput["result"], []any{"first", json.Number("2")},
	) {
		t.Fatalf("extract between array output = %#v", arrayOutput)
	}

	node.Config = map[string]any{
		"input": `before<{"enabled":true}>after`, "start_marker": "<", "end_marker": ">",
		"value_type": "json", "variable": "metadata", "overwrite": "overwrite",
	}
	jsonOutput, _ := executeAutomationExtractBetweenNode(node, runContext)
	metadata, ok := jsonOutput["result"].(map[string]any)
	if jsonOutput["selected_port"] != "success" || !ok || metadata["enabled"] != true {
		t.Fatalf("extract between JSON output = %#v", jsonOutput)
	}
}

func TestExecuteAutomationExtractBetweenReportsLocatorAndConversionFailures(t *testing.T) {
	runContext := map[string]any{"vars": map[string]any{}}
	node := AutomationNode{Type: AutomationNodeExtractBetween, Config: map[string]any{
		"input": "prefix[value", "start_marker": "[", "end_marker": "]",
		"value_type": "string", "variable": "result",
	}}
	missingEnd, _ := executeAutomationExtractBetweenNode(node, runContext)
	if missingEnd["selected_port"] != "failure" || missingEnd["found"] != false ||
		!strings.Contains(missingEnd["reason"].(string), "后定位词") {
		t.Fatalf("missing end output = %#v", missingEnd)
	}

	node.Config["input"] = "prefix[not-a-number]suffix"
	node.Config["value_type"] = "integer"
	invalidType, _ := executeAutomationExtractBetweenNode(node, runContext)
	if invalidType["selected_port"] != "failure" || invalidType["found"] != true ||
		invalidType["raw_result"] != "not-a-number" || len(invalidType["variables"].(map[string]any)) != 0 {
		t.Fatalf("invalid conversion output = %#v", invalidType)
	}
}

func TestValidateAutomationExtractBetweenConfig(t *testing.T) {
	valid := AutomationNode{Type: AutomationNodeExtractBetween, Config: map[string]any{
		"input": "$item.title", "start_marker": "[", "end_marker": "]",
		"value_type": "array", "variable": "items", "overwrite": "overwrite",
	}}
	if err := validateAutomationNodeConfig(valid); err != nil {
		t.Fatalf("valid extract between config rejected: %v", err)
	}
	for _, port := range []string{"success", "failure", "always"} {
		if !isAutomationSourcePortValid(AutomationNodeExtractBetween, port) {
			t.Errorf("extract between missing %s port", port)
		}
	}

	for _, config := range []map[string]any{
		{"input": "$item.title", "start_marker": "", "end_marker": "]", "value_type": "string", "variable": "result"},
		{"input": "$item.title", "start_marker": "[", "end_marker": "]", "value_type": "object", "variable": "result"},
	} {
		if err := validateAutomationNodeConfig(AutomationNode{Type: AutomationNodeExtractBetween, Config: config}); err == nil {
			t.Errorf("invalid extract between config accepted: %#v", config)
		}
	}
}

func TestExecuteAutomationSetVariablePreservesReferenceTypeAndOverwritePolicies(t *testing.T) {
	runContext := map[string]any{
		"item": map[string]any{
			"metadata": map[string]any{"title": "Example", "count": json.Number("2")},
			"enabled":  false, "zero": 0, "nothing": nil,
		},
		"vars": map[string]any{"existing": "old"},
	}
	node := AutomationNode{Type: AutomationNodeSetVariable, Config: map[string]any{
		"variable": " metadata ", "value": "$item.metadata", "value_type": "auto", "overwrite": "overwrite",
	}}
	output, err := executeAutomationSetVariableNode(node, runContext)
	if err != nil || output["selected_port"] != "success" || output["written"] != true {
		t.Fatalf("set reference output = %#v, err = %v", output, err)
	}
	metadata, ok := output["value"].(map[string]any)
	if !ok || metadata["title"] != "Example" {
		t.Fatalf("reference type was not preserved: %#v", output["value"])
	}
	if _, ok := output["variables"].(map[string]any)["metadata"].(map[string]any); !ok {
		t.Fatalf("variables did not receive object value: %#v", output["variables"])
	}
	node.Config = map[string]any{
		"variable": "nested_title", "value": "$item.items.0.title", "value_type": "string", "overwrite": "overwrite",
	}
	runContext["item"].(map[string]any)["items"] = []any{map[string]any{"title": "Nested"}}
	nested, _ := executeAutomationSetVariableNode(node, runContext)
	if nested["selected_port"] != "success" || nested["value"] != "Nested" {
		t.Fatalf("array reference output = %#v", nested)
	}

	node.Config = map[string]any{
		"variable": "price", "value": "$5", "value_type": "auto", "overwrite": "overwrite",
	}
	literalDollar, _ := executeAutomationSetVariableNode(node, runContext)
	if literalDollar["selected_port"] != "success" || literalDollar["value"] != "$5" || literalDollar["source_kind"] != "literal" {
		t.Fatalf("literal dollar output = %#v", literalDollar)
	}
	for _, test := range []struct {
		name string
		want any
	}{
		{name: "enabled", want: false},
		{name: "zero", want: 0},
		{name: "nothing", want: nil},
	} {
		node.Config = map[string]any{"variable": test.name, "value": "$item." + test.name, "value_type": "auto"}
		preserved, _ := executeAutomationSetVariableNode(node, runContext)
		if preserved["selected_port"] != "success" || !reflect.DeepEqual(preserved["value"], test.want) {
			t.Errorf("%s was not preserved: %#v", test.name, preserved)
		}
	}

	node.Config = map[string]any{
		"variable": "existing", "value": "new", "value_type": "string", "overwrite": "keep",
	}
	kept, _ := executeAutomationSetVariableNode(node, runContext)
	if kept["selected_port"] != "success" || kept["written"] != false || len(kept["variables"].(map[string]any)) != 0 {
		t.Fatalf("keep output = %#v", kept)
	}

	node.Config["overwrite"] = "error"
	conflict, _ := executeAutomationSetVariableNode(node, runContext)
	if conflict["selected_port"] != "failure" || conflict["written"] != false || !strings.Contains(conflict["reason"].(string), "已存在") {
		t.Fatalf("error overwrite output = %#v", conflict)
	}

	node.Config["overwrite"] = "overwrite"
	overwritten, _ := executeAutomationSetVariableNode(node, runContext)
	if overwritten["selected_port"] != "success" || overwritten["written"] != true || overwritten["existing"] != true || overwritten["overwritten"] != true {
		t.Fatalf("overwrite output = %#v", overwritten)
	}
}

func TestExecuteAutomationSetVariableRejectsUnsafeIntegerAndOversizedValue(t *testing.T) {
	node := AutomationNode{Type: AutomationNodeSetVariable, Config: map[string]any{
		"variable": "large", "value": "9007199254740992", "value_type": "integer",
	}}
	output, _ := executeAutomationSetVariableNode(node, map[string]any{"vars": map[string]any{}})
	if output["selected_port"] != "failure" || !strings.Contains(output["reason"].(string), "9007199254740991") {
		t.Fatalf("unsafe integer output = %#v", output)
	}

	node.Config = map[string]any{
		"variable": "large", "value": strings.Repeat("x", automationMaxVariableJSONBytes+1), "value_type": "string",
	}
	output, _ = executeAutomationSetVariableNode(node, map[string]any{"vars": map[string]any{}})
	if output["selected_port"] != "failure" || !strings.Contains(output["reason"].(string), "JSON") {
		t.Fatalf("oversized variable output = %#v", output)
	}

	node.Config = map[string]any{
		"variable": "nested", "value": map[string]any{"unsafe": json.Number("9007199254740992")}, "value_type": "auto",
	}
	output, _ = executeAutomationSetVariableNode(node, map[string]any{"vars": map[string]any{}})
	if output["selected_port"] != "failure" || !strings.Contains(output["reason"].(string), "9007199254740991") {
		t.Fatalf("nested unsafe number output = %#v", output)
	}
}

func TestExecuteAutomationTemplateMissingPoliciesAndJSONRendering(t *testing.T) {
	runContext := map[string]any{
		"item": map[string]any{
			"title": "Example", "metadata": map[string]any{"html": "<tag>", "count": json.Number("2")}, "empty": nil,
		},
		"vars": map[string]any{},
	}
	node := AutomationNode{Type: AutomationNodeTemplate, Config: map[string]any{
		"template": "  {{item.title}} {{item.metadata}} {{item.empty}} {{vars.missing}}  ",
		"variable": "rendered", "missing": "empty", "trim": true,
	}}
	output, err := executeAutomationTemplateNode(node, runContext)
	if err != nil || output["selected_port"] != "success" {
		t.Fatalf("template empty output = %#v, err = %v", output, err)
	}
	if got := output["result"]; got != `Example {"count":2,"html":"<tag>"}` {
		t.Fatalf("template result = %#v", got)
	}
	missing := output["missing_references"].([]string)
	if len(missing) != 1 || missing[0] != "vars.missing" {
		t.Fatalf("missing references = %#v", missing)
	}

	node.Config["missing"] = "error"
	failed, _ := executeAutomationTemplateNode(node, runContext)
	if failed["selected_port"] != "failure" || len(failed["missing_references"].([]string)) != 1 || len(failed["variables"].(map[string]any)) != 0 {
		t.Fatalf("template error output = %#v", failed)
	}
}

func TestExecuteAutomationJSONExtractPointerAndDefault(t *testing.T) {
	runContext := map[string]any{"vars": map[string]any{}}
	node := AutomationNode{Type: AutomationNodeJSONExtract, Config: map[string]any{
		"input":   `{"data":{"items":[{"title":"Example"}],"a/b":{"~key":7}}}`,
		"pointer": "/data/items/0/title", "variable": "title", "missing": "failure", "value_type": "string",
	}}
	output, err := executeAutomationJSONExtractNode(node, runContext)
	if err != nil || output["selected_port"] != "success" || output["found"] != true || output["result"] != "Example" {
		t.Fatalf("json extract output = %#v, err = %v", output, err)
	}

	node.Config["pointer"] = "/data/a~1b/~0key"
	node.Config["variable"] = "number"
	node.Config["value_type"] = "integer"
	escaped, _ := executeAutomationJSONExtractNode(node, runContext)
	if escaped["selected_port"] != "success" || escaped["result"] != int64(7) {
		t.Fatalf("escaped pointer output = %#v", escaped)
	}

	node.Config["pointer"] = "/data/items/01/title"
	node.Config["missing"] = "default"
	node.Config["default_value"] = "$vars.fallback"
	runContext["vars"] = map[string]any{"fallback": "Fallback"}
	node.Config["value_type"] = "string"
	defaulted, _ := executeAutomationJSONExtractNode(node, runContext)
	if defaulted["selected_port"] != "success" || defaulted["found"] != false || defaulted["result"] != "Fallback" {
		t.Fatalf("json default output = %#v", defaulted)
	}

	node.Config["missing"] = "failure"
	missing, _ := executeAutomationJSONExtractNode(node, runContext)
	if missing["selected_port"] != "failure" || len(missing["variables"].(map[string]any)) != 0 {
		t.Fatalf("json missing output = %#v", missing)
	}

	node.Config = map[string]any{
		"input": map[string]any{"present_null": nil}, "pointer": "/present_null",
		"variable": "present_null", "missing": "default", "default_value": "wrong", "value_type": "auto",
	}
	nullValue, _ := executeAutomationJSONExtractNode(node, runContext)
	if nullValue["selected_port"] != "success" || nullValue["found"] != true || nullValue["result"] != nil {
		t.Fatalf("present null should not use default: %#v", nullValue)
	}
}

func TestExecuteAutomationMathOperationsAndFailures(t *testing.T) {
	runContext := map[string]any{"vars": map[string]any{"left": -1.25}}
	node := AutomationNode{Type: AutomationNodeMath, Config: map[string]any{
		"operation": "round", "left": "$vars.left", "precision": 1,
		"result_type": "number", "variable": "rounded",
	}}
	output, err := executeAutomationMathNode(node, runContext)
	if err != nil || output["selected_port"] != "success" || math.Abs(output["result"].(float64)-(-1.3)) > 1e-9 {
		t.Fatalf("math round output = %#v, err = %v", output, err)
	}

	node.Config = map[string]any{
		"operation": "round", "left": -1.5, "precision": 0,
		"result_type": "integer", "variable": "rounded_integer",
	}
	roundedInteger, _ := executeAutomationMathNode(node, runContext)
	if roundedInteger["selected_port"] != "success" || roundedInteger["result"] != int64(-2) {
		t.Fatalf("integer round output = %#v", roundedInteger)
	}

	node.Config = map[string]any{
		"operation": "add", "left": 1.2, "right": 0,
		"result_type": "number", "variable": "unrounded",
	}
	unrounded, _ := executeAutomationMathNode(node, runContext)
	if unrounded["selected_port"] != "success" || math.Abs(unrounded["result"].(float64)-1.2) > 1e-9 {
		t.Fatalf("unconfigured precision changed result: %#v", unrounded)
	}

	node.Config["left"] = "0x10"
	nonDecimal, _ := executeAutomationMathNode(node, runContext)
	if nonDecimal["selected_port"] != "failure" || !strings.Contains(nonDecimal["reason"].(string), "有效数字") {
		t.Fatalf("non-decimal number output = %#v", nonDecimal)
	}

	node.Config = map[string]any{
		"operation": "divide", "left": 5, "right": 0,
		"result_type": "number", "variable": "quotient",
	}
	zero, _ := executeAutomationMathNode(node, runContext)
	if zero["selected_port"] != "failure" || !strings.Contains(zero["reason"].(string), "不能为 0") {
		t.Fatalf("divide by zero output = %#v", zero)
	}

	node.Config["right"] = 2
	node.Config["result_type"] = "integer"
	fraction, _ := executeAutomationMathNode(node, runContext)
	if fraction["selected_port"] != "failure" || !strings.Contains(fraction["reason"].(string), "不是整数") {
		t.Fatalf("fractional integer output = %#v", fraction)
	}

	node.Config = map[string]any{
		"operation": "add", "left": float64(automationJSONSafeInteger), "right": 1,
		"result_type": "integer", "variable": "overflow",
	}
	overflow, _ := executeAutomationMathNode(node, runContext)
	if overflow["selected_port"] != "failure" || !strings.Contains(overflow["reason"].(string), "9007199254740991") {
		t.Fatalf("unsafe integer math output = %#v", overflow)
	}

	for _, test := range []struct {
		name      string
		config    map[string]any
		wantValue float64
	}{
		{name: "decimal precision", config: map[string]any{"operation": "add", "left": 0.1, "right": 0.2, "precision": 2, "result_type": "number", "variable": "decimal"}, wantValue: 0.3},
		{name: "round 2.675", config: map[string]any{"operation": "round", "left": 2.675, "precision": 2, "result_type": "number", "variable": "rounded"}, wantValue: 2.68},
		{name: "floor negative", config: map[string]any{"operation": "floor", "left": -1.2, "result_type": "number", "variable": "floor"}, wantValue: -2},
		{name: "ceil negative", config: map[string]any{"operation": "ceil", "left": -1.2, "result_type": "number", "variable": "ceil"}, wantValue: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calculated, _ := executeAutomationMathNode(AutomationNode{Type: AutomationNodeMath, Config: test.config}, runContext)
			if calculated["selected_port"] != "success" || math.Abs(calculated["result"].(float64)-test.wantValue) > 1e-9 {
				t.Fatalf("math parity output = %#v", calculated)
			}
		})
	}
}

func TestBuildAutomationRunContextUsesAncestorTopologyInsteadOfNodeRunID(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{db: db}
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "end", Type: AutomationNodeEnd},
			{ID: "second", Type: AutomationNodeSetVariable, Config: map[string]any{"variable": "shared", "value": 2}},
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "first", Type: AutomationNodeSetVariable, Config: map[string]any{"variable": "shared", "value": 1}},
		},
		Edges: []AutomationEdge{
			{Source: "trigger", SourcePort: "next", Target: "first"},
			{Source: "first", SourcePort: "success", Target: "second"},
			{Source: "second", SourcePort: "success", Target: "end"},
		},
	}
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("sequential overwrite definition invalid: %#v", result.Errors)
	}
	definitionJSON, _ := MarshalAutomationDefinition(definition)
	run := model.AutomationRun{ID: 7001, DefinitionJSON: definitionJSON, ContextJSON: `{"item":{},"vars":{},"nodes":{}}`}
	secondOutput, _ := json.Marshal(map[string]any{"variables": map[string]any{"shared": 2}})
	firstOutput, _ := json.Marshal(map[string]any{"variables": map[string]any{"shared": 1}})
	for _, nodeRun := range []model.AutomationNodeRun{
		{RunID: run.ID, NodeID: "second", NodeType: AutomationNodeSetVariable, Status: model.AutomationNodeSucceeded, OutputJSON: string(secondOutput)},
		{RunID: run.ID, NodeID: "first", NodeType: AutomationNodeSetVariable, Status: model.AutomationNodeSucceeded, OutputJSON: string(firstOutput)},
		{RunID: run.ID, NodeID: "trigger", NodeType: AutomationNodeTrigger, Status: model.AutomationNodeSucceeded, OutputJSON: `{"selected_port":"next"}`},
	} {
		if err := db.Create(&nodeRun).Error; err != nil {
			t.Fatal(err)
		}
	}
	contextValue, err := service.buildAutomationRunContext(run, "end")
	if err != nil {
		t.Fatal(err)
	}
	if got := contextValue["vars"].(map[string]any)["shared"]; got != json.Number("2") {
		t.Fatalf("topological shared value = %#v", got)
	}
}

func TestBuildAutomationRunContextIsolatesSiblingAndFailedVariables(t *testing.T) {
	db := newAutomationTestDB(t)
	service := &AutomationService{db: db}
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "parallel", Type: AutomationNodeParallel},
			{ID: "writer_a", Type: AutomationNodeSetVariable},
			{ID: "writer_b", Type: AutomationNodeSetVariable},
			{ID: "end_a", Type: AutomationNodeEnd},
			{ID: "end_b", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{Source: "trigger", SourcePort: "next", Target: "parallel"},
			{Source: "parallel", SourcePort: "branch-a", Target: "writer_a"},
			{Source: "parallel", SourcePort: "branch-b", Target: "writer_b"},
			{Source: "writer_a", SourcePort: "always", Target: "end_a"},
			{Source: "writer_b", SourcePort: "success", Target: "end_b"},
		},
	}
	definitionJSON, _ := MarshalAutomationDefinition(definition)
	run := model.AutomationRun{ID: 7002, DefinitionJSON: definitionJSON, ContextJSON: `{"item":{},"vars":{},"nodes":{}}`}
	for _, nodeRun := range []model.AutomationNodeRun{
		{RunID: run.ID, NodeID: "trigger", NodeType: AutomationNodeTrigger, Status: model.AutomationNodeSucceeded},
		{RunID: run.ID, NodeID: "parallel", NodeType: AutomationNodeParallel, Status: model.AutomationNodeSucceeded},
		{RunID: run.ID, NodeID: "writer_a", NodeType: AutomationNodeSetVariable, Status: model.AutomationNodeFailed, OutputJSON: `{"variables":{"failed":"no"}}`},
		{RunID: run.ID, NodeID: "writer_b", NodeType: AutomationNodeSetVariable, Status: model.AutomationNodeSucceeded, OutputJSON: `{"variables":{"sibling":"hidden"}}`},
	} {
		if err := db.Create(&nodeRun).Error; err != nil {
			t.Fatal(err)
		}
	}
	contextValue, err := service.buildAutomationRunContext(run, "end_a")
	if err != nil {
		t.Fatal(err)
	}
	variables := contextValue["vars"].(map[string]any)
	if _, exists := variables["failed"]; exists {
		t.Fatalf("failed ancestor variable leaked: %#v", variables)
	}
	if _, exists := variables["sibling"]; exists {
		t.Fatalf("sibling variable leaked: %#v", variables)
	}
	nodes := contextValue["nodes"].(map[string]any)
	if _, exists := nodes["writer_a"]; !exists {
		t.Fatalf("failed ancestor should remain visible in nodes: %#v", nodes)
	}
	if _, exists := nodes["writer_b"]; exists {
		t.Fatalf("sibling node leaked into context: %#v", nodes)
	}
}

func TestValidateAutomationDefinitionRejectsParallelVariableConflict(t *testing.T) {
	definition := AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger},
			{ID: "parallel", Type: AutomationNodeParallel, Config: map[string]any{"branches": []any{"branch-a", "branch-b"}}},
			{ID: "writer_a", Type: AutomationNodeSetVariable, Config: map[string]any{"variable": "shared", "value": 1}},
			{ID: "writer_b", Type: AutomationNodeSetVariable, Config: map[string]any{"variable": "shared", "value": 2}},
			{ID: "join", Type: AutomationNodeJoin, Config: map[string]any{"policy": "all_completed"}},
			{ID: "end", Type: AutomationNodeEnd},
		},
		Edges: []AutomationEdge{
			{Source: "trigger", SourcePort: "next", Target: "parallel"},
			{Source: "parallel", SourcePort: "branch-a", Target: "writer_a"},
			{Source: "parallel", SourcePort: "branch-b", Target: "writer_b"},
			{Source: "writer_a", SourcePort: "success", Target: "join"},
			{Source: "writer_b", SourcePort: "success", Target: "join"},
			{Source: "join", SourcePort: "success", Target: "end"},
		},
	}
	result := ValidateAutomationDefinition(definition)
	if result.Valid || !containsAutomationValidationError(result.Errors, "互不可达") {
		t.Fatalf("parallel variable conflict not rejected: %#v", result.Errors)
	}

	definition.Nodes[3].Config["variable"] = "other"
	if result := ValidateAutomationDefinition(definition); !result.Valid {
		t.Fatalf("distinct parallel variables rejected: %#v", result.Errors)
	}
}
