package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func validateAutomationTorrentConfig(config map[string]any) error {
	input := automationConfigString(config, "url")
	if input == "" || len(input) > 8192 {
		return errors.New("必须配置种子下载地址或磁力链接，最长 8192 字节")
	}
	_, err := automationHTTPHeaders(config)
	return err
}

func executeAutomationTorrentToMagnet(ctx context.Context, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	if err := validateAutomationTorrentConfig(node.Config); err != nil {
		return nil, err
	}
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "url"))
	if err != nil {
		return nil, err
	}
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 8192 {
		return nil, errors.New("解析后的种子地址为空或超过 8192 字节")
	}
	includeTrackers := node.Config["include_trackers"] == nil || automationConfigBool(node.Config, "include_trackers")
	if strings.HasPrefix(strings.ToLower(input), "magnet:") {
		return passthroughTorrentMagnet(input, includeTrackers)
	}
	parsed, err := validateAutomationHTTPRequestURL(input)
	if err != nil {
		return nil, errors.New("种子来源必须是 HTTP/HTTPS 下载地址或磁力链接")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/x-bittorrent, application/octet-stream;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", "FilmFusion-Automation/1.0")
	headers, err := automationHTTPHeaders(node.Config)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		rendered, err := resolveAutomationString(runContext, value)
		if err != nil {
			return nil, fmt.Errorf("渲染种子请求头 %s 失败: %w", key, err)
		}
		if len(rendered) > 8192 || strings.ContainsAny(rendered, "\r\n") {
			return nil, fmt.Errorf("种子请求头 %s 过长或包含换行符", key)
		}
		req.Header.Set(key, rendered)
	}
	followRedirects := node.Config["follow_redirects"] == nil || automationConfigBool(node.Config, "follow_redirects")
	client := newAutomationHTTPClient(automationConfigBool(node.Config, "allow_private_network"), followRedirects)
	client.Timeout = automationNodeTimeout(node)
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		// Download URLs often contain RSS/PT passkeys; omit the URL from errors.
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return nil, fmt.Errorf("获取种子失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("种子下载返回 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxAutomationTorrentBytes {
		return nil, errors.New("种子文件超过 8 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAutomationTorrentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取种子内容失败: %w", err)
	}
	return parseTorrentMagnet(data, includeTrackers)
}

func automationTorrentMagnetProtocol() AutomationNodeProtocol {
	return AutomationNodeProtocol{Type: AutomationNodeTorrentToMagnet, Label: "种子转磁力", Inputs: []AutomationVariableProtocol{
		automationTemplateVariable("url", "string", "种子地址或磁力链接", "获取 HTTP/HTTPS 种子文件并在本地解析；输入已是磁链时直接提取 Hash。", "$item.download_url", true),
		automationTemplateVariable("headers", "object", "种子请求头", "可配置下载种子所需的 Cookie 等请求头，值支持变量。不会调用 115 API。", map[string]string{"Referer": "https://example.com"}, false),
	}, Outputs: []AutomationVariableProtocol{
		automationVariable("magnet", "string", "磁力链接", "交给 115 添加链接任务或 qBittorrent 的 url 字段。", "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"),
		automationVariable("info_hash", "string", "BT 任务 Hash", "原始 info 字典的 SHA1，可用于查询 115 任务；不是种子文件 SHA1。", "0123456789abcdef0123456789abcdef01234567"),
		automationVariable("torrent_name", "string", "种子名称", "从种子或磁链 dn 参数读取的名称。", "Example.Show"),
		automationVariable("file_size", "integer", "文件总大小", "种子内文件的总字节数；磁链透传时不返回。", 1024),
		automationVariable("file_count", "integer", "文件数量", "种子中的文件数量；磁链透传时不返回。", 1),
		automationVariable("files", "array", "文件列表", "每项包含 index、path、size；磁链透传时不返回。", []map[string]any{{"index": 0, "path": "Example.Show/01.mkv", "size": 1024}}),
		automationVariable("private", "boolean", "私有种子", "种子 private 标记；磁链透传时未知，不返回此字段。", false),
		automationVariable("trackers", "array", "Tracker 列表", "种子或磁链内的 Tracker 地址，可能包含站点 passkey。", []string{"https://tracker.example/announce"}),
		automationVariable("trackers_included", "boolean", "磁链携带 Tracker", "生成磁链时是否保留 Tracker 参数。", true),
		automationVariable("torrent_version", "string", "种子格式", "v1 或 hybrid；暂不支持纯 v2，磁链透传时未知。", "v1"),
		automationVariable("metadata_available", "boolean", "已解析种子元数据", "true 表示已解析真实种子；false 表示仅透传磁链，不能判断文件和私有标记。", true),
		automationVariable("warnings", "array", "转换提示", "私有种子和磁链元数据缺失等提示，不代表下载已成功。", []string{}),
	}}
}
