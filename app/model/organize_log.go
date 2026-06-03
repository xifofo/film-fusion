package model

import "time"

// 整理动作类型
const (
	OrganizeActionStrmCreate   = "strm_create"      // 创建 STRM 文件
	OrganizeActionStrmDelete   = "strm_delete"      // 删除 STRM 文件 / 目录
	OrganizeActionStrmRename   = "strm_rename"      // 重命名 STRM 文件
	OrganizeActionFileDownload = "file_download"    // 通过 115 Open 下载非视频文件（如字幕）
	OrganizeActionWalkDir      = "walk_dir"         // 目录递归遍历入口
	OrganizeActionWebhookRecv  = "webhook_received" // 接收到外部 webhook
)

// 整理状态
const (
	OrganizeStatusSuccess = "success"
	OrganizeStatusSkipped = "skipped"
	OrganizeStatusFailed  = "failed"
)

// 触发来源
const (
	OrganizeTriggerCD2     = "cd2_notify"
	OrganizeTriggerMP2     = "mp2_notify"
	OrganizeTriggerManual  = "manual"
	OrganizeTriggerSystem  = "system"
	OrganizeTriggerWebhook = "webhook"
)

// OrganizeLog 整理日志（生成 STRM / 下载字幕等业务事件）
type OrganizeLog struct {
	ID             uint      `gorm:"primarykey" json:"id"`
	Action         string    `gorm:"size:32;index;not null;comment:整理动作" json:"action"`
	Status         string    `gorm:"size:16;index;not null;comment:状态" json:"status"`
	Trigger        string    `gorm:"size:32;index;comment:触发来源" json:"trigger"`
	Source         string    `gorm:"size:1024;index;comment:云盘源路径" json:"source"`
	Target         string    `gorm:"size:1024;comment:本地目标路径" json:"target"`
	CloudPathID    uint      `gorm:"index;comment:关联 cloud_paths" json:"cloud_path_id"`
	CloudStorageID uint      `gorm:"index;comment:关联 cloud_storages" json:"cloud_storage_id"`
	PickCode       string    `gorm:"size:64;index;comment:115 PickCode" json:"pick_code"`
	Message        string    `gorm:"size:512;comment:跳过原因或附加说明" json:"message"`
	Error          string    `gorm:"type:text;comment:失败时的错误" json:"error"`
	DurationMS     int64     `gorm:"comment:耗时 ms" json:"duration_ms"`
	SizeBytes      int64     `gorm:"comment:文件大小 bytes" json:"size_bytes"`
	CreatedAt      time.Time `gorm:"index" json:"created_at"`

	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

func (OrganizeLog) TableName() string {
	return "organize_logs"
}
