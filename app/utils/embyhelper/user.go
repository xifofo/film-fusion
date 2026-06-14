package embyhelper

import (
	"film-fusion/app/config"
	"fmt"
	"net"
	"net/http"
	"strings"

	"resty.dev/v3"
)

// EmbyUser 表示一个 Emby 用户(精简字段)
type EmbyUser struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// embySession /Sessions 响应里关心的精简字段
type embySession struct {
	UserID         string `json:"UserId"`
	UserName       string `json:"UserName"`
	RemoteEndPoint string `json:"RemoteEndPoint"`
	NowPlayingItem *struct {
		ID string `json:"Id"`
	} `json:"NowPlayingItem"`
}

// GetUserByToken 用播放请求里携带的用户 token 调用 /Users/Me 解析出当前 Emby 用户。
// token 为空、无效或为管理员 APIKey(无用户上下文)时无法解析，返回 nil, nil（非错误，调用方据此跳过绑定）。
func GetUserByToken(cfg *config.Config, token string) (*EmbyUser, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.Emby.URL), "/")
	if base == "" {
		return nil, fmt.Errorf("Emby URL 未配置")
	}

	client := resty.New()
	defer client.Close()
	client.SetBaseURL(base)

	var user EmbyUser
	r, err := client.R().
		// 同时带上标准 Authorization 头与传统 X-Emby-Token 头，兼容禁用了传统头的服务端
		SetHeader("Authorization", fmt.Sprintf("MediaBrowser Token=%q", token)).
		SetHeader("X-Emby-Token", token).
		SetResult(&user).
		Get("/Users/Me")
	if err != nil {
		return nil, fmt.Errorf("请求 Emby 当前用户失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		// 管理员 APIKey 或失效 token 调 /Users/Me 通常返回非 200，视为"无法识别具体用户"
		return nil, nil
	}
	if strings.TrimSpace(user.ID) == "" {
		return nil, nil
	}
	return &user, nil
}

// GetUserBySession 通过 /Sessions(管理员 APIKey) 反查当前正在播放该 item 的用户。
// 作为 GetUserByToken 的兜底：优先按 NowPlayingItem.Id 匹配；匹配不到再按客户端 IP 唯一匹配。
// 无法确定时返回 nil, nil。
func (e *EmbyClient) GetUserBySession(itemID, remoteIP string) (*EmbyUser, error) {
	var sessions []embySession
	r, err := e.client.R().
		SetResult(&sessions).
		Get("/Sessions")
	if err != nil {
		return nil, fmt.Errorf("请求 Emby 会话失败: %w", err)
	}
	if r.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("Emby 会话 HTTP %d: %s", r.StatusCode(), truncate(r.String(), 256))
	}

	itemID = strings.TrimSpace(itemID)
	remoteIP = strings.TrimSpace(remoteIP)

	// 1) 按正在播放的 item 精确匹配
	if itemID != "" {
		for i := range sessions {
			s := sessions[i]
			if strings.TrimSpace(s.UserID) == "" {
				continue
			}
			if s.NowPlayingItem != nil && strings.TrimSpace(s.NowPlayingItem.ID) == itemID {
				return &EmbyUser{ID: s.UserID, Name: s.UserName}, nil
			}
		}
	}

	// 2) 退化为按客户端 IP 匹配(仅当唯一命中时采用，避免误判)
	if remoteIP != "" {
		var matched *EmbyUser
		count := 0
		for i := range sessions {
			s := sessions[i]
			if strings.TrimSpace(s.UserID) == "" {
				continue
			}
			if sessionMatchesIP(s.RemoteEndPoint, remoteIP) {
				matched = &EmbyUser{ID: s.UserID, Name: s.UserName}
				count++
			}
		}
		if count == 1 {
			return matched, nil
		}
	}

	return nil, nil
}

func sessionMatchesIP(remoteEndPoint, ip string) bool {
	remoteEndPoint = strings.TrimSpace(remoteEndPoint)
	if remoteEndPoint == "" || ip == "" {
		return false
	}
	if remoteEndPoint == ip {
		return true
	}
	if host, _, err := net.SplitHostPort(remoteEndPoint); err == nil {
		return host == ip
	}
	return false
}

// ListUsers 列出 Emby 所有用户(使用管理员 APIKey)。
func (e *EmbyClient) ListUsers() ([]EmbyUser, error) {
	var users []EmbyUser
	r, err := e.client.R().
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
