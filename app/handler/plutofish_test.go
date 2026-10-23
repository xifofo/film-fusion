package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

func TestPlutoFishHandlersHideSecretsAndExposeDurableReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "state.db")), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	if err := db.AutoMigrate(&model.SystemConfig{}, &model.PlutoFishCursor{}, &model.PlutoFishEvent{}, &model.PlutoFishReceipt{}, &model.PlutoFishBusinessPlan{}, &model.CloudPath{}); err != nil {
		t.Fatal(err)
	}
	svc := service.NewPlutoFishService(db)
	defer svc.Stop()
	h := NewPlutoFishHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", uint(1)); c.Next() })
	r.PUT("/config", h.Save)
	r.GET("/config", h.Config)
	r.GET("/events", h.Events)
	r.GET("/status", h.Status)
	body := `{"enabled":false,"baseURL":"https://plutofish.shop","ownerUID":"owner","mode":"sse","pollSeconds":15,"apiKey":"dummy-secret-only"}`
	put := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(body))
	put.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	r.ServeHTTP(out, put)
	if out.Code != 200 || strings.Contains(out.Body.String(), "dummy-secret-only") {
		t.Fatal(out.Code, "config save failed or exposed secret")
	}
	for _, path := range []string{"/config", "/status", "/events"} {
		out = httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest(http.MethodGet, path, nil))
		if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" || strings.Contains(out.Body.String(), "dummy-secret-only") {
			t.Fatal(path, out.Code)
		}
	}
	// Malformed config errors must not echo submitted secrets or parse details.
	bad := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{"enabled":"dummy-secret-only"}`))
	bad.Header.Set("Content-Type", "application/json")
	out = httptest.NewRecorder()
	r.ServeHTTP(out, bad)
	if out.Code != 400 || strings.Contains(out.Body.String(), "dummy-secret-only") {
		t.Fatal("invalid JSON leaked input")
	}
	cfg, _ := svc.Config(context.Background())
	encoded, _ := json.Marshal(cfg)
	if bytes.Contains(encoded, []byte("apiKey")) {
		t.Fatal("GET config includes key field")
	}
}
