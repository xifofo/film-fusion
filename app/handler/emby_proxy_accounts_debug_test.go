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

type playbackDebugCapture struct{ lines []string }

func (capture *playbackDebugCapture) Debugf(format string, args ...interface{}) {
	capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
}

func identityTestHandler(t *testing.T, base string) *EmbyProxyHandler {
	t.Helper()
	log := logger.New(config.LogConfig{Output: "stdout", Level: "error"})
	t.Cleanup(func() { _ = log.Close() })
	return &EmbyProxyHandler{config: &config.Config{Emby: config.EmbyConfig{URL: base, APIKey: "private-admin-key"}}, logger: log, goCache: cache.New(cache.DefaultExpiration, 0)}
}

func identityTestContext(token, device string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/emby/Videos/same-film/stream.mkv?UserId=forged-user", nil)
	c.Request.RemoteAddr = "192.0.2.1:1234"
	c.Request.Header.Set("X-Emby-Token", token)
	c.Request.Header.Set("X-Emby-Device-Id", device)
	return c
}

func TestEmbyUserDebugDeviceLookupAndCache(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/Sessions" {
			t.Error("must not call /Users/Me")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"DeviceId":"private-device","UserId":"user-1","UserName":"private-name"}]`)
	}))
	defer server.Close()
	h := identityTestHandler(t, server.URL)
	capture := &playbackDebugCapture{}
	c := identityTestContext("private-user-token", "private-device")
	c.Request = c.Request.WithContext(embydebug.Start(context.Background(), capture))
	for range 2 {
		if got := h.resolveEmbyUserID(c, "same-film"); got != "user-1" {
			t.Fatalf("user=%q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("session calls=%d", calls)
	}
	output := strings.Join(capture.lines, "\n")
	for _, want := range []string{"http_status=200", "method=device", "method=device_cache"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing diagnostic %q", want)
		}
	}
	for _, secret := range []string{"private-user-token", "private-admin-key", "private-name", "private-device", "192.0.2.1"} {
		if strings.Contains(output, secret) {
			t.Errorf("diagnostic leaked %q", secret)
		}
		for key := range h.goCache.Items() {
			if strings.Contains(key, secret) {
				t.Errorf("cache key leaked %q", secret)
			}
		}
	}
}

func TestEmbyIdentityDoesNotCacheFailureOrUnresolved(t *testing.T) {
	for _, status := range []int{200, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					w.WriteHeader(status)
					fmt.Fprint(w, `[]`)
					return
				}
				fmt.Fprint(w, `[{"DeviceId":"device","UserId":"user"}]`)
			}))
			defer server.Close()
			h := identityTestHandler(t, server.URL)
			c := identityTestContext("token", "device")
			if got := h.resolveEmbyUserID(c, "same-film"); got != "" {
				t.Fatalf("unexpected identity %q", got)
			}
			if got := h.resolveEmbyUserID(c, "same-film"); got != "user" || calls != 2 {
				t.Fatalf("user=%q calls=%d", got, calls)
			}
		})
	}
}

func TestEmbyIdentityRequiresTokenAndDevice(t *testing.T) {
	h := identityTestHandler(t, "http://unused.invalid")
	for _, pair := range [][2]string{{"", "device"}, {"private-admin-key", "device"}, {"token", ""}} {
		if got := h.resolveEmbyUserID(identityTestContext(pair[0], pair[1]), "same-film"); got != "" {
			t.Fatalf("guessed identity %q", got)
		}
	}
}
