package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"film-fusion/app/auth"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/middleware"
	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type apiTokenFixture struct {
	db     *gorm.DB
	router *gin.Engine
	user   model.User
	jwt    string
	cfg    *config.Config
}

func newAPITokenFixture(t *testing.T) *apiTokenFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tokens.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous; _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.APIToken{}); err != nil {
		t.Fatal(err)
	}
	f := &apiTokenFixture{db: db, user: model.User{Username: "owner", Password: "unused", IsActive: true, IsAdmin: true}, cfg: &config.Config{JWT: config.JWTConfig{Secret: "api-token-test-secret", ExpireTime: 1, Issuer: "test"}}}
	if err := db.Create(&f.user).Error; err != nil {
		t.Fatal(err)
	}
	f.jwt, err = auth.NewJWTService(f.cfg).GenerateToken(f.user.ID, f.user.Username)
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewAPITokenService(db)
	h := NewAPITokenHandler(svc)
	f.router = gin.New()
	api := f.router.Group("/api", middleware.APIAuth(f.cfg, svc), middleware.RequireAdmin())
	api.GET("/api-tokens", h.List)
	api.GET("/api-tokens/scopes", h.Scopes)
	api.POST("/api-tokens", h.Create)
	api.POST("/api-tokens/:id/revoke", h.Revoke)
	ok := func(c *gin.Context) {
		c.JSON(200, gin.H{"user_id": c.GetUint("user_id"), "username": c.GetString("username")})
	}
	for _, path := range []string{"/emby-stats", "/emby-stats/image", "/emby-stats-extra", "/system-info", "/app-config", "/me", "/new-module"} {
		api.GET(path, ok)
	}
	api.POST("/emby-accounts/:id/expiry", ok)
	api.PUT("/app-config", ok)
	api.GET("/auth/115/sessions", ok)
	return f
}

func (f *apiTokenFixture) request(method, path, token, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	f.router.ServeHTTP(w, r)
	return w
}

func (f *apiTokenFixture) issue(t *testing.T, scopes ...string) (string, model.APIToken) {
	t.Helper()
	body, _ := json.Marshal(gin.H{"name": "Test integration", "scopes": scopes, "expires_at": time.Now().Add(time.Hour).UTC()})
	w := f.request("POST", "/api/api-tokens", f.jwt, string(body))
	if w.Code != http.StatusCreated || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issue failed: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Token  string         `json:"token"`
			Record model.APIToken `json:"record"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("token_hash")) {
		t.Fatal("create response exposed digest")
	}
	return response.Data.Token, response.Data.Record
}

func TestAPITokenLifecycleAndScopeEnforcement(t *testing.T) {
	f := newAPITokenFixture(t)
	raw, record := f.issue(t, "emby-stats", "emby-accounts", "emby-stats")
	if !strings.HasPrefix(raw, service.APITokenPrefix) || len(raw) != 49 || len(record.Scopes) != 2 {
		t.Fatal("invalid credential or non-deduplicated scopes")
	}
	var stored model.APIToken
	if err := f.db.First(&stored, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored.TokenHash) != 64 || strings.Contains(stored.TokenHash, raw) || stored.Prefix != raw[:14] {
		t.Fatal("credential was not stored as a digest")
	}
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/api/emby-stats", raw, 200},
		{"GET", "/api/emby-stats/image", raw, 200},
		{"POST", "/api/emby-accounts/42/expiry", raw, 200},
		{"GET", "/api/system-info", raw, 403},
		{"GET", "/api/emby-stats-extra", raw, 403},
		{"GET", "/api/new-module", raw, 403},
		{"GET", "/api/app-config", raw, 403},
		{"PUT", "/api/app-config", raw, 403},
		{"GET", "/api/me", raw, 403},
		{"GET", "/api/api-tokens", raw, 403},
		{"GET", "/api/api-tokens/scopes", raw, 403},
		{"POST", "/api/api-tokens", raw, 403},
		{"POST", fmt.Sprintf("/api/api-tokens/%d/revoke", record.ID), raw, 403},
		{"GET", "/api/emby-stats?token=" + raw, "", 401},
		{"GET", "/api/emby-stats", "ffapi_" + strings.Repeat("x", 43), 401},
		{"GET", "/api/emby-stats", "ffapi_short", 401},
		{"GET", "/api/emby-stats", "", 401},
		{"GET", "/api/emby-stats", f.jwt, 200},
		{"GET", "/api/emby-stats?token=" + f.jwt, "", 200},
		{"GET", "/api/app-config", f.jwt, 200},
	} {
		t.Run(tc.method+tc.path+fmt.Sprint(tc.want), func(t *testing.T) {
			w := f.request(tc.method, tc.path, tc.token, "{}")
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	w := f.request("GET", "/api/api-tokens", f.jwt, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), raw) || strings.Contains(w.Body.String(), stored.TokenHash) || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatal("list leaked credential or failed")
	}
	if err := f.db.First(&stored, record.ID).Error; err != nil || stored.LastUsedAt == nil {
		t.Fatal("authorized use was not recorded")
	}
	for i := 0; i < 2; i++ {
		w = f.request("POST", fmt.Sprintf("/api/api-tokens/%d/revoke", record.ID), f.jwt, "")
		if w.Code != 200 {
			t.Fatalf("revoke is not idempotent: %d", w.Code)
		}
	}
	if w = f.request("GET", "/api/emby-stats", raw, ""); w.Code != 401 {
		t.Fatalf("revoked token still usable: %d", w.Code)
	}
}

func TestAPITokenRejectsInvalidCreateAndIsolatesOwners(t *testing.T) {
	f := newAPITokenFixture(t)
	for _, body := range []string{
		`{}`, `{"name":"  ","scopes":["emby-stats"]}`,
		`{"name":"test","scopes":[]}`, `{"name":"test","scopes":["*"]}`,
		`{"name":"test","scopes":["emby-stats","unknown"]}`,
		`{"name":"test","scopes":["emby-stats"],"expires_at":"2000-01-01T00:00:00Z"}`,
		`{"name":"test","scopes":["emby-stats"],"expires_at":"bad"}`,
		`{"name":"` + strings.Repeat("长", 81) + `","scopes":["emby-stats"]}`,
	} {
		if w := f.request("POST", "/api/api-tokens", f.jwt, body); w.Code != 400 {
			t.Fatalf("invalid creation accepted: %s status=%d", body, w.Code)
		}
	}
	_, record := f.issue(t, "emby-stats")
	other := model.User{Username: "other", Password: "unused", IsActive: true, IsAdmin: true}
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherJWT, _ := auth.NewJWTService(f.cfg).GenerateToken(other.ID, other.Username)
	if w := f.request("GET", "/api/api-tokens", otherJWT, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatal("list exposed another owner's token")
	}
	if w := f.request("POST", fmt.Sprintf("/api/api-tokens/%d/revoke", record.ID), otherJWT, ""); w.Code != 404 {
		t.Fatal("another owner can revoke token")
	}
	for _, id := range []string{"0", "-1", "abc", "4294967296"} {
		if w := f.request("POST", "/api/api-tokens/"+id+"/revoke", f.jwt, ""); w.Code != 400 {
			t.Fatalf("invalid ID accepted: %s", id)
		}
	}
}

func TestAPITokenExpiryOwnerStateAndPersistence(t *testing.T) {
	for _, state := range []string{"expired", "revoked", "disabled", "demoted", "deleted", "empty-scopes"} {
		t.Run(state, func(t *testing.T) {
			f := newAPITokenFixture(t)
			raw, record := f.issue(t, "emby-stats")
			// A new service instance authenticates the persisted credential.
			if _, user, err := service.NewAPITokenService(f.db).Authenticate(t.Context(), raw); err != nil || user.ID != f.user.ID {
				t.Fatalf("persisted token could not authenticate: %v", err)
			}
			var err error
			want := 401
			switch state {
			case "expired":
				err = f.db.Model(&record).Update("expires_at", time.Now().Add(-time.Second)).Error
			case "revoked":
				err = f.db.Model(&record).Update("revoked_at", time.Now()).Error
			case "disabled":
				err = f.db.Model(&f.user).Update("is_active", false).Error
			case "demoted":
				err = f.db.Model(&f.user).Update("is_admin", false).Error
			case "deleted":
				err = f.db.Delete(&f.user).Error
			case "empty-scopes":
				err = f.db.Model(&record).Update("scopes", "[]").Error
				want = 403
			}
			if err != nil {
				t.Fatal(err)
			}
			if w := f.request("GET", "/api/emby-stats", raw, ""); w.Code != want {
				t.Fatalf("%s returned %d want %d", state, w.Code, want)
			}
		})
	}
}

func TestAPITokenNoExpiryAndDatabaseFailure(t *testing.T) {
	f := newAPITokenFixture(t)
	svc := service.NewAPITokenService(f.db)
	record, raw, err := svc.Create(t.Context(), f.user.ID, "  no expiry  ", []string{"cloud-storage"}, nil)
	if err != nil || record.ExpiresAt != nil || record.Name != "no expiry" {
		t.Fatalf("create never-expiring token: %v", err)
	}
	if w := f.request("GET", "/api/auth/115/sessions", raw, ""); w.Code != 200 {
		t.Fatalf("explicit 115 module authorization failed: %d", w.Code)
	}
	if err := f.db.Migrator().DropTable(&model.APIToken{}); err != nil {
		t.Fatal(err)
	}
	if w := f.request("GET", "/api/emby-stats", raw, ""); w.Code != 503 {
		t.Fatalf("database failure must fail closed: %d", w.Code)
	}
}
