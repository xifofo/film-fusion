package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

func TestUpdateCloudStorageMatch302AccessMode(t *testing.T) {
	oldDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cloud-storage.db")), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Silent),
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = oldDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	storage := model.CloudStorage{
		UserID:             7,
		StorageType:        model.StorageType115Open,
		StorageName:        "source",
		Status:             model.StatusActive,
		Match302AccessMode: model.Match302AccessModeAuto,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatalf("create storage: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewCloudStorageHandler()
	router.PUT("/storages/:id", func(c *gin.Context) {
		c.Set("user_id", uint(7))
		handler.UpdateCloudStorage(c)
	})

	request := httptest.NewRequest(
		http.MethodPut,
		"/storages/"+strconv.FormatUint(uint64(storage.ID), 10),
		bytes.NewBufferString(`{"match302_access_mode":"cookie_only"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body.String())
	}

	var saved model.CloudStorage
	if err := db.First(&saved, storage.ID).Error; err != nil {
		t.Fatalf("reload storage: %v", err)
	}
	if saved.Match302AccessMode != model.Match302AccessModeCookieOnly {
		t.Fatalf("saved mode=%q", saved.Match302AccessMode)
	}

	request = httptest.NewRequest(
		http.MethodPut,
		"/storages/"+strconv.FormatUint(uint64(storage.ID), 10),
		bytes.NewBufferString(`{"match302_access_mode":"invalid"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status=%d body=%s", response.Code, response.Body.String())
	}
	if err := db.First(&saved, storage.ID).Error; err != nil {
		t.Fatalf("reload after invalid update: %v", err)
	}
	if saved.Match302AccessMode != model.Match302AccessModeCookieOnly {
		t.Fatalf("invalid update changed mode to %q", saved.Match302AccessMode)
	}
}
