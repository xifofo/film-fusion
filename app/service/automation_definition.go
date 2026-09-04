package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

const AutomationSchemaVersion = 1

const (
	AutomationNodeTrigger             = "trigger"
	AutomationNodeDelay               = "delay"
	AutomationNodeRegex               = "regex"
	AutomationNodeExtractBetween      = "extract_between"
	AutomationNodeKeyword             = "keyword"
	AutomationNodeKeywordReplace      = "keyword_replace"
	AutomationNodeRegexReplace        = "regex_replace"
	AutomationNodeConvert             = "convert"
	AutomationNodeSetVariable         = "set_variable"
	AutomationNodeTemplate            = "template"
	AutomationNodeJSONExtract         = "json_extract"
	AutomationNodeMath                = "math"
	AutomationNodeDatetimeOperation   = "datetime_operation"
	AutomationNodeListOperation       = "list_operation"
	AutomationNodeSwitch              = "switch"
	AutomationNodeCoalesce            = "coalesce"
	AutomationNodeDeduplicate         = "deduplicate"
	AutomationNodeRateLimit           = "rate_limit"
	AutomationNodeForeach             = "foreach"
	AutomationNodeIf                  = "if"
	AutomationNodeParallel            = "parallel"
	AutomationNodeJoin                = "join"
	AutomationNodeQBittorrent         = "qbittorrent"
	AutomationNodeWaitQBittorrent     = "wait_qbittorrent"
	AutomationNodeDeleteQBittorrent   = "delete_qbittorrent"
	AutomationNodeOffline115          = "offline115"
	AutomationNodeOffline115OpenAPI   = "offline115_openapi"
	AutomationNodeWait115             = "wait115"
	AutomationNodeRename115OpenAPI    = "rename115_openapi"
	AutomationNodeMoviePilotTitle     = "moviepilot_title_recognize"
	AutomationNodeFilmFusionRecognize = "filmfusion_recognize"
	AutomationNodeMoviePilotTransfer  = "moviepilot_transfer"
	AutomationNodeMediaExists         = "media_exists"
	AutomationNodeMoviePilotRecognize = "moviepilot_recognize"
	AutomationNodeOrganizeStrm        = "organize_strm"
	AutomationNodeStrmVerify          = "strm_verify"
	AutomationNodeStrmRegenerate      = "strm_regenerate"
	AutomationNodeEmbyRefreshWait     = "emby_refresh_wait"
	AutomationNodeHTTPRequest         = "http_request"
	AutomationNodeNotification        = "notification"
	AutomationNodeEnd                 = "end"
)

var automationNodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

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
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Name        string                 `json:"name,omitempty"`
	Position    AutomationPosition     `json:"position"`
	Config      map[string]any         `json:"config,omitempty"`
	MaxAttempts int                    `json:"max_attempts,omitempty"`
	UI          map[string]interface{} `json:"ui,omitempty"`
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
}

type AutomationValidationResult struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
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

func ValidateAutomationDefinition(definition AutomationDefinition) AutomationValidationResult {
	result := AutomationValidationResult{Errors: []string{}, Warnings: []string{}}
	if definition.SchemaVersion != AutomationSchemaVersion {
		result.Errors = append(result.Errors, fmt.Sprintf("不支持的流程版本 %d", definition.SchemaVersion))
	}
	if len(definition.Nodes) == 0 {
		result.Errors = append(result.Errors, "流程至少需要一个节点")
		return result
	}
	if len(definition.Nodes) > 200 {
		result.Errors = append(result.Errors, "单个流程不能超过 200 个节点")
	}
	if len(definition.Edges) > 500 {
		result.Errors = append(result.Errors, "单个流程不能超过 500 条连线")
	}

	nodes := make(map[string]AutomationNode, len(definition.Nodes))
	triggerIDs := make([]string, 0, 1)
	endIDs := make([]string, 0, 1)
	for _, node := range definition.Nodes {
		if !automationNodeIDPattern.MatchString(node.ID) {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 ID %q 无效", node.ID))
			continue
		}
		if _, exists := nodes[node.ID]; exists {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 ID %q 重复", node.ID))
			continue
		}
		if !isAutomationNodeType(node.Type) {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 使用了不支持的类型 %q", node.ID, node.Type))
		}
		if len([]rune(node.Name)) > 120 {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 的名称不能超过 120 个字符", node.ID))
		}
		if node.MaxAttempts < 0 || node.MaxAttempts > 10 {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 的重试次数必须在 0 到 10 之间", node.ID))
		}
		if isAutomationNonRetryableNode(node.Type) && node.MaxAttempts > 1 {
			result.Errors = append(result.Errors, fmt.Sprintf("可能产生重复副作用的节点 %s 不能自动重试", node.ID))
		}
		if err := validateAutomationNodeConfig(node); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s: %v", node.ID, err))
		}
		nodes[node.ID] = node
		if node.Type == AutomationNodeTrigger {
			triggerIDs = append(triggerIDs, node.ID)
		}
		if node.Type == AutomationNodeEnd {
			endIDs = append(endIDs, node.ID)
		}
	}
	if len(triggerIDs) != 1 {
		result.Errors = append(result.Errors, "流程必须且只能有一个触发器")
	}
	if len(endIDs) == 0 {
		result.Errors = append(result.Errors, "流程至少需要一个结束节点")
	}

	incoming := make(map[string][]AutomationEdge, len(nodes))
	outgoing := make(map[string][]AutomationEdge, len(nodes))
	edgeIDs := make(map[string]struct{}, len(definition.Edges))
	for edgeIndex, edge := range definition.Edges {
		label := edge.ID
		if label == "" {
			label = fmt.Sprintf("#%d", edgeIndex+1)
		}
		if edge.ID != "" {
			if _, exists := edgeIDs[edge.ID]; exists {
				result.Errors = append(result.Errors, fmt.Sprintf("连线 ID %q 重复", edge.ID))
			}
			edgeIDs[edge.ID] = struct{}{}
		}
		source, sourceExists := nodes[edge.Source]
		_, targetExists := nodes[edge.Target]
		if !sourceExists {
			result.Errors = append(result.Errors, fmt.Sprintf("连线 %s 的起点 %q 不存在", label, edge.Source))
		}
		if !targetExists {
			result.Errors = append(result.Errors, fmt.Sprintf("连线 %s 的终点 %q 不存在", label, edge.Target))
		}
		if edge.Source == edge.Target && edge.Source != "" {
			result.Errors = append(result.Errors, fmt.Sprintf("连线 %s 不能连接节点自身", label))
		}
		if edge.TargetPort != "" && !strings.EqualFold(strings.TrimSpace(edge.TargetPort), "input") {
			result.Errors = append(result.Errors, fmt.Sprintf("连线 %s 的入口 %q 无效", label, edge.TargetPort))
		}
		if sourceExists && !isAutomationSourcePortValid(source.Type, edge.SourcePort) {
			result.Errors = append(result.Errors, fmt.Sprintf("连线 %s 的出口 %q 不适用于 %s 节点", label, edge.SourcePort, source.Type))
		}
		if sourceExists && targetExists {
			incoming[edge.Target] = append(incoming[edge.Target], edge)
			outgoing[edge.Source] = append(outgoing[edge.Source], edge)
		}
	}

	for id, node := range nodes {
		inCount := len(incoming[id])
		outCount := len(outgoing[id])
		portCounts := make(map[string]int)
		for _, edge := range outgoing[id] {
			portCounts[edge.SourcePort]++
		}
		for port, count := range portCounts {
			if count > 1 {
				result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 的 %s 出口连接了多个分支；请显式使用并行节点", id, port))
			}
		}
		if node.Type != AutomationNodeParallel && portCounts["always"] > 0 && outCount > 1 {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 的 always 出口不能与其他出口同时使用；请显式使用并行节点", id))
		}
		if node.Type == AutomationNodeParallel {
			configuredBranches := automationConfigStringSlice(node.Config, "branches")
			if len(configuredBranches) > 0 {
				allowed := make(map[string]struct{}, len(configuredBranches))
				for _, branch := range configuredBranches {
					allowed[branch] = struct{}{}
				}
				for port := range portCounts {
					if _, exists := allowed[port]; !exists {
						result.Errors = append(result.Errors, fmt.Sprintf("并行节点 %s 的出口 %s 不在已配置分支中", id, port))
					}
				}
				for _, branch := range configuredBranches {
					if portCounts[branch] == 0 {
						result.Errors = append(result.Errors, fmt.Sprintf("并行节点 %s 的分支 %s 尚未连接", id, branch))
					}
				}
			}
		}
		switch node.Type {
		case AutomationNodeTrigger:
			if inCount != 0 || outCount != 1 {
				result.Errors = append(result.Errors, fmt.Sprintf("触发器 %s 必须没有入口且只有一个出口", id))
			}
		case AutomationNodeEnd:
			if inCount < 1 || outCount != 0 {
				result.Errors = append(result.Errors, fmt.Sprintf("结束节点 %s 必须至少有一个入口且没有出口", id))
			}
		case AutomationNodeJoin:
			if inCount < 2 {
				result.Errors = append(result.Errors, fmt.Sprintf("汇合节点 %s 至少需要两个入口", id))
			}
			if outCount < 1 {
				result.Errors = append(result.Errors, fmt.Sprintf("汇合节点 %s 至少需要一个出口", id))
			}
			ports := edgePortSet(outgoing[id])
			if _, ok := ports["success"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("汇合节点 %s 缺少“继续”出口", id))
			}
			policy := automationConfigString(node.Config, "policy")
			if policy == "" {
				policy = "all_completed"
			}
			_, hasFailure := ports["failure"]
			if policy == "all_success" || policy == "any_success" {
				if !hasFailure {
					result.Errors = append(result.Errors, fmt.Sprintf("结果型汇合节点 %s 缺少“未满足”出口", id))
				}
			} else if hasFailure {
				result.Warnings = append(result.Warnings, fmt.Sprintf("完成型汇合节点 %s 不会使用“失败”出口", id))
			}
		case AutomationNodeParallel:
			if inCount != 1 || outCount < 2 {
				result.Errors = append(result.Errors, fmt.Sprintf("并行节点 %s 必须有一个入口和至少两个分支", id))
			}
		default:
			if inCount != 1 {
				result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 必须且只能有一个入口；多分支请先使用汇合节点", id))
			}
			if outCount == 0 {
				result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 没有后续节点", id))
			}
		}
		if inCount == 1 {
			predecessor, sourcePort := effectiveAutomationPredecessor(id, incoming, nodes)
			switch node.Type {
			case AutomationNodeWaitQBittorrent:
				if predecessor.Type != AutomationNodeQBittorrent {
					result.Errors = append(result.Errors, fmt.Sprintf("等待 qBittorrent 节点 %s 必须连接在 qBittorrent 下载节点之后，中间仅可插入延迟节点", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("等待 qBittorrent 节点 %s 必须连接 qBittorrent 节点的成功出口", id))
				}
			case AutomationNodeMoviePilotTransfer:
				if predecessor.Type != AutomationNodeWaitQBittorrent {
					result.Errors = append(result.Errors, fmt.Sprintf("MP2 整理节点 %s 必须连接在等待 qBittorrent 完成节点之后，中间仅可插入延迟节点", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("MP2 整理节点 %s 必须连接等待 qBittorrent 节点的成功出口", id))
				}
			case AutomationNodeDeleteQBittorrent:
				if predecessor.Type != AutomationNodeWaitQBittorrent && predecessor.Type != AutomationNodeMoviePilotTransfer {
					result.Errors = append(result.Errors, fmt.Sprintf("删除 qBittorrent 任务节点 %s 必须连接在等待 qBittorrent 完成或 MP2 整理节点之后", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("删除 qBittorrent 任务节点 %s 必须连接上游节点的成功出口", id))
				}
				if automationConfigBool(node.Config, "delete_files") && predecessor.Type != AutomationNodeMoviePilotTransfer {
					result.Errors = append(result.Errors, fmt.Sprintf("删除 qBittorrent 任务节点 %s 只有在 MP2 整理成功后才能同时删除下载文件", id))
				}
			case AutomationNodeWait115:
				if predecessor.Type != AutomationNodeOffline115 && predecessor.Type != AutomationNodeOffline115OpenAPI {
					result.Errors = append(result.Errors, fmt.Sprintf("等待节点 %s 必须连接在 115 离线节点之后，中间仅可插入延迟节点", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("等待节点 %s 必须连接 115 离线节点的成功出口", id))
				}
			case AutomationNodeMoviePilotRecognize:
				if predecessor.Type != AutomationNodeWait115 && predecessor.Type != AutomationNodeTrigger {
					result.Errors = append(result.Errors, fmt.Sprintf("MP 媒体识别节点 %s 必须连接在 115 目录触发器或等待 115 下载完成节点之后", id))
				}
				if (predecessor.Type == AutomationNodeTrigger && sourcePort != "next") ||
					(predecessor.Type != AutomationNodeTrigger && sourcePort != "success") {
					result.Errors = append(result.Errors, fmt.Sprintf("MP 媒体识别节点 %s 必须连接等待节点的成功出口", id))
				}
			case AutomationNodeFilmFusionRecognize:
				if automationFilmFusionRecognitionMode(node) == MediaRecognitionModeFile {
					if predecessor.Type != AutomationNodeWait115 && predecessor.Type != AutomationNodeTrigger {
						result.Errors = append(result.Errors, fmt.Sprintf("FilmFusion 本地识别节点 %s 的 115 文件模式必须连接在 115 目录触发器或等待节点之后", id))
					}
					if (predecessor.Type == AutomationNodeTrigger && sourcePort != "next") ||
						(predecessor.Type != AutomationNodeTrigger && sourcePort != "success") {
						result.Errors = append(result.Errors, fmt.Sprintf("FilmFusion 本地识别节点 %s 的 115 下载文件模式必须连接等待节点的成功出口", id))
					}
				}
			case AutomationNodeOrganizeStrm:
				validPredecessor := predecessor.Type == AutomationNodeTrigger || predecessor.Type == AutomationNodeWait115 || predecessor.Type == AutomationNodeMoviePilotRecognize
				if predecessor.Type == AutomationNodeFilmFusionRecognize {
					validPredecessor = automationFilmFusionRecognitionMode(predecessor) == MediaRecognitionModeFile
				}
				if !validPredecessor {
					result.Errors = append(result.Errors, fmt.Sprintf("整理生成 STRM 节点 %s 必须连接在 115 目录触发器、媒体识别或等待 115 下载节点之后", id))
				}
				if (predecessor.Type == AutomationNodeTrigger && sourcePort != "next") ||
					(predecessor.Type != AutomationNodeTrigger && sourcePort != "success") {
					result.Errors = append(result.Errors, fmt.Sprintf("整理生成 STRM 节点 %s 必须连接上游节点的成功出口", id))
				}
			case AutomationNodeStrmVerify:
				if predecessor.Type != AutomationNodeOrganizeStrm || sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("STRM 校验节点 %s 必须连接整理生成 STRM 节点的成功出口", id))
				}
			case AutomationNodeStrmRegenerate:
				if predecessor.Type != AutomationNodeStrmVerify || sourcePort != "invalid" {
					result.Errors = append(result.Errors, fmt.Sprintf("STRM 重生成节点 %s 必须连接 STRM 校验节点的“无效”出口", id))
				}
			}
		}
		if node.Type == AutomationNodeIf {
			ports := edgePortSet(outgoing[id])
			if _, ok := ports["true"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("IF 节点 %s 缺少 true 分支", id))
			}
			if _, ok := ports["false"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("IF 节点 %s 缺少 false 分支", id))
			}
		}
		if node.Type == AutomationNodeSwitch {
			casePorts := automationSwitchCasePortSet(node.Config)
			for _, edge := range outgoing[id] {
				port := strings.ToLower(strings.TrimSpace(edge.SourcePort))
				if !strings.HasPrefix(port, "case-") {
					continue
				}
				if _, exists := casePorts[port]; !exists {
					result.Errors = append(result.Errors, fmt.Sprintf("Switch 节点 %s 的出口 %q 没有对应 case", id, edge.SourcePort))
				}
			}
		}
		if node.Type == AutomationNodeKeyword {
			ports := edgePortSet(outgoing[id])
			if _, ok := ports["matched"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("关键词节点 %s 缺少“匹配”分支", id))
			}
			if _, ok := ports["unmatched"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("关键词节点 %s 缺少“不匹配”分支", id))
			}
		}
	}
	result.Errors = append(result.Errors, validateAutomationVariableProducerConflicts(nodes, outgoing)...)

	if len(triggerIDs) == 1 {
		if hasAutomationCycle(nodes, outgoing) {
			result.Errors = append(result.Errors, "流程不能包含循环")
		} else {
			reachable := automationReachable(triggerIDs[0], outgoing)
			for id := range nodes {
				if _, ok := reachable[id]; !ok {
					result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 无法从触发器到达", id))
				}
			}
			canReachEnd := automationCanReachAnyEnd(endIDs, incoming)
			for id := range nodes {
				if _, ok := canReachEnd[id]; !ok {
					result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 无法到达结束节点", id))
				}
			}
		}
	}

	sort.Strings(result.Errors)
	sort.Strings(result.Warnings)
	result.Valid = len(result.Errors) == 0
	return result
}

func isAutomationNodeType(nodeType string) bool {
	_, exists := automationNodeProtocolByType(nodeType)
	return exists
}

func isAutomationNonRetryableNode(nodeType string) bool {
	switch nodeType {
	case AutomationNodeDelay, AutomationNodeExtractBetween, AutomationNodeSetVariable, AutomationNodeTemplate, AutomationNodeJSONExtract, AutomationNodeMath,
		AutomationNodeDatetimeOperation, AutomationNodeListOperation, AutomationNodeSwitch, AutomationNodeCoalesce,
		AutomationNodeDeduplicate, AutomationNodeRateLimit, AutomationNodeForeach,
		AutomationNodeDeleteQBittorrent, AutomationNodeMoviePilotTransfer,
		AutomationNodeOrganizeStrm, AutomationNodeStrmRegenerate, AutomationNodeHTTPRequest:
		return true
	default:
		return false
	}
}

func isAutomationSourcePortValid(nodeType, port string) bool {
	port = strings.TrimSpace(strings.ToLower(port))
	if port == "always" {
		switch nodeType {
		case AutomationNodeRegex, AutomationNodeExtractBetween, AutomationNodeKeyword, AutomationNodeKeywordReplace, AutomationNodeRegexReplace,
			AutomationNodeConvert, AutomationNodeSetVariable, AutomationNodeTemplate, AutomationNodeJSONExtract, AutomationNodeMath,
			AutomationNodeDatetimeOperation, AutomationNodeListOperation, AutomationNodeSwitch, AutomationNodeCoalesce,
			AutomationNodeDeduplicate, AutomationNodeRateLimit, AutomationNodeForeach,
			AutomationNodeDelay, AutomationNodeJoin,
			AutomationNodeQBittorrent, AutomationNodeWaitQBittorrent, AutomationNodeDeleteQBittorrent,
			AutomationNodeOffline115, AutomationNodeOffline115OpenAPI,
			AutomationNodeWait115, AutomationNodeRename115OpenAPI, AutomationNodeMoviePilotTitle, AutomationNodeFilmFusionRecognize, AutomationNodeMoviePilotTransfer, AutomationNodeMediaExists,
			AutomationNodeMoviePilotRecognize, AutomationNodeOrganizeStrm,
			AutomationNodeStrmVerify, AutomationNodeStrmRegenerate, AutomationNodeEmbyRefreshWait,
			AutomationNodeHTTPRequest, AutomationNodeNotification:
			return true
		default:
			return false
		}
	}
	switch nodeType {
	case AutomationNodeSwitch:
		return port == "default" || port == "failure" || (strings.HasPrefix(port, "case-") && automationSwitchCaseIDPattern.MatchString(strings.TrimPrefix(port, "case-")))
	case AutomationNodeListOperation:
		return port == "success" || port == "empty" || port == "failure"
	case AutomationNodeForeach:
		return port == "success" || port == "partial" || port == "empty" || port == "failure"
	case AutomationNodeDeduplicate:
		return port == "new" || port == "duplicate" || port == "failure"
	case AutomationNodeRateLimit:
		return port == "allowed" || port == "throttled" || port == "failure"
	case AutomationNodeIf:
		return port == "true" || port == "false" || port == "failure"
	case AutomationNodeKeyword:
		return port == "matched" || port == "unmatched" || port == "failure"
	case AutomationNodeMediaExists:
		return port == "exists" || port == "missing" || port == "failure"
	case AutomationNodeStrmVerify:
		return port == "valid" || port == "invalid" || port == "failure"
	case AutomationNodeParallel:
		return strings.HasPrefix(port, "branch-") && len(port) > len("branch-")
	case AutomationNodeRegex, AutomationNodeExtractBetween, AutomationNodeKeywordReplace, AutomationNodeRegexReplace,
		AutomationNodeConvert, AutomationNodeSetVariable, AutomationNodeTemplate, AutomationNodeJSONExtract, AutomationNodeMath,
		AutomationNodeDatetimeOperation, AutomationNodeCoalesce,
		AutomationNodeDelay, AutomationNodeJoin,
		AutomationNodeQBittorrent, AutomationNodeWaitQBittorrent, AutomationNodeDeleteQBittorrent,
		AutomationNodeOffline115, AutomationNodeOffline115OpenAPI,
		AutomationNodeWait115, AutomationNodeRename115OpenAPI, AutomationNodeMoviePilotTitle, AutomationNodeFilmFusionRecognize, AutomationNodeMoviePilotTransfer,
		AutomationNodeMoviePilotRecognize, AutomationNodeOrganizeStrm, AutomationNodeStrmRegenerate, AutomationNodeEmbyRefreshWait,
		AutomationNodeHTTPRequest, AutomationNodeNotification:
		return port == "success" || port == "failure"
	case AutomationNodeTrigger:
		return port == "next"
	default:
		return false
	}
}

func validateAutomationNodeConfig(node AutomationNode) error {
	config := node.Config
	if config == nil {
		config = map[string]any{}
	}
	switch node.Type {
	case AutomationNodeDelay:
		_, err := automationDelayDuration(config)
		return err
	case AutomationNodeRegex:
		pattern := automationConfigString(config, "pattern")
		if pattern == "" {
			return errors.New("正则表达式不能为空")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("正则表达式无效: %w", err)
		}
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		if valueType := automationConfigString(config, "value_type"); valueType != "" && !isAutomationScalarValueType(valueType) {
			return errors.New("正则输出类型必须是 string/integer/number/boolean/datetime")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeExtractBetween:
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		startMarker := automationConfigRawString(config, "start_marker")
		endMarker := automationConfigRawString(config, "end_marker")
		if strings.TrimSpace(startMarker) == "" || strings.TrimSpace(endMarker) == "" {
			return errors.New("前定位词和后定位词不能为空")
		}
		if len([]rune(startMarker)) > 200 || len([]rune(endMarker)) > 200 {
			return errors.New("前定位词和后定位词不能超过 200 个字符")
		}
		valueType := strings.ToLower(automationConfigString(config, "value_type"))
		if valueType == "" {
			valueType = "string"
		}
		switch valueType {
		case "string", "integer", "number", "boolean", "json", "array":
		default:
			return errors.New("区间提取类型必须是 string/integer/number/boolean/json/array")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeKeyword:
		if automationConfigString(config, "input") == "" {
			return errors.New("必须配置输入字段")
		}
		keywords := automationConfigTextSlice(config, "keywords")
		if len(keywords) == 0 {
			return errors.New("至少需要一个关键词")
		}
		if len(keywords) > 100 {
			return errors.New("关键词不能超过 100 个")
		}
		for _, keyword := range keywords {
			if len([]rune(keyword)) > 200 {
				return errors.New("单个关键词不能超过 200 个字符")
			}
		}
		switch automationConfigString(config, "match_mode") {
		case "contains_any", "contains_all", "contains_none":
		default:
			return errors.New("关键词规则必须是 contains_any/contains_all/contains_none")
		}
		if caseSensitive, exists := config["case_sensitive"]; exists {
			if _, ok := caseSensitive.(bool); !ok {
				return errors.New("区分大小写配置必须是布尔值")
			}
		}
	case AutomationNodeKeywordReplace:
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		rules, err := automationConfigReplacementRules(config, "replacements")
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			return errors.New("至少需要一条关键词替换规则")
		}
		if len(rules) > 100 {
			return errors.New("关键词替换规则不能超过 100 条")
		}
		for _, rule := range rules {
			if len([]rune(rule.Keyword)) > 200 {
				return errors.New("单个替换关键词不能超过 200 个字符")
			}
			if len([]rune(rule.Replacement)) > 2000 {
				return errors.New("单个替换内容不能超过 2000 个字符")
			}
		}
		if caseSensitive, exists := config["case_sensitive"]; exists {
			if _, ok := caseSensitive.(bool); !ok {
				return errors.New("区分大小写配置必须是布尔值")
			}
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeRegexReplace:
		pattern := automationConfigString(config, "pattern")
		if pattern == "" {
			return errors.New("正则表达式不能为空")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("正则表达式无效: %w", err)
		}
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeConvert:
		if automationConfigString(config, "input") == "" || automationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		switch automationConfigString(config, "value_type") {
		case "string", "integer", "number", "boolean", "datetime":
		default:
			return errors.New("转换类型必须是 string/integer/number/boolean/datetime")
		}
		if err := validateAutomationVariableWriterConfig(config); err != nil {
			return err
		}
	case AutomationNodeSetVariable:
		return validateAutomationSetVariableConfig(config)
	case AutomationNodeTemplate:
		return validateAutomationTemplateConfig(config)
	case AutomationNodeJSONExtract:
		return validateAutomationJSONExtractConfig(config)
	case AutomationNodeMath:
		return validateAutomationMathConfig(config)
	case AutomationNodeDatetimeOperation:
		return validateAutomationDatetimeOperationConfig(config)
	case AutomationNodeListOperation:
		return validateAutomationListOperationConfig(config)
	case AutomationNodeSwitch:
		return validateAutomationSwitchConfig(config)
	case AutomationNodeCoalesce:
		return validateAutomationCoalesceConfig(config)
	case AutomationNodeForeach:
		return validateAutomationForeachConfig(config)
	case AutomationNodeDeduplicate:
		return validateAutomationDeduplicateConfig(config)
	case AutomationNodeRateLimit:
		return validateAutomationRateLimitConfig(config)
	case AutomationNodeIf:
		condition, ok := config["condition"]
		if !ok {
			return errors.New("必须配置判断条件")
		}
		if err := validateAutomationConditionShape(condition); err != nil {
			return err
		}
	case AutomationNodeParallel:
		branches := automationConfigStringSlice(config, "branches")
		if len(branches) > 0 {
			if len(branches) < 2 {
				return errors.New("并行节点至少需要两个分支")
			}
			seen := make(map[string]struct{}, len(branches))
			for _, branch := range branches {
				if !strings.HasPrefix(branch, "branch-") || len(branch) == len("branch-") {
					return fmt.Errorf("并行分支 %q 必须以 branch- 开头", branch)
				}
				if _, exists := seen[branch]; exists {
					return fmt.Errorf("并行分支 %q 重复", branch)
				}
				seen[branch] = struct{}{}
			}
		}
	case AutomationNodeJoin:
		switch automationConfigString(config, "policy") {
		case "", "all_completed", "all_success", "any_success", "any_completed":
		default:
			return errors.New("不支持的汇合策略")
		}
	case AutomationNodeQBittorrent:
		if automationConfigUint(config, "target_id") == 0 {
			return errors.New("必须选择 qBittorrent 目标")
		}
		if automationConfigString(config, "url") == "" {
			return errors.New("必须配置下载地址变量")
		}
		if len([]rune(automationConfigString(config, "rename"))) > 255 {
			return errors.New("qBittorrent 任务名称不能超过 255 个字符")
		}
		for _, key := range []string{"paused", "sequential", "skip_checking", "first_last_piece_priority"} {
			if value, exists := config[key]; exists && value != nil {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("qBittorrent 配置 %s 必须是布尔值", key)
				}
			}
		}
		for _, key := range []string{"root_folder", "auto_tmm"} {
			switch strings.ToLower(automationConfigString(config, key)) {
			case "", "default", "true", "false":
			default:
				return fmt.Errorf("qBittorrent 配置 %s 必须是 default/true/false", key)
			}
		}
		for _, key := range []string{"download_limit_kib", "upload_limit_kib"} {
			value, exists, err := automationConfigOptionalInt(config, key)
			if err != nil {
				return err
			}
			if exists && (value < 0 || value > 1_048_576) {
				return fmt.Errorf("qBittorrent 配置 %s 必须在 0 到 1048576 KiB/s 之间", key)
			}
		}
		if value, exists := config["ratio_limit"]; exists && value != nil {
			ratio, ok := automationNumber(value)
			if !ok || math.IsNaN(ratio) || math.IsInf(ratio, 0) || (ratio < 0 && ratio != -1 && ratio != -2) || ratio > 1000 {
				return errors.New("qBittorrent 分享率限制必须是 -2、-1 或 0 到 1000")
			}
		}
		if value, exists, err := automationConfigOptionalInt(config, "seeding_time_limit"); err != nil {
			return err
		} else if exists && ((value < 0 && value != -1 && value != -2) || value > 5_256_000) {
			return errors.New("qBittorrent 做种时间限制必须是 -2、-1 或 0 到 5256000 分钟")
		}
	case AutomationNodeWaitQBittorrent:
		pollSeconds := automationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && (pollSeconds < 5 || pollSeconds > 300) {
			return errors.New("qBittorrent 检查间隔必须在 5 到 300 秒之间")
		}
		if maxWaitMinutes := automationConfigUint(config, "max_wait_minutes"); maxWaitMinutes > 30*24*60 {
			return errors.New("qBittorrent 最长等待不能超过 30 天")
		}
	case AutomationNodeDeleteQBittorrent:
		if value, exists := config["delete_files"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("同时删除下载文件配置必须是布尔值")
			}
		}
	case AutomationNodeOffline115, AutomationNodeOffline115OpenAPI:
		if automationConfigUint(config, "cloud_storage_id") == 0 {
			return errors.New("必须选择 115 账号")
		}
		if automationConfigString(config, "url") == "" {
			return errors.New("必须配置下载地址变量")
		}
	case AutomationNodeWait115:
		pollSeconds := automationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && pollSeconds < minAutomation115PollSeconds {
			return fmt.Errorf("检查间隔不能少于 %d 秒", minAutomation115PollSeconds)
		}
		if pollSeconds > maxAutomation115PollSeconds {
			return fmt.Errorf("检查间隔不能超过 %d 秒", maxAutomation115PollSeconds)
		}
		maxWaitMinutes := automationConfigUint(config, "max_wait_minutes")
		if maxWaitMinutes > maxAutomation115MaxWaitMinutes {
			return fmt.Errorf("最长等待不能超过 %d 分钟", maxAutomation115MaxWaitMinutes)
		}
	case AutomationNodeRename115OpenAPI:
		if automationConfigUint(config, "cloud_storage_id") == 0 {
			return errors.New("必须选择 115 OpenAPI 账号")
		}
		if automationConfigString(config, "file_id") == "" {
			return errors.New("必须配置要重命名的文件或文件夹 ID")
		}
		newName := automationConfigString(config, "new_name")
		if newName == "" {
			return errors.New("必须配置新名称")
		}
		if !strings.HasPrefix(newName, "$") && !strings.Contains(newName, "{{") {
			if err := validateAutomation115RenameName(newName); err != nil {
				return err
			}
		}
	case AutomationNodeMoviePilotTitle:
		if automationConfigString(config, "input") == "" {
			return errors.New("必须配置待识别标题")
		}
		fallthrough
	case AutomationNodeMoviePilotRecognize:
		if source := automationConfigString(config, "recognition_source"); source != "" {
			if _, err := NormalizeMediaRecognitionSource(source); err != nil {
				return err
			}
		}
		tmdbID := automationConfigString(config, "tmdb_id")
		if tmdbID != "" && !strings.HasPrefix(tmdbID, "$") && !strings.Contains(tmdbID, "{{") && !automationTMDBIDPattern.MatchString(tmdbID) {
			return errors.New("TMDB ID 必须是正整数或流程变量")
		}
	case AutomationNodeFilmFusionRecognize:
		mode := automationFilmFusionRecognitionMode(node)
		configuredMode := strings.ToLower(automationConfigString(config, "recognition_mode"))
		if configuredMode != "" && configuredMode != MediaRecognitionModeTitle && configuredMode != MediaRecognitionModeFile {
			return errors.New("本地识别模式必须是 title/file")
		}
		if mode == MediaRecognitionModeTitle && automationConfigString(config, "input") == "" {
			return errors.New("发布标题模式必须配置待识别标题")
		}
		if value, exists := config["lookup_tmdb"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("查询 TMDB 配置必须是布尔值")
			}
		}
		tmdbID := automationConfigString(config, "tmdb_id")
		if tmdbID != "" && !strings.HasPrefix(tmdbID, "$") && !strings.Contains(tmdbID, "{{") && !automationTMDBIDPattern.MatchString(tmdbID) {
			return errors.New("TMDB ID 必须是正整数或流程变量")
		}
	case AutomationNodeMoviePilotTransfer:
		tmdbID := automationConfigString(config, "tmdb_id")
		if tmdbID != "" && !strings.HasPrefix(tmdbID, "$") && !strings.Contains(tmdbID, "{{") && !automationTMDBIDPattern.MatchString(tmdbID) {
			return errors.New("TMDB ID 必须是正整数或流程变量")
		}
		switch automationConfigString(config, "file_type") {
		case "", "auto", "file", "dir":
		default:
			return errors.New("MP2 整理源类型必须是 auto/file/dir")
		}
		switch automationConfigString(config, "media_type") {
		case "", "auto", "movie", "tv":
		default:
			return errors.New("MP2 整理媒体类型必须是 auto/movie/tv")
		}
		if len([]rune(automationConfigString(config, "source_path"))) > 4096 {
			return errors.New("MP2 可见源路径不能超过 4096 个字符")
		}
		if len([]rune(automationConfigString(config, "transfer_type"))) > 64 {
			return errors.New("MP2 整理方式不能超过 64 个字符")
		}
		if value, exists := config["scrape"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("MP2 刮削配置必须是布尔值")
			}
		}
	case AutomationNodeMediaExists:
		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择用于查重的目录配置")
		}
		if automationConfigString(config, "tmdb_id") == "" {
			return errors.New("必须配置 TMDB ID")
		}
	case AutomationNodeOrganizeStrm:
		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择整理目录配置")
		}
		switch automationConfigString(config, "media_type") {
		case "", "auto", "movie", "tv":
		default:
			return errors.New("媒体类型必须是 auto/movie/tv")
		}
		if len([]rune(automationConfigString(config, "category"))) > 200 {
			return errors.New("媒体分类不能超过 200 个字符")
		}
		for _, key := range []string{"best_version_enabled", "delete_source_folder", "filename_regex_enabled"} {
			if value, exists := config[key]; exists {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("%s 必须是布尔值", key)
				}
			}
		}
		if automationConfigBool(config, "filename_regex_enabled") {
			pattern := automationConfigString(config, "filename_regex_pattern")
			if pattern == "" {
				return errors.New("启用文件名正则后必须填写表达式")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("文件名正则表达式无效: %w", err)
			}
		}
	case AutomationNodeStrmVerify:
		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择 STRM 所属目录配置")
		}
	case AutomationNodeStrmRegenerate:
		if automationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择 STRM 重生成使用的目录配置")
		}
	case AutomationNodeEmbyRefreshWait:
		if automationConfigString(config, "tmdb_id") == "" {
			return errors.New("必须配置等待入库使用的 TMDB ID")
		}
		pollSeconds := automationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && (pollSeconds < 5 || pollSeconds > 300) {
			return errors.New("Emby 检查间隔必须在 5 到 300 秒之间")
		}
		if maxWaitMinutes := automationConfigUint(config, "max_wait_minutes"); maxWaitMinutes > 24*60 {
			return errors.New("Emby 最长等待不能超过 24 小时")
		}
	case AutomationNodeHTTPRequest:
		method := strings.ToUpper(automationConfigString(config, "method"))
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return errors.New("HTTP 方法必须是 GET/POST/PUT/PATCH/DELETE")
		}
		if automationConfigString(config, "url") == "" {
			return errors.New("必须配置 HTTP 请求地址")
		}
		if len(automationConfigString(config, "url")) > 8192 {
			return errors.New("HTTP 请求地址不能超过 8192 字节")
		}
		if len(automationConfigString(config, "body")) > 1024*1024 {
			return errors.New("HTTP 请求体不能超过 1 MiB")
		}
		if _, err := automationHTTPHeaders(config); err != nil {
			return err
		}
	case AutomationNodeNotification:
		if automationConfigString(config, "message") == "" {
			return errors.New("通知内容不能为空")
		}
		if len(automationConfigString(config, "image_url")) > 4096 {
			return errors.New("通知图片地址不能超过 4096 字节")
		}
	}
	return nil
}

func edgePortSet(edges []AutomationEdge) map[string]struct{} {
	result := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		result[strings.ToLower(strings.TrimSpace(edge.SourcePort))] = struct{}{}
	}
	return result
}

// effectiveAutomationPredecessor treats one or more successful delay nodes as
// transparent for adjacency rules. This keeps safeguards such as
// qBittorrent -> wait and 115 download -> wait intact while allowing a delay
// to be inserted between the guarded nodes.
func effectiveAutomationPredecessor(nodeID string, incoming map[string][]AutomationEdge, nodes map[string]AutomationNode) (AutomationNode, string) {
	edge := incoming[nodeID][0]
	predecessor := nodes[edge.Source]
	port := strings.ToLower(strings.TrimSpace(edge.SourcePort))
	visited := map[string]struct{}{}
	for predecessor.Type == AutomationNodeDelay && port == "success" {
		if _, exists := visited[predecessor.ID]; exists {
			break
		}
		visited[predecessor.ID] = struct{}{}
		delayIncoming := incoming[predecessor.ID]
		if len(delayIncoming) != 1 {
			break
		}
		edge = delayIncoming[0]
		predecessor = nodes[edge.Source]
		port = strings.ToLower(strings.TrimSpace(edge.SourcePort))
	}
	return predecessor, port
}

func hasAutomationCycle(nodes map[string]AutomationNode, outgoing map[string][]AutomationEdge) bool {
	indegree := make(map[string]int, len(nodes))
	for id := range nodes {
		indegree[id] = 0
	}
	for _, edges := range outgoing {
		for _, edge := range edges {
			indegree[edge.Target]++
		}
	}
	queue := make([]string, 0, len(nodes))
	for id, count := range indegree {
		if count == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, edge := range outgoing[id] {
			indegree[edge.Target]--
			if indegree[edge.Target] == 0 {
				queue = append(queue, edge.Target)
			}
		}
	}
	return visited != len(nodes)
}

func automationReachable(start string, outgoing map[string][]AutomationEdge) map[string]struct{} {
	visited := map[string]struct{}{}
	queue := []string{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, ok := visited[id]; ok {
			continue
		}
		visited[id] = struct{}{}
		for _, edge := range outgoing[id] {
			queue = append(queue, edge.Target)
		}
	}
	return visited
}

func automationCanReachAnyEnd(endIDs []string, incoming map[string][]AutomationEdge) map[string]struct{} {
	visited := make(map[string]struct{})
	queue := append([]string(nil), endIDs...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, exists := visited[id]; exists {
			continue
		}
		visited[id] = struct{}{}
		for _, edge := range incoming[id] {
			queue = append(queue, edge.Source)
		}
	}
	return visited
}

func validateAutomationConditionShape(raw any) error {
	condition, ok := raw.(map[string]any)
	if !ok {
		return errors.New("IF 条件格式无效")
	}
	for _, logicKey := range []string{"all", "any"} {
		if rawItems, exists := condition[logicKey]; exists {
			items, ok := rawItems.([]any)
			if !ok || len(items) == 0 {
				return fmt.Errorf("%s 条件不能为空", logicKey)
			}
			for _, item := range items {
				if err := validateAutomationConditionShape(item); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if nested, exists := condition["not"]; exists {
		return validateAutomationConditionShape(nested)
	}
	field := strings.TrimSpace(fmt.Sprint(firstAutomationConditionValue(condition, "field", "left")))
	operator := strings.ToLower(strings.TrimSpace(fmt.Sprint(firstAutomationConditionValue(condition, "operator", "op"))))
	if field == "" {
		return errors.New("IF 条件字段不能为空")
	}
	switch operator {
	case "exists", "not_exists":
		return nil
	case "eq", "neq", "gt", "gte", "lt", "lte", "contains", "not_contains", "starts_with", "ends_with":
		if _, exists := condition["value"]; !exists {
			if _, exists = condition["right"]; !exists {
				return errors.New("IF 条件缺少比较值")
			}
		}
		return nil
	case "regex":
		pattern := fmt.Sprint(firstAutomationConditionValue(condition, "value", "right"))
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("IF 正则表达式无效: %w", err)
		}
		return nil
	case "in":
		if _, ok := firstAutomationConditionValue(condition, "value", "right").([]any); !ok {
			return errors.New("in 条件的比较值必须是数组")
		}
		return nil
	default:
		return fmt.Errorf("不支持的 IF 条件操作符 %q", operator)
	}
}

func automationConfigString(config map[string]any, key string) string {
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func automationConfigUint(config map[string]any, key string) uint {
	value, ok := config[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		if typed > 0 {
			return uint(typed)
		}
	case int:
		if typed > 0 {
			return uint(typed)
		}
	case uint:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		if parsed > 0 {
			return uint(parsed)
		}
	}
	return 0
}

func automationConfigStringSlice(config map[string]any, key string) []string {
	value, ok := config[key]
	if !ok || value == nil {
		return nil
	}
	values, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			values = make([]any, len(typed))
			for index := range typed {
				values[index] = typed[index]
			}
		} else {
			return nil
		}
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		if text := strings.ToLower(strings.TrimSpace(fmt.Sprint(item))); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func automationConfigTextSlice(config map[string]any, key string) []string {
	value, ok := config[key]
	if !ok || value == nil {
		return nil
	}
	values, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			values = make([]any, len(typed))
			for index := range typed {
				values[index] = typed[index]
			}
		} else {
			return nil
		}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		text := strings.TrimSpace(fmt.Sprint(item))
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
	}
	return result
}

type automationReplacementRule struct {
	Keyword     string
	Replacement string
}

func automationConfigReplacementRules(config map[string]any, key string) ([]automationReplacementRule, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("关键词替换规则格式无效")
	}
	rules := make([]automationReplacementRule, 0, len(values))
	for index, value := range values {
		raw, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("第 %d 条关键词替换规则格式无效", index+1)
		}
		keywordValue, keywordOK := raw["keyword"].(string)
		replacementValue, replacementOK := raw["replacement"].(string)
		keyword := strings.TrimSpace(keywordValue)
		if !keywordOK || keyword == "" {
			return nil, fmt.Errorf("第 %d 条关键词替换规则的关键词不能为空", index+1)
		}
		if !replacementOK {
			return nil, fmt.Errorf("第 %d 条关键词替换规则的替换内容必须是文本", index+1)
		}
		rules = append(rules, automationReplacementRule{
			Keyword: keyword, Replacement: replacementValue,
		})
	}
	return rules, nil
}

func automationConfigRawString(config map[string]any, key string) string {
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
