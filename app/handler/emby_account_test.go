package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestEmbyAccountExpiryAPIValidatesAndPersistsTimezone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("saving a future expiry must not update policy")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"Id":"u1","Name":"Alice","ServerId":"s1","Policy":{"IsDisabled":false}}`)
	}))
	defer upstream.Close()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "expiry.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.EmbyAccountExpiry{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	svc := service.NewEmbyAccountService(db, &config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "test"}}, nil)
	h := NewEmbyAccountHandler(svc)
	router := gin.New()
	router.PUT("/api/emby-accounts/:user_id/expiry", h.SetExpiry)
	request := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/emby-accounts/u1/expiry", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{
		`{}`, `{"server_id":"s1"}`, `{"expires_at":null}`,
		`{"server_id":"s1","expires_at":""}`, `{"server_id":"s1","expires_at":"2030-01-01 08:00:00"}`,
		`{"server_id":"s1","expires_at":123}`, `{"server_id":"s1","expires_at":"0001-01-01T00:00:00Z"}`,
	} {
		if w := request(body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid payload accepted: %s (%d)", body, w.Code)
		}
	}
	if w := request(`{"server_id":"old-server","expires_at":null}`); w.Code != http.StatusConflict {
		t.Fatalf("server mismatch status=%d", w.Code)
	}
	w := request(`{"server_id":"s1","expires_at":"2030-01-01T08:00:00+08:00"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data model.EmbyAccountExpiry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if response.Data.ExpiresAt == nil || !response.Data.ExpiresAt.Equal(want) {
		t.Fatalf("timezone changed: %+v", response.Data)
	}
	// Missing expires_at cannot accidentally clear an existing setting.
	_ = request(`{"server_id":"s1"}`)
	var saved model.EmbyAccountExpiry
	if err := db.First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ExpiresAt == nil || !saved.ExpiresAt.Equal(want) {
		t.Fatal("invalid request erased deadline")
	}
	if w := request(`{"server_id":"s1","expires_at":null}`); w.Code != http.StatusOK {
		t.Fatalf("clear failed: %s", w.Body.String())
	}
	var cleared model.EmbyAccountExpiry
	if err := db.First(&cleared).Error; err != nil || cleared.ExpiresAt != nil {
		t.Fatalf("clear not persisted: %+v %v", cleared, err)
	}
}
