package service

import (
	"errors"
	"film-fusion/app/model"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const sourceFolderDeletionRetryDelay = time.Minute

type sourceFolderDeleteFunc func(storage model.CloudStorage, folderID string) error

// SourceFolderDeletionScheduleResult 描述启用延迟删除后的即时状态。
type SourceFolderDeletionScheduleResult struct {
	Deleted            bool
	Pending            bool
	TotalDownloads     int
	CompletedDownloads int
}

// BeginSourceFolderDeletion 先建立 collecting 记录，防止下载任务在整理尚未结束时触发删除。
func (s *Download115Service) BeginSourceFolderDeletion(cloudStorageID uint, folderID string) (uint, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("下载服务未初始化")
	}
	folderID = strings.TrimSpace(folderID)
	if cloudStorageID == 0 || folderID == "" || folderID == "0" {
		return 0, errors.New("源文件夹删除参数无效")
	}
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()

	var storage model.CloudStorage
	if err := s.db.Select("id").First(&storage, cloudStorageID).Error; err != nil {
		return 0, fmt.Errorf("云存储不存在: %w", err)
	}

	var existing model.OrganizeSourceFolderDeletionTask
	err := s.db.Where("cloud_storage_id = ? AND folder_id = ?", cloudStorageID, folderID).First(&existing).Error
	if err == nil {
		return 0, fmt.Errorf("源文件夹已有待删除任务（ID=%d）", existing.ID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}

	task := model.OrganizeSourceFolderDeletionTask{
		CloudStorageID: cloudStorageID,
		FolderID:       folderID,
		Status:         model.SourceFolderDeletionStatusCollecting,
	}
	if err := s.db.Create(&task).Error; err != nil {
		return 0, fmt.Errorf("创建源文件夹延迟删除任务失败: %w", err)
	}
	return task.ID, nil
}

// CancelSourceFolderDeletion 在整理失败时解除已入队下载与删除任务的关联，并保留源文件夹。
func (s *Download115Service) CancelSourceFolderDeletion(id uint) error {
	if s == nil || s.db == nil || id == 0 {
		return nil
	}
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Download115Queue{}).
			Where("source_folder_deletion_id = ?", id).
			Update("source_folder_deletion_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&model.OrganizeSourceFolderDeletionTask{}, id).Error
	})
}

// ArmSourceFolderDeletion 只在整理整体成功后把 collecting 任务切换为 waiting。
// 无关联下载时会立即删除；有下载时则等待全部任务成功完成。
func (s *Download115Service) ArmSourceFolderDeletion(id uint) (SourceFolderDeletionScheduleResult, error) {
	if s == nil || s.db == nil || id == 0 {
		return SourceFolderDeletionScheduleResult{}, errors.New("源文件夹延迟删除任务无效")
	}
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()

	result := s.db.Model(&model.OrganizeSourceFolderDeletionTask{}).
		Where("id = ? AND status = ?", id, model.SourceFolderDeletionStatusCollecting).
		Updates(map[string]any{
			"status":        model.SourceFolderDeletionStatusWaiting,
			"last_error":    "",
			"next_retry_at": nil,
		})
	if result.Error != nil {
		return SourceFolderDeletionScheduleResult{}, result.Error
	}
	if result.RowsAffected == 0 {
		var task model.OrganizeSourceFolderDeletionTask
		if err := s.db.First(&task, id).Error; err != nil {
			return SourceFolderDeletionScheduleResult{}, err
		}
		if task.Status != model.SourceFolderDeletionStatusWaiting && task.Status != model.SourceFolderDeletionStatusDeleting {
			return SourceFolderDeletionScheduleResult{}, fmt.Errorf("源文件夹延迟删除任务状态无效: %s", task.Status)
		}
	}

	return s.tryDeleteReadySourceFolderLocked(id)
}

// tryDeleteReadySourceFolder 原子领取一个已满足条件的删除任务，避免下载完成与定时恢复并发删除。
func (s *Download115Service) tryDeleteReadySourceFolder(id uint) (SourceFolderDeletionScheduleResult, error) {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	return s.tryDeleteReadySourceFolderLocked(id)
}

func (s *Download115Service) tryDeleteReadySourceFolderLocked(id uint) (SourceFolderDeletionScheduleResult, error) {
	var current model.OrganizeSourceFolderDeletionTask
	if err := s.db.First(&current, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SourceFolderDeletionScheduleResult{Deleted: true}, nil
		}
		return SourceFolderDeletionScheduleResult{}, err
	}

	schedule := SourceFolderDeletionScheduleResult{
		Pending:            true,
		TotalDownloads:     current.TotalDownloads,
		CompletedDownloads: current.CompletedDownloads,
	}
	if current.Status != model.SourceFolderDeletionStatusWaiting ||
		current.CompletedDownloads != current.TotalDownloads ||
		(current.NextRetryAt != nil && current.NextRetryAt.After(time.Now())) {
		return schedule, nil
	}

	now := time.Now()
	claim := s.db.Model(&model.OrganizeSourceFolderDeletionTask{}).
		Where("id = ? AND status = ? AND completed_downloads = total_downloads", id, model.SourceFolderDeletionStatusWaiting).
		Where("next_retry_at IS NULL OR next_retry_at <= ?", now).
		Update("status", model.SourceFolderDeletionStatusDeleting)
	if claim.Error != nil {
		return schedule, claim.Error
	}
	if claim.RowsAffected == 0 {
		return schedule, nil
	}

	var task model.OrganizeSourceFolderDeletionTask
	if err := s.db.Preload("CloudStorage").First(&task, id).Error; err != nil {
		return schedule, s.deferSourceFolderDeletionRetry(id, err)
	}
	if task.CloudStorage == nil {
		err := errors.New("源云存储配置不存在")
		return schedule, s.deferSourceFolderDeletionRetry(id, err)
	}
	if s.deleteSourceFolder == nil {
		err := errors.New("源文件夹删除执行器未初始化")
		return schedule, s.deferSourceFolderDeletionRetry(id, err)
	}
	if err := s.deleteSourceFolder(*task.CloudStorage, task.FolderID); err != nil {
		return schedule, s.deferSourceFolderDeletionRetry(id, err)
	}
	if err := s.db.Delete(&model.OrganizeSourceFolderDeletionTask{}, id).Error; err != nil {
		return schedule, fmt.Errorf("源文件夹已删除，但清理延迟删除记录失败: %w", err)
	}

	if s.logger != nil {
		s.logger.Infof("字幕下载全部完成，已删除整理源文件夹: StorageID=%d FolderID=%s Downloads=%d",
			task.CloudStorageID, task.FolderID, task.TotalDownloads)
	}
	return SourceFolderDeletionScheduleResult{
		Deleted:            true,
		TotalDownloads:     task.TotalDownloads,
		CompletedDownloads: task.CompletedDownloads,
	}, nil
}

func (s *Download115Service) deferSourceFolderDeletionRetry(id uint, deleteErr error) error {
	nextRetryAt := time.Now().Add(sourceFolderDeletionRetryDelay)
	updateErr := s.db.Model(&model.OrganizeSourceFolderDeletionTask{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":        model.SourceFolderDeletionStatusWaiting,
			"last_error":    deleteErr.Error(),
			"retry_count":   gorm.Expr("retry_count + 1"),
			"next_retry_at": nextRetryAt,
		}).Error
	if updateErr != nil {
		return fmt.Errorf("删除源文件夹失败: %v；保存重试状态失败: %w", deleteErr, updateErr)
	}
	return fmt.Errorf("删除源文件夹失败，已安排自动重试: %w", deleteErr)
}

func (s *Download115Service) processReadySourceFolderDeletions() {
	if s == nil || s.db == nil {
		return
	}
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	now := time.Now()
	var tasks []model.OrganizeSourceFolderDeletionTask
	if err := s.db.Select("id").
		Where("status = ? AND completed_downloads = total_downloads", model.SourceFolderDeletionStatusWaiting).
		Where("next_retry_at IS NULL OR next_retry_at <= ?", now).
		Order("created_at ASC").
		Limit(20).
		Find(&tasks).Error; err != nil {
		if s.logger != nil {
			s.logger.Errorf("查询待执行的源文件夹删除任务失败: %v", err)
		}
		return
	}
	for _, task := range tasks {
		if _, err := s.tryDeleteReadySourceFolderLocked(task.ID); err != nil && s.logger != nil {
			s.logger.Warnf("执行源文件夹延迟删除失败: ID=%d Error=%v", task.ID, err)
		}
	}
}

func (s *Download115Service) recoverInterruptedQueueState() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var abandoned []model.OrganizeSourceFolderDeletionTask
		if err := tx.Select("id").
			Where("status = ?", model.SourceFolderDeletionStatusCollecting).
			Find(&abandoned).Error; err != nil {
			return err
		}
		if len(abandoned) > 0 {
			ids := make([]uint, 0, len(abandoned))
			for _, task := range abandoned {
				ids = append(ids, task.ID)
			}
			if err := tx.Model(&model.Download115Queue{}).
				Where("source_folder_deletion_id IN ?", ids).
				Update("source_folder_deletion_id", nil).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", ids).Delete(&model.OrganizeSourceFolderDeletionTask{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.Download115Queue{}).
			Where("status = ?", model.QueueStatusDownloading).
			Updates(map[string]any{
				"status":     model.QueueStatusPending,
				"last_error": "服务重启，已恢复到等待队列",
			}).Error; err != nil {
			return err
		}
		return tx.Model(&model.OrganizeSourceFolderDeletionTask{}).
			Where("status = ?", model.SourceFolderDeletionStatusDeleting).
			Updates(map[string]any{
				"status":        model.SourceFolderDeletionStatusWaiting,
				"next_retry_at": nil,
			}).Error
	})
}

func (s *Download115Service) deleteSourceFolderWithWeb115(storage model.CloudStorage, folderID string) error {
	if strings.TrimSpace(storage.Cookie) == "" {
		return errors.New("源云存储 Cookie 缺失")
	}
	web115Service := NewWeb115Service(s.logger)
	client, err := web115Service.NewClient(storage.Cookie)
	if err != nil {
		return err
	}
	return web115Service.DeleteFilesWithClient(client, []string{folderID})
}

// completeDownloadQueueTask 在同一事务中累计成功数并删除已完成队列记录。
func (s *Download115Service) completeDownloadQueueTask(queueTaskID uint) (*uint, error) {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	var deletionID *uint
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var task model.Download115Queue
		if err := tx.First(&task, queueTaskID).Error; err != nil {
			return err
		}
		if task.SourceFolderDeletionID != nil {
			id := *task.SourceFolderDeletionID
			updated := tx.Model(&model.OrganizeSourceFolderDeletionTask{}).
				Where("id = ? AND completed_downloads < total_downloads", id).
				UpdateColumn("completed_downloads", gorm.Expr("completed_downloads + 1"))
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("源文件夹延迟删除任务不存在或计数异常: ID=%d", id)
			}
			deletionID = &id
		}
		return tx.Delete(&task).Error
	})
	return deletionID, err
}

func (s *Download115Service) noteSourceFolderDownloadFailure(queueTaskID uint, downloadErr error) {
	if s == nil || s.db == nil || downloadErr == nil {
		return
	}
	var task model.Download115Queue
	if err := s.db.Select("source_folder_deletion_id").First(&task, queueTaskID).Error; err != nil || task.SourceFolderDeletionID == nil {
		return
	}
	_ = s.db.Model(&model.OrganizeSourceFolderDeletionTask{}).
		Where("id = ?", *task.SourceFolderDeletionID).
		Update("last_error", "字幕下载失败，源文件夹将保留: "+downloadErr.Error()).Error
}

// cancelSourceFolderDeletionsForRemovedDownloads 在关联下载被人工移除时取消整批自动删除。
// 同一删除任务下仍在队列中的字幕会继续下载，但不再触发源文件夹删除。
func cancelSourceFolderDeletionsForRemovedDownloads(tx *gorm.DB, deletionIDs []uint) error {
	if tx == nil || len(deletionIDs) == 0 {
		return nil
	}
	uniqueIDs := make([]uint, 0, len(deletionIDs))
	seen := make(map[uint]struct{}, len(deletionIDs))
	for _, id := range deletionIDs {
		if id == 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return nil
	}
	if err := tx.Model(&model.Download115Queue{}).
		Where("source_folder_deletion_id IN ?", uniqueIDs).
		Update("source_folder_deletion_id", nil).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", uniqueIDs).
		Delete(&model.OrganizeSourceFolderDeletionTask{}).Error
}
