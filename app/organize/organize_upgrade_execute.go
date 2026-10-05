package organize

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/service"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type organizeUpgradeOperation struct {
	SourceID   string                  `json:"source_id"`
	Item       Organize115ItemResult   `json:"item"`
	Subtitles  []Organize115ItemResult `json:"subtitles"`
	RecoveryID map[string]string       `json:"recovery_ids"`
}

type organizeUpgradePayload struct {
	Directory  model.CloudDirectory       `json:"directory"`
	Result     Organize115CookieResult    `json:"result"`
	Operations []organizeUpgradeOperation `json:"operations"`
	Rollback   bool                       `json:"rollback"`
	Prepared   bool                       `json:"prepared"`
}

func (h *Service) acquireOrganizeStorageLock(storageID uint, planID string) error {
	until := time.Now().Add(15 * time.Minute)
	if err := h.store().Where("cloud_storage_id = ? AND lease_until IS NOT NULL AND lease_until < ?", storageID, time.Now()).Delete(&model.OrganizeStorageLock{}).Error; err != nil {
		return err
	}
	row := model.OrganizeStorageLock{CloudStorageID: storageID, PlanID: planID, LeaseUntil: &until}
	if err := h.store().Create(&row).Error; err != nil {
		var existing model.OrganizeStorageLock
		if e := h.store().WithContext(h.requestContext()).First(&existing, storageID).Error; e == nil {
			return fmt.Errorf("此账号有尚未完成的整理任务 %s，请先在洗版记录中继续或恢复", existing.PlanID)
		}
		return err
	}
	return nil
}

func (h *Service) releaseOrganizeStorageLock(storageID uint, planID string) {
	h.store().Where("cloud_storage_id = ? AND plan_id = ?", storageID, planID).Delete(&model.OrganizeStorageLock{})
}

func keepOrganizeLeaseAlive(renew func()) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				renew()
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}

func (h *Service) validateUpgradeTargets(client *driver.Pan115Client, storageID uint, result Organize115CookieResult) error {
	for _, before := range result.UpgradeDirectories {
		after, _, err := h.upgradeDirectory(client, storageID, before.Path)
		if err != nil {
			return err
		}
		if after != before {
			return fmt.Errorf("库内目录已变化，请重新预览: %s", before.Path)
		}
	}
	for _, item := range result.Items {
		if item.Upgrade == nil {
			continue
		}
		for _, change := range item.Upgrade.Changes {
			current, err := service.ReadUpgradeFile(change.Before.Root, change.Before.Path)
			if err != nil {
				return err
			}
			if !service.SameUpgradeFile(current, change.Before) {
				return fmt.Errorf("关联文件已变化，请重新预览: %s", change.Before.Path)
			}
		}
	}
	return nil
}

func (h *Service) startUpgradeJob(dir model.CloudDirectory, storage model.CloudStorage, client *driver.Pan115Client, result Organize115CookieResult) (Organize115CookieResult, error) {
	if err := h.validateUpgradeTargets(client, storage.ID, result); err != nil {
		return result, err
	}
	payload := organizeUpgradePayload{Directory: dir, Result: result}
	payload.Directory.CloudStorage, payload.Directory.User = nil, nil
	for _, group := range result.Groups {
		for _, item := range group.Items {
			if item.IsSubtitle {
				continue
			}
			if item.Upgrade == nil || (item.Upgrade.Action != "add" && item.Upgrade.Action != "replace") {
				return result, errors.New("洗版预览中包含不可执行的文件")
			}
			op := organizeUpgradeOperation{SourceID: group.FolderID, Item: item, RecoveryID: map[string]string{}}
			for _, subtitle := range group.Items {
				if subtitle.IsSubtitle && subtitle.MatchedVideoFileID == item.FileID {
					op.Subtitles = append(op.Subtitles, subtitle)
				}
			}
			payload.Operations = append(payload.Operations, op)
		}
	}
	job := model.OrganizeUpgradeJob{ID: result.PlanID, UserID: dir.UserID, CloudDirectoryID: dir.ID, CloudStorageID: storage.ID, Status: "pending", RecoveryPath: path.Join(service.UpgradeRecoveryRoot, result.PlanID), PayloadJSON: planJSON(payload)}
	if err := h.store().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		lock := tx.Model(&model.OrganizeStorageLock{}).Where("cloud_storage_id = ? AND plan_id = ?", storage.ID, job.ID).Update("lease_until", nil)
		if lock.Error != nil {
			return lock.Error
		}
		if lock.RowsAffected != 1 {
			return errors.New("洗版执行锁已失效")
		}
		return nil
	}); err != nil {
		return result, err
	}
	result, err := h.runUpgradeJob(&job, &payload, &storage, client)
	if result.UpgradeJob == nil {
		result.UpgradeJob = &job
	}
	return result, err
}

func (h *Service) checkpointUpgradeJob(job *model.OrganizeUpgradeJob, payload *organizeUpgradePayload) error {
	until := time.Now().Add(5 * time.Minute)
	job.PayloadJSON = planJSON(payload)
	updated := h.store().Model(job).Where("run_token = ? AND status = ?", job.RunToken, "running").Updates(map[string]any{"payload_json": job.PayloadJSON, "lease_until": until})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return errors.New("洗版执行租约已变化，请刷新记录")
	}
	return nil
}

// Take SQLite's write lock before reading or changing managed records. A
// deferred read-then-write transaction can deadlock against the async log writer.
// The same conditional write also fences a request whose run token was replaced.
func renewUpgradeJobForWrite(tx *gorm.DB, job *model.OrganizeUpgradeJob) error {
	updated := tx.Model(job).Where("run_token = ? AND status = ?", job.RunToken, "running").Update("lease_until", time.Now().Add(5*time.Minute))
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return errors.New("洗版执行租约已变化，请刷新记录")
	}
	return nil
}

func (h *Service) runUpgradeJob(job *model.OrganizeUpgradeJob, payload *organizeUpgradePayload, storage *model.CloudStorage, client *driver.Pan115Client) (result Organize115CookieResult, runErr error) {
	result = payload.Result
	until := time.Now().Add(5 * time.Minute)
	token := rand.Text()
	if err := h.store().Transaction(func(tx *gorm.DB) error {
		lock := tx.Model(&model.OrganizeStorageLock{}).Select("1").Where("cloud_storage_id = ? AND plan_id = ?", job.CloudStorageID, job.ID)
		claim := tx.Model(job).Where("EXISTS (?)", lock).Where("status IN ? AND payload_json = ?", []string{"pending", "running", "failed"}, job.PayloadJSON).Where("lease_until IS NULL OR lease_until < ?", time.Now()).Updates(map[string]any{"status": "running", "lease_until": until, "run_token": token, "restoring": payload.Rollback, "error": "", "payload_json": planJSON(payload)})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return errors.New("洗版任务正在执行或状态已变化，请刷新记录；中断租约最多等待 5 分钟")
		}
		return nil
	}); err != nil {
		return result, err
	}
	job.RunToken, job.Restoring = token, payload.Rollback
	stopHeartbeat := keepOrganizeLeaseAlive(func() {
		h.store().Model(&model.OrganizeUpgradeJob{}).Where("id = ? AND run_token = ? AND status = ?", job.ID, token, "running").Update("lease_until", time.Now().Add(5*time.Minute))
	})
	defer stopHeartbeat()
	defer func() {
		if runErr != nil {
			job.Status, job.Error = "failed", runErr.Error()
			h.store().Model(job).Where("run_token = ?", token).Updates(map[string]any{"status": "failed", "error": job.Error, "lease_until": nil})
			WriteUpgradeLog(h, job, runErr)
		}
		job.LeaseUntil = nil
		result.UpgradeJob = job
	}()
	if payload.Rollback {
		runErr = h.rollbackUpgradeJob(job, payload, storage, client)
		if runErr == nil {
			WriteUpgradeLog(h, job, nil)
		}
		return result, runErr
	}
	if !payload.Prepared {
		readSubtitle := h.readUpgradeSubtitle
		if readSubtitle == nil {
			readSubtitle = func(storage *model.CloudStorage, pickcode string, size int64) ([]byte, error) {
				return service.FetchUpgradeSubtitleContext(h.requestContext(), storage, pickcode, size)
			}
		}
		downloads := map[string][]byte{}
		for i := range payload.Operations {
			for j := range payload.Operations[i].Item.Upgrade.Changes {
				change := &payload.Operations[i].Item.Upgrade.Changes[j]
				if change.DownloadPickCode == "" || len(change.Data) > 0 {
					continue
				}
				data, ok := downloads[change.DownloadPickCode]
				if !ok {
					var err error
					data, err = readSubtitle(storage, change.DownloadPickCode, change.DownloadSize)
					if err != nil {
						return result, err
					}
					downloads[change.DownloadPickCode] = data
				}
				if int64(len(data)) != change.DownloadSize {
					return result, errors.New("字幕大小与计划不符")
				}
				change.Data = data
			}
		}
		for i := range payload.Operations {
			op := &payload.Operations[i]
			items := append([]Organize115ItemResult{op.Item}, op.Subtitles...)
			if _, err := h.resolveAndPrepareDirectories(storage, client, &items, false); err != nil {
				return result, err
			}
			op.Item, op.Subtitles = items[0], items[1:]
			for _, existing := range op.Item.Upgrade.Existing {
				if _, ok := op.RecoveryID[existing.File.ParentID]; ok {
					continue
				}
				recovery := []Organize115ItemResult{{TargetDir: path.Join(job.RecoveryPath, existing.File.ParentID)}}
				if _, err := h.resolveAndPrepareDirectories(storage, client, &recovery, false); err != nil {
					return result, err
				}
				op.RecoveryID[existing.File.ParentID] = recovery[0].TargetDirID
			}
		}
		payload.Prepared = true
		if err := h.checkpointUpgradeJob(job, payload); err != nil {
			return result, err
		}
	}
	// Register guards before cloud moves can emit delayed create/delete webhooks.
	if err := h.persistUpgradeGuards(job, payload); err != nil {
		return result, err
	}
	for i := range payload.Operations {
		op := &payload.Operations[i]
		if err := h.checkpointUpgradeJob(job, payload); err != nil {
			return result, err
		}
		for _, existing := range op.Item.Upgrade.Existing {
			for _, oldFile := range append([]service.Web115File{existing.File}, existing.Sidecars...) {
				for _, incoming := range append([]Organize115ItemResult{op.Item}, op.Subtitles...) {
					if oldFile.ParentID == incoming.TargetDirID && oldFile.Name == incoming.RenameTo {
						if err := h.ensureUpgradeFile(client, oldFile, oldFile.ParentID, op.RecoveryID[oldFile.ParentID], oldFile.Name); err != nil {
							return result, err
						}
					}
				}
			}
		}
		for _, item := range append([]Organize115ItemResult{op.Item}, op.Subtitles...) {
			file := service.Web115File{FileID: item.FileID, Name: item.FileName, Size: item.FileSize, PickCode: item.PickCode, IsFile: true}
			if err := h.ensureUpgradeFile(client, file, op.SourceID, item.TargetDirID, item.RenameTo); err != nil {
				return result, err
			}
		}
	}
	// Install all new entries before removing any old entry. Retry accepts only
	// the recorded pre-state or exact post-state, including interrupted writes.
	for _, op := range payload.Operations {
		for _, change := range op.Item.Upgrade.Changes {
			if change.Write {
				if err := service.ApplyUpgradeFile(change, false); err != nil {
					return result, err
				}
			}
		}
	}
	if err := h.commitUpgradeRecords(job, payload, false); err != nil {
		return result, err
	}
	for _, op := range payload.Operations {
		for _, change := range op.Item.Upgrade.Changes {
			if !change.Write {
				if err := service.ApplyUpgradeFile(change, false); err != nil {
					return result, err
				}
			}
		}
	}
	for _, op := range payload.Operations {
		if err := h.checkpointUpgradeJob(job, payload); err != nil {
			return result, err
		}
		for _, existing := range op.Item.Upgrade.Existing {
			for _, oldFile := range append([]service.Web115File{existing.File}, existing.Sidecars...) {
				if err := h.ensureUpgradeFile(client, oldFile, oldFile.ParentID, op.RecoveryID[oldFile.ParentID], oldFile.Name); err != nil {
					return result, err
				}
			}
		}
	}
	job.Status, job.Error = "completed", ""
	if h.embyClient != nil {
		if status, body, err := h.embyClient.RefreshLibrary(); err != nil {
			job.RefreshError = err.Error()
		} else if status < 200 || status >= 300 {
			job.RefreshError = fmt.Sprintf("Emby 刷新失败 HTTP %d: %s", status, body)
		}
	}
	if err := h.finishUpgradeJob(job, payload, "executed"); err != nil {
		return result, err
	}
	WriteUpgradeLog(h, job, nil)
	return result, nil
}

func WriteUpgradeLog(h *Service, job *model.OrganizeUpgradeJob, err error) {
	entry := model.OrganizeLog{Action: "media_upgrade", Status: model.OrganizeStatusSuccess, Trigger: model.OrganizeTriggerManual, CloudStorageID: job.CloudStorageID, Target: job.RecoveryPath, Message: "洗版任务 " + job.ID + " " + job.Status}
	if err != nil {
		entry.Status, entry.Error = model.OrganizeStatusFailed, err.Error()
	}
	// Finish the audit write before returning the recoverable job to the caller.
	if err := h.store().Create(&entry).Error; err != nil && h.logger != nil {
		h.logger.Warnf("写入洗版日志失败: %v", err)
	}
}

// Remote mutations can time out after succeeding. Every step queries actual IDs
// before and after the request, and accepts an already completed transition.
func (h *Service) ensureUpgradeFile(client *driver.Pan115Client, expected service.Web115File, sourceID, targetID, targetName string) error {
	if sourceID == "" || targetID == "" || expected.FileID == "" || targetName == "" {
		return errors.New("洗版缺少文件或目录标识")
	}
	locate := func() (service.Web115File, error) {
		for _, id := range uniqueUpgradeStrings([]string{targetID, sourceID}) {
			files, err := h.upgradeList(client, id)
			if err != nil {
				return service.Web115File{}, err
			}
			for _, file := range files {
				if file.FileID == expected.FileID {
					if !file.IsFile || file.Size != expected.Size || file.PickCode != expected.PickCode || (file.Name != expected.Name && file.Name != targetName) {
						return file, fmt.Errorf("文件身份已变化: %s", expected.Name)
					}
					return file, nil
				}
			}
		}
		return service.Web115File{}, fmt.Errorf("文件不在记录的原位置或目标位置: %s", expected.Name)
	}
	current, err := locate()
	if err != nil {
		return err
	}
	if current.ParentID == targetID && current.Name == targetName {
		return nil
	}
	// A provider's implicit overwrite must never decide which file gets deleted.
	for _, id := range uniqueUpgradeStrings([]string{current.ParentID, targetID}) {
		files, err := h.upgradeList(client, id)
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.Name == targetName && file.FileID != expected.FileID {
				return fmt.Errorf("目标名称已被其他文件占用: %s", targetName)
			}
		}
	}
	if current.Name != targetName {
		renameErr := h.web115Svc.BatchRename(client, map[string]string{expected.FileID: targetName})
		current, err = locate()
		if err != nil || current.Name != targetName {
			return fmt.Errorf("重命名未确认 %s: %v / %v", targetName, renameErr, err)
		}
	}
	if current.ParentID != targetID {
		moveErr := h.web115Svc.MoveFiles(client, targetID, []string{expected.FileID})
		current, err = locate()
		if err != nil || current.ParentID != targetID || current.Name != targetName {
			return fmt.Errorf("移动未确认 %s: %v / %v", targetName, moveErr, err)
		}
	}
	return nil
}

func uniqueUpgradeStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func (h *Service) persistUpgradeGuards(job *model.OrganizeUpgradeJob, payload *organizeUpgradePayload) error {
	return h.store().Transaction(func(tx *gorm.DB) error {
		if err := renewUpgradeJobForWrite(tx, job); err != nil {
			return err
		}
		for _, op := range payload.Operations {
			for _, old := range op.Item.Upgrade.Existing {
				if err := putUpgradeGuards(tx, job, old.File, old.CloudPath, old.Bindings); err != nil {
					return err
				}
				for _, change := range op.Item.Upgrade.Changes {
					if !change.Before.Exists || change.DownloadPickCode != "" {
						continue
					}
					stem := strings.TrimSuffix(path.Base(old.CloudPath), path.Ext(old.CloudPath))
					if upgradeAssociatedArtifact(stem, filepath.Base(change.Before.Path)) {
						binding := OrganizeUpgradeBinding{Root: change.Before.Root, Path: change.Before.Path}
						if err := putUpgradeGuards(tx, job, old.File, old.CloudPath, []OrganizeUpgradeBinding{binding}); err != nil {
							return err
						}
					}
				}
			}
			file := service.Web115File{FileID: op.Item.FileID, ParentID: op.Item.TargetDirID}
			if err := putUpgradeGuards(tx, job, file, op.Item.TargetPath, op.Item.Upgrade.Bindings); err != nil {
				return err
			}
			if err := putUpgradeSubtitleGuards(tx, job, op); err != nil {
				return err
			}
		}
		return nil
	})
}

func putUpgradeSubtitleGuards(tx *gorm.DB, job *model.OrganizeUpgradeJob, op organizeUpgradeOperation) error {
	for _, subtitle := range op.Subtitles {
		for _, change := range op.Item.Upgrade.Changes {
			if change.DownloadPickCode != subtitle.PickCode {
				continue
			}
			binding := OrganizeUpgradeBinding{Root: change.Before.Root, Path: change.Before.Path}
			file := service.Web115File{FileID: subtitle.FileID, ParentID: subtitle.TargetDirID}
			if err := putUpgradeGuards(tx, job, file, subtitle.TargetPath, []OrganizeUpgradeBinding{binding}); err != nil {
				return err
			}
		}
	}
	return nil
}

func putUpgradeGuards(tx *gorm.DB, job *model.OrganizeUpgradeJob, file service.Web115File, cloudPath string, bindings []OrganizeUpgradeBinding) error {
	for _, binding := range bindings {
		var prior model.OrganizeManagedStrm
		if err := tx.Where("path = ?", binding.Path).First(&prior).Error; err == nil && (prior.UserID != job.UserID || prior.CloudStorageID != job.CloudStorageID) {
			return fmt.Errorf("STRM 已归属于其他账号: %s", binding.Path)
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		row := model.OrganizeManagedStrm{JobID: job.ID, Path: binding.Path, Root: binding.Root, UserID: job.UserID, CloudStorageID: job.CloudStorageID, ParentID: file.ParentID, FileID: file.FileID, CloudPath: cloudPath, Content: binding.Content, CacheKey: binding.CacheKey}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "path"}}, UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func retireUpgradeCaches(tx *gorm.DB, storageID uint, fileID, pickcode string) error {
	var rows []model.Match302BalanceAssignment
	query := tx.Where("source_storage_id = ? AND retired = ?", storageID, false)
	if pickcode != "" {
		query = query.Where("(source_file_id = ? OR source_pickcode = ?)", fileID, pickcode)
	} else {
		query = query.Where("source_file_id = ?", fileID)
	}
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		// Keep the old target and its cleanup metadata. Its old source lookup key
		// is freed for a fresh assignment; even an in-flight transfer cannot revive it.
		if err := tx.Model(&row).Updates(map[string]any{"retired": true, "retired_source_path": row.SourceFilePath, "source_file_path": fmt.Sprintf("/__filmfusion_retired__/%d", row.ID), "expires_at": time.Now()}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (h *Service) commitUpgradeRecords(job *model.OrganizeUpgradeJob, payload *organizeUpgradePayload, restore bool) error {
	return h.store().Transaction(func(tx *gorm.DB) error {
		if err := renewUpgradeJobForWrite(tx, job); err != nil {
			return err
		}
		for _, op := range payload.Operations {
			decision := op.Item.Upgrade
			incoming := OrganizeUpgradeExisting{File: service.Web115File{FileID: op.Item.FileID, ParentID: op.Item.TargetDirID, Size: op.Item.FileSize, PickCode: op.Item.PickCode}, CloudPath: op.Item.TargetPath, MediaKey: decision.MediaKey, Facts: decision.Facts, Bindings: decision.Bindings}
			remove, install := decision.Existing, []OrganizeUpgradeExisting{incoming}
			if restore {
				remove, install = install, remove
			}
			for _, old := range remove {
				if err := tx.Where("cloud_storage_id = ? AND file_id = ?", job.CloudStorageID, old.File.FileID).Delete(&model.OrganizeMediaFile{}).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.OrganizeManagedStrm{}).Where("cloud_storage_id = ? AND file_id = ?", job.CloudStorageID, old.File.FileID).Update("retired", true).Error; err != nil {
					return err
				}
				for _, binding := range old.Bindings {
					if err := tx.Where("file_path = ? AND pickcode = ?", binding.CacheKey, old.File.PickCode).Delete(&model.PickcodeCache{}).Error; err != nil {
						return err
					}
				}
				if err := retireUpgradeCaches(tx, job.CloudStorageID, old.File.FileID, old.File.PickCode); err != nil {
					return err
				}
			}
			if restore {
				for _, subtitle := range op.Subtitles {
					if err := tx.Model(&model.OrganizeManagedStrm{}).Where("cloud_storage_id = ? AND file_id = ?", job.CloudStorageID, subtitle.FileID).Update("retired", true).Error; err != nil {
						return err
					}
				}
			} else if err := putUpgradeSubtitleGuards(tx, job, op); err != nil {
				return err
			}
			for _, file := range install {
				row := model.OrganizeMediaFile{UserID: job.UserID, CloudDirectoryID: job.CloudDirectoryID, CloudStorageID: job.CloudStorageID, FileID: file.File.FileID, MediaKey: file.MediaKey, CloudPath: file.CloudPath, PickCode: file.File.PickCode, Size: file.File.Size, FactsJSON: planJSON(file.Facts)}
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "cloud_storage_id"}, {Name: "file_id"}}, DoUpdates: clause.AssignmentColumns([]string{"media_key", "cloud_path", "pick_code", "size", "facts_json", "updated_at"})}).Create(&row).Error; err != nil {
					return err
				}
				if err := putUpgradeGuards(tx, job, file.File, file.CloudPath, file.Bindings); err != nil {
					return err
				}
				for _, binding := range file.Bindings {
					if _, _, err := model.UpsertPickcodeCache(tx, binding.CacheKey, file.File.PickCode); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func (h *Service) rollbackUpgradeJob(job *model.OrganizeUpgradeJob, payload *organizeUpgradePayload, storage *model.CloudStorage, client *driver.Pan115Client) error {
	if !payload.Prepared {
		job.Status = "rolled_back"
		return h.finishUpgradeJob(job, payload, "failed")
	}
	for i := len(payload.Operations) - 1; i >= 0; i-- {
		op := payload.Operations[i]
		if err := h.checkpointUpgradeJob(job, payload); err != nil {
			return err
		}
		for _, item := range append([]Organize115ItemResult{op.Item}, op.Subtitles...) {
			file := service.Web115File{FileID: item.FileID, Name: item.RenameTo, Size: item.FileSize, PickCode: item.PickCode, IsFile: true}
			if err := h.ensureUpgradeFile(client, file, item.TargetDirID, op.SourceID, item.FileName); err != nil {
				return err
			}
		}
		for _, old := range op.Item.Upgrade.Existing {
			for _, oldFile := range append([]service.Web115File{old.File}, old.Sidecars...) {
				if err := h.ensureUpgradeFile(client, oldFile, op.RecoveryID[oldFile.ParentID], oldFile.ParentID, oldFile.Name); err != nil {
					return err
				}
			}
		}
		for _, change := range op.Item.Upgrade.Changes {
			if err := service.ApplyUpgradeFile(change, true); err != nil {
				return err
			}
		}
	}
	if err := h.commitUpgradeRecords(job, payload, true); err != nil {
		return err
	}
	job.Status, job.Error = "rolled_back", ""
	return h.finishUpgradeJob(job, payload, "failed")
}

func (h *Service) finishUpgradeJob(job *model.OrganizeUpgradeJob, payload *organizeUpgradePayload, planStatus string) error {
	return h.store().Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(job).Where("run_token = ? AND status = ?", job.RunToken, "running").Updates(map[string]any{"status": job.Status, "error": job.Error, "refresh_error": job.RefreshError, "payload_json": planJSON(payload), "lease_until": nil})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("洗版执行租约已变化，不能结束任务")
		}
		if err := tx.Model(&model.OrganizePlan{}).Where("id = ?", job.ID).Update("status", planStatus).Error; err != nil {
			return err
		}
		return tx.Where("cloud_storage_id = ? AND plan_id = ?", job.CloudStorageID, job.ID).Delete(&model.OrganizeStorageLock{}).Error
	})
}

func decodeUpgradePayload(job model.OrganizeUpgradeJob) (organizeUpgradePayload, error) {
	var payload organizeUpgradePayload
	err := json.Unmarshal([]byte(job.PayloadJSON), &payload)
	if err == nil && (len(payload.Operations) == 0 || payload.Directory.UserID != job.UserID || strings.TrimSpace(job.ID) == "") {
		err = errors.New("洗版恢复记录不完整")
	}
	return payload, err
}
