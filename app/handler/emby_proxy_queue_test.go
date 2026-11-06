package handler

// 验证队列清理必须明确范围，并能通过接口清理超过单页快照的失败记录。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClearTransferQueueScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "queue.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Match302BalanceAssignment{}); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = oldDB
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	router := gin.New()
	router.POST("/clear", NewEmbyProxyLogHandler(nil).ClearTransferQueue)
	for _, body := range []string{`{}`, `{"all":false}`, `{"all":true,"ids":[1]}`, `{"ids":[0]}`, `{"ids":[-1]}`, `{"ids":[]}`, `{`} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/clear", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d", body, recorder.Code)
		}
	}

	for i := 0; i < 60; i++ {
		item := model.Match302BalanceAssignment{Match302ID: 1, SourceFilePath: fmt.Sprintf("/failed-%d", i), Status: model.BalanceAssignmentStatusFailed}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/clear", strings.NewReader(`{"all":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			ClearedCount int64 `json:"cleared_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ClearedCount != 60 {
		t.Fatalf("cleared=%d, expected all 60 records", response.Data.ClearedCount)
	}
}
