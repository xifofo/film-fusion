package service

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var automationNodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

type AutomationValidationResult struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

func ValidateAutomationDefinition(definition AutomationDefinition) AutomationValidationResult {
	result := AutomationValidationResult{Errors: []string{}, Warnings: []string{}}
	resolved, err := resolveAutomationParameters(definition)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	definition = resolved
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
		if isAutomationNonRetryableNode(node.Type) && node.MaxAttempts > 1 && node.Type != AutomationNodeOffline115OpenAPI {
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
			case AutomationNodeStopQBittorrent:
				if predecessor.Type != AutomationNodeWaitQBittorrent && predecessor.Type != AutomationNodeMoviePilotTransfer {
					result.Errors = append(result.Errors, fmt.Sprintf("停止 qBittorrent 做种节点 %s 必须连接在等待 qBittorrent 完成或 MP2 整理节点之后", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("停止 qBittorrent 做种节点 %s 必须连接上游节点的成功出口", id))
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
				if predecessor.Type != AutomationNodeOffline115 && predecessor.Type != AutomationNodeOffline115OpenAPI && predecessor.Type != AutomationNodeOffline115BTOpenAPI && predecessor.Type != AutomationNodeList115TasksOpenAPI {
					result.Errors = append(result.Errors, fmt.Sprintf("等待节点 %s 必须连接在 115 下载提交或按 Hash 查询任务节点之后，中间仅可插入延迟节点", id))
				}
				if sourcePort != "success" {
					result.Errors = append(result.Errors, fmt.Sprintf("等待节点 %s 必须连接 115 下载提交或查询节点的成功出口", id))
				}
				if predecessor.Type == AutomationNodeList115TasksOpenAPI && automationConfigString(predecessor.Config, "info_hash") == "" {
					result.Errors = append(result.Errors, "等待 115 下载前的任务列表节点必须配置 info_hash")
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
		AutomationNodeOffline115OpenAPI, AutomationNodeOffline115BTOpenAPI, AutomationNodeDelete115TaskOpenAPI, AutomationNodeClear115TasksOpenAPI,
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
			AutomationNodeQBittorrent, AutomationNodeWaitQBittorrent, AutomationNodeStopQBittorrent, AutomationNodeDeleteQBittorrent,
			AutomationNodeOffline115, AutomationNodeOffline115OpenAPI,
			AutomationNodeTorrentToMagnet, AutomationNodeTorrent115OpenAPI, AutomationNodeOffline115BTOpenAPI, AutomationNodeList115TasksOpenAPI,
			AutomationNodeQuota115OpenAPI, AutomationNodeDelete115TaskOpenAPI, AutomationNodeClear115TasksOpenAPI,
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
		AutomationNodeQBittorrent, AutomationNodeWaitQBittorrent, AutomationNodeStopQBittorrent, AutomationNodeDeleteQBittorrent,
		AutomationNodeOffline115, AutomationNodeOffline115OpenAPI,
		AutomationNodeTorrentToMagnet, AutomationNodeTorrent115OpenAPI, AutomationNodeOffline115BTOpenAPI, AutomationNodeList115TasksOpenAPI,
		AutomationNodeQuota115OpenAPI, AutomationNodeDelete115TaskOpenAPI, AutomationNodeClear115TasksOpenAPI,
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
