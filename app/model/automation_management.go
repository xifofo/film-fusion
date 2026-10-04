package model

import "time"

type AutomationSettings struct {
	SourceID         uint      `gorm:"primaryKey" json:"source_id"`
	GroupName        string    `gorm:"size:80;index" json:"group_name"`
	AlertsEnabled    bool      `json:"alerts_enabled"`
	FailureThreshold int       `json:"failure_threshold"`
	StuckMinutes     int       `json:"stuck_minutes"`
	SourceErrors     bool      `json:"source_errors"`
	CooldownMinutes  int       `json:"cooldown_minutes"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type AutomationTemplate struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	Name              string    `gorm:"size:120;not null" json:"name"`
	Description       string    `gorm:"type:text" json:"description"`
	TriggerType       string    `gorm:"size:40;not null" json:"trigger_type"`
	ConfigurationJSON string    `gorm:"type:text;not null" json:"configuration_json"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type AutomationRevision struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	SourceID          uint      `gorm:"not null;index" json:"source_id"`
	WorkflowID        uint      `gorm:"not null;uniqueIndex:idx_automation_revision,priority:1" json:"workflow_id"`
	Version           int       `gorm:"not null;uniqueIndex:idx_automation_revision,priority:2" json:"version"`
	ConfigurationJSON string    `gorm:"type:text;not null" json:"configuration_json"`
	CreatedAt         time.Time `json:"created_at"`
}

type AutomationAlert struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	SourceID      uint       `gorm:"not null;uniqueIndex:idx_automation_alert,priority:1" json:"source_id"`
	Kind          string     `gorm:"size:40;not null;uniqueIndex:idx_automation_alert,priority:2" json:"kind"`
	Message       string     `gorm:"type:text" json:"message"`
	Active        bool       `gorm:"index" json:"active"`
	LastSentAt    *time.Time `json:"last_sent_at,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastError     string     `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
