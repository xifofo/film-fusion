package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func Test115AppOptionsSearchPaginationAndResolve(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old; _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.SystemConfig{}, &model.Dictionary{}, &model.DictionaryItem{}); err != nil {
		t.Fatal(err)
	}
	value := `[{"value":"101","label":"Alpha","enabled":true,"sort_order":2},
		{"value":"102","label":"Beta","enabled":true,"sort_order":1},
		{"value":"103","label":"Hidden","enabled":false,"sort_order":0}]`
	if err := db.Create(&model.SystemConfig{ConfigKey: database.Catalog115Key, ConfigValue: value}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Init115AppCatalog(db); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	h := &Auth115Handler{}
	router.GET("/apps", h.ListApps)

	for _, tc := range []struct {
		query         string
		status, total int
		id            string
	}{
		{"?page_size=1", 200, 2, "102"},
		{"?page_size=1&page=2", 200, 2, "101"},
		{"?page=99", 200, 2, ""},
		{"?q=ALPHA", 200, 1, "101"},
		{"?q=02", 200, 1, "102"},
		{"?q=missing", 200, 0, ""},
		{"?ids=101&q=Beta", 200, 1, "101"},
		{"?ids=103", 200, 0, ""},
		{"?ids=999", 200, 0, ""},
		{"?page_size=101", 400, 0, ""},
		{"?page=0", 400, 0, ""},
		{"?page=9999999999999999999999", 400, 0, ""},
		{"?ids=invalid", 400, 0, ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/apps"+tc.query, nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			if tc.status != 200 {
				return
			}

			var response struct {
				Data struct {
					List  []database.App115Option `json:"list"`
					Total int                     `json:"total"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Data.Total != tc.total {
				t.Fatalf("total=%d", response.Data.Total)
			}
			if tc.id == "" && len(response.Data.List) != 0 {
				t.Fatal("expected empty list")
			}
			if tc.id != "" && (len(response.Data.List) != 1 || response.Data.List[0].Value != tc.id) {
				t.Fatalf("items=%v", response.Data.List)
			}
		})
	}

	if err := db.Model(&model.Dictionary{}).Where("code = ?", database.Catalog115Key).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/apps", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal("disabled dictionary must not return seeded choices")
	}
}

func Test115QRCodeRejectsInvalidClientIDBeforeProviderRequest(t *testing.T) {
	router := gin.New()
	router.POST("/qrcode", (&Auth115Handler{}).GetQrCode)
	for _, id := range []string{"0", "001", "abc", "1e8", "123456789012345678901"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/qrcode", strings.NewReader(`{"name":"test","client_id":"`+id+`"}`)))
		if w.Code != 400 {
			t.Fatalf("invalid client %q returned %d", id, w.Code)
		}
	}
}
