package embyhelper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"film-fusion/app/config"
)

func TestPlaybackDashboardRejectsInvalidResponses(t *testing.T) {
	for _, body := range []string{"null", "{}", "<html>private-test-key</html>", "[broken"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			client := New(&config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "private-test-key"}})
			defer client.Close()
			_, err := client.ReadPlaybackSessions(context.Background())
			if err == nil || strings.Contains(err.Error(), "private-test-key") {
				t.Fatalf("invalid/sensitive response accepted: %v", err)
			}
			_, err = client.ReadPlaybackActivities(context.Background(), 0, 100)
			if err == nil || strings.Contains(err.Error(), "private-test-key") {
				t.Fatalf("invalid history accepted: %v", err)
			}
		})
	}
}

func TestPlaybackDashboardHTTPErrorSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); fmt.Fprint(w, "private-test-key") }))
	client := New(&config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "private-test-key"}})
	defer client.Close()
	_, err := client.ReadPlaybackSessions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "private-test-key") {
		t.Fatalf("unsafe HTTP error: %v", err)
	}
	server.Close()
	_, err = client.ReadPlaybackSessions(context.Background())
	if err == nil || strings.Contains(err.Error(), "private-test-key") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestPlaybackDashboardCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	client := New(&config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "private-test-key"}})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := client.ReadPlaybackSessions(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller cancellation lost: %v", err)
	}
}
