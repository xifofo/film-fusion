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
	"unicode"
	"unicode/utf8"

	"film-fusion/app/model"
)

var automationTemplatePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

func (s *AutomationService) TestTarget(ctx context.Context, targetID uint) error {
	var target model.AutomationTarget
	if err := s.db.First(&target, targetID).Error; err != nil {
		return err
	}
	if !target.Enabled {
		return errors.New("目标已停用")
	}
	switch target.Type {
	case model.AutomationTargetQBittorrent:
		client, err := s.newAutomationQBClient(ctx, target)
		if err != nil {
			return err
		}
		return client.testConnection(ctx)
	default:
		return fmt.Errorf("不支持的目标类型 %q", target.Type)
	}
}

type automationQBAddResponse struct {
	SuccessCount    int      `json:"success_count"`
	PendingCount    int      `json:"pending_count"`
	FailureCount    int      `json:"failure_count"`
	AddedTorrentIDs []string `json:"added_torrent_ids"`
}

func automationQBAddResult(status int, responseBody []byte) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(responseBody))
	result := map[string]any{
		"response_status": status,
		"submitted":       false,
		"accepted":        false,
		"pending":         false,
	}

	var modern automationQBAddResponse
	if trimmed != "" && json.Unmarshal(responseBody, &modern) == nil {
		result["response_format"] = "webapi_2_14"
		result["success_count"] = modern.SuccessCount
		result["pending_count"] = modern.PendingCount
		result["failure_count"] = modern.FailureCount
		result["added_torrent_ids"] = modern.AddedTorrentIDs
		result["torrent_hashes"] = modern.AddedTorrentIDs
		if len(modern.AddedTorrentIDs) > 0 {
			result["hash"] = modern.AddedTorrentIDs[0]
		}
		accepted := modern.SuccessCount+modern.PendingCount > 0
		result["submitted"] = accepted
		result["accepted"] = accepted
		result["pending"] = modern.PendingCount > 0
		if status < 200 || status >= 300 || !accepted {
			return result, fmt.Errorf(
				"qBittorrent 添加任务失败: HTTP %d (成功 %d，等待 %d，失败 %d)",
				status, modern.SuccessCount, modern.PendingCount, modern.FailureCount,
			)
		}
		return result, nil
	}

	if status < 200 || status >= 300 {
		return result, fmt.Errorf("qBittorrent 返回 HTTP %d: %s", status, trimmed)
	}
	if trimmed != "" && !strings.EqualFold(trimmed, "Ok.") {
		return result, fmt.Errorf("qBittorrent 返回了无法识别的添加结果: %s", trimmed)
	}
	result["response_format"] = "legacy"
	result["success_count"] = 1
	result["pending_count"] = 0
	result["failure_count"] = 0
	result["submitted"] = true
	result["accepted"] = true
	return result, nil
}

func (s *AutomationService) executeAutomationQBittorrent(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	targetID := automationConfigUint(node.Config, "target_id")
	var target model.AutomationTarget
	if err := s.db.First(&target, targetID).Error; err != nil {
		return nil, fmt.Errorf("qBittorrent 目标不存在: %w", err)
	}
	if !target.Enabled {
		return nil, errors.New("qBittorrent 目标已停用")
	}
	client, err := s.newAutomationQBClient(ctx, target)
	if err != nil {
		return nil, err
	}
	downloadURL, err := resolveAutomationString(runContext, automationConfigString(node.Config, "url"))
	if err != nil {
		return nil, err
	}
	if err := validateAutomationDownloadURL(downloadURL); err != nil {
		return nil, err
	}
	contentKey := automationContentKey(downloadURL)
	torrentTag := "filmfusion-automation-" + contentKey

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	configuredTags := renderAutomationTemplate(automationConfigString(node.Config, "tags"), runContext)
	allTags := torrentTag
	if strings.TrimSpace(configuredTags) != "" {
		allTags = configuredTags + "," + torrentTag
	}
	fields := map[string]string{
		"urls":               downloadURL,
		"savepath":           renderAutomationTemplate(automationConfigString(node.Config, "save_path"), runContext),
		"category":           renderAutomationTemplate(automationConfigString(node.Config, "category"), runContext),
		"tags":               allTags,
		"rename":             renderAutomationTemplate(automationConfigString(node.Config, "rename"), runContext),
		"paused":             strconv.FormatBool(automationConfigBool(node.Config, "paused")),
		"sequentialDownload": strconv.FormatBool(automationConfigBool(node.Config, "sequential")),
		"skip_checking":      strconv.FormatBool(automationConfigBool(node.Config, "skip_checking")),
		"firstLastPiecePrio": strconv.FormatBool(automationConfigBool(node.Config, "first_last_piece_priority")),
	}
	for configKey, apiKey := range map[string]string{
		"root_folder": "root_folder",
		"auto_tmm":    "autoTMM",
	} {
		if value := strings.ToLower(automationConfigString(node.Config, configKey)); value == "true" || value == "false" {
			fields[apiKey] = value
		}
	}
	for configKey, apiKey := range map[string]string{
		"download_limit_kib": "dlLimit",
		"upload_limit_kib":   "upLimit",
	} {
		value, exists, valueErr := automationConfigOptionalInt(node.Config, configKey)
		if valueErr != nil {
			return nil, valueErr
		}
		if exists {
			fields[apiKey] = strconv.FormatInt(int64(value)*1024, 10)
		}
	}
	if value, exists := node.Config["ratio_limit"]; exists && value != nil {
		number, ok := automationNumber(value)
		if !ok {
			return nil, errors.New("qBittorrent 分享率限制必须是数字")
		}
		fields["ratioLimit"] = strconv.FormatFloat(number, 'f', -1, 64)
	}
	value, exists, valueErr := automationConfigOptionalInt(node.Config, "seeding_time_limit")
	if valueErr != nil {
		return nil, valueErr
	}
	if exists {
		fields["seedingTimeLimit"] = strconv.Itoa(value)
	}
	for key, value := range fields {
		if strings.TrimSpace(value) != "" {
			_ = writer.WriteField(key, value)
		}
	}
	_ = writer.Close()
	req, err := client.newRequest(ctx, http.MethodPost, "/api/v2/torrents/add", &body)
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
	output := map[string]any{
		"target_id": target.ID, "target_name": target.Name,
		"content_key": contentKey, "torrent_tag": torrentTag,
	}
	addResult, err := automationQBAddResult(resp.StatusCode, responseBody)
	for key, value := range addResult {
		output[key] = value
	}
	if err != nil {
		return output, err
	}
	return output, nil
}

type automationQBClient struct {
	baseURL string
	client  *http.Client
	apiKey  string
}

func (client *automationQBClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	baseURL := strings.TrimRight(client.baseURL, "/")
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.8")
	req.Header.Set("Referer", baseURL+"/")
	if parsed, parseErr := url.Parse(baseURL); parseErr == nil && parsed.Scheme != "" && parsed.Host != "" {
		req.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	}
	if client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+client.apiKey)
	}
	return req, nil
}

func (client *automationQBClient) testConnection(ctx context.Context) error {
	req, err := client.newRequest(ctx, http.MethodGet, "/api/v2/app/version", nil)
	if err != nil {
		return err
	}
	resp, err := client.client.Do(req)
	if err != nil {
		return fmt.Errorf("连接 qBittorrent API 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if client.apiKey != "" {
			return fmt.Errorf("qBittorrent API Key 验证失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return fmt.Errorf("qBittorrent API 连接失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(string(body)) == "" {
		return errors.New("qBittorrent API 未返回应用版本")
	}
	return nil
}

func (s *AutomationService) newAutomationQBClient(ctx context.Context, target model.AutomationTarget) (*automationQBClient, error) {
	var config AutomationQBittorrentConfig
	if err := json.Unmarshal([]byte(target.ConfigJSON), &config); err != nil {
		return nil, errors.New("qBittorrent 目标配置损坏")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 20 * time.Second, Jar: jar}
	apiKey := strings.TrimSpace(config.APIKey)
	qbClient := &automationQBClient{baseURL: config.BaseURL, client: client, apiKey: apiKey}
	if apiKey != "" {
		return qbClient, nil
	}
	form := url.Values{"username": {config.Username}, "password": {config.Password}}
	req, err := qbClient.newRequest(ctx, http.MethodPost, "/api/v2/auth/login", strings.NewReader(form.Encode()))
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
	return qbClient, nil
}

func (s *AutomationService) executeAutomationOffline115(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	storageID := automationConfigUint(node.Config, "cloud_storage_id")
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
	downloadURL, err := resolveAutomationString(runContext, automationConfigString(node.Config, "url"))
	if err != nil {
		return nil, err
	}
	if err := validateAutomationDownloadURL(downloadURL); err != nil {
		return nil, err
	}
	directoryID := strings.TrimSpace(automationConfigString(node.Config, "directory_id"))
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
		"content_key":   automationContentKey(downloadURL), "submitted": true,
	}, nil
}

func (s *AutomationService) executeAutomationOffline115OpenAPI(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	return s.executeAutomation115Links(ctx, node, runContext)
}

func (s *AutomationService) executeAutomationRename115OpenAPI(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	storage, err := s.automationOpenAPIStorage(node)
	if err != nil {
		return nil, err
	}

	fileID, err := resolveAutomationString(runContext, automationConfigString(node.Config, "file_id"))
	if err != nil {
		return nil, fmt.Errorf("解析 115 文件 ID 失败: %w", err)
	}
	if fileID == "" || fileID == "0" || len(fileID) > 100 || strings.IndexFunc(fileID, unicode.IsSpace) >= 0 {
		return nil, errors.New("115 文件或文件夹 ID 无效")
	}
	newName, err := resolveAutomationString(runContext, automationConfigString(node.Config, "new_name"))
	if err != nil {
		return nil, fmt.Errorf("解析 115 新名称失败: %w", err)
	}
	if err := validateAutomation115RenameName(newName); err != nil {
		return nil, err
	}

	response, err := automation115OpenAPIRequest(ctx, s.httpClient, storage.AccessToken, http.MethodPost, "ufile/update", map[string]string{
		"file_id": fileID, "file_name": newName,
	})
	output := automation115APIOutput(storage, response)
	if err != nil {
		return output, fmt.Errorf("通过 115 OpenAPI 重命名失败: %w", err)
	}
	fileName := newName
	if data, ok := response["data"].(map[string]any); ok && automationAnyString(data["file_name"]) != "" {
		fileName = automationAnyString(data["file_name"])
	}
	output["renamed"], output["file_id"], output["file_name"], output["new_name"] = true, fileID, fileName, newName
	return output, nil
}

func validateAutomation115RenameName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("115 新名称不能为空")
	}
	if !utf8.ValidString(name) {
		return errors.New("115 新名称不是有效的 UTF-8 文本")
	}
	if len(name) > 255 {
		return errors.New("115 新名称不能超过 255 字节")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return errors.New("115 新名称不能包含路径分隔符")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("115 新名称不能包含控制字符")
	}
	return nil
}

func (s *AutomationService) executeAutomationNotification(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	if s.notifier == nil {
		return nil, errors.New("通知服务未初始化")
	}
	message := renderAutomationTemplate(automationConfigString(node.Config, "message"), runContext)
	title := renderAutomationTemplate(automationConfigString(node.Config, "title"), runContext)
	imageURL := renderAutomationTemplate(automationConfigString(node.Config, "image_url"), runContext)
	var channels []string
	channel := automationConfigString(node.Config, "channel")
	if channel != "" && channel != "default" {
		if channel != "telegram" && channel != "webhook" {
			return nil, errors.New("通知渠道必须为 default、telegram 或 webhook")
		}
		channels = []string{channel}
	}
	if strings.TrimSpace(message) == "" {
		return nil, errors.New("渲染后的通知内容为空")
	}
	if len(imageURL) > 4096 {
		return nil, errors.New("渲染后的通知图片地址超过 4096 字节")
	}
	report := s.notifier.Publish(ctx, NotificationEvent{
		Channels: channels,
		Type:     NotificationEventAutomationTriggered, Title: title, Message: message, ImageURL: imageURL,
		Severity: NotificationSeverityInfo, OccurredAt: time.Now(),
		Metadata: map[string]string{"source": "automation"},
	})
	output := map[string]any{"skipped": report.Skipped, "skip_reason": report.SkipReason, "deliveries": report.Deliveries, "image_url": imageURL}
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

func validateAutomationDownloadURL(value string) error {
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
		hasBTIH := false
		for _, xt := range parsed.Query()["xt"] {
			if strings.HasPrefix(strings.ToLower(xt), "urn:btih:") && len(xt) > len("urn:btih:") {
				hasBTIH = true
			}
		}
		if !hasBTIH {
			return errors.New("磁力链接缺少 BTIH")
		}
	case "bc":
		if !strings.EqualFold(parsed.Host, "bt") || strings.Trim(parsed.Path, "/") == "" {
			return errors.New("BC 下载地址必须使用 bc://bt/ 格式")
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

func renderAutomationTemplate(template string, runContext map[string]any) string {
	return strings.TrimSpace(automationTemplatePattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := automationTemplatePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		value, ok := resolveAutomationReference(runContext, parts[1])
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

func resolveAutomationString(runContext map[string]any, expression string) (string, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return "", errors.New("输入值为空")
	}
	if strings.Contains(expression, "{{") {
		value := renderAutomationTemplate(expression, runContext)
		if value == "" {
			return "", fmt.Errorf("模板 %q 没有解析出值", expression)
		}
		return value, nil
	}
	trimmedReference := strings.TrimPrefix(expression, "$")
	if strings.HasPrefix(expression, "$") || strings.HasPrefix(trimmedReference, "item.") ||
		strings.HasPrefix(trimmedReference, "vars.") || strings.HasPrefix(trimmedReference, "nodes.") {
		value, ok := resolveAutomationReference(runContext, expression)
		if !ok || value == nil {
			return "", fmt.Errorf("变量 %q 不存在", expression)
		}
		return strings.TrimSpace(fmt.Sprint(value)), nil
	}
	return expression, nil
}

func resolveAutomationReference(runContext map[string]any, reference string) (any, bool) {
	reference = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reference), "$"))
	if reference == "" {
		return nil, false
	}
	parts := strings.Split(reference, ".")
	var current any = runContext
	for _, part := range parts {
		part = strings.TrimSpace(part)
		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[part]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			if part == "" || (len(part) > 1 && part[0] == '0') {
				return nil, false
			}
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func automationConfigBool(config map[string]any, key string) bool {
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
