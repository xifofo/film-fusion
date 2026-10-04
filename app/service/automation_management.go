package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func automationConfiguration(source model.AutomationSource, workflow model.AutomationWorkflow) (AutomationCreateInput, error) {
	definition, err := ParseAutomationDefinition(workflow.DefinitionJSON)
	if err != nil {
		return AutomationCreateInput{}, err
	}
	return AutomationCreateInput{
		Source:   automationSourceInputFromModel(source),
		Workflow: AutomationCreateWorkflowInput{Name: workflow.Name, Description: workflow.Description, Enabled: workflow.Enabled, Definition: definition},
	}, nil
}

func snapshotAutomationRevision(tx *gorm.DB, sourceID uint) error {
	var source model.AutomationSource
	var workflow model.AutomationWorkflow
	if err := tx.First(&source, sourceID).Error; err != nil {
		return err
	}
	if err := tx.Where("source_id = ?", sourceID).First(&workflow).Error; err != nil {
		return err
	}
	input, err := automationConfiguration(source, workflow)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.AutomationRevision{
		SourceID: sourceID, WorkflowID: workflow.ID, Version: workflow.Version,
		ConfigurationJSON: string(raw), CreatedAt: workflow.UpdatedAt,
	}).Error
}

func (s *AutomationService) ListAutomationRevisions(id uint) ([]model.AutomationRevision, error) {
	revisions := []model.AutomationRevision{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := snapshotAutomationRevision(tx, id); err != nil {
			return err
		}
		return tx.Where("source_id = ?", id).Order("version DESC").Find(&revisions).Error
	})
	return revisions, err
}

func (s *AutomationService) RestoreAutomationRevision(id, revisionID uint, expectedVersion int) (AutomationCreateResult, error) {
	var revision model.AutomationRevision
	if err := s.db.Where("id = ? AND source_id = ?", revisionID, id).First(&revision).Error; err != nil {
		return AutomationCreateResult{}, err
	}
	var input AutomationCreateInput
	if err := json.Unmarshal([]byte(revision.ConfigurationJSON), &input); err != nil {
		return AutomationCreateResult{}, err
	}
	if expectedVersion < 1 {
		return AutomationCreateResult{}, errors.New("请刷新版本历史后再恢复")
	}
	input.ExpectedVersion = expectedVersion
	input.Source.Enabled = false
	input.Workflow.Enabled = false
	return s.UpdateAutomation(id, input)
}

type AutomationTemplateInput struct {
	Name          string                `json:"name"`
	Description   string                `json:"description"`
	Configuration AutomationCreateInput `json:"configuration"`
}

func (s *AutomationService) ListAutomationTemplates() ([]model.AutomationTemplate, error) {
	items := []model.AutomationTemplate{}
	err := s.db.Order("updated_at DESC, id DESC").Find(&items).Error
	return items, err
}

func (s *AutomationService) SaveAutomationTemplate(id uint, input AutomationTemplateInput) (model.AutomationTemplate, error) {
	item := model.AutomationTemplate{}
	if id > 0 {
		if err := s.db.First(&item, id).Error; err != nil {
			return item, err
		}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 120 {
		return item, errors.New("模板名称必须为 1 到 120 个字符")
	}
	trigger := input.Configuration.Source.TriggerType
	if trigger != model.AutomationTriggerRSS && trigger != model.AutomationTrigger115Directory {
		return item, errors.New("模板触发器类型无效")
	}
	definition := input.Configuration.Workflow.Definition
	if definition.SchemaVersion != 1 || len(definition.Nodes) == 0 || len(definition.Nodes) > 200 || len(definition.Edges) > 500 {
		return item, errors.New("模板流程格式或大小无效")
	}
	// Templates may intentionally have empty bindings for the recipient to fill.
	input.Configuration.Source.Enabled = false
	input.Configuration.Workflow.Enabled = false
	input.Configuration.ExpectedVersion = 0
	raw, err := json.Marshal(input.Configuration)
	if err != nil {
		return item, err
	}
	if len(raw) > 1<<20 {
		return item, errors.New("模板不能超过 1 MiB")
	}
	item.Name, item.Description, item.TriggerType = name, strings.TrimSpace(input.Description), trigger
	item.ConfigurationJSON = string(raw)
	err = s.db.Save(&item).Error
	return item, err
}

func (s *AutomationService) DeleteAutomationTemplate(id uint) error {
	result := s.db.Delete(&model.AutomationTemplate{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func defaultAutomationSettings(id uint) model.AutomationSettings {
	return model.AutomationSettings{SourceID: id, FailureThreshold: 3, StuckMinutes: 60, SourceErrors: true, CooldownMinutes: 60}
}

func (s *AutomationService) SaveAutomationSettings(id uint, input model.AutomationSettings) (model.AutomationSettings, error) {
	input.SourceID = id
	input.GroupName = strings.TrimSpace(input.GroupName)
	if len([]rune(input.GroupName)) > 80 {
		return input, errors.New("分组名称不能超过 80 个字符")
	}
	if input.FailureThreshold < 1 || input.FailureThreshold > 100 || input.StuckMinutes < 1 || input.StuckMinutes > 43200 || input.CooldownMinutes < 1 || input.CooldownMinutes > 43200 {
		return input, errors.New("连续失败阈值为 1 到 100，卡住与冷却时间为 1 到 43200 分钟")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var source model.AutomationSource
		if err := tx.First(&source, id).Error; err != nil {
			return err
		}
		if err := tx.Save(&input).Error; err != nil {
			return err
		}
		if !input.AlertsEnabled {
			return tx.Model(&model.AutomationAlert{}).Where("source_id = ?", id).Update("active", false).Error
		}
		return nil
	})
	return input, err
}

type AutomationBatchInput struct {
	SourceIDs []uint `json:"source_ids"`
	Action    string `json:"action"`
	GroupName string `json:"group_name"`
}

func (s *AutomationService) BatchAutomations(input AutomationBatchInput) error {
	if len(input.SourceIDs) == 0 || len(input.SourceIDs) > 200 {
		return errors.New("请选择 1 到 200 条自动化")
	}
	if input.Action != "enable" && input.Action != "disable" && input.Action != "group" {
		return errors.New("批量操作无效")
	}
	input.GroupName = strings.TrimSpace(input.GroupName)
	if len([]rune(input.GroupName)) > 80 {
		return errors.New("分组名称不能超过 80 个字符")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		seen := map[uint]bool{}
		validator := &AutomationService{db: tx}
		for _, id := range input.SourceIDs {
			if id == 0 {
				return errors.New("自动化 ID 无效")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			var source model.AutomationSource
			var workflow model.AutomationWorkflow
			if err := tx.First(&source, id).Error; err != nil {
				return err
			}
			if err := tx.Where("source_id = ?", id).First(&workflow).Error; err != nil {
				return err
			}
			if input.Action == "group" {
				settings := defaultAutomationSettings(id)
				if err := tx.First(&settings, "source_id = ?", id).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				settings.GroupName = input.GroupName
				if err := tx.Save(&settings).Error; err != nil {
					return err
				}
				continue
			}
			enabled := input.Action == "enable"
			if enabled {
				definition, err := ParseAutomationDefinition(workflow.DefinitionJSON)
				if err != nil {
					return err
				}
				validation := ValidateAutomationDefinition(definition)
				if !validation.Valid {
					return fmt.Errorf("%s: %s", source.Name, strings.Join(validation.Errors, "; "))
				}
				if _, _, err := validator.validateAutomationSourceInput(automationSourceInputFromModel(source)); err != nil {
					return fmt.Errorf("%s: %w", source.Name, err)
				}
				if err := validator.validateAutomationSourceWorkflow(automationSourceInputFromModel(source), definition); err != nil {
					return err
				}
			}
			if err := tx.Model(&source).Update("enabled", enabled).Error; err != nil {
				return err
			}
			if err := tx.Model(&workflow).Update("enabled", enabled).Error; err != nil {
				return err
			}
			if !enabled {
				if err := tx.Model(&model.AutomationAlert{}).Where("source_id = ?", id).Updates(map[string]any{"active": false, "updated_at": time.Now()}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		s.Wake()
	}
	return err
}
