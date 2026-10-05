package service

import (
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// PlaybackInfoCallback 播放信息回调函数类型
type PlaybackInfoCallback func(itemID string, cfg *config.Config) error

// PersistentTaskQueue 持久化任务队列
type PersistentTaskQueue struct {
	db               *gorm.DB
	cfg              *config.Config
	log              *logger.Logger
	stopCh           chan struct{}
	wg               sync.WaitGroup
	running          bool
	mu               sync.Mutex
	executing        atomic.Bool    // worker 与执行协程共享的状态
	taskWg           sync.WaitGroup // 已领取任务必须在 Stop 返回前完成
	addMu            sync.Mutex
	cleanupWg        sync.WaitGroup       // 清理任务的WaitGroup
	playbackCallback PlaybackInfoCallback // 播放信息回调函数
}

var (
	taskQueue *PersistentTaskQueue
	queueOnce sync.Once
)

// NewPersistentTaskQueue 创建持久化任务队列
func NewPersistentTaskQueue(cfg *config.Config, log *logger.Logger, callback PlaybackInfoCallback) *PersistentTaskQueue {
	queueOnce.Do(func() {
		db := database.GetDB()
		if db == nil {
			log.Error("获取数据库连接失败")
			return
		}

		taskQueue = &PersistentTaskQueue{
			db:               db,
			cfg:              cfg,
			log:              log,
			stopCh:           make(chan struct{}),
			playbackCallback: callback,
		}

		// 启动时重置处理中的任务为待处理状态
		db.Model(&model.MediaTask{}).Where("status = ?", model.TaskStatusProcessing).Update("status", model.TaskStatusPending)

		taskQueue.Start()
	})
	return taskQueue
}

// GetTaskQueue 获取任务队列单例
func GetTaskQueue() *PersistentTaskQueue {
	return taskQueue
}

// AddTask 添加任务
func (q *PersistentTaskQueue) AddTask(itemID string) error {
	q.addMu.Lock()
	defer q.addMu.Unlock()
	// 检查是否已存在未完成的任务
	var count int64
	err := q.db.Model(&model.MediaTask{}).Where("item_id = ? AND status IN (?)",
		itemID, []model.TaskStatus{model.TaskStatusPending, model.TaskStatusProcessing}).Count(&count).Error
	if err != nil {
		return err
	}

	if count > 0 {
		q.log.Infof("任务已存在，跳过添加: ItemID=%s", itemID)
		return nil
	}

	task := &model.MediaTask{
		ItemID: itemID,
		Status: model.TaskStatusPending,
	}

	if err := q.db.Create(task).Error; err != nil {
		q.log.Errorf("添加任务失败: %v", err)
		return err
	}

	q.log.Infof("任务已添加到队列: ItemID=%s, TaskID=%d", itemID, task.ID)
	return nil
}

// Start 启动任务处理器
func (q *PersistentTaskQueue) Start() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.running {
		return
	}

	q.running = true
	q.stopCh = make(chan struct{})

	// 启动任务处理器
	q.wg.Add(1)
	go q.worker()

	// 启动定期清理器
	q.cleanupWg.Add(1)
	go q.cleanupWorker()

	q.log.Info("任务队列处理器已启动")
}

// Stop 停止任务处理器
func (q *PersistentTaskQueue) Stop() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.running {
		return
	}

	q.running = false
	close(q.stopCh)

	// 等待任务处理器和清理器都停止
	q.wg.Wait()
	q.cleanupWg.Wait()
	// worker 已退出，之后不会再向 taskWg 添加任务。
	q.taskWg.Wait()

	q.log.Info("任务队列处理器已停止")
}

// worker 任务处理器
func (q *PersistentTaskQueue) worker() {
	defer q.wg.Done()

	var lastProcessTime time.Time
	ticker := time.NewTicker(1 * time.Second) // 每1秒检查一次
	defer ticker.Stop()

	for {
		select {
		case <-q.stopCh:
			return
		case <-ticker.C:
			// 检查是否距离上次处理已经过了10秒，并且没有任务正在执行
			if time.Since(lastProcessTime) >= 10*time.Second && !q.executing.Load() {
				if q.processNextTask() {
					lastProcessTime = time.Now() // 更新最后处理时间
				}
			}
		}
	}
}

// processNextTask 处理下一个任务，返回是否成功处理了任务
func (q *PersistentTaskQueue) processNextTask() bool {
	var task model.MediaTask

	// 使用事务获取并更新任务状态
	err := q.db.Transaction(func(tx *gorm.DB) error {
		// 获取最早的待处理任务
		if err := tx.Where("status = ?", model.TaskStatusPending).
			Order("created_at ASC").First(&task).Error; err != nil {
			return err // 没有待处理任务
		}

		// 更新为处理中状态
		now := time.Now()
		return tx.Model(&task).Updates(model.MediaTask{
			Status:    model.TaskStatusProcessing,
			StartedAt: &now,
		}).Error
	})

	if err != nil {
		if err != gorm.ErrRecordNotFound {
			q.log.Errorf("获取任务失败: %v", err)
		}
		return false // 没有任务处理
	}

	// 设置执行状态
	q.executing.Store(true)

	// 处理任务（异步处理，不阻塞）
	q.taskWg.Add(1)
	go func() {
		defer q.taskWg.Done()
		q.executeTask(&task)
	}()

	return true // 成功开始处理任务
}

// executeTask 执行任务
func (q *PersistentTaskQueue) executeTask(task *model.MediaTask) {
	// 确保在函数退出时重置执行状态
	defer func() {
		q.executing.Store(false)
	}()

	q.log.Infof("🔄 开始处理媒体任务: TaskID=%d, ItemID=%s", task.ID, task.ItemID)
	q.log.Debugf("媒体任务排队耗时: TaskID=%d queue_wait_ms=%d", task.ID, time.Since(task.CreatedAt).Milliseconds())

	// 记录任务开始时间
	startTime := time.Now()

	// 调用播放信息处理函数
	err := q.callGETPlaybackInfo(task.ItemID)

	// 计算执行时间
	executionTime := time.Since(startTime)
	q.log.Infof("⏱️ 任务执行时间: TaskID=%d, ItemID=%s, 耗时: %v", task.ID, task.ItemID, executionTime)

	now := time.Now()
	if err != nil {
		// 任务失败，增加重试次数
		task.Retries++
		q.log.Warnf("❌ 任务执行失败: TaskID=%d, ItemID=%s, 重试次数: %d, 错误: %v",
			task.ID, task.ItemID, task.Retries, err)

		if task.Retries >= 3 {
			// 超过重试次数，标记为失败
			q.db.Model(task).Updates(model.MediaTask{
				Status:      model.TaskStatusFailed,
				CompletedAt: &now,
				ErrorMsg:    err.Error(),
			})
			q.log.Errorf("💀 任务失败(超过重试次数): TaskID=%d, ItemID=%s, 总重试次数: %d, 最终错误: %v",
				task.ID, task.ItemID, task.Retries, err)
		} else {
			// 重新标记为待处理，稍后重试
			q.db.Model(task).Updates(model.MediaTask{
				Status:   model.TaskStatusPending,
				ErrorMsg: err.Error(),
				Retries:  task.Retries,
			})
			q.log.Infof("🔄 任务将重试: TaskID=%d, ItemID=%s, 当前重试次数: %d/%d",
				task.ID, task.ItemID, task.Retries, 3)
		}
	} else {
		// 任务成功
		q.db.Model(task).Updates(model.MediaTask{
			Status:      model.TaskStatusCompleted,
			CompletedAt: &now,
		})
		q.log.Infof("✅ 任务完成: TaskID=%d, ItemID=%s, 执行时间: %v",
			task.ID, task.ItemID, executionTime)
	}
}

// callGETPlaybackInfo 调用 GETPlaybackInfo（需要实现具体逻辑）
func (q *PersistentTaskQueue) callGETPlaybackInfo(itemID string) error {
	q.log.Infof("📺 开始处理媒体播放信息: ItemID=%s", itemID)

	if q.playbackCallback != nil {
		err := q.playbackCallback(itemID, q.cfg)
		if err != nil {
			q.log.Errorf("❌ 处理媒体播放信息失败: ItemID=%s, 错误: %v", itemID, err)
			return err
		}
		q.log.Infof("✅ 媒体播放信息处理成功: ItemID=%s", itemID)
		return nil
	}

	q.log.Warnf("⚠️ 未设置播放信息回调函数: ItemID=%s", itemID)
	return nil // 如果没有回调函数，返回 nil
}

// GetQueueStatus 获取队列状态
func (q *PersistentTaskQueue) GetQueueStatus() (map[string]int64, error) {
	status := map[string]int64{
		string(model.TaskStatusPending): 0, string(model.TaskStatusProcessing): 0,
		string(model.TaskStatusCompleted): 0, string(model.TaskStatusFailed): 0,
	}
	var rows []struct {
		Status model.TaskStatus
		Count  int64
	}
	if err := q.db.Model(&model.MediaTask{}).
		Select("status, COUNT(*) AS count").Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, known := status[string(row.Status)]; known {
			status[string(row.Status)] = row.Count
		}
	}
	return status, nil
}

// cleanupWorker 定期清理已完成的任务
func (q *PersistentTaskQueue) cleanupWorker() {
	defer q.cleanupWg.Done()

	// 每1小时执行一次清理
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// 启动时先执行一次清理
	q.cleanupOldTasks()

	for {
		select {
		case <-q.stopCh:
			return
		case <-ticker.C:
			q.cleanupOldTasks()
		}
	}
}

// cleanupOldTasks 清理旧的已完成任务
func (q *PersistentTaskQueue) cleanupOldTasks() {
	// 删除7天前已完成的任务
	cutoffTime := time.Now().AddDate(0, 0, -7)

	// 清理已完成的任务
	result := q.db.Where("status = ? AND completed_at < ?", model.TaskStatusCompleted, cutoffTime).Delete(&model.MediaTask{})
	if result.Error != nil {
		q.log.Errorf("清理已完成任务失败: %v", result.Error)
		return
	}

	if result.RowsAffected > 0 {
		q.log.Infof("清理了 %d 个已完成的任务（超过7天）", result.RowsAffected)
	}

	// 清理30天前失败的任务
	oldFailureCutoff := time.Now().AddDate(0, 0, -30)
	result = q.db.Where("status = ? AND completed_at < ?", model.TaskStatusFailed, oldFailureCutoff).Delete(&model.MediaTask{})
	if result.Error != nil {
		q.log.Errorf("清理失败任务失败: %v", result.Error)
		return
	}

	if result.RowsAffected > 0 {
		q.log.Infof("清理了 %d 个失败的任务（超过30天）", result.RowsAffected)
	}
}

// ManualCleanup 手动触发清理（可用于测试或管理）
func (q *PersistentTaskQueue) ManualCleanup() {
	q.log.Info("手动触发任务清理")
	q.cleanupOldTasks()
}
