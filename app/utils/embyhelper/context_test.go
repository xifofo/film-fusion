package embyhelper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"film-fusion/app/config"
)

func TestEmbyReadsHonorCallerCancellation(t *testing.T) {
	for _, kind := range []string{"items", "playback", "scoped-playback", "library", "user-token", "session"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			client := New(&config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "test"}})
			defer client.client.Close()
			var err error
			switch kind {
			case "items":
				_, err = GetEmbyItemsContext(ctx, server.URL+"/Items", "1", "", "", "")
			case "playback":
				_, err = client.GetPlaybackInfoContext(ctx, "1")
			case "scoped-playback":
				_, err = client.WithContext(ctx).GetPlaybackInfo("1")
			case "library":
				_, err = client.WithContext(ctx).FindItemByTmdbID("1", "movie")
			case "user-token":
				_, err = GetUserByTokenContext(ctx, &config.Config{Emby: config.EmbyConfig{URL: server.URL}}, "user-token")
			case "session":
				_, err = client.WithContext(ctx).GetUserBySession("1", "127.0.0.1")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation not propagated: %v", err)
			}
			if client.requestContext().Err() != nil {
				t.Fatal("scoped request cancelled shared client")
			}
		})
	}
}
