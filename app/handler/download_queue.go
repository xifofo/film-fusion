package handler

import (
	"errors"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// DownloadQueueHandler 提供 115 下载队列的管理页面查询接口。
type DownloadQueueHandler struct {
	downloadService *service.Download115Service
}

type downloadQueueStorageSummary struct {
	ID          uint   `json:"id"`
	StorageName string `json:"storage_name"`
	StorageType string `json:"storage_type"`
}

type downloadQueueTaskResponse struct {
	ID             uint                         `json:"id"`
	CloudStorageID uint                         `json:"cloud_storage_id"`
	PickCode       string                       `json:"pick_code"`
	SavePath       string                       `json:"save_path"`
	RetryCount     int                          `json:"retry_count"`
	MaxRetryCount  int                          `json:"max_retry_count"`
	LastError      string                       `json:"last_error"`
	Status         string                       `json:"status"`
	CreatedAt      time.Time                    `json:"created_at"`
	CloudStorage   *downloadQueueStorageSummary `json:"cloud_storage,omitempty"`
}

func NewDownloadQueueHandler(downloadService *service.Download115Service) *DownloadQueueHandler {
	return &DownloadQueueHandler{downloadService: downloadService}
}

// List 分页查询下载中、等待中和失败的队列任务。
func (h *DownloadQueueHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 200 {
		size = 20
	}

	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != model.QueueStatusPending && status != model.QueueStatusDownloading && status != model.QueueStatusFailed {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "无效的下载队列状态", Data: nil})
		return
	}
	createdAtOrder := strings.ToLower(strings.TrimSpace(c.DefaultQuery("created_at_order", "asc")))
	if createdAtOrder != "asc" && createdAtOrder != "desc" {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "无效的入队时间排序方式", Data: nil})
		return
	}

	tasks, total, err := h.downloadService.ListQueueTasks(status, c.Query("search"), createdAtOrder, size, (page-1)*size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "查询下载队列失败: " + err.Error(), Data: nil})
		return
	}

	stats, err := h.downloadService.GetQueueStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "查询下载队列统计失败: " + err.Error(), Data: nil})
		return
	}

	list := make([]downloadQueueTaskResponse, 0, len(tasks))
	for _, task := range tasks {
		item := downloadQueueTaskResponse{
			ID:             task.ID,
			CloudStorageID: task.CloudStorageID,
			PickCode:       task.PickCode,
			SavePath:       task.SavePath,
			RetryCount:     task.RetryCount,
			MaxRetryCount:  task.MaxRetryCount,
			LastError:      task.LastError,
			Status:         task.Status,
			CreatedAt:      task.CreatedAt,
		}
		if task.CloudStorage != nil {
			item.CloudStorage = &downloadQueueStorageSummary{
				ID:          task.CloudStorage.ID,
				StorageName: task.CloudStorage.StorageName,
				StorageType: task.CloudStorage.StorageType,
			}
		}
		list = append(list, item)
	}

	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: "获取下载队列成功",
		Data: gin.H{
			"list":  list,
			"total": total,
			"page":  page,
			"size":  size,
			"stats": stats,
		},
	})
}

// Retry 将失败任务重新放回等待队列。
func (h *DownloadQueueHandler) Retry(c *gin.Context) {
	id, ok := h.taskID(c)
	if !ok {
		return
	}
	if err := h.downloadService.RetryFailedTaskByID(id); err != nil {
		h.taskError(c, err)
		return
	}
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: "任务已重新加入等待队列",
		Data:    gin.H{"id": id, "status": model.QueueStatusPending},
	})
}

// Remove 移除等待中或失败的任务。
func (h *DownloadQueueHandler) Remove(c *gin.Context) {
	id, ok := h.taskID(c)
	if !ok {
		return
	}
	if err := h.downloadService.RemoveQueueTask(id); err != nil {
		h.taskError(c, err)
		return
	}
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: "任务已移除",
		Data:    gin.H{"id": id},
	})
}

// ClearFailed 批量清理失败任务。
func (h *DownloadQueueHandler) ClearFailed(c *gin.Context) {
	deletedCount, err := h.downloadService.ClearFailedTasks()
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "清理失败任务失败: " + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: "失败任务已清理",
		Data:    gin.H{"deleted_count": deletedCount},
	})
}

func (h *DownloadQueueHandler) taskID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "无效的任务 ID", Data: nil})
		return 0, false
	}
	return uint(id), true
}

func (h *DownloadQueueHandler) taskError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDownloadTaskNotFound):
		c.JSON(http.StatusNotFound, ApiResponse{Code: 404, Message: err.Error(), Data: nil})
	case errors.Is(err, service.ErrDownloadTaskRunning), errors.Is(err, service.ErrDownloadTaskNotFailed):
		c.JSON(http.StatusConflict, ApiResponse{Code: 409, Message: err.Error(), Data: nil})
	default:
		c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "操作下载任务失败: " + err.Error(), Data: nil})
	}
}
