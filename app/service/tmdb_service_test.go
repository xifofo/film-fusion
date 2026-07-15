package service

import (
	"context"
	"film-fusion/app/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTMDBServiceGetTVEpisodeCount(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/3/tv/250900" {
			t.Fatalf("path=%s want /3/tv/250900", r.URL.Path)
		}
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Fatalf("api_key=%q want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number_of_episodes":28}`))
	}))
	defer server.Close()

	svc := NewTMDBService(&config.Config{
		TMDB: config.TMDBConfig{
			Enabled:        true,
			BaseURL:        server.URL,
			APIKey:         "test-key",
			TimeoutSeconds: 1,
			CacheMinutes:   60,
		},
	}, nil)

	count, err := svc.GetTVEpisodeCount(context.Background(), "250900")
	if err != nil {
		t.Fatalf("GetTVEpisodeCount error: %v", err)
	}
	if count != 28 {
		t.Fatalf("count=%d want 28", count)
	}

	count, err = svc.GetTVEpisodeCount(context.Background(), "250900")
	if err != nil {
		t.Fatalf("GetTVEpisodeCount cached error: %v", err)
	}
	if count != 28 || requests != 1 {
		t.Fatalf("cached count=%d requests=%d want count=28 requests=1", count, requests)
	}
}
