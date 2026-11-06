package service

// 本文件负责转存队列失败记录的展示清理，不删除播放分配，也不调用网盘接口。

import (
	"context"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

func visibleTransferQueue(query *gorm.DB) *gorm.DB {
	return query.Where("is_source_playback = ?", false).
		Where("status <> ? OR queue_cleared_at IS NULL OR updated_at > queue_cleared_at", model.BalanceAssignmentStatusFailed)
}

// ClearFailedTransferQueue 清理指定 ID 或全部失败记录，返回实际清理数。
// 空 IDs 表示全部；调用方须显式确认范围。条件更新避免覆盖已经开始转存的任务。
func (s *BalanceStatusService) ClearFailedTransferQueue(ctx context.Context, ids []uint) (int64, error) {
	query := visibleTransferQueue(database.DB.WithContext(ctx).Model(&model.Match302BalanceAssignment{})).
		Where("status = ?", model.BalanceAssignmentStatusFailed)
	if len(ids) > 0 {
		query = query.Where("id IN ?", ids)
	}

	// 不更新 updated_at：清理不是转存状态变化；后续重试或新失败的更新时间会使记录重新可见。
	result := query.UpdateColumn("queue_cleared_at", time.Now())
	return result.RowsAffected, result.Error
}
