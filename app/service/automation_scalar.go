package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func convertAutomationScalar(value, valueType string) (any, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(valueType)) {
	case "", "string":
		return value, nil
	case "integer":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效整数", value)
		}
		return parsed, nil
	case "number":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效数字", value)
		}
		return parsed, nil
	case "boolean":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效布尔值", value)
		}
		return parsed, nil
	case "datetime":
		parsed := parseAutomationTime(value)
		if parsed == nil {
			return nil, fmt.Errorf("%q 不是支持的日期时间", value)
		}
		return parsed.UTC().Format(time.RFC3339Nano), nil
	default:
		return nil, fmt.Errorf("不支持的类型 %q", valueType)
	}
}

func parseAutomationTime(value string) *time.Time {
	for _, layout := range []string{
		time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
		time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02",
	} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return &parsed
		}
	}
	return nil
}

func automationContentKey(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err == nil && strings.EqualFold(parsed.Scheme, "magnet") {
		for _, xt := range parsed.Query()["xt"] {
			const prefix = "urn:btih:"
			if strings.HasPrefix(strings.ToLower(xt), prefix) {
				return "btih:" + strings.ToLower(strings.TrimSpace(xt[len(prefix):]))
			}
		}
	}
	sum := sha256.Sum256([]byte(rawURL))
	return "url:" + hex.EncodeToString(sum[:])
}

// previewAutomationActionNode remains a pure helper for editor previews. It
// never calls an external provider.
func previewAutomationActionNode(node AutomationNode, runContext map[string]any) (map[string]any, error) {
	switch node.Type {
	case AutomationNodeQBittorrent:
		downloadURL, err := resolveAutomationString(runContext, automationConfigString(node.Config, "url"))
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		if err := validateAutomationDownloadURL(downloadURL); err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		contentKey := automationContentKey(downloadURL)
		return map[string]any{
			"selected_port": "success", "content_key": contentKey,
			"torrent_tag": "filmfusion-automation-" + contentKey,
			"submitted":   true, "accepted": true, "pending": false,
			"success_count": 1, "pending_count": 0, "failure_count": 0,
			"response_format": "preview", "preview": true,
		}, nil
	case AutomationNodeOffline115, AutomationNodeOffline115OpenAPI:
		downloadURL, err := resolveAutomationString(runContext, automationConfigString(node.Config, "url"))
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		if err := validateAutomationDownloadURL(downloadURL); err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		return map[string]any{"selected_port": "success", "content_key": automationContentKey(downloadURL), "preview": true}, nil
	case AutomationNodeRename115OpenAPI:
		fileID, err := resolveAutomationString(runContext, automationConfigString(node.Config, "file_id"))
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		newName, err := resolveAutomationString(runContext, automationConfigString(node.Config, "new_name"))
		if err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		if err := validateAutomation115RenameName(newName); err != nil {
			return map[string]any{"selected_port": "failure"}, err
		}
		return map[string]any{
			"selected_port": "success", "preview": true, "renamed": true,
			"file_id": fileID, "file_name": newName, "new_name": newName, "access_method": "openapi",
		}, nil
	default:
		return nil, fmt.Errorf("预演不支持节点类型 %q", node.Type)
	}
}
