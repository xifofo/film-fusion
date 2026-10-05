package organize

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/service"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func (h *Service) buildOrganize115CookieResult(userID uint, req Organize115CookieRequest) (Organize115CookieResult, error) {
	if err := h.requestContext().Err(); err != nil {
		return Organize115CookieResult{}, err
	}
	if req.PlanID != "" {
		return h.applyOrganizePlan(userID, req)
	}
	if !req.DryRun {
		return Organize115CookieResult{}, errors.New("缺少已确认的整理计划；历史预览不兼容，请重新预览")
	}
	// Capture destination conditions before any remote reads; changes during preview invalidate it.
	var dir model.CloudDirectory
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", req.CloudDirectoryID, userID).First(&dir).Error; err != nil {
		return Organize115CookieResult{}, errors.New("云盘目录不存在或无权限")
	}
	if req.RecognitionSource == "" {
		req.RecognitionSource = dir.RecognitionSource
	}
	frozen, snapshot, err := h.moviePilotSvc.FreezeMediaPipeline(req.RecognitionSource)
	if err != nil {
		return Organize115CookieResult{}, err
	}
	worker := *h
	worker.moviePilotSvc = frozen
	if tmdb := frozen.PipelineTMDB(); tmdb != nil {
		worker.tmdbSvc = tmdb
	}
	req.RecognitionSource = snapshot.Source
	result, err := worker.computeOrganize115CookieResult(userID, req)
	if err != nil {
		return result, err
	}
	if err := h.requestContext().Err(); err != nil {
		return result, err
	}
	result.Pipeline = snapshot
	if err := worker.prepareUpgradePreview(dir, &result); err != nil {
		return result, err
	}
	if err := h.requestContext().Err(); err != nil {
		return result, err
	}
	token := make([]byte, 24)
	if _, err = rand.Read(token); err != nil {
		return result, err
	}
	result.PlanID = hex.EncodeToString(token)
	saved := model.OrganizePlan{ID: result.PlanID, UserID: userID, CloudDirectoryID: req.CloudDirectoryID, RequestJSON: planJSON(req), ResultJSON: planJSON(result), DirectoryJSON: organizeDirectoryConditions(dir), Status: "ready"}
	if err = h.store().Create(&saved).Error; err != nil {
		return result, fmt.Errorf("保存预览计划失败: %w", err)
	}
	return result, nil
}

func organizeDirectoryConditions(dir model.CloudDirectory) string {
	// Dates, credentials and unrelated user metadata do not affect the plan.
	dir.CreatedAt = model.CloudDirectory{}.CreatedAt
	dir.UpdatedAt = dir.CreatedAt
	dir.CloudStorage = nil
	dir.User = nil
	return planJSON(dir)
}

func validateOrganizePlanRequest(req, saved Organize115CookieRequest) error {
	if req.CloudDirectoryID != saved.CloudDirectoryID {
		return errors.New("预览目录已改变，请重新预览")
	}
	if req.RecognitionSource != "" && req.RecognitionSource != saved.RecognitionSource {
		return errors.New("预览引擎已改变，请重新预览")
	}
	if req.MediaType != saved.MediaType || req.Category != saved.Category || req.FilenameRegexEnabled != saved.FilenameRegexEnabled || req.FilenameRegexPattern != saved.FilenameRegexPattern || req.FilenameRegexReplacement != saved.FilenameRegexReplacement {
		return errors.New("整理选项已改变，请重新预览")
	}
	if req.BestVersionEnabled != nil && (saved.BestVersionEnabled == nil || *req.BestVersionEnabled != *saved.BestVersionEnabled) {
		return errors.New("版本选择选项已改变，请重新预览")
	}
	allowed := map[string]bool{}
	for _, id := range normalizeFolderIDs(saved.FolderIDs, saved.FolderID) {
		allowed[id] = true
	}
	for _, id := range normalizeFolderIDs(req.FolderIDs, req.FolderID) {
		if !allowed[id] {
			return errors.New("源目录不在预览计划内，请重新预览")
		}
	}
	return nil
}

func selectOrganizePlan(result Organize115CookieResult, req Organize115CookieRequest) (Organize115CookieResult, error) {
	selected := normalizeFileIDSet(req.FileIDs)
	found := map[string]bool{}
	targets := map[string]string{}
	groups := make([]Organize115CookieGroup, 0, len(result.Groups))
	total := 0
	folders := normalizeFileIDSet(normalizeFolderIDs(req.FolderIDs, req.FolderID))
	blocked := map[string]string{}
	for _, group := range result.Groups {
		for _, item := range group.Items {
			if item.Upgrade != nil && item.Upgrade.Action != "add" && item.Upgrade.Action != "replace" {
				blocked[item.FileID] = item.Upgrade.Reason
			}
		}
	}
	for _, group := range result.Groups {
		if len(folders) > 0 {
			if _, ok := folders[group.FolderID]; !ok {
				continue
			}
		}
		items := []Organize115ItemResult{}
		for _, item := range group.Items {
			if len(selected) > 0 {
				_, keep := selected[item.FileID]
				_, videoKeep := selected[item.MatchedVideoFileID]
				if !keep && !(item.IsSubtitle && videoKeep) {
					continue
				}
			}
			found[item.FileID] = true
			if reason, stop := blocked[item.FileID]; stop {
				if _, explicitlySelected := selected[item.FileID]; explicitlySelected {
					return result, fmt.Errorf("%s 不可执行: %s", item.FileName, reason)
				}
				continue
			}
			if _, stop := blocked[item.MatchedVideoFileID]; item.IsSubtitle && stop {
				continue
			}
			if item.Error != "" || item.TargetPath == "" {
				return result, fmt.Errorf("文件 %s 的预览失败，不能执行: %s", item.FileName, item.Error)
			}
			if prior, ok := targets[item.TargetPath]; ok && prior != item.FileID {
				return result, fmt.Errorf("多个文件指向同一目标 %s，请重新预览", item.TargetPath)
			}
			targets[item.TargetPath] = item.FileID
			items = append(items, item)
			if !item.IsSubtitle {
				total++
			}
		}
		if len(items) > 0 {
			group.Items = items
			group.Error = ""
			group.Total = 0
			for _, item := range items {
				if !item.IsSubtitle {
					group.Total++
				}
			}
			groups = append(groups, group)
		}
	}
	for id := range selected {
		if !found[id] {
			return result, errors.New("选中文件不在预览计划内，请重新预览")
		}
	}
	if total == 0 && result.UpgradePolicy == nil {
		return result, errors.New("预览计划没有可执行文件")
	}
	result.Groups = groups
	result.Total = total
	result.Items = nil
	for _, group := range groups {
		result.Items = append(result.Items, group.Items...)
	}
	return result, nil
}

// All selected sources are checked before any cloud or disk mutation. Target names,
// source identity, STRM paths and contents come exclusively from the stored plan.
func (h *Service) validateOrganizePlanSources(client *driver.Pan115Client, result Organize115CookieResult) error {
	for _, group := range result.Groups {
		found := map[string]service.Web115File{}
		manifest := []organizeSourceIdentity{}
		for offset := 0; ; offset += 1150 {
			page, err := h.web115Svc.GetFilesWithClient(client, group.FolderID, offset, 1150)
			if err != nil {
				return err
			}
			for _, file := range page.Items {
				manifest = append(manifest, sourceIdentity(file))
				if file.IsFile {
					found[file.FileID] = file
				}
			}
			if (page.Total > 0 && int64(offset+1150) >= page.Total) || (page.Total <= 0 && len(page.Items) < 1150) {
				break
			}
		}
		if group.SourceManifest == "" || group.SourceManifest != organizeManifest(manifest) {
			return errors.New("源目录内容已改变，请重新预览")
		}
		for _, item := range group.Items {
			file, ok := found[item.FileID]
			if !ok || file.Name != item.FileName || file.Size != item.FileSize || file.PickCode != item.PickCode {
				return fmt.Errorf("源文件 %s 已变化，请重新预览", item.FileName)
			}
		}
	}
	return nil
}

func (h *Service) applyOrganizePlan(userID uint, req Organize115CookieRequest) (Organize115CookieResult, error) {
	var row model.OrganizePlan
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", req.PlanID, userID).First(&row).Error; err != nil {
		return Organize115CookieResult{}, errors.New("预览计划不存在或无权限，请重新预览")
	}
	if row.Status != "ready" {
		return Organize115CookieResult{}, errors.New("预览计划已执行或执行中，请重新预览")
	}
	var result Organize115CookieResult
	var saved Organize115CookieRequest
	if json.Unmarshal([]byte(row.ResultJSON), &result) != nil || json.Unmarshal([]byte(row.RequestJSON), &saved) != nil || result.Pipeline == nil || result.Pipeline.Schema != 1 || result.Pipeline.Source == "" {
		return result, errors.New("历史预览缺少引擎或配置快照，请重新预览")
	}
	if err := validateOrganizePlanRequest(req, saved); err != nil {
		return result, err
	}
	var dir model.CloudDirectory
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", row.CloudDirectoryID, userID).First(&dir).Error; err != nil {
		return result, errors.New("目录不可用，请重新预览")
	}
	if organizeDirectoryConditions(dir) != row.DirectoryJSON {
		return result, errors.New("媒体库根目录或整理条件已改变，请重新预览")
	}
	var err error
	result, err = selectOrganizePlan(result, req)
	if err != nil {
		return result, err
	}
	result.DryRun = req.DryRun
	if req.DryRun {
		return result, nil
	}
	if result.Total == 0 {
		return result, nil
	}
	var storage model.CloudStorage
	if err = h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", dir.CloudStorageID, userID).First(&storage).Error; err != nil {
		return result, err
	}
	client, err := h.organizeClient(storage.Cookie)
	if err != nil {
		return result, err
	}
	if err := h.acquireOrganizeStorageLock(storage.ID, row.ID); err != nil {
		return result, err
	}
	retainLock := false
	stopHeartbeat := keepOrganizeLeaseAlive(func() {
		h.store().Model(&model.OrganizeStorageLock{}).Where("cloud_storage_id = ? AND plan_id = ? AND lease_until IS NOT NULL", storage.ID, row.ID).Update("lease_until", time.Now().Add(15*time.Minute))
	})
	defer stopHeartbeat()
	defer func() {
		if !retainLock {
			h.releaseOrganizeStorageLock(storage.ID, row.ID)
		}
	}()
	if err = h.validateOrganizePlanSources(client, result); err != nil {
		return result, err
	}
	// Never reuse directory IDs from preview; re-resolve them against the current account.
	worker := *h
	worker.dirCache = service.NewWeb115DirCache(web115DirCacheTTL)
	h = &worker
	if result.UpgradePolicy != nil && result.UpgradePolicy.Enabled {
		claimed := h.store().Model(&model.OrganizePlan{}).Where("id = ? AND status = ?", row.ID, "ready").Update("status", "executing")
		if claimed.Error != nil {
			return result, claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return result, errors.New("计划已被其它请求执行")
		}
		result, err = h.startUpgradeJob(dir, storage, client, result)
		if result.UpgradeJob != nil {
			retainLock = result.UpgradeJob.Status != "completed" && result.UpgradeJob.Status != "rolled_back"
			if err != nil && len(result.Groups) > 0 {
				result.Groups[0].Error = fmt.Sprintf("洗版任务 %s 未完成，可在洗版记录中继续或恢复: %v", row.ID, err)
				return result, nil
			}
		} else if err != nil {
			h.store().Model(&model.OrganizePlan{}).Where("id = ?", row.ID).Update("status", "ready")
		}
		return result, err
	}
	claimed := h.store().Model(&model.OrganizePlan{}).Where("id = ? AND status = ?", row.ID, "ready").Update("status", "executing")
	if claimed.Error != nil {
		return result, claimed.Error
	}
	if claimed.RowsAffected != 1 {
		return result, errors.New("计划已被其它请求执行")
	}
	status := "failed"
	failed := false
	defer func() { h.store().Model(&model.OrganizePlan{}).Where("id = ?", row.ID).Update("status", status) }()
	result.Items = nil
	for i := range result.Groups {
		group := &result.Groups[i]
		var deleteID uint
		if req.DeleteSourceFolder {
			if h.download115Svc == nil {
				return result, errors.New("源目录删除服务未初始化")
			}
			deleteID, err = h.download115Svc.BeginSourceFolderDeletion(storage.ID, group.FolderID)
			if err != nil {
				return result, err
			}
		}
		runErr := func() error {
			group.DirDebug, err = h.resolveAndPrepareDirectories(&storage, client, &group.Items, false)
			if err != nil {
				return err
			}
			if err = h.batchRenameAndMove(client, group.Items); err != nil {
				return err
			}
			if err = h.enqueueSubtitleDownloads(dir, &storage, &group.Items, false, deleteID); err != nil {
				return err
			}
			if queueErr := firstOrganizeSubtitleQueueError(group.Items); queueErr != "" {
				return errors.New(queueErr)
			}
			h.populateLocalLibraryStatus(dir, &group.Items)
			for _, item := range group.Items {
				if item.IsSubtitle {
					continue
				}
				if item.StrmPath == "" {
					return errors.New("预览未包含 STRM 计划，请重新预览")
				}
				if err = os.MkdirAll(filepath.Dir(item.StrmPath), 0755); err != nil {
					return err
				}
				if err = os.WriteFile(item.StrmPath, []byte(item.StrmContent), 0777); err != nil {
					return err
				}
			}
			h.cachePickcodeCaches(dir, group.Items)
			return nil
		}()
		if runErr != nil {
			group.Error = runErr.Error()
			if deleteID != 0 {
				_ = h.download115Svc.CancelSourceFolderDeletion(deleteID)
			}
		} else if deleteID != 0 {
			schedule, e := h.download115Svc.ArmSourceFolderDeletion(deleteID)
			if schedule.Deleted {
				result.SourceFolderDeletedCount++
			}
			if schedule.Pending {
				result.SourceFolderDeletePendingCount++
			}
			if e != nil {
				result.SourceFolderDeleteErrors = append(result.SourceFolderDeleteErrors, e.Error())
			}
		}
		result.Items = append(result.Items, group.Items...)
		if runErr != nil {
			failed = true
		}
	}
	result.SourceFolderDeleted = result.SourceFolderDeletedCount > 0
	result.SourceFolderDeletePending = result.SourceFolderDeletePendingCount > 0
	if !failed {
		status = "executed"
	}
	return result, nil
}

// Prevent error-only preview results from being auto-confirmed by an automation.
func organizePreviewError(result Organize115CookieResult) error {
	for _, group := range result.Groups {
		if strings.TrimSpace(group.Error) != "" {
			return errors.New(group.Error)
		}
	}
	for _, item := range result.Items {
		if item.Error != "" {
			return errors.New(item.Error)
		}
	}
	return nil
}

func (h *Service) organizeClient(cookie string) (*driver.Pan115Client, error) {
	if h.newOrganizeClient != nil {
		return h.newOrganizeClient(cookie)
	}
	return h.web115Svc.NewClientWithContext(h.requestContext(), cookie, 30*time.Second)
}

type organizeSourceIdentity struct {
	ID, Name, PickCode string
	Size               int64
	IsFile             bool
}

func sourceIdentity(file service.Web115File) organizeSourceIdentity {
	return organizeSourceIdentity{file.FileID, file.Name, file.PickCode, file.Size, file.IsFile}
}

func organizeManifest(entries []organizeSourceIdentity) string {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ID == entries[j].ID {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].ID < entries[j].ID
	})
	sum := sha256.Sum256([]byte(planJSON(entries)))
	return hex.EncodeToString(sum[:])
}
