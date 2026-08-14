package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const RSSAutomationSchemaVersion = 1

const (
	RSSAutomationNodeTrigger             = "trigger"
	RSSAutomationNodeRegex               = "regex"
	RSSAutomationNodeKeyword             = "keyword"
	RSSAutomationNodeConvert             = "convert"
	RSSAutomationNodeIf                  = "if"
	RSSAutomationNodeParallel            = "parallel"
	RSSAutomationNodeJoin                = "join"
	RSSAutomationNodeQBittorrent         = "qbittorrent"
	RSSAutomationNodeWaitQBittorrent     = "wait_qbittorrent"
	RSSAutomationNodeOffline115          = "offline115"
	RSSAutomationNodeOffline115OpenAPI   = "offline115_openapi"
	RSSAutomationNodeWait115             = "wait115"
	RSSAutomationNodeMoviePilotTitle     = "moviepilot_title_recognize"
	RSSAutomationNodeMediaExists         = "media_exists"
	RSSAutomationNodeHDHiveQuery         = "hdhive_query"
	RSSAutomationNodeHDHiveUnlock        = "hdhive_unlock"
	RSSAutomationNodeMoviePilotRecognize = "moviepilot_recognize"
	RSSAutomationNodeOrganizeStrm        = "organize_strm"
	RSSAutomationNodeStrmVerify          = "strm_verify"
	RSSAutomationNodeStrmRegenerate      = "strm_regenerate"
	RSSAutomationNodeEmbyRefreshWait     = "emby_refresh_wait"
	RSSAutomationNodeHTTPRequest         = "http_request"
	RSSAutomationNodeNotification        = "notification"
	RSSAutomationNodeEnd                 = "end"
)

var rssAutomationNodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

type RSSAutomationPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type RSSAutomationViewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

type RSSAutomationNode struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Name        string                 `json:"name,omitempty"`
	Position    RSSAutomationPosition  `json:"position"`
	Config      map[string]any         `json:"config,omitempty"`
	MaxAttempts int                    `json:"max_attempts,omitempty"`
	UI          map[string]interface{} `json:"ui,omitempty"`
}

type RSSAutomationEdge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"source_port"`
	Target     string `json:"target"`
	TargetPort string `json:"target_port,omitempty"`
}

type RSSAutomationDefinition struct {
	SchemaVersion int                        `json:"schema_version"`
	Nodes         []RSSAutomationNode        `json:"nodes"`
	Edges         []RSSAutomationEdge        `json:"edges"`
	Viewport      RSSAutomationViewport      `json:"viewport,omitempty"`
	Metadata      map[string]json.RawMessage `json:"metadata,omitempty"`
}

type RSSAutomationValidationResult struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

func DefaultRSSAutomationDefinition() RSSAutomationDefinition {
	return RSSAutomationDefinition{
		SchemaVersion: RSSAutomationSchemaVersion,
		Nodes: []RSSAutomationNode{
			{ID: "trigger", Type: RSSAutomationNodeTrigger, Name: "RSS 条目进入", Position: RSSAutomationPosition{X: 80, Y: 180}},
			{ID: "end", Type: RSSAutomationNodeEnd, Name: "结束", Position: RSSAutomationPosition{X: 380, Y: 180}},
		},
		Edges:    []RSSAutomationEdge{{ID: "trigger-end", Source: "trigger", SourcePort: "next", Target: "end"}},
		Viewport: RSSAutomationViewport{Zoom: 1},
	}
}

func ParseRSSAutomationDefinition(raw string) (RSSAutomationDefinition, error) {
	var definition RSSAutomationDefinition
	if strings.TrimSpace(raw) == "" {
		return definition, errors.New("流程定义不能为空")
	}
	if err := json.Unmarshal([]byte(raw), &definition); err != nil {
		return definition, fmt.Errorf("流程定义 JSON 无效: %w", err)
	}
	if definition.SchemaVersion == 0 {
		definition.SchemaVersion = RSSAutomationSchemaVersion
	}
	return definition, nil
}

func MarshalRSSAutomationDefinition(definition RSSAutomationDefinition) (string, error) {
	definition.SchemaVersion = RSSAutomationSchemaVersion
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

func ValidateRSSAutomationDefinition(definition RSSAutomationDefinition) RSSAutomationValidationResult {
	result := RSSAutomationValidationResult{Errors: []string{}, Warnings: []string{}}
	if definition.SchemaVersion != RSSAutomationSchemaVersion {
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

	nodes := make(map[string]RSSAutomationNode, len(definition.Nodes))
	triggerIDs := make([]string, 0, 1)
	endIDs := make([]string, 0, 1)
	for _, node := range definition.Nodes {
		if !rssAutomationNodeIDPattern.MatchString(node.ID) {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 ID %q 无效", node.ID))
			continue
		}
		if _, exists := nodes[node.ID]; exists {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 ID %q 重复", node.ID))
			continue
		}
		if !isRSSAutomationNodeType(node.Type) {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 使用了不支持的类型 %q", node.ID, node.Type))
		}
		if len([]rune(node.Name)) > 120 {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 的名称不能超过 120 个字符", node.ID))
		}
		if node.MaxAttempts < 0 || node.MaxAttempts > 10 {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 的重试次数必须在 0 到 10 之间", node.ID))
		}
		if isRSSAutomationNonRetryableNode(node.Type) && node.MaxAttempts > 1 {
			result.Errors = append(result.Errors, fmt.Sprintf("可能产生重复副作用的节点 %s 不能自动重试", node.ID))
		}
		if err := validateRSSAutomationNodeConfig(node); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s: %v", node.ID, err))
		}
		nodes[node.ID] = node
		if node.Type == RSSAutomationNodeTrigger {
			triggerIDs = append(triggerIDs, node.ID)
		}
		if node.Type == RSSAutomationNodeEnd {
			endIDs = append(endIDs, node.ID)
		}
	}
	if len(triggerIDs) != 1 {
		result.Errors = append(result.Errors, "流程必须且只能有一个 RSS 触发器")
	}
	if len(endIDs) == 0 {
		result.Errors = append(result.Errors, "流程至少需要一个结束节点")
	}

	incoming := make(map[string][]RSSAutomationEdge, len(nodes))
	outgoing := make(map[string][]RSSAutomationEdge, len(nodes))
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
		if sourceExists && !isRSSAutomationSourcePortValid(source.Type, edge.SourcePort) {
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
		if node.Type != RSSAutomationNodeParallel && portCounts["always"] > 0 && outCount > 1 {
			result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 的 always 出口不能与其他出口同时使用；请显式使用并行节点", id))
		}
		if node.Type == RSSAutomationNodeParallel {
			configuredBranches := rssAutomationConfigStringSlice(node.Config, "branches")
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
		case RSSAutomationNodeTrigger:
			if inCount != 0 || outCount != 1 {
				result.Errors = append(result.Errors, fmt.Sprintf("触发器 %s 必须没有入口且只有一个出口", id))
			}
		case RSSAutomationNodeEnd:
			if inCount < 1 || outCount != 0 {
				result.Errors = append(result.Errors, fmt.Sprintf("结束节点 %s 必须至少有一个入口且没有出口", id))
			}
		case RSSAutomationNodeJoin:
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
			policy := rssAutomationConfigString(node.Config, "policy")
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
		case RSSAutomationNodeParallel:
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
			predecessor := nodes[incoming[id][0].Source]
			sourcePort := strings.ToLower(strings.TrimSpace(incoming[id][0].SourcePort))
			switch node.Type {
			case RSSAutomationNodeWaitQBittorrent:
				if predecessor.Type != RSSAutomationNodeQBittorrent {
					result.Errors = append(result.Errors, fmt.Sprintf("等待 qBittorrent 节点 %s 必须直接连接在 qBittorrent 下载节点之后", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("等待 qBittorrent 节点 %s 必须连接 qBittorrent 节点的成功出口", id))
				}
			case RSSAutomationNodeWait115:
				if predecessor.Type != RSSAutomationNodeOffline115 && predecessor.Type != RSSAutomationNodeOffline115OpenAPI {
					result.Errors = append(result.Errors, fmt.Sprintf("等待节点 %s 必须直接连接在 115 离线节点之后", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("等待节点 %s 必须连接 115 离线节点的成功出口", id))
				}
			case RSSAutomationNodeMoviePilotRecognize:
				if predecessor.Type != RSSAutomationNodeWait115 {
					result.Errors = append(result.Errors, fmt.Sprintf("MP 媒体识别节点 %s 必须直接连接在等待 115 下载完成节点之后", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("MP 媒体识别节点 %s 必须连接等待节点的成功出口", id))
				}
			case RSSAutomationNodeOrganizeStrm:
				if predecessor.Type != RSSAutomationNodeWait115 && predecessor.Type != RSSAutomationNodeMoviePilotRecognize {
					result.Errors = append(result.Errors, fmt.Sprintf("整理生成 STRM 节点 %s 必须直接连接在等待 115 下载完成或 MP 媒体识别节点之后", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("整理生成 STRM 节点 %s 必须连接上游节点的成功出口", id))
				}
			case RSSAutomationNodeHDHiveUnlock:
				if predecessor.Type != RSSAutomationNodeHDHiveQuery || sourcePort != "found" {
					result.Errors = append(result.Errors, fmt.Sprintf("HDHive 解锁节点 %s 必须连接资源查询节点的“找到资源”出口", id))
				}
			case RSSAutomationNodeStrmVerify:
				if predecessor.Type != RSSAutomationNodeOrganizeStrm || sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("STRM 校验节点 %s 必须连接整理生成 STRM 节点的成功出口", id))
				}
			case RSSAutomationNodeStrmRegenerate:
				if predecessor.Type != RSSAutomationNodeStrmVerify || sourcePort != "invalid" {
					result.Errors = append(result.Errors, fmt.Sprintf("STRM 重生成节点 %s 必须连接 STRM 校验节点的“无效”出口", id))
				}
			}
		}
		if node.Type == RSSAutomationNodeIf {
			ports := edgePortSet(outgoing[id])
			if _, ok := ports["true"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("IF 节点 %s 缺少 true 分支", id))
			}
			if _, ok := ports["false"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("IF 节点 %s 缺少 false 分支", id))
			}
		}
		if node.Type == RSSAutomationNodeKeyword {
			ports := edgePortSet(outgoing[id])
			if _, ok := ports["matched"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("关键词节点 %s 缺少“匹配”分支", id))
			}
			if _, ok := ports["unmatched"]; !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("关键词节点 %s 缺少“不匹配”分支", id))
			}
		}
	}

	if len(triggerIDs) == 1 {
		if hasRSSAutomationCycle(nodes, outgoing) {
			result.Errors = append(result.Errors, "流程不能包含循环")
		} else {
			reachable := rssAutomationReachable(triggerIDs[0], outgoing)
			for id := range nodes {
				if _, ok := reachable[id]; !ok {
					result.Errors = append(result.Errors, fmt.Sprintf("节点 %s 无法从触发器到达", id))
				}
			}
			canReachEnd := rssAutomationCanReachAnyEnd(endIDs, incoming)
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

func isRSSAutomationNodeType(nodeType string) bool {
	_, exists := rssAutomationNodeProtocolByType(nodeType)
	return exists
}

func isRSSAutomationNonRetryableNode(nodeType string) bool {
	switch nodeType {
	case RSSAutomationNodeOrganizeStrm, RSSAutomationNodeStrmRegenerate, RSSAutomationNodeHTTPRequest:
		return true
	default:
		return false
	}
}

func isRSSAutomationSourcePortValid(nodeType, port string) bool {
	port = strings.TrimSpace(strings.ToLower(port))
	if port == "always" {
		switch nodeType {
		case RSSAutomationNodeRegex, RSSAutomationNodeKeyword, RSSAutomationNodeConvert, RSSAutomationNodeJoin,
			RSSAutomationNodeQBittorrent, RSSAutomationNodeWaitQBittorrent,
			RSSAutomationNodeOffline115, RSSAutomationNodeOffline115OpenAPI,
			RSSAutomationNodeWait115, RSSAutomationNodeMoviePilotTitle, RSSAutomationNodeMediaExists,
			RSSAutomationNodeHDHiveQuery, RSSAutomationNodeHDHiveUnlock,
			RSSAutomationNodeMoviePilotRecognize, RSSAutomationNodeOrganizeStrm,
			RSSAutomationNodeStrmVerify, RSSAutomationNodeStrmRegenerate, RSSAutomationNodeEmbyRefreshWait,
			RSSAutomationNodeHTTPRequest, RSSAutomationNodeNotification:
			return true
		default:
			return false
		}
	}
	switch nodeType {
	case RSSAutomationNodeIf:
		return port == "true" || port == "false" || port == "failure"
	case RSSAutomationNodeKeyword:
		return port == "matched" || port == "unmatched" || port == "failure"
	case RSSAutomationNodeMediaExists:
		return port == "exists" || port == "missing" || port == "failure"
	case RSSAutomationNodeHDHiveQuery:
		return port == "found" || port == "not_found" || port == "failure"
	case RSSAutomationNodeStrmVerify:
		return port == "valid" || port == "invalid" || port == "failure"
	case RSSAutomationNodeParallel:
		return strings.HasPrefix(port, "branch-") && len(port) > len("branch-")
	case RSSAutomationNodeRegex, RSSAutomationNodeConvert, RSSAutomationNodeJoin,
		RSSAutomationNodeQBittorrent, RSSAutomationNodeWaitQBittorrent,
		RSSAutomationNodeOffline115, RSSAutomationNodeOffline115OpenAPI,
		RSSAutomationNodeWait115, RSSAutomationNodeMoviePilotTitle, RSSAutomationNodeHDHiveUnlock,
		RSSAutomationNodeMoviePilotRecognize, RSSAutomationNodeOrganizeStrm, RSSAutomationNodeStrmRegenerate, RSSAutomationNodeEmbyRefreshWait,
		RSSAutomationNodeHTTPRequest, RSSAutomationNodeNotification:
		return port == "success" || port == "failure"
	case RSSAutomationNodeTrigger:
		return port == "next"
	default:
		return false
	}
}

func validateRSSAutomationNodeConfig(node RSSAutomationNode) error {
	config := node.Config
	if config == nil {
		config = map[string]any{}
	}
	switch node.Type {
	case RSSAutomationNodeRegex:
		pattern := rssAutomationConfigString(config, "pattern")
		if pattern == "" {
			return errors.New("正则表达式不能为空")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("正则表达式无效: %w", err)
		}
		if rssAutomationConfigString(config, "input") == "" || rssAutomationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
	case RSSAutomationNodeKeyword:
		if rssAutomationConfigString(config, "input") == "" {
			return errors.New("必须配置输入字段")
		}
		keywords := rssAutomationConfigTextSlice(config, "keywords")
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
		switch rssAutomationConfigString(config, "match_mode") {
		case "contains_any", "contains_all", "contains_none":
		default:
			return errors.New("关键词规则必须是 contains_any/contains_all/contains_none")
		}
		if caseSensitive, exists := config["case_sensitive"]; exists {
			if _, ok := caseSensitive.(bool); !ok {
				return errors.New("区分大小写配置必须是布尔值")
			}
		}
	case RSSAutomationNodeConvert:
		if rssAutomationConfigString(config, "input") == "" || rssAutomationConfigString(config, "variable") == "" {
			return errors.New("必须配置输入字段和输出变量")
		}
		switch rssAutomationConfigString(config, "value_type") {
		case "string", "integer", "number", "boolean", "datetime":
		default:
			return errors.New("转换类型必须是 string/integer/number/boolean/datetime")
		}
	case RSSAutomationNodeIf:
		condition, ok := config["condition"]
		if !ok {
			return errors.New("必须配置判断条件")
		}
		if err := validateRSSAutomationConditionShape(condition); err != nil {
			return err
		}
	case RSSAutomationNodeParallel:
		branches := rssAutomationConfigStringSlice(config, "branches")
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
	case RSSAutomationNodeJoin:
		switch rssAutomationConfigString(config, "policy") {
		case "", "all_completed", "all_success", "any_success", "any_completed":
		default:
			return errors.New("不支持的汇合策略")
		}
	case RSSAutomationNodeQBittorrent:
		if rssAutomationConfigUint(config, "target_id") == 0 {
			return errors.New("必须选择 qBittorrent 目标")
		}
		if rssAutomationConfigString(config, "url") == "" {
			return errors.New("必须配置下载地址变量")
		}
	case RSSAutomationNodeWaitQBittorrent:
		pollSeconds := rssAutomationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && (pollSeconds < 5 || pollSeconds > 300) {
			return errors.New("qBittorrent 检查间隔必须在 5 到 300 秒之间")
		}
		if maxWaitMinutes := rssAutomationConfigUint(config, "max_wait_minutes"); maxWaitMinutes > 30*24*60 {
			return errors.New("qBittorrent 最长等待不能超过 30 天")
		}
	case RSSAutomationNodeOffline115, RSSAutomationNodeOffline115OpenAPI:
		if rssAutomationConfigUint(config, "cloud_storage_id") == 0 {
			return errors.New("必须选择 115 账号")
		}
		if rssAutomationConfigString(config, "url") == "" {
			return errors.New("必须配置下载地址变量")
		}
	case RSSAutomationNodeWait115:
		pollSeconds := rssAutomationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && pollSeconds < minRSSAutomation115PollSeconds {
			return fmt.Errorf("检查间隔不能少于 %d 秒", minRSSAutomation115PollSeconds)
		}
		if pollSeconds > maxRSSAutomation115PollSeconds {
			return fmt.Errorf("检查间隔不能超过 %d 秒", maxRSSAutomation115PollSeconds)
		}
		maxWaitMinutes := rssAutomationConfigUint(config, "max_wait_minutes")
		if maxWaitMinutes > maxRSSAutomation115MaxWaitMinutes {
			return fmt.Errorf("最长等待不能超过 %d 分钟", maxRSSAutomation115MaxWaitMinutes)
		}
	case RSSAutomationNodeMoviePilotTitle:
		if rssAutomationConfigString(config, "input") == "" {
			return errors.New("必须配置待识别标题")
		}
		fallthrough
	case RSSAutomationNodeMoviePilotRecognize:
		tmdbID := rssAutomationConfigString(config, "tmdb_id")
		if tmdbID != "" && !strings.HasPrefix(tmdbID, "$") && !strings.Contains(tmdbID, "{{") && !rssAutomationTMDBIDPattern.MatchString(tmdbID) {
			return errors.New("TMDB ID 必须是正整数或流程变量")
		}
	case RSSAutomationNodeMediaExists:
		if rssAutomationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择用于查重的目录配置")
		}
		if rssAutomationConfigString(config, "tmdb_id") == "" {
			return errors.New("必须配置 TMDB ID")
		}
	case RSSAutomationNodeHDHiveQuery:
		if rssAutomationConfigString(config, "tmdb_id") == "" {
			return errors.New("必须配置 HDHive 查询使用的 TMDB ID")
		}
		if rssAutomationConfigString(config, "media_type") == "" {
			return errors.New("必须配置 HDHive 查询使用的媒体类型")
		}
	case RSSAutomationNodeHDHiveUnlock:
		if rssAutomationConfigString(config, "slug") == "" {
			return errors.New("必须配置待解锁资源 slug")
		}
	case RSSAutomationNodeOrganizeStrm:
		if rssAutomationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择整理目录配置")
		}
		switch rssAutomationConfigString(config, "media_type") {
		case "", "auto", "movie", "tv":
		default:
			return errors.New("媒体类型必须是 auto/movie/tv")
		}
		if len([]rune(rssAutomationConfigString(config, "category"))) > 200 {
			return errors.New("媒体分类不能超过 200 个字符")
		}
		for _, key := range []string{"best_version_enabled", "delete_source_folder", "filename_regex_enabled"} {
			if value, exists := config[key]; exists {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("%s 必须是布尔值", key)
				}
			}
		}
		if rssAutomationConfigBool(config, "filename_regex_enabled") {
			pattern := rssAutomationConfigString(config, "filename_regex_pattern")
			if pattern == "" {
				return errors.New("启用文件名正则后必须填写表达式")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("文件名正则表达式无效: %w", err)
			}
		}
	case RSSAutomationNodeStrmVerify:
		if rssAutomationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择 STRM 所属目录配置")
		}
	case RSSAutomationNodeStrmRegenerate:
		if rssAutomationConfigUint(config, "cloud_directory_id") == 0 {
			return errors.New("必须选择 STRM 重生成使用的目录配置")
		}
	case RSSAutomationNodeEmbyRefreshWait:
		if rssAutomationConfigString(config, "tmdb_id") == "" {
			return errors.New("必须配置等待入库使用的 TMDB ID")
		}
		pollSeconds := rssAutomationConfigUint(config, "poll_interval_seconds")
		if pollSeconds > 0 && (pollSeconds < 5 || pollSeconds > 300) {
			return errors.New("Emby 检查间隔必须在 5 到 300 秒之间")
		}
		if maxWaitMinutes := rssAutomationConfigUint(config, "max_wait_minutes"); maxWaitMinutes > 24*60 {
			return errors.New("Emby 最长等待不能超过 24 小时")
		}
	case RSSAutomationNodeHTTPRequest:
		method := strings.ToUpper(rssAutomationConfigString(config, "method"))
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return errors.New("HTTP 方法必须是 GET/POST/PUT/PATCH/DELETE")
		}
		if rssAutomationConfigString(config, "url") == "" {
			return errors.New("必须配置 HTTP 请求地址")
		}
		if len(rssAutomationConfigString(config, "url")) > 8192 {
			return errors.New("HTTP 请求地址不能超过 8192 字节")
		}
		if len(rssAutomationConfigString(config, "body")) > 1024*1024 {
			return errors.New("HTTP 请求体不能超过 1 MiB")
		}
		if _, err := rssAutomationHTTPHeaders(config); err != nil {
			return err
		}
	case RSSAutomationNodeNotification:
		if rssAutomationConfigString(config, "message") == "" {
			return errors.New("通知内容不能为空")
		}
		if len(rssAutomationConfigString(config, "image_url")) > 4096 {
			return errors.New("通知图片地址不能超过 4096 字节")
		}
	}
	return nil
}

func edgePortSet(edges []RSSAutomationEdge) map[string]struct{} {
	result := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		result[strings.ToLower(strings.TrimSpace(edge.SourcePort))] = struct{}{}
	}
	return result
}

func hasRSSAutomationCycle(nodes map[string]RSSAutomationNode, outgoing map[string][]RSSAutomationEdge) bool {
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

func rssAutomationReachable(start string, outgoing map[string][]RSSAutomationEdge) map[string]struct{} {
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

func rssAutomationCanReachAnyEnd(endIDs []string, incoming map[string][]RSSAutomationEdge) map[string]struct{} {
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

func validateRSSAutomationConditionShape(raw any) error {
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
				if err := validateRSSAutomationConditionShape(item); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if nested, exists := condition["not"]; exists {
		return validateRSSAutomationConditionShape(nested)
	}
	field := strings.TrimSpace(fmt.Sprint(firstRSSAutomationConditionValue(condition, "field", "left")))
	operator := strings.ToLower(strings.TrimSpace(fmt.Sprint(firstRSSAutomationConditionValue(condition, "operator", "op"))))
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
		pattern := fmt.Sprint(firstRSSAutomationConditionValue(condition, "value", "right"))
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("IF 正则表达式无效: %w", err)
		}
		return nil
	case "in":
		if _, ok := firstRSSAutomationConditionValue(condition, "value", "right").([]any); !ok {
			return errors.New("in 条件的比较值必须是数组")
		}
		return nil
	default:
		return fmt.Errorf("不支持的 IF 条件操作符 %q", operator)
	}
}

func rssAutomationConfigString(config map[string]any, key string) string {
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func rssAutomationConfigUint(config map[string]any, key string) uint {
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

func rssAutomationConfigStringSlice(config map[string]any, key string) []string {
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

func rssAutomationConfigTextSlice(config map[string]any, key string) []string {
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
