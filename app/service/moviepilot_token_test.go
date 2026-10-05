package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/config"
)

func newTokenTestService(url string) *MoviePilotService {
	return NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{API: url, Username: "user", Password: "test"}}, nil)
}

func TestMoviePilotConcurrentTokenRefreshSingleLogin(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}, 16), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"access_token":"shared-token","expires_in":3600}`))
	}))
	defer server.Close()
	root := newTokenTestService(server.URL)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			token, err := root.WithContext(context.Background()).GetAccessToken()
			if err != nil || token != "shared-token" {
				t.Errorf("token=%q err=%v", token, err)
			}
		}()
	}
	close(start)
	<-entered
	// Hold the first login open while the other callers reach the shared refresh.
	select {
	case <-entered:
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	workers.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("16 concurrent callers made %d login requests, want 1", got)
	}
}

func TestMoviePilotUnauthorizedRefreshIsBounded(t *testing.T) {
	for _, recoverAfterRefresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "still-unauthorized", true: "recovered"}[recoverAfterRefresh], func(t *testing.T) {
			var reads, logins atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/login/access-token" {
					if logins.Add(1) > 5 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"access_token":"refreshed","expires_in":3600}`))
					return
				}
				reads.Add(1)
				if recoverAfterRefresh && r.Header.Get("Authorization") == "Bearer refreshed" {
					_, _ = w.Write([]byte(`{"success":true}`))
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			root := newTokenTestService(server.URL)
			root.accessToken, root.tokenExpiresAt = "rejected", time.Now().Add(time.Hour)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := root.WithContext(ctx).doGet("/protected", nil)
			if recoverAfterRefresh && err != nil {
				t.Fatal(err)
			}
			if !recoverAfterRefresh && (err == nil || !strings.Contains(err.Error(), "401")) {
				t.Fatalf("persistent authentication error lost: %v", err)
			}
			if reads.Load() != 2 || logins.Load() != 1 {
				t.Fatalf("unbounded authentication retry: reads=%d logins=%d", reads.Load(), logins.Load())
			}
		})
	}
}

func TestMoviePilotTokenWaiterCancellationDoesNotCancelLeader(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		_, _ = w.Write([]byte(`{"access_token":"ready","expires_in":3600}`))
	}))
	defer server.Close()
	root := newTokenTestService(server.URL)
	leaderDone := make(chan error, 1)
	go func() { _, err := root.GetAccessToken(); leaderDone <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := root.WithContext(ctx).GetAccessToken()
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter ignored cancellation: %v", err)
	}
	if err := <-leaderDone; err != nil || calls.Load() != 1 {
		t.Fatalf("waiter affected shared login: calls=%d err=%v", calls.Load(), err)
	}
}

func TestMoviePilotCancelledLeaderDoesNotFailLiveWaiter(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"ready","expires_in":3600}`))
	}))
	defer server.Close()
	root := newTokenTestService(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	leaderDone := make(chan error, 1)
	go func() { _, err := root.WithContext(ctx).GetAccessToken(); leaderDone <- err }()
	<-entered
	waiterCtx, cancelWaiter := context.WithTimeout(context.Background(), time.Second)
	defer cancelWaiter()
	token, err := root.WithContext(waiterCtx).GetAccessToken()
	if err != nil || token != "ready" || calls.Load() != 2 {
		t.Fatalf("leader cancellation affected live waiter: token=%q calls=%d err=%v", token, calls.Load(), err)
	}
	if err := <-leaderDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("leader deadline not propagated: %v", err)
	}
}
