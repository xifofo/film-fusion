package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"film-fusion/app/config"
)

func TestWebhookNotificationChannelSendsStructuredEvent(t *testing.T) {
	var gotAuthorization, gotEvent string
	var gotPayload struct {
		Instance string            `json:"instance"`
		Event    string            `json:"event"`
		Title    string            `json:"title"`
		Message  string            `json:"message"`
		ImageURL string            `json:"image_url"`
		Severity string            `json:"severity"`
		Metadata map[string]string `json:"metadata"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotEvent = r.Header.Get("X-FilmFusion-Event")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := &config.Config{Notifications: config.NotificationConfig{
		InstanceName: "家庭媒体",
		Webhook: config.NotificationWebhookConfig{
			Enabled: true, URL: server.URL, Token: "secret-token", TimeoutSeconds: 2,
		},
	}}
	channel := NewWebhookNotificationChannel(cfg, nil)
	event := NotificationEvent{
		Type: NotificationEventRSSMatched, Title: "RSS 上新", Message: "百花杀 S01E01",
		ImageURL: "https://image.example/poster.jpg", Severity: NotificationSeverityInfo,
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.Local),
		Metadata:   map[string]string{"rule": "首集"},
	}
	if err := channel.Send(context.Background(), event); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotAuthorization != "Bearer secret-token" || gotEvent != string(NotificationEventRSSMatched) {
		t.Fatalf("unexpected headers auth=%q event=%q", gotAuthorization, gotEvent)
	}
	if gotPayload.Instance != "家庭媒体" || gotPayload.Event != string(NotificationEventRSSMatched) ||
		gotPayload.Message != "百花杀 S01E01" || gotPayload.ImageURL == "" || gotPayload.Metadata["rule"] != "首集" {
		t.Fatalf("unexpected payload: %+v", gotPayload)
	}
}

func TestWebhookNotificationChannelTestIgnoresEnabledSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := &config.Config{Notifications: config.NotificationConfig{Webhook: config.NotificationWebhookConfig{
		Enabled: false, URL: server.URL, TimeoutSeconds: 2,
	}}}
	channel := NewWebhookNotificationChannel(cfg, nil)
	if err := channel.Test(context.Background(), NotificationEvent{Type: NotificationEventTest, Message: "test"}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if err := channel.Send(context.Background(), NotificationEvent{Message: "test"}); err == nil {
		t.Fatal("Send() unexpectedly ignored disabled switch")
	}
}
