package embyhelper

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"film-fusion/app/config"
	"film-fusion/app/utils/embydebug"

	"resty.dev/v3"
)

// EmbyUser 表示一个 Emby 用户(精简字段)
type EmbyUser struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// embySession /Sessions 响应里关心的精简字段
type embySession struct {
	UserID   string `json:"UserId"`
	UserName string `json:"UserName"`
	DeviceID string `json:"DeviceId"`
}

// GetUserByDeviceContext 使用播放请求的用户 Token 查询指定设备的会话。
// 不携带管理员 API Key，不按影片或 IP 推断身份；同设备存在不同用户时拒绝选择。
func GetUserByDeviceContext(ctx context.Context, cfg *config.Config, token, deviceID string) (*EmbyUser, error) {
	token, deviceID = strings.TrimSpace(token), strings.TrimSpace(deviceID)
	if token == "" || deviceID == "" {
		return nil, nil
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.Emby.URL), "/")
	if base == "" {
		return nil, fmt.Errorf("Emby URL 未配置")
	}
	client := resty.New()
	client.SetTimeout(embyRequestTimeout)
	defer client.Close()
	client.SetBaseURL(base)
	var sessions []embySession
	r, err := client.R().SetContext(ctx).
		SetHeader("Authorization", fmt.Sprintf("MediaBrowser Token=%q", token)).
		SetHeader("X-Emby-Token", token).
		SetQueryParam("DeviceId", deviceID).
		SetResult(&sessions).Get("/Sessions")
	status := 0
	if r != nil {
		status = r.StatusCode()
	}
	embydebug.Logf(ctx, "user_session_http", "endpoint=/Sessions http_status=%d session_count=%d", status, len(sessions))
	if err != nil {
		return nil, fmt.Errorf("请求 Emby 设备会话失败: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Emby 设备会话 HTTP %d", status)
	}
	var matched *EmbyUser
	for _, session := range sessions {
		// 即使服务端忽略过滤参数，也不能选择其它设备的会话。
		if strings.TrimSpace(session.DeviceID) != deviceID || strings.TrimSpace(session.UserID) == "" {
			continue
		}
		id := strings.TrimSpace(session.UserID)
		if matched != nil && matched.ID != id {
			embydebug.Logf(ctx, "user_session_match", "method=device result=ambiguous")
			return nil, nil
		}
		matched = &EmbyUser{ID: id, Name: session.UserName}
	}
	if matched == nil {
		embydebug.Logf(ctx, "user_session_match", "method=device result=unresolved")
		return nil, nil
	}
	embydebug.Logf(ctx, "user_session_match", "method=device result=matched user_id=%q", matched.ID)
	return matched, nil
}

// ListUsers 列出 Emby 所有用户(使用管理员 APIKey)。
func (e *EmbyClient) ListUsers() ([]EmbyUser, error) {
	var users []EmbyUser
	r, err := e.client.R().SetContext(e.requestContext()).
		SetResult(&users).
		Get("/Users")
	if err != nil {
		return nil, fmt.Errorf("请求 Emby 用户列表失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("Emby 用户列表 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}
	out := make([]EmbyUser, 0, len(users))
	for _, u := range users {
		if strings.TrimSpace(u.ID) == "" {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}
