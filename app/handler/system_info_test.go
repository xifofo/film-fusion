package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type fakeSystemInfoRSSGenerator struct {
	url      string
	token    string
	tokenErr error
	worker   service.RSSGeneratorWorkerStatus
}

func (f fakeSystemInfoRSSGenerator) WorkerURL() string { return f.url }

func (f fakeSystemInfoRSSGenerator) WorkerAccessToken() (string, error) {
	return f.token, f.tokenErr
}

func (f fakeSystemInfoRSSGenerator) WorkerHealth(context.Context) service.RSSGeneratorWorkerStatus {
	return f.worker
}

func TestSystemInfoReturnsWorkerTokenWithoutCaching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewSystemInfoHandler(fakeSystemInfoRSSGenerator{
		url:   "http://rss-generator-worker:8787",
		token: "generated-worker-token",
		worker: service.RSSGeneratorWorkerStatus{
			Available: true, Status: "ok", Service: "rss-generator-worker", Version: "0.1.0", AuthConfigured: true,
		},
	})
	router := gin.New()
	router.GET("/api/system-info", handler.Get)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/system-info", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	for _, value := range []string{"generated-worker-token", "rss-generator-worker", "http://rss-generator-worker:8787"} {
		if !strings.Contains(recorder.Body.String(), value) {
			t.Fatalf("response missing %q: %s", value, recorder.Body.String())
		}
	}
}

func TestSystemInfoReturnsTokenReadErrorWithoutFailingThePage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewSystemInfoHandler(fakeSystemInfoRSSGenerator{
		url:      "http://rss-generator-worker:8787",
		tokenErr: errors.New("RSS Worker Token 尚未生成"),
		worker:   service.RSSGeneratorWorkerStatus{Status: "unavailable"},
	})
	router := gin.New()
	router.GET("/api/system-info", handler.Get)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/system-info", nil))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "RSS Worker Token 尚未生成") {
		t.Fatalf("unexpected response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"token":"`) {
		t.Fatalf("empty token should not be serialized: %s", recorder.Body.String())
	}
}
