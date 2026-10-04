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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"
)

// The cloud-download endpoints use multipart POST bodies and query parameters
// for GET. Keep the response envelope: the SDK's convenience methods discard
// per-link errors and fields that workflows need to pass to later nodes.
func automation115OpenAPIRequest(ctx context.Context, httpClient *http.Client, token, method, path string, fields map[string]string) (map[string]any, error) {
	endpoint := "https://proapi.115.com/open/" + path
	var body bytes.Buffer
	contentType := ""
	if method == http.MethodGet {
		query := url.Values{}
		for key, value := range fields {
			query.Set(key, value)
		}
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
	} else {
		writer := multipart.NewWriter(&body)
		for key, value := range fields {
			if err := writer.WriteField(key, value); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		contentType = writer.FormDataContentType()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := http.Client{Timeout: 30 * time.Second}
	if httpClient != nil {
		client = *httpClient
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("115 OpenAPI 请求失败: %w", err)
	}
	defer resp.Body.Close()
	const maxResponse = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("读取 115 响应失败: %w", err)
	}
	if len(raw) > maxResponse {
		return nil, errors.New("115 响应超过 8 MiB 限制")
	}
	var envelope map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decodeErr := decoder.Decode(&envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return envelope, fmt.Errorf("115 OpenAPI 返回 HTTP %d", resp.StatusCode)
	}
	if decodeErr != nil || envelope == nil {
		return envelope, errors.New("115 OpenAPI 返回无效 JSON")
	}
	state, validState := envelope["state"].(bool)
	code, validCode := envelope["code"].(json.Number)
	if !validState || !validCode {
		return envelope, errors.New("115 OpenAPI 响应缺少 state 或 code")
	}
	if !state || code.String() != "0" {
		return envelope, fmt.Errorf("115 OpenAPI 失败（%s）: %s", code, automationAnyString(envelope["message"]))
	}
	return envelope, nil
}

func (s *AutomationService) automationOpenAPIStorage(node AutomationNode) (model.CloudStorage, error) {
	var storage model.CloudStorage
	id := automationConfigUint(node.Config, "cloud_storage_id")
	if id == 0 {
		return storage, errors.New("必须选择 115 OpenAPI 账号")
	}
	if err := s.db.First(&storage, id).Error; err != nil {
		return storage, fmt.Errorf("115 OpenAPI 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open || storage.Status != model.StatusActive {
		return storage, errors.New("所选账号不是启用的 115 OpenAPI 账号")
	}
	if strings.TrimSpace(storage.AccessToken) == "" {
		return storage, errors.New("115 OpenAPI 账号没有可用 AccessToken")
	}
	return storage, nil
}

func automation115APIOutput(storage model.CloudStorage, response map[string]any) map[string]any {
	output := map[string]any{
		"cloud_storage_id": storage.ID, "storage_name": storage.StorageName, "access_method": "openapi",
	}
	if response != nil {
		output["response"] = response
		for _, key := range []string{"state", "code", "message", "data"} {
			output[key] = response[key]
		}
	}
	return output
}

func automation115ResolvedFields(node AutomationNode, runContext map[string]any, keys ...string) (map[string]string, error) {
	fields := make(map[string]string, len(keys))
	for _, key := range keys {
		value, err := resolveAutomationString(runContext, automationConfigString(node.Config, key))
		if err != nil || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("115 参数 %s 未配置或未解析出值", key)
		}
		fields[key] = strings.TrimSpace(value)
	}
	return fields, nil
}

func automation115Directory(node AutomationNode, runContext map[string]any) (string, error) {
	configured := automationConfigString(node.Config, "directory_id")
	if configured == "" {
		return "0", nil
	}
	return resolveAutomationString(runContext, configured)
}

func automation115DownloadURLs(value string) ([]string, error) {
	urls := make([]string, 0)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "ed2k://|file|") && len(line) <= 65535 {
			urls = append(urls, line)
			continue
		}
		parsed, err := url.Parse(line)
		if err != nil || len(line) > 65535 {
			return nil, errors.New("115 下载链接格式无效或过长")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "ftp":
			if parsed.Host == "" {
				return nil, errors.New("115 下载链接缺少主机名")
			}
		case "magnet":
			if err := validateAutomationDownloadURL(line); err != nil {
				return nil, err
			}
		case "ed2k":
			if !strings.HasPrefix(strings.ToLower(line), "ed2k://|file|") {
				return nil, errors.New("电驴链接必须使用 ed2k://|file| 格式")
			}
		default:
			return nil, fmt.Errorf("115 OpenAPI 不支持下载协议 %q", parsed.Scheme)
		}
		urls = append(urls, line)
	}
	if len(urls) == 0 || len(urls) > 115 {
		return nil, errors.New("115 链接任务每批需要 1 到 115 个链接")
	}
	return urls, nil
}

func (s *AutomationService) executeAutomation115Links(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	storage, err := s.automationOpenAPIStorage(node)
	if err != nil {
		return nil, err
	}
	fields, err := automation115ResolvedFields(node, runContext, "url")
	if err != nil {
		return nil, err
	}
	urls, err := automation115DownloadURLs(fields["url"])
	if err != nil {
		return nil, err
	}
	directory, err := automation115Directory(node, runContext)
	if err != nil {
		return nil, err
	}
	response, err := automation115OpenAPIRequest(ctx, s.httpClient, storage.AccessToken, http.MethodPost, "offline/add_task_urls", map[string]string{
		"urls": strings.Join(urls, "\n"), "wp_path_id": directory,
	})
	output := automation115APIOutput(storage, response)
	output["directory_id"] = directory
	output["content_key"] = automationContentKey(strings.Join(urls, "\n"))
	output["submitted"] = false
	if err != nil {
		return output, err
	}
	results, ok := response["data"].([]any)
	if !ok || len(results) == 0 {
		return output, errors.New("115 未返回逐链接任务结果")
	}
	hashes := make([]string, 0, len(results))
	failed := make([]any, 0)
	for _, result := range results {
		item, _ := result.(map[string]any)
		state, _ := item["state"].(bool)
		hash := automationAnyString(item["info_hash"])
		if state && automationAnyString(item["code"]) == "0" && hash != "" {
			hashes = append(hashes, hash)
		} else {
			failed = append(failed, result)
		}
	}
	output["hashes"] = hashes
	output["results"] = results
	output["failed_results"] = failed
	output["success_count"] = len(hashes)
	output["failure_count"] = len(failed)
	output["partial"] = len(hashes) > 0 && (len(failed) > 0 || len(results) != len(urls))
	if len(hashes) == 1 {
		output["info_hash"] = hashes[0]
	}
	if len(failed) > 0 || len(hashes) != len(urls) {
		return output, fmt.Errorf("115 链接任务未全部提交成功：成功 %d / 请求 %d，详见 results；请勿重复提交成功项", len(hashes), len(urls))
	}
	output["submitted"] = true
	return output, nil
}

var automation115WantedPattern = regexp.MustCompile(`^\d+(,\d+)*$`)

func (s *AutomationService) executeAutomation115OpenAPI(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	storage, err := s.automationOpenAPIStorage(node)
	if err != nil {
		return nil, err
	}
	if node.Type == AutomationNodeList115TasksOpenAPI {
		return s.executeAutomation115TaskList(ctx, node, runContext, storage)
	}
	method, path := http.MethodPost, ""
	fields := map[string]string{}
	switch node.Type {
	case AutomationNodeTorrent115OpenAPI:
		path = "offline/torrent"
		fields, err = automation115ResolvedFields(node, runContext, "torrent_sha1", "pick_code")
	case AutomationNodeOffline115BTOpenAPI:
		path = "offline/add_task_bt"
		fields, err = automation115ResolvedFields(node, runContext, "info_hash", "wanted", "save_path", "torrent_sha1", "pick_code")
		if err == nil && !automation115WantedPattern.MatchString(fields["wanted"]) {
			err = errors.New("wanted 必须是逗号分隔的非负文件索引，例如 0,1,2")
		}
		if err == nil {
			fields["wp_path_id"], err = automation115Directory(node, runContext)
		}
	case AutomationNodeQuota115OpenAPI:
		method, path = http.MethodGet, "offline/get_quota_info"
	case AutomationNodeDelete115TaskOpenAPI:
		path = "offline/del_task"
		fields, err = automation115ResolvedFields(node, runContext, "info_hash")
		if err == nil {
			fields["del_source_file"] = "0"
			if automationConfigBool(node.Config, "delete_files") {
				fields["del_source_file"] = "1"
			}
		}
	case AutomationNodeClear115TasksOpenAPI:
		path = "offline/clear_task"
		var flag int
		flag, err = automation115Integer(node.Config["flag"], 0, 5)
		fields["flag"] = strconv.Itoa(flag)
	default:
		return nil, fmt.Errorf("不支持的 115 节点 %s", node.Type)
	}
	if err != nil {
		return nil, err
	}
	response, err := automation115OpenAPIRequest(ctx, s.httpClient, storage.AccessToken, method, path, fields)
	output := automation115APIOutput(storage, response)
	if err != nil {
		return output, err
	}
	switch node.Type {
	case AutomationNodeTorrent115OpenAPI:
		data, ok := response["data"].(map[string]any)
		if !ok || automationAnyString(data["info_hash"]) == "" {
			return output, errors.New("115 种子解析结果缺少 info_hash")
		}
		for _, key := range []string{"file_size", "torrent_name", "file_count", "info_hash", "torrent_filelist"} {
			output[key] = data[key]
		}
		output["torrent_sha1"], output["pick_code"] = fields["torrent_sha1"], fields["pick_code"]
		selected := []string{}
		files, _ := data["torrent_filelist"].([]any)
		for index, raw := range files {
			file, _ := raw.(map[string]any)
			if automationAnyString(file["wanted"]) == "1" {
				selected = append(selected, strconv.Itoa(index))
			}
		}
		output["wanted"] = strings.Join(selected, ",")
	case AutomationNodeOffline115BTOpenAPI:
		// add_task_bt returns an empty data array. The submitted hash comes from
		// the resolved input, not a fabricated provider response.
		output["submitted"] = true
		output["info_hash"], output["hashes"] = fields["info_hash"], []string{fields["info_hash"]}
		output["directory_id"], output["save_path"] = fields["wp_path_id"], fields["save_path"]
		output["content_key"] = "btih:" + strings.ToLower(fields["info_hash"])
	case AutomationNodeQuota115OpenAPI:
		data, ok := response["data"].(map[string]any)
		if !ok {
			return output, errors.New("115 配额响应缺少 data 对象")
		}
		for _, key := range []string{"count", "surplus", "used", "package"} {
			output[key] = data[key]
		}
	case AutomationNodeDelete115TaskOpenAPI:
		output["deleted"], output["info_hash"], output["delete_files"] = true, fields["info_hash"], fields["del_source_file"] == "1"
	case AutomationNodeClear115TasksOpenAPI:
		output["cleared"] = true
		output["flag"], _ = strconv.Atoi(fields["flag"])
	}
	return output, nil
}

func automation115Integer(value any, min, max int) (int, error) {
	number, err := strconv.Atoi(automationAnyString(value))
	if err != nil || number < min || number > max {
		return 0, fmt.Errorf("115 参数必须是 %d 到 %d 之间的整数", min, max)
	}
	return number, nil
}

func validateAutomation115OpenAPIConfig(node AutomationNode) error {
	if automationConfigUint(node.Config, "cloud_storage_id") == 0 {
		return errors.New("必须选择 115 OpenAPI 账号")
	}
	keys := []string{}
	switch node.Type {
	case AutomationNodeTorrent115OpenAPI:
		keys = []string{"torrent_sha1", "pick_code"}
	case AutomationNodeOffline115BTOpenAPI:
		keys = []string{"info_hash", "wanted", "save_path", "torrent_sha1", "pick_code"}
	case AutomationNodeDelete115TaskOpenAPI:
		keys = []string{"info_hash"}
		if value, exists := node.Config["delete_files"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("同时删除源文件必须是布尔值")
			}
		}
	case AutomationNodeClear115TasksOpenAPI:
		_, err := automation115Integer(node.Config["flag"], 0, 5)
		return err
	case AutomationNodeList115TasksOpenAPI:
		if page, exists := node.Config["page"]; exists {
			_, err := automation115Integer(page, 1, 1000000)
			return err
		}
	}
	for _, key := range keys {
		if automationConfigString(node.Config, key) == "" {
			return fmt.Errorf("必须配置 115 参数 %s", key)
		}
	}
	return nil
}

func (s *AutomationService) executeAutomation115TaskList(ctx context.Context, node AutomationNode, runContext map[string]any, storage model.CloudStorage) (map[string]any, error) {
	page := 1
	var err error
	if value, exists := node.Config["page"]; exists {
		page, err = automation115Integer(value, 1, 1000000)
		if err != nil {
			return nil, err
		}
	}
	hash := ""
	if automationConfigString(node.Config, "info_hash") != "" {
		fields, err := automation115ResolvedFields(node, runContext, "info_hash")
		if err != nil {
			return nil, err
		}
		hash, page = fields["info_hash"], 1
	}
	responses := []any{}
	for checked := 0; checked < maxAutomation115TaskPages; checked++ {
		response, err := automation115OpenAPIRequest(ctx, s.httpClient, storage.AccessToken, http.MethodGet, "offline/get_task_list", map[string]string{"page": strconv.Itoa(page)})
		output := automation115APIOutput(storage, response)
		if response != nil {
			responses = append(responses, response)
		}
		output["responses"], output["pages_checked"] = responses, checked+1
		if err != nil {
			return output, err
		}
		data, ok := response["data"].(map[string]any)
		if !ok {
			return output, errors.New("115 任务列表缺少 data 对象")
		}
		tasks, ok := data["tasks"].([]any)
		if !ok {
			return output, errors.New("115 任务列表缺少 tasks 数组")
		}
		for _, key := range []string{"page", "page_count", "count"} {
			output[key] = data[key]
		}
		output["tasks"] = tasks
		if hash == "" {
			return output, nil
		}
		output["info_hash"], output["hashes"] = hash, []string{hash}
		output["matched"], output["completed"] = false, false
		output["file_id"], output["file_name"] = "", ""
		output["tasks"] = []any{}
		for _, raw := range tasks {
			task, _ := raw.(map[string]any)
			if !strings.EqualFold(strings.TrimSpace(automationAnyString(task["info_hash"])), hash) {
				continue
			}
			output["matched"], output["task"], output["tasks"] = true, task, []any{task}
			output["status"], output["percent"] = task["status"], task["percentDone"]
			output["directory_id"] = task["wp_path_id"]
			fileID := automationAnyString(task["file_id"])
			if automationAnyString(task["status"]) == "2" && fileID != "" && fileID != "0" {
				output["completed"], output["file_id"], output["file_name"] = true, fileID, task["name"]
			}
			return output, nil
		}
		pageCount, err := automation115Integer(data["page_count"], 0, 1000000)
		if err != nil {
			return output, fmt.Errorf("115 分页信息无效: %w", err)
		}
		if page >= pageCount {
			return output, nil
		}
		if checked+1 == maxAutomation115TaskPages {
			return output, errors.New("115 任务查询达到 200 页上限，尚未确认目标任务是否存在")
		}
		page++
	}
	return nil, errors.New("115 任务查询未完成")
}
