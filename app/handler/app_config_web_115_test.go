package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAppConfigHandlerGetUsesDatabase115Settings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const userAgent = "Mozilla/5.0 Chrome/150.0.0.0 Safari/537.36"
	db, err := gorm.Open(sqlite.Open("file:handler-settings-115?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := database.Save115Settings(db, config.ServerConfig{
		Cookie115DefaultApp: "wechatmini",
		Web115UserAgent:     userAgent,
	}); err != nil {
		t.Fatalf("save database settings: %v", err)
	}

	cfg := &config.Config{Server: config.ServerConfig{
		Cookie115DefaultApp: "tv",
		Web115UserAgent:     "Mozilla/5.0 YAML",
	}}
	handler := NewAppConfigHandler(nil, cfg, nil, nil)
	handler.db = db

	router := gin.New()
	router.GET("/api/app-config", handler.Get)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/app-config", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Config config.Config `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 {
		t.Fatalf("expected response code 0, got %d", response.Code)
	}
	if got := response.Data.Config.Server.Web115UserAgent; got != userAgent {
		t.Fatalf("web_115_user_agent = %q, want %q", got, userAgent)
	}
	if got := response.Data.Config.Server.Cookie115DefaultApp; got != "wechatmini" {
		t.Fatalf("cookie_115_default_app = %q, want database value wechatmini", got)
	}
}

func TestSaveConfigWrites115SettingsOnlyToDatabase(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := "server:\n" +
		"  port: \"9000\"\n" +
		"  cookie_115_default_app: tv\n" +
		"  web_115_user_agent: \"Mozilla/5.0 YAML\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}

	db, err := gorm.Open(sqlite.Open("file:handler-save-settings-115?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	in := &config.Config{Server: config.ServerConfig{
		Port:                "9000",
		Cookie115DefaultApp: "wechatmini",
		Web115UserAgent:     "Mozilla/5.0 DATABASE",
	}}
	handler := NewAppConfigHandler(nil, in, nil, nil)
	handler.db = db
	if err := handler.saveConfigAndSiteSettings(in); err != nil {
		t.Fatalf("save app config: %v", err)
	}

	stored, err := database.Load115Settings(db)
	if err != nil {
		t.Fatalf("load database settings: %v", err)
	}
	if stored.CookieDefaultApp != "wechatmini" || stored.WebUserAgent != "Mozilla/5.0 DATABASE" {
		t.Fatalf("unexpected database settings: %+v", stored)
	}

	reloaded := viper.New()
	reloaded.SetConfigFile(path)
	if err := reloaded.ReadInConfig(); err != nil {
		t.Fatalf("reload YAML: %v", err)
	}
	if got := reloaded.GetString("server.cookie_115_default_app"); got != "tv" {
		t.Fatalf("YAML cookie app = %q, want unchanged value tv", got)
	}
	if got := reloaded.GetString("server.web_115_user_agent"); got != "Mozilla/5.0 YAML" {
		t.Fatalf("YAML user agent = %q, want unchanged legacy value", got)
	}
}

func TestAppConfigUpdatePayloadDistinguishesMissingAndEmpty115UserAgent(t *testing.T) {
	var missing appConfigUpdatePayload
	if err := json.Unmarshal([]byte(`{"config":{"server":{"port":"9000"}}}`), &missing); err != nil {
		t.Fatalf("decode missing field payload: %v", err)
	}
	if missing.web115UserAgentFieldPresent {
		t.Fatal("missing web_115_user_agent was marked present")
	}

	var empty appConfigUpdatePayload
	if err := json.Unmarshal([]byte(`{"config":{"server":{"port":"9000","web_115_user_agent":""}}}`), &empty); err != nil {
		t.Fatalf("decode empty field payload: %v", err)
	}
	if !empty.web115UserAgentFieldPresent {
		t.Fatal("explicitly empty web_115_user_agent was marked missing")
	}
	if empty.Config.Server.Web115UserAgent != "" {
		t.Fatalf("empty web_115_user_agent decoded as %q", empty.Config.Server.Web115UserAgent)
	}

	var notifications appConfigUpdatePayload
	if err := json.Unmarshal([]byte(`{"config":{"notifications":{"instance_name":"FilmFusion"}}}`), &notifications); err != nil {
		t.Fatalf("decode notifications payload: %v", err)
	}
	if !notifications.notificationsFieldPresent || notifications.telegramFieldPresent {
		t.Fatalf("notification field presence was not detected: %+v", notifications)
	}

	var legacy appConfigUpdatePayload
	if err := json.Unmarshal([]byte(`{"config":{"telegram":{"enabled":true}}}`), &legacy); err != nil {
		t.Fatalf("decode legacy Telegram payload: %v", err)
	}
	if legacy.notificationsFieldPresent || !legacy.telegramFieldPresent {
		t.Fatalf("legacy Telegram field presence was not detected: %+v", legacy)
	}
}
