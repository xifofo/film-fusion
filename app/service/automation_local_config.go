package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"film-fusion/app/directorymonitor"
	"film-fusion/app/model"
)

// ParseLocalDirectoryConfig is shared by configuration round trips and the
// runtime. Empty persisted configuration receives the same safe defaults as
// a newly created monitor.
func ParseLocalDirectoryConfig(raw string) (model.LocalDirectoryConfig, error) {
	config := model.LocalDirectoryConfig{}
	if strings.TrimSpace(raw) != "" {
		if len(raw) > 128*1024 {
			return config, errors.New("本地目录配置不能超过 128 KiB")
		}
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			return config, fmt.Errorf("本地目录配置无效: %w", err)
		}
	}
	normalized, err := normalizeLocalDirectoryConfig(&config)
	if err != nil {
		return config, err
	}
	return *normalized, nil
}

func normalizeLocalDirectoryConfig(input *model.LocalDirectoryConfig) (*model.LocalDirectoryConfig, error) {
	config := model.LocalDirectoryConfig{}
	if input != nil {
		config = *input
		config.Events = append([]string(nil), input.Events...)
		config.IncludeExtensions = append([]string(nil), input.IncludeExtensions...)
		config.ExcludePatterns = append([]string(nil), input.ExcludePatterns...)
	}
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	config.ItemType = strings.ToLower(strings.TrimSpace(config.ItemType))
	config.InitialScan = strings.ToLower(strings.TrimSpace(config.InitialScan))
	for i := range config.Events {
		config.Events[i] = strings.ToLower(strings.TrimSpace(config.Events[i]))
	}
	if config.BatchSize != 0 && config.BatchSize < 16 {
		return nil, errors.New("扫描批次大小必须在 16 到 512 之间")
	}
	normalized, err := directorymonitor.NormalizeOptions(config)
	if err != nil {
		return nil, err
	}
	// Rules are sets; keep a canonical representation so merely reordering them
	// does not erase an established baseline.
	normalized.Events = canonicalLocalRules(normalized.Events)
	normalized.IncludeExtensions = canonicalLocalRules(normalized.IncludeExtensions)
	normalized.ExcludePatterns = canonicalLocalRules(normalized.ExcludePatterns)
	return &normalized, nil
}

func canonicalLocalRules(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func validateAutomationLocalSourceInput(input AutomationSourceInput) (AutomationSourceInput, error) {
	input.DirectoryPath = strings.TrimSpace(input.DirectoryPath)
	if len(input.DirectoryPath) == 0 || len(input.DirectoryPath) > 4096 ||
		strings.ContainsRune(input.DirectoryPath, 0) || !filepath.IsAbs(input.DirectoryPath) {
		return input, errors.New("本地监控目录必须是 FilmFusion 服务器可见的绝对路径，且不能超过 4096 字节")
	}
	input.DirectoryPath = filepath.Clean(input.DirectoryPath)
	if filepath.Dir(input.DirectoryPath) == input.DirectoryPath {
		return input, errors.New("不能监控整个文件系统根目录，请选择具体目录")
	}
	if input.IntervalSeconds == 0 {
		input.IntervalSeconds = 300
	}
	if input.IntervalSeconds < 30 || input.IntervalSeconds > 86400 {
		return input, errors.New("补偿扫描间隔必须在 30 秒到 24 小时之间")
	}
	if input.QuietSeconds == 0 {
		input.QuietSeconds = 30
	}
	if input.QuietSeconds < 1 || input.QuietSeconds > 86400 {
		return input, errors.New("稳定等待时间必须在 1 秒到 24 小时之间")
	}
	config, err := normalizeLocalDirectoryConfig(input.LocalConfig)
	if err != nil {
		return input, err
	}
	input.LocalConfig = config
	input.CloudStorageID, input.DirectoryID = 0, ""
	input.FeedURL, input.IntervalMinutes = "", 0
	input.Mapping = RSSAutomationMapping{}
	// Availability and symlink checks belong to the scanner: an unplugged NAS
	// must not prevent saving or disabling its configuration.
	return input, nil
}

func automationLocalConfigJSON(input AutomationSourceInput) string {
	if input.TriggerType != model.AutomationTriggerLocalDirectory || input.LocalConfig == nil {
		return ""
	}
	raw, _ := json.Marshal(input.LocalConfig) // All fields are concrete JSON scalar/slice types.
	return string(raw)
}

func automationLocalDiscoveryConfigChanged(raw string, input *model.LocalDirectoryConfig) bool {
	previous, err := ParseLocalDirectoryConfig(raw)
	if err != nil {
		return true
	}
	next, err := normalizeLocalDirectoryConfig(input)
	if err != nil {
		return true
	}
	// Runtime resource and timing adjustments retain durable discovery state.
	previous.Mode, next.Mode = "", ""
	previous.MaxWatches, next.MaxWatches = 0, 0
	previous.BatchSize, next.BatchSize = 0, 0
	previous.ScanBudgetMS, next.ScanBudgetMS = 0, 0
	previous.ScanItemLimit, next.ScanItemLimit = 0, 0
	previous.MaxPendingRuns, next.MaxPendingRuns = 0, 0
	previous.HistoryRetentionDays, next.HistoryRetentionDays = 0, 0
	previous.HistoryMaxRuns, next.HistoryMaxRuns = 0, 0
	previous.MissingRetentionDays, next.MissingRetentionDays = 0, 0
	return !reflect.DeepEqual(previous, *next)
}

func DefaultLocalDirectoryAutomationDefinition() AutomationDefinition {
	return AutomationDefinition{
		SchemaVersion: AutomationSchemaVersion,
		Nodes: []AutomationNode{
			{ID: "trigger", Type: AutomationNodeTrigger, Name: "本地目录变更", Position: AutomationPosition{X: 80, Y: 180}},
			{ID: "notify", Type: AutomationNodeNotification, Name: "发送通知", Position: AutomationPosition{X: 380, Y: 180}, Config: map[string]any{
				"channel": "default", "title": "目录变更：{{item.name}}", "message": "事件：{{item.event_type}}\n路径：{{item.path}}",
			}},
			{ID: "end", Type: AutomationNodeEnd, Name: "结束", Position: AutomationPosition{X: 680, Y: 180}},
		},
		Edges: []AutomationEdge{
			{ID: "trigger-notify", Source: "trigger", SourcePort: "next", Target: "notify"},
			{ID: "notify-end", Source: "notify", SourcePort: "success", Target: "end"},
		},
		Viewport: AutomationViewport{Zoom: 1},
	}
}

func validateAutomationLocalSourceWorkflow(definition AutomationDefinition) error {
	nodes := make(map[string]AutomationNode, len(definition.Nodes))
	incoming := make(map[string][]AutomationEdge, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range definition.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}
	for _, node := range definition.Nodes {
		requires115Files := node.Type == AutomationNodeOrganizeStrm || node.Type == AutomationNodeMoviePilotRecognize ||
			(node.Type == AutomationNodeFilmFusionRecognize && automationFilmFusionRecognitionMode(node) == MediaRecognitionModeFile)
		if !requires115Files {
			continue
		}
		predecessor, _ := effectiveAutomationPredecessor(node.ID, incoming, nodes)
		if predecessor.Type == AutomationNodeTrigger {
			return fmt.Errorf("节点 %s 需要 115 文件数据，不能直接连接本地目录触发器；本地文件名识别请使用标题模式，115 操作请先配置独立的下载或任务查询节点", node.ID)
		}
	}
	return nil
}
