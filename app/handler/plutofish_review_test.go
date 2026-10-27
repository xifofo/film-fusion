package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

func TestPlutoFishReviewRequiresExplicitConfirmationAndReturnsAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "state.db")), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	if err := db.AutoMigrate(&model.SystemConfig{}, &model.PlutoFishEvent{}, &model.PlutoFishCursor{}); err != nil {
		t.Fatal(err)
	}
	svc := service.NewPlutoFishService(db)
	cfg := service.PlutoFishConfig{BaseURL: "https://plutofish.shop", OwnerUID: "mock-owner", APIKey: "local-fixture", Mode: "sse", PollSeconds: 15}
	if err := svc.Save(context.Background(), cfg, 7); err != nil {
		t.Fatal(err)
	}
	store := service.NewPlutoFishStore(db, cfg.BaseURL, cfg.OwnerUID)
	if err := store.Commit(context.Background(), []plutofish.Event{{ID: "opaque-event", TransferID: "transfer", Type: "transfer.unknown", Status: "unknown", SchemaVersion: 1}}, "opaque-cursor"); err != nil {
		t.Fatal(err)
	}
	var row model.PlutoFishEvent
	db.First(&row)
	h := NewPlutoFishHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-Authenticated") == "yes" {
			c.Set("user_id", uint(7))
		}
		c.Next()
	})
	r.POST("/events/:id/review", h.Review)
	r.GET("/events", h.Events)
	endpoint := fmt.Sprintf("/events/%d/review", row.ID)
	for _, test := range []struct {
		path, body    string
		authenticated bool
		want          int
	}{
		{endpoint, `{"confirmed":true}`, false, 401},
		{endpoint, `{}`, true, 400},
		{endpoint, `{"confirmed":false}`, true, 400},
		{endpoint, `{"confirmed":"private-invalid-input"}`, true, 400},
		{"/events/invalid/review", `{"confirmed":true}`, true, 400},
		{"/events/0/review", `{"confirmed":true}`, true, 400},
	} {
		req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		if test.authenticated {
			req.Header.Set("X-Test-Authenticated", "yes")
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != test.want || strings.Contains(out.Body.String(), "private-invalid-input") {
			t.Fatalf("unexpected rejected request status/body %d", out.Code)
		}
		db.First(&row, row.ID)
		if row.State != "review" || row.ReviewedAt != nil {
			t.Fatal("unconfirmed request changed review")
		}
	}
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"confirmed":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Authenticated", "yes")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/events", nil))
	for _, expected := range []string{`"state":"reviewed"`, `"status":"unknown"`, `"reviewedBy":7`, `"reviewedAt":`} {
		if !strings.Contains(out.Body.String(), expected) {
			t.Fatalf("missing audit/original result %s", expected)
		}
	}
}
