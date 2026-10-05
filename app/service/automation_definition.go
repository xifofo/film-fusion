package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const AutomationSchemaVersion = 1

const (
	AutomationNodeTrigger              = "trigger"
	AutomationNodeDelay                = "delay"
	AutomationNodeRegex                = "regex"
	AutomationNodeExtractBetween       = "extract_between"
	AutomationNodeKeyword              = "keyword"
	AutomationNodeKeywordReplace       = "keyword_replace"
	AutomationNodeRegexReplace         = "regex_replace"
	AutomationNodeConvert              = "convert"
	AutomationNodeSetVariable          = "set_variable"
	AutomationNodeTemplate             = "template"
	AutomationNodeJSONExtract          = "json_extract"
	AutomationNodeMath                 = "math"
	AutomationNodeDatetimeOperation    = "datetime_operation"
	AutomationNodeListOperation        = "list_operation"
	AutomationNodeSwitch               = "switch"
	AutomationNodeCoalesce             = "coalesce"
	AutomationNodeDeduplicate          = "deduplicate"
	AutomationNodeRateLimit            = "rate_limit"
	AutomationNodeForeach              = "foreach"
	AutomationNodeIf                   = "if"
	AutomationNodeParallel             = "parallel"
	AutomationNodeJoin                 = "join"
	AutomationNodeQBittorrent          = "qbittorrent"
	AutomationNodeWaitQBittorrent      = "wait_qbittorrent"
	AutomationNodeStopQBittorrent      = "stop_qbittorrent"
	AutomationNodeDeleteQBittorrent    = "delete_qbittorrent"
	AutomationNodeOffline115           = "offline115"
	AutomationNodeOffline115OpenAPI    = "offline115_openapi"
	AutomationNodeTorrentToMagnet      = "torrent_to_magnet"
	AutomationNodeTorrent115OpenAPI    = "torrent115_openapi"
	AutomationNodeOffline115BTOpenAPI  = "offline115_bt_openapi"
	AutomationNodeList115TasksOpenAPI  = "list115_tasks_openapi"
	AutomationNodeQuota115OpenAPI      = "quota115_openapi"
	AutomationNodeDelete115TaskOpenAPI = "delete115_task_openapi"
	AutomationNodeClear115TasksOpenAPI = "clear115_tasks_openapi"
	AutomationNodeWait115              = "wait115"
	AutomationNodeRename115OpenAPI     = "rename115_openapi"
	AutomationNodeMoviePilotTitle      = "moviepilot_title_recognize"
	AutomationNodeFilmFusionRecognize  = "filmfusion_recognize"
	AutomationNodeMoviePilotTransfer   = "moviepilot_transfer"
	AutomationNodeMediaExists          = "media_exists"
	AutomationNodeMoviePilotRecognize  = "moviepilot_recognize"
	AutomationNodeOrganizeStrm         = "organize_strm"
	AutomationNodeStrmVerify           = "strm_verify"
	AutomationNodeStrmRegenerate       = "strm_regenerate"
	AutomationNodeEmbyRefreshWait      = "emby_refresh_wait"
	AutomationNodeHTTPRequest          = "http_request"
	AutomationNodeNotification         = "notification"
	AutomationNodeEnd                  = "end"
)

type AutomationPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type AutomationViewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

type AutomationNode struct {
	ID                string                 `json:"id"`
	Type              string                 `json:"type"`
	Name              string                 `json:"name,omitempty"`
	Position          AutomationPosition     `json:"position"`
	Config            map[string]any         `json:"config,omitempty"`
	ParameterBindings map[string]string      `json:"parameter_bindings,omitempty"`
	MaxAttempts       int                    `json:"max_attempts,omitempty"`
	UI                map[string]interface{} `json:"ui,omitempty"`
}

type AutomationEdge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"source_port"`
	Target     string `json:"target"`
	TargetPort string `json:"target_port,omitempty"`
}

type AutomationDefinition struct {
	SchemaVersion int                        `json:"schema_version"`
	Nodes         []AutomationNode           `json:"nodes"`
	Edges         []AutomationEdge           `json:"edges"`
	Viewport      AutomationViewport         `json:"viewport,omitempty"`
	Metadata      map[string]json.RawMessage `json:"metadata,omitempty"`
	Parameters    map[string]any             `json:"parameters,omitempty"`
}

func DefaultAutomationDefinition() AutomationDefinition {
	return AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger, Name: "115 目录事件", Position: AutomationPosition{X: 80, Y: 180}},
			{ID: "end", Type: AutomationNodeEnd, Name: "结束", Position: AutomationPosition{X: 380, Y: 180}},
		},
		Edges:    []AutomationEdge{{ID: "trigger-end", Source: "trigger", SourcePort: "next", Target: "end"}},
		Viewport: AutomationViewport{Zoom: 1},
	}
}

func DefaultRSSAutomationDefinition() AutomationDefinition {
	definition := DefaultAutomationDefinition()
	definition.Nodes[0].Name = "收到 RSS 条目"
	return definition
}

func ParseAutomationDefinition(raw string) (AutomationDefinition, error) {
	var definition AutomationDefinition
	if strings.TrimSpace(raw) == "" {
		return definition, errors.New("流程定义不能为空")
	}
	if err := json.Unmarshal([]byte(raw), &definition); err != nil {
		return definition, fmt.Errorf("流程定义 JSON 无效: %w", err)
	}
	if definition.SchemaVersion == 0 {
		definition.SchemaVersion = AutomationSchemaVersion
	}
	return definition, nil
}

func MarshalAutomationDefinition(definition AutomationDefinition) (string, error) {
	definition.SchemaVersion = AutomationSchemaVersion
	for index := range definition.Nodes {
		definition.Nodes[index].ID = strings.TrimSpace(definition.Nodes[index].ID)
		definition.Nodes[index].Type = strings.TrimSpace(strings.ToLower(definition.Nodes[index].Type))
		definition.Nodes[index].Name = strings.TrimSpace(definition.Nodes[index].Name)
		if definition.Nodes[index].Config == nil {
			definition.Nodes[index].Config = map[string]any{}
		}
	}
	for index := range definition.Edges {
		definition.Edges[index].ID = strings.TrimSpace(definition.Edges[index].ID)
		definition.Edges[index].Source = strings.TrimSpace(definition.Edges[index].Source)
		definition.Edges[index].SourcePort = strings.TrimSpace(strings.ToLower(definition.Edges[index].SourcePort))
		definition.Edges[index].Target = strings.TrimSpace(definition.Edges[index].Target)
		definition.Edges[index].TargetPort = strings.TrimSpace(strings.ToLower(definition.Edges[index].TargetPort))
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return "", fmt.Errorf("序列化流程定义失败: %w", err)
	}
	return string(encoded), nil
}
