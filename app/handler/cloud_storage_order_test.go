package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/auth"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCloudStorageOrderHTTP(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "order.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = oldDB
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	rows := []model.CloudStorage{
		{UserID: 7, StorageName: "first", SortOrder: 1, Cookie: "private-cookie"},
		{UserID: 7, StorageName: "second", SortOrder: 2, AccessToken: "private-access"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	input := service.StorageOrderInput{
		Expected: []service.StorageOrderVersion{{ID: rows[0].ID, SortOrder: 1}, {ID: rows[1].ID, SortOrder: 2}},
		Order:    []uint{rows[1].ID, rows[0].ID},
	}
	body, _ := json.Marshal(input)
	handler := NewCloudStorageHandler()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/api/cloud-storage/order", func(c *gin.Context) {
		if c.GetHeader("X-Test-User") == "7" {
			c.Set("user_id", uint(7))
		}
		handler.ReorderCloudStorages(c)
	})
	// 静态排序路由必须优先于原有 ID 更新路由。
	router.PUT("/api/cloud-storage/:id", handler.UpdateCloudStorage)
	request := func(payload []byte, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/cloud-storage/order", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req.Header.Set("X-Test-User", "7")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	if got := request(body, false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", got)
	}
	for _, invalid := range []string{"null", "{}", `{"extra":true}`, string(body) + "{}", `{"order":[]}` + strings.Repeat(" ", (1<<20)+1)} {
		if got := request([]byte(invalid), true).Code; got != http.StatusBadRequest {
			t.Fatalf("invalid input status: %d", got)
		}
	}
	response := request(body, true)
	if response.Code != http.StatusOK {
		t.Fatalf("save status=%d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-") || strings.Contains(response.Body.String(), "cookie") || strings.Contains(response.Body.String(), "access_token") {
		t.Fatal("sort response leaked credentials")
	}
	if got := request(body, true).Code; got != http.StatusConflict {
		t.Fatalf("stale request status: %d", got)
	}
	if got := auth.APITokenScopeForRoute("/api/cloud-storage/order"); got != "cloud-storage" {
		t.Fatalf("unexpected token scope: %s", got)
	}
}
