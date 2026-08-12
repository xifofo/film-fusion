package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
)

const maxNotificationWebhookResponseBytes = 1 << 20

type WebhookNotificationChannel struct {
	cfg    *config.Config
	logger *logger.Logger
	client *http.Client
}

func NewWebhookNotificationChannel(cfg *config.Config, log *logger.Logger) *WebhookNotificationChannel {
	return &WebhookNotificationChannel{cfg: cfg, logger: log, client: &http.Client{}}
}

func (c *WebhookNotificationChannel) ID() string { return config.NotificationChannelWebhook }

func (c *WebhookNotificationChannel) Ready() bool {
	if c == nil || c.cfg == nil {
		return false
	}
	settings := c.cfg.Notifications.Webhook
	return settings.Enabled && strings.TrimSpace(settings.URL) != ""
}

func (c *WebhookNotificationChannel) Send(ctx context.Context, event NotificationEvent) error {
	if c == nil || c.cfg == nil {
		return fmt.Errorf("Webhook 通知渠道未初始化")
	}
	settings := c.cfg.Notifications.Webhook
	if !settings.Enabled {
		return fmt.Errorf("Webhook 通知渠道未启用")
	}
	return c.post(ctx, settings, event)
}

// Test 使用已保存的 URL 和 Token 测试连接，即使渠道开关尚未启用也允许发送。
func (c *WebhookNotificationChannel) Test(ctx context.Context, event NotificationEvent) error {
	if c == nil || c.cfg == nil {
		return fmt.Errorf("Webhook 通知渠道未初始化")
	}
	settings := c.cfg.Notifications.Webhook
	settings.Enabled = true
	if err := config.ValidateNotificationWebhook(settings); err != nil {
		return err
	}
	return c.post(ctx, settings, event)
}

func (c *WebhookNotificationChannel) post(parent context.Context, settings config.NotificationWebhookConfig, event NotificationEvent) error {
	if settings.TimeoutSeconds <= 0 {
		settings.TimeoutSeconds = 10
	}
	payload := struct {
		Instance string `json:"instance"`
		NotificationEvent
	}{
		Instance:          notificationInstanceName(c.cfg),
		NotificationEvent: event,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化通知 Webhook 请求失败")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建通知 Webhook 请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "FilmFusion-Notification/1.0")
	req.Header.Set("X-FilmFusion-Event", string(event.Type))
	if strings.TrimSpace(settings.Token) != "" {
		req.Header.Set("Authorization", "Bearer "+settings.Token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		message := err.Error()
		message = strings.ReplaceAll(message, settings.URL, "[REDACTED_URL]")
		message = redactNotificationSecret(message, settings.Token)
		return fmt.Errorf("请求通知 Webhook 失败: %s", message)
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxNotificationWebhookResponseBytes))
	if readErr != nil {
		return fmt.Errorf("读取通知 Webhook 响应失败")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail := strings.TrimSpace(string(responseBody))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		detail = truncateNotificationRunes(redactNotificationSecret(detail, settings.Token), 500)
		return fmt.Errorf("通知 Webhook 返回 HTTP %d: %s", resp.StatusCode, detail)
	}
	return nil
}

func redactNotificationSecret(message, secret string) string {
	if secret == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[REDACTED]")
}
