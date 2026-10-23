// 秒传校验沿用 P0001 的单次精确 Range 读取，只计算挑战片段，不下载整个文件。
package service

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxTransferRangeBytes int64 = 8 << 20

// 签发直链与读取 CDN 共用直连 Transport，避免环境代理按域名分流改变出口。
var transferRangeTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}()

func (s *Web115Service) transferHTTPClient() *http.Client {
	client := http.Client{Transport: transferRangeTransport, Timeout: 30 * time.Second}
	if s.httpClient != nil {
		client = *s.httpClient
		if client.Timeout <= 0 {
			client.Timeout = 30 * time.Second
		}
	}
	// 不混入 CookieJar 的登录凭据，也不把签发 Cookie 带到重定向地址。
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func hashTransferHTTPRange(ctx context.Context, client *http.Client, rawURL string, headers http.Header, size, start, end int64) (string, error) {
	if start < 0 || end < start || end >= size || end-start >= maxTransferRangeBytes {
		return "", fmt.Errorf("秒传校验范围无效或超过 8 MiB")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("源直链地址无效")
	}
	// 空 UA 也必须显式设置，防止 Go 自动发送另一种 UA。
	req.Header.Set("User-Agent", headers.Get("User-Agent"))
	if cookie := headers.Get("Cookie"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		// url.Error 含带签名的下载地址，不写入任务错误。
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("源直链片段请求失败，请检查网络连接")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return "", fmt.Errorf("源直链拒绝片段读取: HTTP %d，CDN 原因=%s，已携带下载 Cookie=%t；range=bytes=%d-%d", resp.StatusCode,
				transferRangeRejectionReason(resp), req.Header.Get("Cookie") != "", start, end)
		}
		return "", fmt.Errorf("源直链未返回分段内容: HTTP %d range=bytes=%d-%d", resp.StatusCode, start, end)
	}
	wantRange := fmt.Sprintf("bytes %d-%d/%d", start, end, size)
	if got := resp.Header.Get("Content-Range"); got != wantRange {
		return "", fmt.Errorf("源直链 Content-Range 不匹配: got=%q want=%q", got, wantRange)
	}
	if encoding := resp.Header.Get("Content-Encoding"); (encoding != "" && encoding != "identity") || resp.Uncompressed {
		return "", fmt.Errorf("源直链返回了压缩内容，无法计算原始片段校验")
	}
	length := end - start + 1
	if resp.ContentLength >= 0 && resp.ContentLength != length {
		return "", fmt.Errorf("源直链片段长度不匹配: got=%d want=%d", resp.ContentLength, length)
	}
	hash := sha1.New()
	read, err := io.Copy(hash, io.LimitReader(resp.Body, length+1))
	if err != nil {
		return "", fmt.Errorf("源直链片段读取失败")
	}
	if read != length {
		return "", fmt.Errorf("源直链片段长度不匹配: got=%d want=%d", read, length)
	}
	return fmt.Sprintf("%X", hash.Sum(nil)), nil
}

// 只读取拒绝响应的少量 JSON，并输出已知固定错误文案；未知正文可能带签名、
// Cookie 或文件名，不进入任务错误。此诊断不增加请求或重试。
func transferRangeRejectionReason(resp *http.Response) string {
	if resp.Body == nil || resp.Uncompressed || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return "未识别"
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return "未识别"
	}
	var result struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "未识别"
	}
	switch reason := strings.ToLower(strings.TrimSpace(result.Message)); reason {
	case "no cookie value", "invalid cookie", "cookie expired", "ip not match", "ip mismatch",
		"user agent not match", "user-agent mismatch", "url expired", "signature expired", "invalid signature", "access denied", "forbidden":
		return reason
	default:
		return "未识别"
	}
}
