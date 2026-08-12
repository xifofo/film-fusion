package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

var rssAutomationTemplatePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

func (s *RSSAutomationService) TestTarget(ctx context.Context, targetID uint) error {
	var target model.RSSAutomationTarget
	if err := s.db.First(&target, targetID).Error; err != nil {
		return err
	}
	if !target.Enabled {
		return errors.New("目标已停用")
	}
	switch target.Type {
	case model.RSSAutomationTargetQBittorrent:
		_, err := s.newRSSAutomationQBClient(ctx, target)
		return err
	default:
		return fmt.Errorf("不支持的目标类型 %q", target.Type)
	}
}

func (s *RSSAutomationService) executeRSSAutomationQBittorrent(ctx context.Context, node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	targetID := rssAutomationConfigUint(node.Config, "target_id")
	var target model.RSSAutomationTarget
	if err := s.db.First(&target, targetID).Error; err != nil {
		return nil, fmt.Errorf("qBittorrent 目标不存在: %w", err)
	}
	if !target.Enabled {
		return nil, errors.New("qBittorrent 目标已停用")
	}
	client, err := s.newRSSAutomationQBClient(ctx, target)
	if err != nil {
		return nil, err
	}
	downloadURL, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "url"))
	if err != nil {
		return nil, err
	}
	if err := validateRSSAutomationDownloadURL(downloadURL); err != nil {
		return nil, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"urls":     downloadURL,
		"savepath": renderRSSAutomationTemplate(rssAutomationConfigString(node.Config, "save_path"), runContext),
		"category": renderRSSAutomationTemplate(rssAutomationConfigString(node.Config, "category"), runContext),
		"tags":     renderRSSAutomationTemplate(rssAutomationConfigString(node.Config, "tags"), runContext),
	}
	if rssAutomationConfigBool(node.Config, "paused") {
		fields["paused"] = "true"
	}
	if rssAutomationConfigBool(node.Config, "sequential") {
		fields["sequentialDownload"] = "true"
	}
	for key, value := range fields {
		if strings.TrimSpace(value) != "" {
			_ = writer.WriteField(key, value)
		}
	}
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(client.baseURL, "/")+"/api/v2/torrents/add", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("提交 qBittorrent 任务失败: %w", err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.EqualFold(strings.TrimSpace(string(responseBody)), "Ok.") {
		return nil, fmt.Errorf("qBittorrent 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return map[string]any{
		"target_id": target.ID, "target_name": target.Name,
		"content_key": rssAutomationContentKey(downloadURL), "submitted": true,
	}, nil
}

type rssAutomationQBClient struct {
	baseURL string
	client  *http.Client
}

func (s *RSSAutomationService) newRSSAutomationQBClient(ctx context.Context, target model.RSSAutomationTarget) (*rssAutomationQBClient, error) {
	var config RSSAutomationQBittorrentConfig
	if err := json.Unmarshal([]byte(target.ConfigJSON), &config); err != nil {
		return nil, errors.New("qBittorrent 目标配置损坏")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 20 * time.Second, Jar: jar}
	form := url.Values{"username": {config.Username}, "password": {config.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(config.BaseURL, "/")+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 qBittorrent 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.EqualFold(strings.TrimSpace(string(body)), "Ok.") {
		return nil, fmt.Errorf("qBittorrent 登录失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return &rssAutomationQBClient{baseURL: config.BaseURL, client: client}, nil
}

func (s *RSSAutomationService) executeRSSAutomationOffline115(ctx context.Context, node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	storageID := rssAutomationConfigUint(node.Config, "cloud_storage_id")
	var storage model.CloudStorage
	if err := s.db.First(&storage, storageID).Error; err != nil {
		return nil, fmt.Errorf("115 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, errors.New("所选云存储不是 115 OpenAPI 账号")
	}
	if storage.Status != model.StatusActive {
		return nil, fmt.Errorf("115 账号当前状态为 %s", storage.Status)
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		return nil, errors.New("115 账号没有可用 Cookie")
	}
	downloadURL, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "url"))
	if err != nil {
		return nil, err
	}
	if err := validateRSSAutomationDownloadURL(downloadURL); err != nil {
		return nil, err
	}
	directoryID := strings.TrimSpace(rssAutomationConfigString(node.Config, "directory_id"))
	if directoryID == "" {
		directoryID = "0"
	}
	timeout := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	client, err := s.web115.NewClientWithContext(ctx, storage.Cookie, timeout)
	if err != nil {
		return nil, err
	}
	hashes, err := client.AddOfflineTaskURIs([]string{downloadURL}, directoryID)
	if err != nil {
		return nil, fmt.Errorf("提交 115 离线任务失败: %w", err)
	}
	return map[string]any{
		"cloud_storage_id": storage.ID, "storage_name": storage.StorageName,
		"directory_id": directoryID, "hashes": hashes,
		"access_method": "cookie",
		"content_key":   rssAutomationContentKey(downloadURL), "submitted": true,
	}, nil
}

func (s *RSSAutomationService) executeRSSAutomationOffline115OpenAPI(ctx context.Context, node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	storageID := rssAutomationConfigUint(node.Config, "cloud_storage_id")
	var storage model.CloudStorage
	if err := s.db.First(&storage, storageID).Error; err != nil {
		return nil, fmt.Errorf("115 OpenAPI 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, errors.New("所选云存储不是 115 OpenAPI 账号")
	}
	if storage.Status != model.StatusActive {
		return nil, fmt.Errorf("115 OpenAPI 账号当前状态为 %s", storage.Status)
	}
	accessToken := strings.TrimSpace(storage.AccessToken)
	if accessToken == "" {
		return nil, errors.New("115 OpenAPI 账号没有可用 AccessToken")
	}

	downloadURL, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "url"))
	if err != nil {
		return nil, err
	}
	if err := validateRSSAutomationDownloadURL(downloadURL); err != nil {
		return nil, err
	}
	directoryID := strings.TrimSpace(rssAutomationConfigString(node.Config, "directory_id"))
	if directoryID == "" {
		directoryID = "0"
	}

	client := sdk115.New(sdk115.WithAccessToken(accessToken))
	if s.httpClient != nil {
		client.SetHttpClient(s.httpClient)
	}
	hashes, err := client.AddOfflineTaskURIs(ctx, []string{downloadURL}, directoryID)
	if err != nil {
		return nil, fmt.Errorf("通过 115 OpenAPI 提交离线任务失败: %w", err)
	}
	if len(hashes) == 0 {
		return nil, errors.New("115 OpenAPI 未返回已创建的离线任务")
	}
	return map[string]any{
		"cloud_storage_id": storage.ID, "storage_name": storage.StorageName,
		"directory_id": directoryID, "hashes": hashes,
		"access_method": "openapi",
		"content_key":   rssAutomationContentKey(downloadURL), "submitted": true,
	}, nil
}

func (s *RSSAutomationService) executeRSSAutomationNotification(ctx context.Context, node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.notifier == nil {
		return nil, errors.New("通知服务未初始化")
	}
	message := renderRSSAutomationTemplate(rssAutomationConfigString(node.Config, "message"), runContext)
	title := renderRSSAutomationTemplate(rssAutomationConfigString(node.Config, "title"), runContext)
	if strings.TrimSpace(message) == "" {
		return nil, errors.New("渲染后的通知内容为空")
	}
	report := s.notifier.Publish(ctx, NotificationEvent{
		Type: NotificationEventRSSMatched, Title: title, Message: message,
		Severity: NotificationSeverityInfo, OccurredAt: time.Now(),
		Metadata: map[string]string{"source": "rss_automation"},
	})
	output := map[string]any{"skipped": report.Skipped, "skip_reason": report.SkipReason, "deliveries": report.Deliveries}
	if report.Skipped {
		return output, nil
	}
	if !report.AnySuccess() {
		return output, fmt.Errorf("通知发送失败: %s", report.FailureMessage())
	}
	if report.HasFailures() {
		output["partial"] = true
		output["warning"] = report.FailureMessage()
	}
	return output, nil
}

func validateRSSAutomationDownloadURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("下载地址为空")
	}
	if len(value) > 65535 {
		return errors.New("下载地址超过 65535 字节限制")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("下载地址格式无效")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "magnet":
		if !strings.Contains(strings.ToLower(value), "xt=urn:btih:") {
			return errors.New("磁力链接缺少 BTIH")
		}
	case "http", "https":
		if parsed.Host == "" {
			return errors.New("HTTP 下载地址缺少主机名")
		}
	default:
		return fmt.Errorf("不支持下载地址协议 %q", parsed.Scheme)
	}
	return nil
}

func renderRSSAutomationTemplate(template string, runContext map[string]any) string {
	return strings.TrimSpace(rssAutomationTemplatePattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := rssAutomationTemplatePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		value, ok := resolveRSSAutomationReference(runContext, parts[1])
		if !ok || value == nil {
			return ""
		}
		switch typed := value.(type) {
		case string:
			return typed
		case json.Number:
			return typed.String()
		default:
			return fmt.Sprint(typed)
		}
	}))
}

func resolveRSSAutomationString(runContext map[string]any, expression string) (string, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return "", errors.New("变量引用为空")
	}
	if strings.Contains(expression, "{{") {
		value := renderRSSAutomationTemplate(expression, runContext)
		if value == "" {
			return "", fmt.Errorf("模板 %q 没有解析出值", expression)
		}
		return value, nil
	}
	value, ok := resolveRSSAutomationReference(runContext, expression)
	if !ok || value == nil {
		return "", fmt.Errorf("变量 %q 不存在", expression)
	}
	return strings.TrimSpace(fmt.Sprint(value)), nil
}

func resolveRSSAutomationReference(runContext map[string]any, reference string) (any, bool) {
	reference = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reference), "$"))
	if reference == "" {
		return nil, false
	}
	parts := strings.Split(reference, ".")
	var current any = runContext
	for _, part := range parts {
		part = strings.TrimSpace(part)
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func rssAutomationConfigBool(config map[string]any, key string) bool {
	value, ok := config[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	case float64:
		return typed != 0
	default:
		return false
	}
}
