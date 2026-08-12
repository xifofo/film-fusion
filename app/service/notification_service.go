package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
)

type NotificationEventType string

const (
	NotificationEventEmbySecurity  NotificationEventType = config.NotificationEventEmbySecurity
	NotificationEventAppSecurity   NotificationEventType = config.NotificationEventAppSecurity
	NotificationEventRSSMatched    NotificationEventType = config.NotificationEventRSSMatched
	NotificationEventWeb115Invalid NotificationEventType = config.NotificationEventWeb115Invalid
	NotificationEventTest          NotificationEventType = "system.test"
)

type NotificationSeverity string

const (
	NotificationSeverityInfo     NotificationSeverity = "info"
	NotificationSeverityWarning  NotificationSeverity = "warning"
	NotificationSeverityCritical NotificationSeverity = "critical"
)

// NotificationEvent 是业务层与投递渠道之间的稳定契约。
type NotificationEvent struct {
	Type       NotificationEventType `json:"event"`
	Title      string                `json:"title,omitempty"`
	Message    string                `json:"message"`
	ImageURL   string                `json:"image_url,omitempty"`
	Severity   NotificationSeverity  `json:"severity"`
	OccurredAt time.Time             `json:"occurred_at"`
	Metadata   map[string]string     `json:"metadata,omitempty"`
}

type NotificationDelivery struct {
	Channel string `json:"channel"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type NotificationReport struct {
	Event      NotificationEventType  `json:"event"`
	Skipped    bool                   `json:"skipped"`
	SkipReason string                 `json:"skip_reason,omitempty"`
	Deliveries []NotificationDelivery `json:"deliveries"`
}

func (r NotificationReport) AnySuccess() bool {
	for _, delivery := range r.Deliveries {
		if delivery.Success {
			return true
		}
	}
	return false
}

func (r NotificationReport) HasFailures() bool {
	for _, delivery := range r.Deliveries {
		if !delivery.Success {
			return true
		}
	}
	return false
}

func (r NotificationReport) FailureMessage() string {
	parts := make([]string, 0, len(r.Deliveries))
	for _, delivery := range r.Deliveries {
		if delivery.Success {
			continue
		}
		channel := strings.TrimSpace(delivery.Channel)
		if channel == "" {
			channel = "unknown"
		}
		message := strings.TrimSpace(delivery.Error)
		if message == "" {
			message = "投递失败"
		}
		parts = append(parts, channel+": "+message)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func (r NotificationReport) Err() error {
	if message := r.FailureMessage(); message != "" {
		return fmt.Errorf("%s", message)
	}
	return nil
}

type NotificationPublisher interface {
	Publish(ctx context.Context, event NotificationEvent) NotificationReport
	Ready(eventType NotificationEventType) bool
}

type NotificationChannel interface {
	ID() string
	Ready() bool
	Send(ctx context.Context, event NotificationEvent) error
	Test(ctx context.Context, event NotificationEvent) error
}

type NotificationService struct {
	cfg      *config.Config
	logger   *logger.Logger
	channels map[string]NotificationChannel
}

func NewNotificationService(cfg *config.Config, log *logger.Logger, customChannels ...NotificationChannel) *NotificationService {
	service := &NotificationService{
		cfg: cfg, logger: log, channels: make(map[string]NotificationChannel),
	}
	channels := customChannels
	if len(channels) == 0 {
		channels = []NotificationChannel{
			NewTelegramChannel(cfg, log),
			NewWebhookNotificationChannel(cfg, log),
		}
	}
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(channel.ID()))
		if id != "" {
			service.channels[id] = channel
		}
	}
	return service
}

// Ready 在事件未配置路由时也返回 true，表示用户明确关闭了该事件而非配置故障。
func (s *NotificationService) Ready(eventType NotificationEventType) bool {
	channels := s.route(eventType)
	if len(channels) == 0 {
		return true
	}
	for _, id := range channels {
		channel := s.channels[id]
		if channel != nil && channel.Ready() {
			return true
		}
	}
	return false
}

func (s *NotificationService) Publish(ctx context.Context, event NotificationEvent) NotificationReport {
	report := NotificationReport{Event: event.Type, Deliveries: []NotificationDelivery{}}
	if s == nil || s.cfg == nil {
		report.Deliveries = append(report.Deliveries, NotificationDelivery{Error: "通知服务未初始化"})
		return report
	}
	if event.Type == "" {
		report.Deliveries = append(report.Deliveries, NotificationDelivery{Error: "通知事件类型不能为空"})
		return report
	}
	if strings.TrimSpace(event.Title) == "" && strings.TrimSpace(event.Message) == "" {
		report.Deliveries = append(report.Deliveries, NotificationDelivery{Error: "通知内容不能为空"})
		return report
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if event.Severity == "" {
		event.Severity = NotificationSeverityInfo
	}
	if ctx == nil {
		ctx = context.Background()
	}

	routes := s.route(event.Type)
	if len(routes) == 0 {
		report.Skipped = true
		report.SkipReason = "该事件未配置投递渠道"
		return report
	}

	report.Deliveries = make([]NotificationDelivery, len(routes))
	var wg sync.WaitGroup
	for index, id := range routes {
		channel := s.channels[id]
		if channel == nil {
			report.Deliveries[index] = NotificationDelivery{Channel: id, Error: "通知渠道不存在"}
			continue
		}
		if !channel.Ready() {
			report.Deliveries[index] = NotificationDelivery{Channel: id, Error: "通知渠道未启用或配置不完整"}
			continue
		}
		wg.Add(1)
		go func(index int, id string, channel NotificationChannel) {
			defer wg.Done()
			delivery := NotificationDelivery{Channel: id}
			if err := channel.Send(ctx, event); err != nil {
				delivery.Error = err.Error()
				if s.logger != nil {
					s.logger.Errorf("[NOTIFICATION] event=%s channel=%s 投递失败: %v", event.Type, id, err)
				}
			} else {
				delivery.Success = true
			}
			report.Deliveries[index] = delivery
		}(index, id, channel)
	}
	wg.Wait()
	return report
}

func (s *NotificationService) TestChannel(ctx context.Context, channelID string) error {
	if s == nil || s.cfg == nil {
		return fmt.Errorf("通知服务未初始化")
	}
	channelID = strings.ToLower(strings.TrimSpace(channelID))
	channel := s.channels[channelID]
	if channel == nil {
		return fmt.Errorf("不支持的通知渠道: %s", channelID)
	}
	instance := notificationInstanceName(s.cfg)
	now := time.Now()
	return channel.Test(ctx, NotificationEvent{
		Type:       NotificationEventTest,
		Title:      "[测试通知] " + instance,
		Message:    "FilmFusion 通知渠道连接正常。\n发送时间: " + now.Local().Format("2006-01-02 15:04:05 MST"),
		Severity:   NotificationSeverityInfo,
		OccurredAt: now,
		Metadata:   map[string]string{"channel": channelID},
	})
}

func (s *NotificationService) route(eventType NotificationEventType) []string {
	if s == nil || s.cfg == nil {
		return nil
	}
	raw := s.cfg.Notifications.Routes.Channels(string(eventType))
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, id := range raw {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

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

// NotifySecurityAlert 快速返回，实际投递由独立 goroutine 完成。
func (s *NotificationService) NotifySecurityAlert(alert SecurityAlert) {
	if s == nil {
		return
	}
	if alert.Source != "emby" && alert.Source != "filmfusion" {
		return
	}
	event := securityAlertNotification(notificationInstanceName(s.cfg), alert)
	go func() {
		report := s.Publish(context.Background(), event)
		if err := report.Err(); err != nil && s.logger != nil {
			s.logger.Errorf("[NOTIFICATION] 安全告警发送失败: %v", err)
		}
	}()
}

func securityAlertNotification(instance string, alert SecurityAlert) NotificationEvent {
	eventType := NotificationEventAppSecurity
	source := "FilmFusion 后台"
	if alert.Source == "emby" {
		eventType = NotificationEventEmbySecurity
		source = "Emby"
	}
	scope := "IP"
	if alert.Scope == "account_ip" {
		scope = "账号 + IP"
	}
	lines := []string{
		"来源: " + source,
		"事件: 登录失败达到阈值，已临时封禁",
		"范围: " + scope,
		"IP: " + notificationFallback(alert.IP, "unknown"),
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
	return NotificationEvent{
		Type: eventType, Title: "[安全告警] " + notificationFallback(instance, "FilmFusion"),
		Message: strings.Join(lines, "\n"), Severity: NotificationSeverityCritical,
		OccurredAt: triggeredAt,
		Metadata: map[string]string{
			"source": alert.Source, "ip": alert.IP, "username": alert.Username,
			"scope": alert.Scope, "failure_count": strconv.Itoa(alert.FailureCount),
		},
	}
}

func notificationInstanceName(cfg *config.Config) string {
	if cfg == nil {
		return "FilmFusion"
	}
	return notificationFallback(cfg.Notifications.InstanceName, "FilmFusion")
}

func notificationFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func renderNotificationText(event NotificationEvent) string {
	parts := make([]string, 0, 2)
	if title := strings.TrimSpace(event.Title); title != "" {
		parts = append(parts, title)
	}
	if message := strings.TrimSpace(event.Message); message != "" {
		parts = append(parts, message)
	}
	return strings.Join(parts, "\n")
}

func truncateNotificationRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
