package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type moviePilotTokenRefresh struct {
	done      chan struct{}
	token     string
	err       error
	cancelled bool
}

func (s *MoviePilotService) refreshToken() (string, error) {
	return s.refreshAccessToken(true, "")
}

func (s *MoviePilotService) refreshTokenAfterRejection(token string) (string, error) {
	return s.refreshAccessToken(true, token)
}

// Request scopes share one in-flight login on their state owner. Waiters can
// cancel independently. If the leading caller cancels, a live waiter can retry
// with its own context; ordinary provider failures are returned to all waiters.
func (s *MoviePilotService) refreshAccessToken(force bool, rejected string) (string, error) {
	state, ctx := s.sharedState(), s.requestContext()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		state.mu.Lock()
		valid := state.accessToken != "" && time.Now().Before(state.tokenExpiresAt.Add(-moviePilotTokenSkew))
		if valid && (!force || (rejected != "" && state.accessToken != rejected)) {
			token := state.accessToken
			state.mu.Unlock()
			return token, nil
		}
		if call := state.tokenRefresh; call != nil {
			state.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-call.done:
				if err := ctx.Err(); err != nil {
					return "", err
				}
				if call.cancelled {
					continue
				}
				return call.token, call.err
			}
		}
		call := &moviePilotTokenRefresh{done: make(chan struct{})}
		state.tokenRefresh = call
		state.mu.Unlock()

		token, err := s.fetchAccessToken()
		state.mu.Lock()
		call.token, call.err = token, err
		call.cancelled = err != nil && ctx.Err() != nil
		state.tokenRefresh = nil
		close(call.done)
		state.mu.Unlock()
		return token, err
	}
}

func (s *MoviePilotService) GetAccessToken() (string, error) {
	state := s.sharedState()
	if !s.isConfigured() {
		return "", errors.New("moviepilot 未配置")
	}

	state.mu.RLock()
	token := state.accessToken
	expiresAt := state.tokenExpiresAt
	state.mu.RUnlock()

	if token != "" && time.Now().Before(expiresAt.Add(-moviePilotTokenSkew)) {
		return token, nil
	}

	return s.refreshAccessToken(false, "")
}

func (s *MoviePilotService) fetchAccessToken() (string, error) {
	state := s.sharedState()
	if !s.isConfigured() {
		return "", errors.New("moviepilot 未配置")
	}

	loginURL := s.baseURL() + "/api/v1/login/access-token"
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", s.cfg.MoviePilot.Username)
	form.Set("password", s.cfg.MoviePilot.Password)

	req, err := http.NewRequestWithContext(s.requestContext(), "POST", loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("创建 MoviePilot 登录请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 MoviePilot 登录失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MoviePilot 登录失败: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	token, expiresIn, err := parseAccessToken(body)
	if err != nil {
		return "", err
	}

	if token == "" {
		return "", errors.New("MoviePilot 登录未返回 access_token")
	}

	expireAt := time.Now().Add(1 * time.Hour)
	if expiresIn > 0 {
		expireAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	}

	state.mu.Lock()
	state.accessToken = token
	state.tokenExpiresAt = expireAt
	state.mu.Unlock()

	return token, nil
}

func parseAccessToken(body []byte) (string, int64, error) {
	dataMap := unwrapDataMap(body)
	token := extractString(dataMap, "access_token", "token", "accessToken")
	expires := extractInt64(dataMap, "expires_in", "expires", "expire_in")
	if token == "" {
		if raw, ok := dataMap["data"]; ok {
			if m, ok := raw.(map[string]any); ok {
				token = extractString(m, "access_token", "token", "accessToken")
				expires = extractInt64(m, "expires_in", "expires", "expire_in")
			}
		}
	}
	return token, expires, nil
}
