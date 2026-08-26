package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAutomationHTTPRequestRendersTemplatesAgainstLocalMock(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/media/1396" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Media-Type") != "tv" {
			http.Error(w, "missing rendered header", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"tmdb_id":"1396"}` {
			http.Error(w, "unexpected body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	automation := &AutomationService{}
	output, err := automation.executeAutomationHTTPRequest(context.Background(), AutomationNode{
		Type: AutomationNodeHTTPRequest,
		Config: map[string]any{
			"method":                "POST",
			"url":                   server.URL + "/media/{{nodes.mp.output.tmdb_id}}",
			"headers":               map[string]any{"X-Media-Type": "{{nodes.mp.output.media_type}}"},
			"body":                  `{"tmdb_id":"{{nodes.mp.output.tmdb_id}}"}`,
			"allow_private_network": true,
		},
	}, automationTestRunContext("mp", map[string]any{"tmdb_id": "1396", "media_type": "tv"}))
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || output["selected_port"] != "success" || output["status_code"] != http.StatusOK {
		t.Fatalf("unexpected HTTP output: %#v, requests=%d", output, requests.Load())
	}
	if _, ok := output["json"].(map[string]any); !ok {
		t.Fatalf("JSON response was not decoded: %#v", output)
	}
}

func TestAutomationHTTPRequestBlocksPrivateNetworkByDefault(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	automation := &AutomationService{}
	_, err := automation.executeAutomationHTTPRequest(context.Background(), AutomationNode{
		Type:   AutomationNodeHTTPRequest,
		Config: map[string]any{"method": "GET", "url": server.URL},
	}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "不允许的地址") {
		t.Fatalf("private target error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("blocked private target received %d requests", requests.Load())
	}
}

func TestAutomationHTTPRequestRoutesNon2xxToFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	automation := &AutomationService{}
	output, err := automation.executeAutomationHTTPRequest(context.Background(), AutomationNode{
		Type:   AutomationNodeHTTPRequest,
		Config: map[string]any{"method": "GET", "url": server.URL, "allow_private_network": true},
	}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "failure" || output["status_code"] != http.StatusNotFound {
		t.Fatalf("unexpected non-2xx output: %#v", output)
	}
}

func TestAutomationHTTPRequestRejectsCrossOriginRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, target.URL, http.StatusFound)
	}))
	defer source.Close()

	automation := &AutomationService{}
	_, err := automation.executeAutomationHTTPRequest(context.Background(), AutomationNode{
		Type: AutomationNodeHTTPRequest,
		Config: map[string]any{
			"method":                "GET",
			"url":                   source.URL,
			"allow_private_network": true,
			"follow_redirects":      true,
		},
	}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "必须保持同源") {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("cross-origin target received %d requests", redirectedRequests.Load())
	}
}

func TestValidateAutomationHTTPRequestURLRejectsMalformedURL(t *testing.T) {
	if _, err := validateAutomationHTTPRequestURL("http://%zz"); err == nil {
		t.Fatal("malformed URL should be rejected")
	}
}
