package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestRSSAutomationNodeProtocolsCoverEverySupportedNode(t *testing.T) {
	expected := []string{
		RSSAutomationNodeTrigger,
		RSSAutomationNodeRegex,
		RSSAutomationNodeKeyword,
		RSSAutomationNodeConvert,
		RSSAutomationNodeIf,
		RSSAutomationNodeParallel,
		RSSAutomationNodeJoin,
		RSSAutomationNodeQBittorrent,
		RSSAutomationNodeWaitQBittorrent,
		RSSAutomationNodeOffline115,
		RSSAutomationNodeOffline115OpenAPI,
		RSSAutomationNodeWait115,
		RSSAutomationNodeMoviePilotTitle,
		RSSAutomationNodeMediaExists,
		RSSAutomationNodeHDHiveQuery,
		RSSAutomationNodeHDHiveUnlock,
		RSSAutomationNodeMoviePilotRecognize,
		RSSAutomationNodeOrganizeStrm,
		RSSAutomationNodeStrmVerify,
		RSSAutomationNodeStrmRegenerate,
		RSSAutomationNodeEmbyRefreshWait,
		RSSAutomationNodeHTTPRequest,
		RSSAutomationNodeNotification,
		RSSAutomationNodeEnd,
	}
	protocols := RSSAutomationNodeProtocols()
	if len(protocols) != len(expected) {
		t.Fatalf("protocol count = %d, want %d", len(protocols), len(expected))
	}

	seen := make(map[string]struct{}, len(protocols))
	for _, protocol := range protocols {
		if strings.TrimSpace(protocol.Type) == "" || strings.TrimSpace(protocol.Label) == "" {
			t.Fatalf("node protocol must declare type and Chinese label: %#v", protocol)
		}
		if _, exists := seen[protocol.Type]; exists {
			t.Fatalf("duplicate node protocol %q", protocol.Type)
		}
		seen[protocol.Type] = struct{}{}
		assertRSSAutomationVariableProtocolFields(t, protocol.Type+" input", protocol.Inputs)
		assertRSSAutomationVariableProtocolFields(t, protocol.Type+" output", protocol.Outputs)
	}
	for _, nodeType := range expected {
		if _, exists := seen[nodeType]; !exists {
			t.Errorf("node %q has no variable protocol", nodeType)
		}
		if !isRSSAutomationNodeType(nodeType) {
			t.Errorf("node %q is not supported through the protocol registry", nodeType)
		}
	}
}

func assertRSSAutomationVariableProtocolFields(t *testing.T, scope string, fields []RSSAutomationVariableProtocol) {
	t.Helper()
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.Name) == "" || strings.TrimSpace(field.Type) == "" ||
			strings.TrimSpace(field.Label) == "" || strings.TrimSpace(field.Description) == "" {
			t.Fatalf("%s variable must declare name, type, Chinese label and description: %#v", scope, field)
		}
		if field.Example == nil || (reflect.ValueOf(field.Example).Kind() == reflect.String && strings.TrimSpace(field.Example.(string)) == "") {
			t.Fatalf("%s variable %q must declare an example value", scope, field.Name)
		}
		if _, exists := seen[field.Name]; exists {
			t.Fatalf("%s declares duplicate variable %q", scope, field.Name)
		}
		seen[field.Name] = struct{}{}
	}
}
