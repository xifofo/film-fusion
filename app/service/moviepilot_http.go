package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (s *MoviePilotService) isConfigured() bool {
	return strings.TrimSpace(s.cfg.MoviePilot.API) != "" &&
		strings.TrimSpace(s.cfg.MoviePilot.Username) != "" &&
		strings.TrimSpace(s.cfg.MoviePilot.Password) != ""
}

func (s *MoviePilotService) baseURL() string {
	return strings.TrimRight(strings.TrimSpace(s.cfg.MoviePilot.API), "/")
}

func (s *MoviePilotService) doGet(endpointPath string, query url.Values) ([]byte, error) {
	token, err := s.GetAccessToken()
	if err != nil {
		return nil, err
	}

	endpoint := s.baseURL() + endpointPath
	if len(query) > 0 {
		endpoint = endpoint + "?" + query.Encode()
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(s.requestContext(), "GET", endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Re-authenticate once, matching the manual-transfer endpoint. A valid
		// login followed by persistent 401 must not recurse forever.
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			if refreshed, refreshErr := s.refreshTokenAfterRejection(token); refreshErr == nil {
				token = refreshed
				continue
			}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("MoviePilot 请求失败: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return body, nil
	}
}

func validateMoviePilotSuccess(body []byte) error {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		// Some older endpoints return an unwrapped array. Their existing parsers
		// remain authoritative when no business-status object is present.
		return nil
	}
	rawSuccess, exists := root["success"]
	if !exists {
		return nil
	}

	success, recognized := moviePilotSuccessValue(rawSuccess)
	if !recognized || success {
		return nil
	}

	message := strings.TrimSpace(extractString(root, "message", "msg", "detail", "error"))
	if message == "" {
		if data, ok := root["data"].(string); ok {
			message = strings.TrimSpace(data)
		}
	}
	if message == "" {
		message = "未返回错误详情"
	}
	return fmt.Errorf("MoviePilot 业务请求失败: %s", message)
}

func moviePilotSuccessValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case float64:
		return typed != 0, true
	case json.Number:
		n, err := typed.Float64()
		return n != 0, err == nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}
