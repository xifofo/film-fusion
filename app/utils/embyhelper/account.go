package embyhelper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"film-fusion/app/config"
)

var ErrAccountServerChanged = errors.New("Emby 服务器已变更，请刷新用户列表后重试")

// EmbyAccount retains the entire policy, including fields unknown to FilmFusion.
// A partial UserPolicy POST can reset unrelated permissions on Emby.
type EmbyAccount struct {
	ID               string                     `json:"Id"`
	Name             string                     `json:"Name"`
	ServerID         string                     `json:"ServerId"`
	LastLoginDate    string                     `json:"LastLoginDate"`
	LastActivityDate string                     `json:"LastActivityDate"`
	Policy           map[string]json.RawMessage `json:"Policy"`
}

func (a EmbyAccount) IsDisabled() bool {
	var value bool
	_ = json.Unmarshal(a.Policy["IsDisabled"], &value)
	return value
}
func (a EmbyAccount) IsAdministrator() bool {
	var value bool
	_ = json.Unmarshal(a.Policy["IsAdministrator"], &value)
	return value
}

// EmbyAccountClient snapshots connection settings for a complete read/update/verify
// operation. Hot reload must not redirect a policy write to another server.
type EmbyAccountClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewAccountClient(cfg *config.Config) (*EmbyAccountClient, error) {
	if cfg == nil || strings.TrimSpace(cfg.Emby.URL) == "" || strings.TrimSpace(cfg.Emby.APIKey) == "" {
		return nil, errors.New("请先在系统设置中配置 Emby 地址和管理员 API Key")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.Emby.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Emby 地址无效")
	}
	return &EmbyAccountClient{
		baseURL: strings.TrimRight(u.String(), "/"), apiKey: strings.TrimSpace(cfg.Emby.APIKey),
		http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (e *EmbyAccountClient) request(ctx context.Context, method, path string, body any, result any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return errors.New("编码 Emby 用户策略失败")
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, payload)
	if err != nil {
		return errors.New("创建 Emby 请求失败")
	}
	req.Header.Set("X-Emby-Token", e.apiKey)
	req.Header.Set("Authorization", fmt.Sprintf("MediaBrowser Token=%q", e.apiKey))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.http.Do(req)
	if err != nil {
		// Transport errors and upstream bodies can contain secrets/URLs. Do not
		// persist them in the execution log or return them to the browser.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("连接 Emby 失败或请求超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Emby 请求失败（HTTP %d）", resp.StatusCode)
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(result); err != nil {
			return errors.New("Emby 用户响应格式无效")
		}
	}
	return nil
}

func validateAccount(a EmbyAccount) error {
	if a.ID == "" || a.ServerID == "" || a.Policy == nil {
		return errors.New("Emby 用户响应缺少用户、服务器或权限信息")
	}
	var disabled bool
	if value := a.Policy["IsDisabled"]; len(value) == 0 || string(value) == "null" || json.Unmarshal(value, &disabled) != nil {
		return errors.New("Emby 用户响应缺少有效的禁用状态")
	}
	return nil
}

func (e *EmbyAccountClient) ListAccounts(ctx context.Context) ([]EmbyAccount, error) {
	accounts := make([]EmbyAccount, 0)
	if err := e.request(ctx, http.MethodGet, "/Users", nil, &accounts); err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if err := validateAccount(account); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

func (e *EmbyAccountClient) GetAccount(ctx context.Context, serverID, userID string) (*EmbyAccount, error) {
	if userID == "" || userID == "." || userID == ".." || strings.ContainsAny(userID, "/\\?#") {
		return nil, errors.New("Emby 用户 ID 无效")
	}
	var account EmbyAccount
	if err := e.request(ctx, http.MethodGet, "/Users/"+url.PathEscape(userID), nil, &account); err != nil {
		return nil, err
	}
	if err := validateAccount(account); err != nil {
		return nil, err
	}
	if account.ServerID != serverID {
		return nil, ErrAccountServerChanged
	}
	if account.ID != userID {
		return nil, errors.New("Emby 返回的用户 ID 不匹配")
	}
	return &account, nil
}

// DisableAccount reads the latest policy and verifies the actual resulting state.
// See https://dev.emby.media/reference/RestAPI/UserService/postUsersByIdPolicy.html
func (e *EmbyAccountClient) DisableAccount(ctx context.Context, serverID, userID string) error {
	account, err := e.GetAccount(ctx, serverID, userID)
	if err != nil {
		return err
	}
	if account.IsDisabled() {
		return nil
	}
	account.Policy["IsDisabled"] = json.RawMessage("true")
	if err := e.request(ctx, http.MethodPost, "/Users/"+url.PathEscape(userID)+"/Policy", account.Policy, nil); err != nil {
		return err
	}
	verified, err := e.GetAccount(ctx, serverID, userID)
	if err != nil {
		return err
	}
	if !verified.IsDisabled() {
		return errors.New("Emby 尚未确认账户已禁用，将自动重试")
	}
	return nil
}
