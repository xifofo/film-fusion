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

// 根路径只提供新版 SPA；历史前缀仅跳转，API 和静态资源错误不回退为 HTML。
func TestFrontendRoot(t *testing.T) {
	dist := t.TempDir()
	files := map[string]string{
		"index.html":     "<!doctype html><title>admin</title>",
		"assets/main.js": "console.log('admin')",
		"logo.svg":       "<svg>admin</svg>",
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
		{"root", "GET", "/", 200, files["index.html"], ""},
		{"login", "GET", "/login", 200, files["index.html"], ""},
		{"old_login", "GET", "/user/login", 200, files["index.html"], ""},
		{"deep_link", "GET", "/automations/42?tab=history", 200, files["index.html"], ""},
		{"trailing_slash", "GET", "/media-directories/", 200, files["index.html"], ""},
		{"asset", "GET", "/assets/main.js", 200, files["assets/main.js"], ""},
		{"logo", "GET", "/logo.svg", 200, files["logo.svg"], ""},
		{"old_prefix", "GET", "/v2", 308, "", "/"},
		{"old_prefix_query", "GET", "/v2?from=%2Fautomations", 308, "", "/?from=%2Fautomations"},
		{"old_root", "GET", "/v2/", 308, "", "/"},
		{"old_prefixed_login", "GET", "/v2/login", 308, "", "/login"},
		{"old_deep_link", "GET", "/v2/automations/42?tab=history", 308, "", "/automations/42?tab=history"},
		{"old_trailing_slash", "GET", "/v2/directories/", 308, "", "/directories/"},
		{"old_index", "GET", "/v2/index.html", 308, "", "/index.html"},
		{"old_asset", "GET", "/v2/assets/main.js?v=1", 308, "", "/assets/main.js?v=1"},
		{"old_logo", "GET", "/v2/logo.svg", 308, "", "/logo.svg"},
		{"escaped_path", "GET", "/v2/media%3Fname%23details?tab=history", 308, "", "/media%3Fname%23details?tab=history"},
		{"prefix_boundary", "GET", "/v20/directories", 200, files["index.html"], ""},
		{"missing_asset", "GET", "/assets/removed.js", 404, "", ""},
		{"missing_extensionless_asset", "GET", "/assets/removed", 404, "", ""},
		{"no_directory_listing", "GET", "/assets/", 404, "", ""},
		{"no_post_fallback", "POST", "/v2/directories", 404, "", ""},
		{"api_route", "GET", "/api/ping", 200, `{"source":"api"}`, ""},
		{"api_missing", "GET", "/api/not-found", 404, `{"error":"API路径不存在"}`, ""},
		{"webhook_route", "POST", "/webhook/ping", 202, "", ""},
		{"webhook_missing", "POST", "/webhook/not-found", 404, `{"error":"API路径不存在"}`, ""},
		{"head", "HEAD", "/media-directories", 200, "", ""},
		{"old_head", "HEAD", "/v2/directories", 308, "", "/directories"},
		{"encoded_parent", "GET", "/v2/%2e%2e/assets/main.js", 400, "", ""},
		{"parent_escape", "GET", "/../config.yaml", 400, "", ""},
		{"windows_separator", "GET", "/v2/..%5cassets%5cmain.js", 400, "", ""},
		{"no_external_redirect", "GET", "/v2//example.com", 400, "", ""},
		{"no_encoded_external_redirect", "GET", "/v2/%2fexample.com", 400, "", ""},
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

// 缺少根入口时不回退到残留的第二套产物。
func TestFrontendMissingRootBundle(t *testing.T) {
	dist := t.TempDir()
	if err := os.Mkdir(filepath.Join(dist, "v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "v2", "index.html"), []byte("stale bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.NoRoute(frontendHandler(dist))

	for _, url := range []string{"/", "/login", "/assets/main.js"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "stale bundle") {
			t.Fatalf("%s must not serve stale bundle: status = %d, body = %q", url, response.Code, response.Body.String())
		}
	}
}
