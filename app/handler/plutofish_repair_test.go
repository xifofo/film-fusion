package handler

import (
	"context"
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

func TestPlutoFishRepairHandlersBoundInputAndOwnerScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "state.db")), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	if err := db.AutoMigrate(&model.SystemConfig{}, &model.PlutoFishEvent{}, &model.PlutoFishSTRMRepair{}); err != nil {
		t.Fatal(err)
	}
	svc := service.NewPlutoFishService(db)
	if err := svc.Save(context.Background(), service.PlutoFishConfig{BaseURL: "https://plutofish.example", OwnerUID: "owner", Mode: "sse", PollSeconds: 15, APIKey: "offline-only"}, 7); err != nil {
		t.Fatal(err)
	}
	handler := NewPlutoFishHandler(svc)
	var user uint
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if user != 0 {
			c.Set("user_id", user)
		}
		c.Next()
	})
	router.POST("/preview", handler.PreviewSTRMRepairs)
	router.POST("/apply", handler.RepairSTRMs)
	cases := []struct {
		name, path, body string
		user             uint
		status           int
	}{
		{"no identity", "/preview", "{}", 0, 401},
		{"other user", "/preview", "{}", 8, 400},
		{"empty preview", "/preview", "{}", 7, 200},
		{"too many", "/preview", `{"limit":101}`, 7, 400},
		{"negative page", "/preview", `{"afterID":-1}`, 7, 400},
		{"malformed", "/preview", `{"limit":"private-test-value"}`, 7, 400},
		{"oversize preview", "/preview", `{"unused":"` + strings.Repeat("x", 5000) + `"}`, 7, 400},
		{"no identity apply", "/apply", `{"items":[]}`, 0, 401},
		{"empty apply", "/apply", `{"items":[]}`, 7, 400},
		{"missing preview", "/apply", `{"items":[{"eventID":1}]}`, 7, 400},
		{"malformed apply", "/apply", `{"items":"private-test-value"}`, 7, 400},
		{"oversize apply", "/apply", `{"unused":"` + strings.Repeat("x", 33000) + `"}`, 7, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user = tc.user
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			router.ServeHTTP(out, request)
			if out.Code != tc.status || out.Header().Get("Cache-Control") != "no-store" || strings.Contains(out.Body.String(), "private-test-value") {
				t.Fatalf("status=%d response=%s", out.Code, out.Body)
			}
			if tc.status == 200 && !strings.Contains(out.Body.String(), `"items":[]`) {
				t.Fatalf("response=%s", out.Body)
			}
		})
	}
}
