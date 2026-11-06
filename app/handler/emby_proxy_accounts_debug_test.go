package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/utils/embydebug"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
)

type playbackDebugCapture struct {
	lines []string
}

func (capture *playbackDebugCapture) Debugf(format string, args ...interface{}) {
	capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
}

// 真实 HTTP 模拟接口不支持 /Users/Me 的情况：新日志必须说明状态，并保留会话兜底与缓存。
func TestEmbyUserDebugRetainsSessionFallbackAndCache(t *testing.T) {
	var tokenCalls, sessionCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Users/Me":
			tokenCalls++
			if r.Header.Get("X-Emby-Token") != "private-user-token" {
				t.Error("missing user credential on identity request")
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"private-response-body"}`)
		case "/Sessions":
			sessionCalls++
			fmt.Fprint(w, `[{"UserId":"user-1","UserName":"private-name","RemoteEndPoint":"192.0.2.1","NowPlayingItem":{"Id":"item-1"}}]`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	log := logger.New(config.LogConfig{Output: "stdout", Level: "error"})
	t.Cleanup(func() { _ = log.Close() })
	h := &EmbyProxyHandler{
		config:  &config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "private-admin-key"}},
		logger:  log,
		goCache: cache.New(cache.DefaultExpiration, 0),
	}
	capture := &playbackDebugCapture{}
	ctx := embydebug.Start(context.Background(), capture)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/emby/Videos/item-1/stream.mkv", nil).WithContext(ctx)
	c.Request.Header.Set("X-Emby-Token", "private-user-token")
	for range 2 {
		if got := h.resolveEmbyUserID(c, "item-1"); got != "user-1" {
			t.Fatalf("session fallback user = %q", got)
		}
	}
	if tokenCalls != 1 || sessionCalls != 1 {
		t.Fatalf("cache behavior changed: token=%d sessions=%d", tokenCalls, sessionCalls)
	}

	output := strings.Join(capture.lines, "\n")
	for _, want := range []string{"http_status=404", "next=session_fallback", "method=item", `method=session user_id="user-1"`, "result=cache_hit"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing diagnostic %q", want)
		}
	}
	trace := strings.Fields(capture.lines[0])[2]
	for _, line := range capture.lines {
		if !strings.Contains(line, trace) {
			t.Error("identity stages lost playback trace")
		}
	}
	for _, secret := range []string{"private-user-token", "private-admin-key", "private-response-body", "private-name", "192.0.2.1"} {
		if strings.Contains(output, secret) {
			t.Errorf("debug output leaked %q", secret)
		}
	}
}
