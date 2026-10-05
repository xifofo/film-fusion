package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/config"
)

func TestMoviePilotScopedCancellationIncludesFrozenPipeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	root := NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{API: server.URL, Username: "user", Password: "test"}}, nil)
	root.accessToken = "test"
	root.tokenExpiresAt = time.Now().Add(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	frozen, _, err := root.WithContext(ctx).FreezeMediaPipeline(MediaRecognitionSourceMoviePilot)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = frozen.RecognizeFileWithSource("Example.2024.mkv", MediaRecognitionSourceMoviePilot)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("provider ignored cancellation: %v", err)
	}
	if root.requestContext().Err() != nil || root.accessToken != "test" {
		t.Fatal("scoped provider mutated root")
	}
}

func TestMoviePilotContextScopesShareTokenCache(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"cached-token","expires_in":3600}`))
	}))
	defer server.Close()
	root := NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{API: server.URL, Username: "user", Password: "test"}}, nil)
	for range 2 {
		token, err := root.WithContext(context.Background()).GetAccessToken()
		if err != nil || token != "cached-token" {
			t.Fatalf("token=%s err=%v", token, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("request scopes bypassed token cache: %d calls", requests.Load())
	}
}

func TestMoviePilotManualTransferCancellationIncludesLogin(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	root := newTokenTestService(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := root.ManualTransfer(ctx, MoviePilotManualTransferRequest{SourcePath: "/downloads/example", FileType: "dir"})
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
		t.Fatalf("login ignored manual-transfer context: requests=%d err=%v", requests.Load(), err)
	}
	if root.requestContext().Err() != nil {
		t.Fatal("manual-transfer scope cancelled shared service")
	}
}
