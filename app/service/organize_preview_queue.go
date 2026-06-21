package service

import (
	"errors"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	defaultOrganizePreviewIntervalSeconds = 45
	minOrganizePreviewIntervalSeconds     = 10
	maxOrganizePreviewIntervalSeconds     = 300
	defaultOrganizePreviewMaxDepth        = 1
	minOrganizePreviewMaxDepth            = 1
	maxOrganizePreviewMaxDepth            = 5
)

type OrganizePreviewTaskInput struct {
	UserID                   uint
	CloudDirectoryID         uint
	CloudStorageID           uint
	FolderID                 string
	ParentFolderID           string
	FolderName               string
	FolderPath               string
	Depth                    int
	MaxDepth                 int
	IntervalSeconds          int
	FilenameRegexEnabled     bool
	FilenameRegexPattern     string
	FilenameRegexReplacement string
}

type OrganizePreviewProcessResult struct {
	ResultJSON string
	Total      int
	Children   []OrganizePreviewTaskInput
}

type OrganizePreviewProcessor func(task model.OrganizePreviewTask) (OrganizePreviewProcessResult, error)

type OrganizePreviewQueue struct {
	db                 *gorm.DB
	log                *logger.Logger
	processor          OrganizePreviewProcessor
	stopCh             chan struct{}
	wg                 sync.WaitGroup
	mu                 sync.Mutex
	running            bool
	lastFolderAccessAt time.Time
}

func NewOrganizePreviewQueue(log *logger.Logger, processor OrganizePreviewProcessor) *OrganizePreviewQueue {
	return &OrganizePreviewQueue{
		db:        database.GetDB(),
		log:       log,
		processor: processor,
		stopCh:    make(chan struct{}),
	}
}

func ClampOrganizePreviewIntervalSeconds(value int) int {
	if value <= 0 {
		return defaultOrganizePreviewIntervalSeconds
	}
	if value < minOrganizePreviewIntervalSeconds {
		return minOrganizePreviewIntervalSeconds
	}
	if value > maxOrganizePreviewIntervalSeconds {
		return maxOrganizePreviewIntervalSeconds
	}
	return value
}

func ClampOrganizePreviewMaxDepth(value int) int {
	if value < defaultOrganizePreviewMaxDepth {
		return defaultOrganizePreviewMaxDepth
	}
	if value > maxOrganizePreviewMaxDepth {
		return maxOrganizePreviewMaxDepth
	}
	return value
}

func (q *OrganizePreviewQueue) Start() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.running {
		return
	}
	if q.db == nil {
		if q.log != nil {
			q.log.Error("预整理队列启动失败：数据库未初始化")
		}
		return
	}

	q.stopCh = make(chan struct{})
	q.running = true
	q.db.Model(&model.OrganizePreviewTask{}).
		Where("status = ?", model.OrganizePreviewStatusProcessing).
		Updates(map[string]any{
			"status":     model.OrganizePreviewStatusPending,
			"started_at": nil,
		})

	q.wg.Add(1)
	go q.worker()

	if q.log != nil {
		q.log.Info("预整理队列已启动")
	}
}

func (q *OrganizePreviewQueue) Stop() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if !q.running {
		q.mu.Unlock()
		return
	}
	q.running = false
	close(q.stopCh)
	q.mu.Unlock()

	q.wg.Wait()
	if q.log != nil {
		q.log.Info("预整理队列已停止")
	}
}

func (q *OrganizePreviewQueue) Enqueue(inputs []OrganizePreviewTaskInput) ([]model.OrganizePreviewTask, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("预整理队列未初始化")
	}
	out := make([]model.OrganizePreviewTask, 0, len(inputs))
	for _, input := range inputs {
		folderID := strings.TrimSpace(input.FolderID)
		if folderID == "" {
			continue
		}

		task := model.OrganizePreviewTask{}
		err := q.db.Where(
			"user_id = ? AND cloud_directory_id = ? AND folder_id = ?",
			input.UserID,
			input.CloudDirectoryID,
			folderID,
		).First(&task).Error
		if err == nil {
			if task.Status != model.OrganizePreviewStatusProcessing {
				if err := q.db.Model(&task).Updates(map[string]any{
					"cloud_storage_id":           input.CloudStorageID,
					"parent_folder_id":           strings.TrimSpace(input.ParentFolderID),
					"folder_name":                strings.TrimSpace(input.FolderName),
					"folder_path":                strings.TrimSpace(input.FolderPath),
					"depth":                      input.Depth,
					"max_depth":                  ClampOrganizePreviewMaxDepth(input.MaxDepth),
					"status":                     model.OrganizePreviewStatusPending,
					"total":                      0,
					"result_json":                "",
					"error":                      "",
					"interval_seconds":           ClampOrganizePreviewIntervalSeconds(input.IntervalSeconds),
					"filename_regex_enabled":     input.FilenameRegexEnabled,
					"filename_regex_pattern":     strings.TrimSpace(input.FilenameRegexPattern),
					"filename_regex_replacement": input.FilenameRegexReplacement,
					"started_at":                 nil,
					"completed_at":               nil,
				}).Error; err != nil {
					return nil, err
				}
				if err := q.db.First(&task, task.ID).Error; err != nil {
					return nil, err
				}
			}
			out = append(out, task)
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}

		task = model.OrganizePreviewTask{
			UserID:                   input.UserID,
			CloudDirectoryID:         input.CloudDirectoryID,
			CloudStorageID:           input.CloudStorageID,
			FolderID:                 folderID,
			ParentFolderID:           strings.TrimSpace(input.ParentFolderID),
			FolderName:               strings.TrimSpace(input.FolderName),
			FolderPath:               strings.TrimSpace(input.FolderPath),
			Depth:                    input.Depth,
			MaxDepth:                 ClampOrganizePreviewMaxDepth(input.MaxDepth),
			Status:                   model.OrganizePreviewStatusPending,
			IntervalSeconds:          ClampOrganizePreviewIntervalSeconds(input.IntervalSeconds),
			FilenameRegexEnabled:     input.FilenameRegexEnabled,
			FilenameRegexPattern:     strings.TrimSpace(input.FilenameRegexPattern),
			FilenameRegexReplacement: input.FilenameRegexReplacement,
		}
		if err := q.db.Create(&task).Error; err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}

func (q *OrganizePreviewQueue) List(userID uint, cloudDirectoryID uint, status string) ([]model.OrganizePreviewTask, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("预整理队列未初始化")
	}
	query := q.db.Where("user_id = ?", userID)
	if cloudDirectoryID > 0 {
		query = query.Where("cloud_directory_id = ?", cloudDirectoryID)
	}
	if strings.TrimSpace(status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(status))
	}
	var tasks []model.OrganizePreviewTask
	err := query.
		Order("CASE status WHEN 'processing' THEN 0 WHEN 'pending' THEN 1 WHEN 'completed' THEN 2 ELSE 3 END").
		Order("updated_at DESC").
		Find(&tasks).Error
	return tasks, err
}

func (q *OrganizePreviewQueue) Get(userID uint, id uint) (model.OrganizePreviewTask, error) {
	var task model.OrganizePreviewTask
	if q == nil || q.db == nil {
		return task, errors.New("预整理队列未初始化")
	}
	err := q.db.Where("id = ? AND user_id = ?", id, userID).First(&task).Error
	return task, err
}

func (q *OrganizePreviewQueue) Requeue(userID uint, id uint) (model.OrganizePreviewTask, error) {
	task, err := q.Get(userID, id)
	if err != nil {
		return task, err
	}
	if task.Status == model.OrganizePreviewStatusProcessing {
		return task, errors.New("任务正在处理中，不能重复加入队列")
	}
	if err := q.db.Model(&task).Updates(map[string]any{
		"status":       model.OrganizePreviewStatusPending,
		"total":        0,
		"result_json":  "",
		"error":        "",
		"started_at":   nil,
		"completed_at": nil,
	}).Error; err != nil {
		return task, err
	}
	return q.Get(userID, id)
}

func (q *OrganizePreviewQueue) Delete(userID uint, id uint) error {
	if q == nil || q.db == nil {
		return errors.New("预整理队列未初始化")
	}
	return q.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.OrganizePreviewTask{}).Error
}

func (q *OrganizePreviewQueue) worker() {
	defer q.wg.Done()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-q.stopCh:
			return
		case <-ticker.C:
			q.processNext()
		}
	}
}

func (q *OrganizePreviewQueue) processNext() bool {
	if q.processor == nil || q.db == nil {
		return false
	}

	var task model.OrganizePreviewTask
	if err := q.db.Where("status = ?", model.OrganizePreviewStatusPending).
		Order("created_at ASC").
		First(&task).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) && q.log != nil {
			q.log.Errorf("读取预整理任务失败: %v", err)
		}
		return false
	}

	interval := time.Duration(ClampOrganizePreviewIntervalSeconds(task.IntervalSeconds)) * time.Second
	if !q.lastFolderAccessAt.IsZero() && time.Since(q.lastFolderAccessAt) < interval {
		return false
	}

	now := time.Now()
	res := q.db.Model(&model.OrganizePreviewTask{}).
		Where("id = ? AND status = ?", task.ID, model.OrganizePreviewStatusPending).
		Updates(map[string]any{
			"status":       model.OrganizePreviewStatusProcessing,
			"started_at":   &now,
			"completed_at": nil,
			"error":        "",
		})
	if res.Error != nil {
		if q.log != nil {
			q.log.Errorf("标记预整理任务处理中失败: %v", res.Error)
		}
		return false
	}
	if res.RowsAffected == 0 {
		return false
	}

	q.lastFolderAccessAt = time.Now()
	if q.log != nil {
		q.log.Infof("开始预整理目录: task_id=%d folder_id=%s folder_path=%s", task.ID, task.FolderID, task.FolderPath)
	}

	result, err := q.processor(task)
	completedAt := time.Now()
	updates := map[string]any{
		"total":        result.Total,
		"result_json":  result.ResultJSON,
		"completed_at": &completedAt,
	}
	if err != nil {
		updates["status"] = model.OrganizePreviewStatusFailed
		updates["error"] = err.Error()
	} else {
		updates["status"] = model.OrganizePreviewStatusCompleted
		updates["error"] = ""
	}
	if updateErr := q.db.Model(&model.OrganizePreviewTask{}).
		Where("id = ?", task.ID).
		Updates(updates).Error; updateErr != nil && q.log != nil {
		q.log.Errorf("更新预整理任务结果失败: task_id=%d err=%v", task.ID, updateErr)
	}
	if err == nil && len(result.Children) > 0 {
		if _, enqueueErr := q.Enqueue(result.Children); enqueueErr != nil && q.log != nil {
			q.log.Errorf("加入子目录预整理任务失败: task_id=%d err=%v", task.ID, enqueueErr)
		}
	}
	if q.log != nil {
		if err != nil {
			q.log.Warnf("预整理目录失败: task_id=%d folder_id=%s err=%v", task.ID, task.FolderID, err)
		} else {
			q.log.Infof("预整理目录完成: task_id=%d folder_id=%s total=%d", task.ID, task.FolderID, result.Total)
		}
	}
	return true
}
