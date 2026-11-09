package handler

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const maxEmbyIdentityResponseBytes = 1 << 20

func embyRequestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	path := strings.TrimSuffix(req.URL.Path, "/")
	if len(path) >= 5 && strings.EqualFold(path[:5], "/emby") {
		path = path[5:]
	}
	return path
}

func isEmbyAuthenticationRequest(req *http.Request) bool {
	if req == nil || req.Method != http.MethodPost {
		return false
	}
	path := embyRequestPath(req)
	if strings.EqualFold(path, "/Users/AuthenticateByName") {
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) == 3 && strings.EqualFold(parts[0], "Users") && strings.EqualFold(parts[2], "Authenticate")
}

var embyDeviceIDRegex = regexp.MustCompile(`(?i)(?:^|[\s,])DeviceId="?([^",\s]+)"?`)

func embyDeviceIDFromRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	for _, name := range []string{"X-Emby-Device-Id", "X-MediaBrowser-Device-Id"} {
		if id := strings.TrimSpace(req.Header.Get(name)); id != "" {
			return id
		}
	}
	for _, name := range []string{"Authorization", "X-Emby-Authorization"} {
		if match := embyDeviceIDRegex.FindStringSubmatch(req.Header.Get(name)); len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	}
	for _, name := range []string{"DeviceId", "X-Emby-Device-Id", "X-MediaBrowser-Device-Id"} {
		for key, values := range req.URL.Query() {
			if strings.EqualFold(key, name) && len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
	}
	return ""
}

// 隔离不同上游、管理员配置及 Token；缓存键不包含明文凭据。
func (h *EmbyProxyHandler) embyIdentityPrefix(token string) string {
	parts, _ := json.Marshal([]string{strings.TrimRight(strings.TrimSpace(h.config.Emby.URL), "/"), h.config.Emby.APIKey, strings.TrimSpace(token)})
	return fmt.Sprintf("emby-identity:%x:", sha256.Sum256(parts))
}

func (h *EmbyProxyHandler) embyIdentityKey(kind, token string, device ...string) string {
	data, _ := json.Marshal(device)
	return fmt.Sprintf("%s%s:%x", h.embyIdentityPrefix(token), kind, sha256.Sum256(data))
}

func (h *EmbyProxyHandler) cachedEmbyUserID(key string) string {
	if h.goCache != nil {
		if value, ok := h.goCache.Get(key); ok {
			if id, ok := value.(string); ok {
				return strings.TrimSpace(id)
			}
		}
	}
	return ""
}

func (h *EmbyProxyHandler) forgetEmbyIdentity(token string) {
	if h.goCache == nil || token == "" {
		return
	}
	prefix := h.embyIdentityPrefix(token)
	for key := range h.goCache.Items() {
		if strings.HasPrefix(key, prefix) {
			h.goCache.Delete(key)
		}
	}
}

// 只观察 Emby 的成功认证响应，不读取密码，不信任请求参数中的 UserId。
// 所有读出的字节原样放回响应，不改变播放器收到的状态、编码或内容。
func (h *EmbyProxyHandler) observeEmbyIdentityResponse(resp *http.Response) {
	if resp == nil || resp.Request == nil || h.goCache == nil {
		return
	}
	if resp.StatusCode == http.StatusUnauthorized ||
		(resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.Request.Method == http.MethodPost && strings.EqualFold(embyRequestPath(resp.Request), "/Sessions/Logout")) {
		h.forgetEmbyIdentity(embyTokenFromHTTPRequest(resp.Request))
	}
	if !isEmbyAuthenticationRequest(resp.Request) || resp.StatusCode != http.StatusOK || resp.Body == nil {
		return
	}
	original := resp.Body
	body, err := io.ReadAll(io.LimitReader(original, maxEmbyIdentityResponseBytes+1))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), original), original}
	if err != nil || len(body) > maxEmbyIdentityResponseBytes {
		return
	}
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return
		}
		decoded, readErr := io.ReadAll(io.LimitReader(reader, maxEmbyIdentityResponseBytes+1))
		_ = reader.Close()
		if readErr != nil || len(decoded) > maxEmbyIdentityResponseBytes {
			return
		}
		body = decoded
	case "", "identity":
	default:
		return
	}
	var result struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if json.Unmarshal(body, &result) != nil {
		return
	}
	token, id := strings.TrimSpace(result.AccessToken), strings.TrimSpace(result.User.ID)
	if token == "" || id == "" || token == strings.TrimSpace(h.config.Emby.APIKey) {
		return
	}
	h.goCache.Set(h.embyIdentityKey("login", token), id, h.userResolveCacheTTL())
}
