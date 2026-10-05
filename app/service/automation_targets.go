package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

var automationQBAPIKeyPattern = regexp.MustCompile(`^qbt_[A-Za-z0-9]{28}$`)

type AutomationTargetInput struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

type AutomationQBittorrentConfig struct {
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"password"`
	APIKey   string `json:"api_key"`
}

func (s *AutomationService) CreateTarget(input AutomationTargetInput) (model.AutomationTarget, error) {
	input, configJSON, err := validateAutomationTargetInput(input, "")
	if err != nil {
		return model.AutomationTarget{}, err
	}
	target := model.AutomationTarget{Name: input.Name, Type: input.Type, Enabled: input.Enabled, ConfigJSON: configJSON}
	if err := s.db.Create(&target).Error; err != nil {
		return target, err
	}
	redactAutomationTarget(&target)
	return target, nil
}

func (s *AutomationService) ListTargets() ([]model.AutomationTarget, error) {
	targets := make([]model.AutomationTarget, 0)
	if err := s.db.Order("id ASC").Find(&targets).Error; err != nil {
		return targets, err
	}
	redactAutomationTargets(targets)
	return targets, nil
}

func (s *AutomationService) UpdateTarget(id uint, input AutomationTargetInput) (model.AutomationTarget, error) {
	var target model.AutomationTarget
	if err := s.db.First(&target, id).Error; err != nil {
		return target, err
	}
	input, configJSON, err := validateAutomationTargetInput(input, target.ConfigJSON)
	if err != nil {
		return target, err
	}
	if err := s.db.Model(&target).Updates(map[string]any{
		"name": input.Name, "type": input.Type, "enabled": input.Enabled, "config_json": configJSON,
	}).Error; err != nil {
		return target, err
	}
	if err := s.db.First(&target, id).Error; err != nil {
		return target, err
	}
	redactAutomationTarget(&target)
	return target, nil
}

func (s *AutomationService) DeleteTarget(id uint) error {
	var workflows []model.AutomationWorkflow
	if err := s.db.Select("id", "name", "definition_json").Find(&workflows).Error; err != nil {
		return err
	}
	for _, workflow := range workflows {
		definition, err := ParseAutomationDefinition(workflow.DefinitionJSON)
		if err != nil {
			continue
		}
		for _, node := range definition.Nodes {
			if node.Type == AutomationNodeQBittorrent && automationConfigUint(node.Config, "target_id") == id {
				return fmt.Errorf("下载目标正在被流程 %q 使用，请先修改流程", workflow.Name)
			}
		}
	}
	result := s.db.Delete(&model.AutomationTarget{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func validateAutomationTargetInput(input AutomationTargetInput, oldConfigJSON string) (AutomationTargetInput, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	if input.Name == "" || len([]rune(input.Name)) > maxAutomationNameLength {
		return input, "", errors.New("目标名称不能为空且不能超过 120 个字符")
	}
	if input.Type != model.AutomationTargetQBittorrent {
		return input, "", fmt.Errorf("暂不支持目标类型 %q", input.Type)
	}
	encoded, err := json.Marshal(input.Config)
	if err != nil {
		return input, "", err
	}
	var config AutomationQBittorrentConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return input, "", errors.New("qBittorrent 配置格式错误")
	}
	_, apiKeyProvided := input.Config["api_key"]
	if oldConfigJSON != "" {
		var old AutomationQBittorrentConfig
		if json.Unmarshal([]byte(oldConfigJSON), &old) == nil {
			if config.Password == "" || config.Password == automationSecretMask {
				config.Password = old.Password
			}
			if !apiKeyProvided || config.APIKey == automationSecretMask {
				config.APIKey = old.APIKey
			}
		}
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.Username = strings.TrimSpace(config.Username)
	config.APIKey = strings.TrimSpace(config.APIKey)
	parsed, parseErr := url.ParseRequestURI(config.BaseURL)
	if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return input, "", errors.New("qBittorrent 地址必须是有效的 HTTP 或 HTTPS URL")
	}
	if config.APIKey != "" && !automationQBAPIKeyPattern.MatchString(config.APIKey) {
		return input, "", errors.New("qBittorrent API Key 必须以 qbt_ 开头，后接 28 位字母或数字")
	}
	if config.APIKey == "" && (config.Username == "" || config.Password == "") {
		return input, "", errors.New("请配置 qBittorrent API Key，或同时填写用户名和密码")
	}
	configJSON, _ := json.Marshal(config)
	return input, string(configJSON), nil
}

func redactAutomationTargets(targets []model.AutomationTarget) {
	for index := range targets {
		redactAutomationTarget(&targets[index])
	}
}

func redactAutomationTarget(target *model.AutomationTarget) {
	if target == nil || target.Type != model.AutomationTargetQBittorrent {
		return
	}
	var config AutomationQBittorrentConfig
	if json.Unmarshal([]byte(target.ConfigJSON), &config) != nil {
		return
	}
	if config.Password != "" {
		config.Password = automationSecretMask
	}
	if config.APIKey != "" {
		config.APIKey = automationSecretMask
	}
	encoded, _ := json.Marshal(config)
	target.ConfigJSON = string(encoded)
}
