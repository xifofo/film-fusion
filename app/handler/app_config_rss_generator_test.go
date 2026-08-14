package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"

	"github.com/gin-gonic/gin"
)

func TestAppConfigUpdatePayloadReadsMaskedRSSGeneratorWorkerToken(t *testing.T) {
	var payload appConfigUpdatePayload
	if err := json.Unmarshal([]byte(`{
		"config": {
			"rss_generator": {
				"worker_token": "new-worker-token-with-at-least-32-characters"
			}
		}
	}`), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if !payload.rssGeneratorWorkerTokenFieldPresent {
		t.Fatal("worker_token field was not detected")
	}
	merged, err := payload.mergedRSSGeneratorConfig(config.RSSGeneratorConfig{
		WorkerURL:   "http://rss-generator-worker:8787",
		WorkerToken: "existing-worker-token-with-at-least-32-characters",
	})
	if err != nil {
		t.Fatalf("merge token: %v", err)
	}
	if merged.WorkerToken != "new-worker-token-with-at-least-32-characters" {
		t.Fatalf("worker token = %q", merged.WorkerToken)
	}
	if merged.WorkerURL != "http://rss-generator-worker:8787" {
		t.Fatalf("worker URL changed unexpectedly: %q", merged.WorkerURL)
	}
}

func TestAppConfigUpdatePayloadKeepsExistingRSSGeneratorWorkerTokenWhenEmpty(t *testing.T) {
	var payload appConfigUpdatePayload
	if err := json.Unmarshal([]byte(`{"config":{"rss_generator":{"worker_token":""}}}`), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	const existing = "existing-worker-token-with-at-least-32-characters"
	merged, err := payload.mergedRSSGeneratorConfig(config.RSSGeneratorConfig{WorkerToken: existing})
	if err != nil {
		t.Fatalf("merge token: %v", err)
	}
	if merged.WorkerToken != existing {
		t.Fatalf("empty token replaced existing secret: %q", merged.WorkerToken)
	}
}

func TestAppConfigGetMasksRSSGeneratorWorkerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const token = "internal-worker-token-with-at-least-32-characters"
	cfg := &config.Config{RSSGenerator: config.RSSGeneratorConfig{WorkerToken: token}}
	handler := NewAppConfigHandler(nil, cfg, nil, nil)
	handler.db = nil

	router := gin.New()
	router.GET("/api/app-config", handler.Get)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/app-config", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := recorder.Body.String()
	if strings.Contains(response, token) || strings.Contains(response, `"worker_token":"`) {
		t.Fatalf("worker token leaked in response: %s", response)
	}
	if !strings.Contains(response, `"rss_generator.worker_token":true`) {
		t.Fatalf("masked secret state missing: %s", response)
	}
}
