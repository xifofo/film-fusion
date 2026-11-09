// 授权资料保存使用临时 SQLite，验证新建命名、重新授权保留名称及失败时不覆盖配置。
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func Test115AuthorizationSavesAccountProfileAndName(t *testing.T) {
	for _, tc := range []struct {
		label, nickname, previousName, expectedName string
		isNew                                       bool
	}{
		{"new", " 测试昵称 ", "", "测试昵称的网盘", true},
		{"missing nickname", " ", "", "115 12345的网盘", true},
		{"reauthorize", "新昵称", "电影主盘", "电影主盘", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "authorization.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			connection, _ := db.DB()
			t.Cleanup(func() { _ = connection.Close() })
			if err := db.AutoMigrate(&model.CloudStorage{}); err != nil {
				t.Fatal(err)
			}

			row := model.CloudStorage{
				UserID: 7, StorageType: model.StorageType115Open, ProviderUID: "12345",
				StorageName: tc.previousName, AccessToken: "test-access", RefreshToken: "test-refresh",
				Config: `{"keep":{"number":1234567890123456789}}`,
			}
			if !tc.isNew {
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			info := &sdk115.UserInfoResp{UserID: 12345, UserName: tc.nickname, UserFaceM: "https://example.com/avatar.png"}
			info.VipInfo.LevelName = "VIP"
			info.RtSpaceInfo.AllTotal.Size = json.Number("1099511627776")
			now := time.Now().UTC()
			if err := apply115AccountProfile(&row, info, tc.isNew, now); err != nil {
				t.Fatal(err)
			}
			if err := db.Save(&row).Error; err != nil {
				t.Fatal(err)
			}

			var saved model.CloudStorage
			if err := db.First(&saved, row.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.StorageName != tc.expectedName || saved.AccessToken != row.AccessToken || saved.RefreshToken != row.RefreshToken {
				t.Fatal("account naming or credential preservation failed")
			}
			var config map[string]json.RawMessage
			if err := json.Unmarshal([]byte(saved.Config), &config); err != nil {
				t.Fatal(err)
			}
			var snapshot struct {
				FetchedAt time.Time           `json:"fetched_at"`
				Data      sdk115.UserInfoResp `json:"data"`
			}
			if err := json.Unmarshal(config["115_account_info"], &snapshot); err != nil {
				t.Fatal(err)
			}
			if !snapshot.FetchedAt.Equal(now) || snapshot.Data.UserID != 12345 || snapshot.Data.UserName != tc.nickname || snapshot.Data.RtSpaceInfo.AllTotal.Size != "1099511627776" {
				t.Fatal("authorized account profile was not persisted")
			}
			if string(config["keep"]) != `{"number":1234567890123456789}` || strings.Contains(saved.Config, "test-access") || strings.Contains(saved.Config, "test-refresh") {
				t.Fatal("unrelated config changed or credentials leaked into profile")
			}
		})
	}
}

func Test115AuthorizationProfileFailurePreservesRecord(t *testing.T) {
	for _, raw := range []string{"{", "null", "[]"} {
		row := model.CloudStorage{StorageName: "保留名称", Config: raw}
		if err := apply115AccountProfile(&row, &sdk115.UserInfoResp{UserID: 12345, UserName: "昵称"}, true, time.Now()); err == nil {
			t.Fatal("invalid config accepted")
		}
		if row.StorageName != "保留名称" || row.Config != raw {
			t.Fatal("failed merge changed record")
		}
	}
	for _, info := range []*sdk115.UserInfoResp{nil, {UserID: 0}, {UserID: -1}} {
		row := model.CloudStorage{StorageName: "保留名称", Config: "{}"}
		if err := apply115AccountProfile(&row, info, true, time.Now()); err == nil || row.StorageName != "保留名称" || row.Config != "{}" {
			t.Fatal("invalid account profile accepted or changed record")
		}
	}
}

func Test115QRCodeWithoutNameReachesAuthentication(t *testing.T) {
	router := gin.New()
	router.POST("/qrcode", (&Auth115Handler{}).GetQrCode)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/qrcode", strings.NewReader(`{"client_id":"12345"}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("name should be optional; got status %d", recorder.Code)
	}
}
