package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
)

const (
	maxTelegramResponseBytes = 1 << 20
	maxTelegramCaptionRunes  = 1024
	maxTelegramMessageRunes  = 4096
)

// TelegramChannel 只负责把统一通知事件适配为 Telegram Bot API 请求。
type TelegramChannel struct {
	cfg    *config.Config
	logger *logger.Logger
	client *http.Client
}

func NewTelegramChannel(cfg *config.Config, log *logger.Logger) *TelegramChannel {
	return &TelegramChannel{cfg: cfg, logger: log, client: &http.Client{}}
}

func (c *TelegramChannel) ID() string { return config.NotificationChannelTelegram }

func (c *TelegramChannel) Ready() bool {
	if c == nil || c.cfg == nil {
		return false
	}
	settings := c.cfg.Notifications.Telegram
	return settings.Enabled && strings.TrimSpace(settings.BotToken) != "" && strings.TrimSpace(settings.ChatID) != ""
}

func (c *TelegramChannel) Send(ctx context.Context, event NotificationEvent) error {
	if c == nil || c.cfg == nil {
		return fmt.Errorf("Telegram 通知渠道未初始化")
	}
	settings := c.cfg.Notifications.Telegram
	if !settings.Enabled {
		return fmt.Errorf("Telegram 通知渠道未启用")
	}
	return c.sendEvent(ctx, settings, event)
}

// Test 使用已保存的连接参数测试渠道，即使渠道开关尚未启用也允许发送。
func (c *TelegramChannel) Test(ctx context.Context, event NotificationEvent) error {
	if c == nil || c.cfg == nil {
		return fmt.Errorf("Telegram 通知渠道未初始化")
	}
	return c.sendEvent(ctx, c.cfg.Notifications.Telegram, event)
}

func (c *TelegramChannel) sendEvent(ctx context.Context, settings config.TelegramChannelConfig, event NotificationEvent) error {
	text := renderNotificationText(event)
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("Telegram 通知内容不能为空")
	}
	text = truncateTelegramText(text, maxTelegramMessageRunes)
	if strings.TrimSpace(event.ImageURL) != "" {
		caption := truncateTelegramText(renderNotificationText(event), maxTelegramCaptionRunes)
		if err := c.sendPhoto(ctx, settings, event.ImageURL, caption); err == nil {
			return nil
		} else if c.logger != nil {
			c.logger.Warnf("[TELEGRAM] 图片通知发送失败，降级为文本通知: %v", err)
		}
	}
	return c.sendText(ctx, settings, text)
}

func (c *TelegramChannel) sendText(parent context.Context, settings config.TelegramChannelConfig, text string) error {
	form := url.Values{
		"chat_id":              {settings.ChatID},
		"text":                 {text},
		"disable_notification": {strconv.FormatBool(settings.Silent)},
	}
	if settings.MessageThreadID > 0 {
		form.Set("message_thread_id", strconv.FormatInt(settings.MessageThreadID, 10))
	}
	return c.postForm(parent, settings, "sendMessage", form)
}

func (c *TelegramChannel) sendPhoto(parent context.Context, settings config.TelegramChannelConfig, photoURL, caption string) error {
	form := url.Values{
		"chat_id":              {settings.ChatID},
		"photo":                {strings.TrimSpace(photoURL)},
		"caption":              {caption},
		"disable_notification": {strconv.FormatBool(settings.Silent)},
	}
	if form.Get("photo") == "" {
		return fmt.Errorf("Telegram 图片地址不能为空")
	}
	if settings.MessageThreadID > 0 {
		form.Set("message_thread_id", strconv.FormatInt(settings.MessageThreadID, 10))
	}
	return c.postForm(parent, settings, "sendPhoto", form)
}

func (c *TelegramChannel) postForm(parent context.Context, settings config.TelegramChannelConfig, method string, form url.Values) error {
	if strings.TrimSpace(settings.BotToken) == "" || strings.TrimSpace(settings.ChatID) == "" {
		return fmt.Errorf("请先配置 Telegram Bot Token 和 Chat ID")
	}
	if settings.TimeoutSeconds <= 0 {
		settings.TimeoutSeconds = 10
	}
	if strings.TrimSpace(settings.APIBase) == "" {
		settings.APIBase = "https://api.telegram.org"
	}
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithTimeout(parent, time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(settings.APIBase, "/") + "/bot" + url.PathEscape(settings.BotToken) + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("创建 Telegram 请求失败")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Telegram API 失败: %s", redactTelegramError(err.Error(), settings.BotToken))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTelegramResponseBytes))
	if err != nil {
		return fmt.Errorf("读取 Telegram 响应失败")
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("Telegram 返回了无法解析的响应 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.OK {
		description := strings.TrimSpace(result.Description)
		if description == "" {
			description = http.StatusText(resp.StatusCode)
		}
		description = redactTelegramError(description, settings.BotToken)
		return fmt.Errorf("Telegram 发送失败 (HTTP %d): %s", resp.StatusCode, description)
	}
	return nil
}

func redactTelegramError(message, token string) string {
	if token == "" {
		return message
	}
	return strings.ReplaceAll(message, token, "[REDACTED]")
}

func truncateTelegramText(message string, limit int) string {
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if limit <= 0 || len(runes) <= limit {
		return message
	}
	lines := strings.Split(message, "\n")
	lastLine := strings.TrimSpace(lines[len(lines)-1])
	lastLineRunes := []rune(lastLine)
	if len(lines) > 1 && (strings.HasPrefix(lastLine, "http://") || strings.HasPrefix(lastLine, "https://")) && len(lastLineRunes)+4 < limit {
		prefix := strings.TrimSpace(strings.Join(lines[:len(lines)-1], "\n"))
		prefixLimit := limit - len(lastLineRunes) - 1
		return truncateNotificationRunes(prefix, prefixLimit) + "\n" + lastLine
	}
	return truncateNotificationRunes(message, limit)
}
