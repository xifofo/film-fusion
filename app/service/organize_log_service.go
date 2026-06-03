package service

import (
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
)

// OrganizeLogEntry 写入参数
type OrganizeLogEntry struct {
	Action         string
	Status         string
	Trigger        string
	Source         string
	Target         string
	CloudPathID    uint
	CloudStorageID uint
	PickCode       string
	Message        string
	Error          string
	DurationMS     int64
	SizeBytes      int64
}

// WriteOrganizeLog 异步写入一条整理日志，写入失败仅记录到 logger
func WriteOrganizeLog(log *logger.Logger, entry OrganizeLogEntry) {
	go func() {
		row := model.OrganizeLog{
			Action:         entry.Action,
			Status:         entry.Status,
			Trigger:        entry.Trigger,
			Source:         entry.Source,
			Target:         entry.Target,
			CloudPathID:    entry.CloudPathID,
			CloudStorageID: entry.CloudStorageID,
			PickCode:       entry.PickCode,
			Message:        entry.Message,
			Error:          entry.Error,
			DurationMS:     entry.DurationMS,
			SizeBytes:      entry.SizeBytes,
		}
		if err := database.DB.Create(&row).Error; err != nil {
			if log != nil {
				log.Warnf("写入 OrganizeLog 失败: %v", err)
			}
		}
	}()
}
