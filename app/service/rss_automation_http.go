package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const rssAutomationHTTPMaxResponseBytes = 1024 * 1024

var rssAutomationBlockedHTTPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func (s *RSSAutomationService) executeRSSAutomationHTTPRequest(ctx context.Context, node RSSAutomationNode, runContext map[string]any) (map[string]any, error) {
	requestURL, err := resolveRSSAutomationString(runContext, rssAutomationConfigString(node.Config, "url"))
	if err != nil {
		return nil, err
	}
	parsedURL, err := validateRSSAutomationHTTPRequestURL(requestURL)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(rssAutomationConfigString(node.Config, "method"))
	body := ""
	if configuredBody := rssAutomationConfigString(node.Config, "body"); configuredBody != "" {
		body, err = resolveRSSAutomationString(runContext, configuredBody)
		if err != nil {
			return nil, err
		}
	}
	if len(body) > 1024*1024 {
		return nil, errors.New("渲染后的 HTTP 请求体超过 1 MiB")
	}
	rawHeaders, err := rssAutomationHTTPHeaders(node.Config)
	if err != nil {
		return nil, err
	}
	renderedHeaders := make(map[string]string, len(rawHeaders))
	for name, configuredValue := range rawHeaders {
		value, resolveErr := resolveRSSAutomationString(runContext, configuredValue)
		if resolveErr != nil {
			return nil, fmt.Errorf("渲染 HTTP 请求头 %s 失败: %w", name, resolveErr)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("HTTP 请求头 %s 包含换行符", name)
		}
		renderedHeaders[name] = value
	}

	var requestBody io.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, parsedURL.String(), requestBody)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.5")
	request.Header.Set("User-Agent", "FilmFusion-RSS-Automation/1.0")
	if body != "" {
		contentType := strings.TrimSpace(rssAutomationConfigString(node.Config, "content_type"))
		if contentType == "" {
			contentType = "application/json"
		}
		request.Header.Set("Content-Type", contentType)
	}
	for name, value := range renderedHeaders {
		request.Header.Set(name, value)
	}

	allowPrivate := rssAutomationConfigBool(node.Config, "allow_private_network")
	followRedirects := rssAutomationConfigBool(node.Config, "follow_redirects")
	client := newRSSAutomationHTTPClient(allowPrivate, followRedirects)
	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return map[string]any{
			"request_host": parsedURL.Hostname(),
			"duration_ms":  time.Since(startedAt).Milliseconds(),
		}, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, rssAutomationHTTPMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 HTTP 响应失败: %w", err)
	}
	if len(responseBody) > rssAutomationHTTPMaxResponseBytes {
		return nil, errors.New("HTTP 响应超过 1 MiB 限制")
	}
	bodyText := string(responseBody)
	output := map[string]any{
		"status_code":  response.StatusCode,
		"content_type": strings.TrimSpace(response.Header.Get("Content-Type")),
		"body":         bodyText,
		"request_host": parsedURL.Hostname(),
		"duration_ms":  time.Since(startedAt).Milliseconds(),
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	var jsonValue any
	if len(bytes.TrimSpace(responseBody)) > 0 && decoder.Decode(&jsonValue) == nil {
		output["json"] = jsonValue
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		output["selected_port"] = "success"
		return output, nil
	}
	output["selected_port"] = "failure"
	output["reason"] = "HTTP 返回状态 " + strconv.Itoa(response.StatusCode)
	return output, nil
}

func validateRSSAutomationHTTPRequestURL(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 8192 {
		return nil, errors.New("HTTP 请求地址为空或超过 8192 字节")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return nil, errors.New("HTTP 请求地址必须是有效的 HTTP/HTTPS URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Hostname() == "" || (scheme != "http" && scheme != "https") {
		return nil, errors.New("HTTP 请求地址必须是有效的 HTTP/HTTPS URL")
	}
	parsed.Scheme = scheme
	if parsed.User != nil {
		return nil, errors.New("HTTP 请求地址不允许内嵌用户名或密码")
	}
	return parsed, nil
}

func rssAutomationHTTPRequestHost(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func rssAutomationHTTPHeaders(config map[string]any) (map[string]string, error) {
	result := map[string]string{}
	raw, exists := config["headers"]
	if !exists || raw == nil {
		return result, nil
	}
	switch typed := raw.(type) {
	case map[string]any:
		for name, value := range typed {
			result[name] = fmt.Sprint(value)
		}
	case map[string]string:
		for name, value := range typed {
			result[name] = value
		}
	case string:
		if strings.TrimSpace(typed) == "" {
			return result, nil
		}
		if err := json.Unmarshal([]byte(typed), &result); err != nil {
			return nil, errors.New("HTTP 请求头必须是 JSON 对象")
		}
	default:
		return nil, errors.New("HTTP 请求头必须是 JSON 对象")
	}
	if len(result) > 50 {
		return nil, errors.New("HTTP 请求头不能超过 50 个")
	}
	normalized := make(map[string]string, len(result))
	for name, value := range result {
		name = strings.TrimSpace(name)
		if !validRSSAutomationHTTPHeaderName(name) {
			return nil, fmt.Errorf("HTTP 请求头名 %q 无效", name)
		}
		switch strings.ToLower(name) {
		case "host", "content-length", "connection", "transfer-encoding", "upgrade", "proxy-authorization", "proxy-connection":
			return nil, fmt.Errorf("不允许自定义 HTTP 请求头 %s", name)
		}
		if len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("HTTP 请求头 %s 过长或包含换行符", name)
		}
		canonicalName := http.CanonicalHeaderKey(name)
		if _, exists := normalized[canonicalName]; exists {
			return nil, fmt.Errorf("HTTP 请求头 %s 重复", canonicalName)
		}
		normalized[canonicalName] = value
	}
	return normalized, nil
}

func validRSSAutomationHTTPHeaderName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			continue
		}
		return false
	}
	return true
}

func newRSSAutomationHTTPClient(allowPrivate, followRedirects bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(addresses) == 0 {
				return nil, errors.New("HTTP 目标主机没有可用 IP")
			}
			for _, ip := range addresses {
				if !rssAutomationHTTPIPAllowed(ip, allowPrivate) {
					return nil, fmt.Errorf("HTTP 目标 %s 解析到不允许的地址 %s", host, ip.String())
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
		},
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !followRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("HTTP 重定向超过 10 次")
		}
		validatedURL, err := validateRSSAutomationHTTPRequestURL(request.URL.String())
		if err != nil {
			return err
		}
		if len(via) > 0 && !rssAutomationHTTPSameOrigin(via[0].URL, validatedURL) {
			return errors.New("HTTP 重定向必须保持同源，避免泄露请求头")
		}
		return nil
	}
	return client
}

func rssAutomationHTTPSameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func rssAutomationHTTPIPAllowed(ip net.IP, allowPrivate bool) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if allowPrivate {
		return true
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	for _, prefix := range rssAutomationBlockedHTTPPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
