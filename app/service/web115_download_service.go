package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	m115 "github.com/SheltonZhu/115driver/pkg/crypto/m115"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

// 秒传下载使用普通浏览器 UA。115Browser 标识可能触发 CDN 特殊 Cookie 校验，
// 即使签发与读取的 UA 一致也会返回 403；登录客户端的 UA 不参与直链读取。
// 参考 https://github.com/OpenListTeam/OpenList/issues/2502。
const web115TransferUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

func (s *Web115Service) DownloadForP115Transfer(ctx context.Context, accessMode, accessToken string, client *driver.Pan115Client, sourceInfo BalanceSourceFile) (*driver.DownloadInfo, error) {
	return resolveMatch302DownloadInfo(
		accessMode,
		func() (*driver.DownloadInfo, error) {
			if strings.TrimSpace(accessToken) == "" {
				return nil, fmt.Errorf("115 OpenAPI AccessToken 为空")
			}
			return s.DownloadWithOpenAPIForRange(ctx, accessToken, sourceInfo.PickCode, "")
		},
		func() (*driver.DownloadInfo, error) {
			if client == nil {
				return nil, fmt.Errorf("115 Cookie client 为空")
			}
			if sourceInfo.IsCollect {
				return s.DownloadWithWebAPIForRange(client, sourceInfo.PickCode)
			}
			return s.DownloadWithAndroidAPIForRange(client, sourceInfo.PickCode)
		},
	)
}

func resolveMatch302DownloadInfo(
	mode string,
	openAPI func() (*driver.DownloadInfo, error),
	cookie func() (*driver.DownloadInfo, error),
) (*driver.DownloadInfo, error) {
	var attemptErrors []error
	for _, method := range model.Match302AccessOrder(mode) {
		var (
			info *driver.DownloadInfo
			err  error
		)
		switch method {
		case model.Match302AccessMethodOpenAPI:
			info, err = openAPI()
		case model.Match302AccessMethodCookie:
			info, err = cookie()
		}
		if err == nil && info != nil && strings.TrimSpace(info.Url.Url) != "" {
			return info, nil
		}
		if err == nil {
			err = fmt.Errorf("未返回可用直链")
		}
		attemptErrors = append(attemptErrors, fmt.Errorf("%s: %w", method, err))
	}
	return nil, fmt.Errorf("Match302 秒传源直链获取失败 mode=%s: %w", model.NormalizeMatch302AccessMode(mode), errors.Join(attemptErrors...))
}

func (s *Web115Service) DownloadWithOpenAPIForRange(ctx context.Context, accessToken, pickCode, userAgent string) (*driver.DownloadInfo, error) {
	return s.downloadWithOpenAPIForRange(ctx, accessToken, pickCode, userAgent, nil)
}

// downloadWithOpenAPIForRange 按 P0001 的流程直接解析响应；SDK 的 MIME 自动解析
// 会把 text/plain JSON 或 HTTP 错误误报为 code=0、空 message。
func (s *Web115Service) downloadWithOpenAPIForRange(ctx context.Context, accessToken, pickCode, userAgent string, expected *BalanceSourceFile) (*driver.DownloadInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("115 OpenAPI AccessToken 为空")
	}
	pickCode = strings.TrimSpace(pickCode)
	if pickCode == "" {
		return nil, fmt.Errorf("pickcode 为空")
	}

	// Keep the same UA for URL issuance and the subsequent CDN Range request.
	if strings.TrimSpace(userAgent) == "" {
		userAgent = web115TransferUA
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sdk115.ApiFsDownURL,
		strings.NewReader(url.Values{"pick_code": {pickCode}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := s.transferHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 115 OpenAPI DownURL 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("调用 115 OpenAPI DownURL 失败: HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, fmt.Errorf("115 OpenAPI DownURL 响应读取失败或超过 1 MiB")
	}
	var result struct {
		State   json.RawMessage `json:"state"`
		Code    json.RawMessage `json:"code"`
		Errno   json.RawMessage `json:"errno"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("115 OpenAPI DownURL 响应不是有效的下载信息")
	}
	if string(result.State) != "true" && string(result.State) != "1" {
		if string(result.State) != "false" && string(result.State) != "0" {
			return nil, fmt.Errorf("115 OpenAPI DownURL 响应缺少有效 state")
		}
		return nil, fmt.Errorf("115 OpenAPI DownURL 拒绝请求: code=%s errno=%s message=%s",
			result.Code, result.Errno, firstNonEmptyString(result.Message, result.Msg, result.Error))
	}
	var files map[string]struct {
		driver.DownloadInfo
		SHA1 string `json:"sha1"`
	}
	if err := json.Unmarshal(result.Data, &files); err != nil || len(files) != 1 {
		return nil, fmt.Errorf("115 OpenAPI DownURL 未返回唯一源文件")
	}
	for fileID, urlInfo := range files {
		rawURL := strings.TrimSpace(urlInfo.Url.Url)
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, fmt.Errorf("115 OpenAPI DownURL 返回的下载地址无效")
		}
		if expected != nil && ((expected.FileID != "" && fileID != expected.FileID) ||
			int64(urlInfo.FileSize) != expected.Size || (expected.SHA1 != "" && !strings.EqualFold(urlInfo.SHA1, expected.SHA1)) ||
			(urlInfo.PickCode != "" && urlInfo.PickCode != pickCode)) {
			return nil, fmt.Errorf("115 OpenAPI DownURL 源文件 ID、大小、SHA1 或 pickcode 不匹配")
		}
		headers := http.Header{}
		headers.Set("User-Agent", userAgent)
		if cookie := web115CookieHeaderFromSetCookie(response.Header.Values("Set-Cookie")); cookie != "" {
			headers.Set("Cookie", cookie)
		}
		return &driver.DownloadInfo{
			FileName: urlInfo.FileName,
			FileSize: driver.StringInt64(urlInfo.FileSize),
			PickCode: firstNonEmptyString(urlInfo.PickCode, pickCode),
			Url: driver.FileDownloadUrl{
				Url: rawURL,
			},
			Header: headers,
		}, nil
	}
	return nil, fmt.Errorf("115 OpenAPI DownURL 未返回可用直链，pickcode=%s", pickCode)
}

func (s *Web115Service) DownloadWithWebAPIForRange(client *driver.Pan115Client, pickCode string) (*driver.DownloadInfo, error) {
	if client == nil {
		return nil, fmt.Errorf("115 client 为空")
	}
	var result struct {
		State    bool               `json:"state"`
		Code     int                `json:"code"`
		Errno    driver.StringInt   `json:"errno"`
		ErrNo    int                `json:"errNo"`
		Error    string             `json:"error"`
		Message  string             `json:"message"`
		Msg      string             `json:"msg"`
		FileURL  string             `json:"file_url"`
		FileID   string             `json:"file_id"`
		FileName string             `json:"file_name"`
		FileSize driver.StringInt64 `json:"file_size"`
	}

	resp, err := client.NewRequest().
		SetQueryParam("pickcode", pickCode).
		SetHeader("User-Agent", web115TransferUA).
		ForceContentType("application/json").
		SetResult(&result).
		Get("https://webapi.115.com/files/download")
	if err != nil {
		return nil, err
	}
	raw := ""
	if resp != nil {
		raw = resp.String()
	}
	if !result.State || strings.TrimSpace(result.FileURL) == "" {
		msg := firstNonEmptyString(result.Message, result.Msg, result.Error, raw)
		return nil, fmt.Errorf("web 下载接口失败 code=%d errno=%d errNo=%d msg=%s", result.Code, int(result.Errno), result.ErrNo, msg)
	}

	headers := http.Header{}
	// 明确覆盖登录客户端的 115Browser UA，并在 CDN 请求中使用同一个 UA。
	headers.Set("User-Agent", web115TransferUA)
	if resp != nil {
		if cookie := web115CookieHeaderFromSetCookie(resp.Header().Values("Set-Cookie")); cookie != "" {
			headers.Set("Cookie", cookie)
		}
	}
	return &driver.DownloadInfo{
		FileName: result.FileName,
		FileSize: result.FileSize,
		PickCode: pickCode,
		Url: driver.FileDownloadUrl{
			Url: result.FileURL,
		},
		Header: headers,
	}, nil
}

func (s *Web115Service) DownloadWithAndroidAPIForRange(client *driver.Pan115Client, pickCode string) (*driver.DownloadInfo, error) {
	if client == nil {
		return nil, fmt.Errorf("115 client 为空")
	}
	key := m115.GenerateKey()
	params, err := json.Marshal(map[string]string{"pick_code": pickCode})
	if err != nil {
		return nil, err
	}

	result := driver.DownloadResp{}
	req := client.NewRequest().
		SetQueryParam("t", driver.Now().String()).
		SetFormData(map[string]string{"data": m115.Encode(params, key)}).
		ForceContentType("application/json").
		SetHeader("User-Agent", web115TransferUA).
		SetResult(&result)
	resp, err := req.Post(driver.AndroidApiDownloadGetUrl)
	if err != nil {
		return nil, err
	}
	raw := ""
	if resp != nil {
		raw = resp.String()
	}
	if !result.State || int(result.Errno) != 0 || result.ErrNo != 0 {
		msg := firstNonEmptyString(result.Msg, result.Error, raw)
		return nil, fmt.Errorf("android 下载接口失败 errno=%d errNo=%d msg=%s", int(result.Errno), result.ErrNo, msg)
	}
	if strings.TrimSpace(string(result.EncodedData)) == "" {
		return nil, fmt.Errorf("android 下载接口响应 data 为空")
	}

	decoded, err := m115.Decode(string(result.EncodedData), key)
	if err != nil {
		return nil, err
	}
	var infoResp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(decoded, &infoResp); err != nil {
		return nil, err
	}
	info := &driver.DownloadInfo{
		Url: driver.FileDownloadUrl{
			Url: infoResp.URL,
		},
		PickCode: pickCode,
		Header:   http.Header{},
	}
	info.Header.Set("User-Agent", web115TransferUA)
	// CDN 只接收本次签发的下载 Cookie，不能复制账号 Cookie、Origin 等 API 请求头。
	if resp != nil {
		if cookie := web115CookieHeaderFromSetCookie(resp.Header().Values("Set-Cookie")); cookie != "" {
			info.Header.Set("Cookie", cookie)
		}
	}
	return info, nil
}

func web115CookieHeaderFromSetCookie(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if idx := strings.Index(value, ";"); idx >= 0 {
			value = value[:idx]
		}
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "; ")
}
