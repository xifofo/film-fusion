package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFilmFusionLoginProtectionReturnsOrdinaryUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:auth-security?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	database.DB = db

	hash, err := utils.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Create(&model.User{Username: "admin", Password: hash, IsActive: true, IsAdmin: true}).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{Security: config.LoginSecurityConfig{
			Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 1,
			MaxFailuresPerIP: 5, BlockMinutes: 30,
		}},
		JWT: config.JWTConfig{Secret: "test-secret", ExpireTime: 1, Issuer: "test"},
	}
	protection := service.NewAppLoginProtection(cfg, nil)
	handler := NewAuthHandler(cfg, protection)
	router := gin.New()
	router.POST("/api/auth/login", handler.Login)

	request := func(password string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"`+password+`"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder
	}

	if recorder := request("wrong-password"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("first failed login status = %d", recorder.Code)
	}
	recorder := request("correct-password")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("blocked login status = %d", recorder.Code)
	}
	if bytes.Contains(bytes.ToLower(recorder.Body.Bytes()), []byte("block")) || bytes.Contains(recorder.Body.Bytes(), []byte("封禁")) {
		t.Fatalf("blocked response disclosed protection state: %s", recorder.Body.String())
	}

	snapshot := protection.Snapshot()
	if len(snapshot.Blocks) != 1 || !protection.Unblock(snapshot.Blocks[0].Scope, snapshot.Blocks[0].IP, snapshot.Blocks[0].Username) {
		t.Fatalf("unable to clear test block: %#v", snapshot)
	}
	if recorder := request("correct-password"); recorder.Code != http.StatusOK {
		t.Fatalf("login after unblock status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}
