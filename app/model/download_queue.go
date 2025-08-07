package model

import (
	"time"
)

// Download115Queue 115Open专用下载队列模型（极简版）
type Download115Queue struct {
	ID             uint      `json:"id" gorm:"primarykey"`
	CloudStorageID uint      `json:"cloud_storage_id" gorm:"not null;index;comment:云存储配置ID"` // 关联的云存储配置ID
	PickCode       string    `json:"pick_code" gorm:"not null;uniqueIndex"`                  // 115文件的pickcode，唯一索引
	SavePath       string    `json:"save_path" gorm:"not null"`                              // 保存地址
	RetryCount     int       `json:"retry_count" gorm:"default:0;comment:重试次数"`              // 当前重试次数
	MaxRetryCount  int       `json:"max_retry_count" gorm:"default:3;comment:最大重试次数"`        // 最大重试次数
	LastError      string    `json:"last_error" gorm:"type:text;comment:最后一次错误信息"`           // 最后一次错误信息
	Status         string    `json:"status" gorm:"size:20;default:pending;comment:状态"`       // 状态：pending, downloading, completed, failed
	CreatedAt      time.Time `json:"created_at"`

	// 关联关系
	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

// TableName 指定表名
func (Download115Queue) TableName() string {
	return "download_115_queue"
}

// 状态常量
const (
	QueueStatusPending     = "pending"     // 等待中
	QueueStatusDownloading = "downloading" // 下载中
	QueueStatusCompleted   = "completed"   // 已完成
	QueueStatusFailed      = "failed"      // 失败
)

// CanRetry 检查是否可以重试
func (q *Download115Queue) CanRetry() bool {
	return q.RetryCount < q.MaxRetryCount && q.Status != QueueStatusCompleted
}

// IncrementRetry 增加重试次数
func (q *Download115Queue) IncrementRetry() {
	q.RetryCount++
}

// SetError 设置错误信息
func (q *Download115Queue) SetError(err error) {
	q.LastError = err.Error()
	if q.RetryCount >= q.MaxRetryCount {
		q.Status = QueueStatusFailed
	} else {
		q.Status = QueueStatusPending // 可以重试，回到等待状态
	}
}

// SetCompleted 设置为已完成状态
func (q *Download115Queue) SetCompleted() {
	q.Status = QueueStatusCompleted
	q.LastError = ""
}

// SetDownloading 设置为下载中状态
func (q *Download115Queue) SetDownloading() {
	q.Status = QueueStatusDownloading
}
