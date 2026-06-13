package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	m115 "github.com/SheltonZhu/115driver/pkg/crypto/m115"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func (s *Web115Service) DownloadForP115Transfer(ctx context.Context, accessToken string, client *driver.Pan115Client, sourceInfo BalanceSourceFile) (*driver.DownloadInfo, error) {
	if strings.TrimSpace(accessToken) != "" {
		return s.DownloadWithOpenAPIForRange(ctx, accessToken, sourceInfo.PickCode, "")
	}
	if sourceInfo.IsCollect {
		return s.DownloadWithWebAPIForRange(client, sourceInfo.PickCode)
	}
	return s.DownloadWithAndroidAPIForRange(client, sourceInfo.PickCode)
}

func (s *Web115Service) DownloadWithOpenAPIForRange(ctx context.Context, accessToken, pickCode, userAgent string) (*driver.DownloadInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("115 OpenAPI AccessToken 为空")
	}
	pickCode = strings.TrimSpace(pickCode)
	if pickCode == "" {
		return nil, fmt.Errorf("pickcode 为空")
	}

	client := sdk115.New()
	client.SetAccessToken(accessToken)
	downURLResp, err := client.DownURL(ctx, pickCode, userAgent)
	if err != nil {
		return nil, fmt.Errorf("调用 115 OpenAPI DownURL 失败: %w", err)
	}
	for _, urlInfo := range downURLResp {
		rawURL := strings.TrimSpace(urlInfo.URL.URL)
		if rawURL == "" {
			continue
		}
		headers := http.Header{}
		headers.Set("User-Agent", userAgent)
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
		SetHeaderVerbatim("User-Agent", "").
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
	headers.Set("User-Agent", "")
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
		SetHeaderVerbatim("User-Agent", "").
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
	info.Header.Set("User-Agent", "")
	if resp != nil && resp.Request != nil {
		for key, values := range resp.Request.Header {
			if strings.EqualFold(key, "User-Agent") {
				continue
			}
			for _, value := range values {
				info.Header.Add(key, value)
			}
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
