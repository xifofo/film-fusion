package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"film-fusion/app/config"
)

func TestTMDBServiceListLoginBackgrounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Fatalf("api_key=%q want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/3/movie/popular":
			fmt.Fprint(w, `{"results":[{"id":1,"backdrop_path":"/movie-popular.jpg","popularity":20}]}`)
		case "/3/tv/popular":
			fmt.Fprint(w, `{"results":[{"id":2,"backdrop_path":"/tv-popular.jpg","popularity":40},{"id":3,"backdrop_path":"","popularity":80}]}`)
		case "/3/movie/now_playing":
			fmt.Fprint(w, `{"results":[{"id":4,"backdrop_path":"/movie-latest.jpg","release_date":"2026-07-29"}]}`)
		case "/3/tv/on_the_air":
			fmt.Fprint(w, `{"results":[{"id":5,"backdrop_path":"/tv-latest.jpg","first_air_date":"2026-07-30"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{TMDB: config.TMDBConfig{
		Enabled:        true,
		BaseURL:        server.URL,
		APIKey:         "test-key",
		TimeoutSeconds: 2,
	}}
	service := NewTMDBService(cfg, nil)

	popular, err := service.ListLoginBackgrounds(context.Background(), "popular", 2)
	if err != nil {
		t.Fatalf("list popular backgrounds: %v", err)
	}
	wantPopular := []string{
		"https://image.tmdb.org/t/p/w1280/tv-popular.jpg",
		"https://image.tmdb.org/t/p/w1280/movie-popular.jpg",
	}
	if len(popular) != len(wantPopular) {
		t.Fatalf("popular=%v want %v", popular, wantPopular)
	}
	for index := range wantPopular {
		if popular[index] != wantPopular[index] {
			t.Fatalf("popular[%d]=%q want %q", index, popular[index], wantPopular[index])
		}
	}

	latest, err := service.ListLoginBackgrounds(context.Background(), "latest", 2)
	if err != nil {
		t.Fatalf("list latest backgrounds: %v", err)
	}
	wantLatest := []string{
		"https://image.tmdb.org/t/p/w1280/tv-latest.jpg",
		"https://image.tmdb.org/t/p/w1280/movie-latest.jpg",
	}
	for index := range wantLatest {
		if latest[index] != wantLatest[index] {
			t.Fatalf("latest[%d]=%q want %q", index, latest[index], wantLatest[index])
		}
	}
}
