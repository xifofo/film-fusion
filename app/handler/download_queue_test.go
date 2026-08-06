package handler

import (
	"encoding/json"
	"errors"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDownloadQueueListFiltersAndHidesStorageCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file:download-queue-handler?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}, &model.Download115Queue{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	originalDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = originalDB })

	storage := model.CloudStorage{
		UserID:       1,
		StorageType:  model.StorageType115Open,
		StorageName:  "家庭 115",
		AccessToken:  "must-not-leak-access-token",
		RefreshToken: "must-not-leak-refresh-token",
		Cookie:       "must-not-leak-cookie",
		Status:       model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatalf("create storage: %v", err)
	}

	createdAt := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	tasks := []model.Download115Queue{
		{CloudStorageID: storage.ID, PickCode: "movie-pick", SavePath: "/media/Movies/Movie.mkv", Status: model.QueueStatusPending, MaxRetryCount: 3, CreatedAt: createdAt},
		{CloudStorageID: storage.ID, PickCode: "show-pick", SavePath: "/media/TV/Show.S01E01.mkv", Status: model.QueueStatusDownloading, MaxRetryCount: 3, CreatedAt: createdAt.Add(time.Minute)},
		{CloudStorageID: storage.ID, PickCode: "failed-pick", SavePath: "/media/TV/Failed.mkv", Status: model.QueueStatusFailed, RetryCount: 3, MaxRetryCount: 3, LastError: "download failed", CreatedAt: createdAt.Add(2 * time.Minute)},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	downloadService := service.NewDownload115Service(nil, 1)
	router := gin.New()
	router.GET("/api/download-queue", NewDownloadQueueHandler(downloadService).List)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/download-queue?status=pending&search=Movie&page=1&size=10", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, secret := range []string{storage.AccessToken, storage.RefreshToken, storage.Cookie} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("response leaked cloud storage credential %q: %s", secret, recorder.Body.String())
		}
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				PickCode     string `json:"pick_code"`
				CloudStorage struct {
					StorageName string `json:"storage_name"`
				} `json:"cloud_storage"`
			} `json:"list"`
			Total int64 `json:"total"`
			Stats struct {
				Total       int64 `json:"total"`
				Pending     int64 `json:"pending"`
				Downloading int64 `json:"downloading"`
				Failed      int64 `json:"failed"`
			} `json:"stats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.Total != 1 || len(response.Data.List) != 1 {
		t.Fatalf("unexpected filtered response: %+v", response)
	}
	if response.Data.List[0].PickCode != "movie-pick" || response.Data.List[0].CloudStorage.StorageName != storage.StorageName {
		t.Fatalf("unexpected task response: %+v", response.Data.List[0])
	}
	if response.Data.Stats.Total != 3 || response.Data.Stats.Pending != 1 || response.Data.Stats.Downloading != 1 || response.Data.Stats.Failed != 1 {
		t.Fatalf("unexpected stats: %+v", response.Data.Stats)
	}

	sortRecorder := httptest.NewRecorder()
	sortRequest := httptest.NewRequest(http.MethodGet, "/api/download-queue?created_at_order=desc&page=1&size=10", nil)
	router.ServeHTTP(sortRecorder, sortRequest)
	if sortRecorder.Code != http.StatusOK {
		t.Fatalf("sort status=%d body=%s", sortRecorder.Code, sortRecorder.Body.String())
	}
	var sortedResponse struct {
		Data struct {
			List []struct {
				PickCode string `json:"pick_code"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(sortRecorder.Body.Bytes(), &sortedResponse); err != nil {
		t.Fatalf("decode sorted response: %v", err)
	}
	wantOrder := []string{"failed-pick", "show-pick", "movie-pick"}
	for index, want := range wantOrder {
		if got := sortedResponse.Data.List[index].PickCode; got != want {
			t.Fatalf("sorted task[%d]=%q want %q", index, got, want)
		}
	}
}

func TestDownloadQueueListRejectsInvalidStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/download-queue", NewDownloadQueueHandler(nil).List)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/download-queue?status=completed", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDownloadQueueListRejectsInvalidCreatedAtOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/download-queue", NewDownloadQueueHandler(nil).List)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/download-queue?created_at_order=sideways", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDownloadQueueOperationsRespectTaskState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file:download-queue-operations?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}, &model.Download115Queue{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	originalDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = originalDB })

	storage := model.CloudStorage{
		UserID:      1,
		StorageType: model.StorageType115Open,
		StorageName: "操作测试 115",
		Status:      model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatalf("create storage: %v", err)
	}

	tasks := []model.Download115Queue{
		{CloudStorageID: storage.ID, PickCode: "pending-operation", SavePath: "/media/pending.mkv", Status: model.QueueStatusPending, MaxRetryCount: 3},
		{CloudStorageID: storage.ID, PickCode: "downloading-operation", SavePath: "/media/downloading.mkv", Status: model.QueueStatusDownloading, MaxRetryCount: 3},
		{CloudStorageID: storage.ID, PickCode: "retry-operation", SavePath: "/media/retry.mkv", Status: model.QueueStatusFailed, RetryCount: 3, MaxRetryCount: 3, LastError: "failed"},
		{CloudStorageID: storage.ID, PickCode: "clear-operation", SavePath: "/media/clear.mkv", Status: model.QueueStatusFailed, RetryCount: 3, MaxRetryCount: 3, LastError: "failed"},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	handler := NewDownloadQueueHandler(service.NewDownload115Service(nil, 1))
	router := gin.New()
	router.DELETE("/api/download-queue/failed", handler.ClearFailed)
	router.POST("/api/download-queue/:id/retry", handler.Retry)
	router.DELETE("/api/download-queue/:id", handler.Remove)

	retryRecorder := httptest.NewRecorder()
	retryRequest := httptest.NewRequest(http.MethodPost, "/api/download-queue/"+strconv.FormatUint(uint64(tasks[2].ID), 10)+"/retry", nil)
	router.ServeHTTP(retryRecorder, retryRequest)
	if retryRecorder.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", retryRecorder.Code, retryRecorder.Body.String())
	}
	var retried model.Download115Queue
	if err := db.First(&retried, tasks[2].ID).Error; err != nil {
		t.Fatalf("load retried task: %v", err)
	}
	if retried.Status != model.QueueStatusPending || retried.RetryCount != 0 || retried.LastError != "" {
		t.Fatalf("unexpected retried task: %+v", retried)
	}

	runningRecorder := httptest.NewRecorder()
	runningRequest := httptest.NewRequest(http.MethodDelete, "/api/download-queue/"+strconv.FormatUint(uint64(tasks[1].ID), 10), nil)
	router.ServeHTTP(runningRecorder, runningRequest)
	if runningRecorder.Code != http.StatusConflict {
		t.Fatalf("running delete status=%d body=%s", runningRecorder.Code, runningRecorder.Body.String())
	}

	removeRecorder := httptest.NewRecorder()
	removeRequest := httptest.NewRequest(http.MethodDelete, "/api/download-queue/"+strconv.FormatUint(uint64(tasks[0].ID), 10), nil)
	router.ServeHTTP(removeRecorder, removeRequest)
	if removeRecorder.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", removeRecorder.Code, removeRecorder.Body.String())
	}
	if err := db.First(&model.Download115Queue{}, tasks[0].ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("removed task still exists: %v", err)
	}

	clearRecorder := httptest.NewRecorder()
	clearRequest := httptest.NewRequest(http.MethodDelete, "/api/download-queue/failed", nil)
	router.ServeHTTP(clearRecorder, clearRequest)
	if clearRecorder.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clearRecorder.Code, clearRecorder.Body.String())
	}
	var failedCount int64
	if err := db.Model(&model.Download115Queue{}).Where("status = ?", model.QueueStatusFailed).Count(&failedCount).Error; err != nil {
		t.Fatalf("count failed tasks: %v", err)
	}
	if failedCount != 0 {
		t.Fatalf("failed tasks remaining=%d", failedCount)
	}
}
