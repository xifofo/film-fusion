package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"film-fusion/app/config"
)

func TestTelegramNotifierSendTest(t *testing.T) {
	var gotPath, gotChatID, gotThreadID, gotText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotChatID = r.Form.Get("chat_id")
		gotThreadID = r.Form.Get("message_thread_id")
		gotText = r.Form.Get("text")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	cfg := &config.Config{Telegram: config.TelegramConfig{
		Enabled: true, BotToken: "123456:test-token", ChatID: "-100123",
		MessageThreadID: 42, InstanceName: "家庭媒体", APIBase: server.URL, TimeoutSeconds: 2,
	}}
	notifier := NewTelegramNotifier(cfg, nil)
	if err := notifier.SendTest(context.Background()); err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if gotPath != "/bot123456:test-token/sendMessage" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotChatID != "-100123" || gotThreadID != "42" {
		t.Fatalf("unexpected target: chat=%q thread=%q", gotChatID, gotThreadID)
	}
	if !strings.Contains(gotText, "家庭媒体") || !strings.Contains(gotText, "测试通知") {
		t.Fatalf("unexpected message: %q", gotText)
	}
}

func TestTelegramNotifierSendPhoto(t *testing.T) {
	var gotPath string
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
	}))
	defer server.Close()

	cfg := &config.Config{Telegram: config.TelegramConfig{
		Enabled: true, BotToken: "123456:test-token", ChatID: "-100123",
		MessageThreadID: 42, APIBase: server.URL, TimeoutSeconds: 2, Silent: true,
	}}
	notifier := NewTelegramNotifier(cfg, nil)
	if err := notifier.SendPhoto(context.Background(), "https://image.tmdb.org/t/p/w780/poster.jpg", "百花杀 (2026) S01E01"); err != nil {
		t.Fatalf("SendPhoto: %v", err)
	}

	if gotPath != "/bot123456:test-token/sendPhoto" {
		t.Fatalf("path = %q", gotPath)
	}
	wants := map[string]string{
		"chat_id":              "-100123",
		"message_thread_id":    "42",
		"photo":                "https://image.tmdb.org/t/p/w780/poster.jpg",
		"caption":              "百花杀 (2026) S01E01",
		"disable_notification": "true",
	}
	for key, want := range wants {
		if got := gotForm.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestFormatSecurityAlertDoesNotContainCredentials(t *testing.T) {
	message := formatSecurityAlert("FilmFusion", SecurityAlert{
		Source: "emby", IP: "198.51.100.8", Username: "alice", Scope: "account_ip",
		FailureCount: 5, BlockedUntil: time.Date(2026, 7, 22, 10, 30, 0, 0, time.Local),
		TriggeredAt: time.Date(2026, 7, 22, 10, 0, 0, 0, time.Local),
	})
	for _, want := range []string{"Emby", "198.51.100.8", "alice", "失败次数: 5"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q: %s", want, message)
		}
	}
}

func TestTelegramNotifierRedactsTokenFromNetworkErrors(t *testing.T) {
	const token = "123456:secret-token"
	cfg := &config.Config{Telegram: config.TelegramConfig{
		BotToken: token, ChatID: "-100123", APIBase: "http://127.0.0.1:1", TimeoutSeconds: 1,
	}}
	err := NewTelegramNotifier(cfg, nil).SendTest(context.Background())
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked bot token: %v", err)
	}
}
