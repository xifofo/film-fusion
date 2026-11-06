package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDeleteCloudStorageHTTP(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delete.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = oldDB
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	if err := db.AutoMigrate(
		&model.CloudStorage{}, &model.MediaDirectory{}, &model.Match302{}, &model.Match302BalanceMember{},
		&model.Match302BalanceAssignment{}, &model.EmbyAccountBinding{}, &model.AutomationSource{},
		&model.AutomationWorkflow{}, &model.AutomationTemplate{}, &model.AutomationRun{}, &model.AutomationNodeRun{},
		&model.Download115Queue{}, &model.OrganizePreviewTask{}, &model.OrganizeUpgradeJob{},
		&model.OrganizeSourceFolderDeletionTask{}, &model.OrganizeStorageLock{}, &model.OrganizeManagedStrm{}, &model.OrganizePlan{},
	); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&model.CloudStorage{ID: 1, UserID: 7, StorageName: "被引用的主盘", Cookie: "secret-cookie", AccessToken: "secret-access"},
		&model.CloudStorage{ID: 2, UserID: 7, StorageName: "空闲账号"},
		&model.MediaDirectory{CloudStorageID: 1, UserID: 7, DirectoryName: "电影库"},
		&model.EmbyAccountBinding{CloudStorageID: 1, EmbyUserName: "电视用户"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/api/cloud-storage/:id", func(c *gin.Context) {
		if userID, _ := strconv.ParseUint(c.GetHeader("X-Test-User"), 10, 32); userID > 0 {
			c.Set("user_id", uint(userID))
		}
		NewCloudStorageHandler().DeleteCloudStorage(c)
	})
	request := func(id, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/cloud-storage/"+id, nil)
		req.Header.Set("X-Test-User", user)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	for _, test := range []struct {
		id, user string
		status   int
	}{
		{"1", "", 401}, {"0", "7", 400}, {"bad", "7", 400},
		{"1", "8", 404}, {"999", "7", 404},
	} {
		res := request(test.id, test.user)
		if res.Code != test.status || strings.Contains(res.Body.String(), "电影库") {
			t.Fatalf("unexpected response for %s/%s: %d %s", test.id, test.user, res.Code, res.Body)
		}
	}

	res := request("1", "7")
	var body struct {
		Code    int
		Message string
		Data    service.CloudStorageInUseError
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if res.Code != 409 || body.Code != 409 || len(body.Data.References) != 2 || !strings.Contains(body.Message, "电影库") || !strings.Contains(body.Message, "电视用户") {
		t.Fatalf("missing conflict locations: %d %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "secret-") || strings.Contains(res.Body.String(), "access_token") || strings.Contains(res.Body.String(), "cookie") {
		t.Fatal("response leaked credentials")
	}
	var bindingCount int64
	if err := db.Model(&model.EmbyAccountBinding{}).Where("cloud_storage_id = 1").Count(&bindingCount).Error; err != nil || bindingCount != 1 {
		t.Fatalf("binding removed by blocked/cross-owner deletion: %v", err)
	}
	if got := request("2", "7"); got.Code != 200 {
		t.Fatalf("unused storage: %d %s", got.Code, got.Body)
	}
	if got := request("2", "7"); got.Code != 404 {
		t.Fatalf("already deleted storage: %d %s", got.Code, got.Body)
	}
	if err := db.Migrator().DropTable(&model.AutomationTemplate{}); err != nil {
		t.Fatal(err)
	}
	if got := request("1", "7"); got.Code != 500 {
		t.Fatalf("reference query failure must not delete: %d %s", got.Code, got.Body)
	}
	if err := db.First(&model.CloudStorage{}, 1).Error; err != nil {
		t.Fatalf("storage lost after failed deletion: %v", err)
	}
}
