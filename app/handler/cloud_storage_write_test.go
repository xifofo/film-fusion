// 部分更新验证使用临时数据库，覆盖零值语义、凭据保留及旧占位接口不写入。
package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCloudStorageSparseUpdateAndLegacyActions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "storage.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = previous
		connection, _ := db.DB()
		_ = connection.Close()
	})
	row := model.CloudStorage{
		UserID: 7, StorageType: model.StorageType115Open, StorageName: "保留名称",
		AccessToken: "test-access", RefreshToken: "test-refresh", Cookie: "test-cookie",
		AutoRefresh: true, Match302MaxActive: 5, Match302CacheMaxGB: 10,
		Config: `{"cookie_keepalive":{"healthy":true},"115_account_info":{"data":{"user_id":12345}}}`,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	h := NewCloudStorageHandler()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", uint(7)) })
	router.PUT("/storage/:id", h.UpdateCloudStorage)
	router.POST("/storage/:id/refresh", h.RefreshToken)
	router.POST("/storage/:id/test", h.TestConnection)
	path := "/storage/" + strconv.FormatUint(uint64(row.ID), 10)
	body := `{"auto_refresh":false,"match302_max_active":0,"match302_cache_max_gb":0,"cookie":"","user_id":8,"provider_uid":"foreign","config":"{}"}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, path, strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var saved model.CloudStorage
	if err := db.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.AutoRefresh || saved.Match302MaxActive != 0 || saved.Match302CacheMaxGB != 0 || saved.Cookie != "" {
		t.Fatal("explicit false, zero or empty values were ignored")
	}
	if saved.StorageName != row.StorageName || saved.AccessToken != row.AccessToken || saved.RefreshToken != row.RefreshToken || saved.UserID != 7 || saved.ProviderUID != row.ProviderUID {
		t.Fatal("omitted credentials or server-owned fields were overwritten")
	}
	if saved.Config != row.Config {
		t.Fatal("ordinary update overwrote system-owned account or keepalive snapshots")
	}
	before := saved.UpdatedAt
	for _, action := range []string{"refresh", "test"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path+"/"+action, nil))
		if response.Code != http.StatusNotImplemented {
			t.Fatalf("%s reported false success: %d", action, response.Code)
		}
	}
	if err := db.First(&saved, row.ID).Error; err != nil || !saved.UpdatedAt.Equal(before) || saved.LastRefreshAt != nil {
		t.Fatal("unimplemented action changed storage state")
	}
}
