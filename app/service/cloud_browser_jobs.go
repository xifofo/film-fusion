package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"film-fusion/app/model"
)

type CloudBrowserOperationResult struct {
	JobID              string                       `json:"job_id,omitempty"`
	Name               string                       `json:"name,omitempty"`
	Status             string                       `json:"status"`
	Finished           bool                         `json:"finished"`
	Generated          int                          `json:"generated"`
	Skipped            int                          `json:"skipped"`
	Failed             int                          `json:"failed"`
	VisitedDirectories int                          `json:"visited_directories"`
	ScannedFiles       int                          `json:"scanned_files"`
	Errors             []string                     `json:"errors"`
	Results            []CloudBrowserSTRMItemResult `json:"results,omitempty"`
}

type cloudBrowserJob struct {
	userID, directoryID, storageID uint
	created                        time.Time
	cancel                         context.CancelFunc
	failure                        string
	result                         CloudBrowserOperationResult
}

func (s *CloudBrowserService) GenerateSTRM(ctx context.Context, userID, directoryID uint, req CloudBrowserGenerateRequest) (*CloudBrowserOperationResult, error) {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return nil, cloudMoveError(503, "服务正在关闭，请稍后重试")
	}
	selected, storage, configs, err := s.prepareSelection(ctx, userID, directoryID, req.SelectionToken)
	if err != nil {
		return nil, err
	}
	if !selected.Item.IsFile {
		return s.startJob(ctx, selected, storage)
	}
	// File selections carry authenticated provider metadata. Regenerating the
	// selected file requires only database/filesystem access and no 115 request.
	unlock, err := (&CloudFolderMoveService{db: s.db}).lock(ctx, selected.StorageID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	item := s.writeSTRM(ctx, configs, selected.Ancestors, selected.Item)
	result := &CloudBrowserOperationResult{Name: selected.Item.Name, Status: "completed", Finished: true, ScannedFiles: 1, Errors: []string{}, Results: []CloudBrowserSTRMItemResult{item}}
	cloudBrowserCountResult(result, item)
	return result, nil
}

// prepareSelection verifies a signed selection against the current database
// state. Generation and preview share these checks and their error messages.
func (s *CloudBrowserService) prepareSelection(ctx context.Context, userID, directoryID uint, token string) (cloudBrowserSelection, model.CloudStorage, []model.MediaDirectory, error) {
	var storage model.CloudStorage
	selected, err := s.parseSelection(token, userID, directoryID)
	if err != nil {
		return selected, storage, nil, err
	}
	directory, err := s.directory(ctx, userID, directoryID)
	if err != nil {
		return selected, storage, nil, err
	}
	if directory.CloudStorageID != selected.StorageID {
		return selected, storage, nil, cloudMoveError(409, "目录配置的存储账号已变更，请刷新后重试")
	}
	storage, err = s.storage(ctx, userID, selected.StorageID, selected.Method)
	if err != nil {
		return selected, storage, nil, err
	}
	configs, err := s.directories(ctx, userID, selected.StorageID)
	if err != nil {
		return selected, storage, nil, err
	}
	match, _, err := cloudBrowserMatchDirectory(configs, selected.Ancestors)
	if err != nil {
		return selected, storage, nil, cloudMoveError(409, "所选路径匹配多个重复目录配置，请修正后重试")
	}
	if match == nil {
		return selected, storage, nil, cloudMoveError(409, "所选路径未匹配当前账号的目录配置，无法生成 STRM")
	}
	if reason := cloudBrowserSTRMUnavailable(match, selected.Item); reason != "" {
		return selected, storage, nil, cloudMoveError(409, reason)
	}
	return selected, storage, configs, nil
}

func cloudBrowserCountResult(result *CloudBrowserOperationResult, item CloudBrowserSTRMItemResult) {
	switch item.Status {
	case "created", "updated", "unchanged":
		result.Generated++
	case "skipped":
		result.Skipped++
	default:
		result.Failed++
		if len(result.Errors) < 50 {
			result.Errors = append(result.Errors, item.Name+": "+item.Message)
		}
	}
}

func (s *CloudBrowserService) startJob(ctx context.Context, selected cloudBrowserSelection, storage model.CloudStorage) (*CloudBrowserOperationResult, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, cloudMoveError(500, "创建 STRM 任务失败")
	}
	id := hex.EncodeToString(nonce[:])
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil, cloudMoveError(503, "服务正在关闭，请稍后重试")
	}
	active := 0
	for _, job := range s.jobs {
		if !job.result.Finished {
			active++
			if job.storageID == storage.ID {
				s.mu.Unlock()
				return nil, cloudMoveError(409, "此账号已有 STRM 生成任务，请等待完成或停止后重试")
			}
		}
	}
	if active >= 2 {
		s.mu.Unlock()
		return nil, cloudMoveError(429, "已有 2 个 STRM 任务在运行，请稍后重试")
	}
	// Cooperate with organize/move to protect managed STRM decisions. The lease
	// is renewed locally while this explicit job runs and released on all exits.
	unlock, err := s.acquireBrowserJobLock(ctx, storage.ID, id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if len(s.jobs) >= 64 {
		oldID := ""
		var oldest time.Time
		for key, job := range s.jobs {
			if job.result.Finished && (oldID == "" || job.created.Before(oldest)) {
				oldID, oldest = key, job.created
			}
		}
		delete(s.jobs, oldID)
	}
	jobCtx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	job := &cloudBrowserJob{userID: selected.UserID, directoryID: selected.EntryDirectoryID, storageID: storage.ID, created: time.Now(), cancel: cancel, result: CloudBrowserOperationResult{JobID: id, Name: selected.Item.Name, Status: "queued", Errors: []string{}}}
	s.jobs[id] = job
	snapshot := copyCloudBrowserResult(job.result)
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer unlock()
		defer cancel()
		leaseCtx, leaseCancel := context.WithCancel(jobCtx)
		var leaseWG sync.WaitGroup
		leaseWG.Add(1)
		go func() { defer leaseWG.Done(); s.renewBrowserJobLock(leaseCtx, id, job) }()
		defer func() { leaseCancel(); leaseWG.Wait() }()
		s.runBrowserJob(jobCtx, selected, storage, job)
	}()
	return &snapshot, nil
}

func (s *CloudBrowserService) acquireBrowserJobLock(ctx context.Context, storageID uint, id string) (func(), error) {
	if err := s.db.WithContext(ctx).Where("cloud_storage_id = ? AND lease_until IS NOT NULL AND lease_until < ?", storageID, time.Now()).Delete(&model.OrganizeStorageLock{}).Error; err != nil {
		return nil, cloudMoveError(500, "检查账号任务锁失败")
	}
	until := time.Now().Add(5 * time.Minute)
	if err := s.db.WithContext(ctx).Create(&model.OrganizeStorageLock{CloudStorageID: storageID, PlanID: "browser-" + id, LeaseUntil: &until}).Error; err != nil {
		return nil, cloudMoveError(409, "此账号正在整理、移动或生成 STRM，请稍后重试")
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.db.WithContext(cleanup).Where("cloud_storage_id = ? AND plan_id = ?", storageID, "browser-"+id).Delete(&model.OrganizeStorageLock{})
	}, nil
}

func (s *CloudBrowserService) renewBrowserJobLock(ctx context.Context, id string, job *cloudBrowserJob) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			leaseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			result := s.db.WithContext(leaseCtx).Model(&model.OrganizeStorageLock{}).Where("cloud_storage_id = ? AND plan_id = ?", job.storageID, "browser-"+id).Update("lease_until", time.Now().Add(5*time.Minute))
			cancel()
			if result.Error != nil || result.RowsAffected != 1 {
				s.mu.Lock()
				job.failure = "账号任务锁续期失败，任务已停止；已生成文件保留"
				s.mu.Unlock()
				job.cancel()
				return
			}
		}
	}
}

func (s *CloudBrowserService) runBrowserJob(ctx context.Context, selected cloudBrowserSelection, storage model.CloudStorage, job *cloudBrowserJob) {
	s.mu.Lock()
	if ctx.Err() == nil {
		job.result.Status = "running"
	}
	s.mu.Unlock()
	var runErr error
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		job.result.Finished = true
		switch {
		case job.failure != "":
			job.result.Status = "failed"
			if len(job.result.Errors) < 50 {
				job.result.Errors = append(job.result.Errors, job.failure)
			}
		case ctx.Err() != nil:
			job.result.Status = "cancelled"
			if ctx.Err() == context.DeadlineExceeded {
				job.result.Status = "failed"
				job.result.Errors = append(job.result.Errors, "任务达到 24 小时上限，已停止；已生成文件保留")
			}
		case runErr != nil:
			job.result.Status = "failed"
			if len(job.result.Errors) < 50 {
				job.result.Errors = append(job.result.Errors, runErr.Error())
			}
		default:
			job.result.Status = "completed"
		}
	}()
	p, err := s.newProvider(storage, selected.Method)
	if err != nil {
		runErr = cloudBrowserProviderError(ctx, err)
		return
	}
	type frame struct {
		cid    string
		offset int
	}
	stack := []frame{{cid: selected.Item.FileID}}
	seen := map[string]bool{selected.Item.FileID: true}
	// Hash target paths to keep collision tracking bounded even for long names.
	targets := map[[32]byte]string{}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		page, listErr := p.List(ctx, current.cid, current.offset, CloudBrowserMaxPageSize)
		if listErr != nil {
			runErr = cloudBrowserProviderError(ctx, listErr)
			return
		}
		if page == nil || page.CID != current.cid || page.Offset != current.offset || !cloudBrowserValidAncestry(page.Breadcrumbs, current.cid) || len(page.Items) > CloudBrowserMaxPageSize || (page.HasMore && len(page.Items) == 0) {
			runErr = cloudMoveError(502, "115 返回的目录范围或分页无效，任务已停止")
			return
		}
		inside := false
		for _, ancestor := range page.Breadcrumbs {
			if ancestor.ID == selected.Item.FileID {
				inside = true
			}
		}
		if !inside {
			runErr = cloudMoveError(409, "目录已移出所选目录树，任务已停止，请重新浏览后生成")
			return
		}
		entry, configErr := s.directory(ctx, selected.UserID, selected.EntryDirectoryID)
		if configErr != nil || entry.CloudStorageID != storage.ID {
			runErr = cloudMoveError(409, "入口目录配置已删除或更换账号，任务已停止")
			return
		}
		configs, configErr := s.directories(ctx, selected.UserID, storage.ID)
		if configErr != nil {
			runErr = configErr
			return
		}
		s.mu.Lock()
		if current.offset == 0 {
			job.result.VisitedDirectories++
		}
		s.mu.Unlock()
		if page.HasMore {
			if current.offset+len(page.Items) > 10000000 {
				runErr = cloudMoveError(409, "单个目录超过分页处理上限，任务已停止；已生成文件保留")
				return
			}
			stack = append(stack, frame{cid: current.cid, offset: current.offset + len(page.Items)})
		}
		for _, item := range page.Items {
			if ctx.Err() != nil {
				return
			}
			if item.ParentID != current.cid || !cloudMoveID(item.FileID, false) {
				runErr = cloudMoveError(502, "115 返回了不属于当前目录的条目，任务已停止")
				return
			}
			if !item.IsFile {
				if seen[item.FileID] {
					runErr = cloudMoveError(409, "目录列表出现重复或循环，请重新浏览后生成")
					return
				}
				if len(page.Breadcrumbs) >= 64 || len(seen) >= 100000 || len(stack) >= 12800 {
					runErr = cloudMoveError(409, "目录树超过处理上限（64 层或 10 万目录），请选择较小目录；已生成文件保留")
					return
				}
				seen[item.FileID] = true
				stack = append(stack, frame{cid: item.FileID})
				continue
			}
			var result CloudBrowserSTRMItemResult
			target, targetErr := cloudBrowserSTRMTarget(configs, page.Breadcrumbs, item)
			key := sha256.Sum256([]byte(target))
			if firstID, exists := targets[key]; targetErr == nil && exists && firstID != item.FileID {
				result = CloudBrowserSTRMItemResult{FileID: item.FileID, Name: item.Name, Status: "failed", Message: "多个文件映射到同一 STRM 路径，已保留先生成的文件", LocalPath: target}
			} else {
				if targetErr == nil && len(targets) >= 100000 && targets[key] == "" {
					runErr = cloudMoveError(409, "任务达到 10 万个 STRM 文件上限，请选择较小目录；已生成文件保留")
					return
				}
				result = s.writeSTRM(ctx, configs, page.Breadcrumbs, item)
				if targetErr == nil && (result.Status == "created" || result.Status == "updated" || result.Status == "unchanged") {
					targets[key] = item.FileID
				}
			}
			s.mu.Lock()
			job.result.ScannedFiles++
			cloudBrowserCountResult(&job.result, result)
			s.mu.Unlock()
		}
	}
}

func copyCloudBrowserResult(result CloudBrowserOperationResult) CloudBrowserOperationResult {
	result.Errors = append([]string{}, result.Errors...)
	result.Results = append([]CloudBrowserSTRMItemResult(nil), result.Results...)
	return result
}

// ActiveJob restores the most recent job after closing/reloading the browser.
// Discovery is database/memory-only and never constructs a provider client.
func (s *CloudBrowserService) ActiveJob(ctx context.Context, userID, directoryID uint) (*CloudBrowserOperationResult, error) {
	if _, err := s.directory(ctx, userID, directoryID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *cloudBrowserJob
	for _, job := range s.jobs {
		if job.userID == userID && job.directoryID == directoryID && (latest == nil || job.created.After(latest.created)) {
			latest = job
		}
	}
	if latest == nil {
		return nil, nil
	}
	result := copyCloudBrowserResult(latest.result)
	return &result, nil
}

func (s *CloudBrowserService) JobStatus(ctx context.Context, userID, directoryID uint, jobID string) (*CloudBrowserOperationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.userID != userID || job.directoryID != directoryID {
		return nil, cloudMoveError(404, "未找到当前用户的 STRM 任务；服务重启后任务记录会清除")
	}
	result := copyCloudBrowserResult(job.result)
	return &result, nil
}

func (s *CloudBrowserService) CancelJob(ctx context.Context, userID, directoryID uint, jobID string) (*CloudBrowserOperationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.userID != userID || job.directoryID != directoryID {
		return nil, cloudMoveError(404, "未找到当前用户的 STRM 任务")
	}
	if !job.result.Finished {
		job.result.Status = "cancelling"
		job.cancel()
	}
	result := copyCloudBrowserResult(job.result)
	return &result, nil
}

// Stop is local-only and joins every explicit worker before the database closes.
func (s *CloudBrowserService) Stop() {
	s.mu.Lock()
	s.stopped = true
	for _, job := range s.jobs {
		if !job.result.Finished {
			job.cancel()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
}
