// 账户资料测试使用临时 SQLite 与内存 HTTP 响应，不访问真实 115 账号。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

const accountInfoResponse = `{"state":true,"code":0,"data":{"user_id":12345,"user_name":"测试昵称","user_face_m":"https://example.com/avatar.png","vip_info":{"level_name":"VIP","expire":1900000000},"rt_space_info":{"all_total":{"size":"1099511627776","size_format":"1 TB"},"all_use":{"size":1024,"size_format":"1 KB"},"all_remain":{"size":1099511626752,"size_format":"1 TB"}}}}`

// 每个测试账户由当前测试临时库单独持有，凭据仅用占位值。
func accountInfoFixture(t *testing.T) (*gorm.DB, model.CloudStorage) {
	t.Helper()
	db, rows := newStorageOrderDB(t)
	row := rows[0]
	row.StorageType = model.StorageType115Open
	row.Config = `{"keep":{"number":1234567890123456789},"other":"unchanged"}`
	if err := db.Save(&row).Error; err != nil {
		t.Fatal(err)
	}

	return db, row
}

func accountInfoClient(t *testing.T, response string, before func()) *http.Client {
	t.Helper()

	return &http.Client{Transport: automationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != "https://proapi.115.com/open/user/info" || req.Header.Get("Authorization") != "Bearer keep-a" {
			t.Fatal("unexpected account info request")
		}
		if before != nil {
			before()
		}

		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})}
}

func TestCloudStorageAccountInfoMergesConfig(t *testing.T) {
	db, row := accountInfoFixture(t)
	result, err := RefreshCloudStorageAccountInfo(t.Context(), db, accountInfoClient(t, accountInfoResponse, nil), 7, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saved model.CloudStorage
	if err := db.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if result.Config != saved.Config || saved.ProviderUID != "12345" || saved.AccessToken != row.AccessToken || saved.Cookie != row.Cookie {
		t.Fatal("snapshot missing or unrelated credentials changed")
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(saved.Config), &config); err != nil {
		t.Fatal(err)
	}
	if string(config["keep"]) != `{"number":1234567890123456789}` || string(config["other"]) != `"unchanged"` {
		t.Fatal("existing configuration or number precision changed")
	}
	var snapshot struct {
		FetchedAt string `json:"fetched_at"`
		Data      struct {
			UserName string `json:"user_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(config["115_account_info"], &snapshot); err != nil || snapshot.FetchedAt == "" || snapshot.Data.UserName != "测试昵称" {
		t.Fatal("invalid account snapshot")
	}
}

func TestCloudStorageAccountInfoRejectsScopeAndInvalidConfig(t *testing.T) {
	for _, kind := range []string{"foreign", "unauthenticated", "unsupported", "no token", "invalid JSON", "array", "null"} {
		t.Run(kind, func(t *testing.T) {
			db, row := accountInfoFixture(t)
			userID := uint(7)
			switch kind {
			case "foreign":
				userID = 8
			case "unauthenticated":
				userID = 0
			case "unsupported":
				row.StorageType = "other"
			case "no token":
				row.AccessToken = ""
			case "invalid JSON":
				row.Config = "{"
			case "array":
				row.Config = "[]"
			case "null":
				row.Config = "null"
			}
			if err := db.Save(&row).Error; err != nil {
				t.Fatal(err)
			}
			client := accountInfoClient(t, accountInfoResponse, func() { t.Fatal("invalid request reached 115") })
			if _, err := RefreshCloudStorageAccountInfo(t.Context(), db, client, userID, row.ID); err == nil {
				t.Fatal("invalid scope/config accepted")
			}
		})
	}
}

func TestCloudStorageAccountInfoPreservesDataOnUpstreamFailure(t *testing.T) {
	for _, body := range []string{`{"state":false,"code":40101004}`, `{}`, `{"state":true,"data":null}`, `{"state":true,"data":{}}`, `{"state":true,"code":99,"data":{"user_id":12345}}`, `{`, strings.Repeat("x", (1<<20)+1)} {
		db, row := accountInfoFixture(t)
		_, err := RefreshCloudStorageAccountInfo(t.Context(), db, accountInfoClient(t, body, nil), 7, row.ID)
		if err == nil {
			t.Fatal("invalid upstream response accepted")
		}
		var saved model.CloudStorage
		db.First(&saved, row.ID)
		if saved.Config != row.Config || saved.AccessToken != row.AccessToken {
			t.Fatal("failure changed stored data")
		}
	}
}

func TestCloudStorageAccountInfoRejectsConcurrentChanges(t *testing.T) {
	for _, field := range []string{"config", "access_token", "provider_uid"} {
		t.Run(field, func(t *testing.T) {
			db, row := accountInfoFixture(t)
			client := accountInfoClient(t, accountInfoResponse, func() {
				if err := db.Model(&row).Update(field, "concurrently changed").Error; err != nil {
					t.Fatal(err)
				}
			})
			_, err := RefreshCloudStorageAccountInfo(t.Context(), db, client, 7, row.ID)
			var conflict *StorageAccountInfoError
			if !errors.As(err, &conflict) || conflict.Status != 409 {
				t.Fatalf("expected conflict, got %v", err)
			}
			var saved model.CloudStorage
			db.First(&saved, row.ID)
			if strings.Contains(saved.Config, "115_account_info") {
				t.Fatal("conflict saved a snapshot")
			}
		})
	}
}

func TestCloudStorageAccountInfoRejectsAccountMismatchAndCancellation(t *testing.T) {
	db, row := accountInfoFixture(t)
	db.Model(&row).Update("provider_uid", "999")
	_, err := RefreshCloudStorageAccountInfo(t.Context(), db, accountInfoClient(t, accountInfoResponse, nil), 7, row.ID)
	var conflict *StorageAccountInfoError
	if !errors.As(err, &conflict) || conflict.Status != 409 {
		t.Fatalf("expected mismatch conflict: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := RefreshCloudStorageAccountInfo(ctx, db, accountInfoClient(t, accountInfoResponse, nil), 7, row.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
