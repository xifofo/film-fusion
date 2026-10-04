package service

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"gorm.io/gorm"
)

func upgradeRecoveryEvent(eventPath string, mapping model.CloudPath) bool {
	if mapping.SourceType == model.SourceTypeCloudDrive2 {
		eventPath = pathhelper.RemoveFirstDir(eventPath)
	}
	return IsUpgradeRecoveryPath(eventPath)
}

// Path-only webhooks cannot distinguish a late deletion of v1 from v2 now using
// the same path. Keep the managed entry while a journal is active, or while the
// recorded current cloud file is still present. Query errors preserve the entry.
func (s *StrmService) protectUpgradeEntry(root, localPath string, directory, creating bool) bool {
	if database.DB == nil || !database.DB.Migrator().HasTable(&model.OrganizeManagedStrm{}) {
		return false
	}
	var bindings []model.OrganizeManagedStrm
	query := database.DB.Where("root = ?", filepath.Clean(root))
	if !directory {
		query = query.Where("path = ?", localPath)
	}
	if err := query.Find(&bindings).Error; err != nil {
		return true
	}
	for _, binding := range bindings {
		if directory {
			rel, err := filepath.Rel(localPath, binding.Path)
			if err != nil || !filepath.IsLocal(rel) {
				continue
			}
		}
		if binding.Retired && creating {
			return true
		}
		var job model.OrganizeUpgradeJob
		if err := database.DB.Where("id = ?", binding.JobID).First(&job).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return true
		} else if err == nil && job.Status != "completed" && job.Status != "rolled_back" {
			return true
		}
		var storage model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", binding.CloudStorageID, binding.UserID).First(&storage).Error; err != nil {
			return true
		}
		web := NewWeb115Service(s.logger)
		newClient := s.newUpgradeClient
		if newClient == nil {
			newClient = web.NewClient
		}
		client, err := newClient(storage.Cookie)
		if err != nil {
			return true
		}
		for offset := 0; ; offset += 1150 {
			page, err := web.GetFilesWithClient(client, binding.ParentID, offset, 1150)
			if err != nil {
				return true
			}
			for _, file := range page.Items {
				if file.IsFile && file.Name == path.Base(binding.CloudPath) {
					return true
				}
			}
			if (page.Total > 0 && int64(offset+1150) >= page.Total) || (page.Total <= 0 && len(page.Items) < 1150) {
				break
			}
		}
		// Keep a tombstone so a delayed create/download cannot resurrect this entry.
		if creating {
			return true
		}
	}
	return false
}

func CheckUpgradeDownloadOwnership(db *gorm.DB, storageID uint, localPath string) error {
	if db == nil || !db.Migrator().HasTable(&model.OrganizeManagedStrm{}) {
		return nil
	}
	var count int64
	if err := db.Model(&model.OrganizeManagedStrm{}).Where("cloud_storage_id = ? AND path = ?", storageID, filepath.Clean(localPath)).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("文件已由洗版任务接管，旧下载不能写入: %s", localPath)
	}
	return nil
}

func upgradeStrmPath(localPath string) string {
	return strings.TrimSuffix(localPath, filepath.Ext(localPath)) + ".strm"
}
