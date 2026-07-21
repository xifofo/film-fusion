package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

func TestEmbyProxyBlockedLoginLooksLikeOrdinaryUnauthorized(t *testing.T) {
	cfg := &config.Config{Emby: config.EmbyConfig{Security: config.EmbySecurityConfig{
		Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 1,
		MaxFailuresPerIP: 10, BlockMinutes: 30,
	}}}
	protection := service.NewEmbyLoginProtection(cfg, nil)
	first := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"Alice","Pw":"wrong"}`))
	first.RemoteAddr = "198.51.100.20:1234"
	attempt, blocked := protection.Inspect(first)
	if attempt == nil || blocked {
		t.Fatal("first attempt should reach Emby")
	}
	protection.ObserveResponse(attempt, http.StatusUnauthorized)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/emby/Users/AuthenticateByName", strings.NewReader(`{"Username":"Alice","Pw":"wrong"}`))
	ctx.Request.RemoteAddr = "198.51.100.20:5678"
	handler := &EmbyProxyHandler{loginProtection: protection}
	handler.ProxyRequest(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") != "" {
		t.Fatal("blocked response must not expose a retry window")
	}
	if strings.Contains(strings.ToLower(recorder.Body.String()), "block") || strings.Contains(recorder.Body.String(), "封禁") {
		t.Fatalf("blocked response leaked state: %s", recorder.Body.String())
	}
}
