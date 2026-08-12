package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"film-fusion/app/config"
)

type recordingNotificationChannel struct {
	id      string
	ready   bool
	sendErr error
	mu      sync.Mutex
	events  []NotificationEvent
	tests   []NotificationEvent
}

func (c *recordingNotificationChannel) ID() string  { return c.id }
func (c *recordingNotificationChannel) Ready() bool { return c.ready }

func (c *recordingNotificationChannel) Send(_ context.Context, event NotificationEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return c.sendErr
}

func (c *recordingNotificationChannel) Test(_ context.Context, event NotificationEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tests = append(c.tests, event)
	return c.sendErr
}

func TestNotificationServiceRoutesToMultipleChannels(t *testing.T) {
	telegram := &recordingNotificationChannel{id: config.NotificationChannelTelegram, ready: true}
	webhook := &recordingNotificationChannel{
		id: config.NotificationChannelWebhook, ready: true, sendErr: errors.New("receiver unavailable"),
	}
	cfg := &config.Config{Notifications: config.NotificationConfig{
		Routes: config.NotificationRoutesConfig{
			RSSMatched: []string{config.NotificationChannelTelegram, config.NotificationChannelWebhook},
		},
	}}
	notifications := NewNotificationService(cfg, nil, telegram, webhook)
	report := notifications.Publish(context.Background(), NotificationEvent{
		Type: NotificationEventRSSMatched, Message: "new media",
	})

	if !report.AnySuccess() || !report.HasFailures() || report.Skipped {
		t.Fatalf("unexpected report: %+v", report)
	}
	if !strings.Contains(report.FailureMessage(), "webhook") || len(telegram.events) != 1 || len(webhook.events) != 1 {
		t.Fatalf("unexpected deliveries: report=%+v telegram=%d webhook=%d", report, len(telegram.events), len(webhook.events))
	}
}

func TestNotificationServiceTreatsEmptyRouteAsIntentionalSkip(t *testing.T) {
	cfg := &config.Config{Notifications: config.NotificationConfig{}}
	notifications := NewNotificationService(cfg, nil, &recordingNotificationChannel{
		id: config.NotificationChannelTelegram, ready: false,
	})
	report := notifications.Publish(context.Background(), NotificationEvent{
		Type: NotificationEventRSSMatched, Message: "new media",
	})
	if !report.Skipped || report.Err() != nil || !notifications.Ready(NotificationEventRSSMatched) {
		t.Fatalf("empty route should be an intentional ready skip: %+v", report)
	}
}

func TestNotificationServiceReportsUnreadyConfiguredChannel(t *testing.T) {
	cfg := &config.Config{Notifications: config.NotificationConfig{
		Routes: config.NotificationRoutesConfig{RSSMatched: []string{config.NotificationChannelTelegram}},
	}}
	notifications := NewNotificationService(cfg, nil, &recordingNotificationChannel{
		id: config.NotificationChannelTelegram, ready: false,
	})
	report := notifications.Publish(context.Background(), NotificationEvent{
		Type: NotificationEventRSSMatched, Message: "new media",
	})
	if report.AnySuccess() || !report.HasFailures() || notifications.Ready(NotificationEventRSSMatched) {
		t.Fatalf("unready routed channel was not reported: %+v", report)
	}
}

func TestNotificationServiceTestsNamedChannel(t *testing.T) {
	channel := &recordingNotificationChannel{id: config.NotificationChannelWebhook, ready: false}
	cfg := &config.Config{Notifications: config.NotificationConfig{InstanceName: "家庭媒体"}}
	notifications := NewNotificationService(cfg, nil, channel)
	if err := notifications.TestChannel(context.Background(), config.NotificationChannelWebhook); err != nil {
		t.Fatalf("TestChannel() error = %v", err)
	}
	if len(channel.tests) != 1 || channel.tests[0].Type != NotificationEventTest || !strings.Contains(channel.tests[0].Title, "家庭媒体") {
		t.Fatalf("unexpected test event: %+v", channel.tests)
	}
}
