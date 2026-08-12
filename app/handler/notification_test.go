package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type testNotificationChannel struct{ id string }

func (c testNotificationChannel) ID() string { return c.id }
func (testNotificationChannel) Ready() bool  { return true }
func (testNotificationChannel) Send(context.Context, service.NotificationEvent) error {
	return nil
}
func (testNotificationChannel) Test(context.Context, service.NotificationEvent) error {
	return nil
}

func TestNotificationHandlerTestsSelectedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Notifications: config.NotificationConfig{InstanceName: "FilmFusion"}}
	notifications := service.NewNotificationService(cfg, nil,
		testNotificationChannel{id: config.NotificationChannelWebhook},
	)
	handler := NewNotificationHandler(notifications)
	router := gin.New()
	router.POST("/api/notifications/channels/:channel/test", handler.TestChannel)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/notifications/channels/webhook/test", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/notifications/channels/email/test", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsupported channel status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}
