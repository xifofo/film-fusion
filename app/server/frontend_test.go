package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 两套 SPA 使用相同文件名，确保页面回退和资源都只读取所属版本。
func TestFrontendCoexistence(t *testing.T) {
	dist := t.TempDir()
	files := map[string]string{
		"index.html":        "<!doctype html><title>legacy</title>",
		"assets/main.js":    "console.log('legacy')",
		"logo.svg":          "<svg>legacy</svg>",
		"v2/index.html":     "<!doctype html><title>admin</title>",
		"v2/assets/main.js": "console.log('admin')",
		"v2/logo.svg":       "<svg>admin</svg>",
	}
	for name, content := range files {
		filename := filepath.Join(dist, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	router := gin.New()
	router.NoRoute(frontendHandler(dist))
	router.GET("/api/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"source": "api"}) })
	router.POST("/webhook/ping", func(c *gin.Context) { c.Status(http.StatusAccepted) })

	tests := []struct {
		name     string
		method   string
		url      string
		status   int
		body     string
		location string
	}{
		{"legacy_root", "GET", "/", 200, files["index.html"], ""},
		{"legacy_login", "GET", "/user/login", 200, files["index.html"], ""},
		{"legacy_deep_link", "GET", "/automations/42?tab=history", 200, files["index.html"], ""},
		{"legacy_asset", "GET", "/assets/main.js", 200, files["assets/main.js"], ""},
		{"legacy_logo", "GET", "/logo.svg", 200, files["logo.svg"], ""},
		{"new_redirect", "GET", "/v2", 308, "", "/v2/"},
		{"new_redirect_query", "GET", "/v2?from=%2Fautomations", 308, "", "/v2/?from=%2Fautomations"},
		{"new_root", "GET", "/v2/", 200, files["v2/index.html"], ""},
		{"new_login", "GET", "/v2/login", 200, files["v2/index.html"], ""},
		{"new_deep_link", "GET", "/v2/automations/42?tab=history", 200, files["v2/index.html"], ""},
		{"new_trailing_slash", "GET", "/v2/directories/", 200, files["v2/index.html"], ""},
		{"new_index", "GET", "/v2/index.html", 200, files["v2/index.html"], ""},
		{"new_asset", "GET", "/v2/assets/main.js?v=1", 200, files["v2/assets/main.js"], ""},
		{"new_logo", "GET", "/v2/logo.svg", 200, files["v2/logo.svg"], ""},
		{"prefix_boundary", "GET", "/v20/directories", 200, files["index.html"], ""},
		{"legacy_missing_asset", "GET", "/assets/removed.js", 404, "", ""},
		{"new_missing_asset", "GET", "/v2/assets/removed.js", 404, "", ""},
		{"new_missing_extensionless_asset", "GET", "/v2/assets/removed", 404, "", ""},
		{"no_directory_listing", "GET", "/v2/assets/", 404, "", ""},
		{"no_post_fallback", "POST", "/v2/directories", 404, "", ""},
		{"api_route", "GET", "/api/ping", 200, `{"source":"api"}`, ""},
		{"api_missing", "GET", "/api/not-found", 404, `{"error":"API路径不存在"}`, ""},
		{"webhook_route", "POST", "/webhook/ping", 202, "", ""},
		{"webhook_missing", "POST", "/webhook/not-found", 404, `{"error":"API路径不存在"}`, ""},
		{"new_head", "HEAD", "/v2/directories", 200, "", ""},
		{"legacy_head", "HEAD", "/user/login", 200, "", ""},
		{"encoded_parent", "GET", "/v2/%2e%2e/assets/main.js", 400, "", ""},
		{"parent_escape", "GET", "/../config.yaml", 400, "", ""},
		{"windows_separator", "GET", "/v2/..%5cassets%5cmain.js", 400, "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.url, nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, test.status, response.Body.String())
			}
			if test.location != "" {
				if got := response.Header().Get("Location"); got != test.location {
					t.Fatalf("redirect = %q, want %q", got, test.location)
				}
			} else if test.body != "" && response.Body.String() != test.body {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.body)
			}
			if test.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatal("HEAD must not return a body")
			}
			if strings.HasPrefix(test.body, "<!doctype html>") {
				if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
					t.Fatal("SPA entry must use text/html")
				}
				if response.Header().Get("Cache-Control") != "no-cache" {
					t.Fatal("SPA entry must revalidate after an upgrade")
				}
			}
		})
	}
}

// 漏打包新版时明确返回 404，不能让 /v2/ 静默进入旧版。
func TestFrontendMissingNewBundle(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.NoRoute(frontendHandler(dist))

	for _, url := range []string{"/v2/", "/v2/login", "/v2/assets/main.js"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "legacy") {
			t.Fatalf("%s must not serve legacy bundle: status = %d, body = %q", url, response.Code, response.Body.String())
		}
	}
}
