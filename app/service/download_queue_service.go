package service

import (
	"context"
	"errors"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/downloader"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"gorm.io/gorm"
)

// Download115Config 115Open下载配置
type Download115Config struct {
	MaxConcurrent int           // 最大并发下载数
	RetryLimit    int           // 最大重试次数
	RetryDelay    time.Duration // 重试延迟
}

// DownloadQueueStats 下载队列各状态数量。
type DownloadQueueStats struct {
	Total       int64 `json:"total"`
	Pending     int64 `json:"pending"`
	Downloading int64 `json:"downloading"`
	Failed      int64 `json:"failed"`
}

var (
	ErrDownloadTaskNotFound  = errors.New("下载任务不存在")
	ErrDownloadTaskRunning   = errors.New("下载中的任务不能移除")
	ErrDownloadTaskNotFailed = errors.New("只有失败任务可以重试")
)

// Download115Service 115Open专用下载队列服务
type Download115Service struct {
	logger             *logger.Logger
	db                 *gorm.DB
	sdk115             *sdk115.Client
	config             *Download115Config
	workers            chan struct{} // 用于控制并发数的信号量
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	isRunning          bool
	mu                 sync.RWMutex
	queueMutationMu    sync.Mutex
	deleteSourceFolder sourceFolderDeleteFunc
}

// NewDownload115Service 创建新的115Open下载服务
func NewDownload115Service(log *logger.Logger, maxConcurrent int) *Download115Service {
	ctx, cancel := context.WithCancel(context.Background())

	config := &Download115Config{
		MaxConcurrent: maxConcurrent,
		RetryLimit:    3,
		RetryDelay:    time.Second * 5,
	}

	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 1 // 默认 1 个并发
	}

	service := &Download115Service{
		logger:  log,
		db:      database.DB,
		sdk115:  sdk115.New(),
		config:  config,
		workers: make(chan struct{}, config.MaxConcurrent),
		ctx:     ctx,
		cancel:  cancel,
	}
	service.deleteSourceFolder = service.deleteSourceFolderWithWeb115
	return service
}

// AddDownloadTask 添加115Open下载任务到队列
func (s *Download115Service) AddDownloadTask(cloudStorageID uint, pickCode, savePath string) error {
	return s.addDownloadTask(cloudStorageID, pickCode, savePath, nil)
}

// AddDownloadTaskForSourceFolderDeletion 添加与源文件夹延迟删除任务绑定的字幕下载。
func (s *Download115Service) AddDownloadTaskForSourceFolderDeletion(cloudStorageID uint, pickCode, savePath string, deletionID uint) error {
	if deletionID == 0 {
		return errors.New("源文件夹延迟删除任务无效")
	}
	return s.addDownloadTask(cloudStorageID, pickCode, savePath, &deletionID)
}

func (s *Download115Service) addDownloadTask(cloudStorageID uint, pickCode, savePath string, deletionID *uint) error {
	if s == nil || s.db == nil {
		return errors.New("下载服务未初始化")
	}
	pickCode = strings.TrimSpace(pickCode)
	savePath = strings.TrimSpace(savePath)
	if cloudStorageID == 0 || pickCode == "" || savePath == "" {
		return errors.New("下载任务参数不完整")
	}
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing model.Download115Queue
		if err := tx.Where("pick_code = ?", pickCode).First(&existing).Error; err == nil {
			return fmt.Errorf("下载任务已存在")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var cloudStorage model.CloudStorage
		if err := tx.Select("id").First(&cloudStorage, cloudStorageID).Error; err != nil {
			return fmt.Errorf("云存储配置不存在")
		}

		if deletionID != nil {
			var deletion model.OrganizeSourceFolderDeletionTask
			if err := tx.Where("id = ? AND cloud_storage_id = ? AND status = ?", *deletionID, cloudStorageID, model.SourceFolderDeletionStatusCollecting).
				First(&deletion).Error; err != nil {
				return fmt.Errorf("源文件夹延迟删除任务不可用: %w", err)
			}
		}

		task := &model.Download115Queue{
			CloudStorageID:         cloudStorageID,
			SourceFolderDeletionID: deletionID,
			PickCode:               pickCode,
			SavePath:               savePath,
			MaxRetryCount:          3,
			Status:                 model.QueueStatusPending,
		}
		if err := tx.Create(task).Error; err != nil {
			return err
		}

		if deletionID != nil {
			updated := tx.Model(&model.OrganizeSourceFolderDeletionTask{}).
				Where("id = ? AND status = ?", *deletionID, model.SourceFolderDeletionStatusCollecting).
				UpdateColumn("total_downloads", gorm.Expr("total_downloads + 1"))
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errors.New("源文件夹延迟删除任务计数失败")
			}
		}
		return nil
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Errorf("添加115Open下载任务失败: PickCode=%s Error=%v", pickCode, err)
		}
		return err
	}

	if s.logger != nil {
		s.logger.Infof("添加115Open下载任务成功: CloudStorageID=%d, PickCode=%s, SavePath=%s", cloudStorageID, pickCode, savePath)
	}
	return nil
}

// StartWorkers 启动下载工作者
func (s *Download115Service) StartWorkers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isRunning {
		if s.logger != nil {
			s.logger.Warn("115Open下载服务已经在运行中")
		}
		return
	}

	if err := s.recoverInterruptedQueueState(); err != nil {
		if s.logger != nil {
			s.logger.Errorf("恢复中断的115Open下载和源文件夹删除任务失败: %v", err)
		}
		return
	}
	s.isRunning = true
	if s.logger != nil {
		s.logger.Infof("启动115Open下载服务，最大并发数: %d", s.config.MaxConcurrent)
	}

	go s.processQueue()
}

// StopWorkers 停止下载工作者
func (s *Download115Service) StopWorkers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isRunning {
		return
	}

	if s.logger != nil {
		s.logger.Info("正在停止115Open下载服务...")
	}
	s.cancel()
	s.wg.Wait()
	s.isRunning = false
	if s.logger != nil {
		s.logger.Info("115Open下载服务已停止")
	}
}

// processQueue 处理下载队列
func (s *Download115Service) processQueue() {
	ticker := time.NewTicker(time.Second * 5) // 每5秒检查一次队列
	defer ticker.Stop()
	s.processPendingTasks()
	s.processReadySourceFolderDeletions()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.processPendingTasks()
			s.processReadySourceFolderDeletions()
		}
	}
}

// processPendingTasks 处理待下载任务
func (s *Download115Service) processPendingTasks() {
	// 获取等待中的任务，按创建时间排序，并预加载云存储配置
	var tasks []model.Download115Queue
	if err := s.db.Preload("CloudStorage").
		Where("status = ? OR (status = ? AND retry_count < max_retry_count)",
			model.QueueStatusPending, model.QueueStatusFailed).
		Order("created_at ASC").
		Limit(s.config.MaxConcurrent).
		Find(&tasks).Error; err != nil {
		s.logger.Errorf("获取115Open待下载任务失败: %v", err)
		return
	}

	for _, task := range tasks {
		// 检查任务是否可以重试
		if !task.CanRetry() {
			s.logger.Warnf("任务已达最大重试次数，跳过: PickCode=%s, RetryCount=%d", task.PickCode, task.RetryCount)
			continue
		}

		select {
		case s.workers <- struct{}{}: // 获取工作者槽位
		default:
			// 没有可用的工作者槽位，跳过
			return
		}

		claimed, err := s.claimQueueTask(task.ID)
		if err != nil {
			<-s.workers
			s.logger.Errorf("领取115Open下载任务失败: ID=%d, Error=%v", task.ID, err)
			continue
		}
		if !claimed {
			// 任务可能刚被页面移除，或已经由另一个工作者领取。
			<-s.workers
			continue
		}

		task.Status = model.QueueStatusDownloading
		s.wg.Add(1)
		go s.downloadTask(task)
	}
}

// claimQueueTask 原子地把可执行任务标记为下载中，避免页面移除和工作者领取发生竞态。
func (s *Download115Service) claimQueueTask(id uint) (bool, error) {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	result := s.db.Model(&model.Download115Queue{}).
		Where("id = ? AND retry_count < max_retry_count AND status IN ?", id,
			[]string{model.QueueStatusPending, model.QueueStatusFailed}).
		Update("status", model.QueueStatusDownloading)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// downloadTask 执行单个下载任务
func (s *Download115Service) downloadTask(task model.Download115Queue) {
	defer func() {
		<-s.workers // 释放工作者槽位
		s.wg.Done()
	}()

	taskStart := time.Now()

	s.logger.Infof("开始115Open下载任务: CloudStorageID=%d, PickCode=%s, SavePath=%s, 重试次数: %d/%d",
		task.CloudStorageID, task.PickCode, task.SavePath, task.RetryCount, task.MaxRetryCount)

	// 检查云存储配置是否可用
	if task.CloudStorage == nil {
		s.logger.Errorf("下载任务缺少云存储配置: PickCode=%s", task.PickCode)
		s.handleTaskError(&task, fmt.Errorf("缺少云存储配置"))
		return
	}

	if !task.CloudStorage.IsAvailable() {
		s.logger.Errorf("云存储配置不可用: StorageID=%d, Status=%s", task.CloudStorageID, task.CloudStorage.Status)
		s.handleTaskError(&task, fmt.Errorf("云存储配置不可用: %s", task.CloudStorage.Status))
		return
	}

	// 确保保存目录存在
	if err := os.MkdirAll(filepath.Dir(task.SavePath), 0755); err != nil {
		s.logger.Errorf("创建保存目录失败: %v", err)
		s.handleTaskError(&task, err)
		return
	}

	// 执行实际下载
	if err := s.performDownload(&task); err != nil {
		s.logger.Errorf("115Open下载任务失败: PickCode=%s, Error=%v", task.PickCode, err)
		s.handleTaskError(&task, err)
		return
	}

	// 下载成功后，在同一事务中累计延迟删除进度并移除队列记录。
	deletionID, err := s.completeDownloadQueueTask(task.ID)
	if err != nil {
		if s.logger != nil {
			s.logger.Errorf("保存115Open下载完成状态失败: PickCode=%s Error=%v", task.PickCode, err)
		}
		s.queueMutationMu.Lock()
		_ = s.db.Model(&model.Download115Queue{}).Where("id = ?", task.ID).Updates(map[string]any{
			"status":     model.QueueStatusPending,
			"last_error": "下载已完成，但保存完成状态失败: " + err.Error(),
		}).Error
		s.queueMutationMu.Unlock()
		return
	}

	var size int64
	if fi, err := os.Stat(task.SavePath); err == nil {
		size = fi.Size()
	}
	WriteOrganizeLog(s.logger, OrganizeLogEntry{
		Action: model.OrganizeActionFileDownload, Status: model.OrganizeStatusSuccess,
		Trigger: "download_worker",
		Target:  task.SavePath, CloudStorageID: task.CloudStorageID, PickCode: task.PickCode,
		DurationMS: time.Since(taskStart).Milliseconds(), SizeBytes: size,
		Message: "下载完成",
	})

	if s.logger != nil {
		s.logger.Infof("115Open下载任务完成并已删除记录: PickCode=%s", task.PickCode)
	}
	if deletionID != nil {
		if _, err := s.tryDeleteReadySourceFolder(*deletionID); err != nil && s.logger != nil {
			s.logger.Warnf("字幕已下载，但延迟删除源文件夹暂未完成: DeleteTaskID=%d Error=%v", *deletionID, err)
		}
	}
}

// performDownload 执行实际下载
func (s *Download115Service) performDownload(task *model.Download115Queue) error {
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
	s.logger.Debugf("正在通过115Open下载文件: PickCode=%s", task.PickCode)

	// 设置115 SDK的访问令牌
	s.sdk115.SetAccessToken(task.CloudStorage.AccessToken)

	// 获取下载URL
	downURLResp, err := s.sdk115.DownURL(context.Background(), task.PickCode, userAgent)
	if err != nil {
		s.logger.Errorf("115Open获取下载URL失败: %v", err)
		return err
	}

	s.logger.Debugf("115Open下载响应: %+v", downURLResp)

	// 获取第一个下载URL
	var firstKey string
	for key := range downURLResp {
		firstKey = key
		break
	}

	u, ok := downURLResp[firstKey]
	if !ok {
		return fmt.Errorf("下载响应中未找到 PickCode: %s", task.PickCode)
	}

	s.logger.Infof("开始下载文件: URL=%s, SavePath=%s", u.URL.URL, task.SavePath)

	// 使用简单的下载方法
	startTime := time.Now()

	err = downloader.DownloadFromURLSimple(u.URL.URL, userAgent, task.SavePath)
	if err != nil {
		return fmt.Errorf("文件下载失败: %v", err)
	}

	// 记录下载完成信息
	duration := time.Since(startTime)

	// 获取文件大小并验证文件完整性
	fileInfo, err := os.Stat(task.SavePath)
	if err != nil {
		return fmt.Errorf("获取下载文件信息失败: %v", err)
	}

	fileSize := fileInfo.Size()
	if fileSize == 0 {
		return fmt.Errorf("下载的文件为空: %s", task.SavePath)
	}

	speed := float64(fileSize) / duration.Seconds() / 1024 / 1024 // MB/s

	s.logger.Infof("文件下载完成: %s, 大小: %d bytes (%.2f MB), 耗时: %.2fs, 速度: %.2f MB/s",
		task.SavePath, fileSize, float64(fileSize)/(1024*1024), duration.Seconds(), speed)

	return nil
}

// handleTaskError 处理任务错误，增加重试次数或标记为失败
func (s *Download115Service) handleTaskError(task *model.Download115Queue, err error) {
	task.IncrementRetry()
	task.SetError(err)

	if task.Status == model.QueueStatusFailed {
		s.logger.Errorf("任务已达最大重试次数，标记为失败: PickCode=%s, Error=%v", task.PickCode, err)
		WriteOrganizeLog(s.logger, OrganizeLogEntry{
			Action: model.OrganizeActionFileDownload, Status: model.OrganizeStatusFailed,
			Trigger: "download_worker",
			Target:  task.SavePath, CloudStorageID: task.CloudStorageID, PickCode: task.PickCode,
			Error: err.Error(), Message: fmt.Sprintf("重试达上限 %d/%d", task.RetryCount, task.MaxRetryCount),
		})
	} else {
		s.logger.Warnf("任务失败，将重试: PickCode=%s, RetryCount=%d/%d, Error=%v",
			task.PickCode, task.RetryCount, task.MaxRetryCount, err)
	}

	// 保存更新后的任务状态
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	if dbErr := s.db.Save(task).Error; dbErr != nil {
		s.logger.Errorf("保存任务错误状态失败: %v", dbErr)
		return
	}
	s.noteSourceFolderDownloadFailure(task.ID, err)
}

// GetQueueCount 获取当前队列中的任务数量
func (s *Download115Service) GetQueueCount() (int64, error) {
	var count int64
	if err := s.db.Model(&model.Download115Queue{}).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// GetQueueTasks 获取队列任务列表（用于调试）
func (s *Download115Service) GetQueueTasks(limit, offset int) ([]model.Download115Queue, int64, error) {
	var tasks []model.Download115Queue
	var total int64

	if err := s.db.Model(&model.Download115Queue{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := s.db.Preload("CloudStorage").Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&tasks).Error; err != nil {
		return nil, 0, err
	}

	return tasks, total, nil
}

// ListQueueTasks 获取供管理页面展示的下载队列任务。
func (s *Download115Service) ListQueueTasks(status, search, createdAtOrder string, limit, offset int) ([]model.Download115Queue, int64, error) {
	var tasks []model.Download115Queue
	var total int64

	query := s.db.Model(&model.Download115Queue{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + search + "%"
		query = query.Where("save_path LIKE ? OR pick_code LIKE ?", like, like)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	order := "ASC"
	if strings.EqualFold(createdAtOrder, "desc") {
		order = "DESC"
	}
	if err := query.Preload("CloudStorage").
		Order("created_at " + order).
		Order("id " + order).
		Limit(limit).
		Offset(offset).
		Find(&tasks).Error; err != nil {
		return nil, 0, err
	}

	return tasks, total, nil
}

// GetQueueStats 获取整个下载队列的状态统计。
func (s *Download115Service) GetQueueStats() (DownloadQueueStats, error) {
	type statusCount struct {
		Status string
		Count  int64
	}

	var rows []statusCount
	if err := s.db.Model(&model.Download115Queue{}).
		Select("status, COUNT(*) AS count").
		Group("status").
		Scan(&rows).Error; err != nil {
		return DownloadQueueStats{}, err
	}

	stats := DownloadQueueStats{}
	for _, row := range rows {
		stats.Total += row.Count
		switch row.Status {
		case model.QueueStatusPending:
			stats.Pending = row.Count
		case model.QueueStatusDownloading:
			stats.Downloading = row.Count
		case model.QueueStatusFailed:
			stats.Failed = row.Count
		}
	}

	return stats, nil
}

// ClearQueue 清空队列（用于调试）
func (s *Download115Service) ClearQueue() error {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var tasks []model.Download115Queue
		if err := tx.Select("id", "source_folder_deletion_id").Find(&tasks).Error; err != nil {
			return err
		}
		deletionIDs := make([]uint, 0, len(tasks))
		for _, task := range tasks {
			if task.SourceFolderDeletionID != nil {
				deletionIDs = append(deletionIDs, *task.SourceFolderDeletionID)
			}
		}
		if err := tx.Where("1 = 1").Delete(&model.Download115Queue{}).Error; err != nil {
			return err
		}
		return cancelSourceFolderDeletionsForRemovedDownloads(tx, deletionIDs)
	})
}

// UpdateConcurrency 更新并发数
func (s *Download115Service) UpdateConcurrency(maxConcurrent int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	s.config.MaxConcurrent = maxConcurrent
	s.workers = make(chan struct{}, maxConcurrent)
	s.logger.Infof("更新115Open最大并发数为: %d", maxConcurrent)
}

// ClearFailedTasks 清理失败的任务
func (s *Download115Service) ClearFailedTasks() (int64, error) {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	var deletedCount int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var tasks []model.Download115Queue
		if err := tx.Select("id", "source_folder_deletion_id").Where("status = ?", model.QueueStatusFailed).Find(&tasks).Error; err != nil {
			return err
		}
		deletionIDs := make([]uint, 0, len(tasks))
		for _, task := range tasks {
			if task.SourceFolderDeletionID != nil {
				deletionIDs = append(deletionIDs, *task.SourceFolderDeletionID)
			}
		}
		result := tx.Where("status = ?", model.QueueStatusFailed).Delete(&model.Download115Queue{})
		if result.Error != nil {
			return result.Error
		}
		deletedCount = result.RowsAffected
		return cancelSourceFolderDeletionsForRemovedDownloads(tx, deletionIDs)
	})
	if err != nil {
		return 0, err
	}

	if s.logger != nil {
		s.logger.Infof("清理了 %d 个失败的下载任务", deletedCount)
	}
	return deletedCount, nil
}

// RetryFailedTaskByID 将失败任务重置为等待状态。
func (s *Download115Service) RetryFailedTaskByID(id uint) error {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	var task model.Download115Queue
	if err := s.db.First(&task, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDownloadTaskNotFound
		}
		return err
	}
	if task.Status != model.QueueStatusFailed {
		return ErrDownloadTaskNotFailed
	}

	result := s.db.Model(&task).
		Where("status = ?", model.QueueStatusFailed).
		Updates(map[string]any{
			"retry_count": 0,
			"status":      model.QueueStatusPending,
			"last_error":  "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		if err := s.db.First(&model.Download115Queue{}, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDownloadTaskNotFound
		} else if err != nil {
			return err
		}
		return ErrDownloadTaskNotFailed
	}
	if s.logger != nil {
		s.logger.Infof("下载任务已重新入队: ID=%d, PickCode=%s", task.ID, task.PickCode)
	}
	if task.SourceFolderDeletionID != nil {
		_ = s.db.Model(&model.OrganizeSourceFolderDeletionTask{}).
			Where("id = ?", *task.SourceFolderDeletionID).
			Update("last_error", "").Error
	}
	return nil
}

// RemoveQueueTask 移除等待中或失败的任务；运行中的任务必须由下载工作者自行完成。
func (s *Download115Service) RemoveQueueTask(id uint) error {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	var task model.Download115Queue
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&task, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDownloadTaskNotFound
			}
			return err
		}
		if task.Status == model.QueueStatusDownloading ||
			(task.Status != model.QueueStatusPending && task.Status != model.QueueStatusFailed) {
			return ErrDownloadTaskRunning
		}

		result := tx.Where("id = ? AND status IN ?", id, []string{model.QueueStatusPending, model.QueueStatusFailed}).
			Delete(&model.Download115Queue{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrDownloadTaskRunning
		}
		if task.SourceFolderDeletionID == nil {
			return nil
		}
		return cancelSourceFolderDeletionsForRemovedDownloads(tx, []uint{*task.SourceFolderDeletionID})
	})
	if err != nil {
		return err
	}
	if s.logger != nil {
		s.logger.Infof("下载任务已移除: ID=%d, PickCode=%s", task.ID, task.PickCode)
	}
	return nil
}

// GetTasksByStatus 根据状态获取任务列表
func (s *Download115Service) GetTasksByStatus(status string, limit, offset int) ([]model.Download115Queue, int64, error) {
	var tasks []model.Download115Queue
	var total int64

	query := s.db.Model(&model.Download115Queue{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("CloudStorage").Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&tasks).Error; err != nil {
		return nil, 0, err
	}

	return tasks, total, nil
}

// RetryFailedTask 重试指定的失败任务
func (s *Download115Service) RetryFailedTask(pickCode string) error {
	s.queueMutationMu.Lock()
	defer s.queueMutationMu.Unlock()
	var task model.Download115Queue
	if err := s.db.Where("pick_code = ? AND status = ?", pickCode, model.QueueStatusFailed).First(&task).Error; err != nil {
		return fmt.Errorf("未找到失败的任务: %s", pickCode)
	}

	// 重置重试次数和状态
	task.RetryCount = 0
	task.Status = model.QueueStatusPending
	task.LastError = ""

	if err := s.db.Save(&task).Error; err != nil {
		return fmt.Errorf("重置任务状态失败: %v", err)
	}
	if task.SourceFolderDeletionID != nil {
		_ = s.db.Model(&model.OrganizeSourceFolderDeletionTask{}).
			Where("id = ?", *task.SourceFolderDeletionID).
			Update("last_error", "").Error
	}

	s.logger.Infof("重置任务状态成功: PickCode=%s", pickCode)
	return nil
}
