// 账户资料 HTTP 测试通过临时数据库和内存传输验证权限、字段投影及失败时不写入。
package handler

import (
	"io"
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

type accountInfoTransport func(*http.Request) (*http.Response, error)

func (fn accountInfoTransport) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestCloudStorageAccountInfoHTTP(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "account.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}); err != nil {
		t.Fatal(err)
	}
	previousDB, previousTransport := database.DB, http.DefaultTransport
	database.DB = db
	t.Cleanup(func() {
		database.DB, http.DefaultTransport = previousDB, previousTransport
		connection, _ := db.DB()
		_ = connection.Close()
	})
	calls := 0
	http.DefaultTransport = accountInfoTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/open/user/info" || req.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("unexpected upstream request")
		}

		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"state":true,"code":0,"data":{"user_id":12345,"user_name":"测试账号"}}`)), Request: req}, nil
	})
	row := model.CloudStorage{UserID: 7, StorageType: model.StorageType115Open, StorageName: "测试账号", AccessToken: "private-token", Cookie: "private-cookie", Config: `{"keep":true}`}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/cloud-storage/:id/account-info", func(c *gin.Context) {
		if c.GetHeader("X-Test-User") == "7" {
			c.Set("user_id", uint(7))
		}
		NewCloudStorageHandler().RefreshAccountInfo(c)
	})
	request := func(id string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/cloud-storage/"+id+"/account-info", nil)
		if authenticated {
			req.Header.Set("X-Test-User", "7")
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)

		return recorder
	}
	if got := request(strconv.Itoa(int(row.ID)), false).Code; got != 401 {
		t.Fatalf("unauthenticated: %d", got)
	}
	for _, id := range []string{"0", "bad", "4294967296"} {
		if got := request(id, true).Code; got != 400 {
			t.Fatalf("bad ID: %d", got)
		}
	}
	if got := request("999", true).Code; got != 404 {
		t.Fatalf("missing: %d", got)
	}
	foreign := model.CloudStorage{UserID: 8, StorageType: model.StorageType115Open, AccessToken: "foreign"}
	db.Create(&foreign)
	if got := request(strconv.Itoa(int(foreign.ID)), true).Code; got != 404 {
		t.Fatalf("foreign: %d", got)
	}
	if calls != 0 {
		t.Fatal("invalid requests called 115")
	}

	response := request(strconv.Itoa(int(row.ID)), true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "115_account_info") {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	if calls != 1 || strings.Contains(response.Body.String(), "private-") || strings.Contains(response.Body.String(), "access_token") {
		t.Fatal("response leaked credentials or repeated request")
	}
}
