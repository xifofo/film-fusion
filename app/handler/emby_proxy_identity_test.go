package handler

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestEmbyAuthenticationResponsePreservesBodyAndCachesRealUser(t *testing.T) {
	for _, encoding := range []string{"", "gzip"} {
		t.Run("encoding="+encoding, func(t *testing.T) {
			payload := []byte(`{"AccessToken":"login-token","User":{"Id":"real-user"},"SessionInfo":{"UserId":"real-user"}}`)
			wire := payload
			if encoding == "gzip" {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				_, _ = writer.Write(payload)
				_ = writer.Close()
				wire = compressed.Bytes()
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/emby/Users/AuthenticateByName" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
					t.Error("browser encoding was forwarded")
				}
				w.Header().Set("Content-Type", "application/json")
				if encoding != "" {
					w.Header().Set("Content-Encoding", encoding)
				}
				_, _ = w.Write(wire)
			}))
			defer upstream.Close()
			base := identityTestHandler(t, upstream.URL)
			h := NewEmbyProxyHandler(base.config, base.logger, nil)
			request := httptest.NewRequest(http.MethodPost, "/emby/Users/AuthenticateByName?UserId=forged", strings.NewReader(`{"Username":"name","Pw":"private-password"}`))
			request.Header.Set("Accept-Encoding", "gzip, br, zstd")
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = request
			recorder := httptest.NewRecorder()
			h.proxy.ServeHTTP(recorder, ctx.Request)
			if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
				t.Fatalf("proxy changed login response: status=%d", recorder.Code)
			}
			if got := h.resolveEmbyUserID(identityTestContext("login-token", ""), "film"); got != "real-user" {
				t.Fatalf("login token mapped to %q", got)
			}
		})
	}
}

func TestEmbyIdentityIgnoresUntrustedResponsesAndClearsRevokedTokens(t *testing.T) {
	h := identityTestHandler(t, "http://upstream.invalid")
	for _, test := range []struct {
		path   string
		status int
		body   string
	}{
		{"/Users/AuthenticateByName", 401, `{"AccessToken":"bad","User":{"Id":"forged"}}`},
		{"/Items/1", 200, `{"AccessToken":"bad","User":{"Id":"forged"}}`},
		{"/Users/AuthenticateByName", 200, `{"AccessToken":"bad"}`},
		{"/Users/AuthenticateByName", 200, `{"AccessToken":"bad","User":{"Id":"forged"}} trailing`},
		{"/Users/AuthenticateByName", 200, strings.Repeat(" ", maxEmbyIdentityResponseBytes+1)},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, nil)
		response := &http.Response{StatusCode: test.status, Request: request, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(test.body))}
		h.observeEmbyIdentityResponse(response)
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(body) != test.body {
			t.Fatal("response body was consumed or changed")
		}
		if len(h.goCache.Items()) != 0 {
			t.Fatalf("untrusted response cached: %s", test.path)
		}
	}
	for _, test := range []struct {
		path   string
		status int
	}{{"/Sessions/Logout", 204}, {"/Items/1", 401}} {
		h.goCache.SetDefault(h.embyIdentityKey("login", "token"), "user")
		h.goCache.SetDefault(h.embyIdentityKey("device", "token", "device"), "user")
		request := httptest.NewRequest(http.MethodPost, test.path, nil)
		request.Header.Set("X-Emby-Token", "token")
		h.observeEmbyIdentityResponse(&http.Response{StatusCode: test.status, Request: request})
		if len(h.goCache.Items()) != 0 {
			t.Fatal("revoked token retained identity")
		}
	}
}

func TestEmbyDeviceIDExtraction(t *testing.T) {
	for _, test := range []struct{ header, value, query string }{
		{"X-Emby-Device-Id", "device", ""},
		{"X-MediaBrowser-Device-Id", "device", ""},
		{"Authorization", `MediaBrowser Client="SenPlayer", DeviceId="device", Token="secret"`, ""},
		{"X-Emby-Authorization", `Emby DeviceId=device, Token=secret`, ""},
		{"", "", "DeviceId=device"},
		{"", "", "X-Emby-Device-Id=device"},
		{"", "", "deviceid=device"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/?"+test.query, nil)
		if test.header != "" {
			r.Header.Set(test.header, test.value)
		}
		if got := embyDeviceIDFromRequest(r); got != "device" {
			t.Fatalf("header=%s query=%s device=%q", test.header, test.query, got)
		}
	}
}

func TestEmbyDeviceCacheIsScopedToToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"DeviceId":"shared-device","UserId":%q}]`, "user-"+r.Header.Get("X-Emby-Token"))
	}))
	defer server.Close()
	h := identityTestHandler(t, server.URL)
	for _, token := range []string{"a", "b", "a", "b"} {
		if got := h.resolveEmbyUserID(identityTestContext(token, "shared-device"), "same-film"); got != "user-"+token {
			t.Fatalf("device reused another token's user: %q", got)
		}
	}
}

func TestEmbyDevicesSelectTheirOwnBindingOnSharedIPAndFilm(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "bindings.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB; sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.CloudStorage{}, &model.EmbyAccountBinding{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := db.Create(&model.EmbyAccountBinding{EmbyUserID: fmt.Sprintf("user-%d", i), CloudStorageID: uint(i + 10), Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/Sessions" {
			t.Error("unexpected identity endpoint")
		}
		device := r.URL.Query().Get("DeviceId")
		token := r.Header.Get("X-Emby-Token")
		if token != "token-"+strings.TrimPrefix(device, "device-") {
			t.Error("device credentials crossed users")
		}
		w.Header().Set("Content-Type", "application/json")
		// 故意返回未过滤列表，客户端仍必须自行验证 DeviceId。
		fmt.Fprint(w, `[{"DeviceId":"device-1","UserId":"user-1"},{"DeviceId":"device-2","UserId":"user-2"}]`)
	}))
	defer server.Close()
	h := identityTestHandler(t, server.URL)
	for range 2 {
		for i := 1; i <= 2; i++ {
			c := identityTestContext(fmt.Sprintf("token-%d", i), fmt.Sprintf("device-%d", i))
			if got := h.resolveBoundStorageID(c, "same-film"); got != uint(i+10) {
				t.Fatalf("user-%d selected storage %d", i, got)
			}
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d, caches must be isolated per token and device", calls)
	}
	// 改变上游或授权配置，先前身份不能用于新的服务器。
	key := h.embyIdentityKey("login", "known-token")
	h.goCache.SetDefault(key, "user-1")
	h.config.Emby.URL += "/other-server"
	if got := h.cachedEmbyUserID(h.embyIdentityKey("login", "known-token")); got != "" {
		t.Fatal("identity crossed servers")
	}
	h.config.Emby.URL = server.URL
	h.config.Emby.APIKey = "new-admin-key"
	if got := h.cachedEmbyUserID(h.embyIdentityKey("login", "known-token")); got != "" {
		t.Fatal("identity survived credential configuration change")
	}
}
