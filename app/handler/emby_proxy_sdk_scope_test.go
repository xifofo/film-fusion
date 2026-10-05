package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/model"
	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/patrickmn/go-cache"
	"resty.dev/v3"
)

type scoped115Transport func(*http.Request) (*http.Response, error)

func (f scoped115Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConcurrent115ProxyCallsKeepAccountCredentialsAndCacheKeys(t *testing.T) {
	var calls atomic.Int32
	transport := scoped115Transport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		account := r.Form.Get("pick_code")
		if r.Header.Get("Authorization") != "Bearer "+account {
			return nil, fmt.Errorf("request for %s used another account's credentials", account)
		}
		body := fmt.Sprintf(`{"state":true,"data":{"item":{"url":{"url":"https://example.invalid/%s"}}}}`, account)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	httpClient := resty.NewWithClient(&http.Client{Transport: transport})
	defer httpClient.Close()
	base := sdk115.New(sdk115.WithRestyClient(httpClient), sdk115.WithAccessToken("base"))
	h := &EmbyProxyHandler{sdk115Open: base, goCache: cache.New(time.Minute, 0), config: &config.Config{}}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 1; i <= 16; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			account := fmt.Sprintf("account-%d", i)
			storage := model.CloudStorage{ID: uint(i), AccessToken: account, Match302AccessMode: model.Match302AccessModeOpenAPIOnly}
			for _, cached := range []bool{false, true} {
				got, fromCache, err := h.getDownloadURLForStorage(storage, account, "test-agent")
				if err != nil || got != "https://example.invalid/"+account || fromCache != cached {
					t.Errorf("account=%s url=%s cached=%v err=%v", account, got, fromCache, err)
				}
			}
		}(i)
	}
	close(start)
	workers.Wait()
	if calls.Load() != 16 {
		t.Fatalf("account-scoped cache changed: %d upstream requests", calls.Load())
	}
	if _, err := base.DownURL(context.Background(), "base", "test-agent"); err != nil {
		t.Fatalf("shared SDK credentials were mutated: %v", err)
	}
}
