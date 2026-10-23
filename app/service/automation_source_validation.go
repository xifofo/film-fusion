package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"film-fusion/app/model"
)

func (s *AutomationService) validateAutomationSourceInput(input AutomationSourceInput) (AutomationSourceInput, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.TriggerType = strings.ToLower(strings.TrimSpace(input.TriggerType))
	if input.TriggerType == "" {
		input.TriggerType = model.AutomationTrigger115Directory
	}
	if input.Name == "" || len([]rune(input.Name)) > maxAutomationNameLength {
		return input, "", errors.New("自动化名称不能为空且不能超过 120 个字符")
	}
	switch input.TriggerType {
	case model.AutomationTriggerLocalDirectory:
		input, err := validateAutomationLocalSourceInput(input)
		return input, "", err
	case model.AutomationTriggerRSS:
		input.LocalConfig = nil
		input.FeedURL = strings.TrimSpace(input.FeedURL)
		parsed, err := url.ParseRequestURI(input.FeedURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return input, "", errors.New("RSS 地址必须是有效的 HTTP 或 HTTPS URL")
		}
		if input.IntervalMinutes == 0 {
			input.IntervalMinutes = defaultAutomationRSSInterval
		}
		if input.IntervalMinutes < 1 || input.IntervalMinutes > 1440 {
			return input, "", errors.New("RSS 刷新间隔必须在 1 到 1440 分钟之间")
		}
		if input.Mapping.ItemSelector == "" && len(input.Mapping.Fields) == 0 {
			input.Mapping = DefaultRSSAutomationMapping()
		}
		if err := ValidateRSSAutomationMapping(input.Mapping); err != nil {
			return input, "", err
		}
		mappingJSON, err := json.Marshal(input.Mapping)
		if err != nil {
			return input, "", err
		}
		input.CloudStorageID = 0
		input.DirectoryID = ""
		input.DirectoryPath = ""
		input.Recursive = false
		input.IntervalSeconds = 0
		input.QuietSeconds = 0
		return input, string(mappingJSON), nil

	case model.AutomationTrigger115Directory:
		input.LocalConfig = nil
		input.FeedURL = ""
		input.IntervalMinutes = 0
		input.Mapping = RSSAutomationMapping{}
		input.DirectoryID = strings.TrimSpace(input.DirectoryID)
		input.DirectoryPath = path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(input.DirectoryPath), "/"))
	default:
		return input, "", fmt.Errorf("暂不支持触发器类型 %q", input.TriggerType)
	}
	if input.CloudStorageID == 0 {
		return input, "", errors.New("必须选择 115 网盘账号")
	}
	if input.DirectoryID == "" {
		return input, "", errors.New("监控目录 ID 不能为空")
	}
	if input.DirectoryPath == "/." {
		input.DirectoryPath = "/"
	}
	if input.IntervalSeconds == 0 {
		input.IntervalSeconds = defaultAutomationIntervalSeconds
	}
	if input.IntervalSeconds < 30 || input.IntervalSeconds > 86400 {
		return input, "", errors.New("增量检查间隔必须在 30 秒到 24 小时之间")
	}
	if input.QuietSeconds == 0 {
		input.QuietSeconds = defaultAutomationQuietSeconds
	}
	if input.QuietSeconds < 30 || input.QuietSeconds > 86400 {
		return input, "", errors.New("稳定等待时间必须在 30 秒到 24 小时之间")
	}
	var storage model.CloudStorage
	if err := s.db.First(&storage, input.CloudStorageID).Error; err != nil {
		return input, "", fmt.Errorf("115 网盘账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return input, "", errors.New("所选云存储不是 115 OpenAPI 账号")
	}
	if storage.Status != model.StatusActive || strings.TrimSpace(storage.AccessToken) == "" {
		return input, "", errors.New("所选 115 账号不可用或缺少 AccessToken")
	}
	return input, "", nil
}

func automationSourceInputFromModel(source model.AutomationSource) AutomationSourceInput {
	input := AutomationSourceInput{
		Name: source.Name, Description: source.Description, Enabled: source.Enabled,
		TriggerType: source.TriggerType, CloudStorageID: source.CloudStorageID,
		DirectoryID: source.DirectoryID, DirectoryPath: source.DirectoryPath,
		Recursive: source.Recursive, IntervalSeconds: source.IntervalSeconds,
		QuietSeconds: source.QuietSeconds, FeedURL: source.FeedURL,
		IntervalMinutes: source.IntervalMinutes,
	}
	if source.TriggerType == model.AutomationTriggerRSS {
		mapping, err := ParseRSSAutomationMapping(source.MappingJSON)
		if err == nil {
			input.Mapping = mapping
		}
	}
	if source.TriggerType == model.AutomationTriggerLocalDirectory {
		config, err := ParseLocalDirectoryConfig(source.LocalConfigJSON)
		if err == nil {
			input.LocalConfig = &config
		}
	}
	return input
}

func automationSourceConfigurationChanged(source model.AutomationSource, input AutomationSourceInput, mappingJSON string) bool {
	if source.TriggerType == model.AutomationTriggerRSS {
		return source.FeedURL != input.FeedURL || source.MappingJSON != mappingJSON
	}
	if source.TriggerType == model.AutomationTriggerLocalDirectory {
		return source.DirectoryPath != input.DirectoryPath || source.Recursive != input.Recursive ||
			automationLocalDiscoveryConfigChanged(source.LocalConfigJSON, input.LocalConfig)
	}
	return source.CloudStorageID != input.CloudStorageID || source.DirectoryID != input.DirectoryID ||
		source.DirectoryPath != input.DirectoryPath || source.Recursive != input.Recursive
}

func validateAutomationWorkflowContent(name, description string, definition AutomationDefinition) (string, string, string, AutomationValidationResult, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || len([]rune(name)) > maxAutomationNameLength {
		return name, description, "", AutomationValidationResult{}, errors.New("流程名称不能为空且不能超过 120 个字符")
	}
	definition.SchemaVersion = AutomationSchemaVersion
	validation := ValidateAutomationDefinition(definition)
	if !validation.Valid {
		return name, description, "", validation, errors.New(strings.Join(validation.Errors, "; "))
	}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	return name, description, definitionJSON, validation, err
}

func (s *AutomationService) validateAutomationSourceWorkflow(input AutomationSourceInput, definition AutomationDefinition) error {
	resolved, err := resolveAutomationParameters(definition)
	if err != nil {
		return err
	}
	definition = resolved
	if input.TriggerType == model.AutomationTriggerLocalDirectory {
		return validateAutomationLocalSourceWorkflow(definition)
	}
	if input.TriggerType != model.AutomationTrigger115Directory {
		return nil
	}
	return s.validateAutomationMonitorLoop(input.CloudStorageID, input.DirectoryID, definition)
}

func (s *AutomationService) validateAutomationMonitorLoop(cloudStorageID uint, directoryID string, definition AutomationDefinition) error {
	var organizeStorage *model.CloudStorage
	for _, node := range definition.Nodes {
		if node.Type != AutomationNodeOrganizeStrm {
			continue
		}
		cloudDirectoryID := automationConfigUint(node.Config, "cloud_directory_id")
		if cloudDirectoryID == 0 {
			continue
		}
		var directory model.CloudDirectory
		if err := s.db.First(&directory, cloudDirectoryID).Error; err != nil {
			return fmt.Errorf("整理目录配置不存在: %w", err)
		}
		if directory.CloudStorageID != cloudStorageID {
			return errors.New("整理目录配置与监控目录必须使用同一个 115 账号")
		}
		if directory.CloudStorageID == cloudStorageID && directory.DirectoryID == strings.TrimSpace(directoryID) {
			return errors.New("整理目标不能与 115 监控根目录相同，否则会形成循环触发")
		}
		if organizeStorage == nil {
			var storage model.CloudStorage
			if err := s.db.First(&storage, cloudStorageID).Error; err != nil {
				return fmt.Errorf("115 网盘账号不存在: %w", err)
			}
			organizeStorage = &storage
		}
		if strings.TrimSpace(organizeStorage.Cookie) == "" {
			return errors.New("媒体整理与 STRM 生成还需要所选 115 账号配置可用 Cookie")
		}
	}
	return nil
}
