package service

import (
	"context"
	"film-fusion/app/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTMDBServiceGetTVSeasonEpisodeCount(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Fatalf("api_key=%q want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/3/tv/250900/season/2":
			_, _ = w.Write([]byte(`{"episodes":[{"id":1},{"id":2},{"id":3}]}`))
		case "/3/tv/250900/season/3":
			_, _ = w.Write([]byte(`{"episodes":[{"id":4},{"id":5}]}`))
		default:
			t.Fatalf("unexpected path=%s", r.URL.Path)
		}
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

	count, err := svc.GetTVSeasonEpisodeCount(context.Background(), "250900", 2)
	if err != nil {
		t.Fatalf("GetTVSeasonEpisodeCount error: %v", err)
	}
	if count != 3 {
		t.Fatalf("count=%d want 3", count)
	}

	count, err = svc.GetTVSeasonEpisodeCount(context.Background(), "250900", 2)
	if err != nil {
		t.Fatalf("GetTVSeasonEpisodeCount cached error: %v", err)
	}
	if count != 3 || requests != 1 {
		t.Fatalf("cached count=%d requests=%d want count=3 requests=1", count, requests)
	}

	count, err = svc.GetTVSeasonEpisodeCount(context.Background(), "250900", 3)
	if err != nil {
		t.Fatalf("GetTVSeasonEpisodeCount other season error: %v", err)
	}
	if count != 2 || requests != 2 {
		t.Fatalf("other season count=%d requests=%d want count=2 requests=2", count, requests)
	}
}

func TestParseTMDBSeasonEpisodeCountResponseRequiresEpisodes(t *testing.T) {
	if _, err := ParseTMDBSeasonEpisodeCountResponse([]byte(`{}`)); err == nil {
		t.Fatal("missing episodes should fail")
	}
	count, err := ParseTMDBSeasonEpisodeCountResponse([]byte(`{"episodes":[]}`))
	if err != nil || count != 0 {
		t.Fatalf("empty season count=%d err=%v want count=0", count, err)
	}
}
