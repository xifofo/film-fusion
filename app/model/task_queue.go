package model

import (
	"time"
)

// TaskStatus 任务状态
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusProcessing TaskStatus = "processing"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
)

// MediaTask 媒体任务模型
type MediaTask struct {
	ID          uint       `gorm:"primaryKey"`
	ItemID      string     `gorm:"not null;index"`
	Status      TaskStatus `gorm:"default:'pending';index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	ErrorMsg    string
	Retries     int `gorm:"default:0"`
}
