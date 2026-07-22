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

const maxTelegramResponseBytes = 1 << 20

type SecurityAlert struct {
	Source       string
	IP           string
	Username     string
	Scope        string
	FailureCount int
	BlockedUntil time.Time
	TriggeredAt  time.Time
}

type SecurityAlertNotifier interface {
	NotifySecurityAlert(alert SecurityAlert)
}

type TelegramNotifier struct {
	cfg    *config.Config
	logger *logger.Logger
	client *http.Client
}

func NewTelegramNotifier(cfg *config.Config, log *logger.Logger) *TelegramNotifier {
	return &TelegramNotifier{cfg: cfg, logger: log, client: &http.Client{}}
}

// NotifySecurityAlert 快速返回，实际网络请求在独立 goroutine 中执行。
func (n *TelegramNotifier) NotifySecurityAlert(alert SecurityAlert) {
	if n == nil || n.cfg == nil {
		return
	}
	settings := n.cfg.Telegram
	if !settings.Enabled || !securityAlertEnabled(settings, alert.Source) {
		return
	}
	go func() {
		if err := n.send(context.Background(), settings, formatSecurityAlert(settings.InstanceName, alert)); err != nil && n.logger != nil {
			n.logger.Errorf("[TELEGRAM] 安全告警发送失败: %v", err)
		}
	}()
}

func securityAlertEnabled(settings config.TelegramConfig, source string) bool {
	switch source {
	case "emby":
		return settings.NotifyEmbyBruteForce
	case "filmfusion":
		return settings.NotifySystemBruteForce
	default:
		return false
	}
}

func formatSecurityAlert(instance string, alert SecurityAlert) string {
	if strings.TrimSpace(instance) == "" {
		instance = "FilmFusion"
	}
	source := "FilmFusion 后台"
	if alert.Source == "emby" {
		source = "Emby"
	}
	scope := "IP"
	if alert.Scope == "account_ip" {
		scope = "账号 + IP"
	}
	lines := []string{
		"[安全告警] " + instance,
		"来源: " + source,
		"事件: 登录失败达到阈值，已临时封禁",
		"范围: " + scope,
		"IP: " + emptyFallback(alert.IP, "unknown"),
	}
	if strings.TrimSpace(alert.Username) != "" {
		lines = append(lines, "账号: "+alert.Username)
	}
	if alert.FailureCount > 0 {
		lines = append(lines, "失败次数: "+strconv.Itoa(alert.FailureCount))
	}
	if !alert.BlockedUntil.IsZero() {
		lines = append(lines, "封禁至: "+alert.BlockedUntil.Local().Format("2006-01-02 15:04:05 MST"))
	}
	triggeredAt := alert.TriggeredAt
	if triggeredAt.IsZero() {
		triggeredAt = time.Now()
	}
	lines = append(lines, "触发时间: "+triggeredAt.Local().Format("2006-01-02 15:04:05 MST"))
	return strings.Join(lines, "\n")
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (n *TelegramNotifier) SendTest(ctx context.Context) error {
	if n == nil || n.cfg == nil {
		return fmt.Errorf("Telegram 通知服务未初始化")
	}
	settings := n.cfg.Telegram
	instance := emptyFallback(settings.InstanceName, "FilmFusion")
	message := fmt.Sprintf("[测试通知] %s\nTelegram 通知连接正常。\n发送时间: %s", instance, time.Now().Local().Format("2006-01-02 15:04:05 MST"))
	return n.send(ctx, settings, message)
}

// SendMessage sends a business notification through the saved Telegram target.
func (n *TelegramNotifier) SendMessage(ctx context.Context, text string) error {
	if n == nil || n.cfg == nil {
		return fmt.Errorf("Telegram 通知服务未初始化")
	}
	if !n.cfg.Telegram.Enabled {
		return fmt.Errorf("Telegram 通知未启用")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("Telegram 通知内容不能为空")
	}
	return n.send(ctx, n.cfg.Telegram, text)
}

// SendPhoto sends a remote image with an optional caption to the saved Telegram target.
func (n *TelegramNotifier) SendPhoto(ctx context.Context, photoURL, caption string) error {
	if n == nil || n.cfg == nil {
		return fmt.Errorf("Telegram 通知服务未初始化")
	}
	if !n.cfg.Telegram.Enabled {
		return fmt.Errorf("Telegram 通知未启用")
	}
	photoURL = strings.TrimSpace(photoURL)
	if photoURL == "" {
		return fmt.Errorf("Telegram 图片地址不能为空")
	}
	return n.sendPhoto(ctx, n.cfg.Telegram, photoURL, caption)
}

func (n *TelegramNotifier) send(parent context.Context, settings config.TelegramConfig, text string) error {
	form := url.Values{
		"chat_id":              {settings.ChatID},
		"text":                 {text},
		"disable_notification": {strconv.FormatBool(settings.Silent)},
	}
	if settings.MessageThreadID > 0 {
		form.Set("message_thread_id", strconv.FormatInt(settings.MessageThreadID, 10))
	}
	return n.postForm(parent, settings, "sendMessage", form)
}

func (n *TelegramNotifier) sendPhoto(parent context.Context, settings config.TelegramConfig, photoURL, caption string) error {
	form := url.Values{
		"chat_id":              {settings.ChatID},
		"photo":                {photoURL},
		"caption":              {caption},
		"disable_notification": {strconv.FormatBool(settings.Silent)},
	}
	if settings.MessageThreadID > 0 {
		form.Set("message_thread_id", strconv.FormatInt(settings.MessageThreadID, 10))
	}
	return n.postForm(parent, settings, "sendPhoto", form)
}

func (n *TelegramNotifier) postForm(parent context.Context, settings config.TelegramConfig, method string, form url.Values) error {
	if strings.TrimSpace(settings.BotToken) == "" || strings.TrimSpace(settings.ChatID) == "" {
		return fmt.Errorf("请先配置 Bot Token 和 Chat ID")
	}
	if settings.TimeoutSeconds <= 0 {
		settings.TimeoutSeconds = 10
	}
	if strings.TrimSpace(settings.APIBase) == "" {
		settings.APIBase = "https://api.telegram.org"
	}

	ctx, cancel := context.WithTimeout(parent, time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(settings.APIBase, "/") + "/bot" + url.PathEscape(settings.BotToken) + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("创建 Telegram 请求失败")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := n.client.Do(req)
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
