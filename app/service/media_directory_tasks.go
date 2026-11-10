// 目录删除、停用、换账号和合并共用整理任务生命周期检查，历史记录不作改写。
package service

import (
	"fmt"

	"gorm.io/gorm"
)

// checkMediaDirectoryOrganizeTasks 由调用方在目录事务中执行；仅放行各类任务的已知终态。
func checkMediaDirectoryOrganizeTasks(db *gorm.DB, directoryIDs []uint) error {
	checks := []struct {
		table    string
		label    string
		terminal []string
	}{
		// 优先定位恢复任务：洗版中断时计划可能仍是 executing，应指引用户处理实际任务。
		// failed 洗版可能已经移动文件，必须继续或恢复；rolled_back 才是恢复完成。
		{"organize_upgrade_jobs", "洗版恢复任务", []string{"completed", "rolled_back", "cancelled", "canceled"}},
		// 普通整理失败后不可续跑原计划；洗版失败是否仍需恢复由独立任务检查。
		// ready 计划仍可能被用户提交，继续保护。
		{"organize_plans", "整理计划", []string{"executed", "failed", "completed", "applied", "cancelled", "canceled"}},
		// 预览失败没有执行文件变更。
		{"organize_preview_tasks", "预整理任务", []string{"completed", "failed", "cancelled", "canceled"}},
	}
	for _, check := range checks {
		if !db.Migrator().HasTable(check.table) {
			continue
		}

		var task struct {
			ID               string
			CloudDirectoryID uint
			Status           string
		}
		// 未知状态和 NULL 不应被当作完成；只返回定位信息，不泄露任务载荷。
		result := db.Table(check.table).
			Select("CAST(id AS TEXT) AS id, cloud_directory_id, COALESCE(status, '') AS status").
			Where("cloud_directory_id IN ? AND COALESCE(status, '') NOT IN ?", directoryIDs, check.terminal).
			Order("cloud_directory_id, id").Limit(1).Scan(&task)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return fmt.Errorf("媒体目录 #%d 仍有关联的%s #%s（状态：%s），请先处理该任务后再操作目录", task.CloudDirectoryID, check.label, task.ID, task.Status)
		}
	}

	return nil
}
