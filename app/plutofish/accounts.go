// 接收网盘发现只访问 PlutoFish 的通知接口，不读取或操作网盘。
package plutofish

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ReceivingAccount 是用于下拉选择的安全摘要，ID 与通知的 targetAccountId 相同。
type ReceivingAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
}

// ReceivingAccounts 同时提供真实密钥所属 UID，客户端无需从游标推测身份。
type ReceivingAccounts struct {
	OwnerUID string             `json:"ownerUID"`
	Items    []ReceivingAccount `json:"items"`
}

// DiscoverAccounts 在有限时间内读取全部分页，失败时不返回不完整列表。
// 禁止重定向，避免把 Bearer 密钥转发给其他地址；错误不携带远端正文和密钥。
func DiscoverAccounts(ctx context.Context, baseURL, apiKey string) (ReceivingAccounts, error) {
	result := ReceivingAccounts{Items: []ReceivingAccount{}}
	base, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(apiKey) == "" || len(apiKey) > 4096 || strings.ContainsAny(apiKey, "\r\n\x00") {
		return result, errors.New("请填写有效的 PlutoFish 通知 API Key")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()

	after := ""
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		endpoint := base + "/api/v1/events/accounts?limit=100"
		if after != "" {
			endpoint += "&after=" + url.QueryEscape(after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return ReceivingAccounts{}, errors.New("PlutoFish 服务地址无效")
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return ReceivingAccounts{}, errors.New("无法连接 PlutoFish 获取接收网盘，请检查服务地址与网络")
		}
		if resp.StatusCode != http.StatusOK {
			err := responseError(resp)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return ReceivingAccounts{}, errors.New("PlutoFish 尚未提供接收网盘列表接口，请先升级服务端")
			}
			return ReceivingAccounts{}, err
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		_ = resp.Body.Close()
		var page struct {
			ReceivingAccounts
			NextAfter string `json:"nextAfter"`
			HasMore   bool   `json:"hasMore"`
		}
		if readErr != nil || len(data) > 1<<20 || !utf8.Valid(data) || json.Unmarshal(data, &page) != nil ||
			strings.TrimSpace(page.OwnerUID) == "" || len(page.OwnerUID) > 200 || strings.ContainsAny(page.OwnerUID, "\r\n\x00") || page.Items == nil || len(page.Items) > 100 {
			return ReceivingAccounts{}, errors.New("PlutoFish 接收网盘列表响应无效")
		}
		if result.OwnerUID != "" && page.OwnerUID != result.OwnerUID {
			return ReceivingAccounts{}, errors.New("PlutoFish 账号身份在翻页期间发生变化，请重新获取")
		}
		result.OwnerUID = page.OwnerUID
		for _, account := range page.Items {
			if account.ID == "" || len(account.ID) > 200 || strings.ContainsAny(account.ID, "\r\n\x00") || account.Name == "" || len(account.Name) > 1024 || account.Provider == "" || seen[account.ID] {
				return ReceivingAccounts{}, errors.New("PlutoFish 接收网盘列表包含无效或重复记录")
			}
			seen[account.ID] = true
			result.Items = append(result.Items, account)
		}
		if !page.HasMore {
			return result, nil
		}
		if len(page.Items) == 0 || page.NextAfter == "" || len(page.NextAfter) > 200 || page.NextAfter == after {
			return ReceivingAccounts{}, errors.New("PlutoFish 接收网盘列表未正常翻页")
		}
		after = page.NextAfter
	}
	return ReceivingAccounts{}, errors.New("接收网盘数量超过客户端读取上限，请联系管理员")
}
