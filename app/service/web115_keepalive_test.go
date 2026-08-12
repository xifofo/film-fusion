package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/model"
)

func TestWeb115CookieCheckInterval(t *testing.T) {
	if Web115CookieCheckInterval != 10*time.Minute {
		t.Fatalf("Web115CookieCheckInterval = %s, want 10m", Web115CookieCheckInterval)
	}
}

func TestResolveReloginAppUsesHotReloadedGlobalDefault(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Cookie115DefaultApp: "tv"}}
	service := &Web115KeepAliveService{cfg: cfg}

	if got := string(service.resolveReloginApp("")); got != "tv" {
		t.Fatalf("resolveReloginApp(empty) = %q, want tv", got)
	}
	if got := string(service.resolveReloginApp("web")); got != "web" {
		t.Fatalf("resolveReloginApp(web) = %q, want web", got)
	}

	cfg.Server.Cookie115DefaultApp = "wechatmini"
	if got := string(service.resolveReloginApp("")); got != "wechatmini" {
		t.Fatalf("resolveReloginApp(empty) after reload = %q, want wechatmini", got)
	}
}

func TestApplyReloginAppPreferenceCanFollowGlobalDefault(t *testing.T) {
	meta := web115KeepAliveMeta{App: "tv"}
	applyReloginAppPreference(&meta, UseDefaultReloginApp)
	if meta.App != "" {
		t.Fatalf("default preference kept per-storage app %q", meta.App)
	}

	applyReloginAppPreference(&meta, "ios")
	if meta.App != "ios" {
		t.Fatalf("explicit preference = %q, want ios", meta.App)
	}
}

func TestWeb115CookieAlertDue(t *testing.T) {
	now := time.Now()
	recentAttempt := now.Add(-30 * time.Minute)
	oldAttempt := now.Add(-2 * time.Hour)
	sent := now.Add(-24 * time.Hour)

	tests := []struct {
		name string
		meta web115KeepAliveMeta
		want bool
	}{
		{name: "first alert", want: true},
		{name: "recent failed attempt", meta: web115KeepAliveMeta{LastAlertTry: &recentAttempt}, want: false},
		{name: "retry failed attempt", meta: web115KeepAliveMeta{LastAlertTry: &oldAttempt}, want: true},
		{name: "already sent", meta: web115KeepAliveMeta{LastAlertAt: &sent}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := web115CookieAlertDue(tt.meta, now); got != tt.want {
				t.Fatalf("web115CookieAlertDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClearWeb115CookieAlert(t *testing.T) {
	now := time.Now()
	meta := web115KeepAliveMeta{LastAlertAt: &now, LastAlertTry: &now}
	clearWeb115CookieAlert(&meta)
	if meta.LastAlertAt != nil || meta.LastAlertTry != nil {
		t.Fatalf("alert state was not cleared: %+v", meta)
	}
}

func TestFormatWeb115CookieInvalidAlertDoesNotExposeCookie(t *testing.T) {
	const secretCookie = "UID=123; CID=secret-cid; SEID=secret-seid"
	body := formatWeb115CookieInvalidAlert(
		model.CloudStorage{ID: 7, StorageName: "家庭媒体", Cookie: secretCookie},
		web115KeepAliveMeta{LastError: "旧 Cookie 无效，无法续期"},
		errors.New("探活失败"),
		time.Date(2026, 8, 9, 12, 0, 0, 0, time.Local),
	)
	message := renderNotificationText(NotificationEvent{Title: "[115 Cookie 失效] FilmFusion", Message: body})

	for _, want := range []string{"[115 Cookie 失效]", "家庭媒体", "自动续期也未能恢复", "旧 Cookie 无效"} {
		if !strings.Contains(message, want) {
			t.Fatalf("notification missing %q: %s", want, message)
		}
	}
	if strings.Contains(message, secretCookie) || strings.Contains(message, "secret-seid") {
		t.Fatalf("notification exposed cookie: %s", message)
	}
}
