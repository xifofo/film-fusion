package service

import (
	"context"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/downloader"
	"fmt"
	"os"
	"path/filepath"
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

// Download115Service 115Open专用下载队列服务
type Download115Service struct {
	logger    *logger.Logger
	db        *gorm.DB
	sdk115    *sdk115.Client
	config    *Download115Config
	workers   chan struct{} // 用于控制并发数的信号量
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	isRunning bool
	mu        sync.RWMutex
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

	return &Download115Service{
		logger:  log,
		db:      database.DB,
		sdk115:  sdk115.New(),
		config:  config,
		workers: make(chan struct{}, config.MaxConcurrent),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// AddDownloadTask 添加115Open下载任务到队列
func (s *Download115Service) AddDownloadTask(cloudStorageID uint, pickCode, savePath string) error {
	// 检查任务是否已存在
	var existing model.Download115Queue
	if err := s.db.Where("pick_code = ?", pickCode).First(&existing).Error; err == nil {
		s.logger.Warnf("115Open下载任务已存在: PickCode=%s", pickCode)
		return fmt.Errorf("下载任务已存在")
	}

	// 验证云存储配置是否存在
	var cloudStorage model.CloudStorage
	if err := s.db.First(&cloudStorage, cloudStorageID).Error; err != nil {
		s.logger.Errorf("云存储配置不存在: ID=%d", cloudStorageID)
		return fmt.Errorf("云存储配置不存在")
	}

	// 创建新的下载任务
	task := &model.Download115Queue{
		CloudStorageID: cloudStorageID,
		PickCode:       pickCode,
		SavePath:       savePath,
		MaxRetryCount:  3, // 默认最大重试3次
		Status:         model.QueueStatusPending,
	}

	if err := s.db.Create(task).Error; err != nil {
		s.logger.Errorf("添加115Open下载任务失败: %v", err)
		return err
	}

	s.logger.Infof("添加115Open下载任务成功: CloudStorageID=%d, PickCode=%s, SavePath=%s", cloudStorageID, pickCode, savePath)
	return nil
}

// StartWorkers 启动下载工作者
func (s *Download115Service) StartWorkers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isRunning {
		s.logger.Warn("115Open下载服务已经在运行中")
		return
	}

	s.isRunning = true
	s.logger.Infof("启动115Open下载服务，最大并发数: %d", s.config.MaxConcurrent)

	go s.processQueue()
}

// StopWorkers 停止下载工作者
func (s *Download115Service) StopWorkers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isRunning {
		return
	}

	s.logger.Info("正在停止115Open下载服务...")
	s.cancel()
	s.wg.Wait()
	s.isRunning = false
	s.logger.Info("115Open下载服务已停止")
}

// processQueue 处理下载队列
func (s *Download115Service) processQueue() {
	ticker := time.NewTicker(time.Second * 5) // 每5秒检查一次队列
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.processPendingTasks()
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
			s.wg.Add(1)
			go s.downloadTask(task)
		default:
			// 没有可用的工作者槽位，跳过
			return
		}
	}
}

// downloadTask 执行单个下载任务
func (s *Download115Service) downloadTask(task model.Download115Queue) {
	defer func() {
		<-s.workers // 释放工作者槽位
		s.wg.Done()
	}()

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

	// 设置任务状态为下载中
	task.SetDownloading()
	if err := s.db.Save(&task).Error; err != nil {
		s.logger.Errorf("更新任务状态失败: %v", err)
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

	// 下载成功，设置完成状态并删除记录
	task.SetCompleted()
	if err := s.db.Delete(&task).Error; err != nil {
		s.logger.Errorf("删除115Open下载任务记录失败: %v", err)
	}

	s.logger.Infof("115Open下载任务完成并已删除记录: PickCode=%s", task.PickCode)
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
	} else {
		s.logger.Warnf("任务失败，将重试: PickCode=%s, RetryCount=%d/%d, Error=%v",
			task.PickCode, task.RetryCount, task.MaxRetryCount, err)
	}

	// 保存更新后的任务状态
	if dbErr := s.db.Save(task).Error; dbErr != nil {
		s.logger.Errorf("保存任务错误状态失败: %v", dbErr)
	}
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

// ClearQueue 清空队列（用于调试）
func (s *Download115Service) ClearQueue() error {
	return s.db.Where("1 = 1").Delete(&model.Download115Queue{}).Error
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
	result := s.db.Where("status = ?", model.QueueStatusFailed).Delete(&model.Download115Queue{})
	if result.Error != nil {
		return 0, result.Error
	}

	s.logger.Infof("清理了 %d 个失败的下载任务", result.RowsAffected)
	return result.RowsAffected, nil
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

	s.logger.Infof("重置任务状态成功: PickCode=%s", pickCode)
	return nil
}
