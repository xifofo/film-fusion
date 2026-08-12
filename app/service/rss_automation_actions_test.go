package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/model"
)

func TestRSSAutomationOffline115OpenAPIActionSubmitsWithAccessToken(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:ABCDEF123456&dn=episode"
	var requestCount atomic.Int32
	httpClient := &http.Client{Transport: rssRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount.Add(1)
		if request.URL.Host != "proapi.115.com" || request.URL.Path != "/open/offline/add_task_urls" {
			t.Fatalf("unexpected OpenAPI URL: %s", request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer open-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := request.Form.Get("urls"); got != magnet {
			t.Fatalf("urls = %q", got)
		}
		if got := request.Form.Get("wp_path_id"); got != "9527" {
			t.Fatalf("wp_path_id = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"state": true,
				"code": 0,
				"message": "",
				"data": [{
					"state": true,
					"code": 0,
					"message": "",
					"info_hash": "ABCDEF123456",
					"url": "magnet:?xt=urn:btih:ABCDEF123456&dn=episode"
				}]
			}`)),
			Request: request,
		}, nil
	})}

	db := newRSSAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open,
		StorageName: "OpenAPI 账号", AccessToken: "open-token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{db: db, httpClient: httpClient}
	node := RSSAutomationNode{Type: RSSAutomationNodeOffline115OpenAPI, Config: map[string]any{
		"cloud_storage_id": storage.ID,
		"directory_id":     "9527",
		"url":              "$item.download_url",
	}}
	output, err := automation.executeRSSAutomationOffline115OpenAPI(context.Background(), node, map[string]any{
		"item": map[string]any{"download_url": magnet},
	})
	if err != nil {
		t.Fatalf("executeRSSAutomationOffline115OpenAPI() error = %v", err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count = %d", requestCount.Load())
	}
	if output["access_method"] != "openapi" || output["submitted"] != true {
		t.Fatalf("unexpected output: %#v", output)
	}
	hashes, ok := output["hashes"].([]string)
	if !ok || len(hashes) != 1 || hashes[0] != "ABCDEF123456" {
		t.Fatalf("hashes = %#v", output["hashes"])
	}
}

func TestRSSAutomationQBittorrentActionLogsInAndSubmitsResolvedURL(t *testing.T) {
	var loginCount atomic.Int32
	var addCount atomic.Int32
	const magnet = "magnet:?xt=urn:btih:ABCDEF123456&dn=episode"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginCount.Add(1)
			if err := r.ParseForm(); err != nil || r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
				http.Error(w, "bad credentials", http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-session", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			addCount.Add(1)
			cookie, err := r.Cookie("SID")
			if err != nil || cookie.Value != "test-session" {
				http.Error(w, "missing session", http.StatusUnauthorized)
				return
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if got := r.FormValue("urls"); got != magnet {
				http.Error(w, "unexpected URL: "+got, http.StatusBadRequest)
				return
			}
			if got := r.FormValue("category"); got != "动画" {
				http.Error(w, "unexpected category: "+got, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newRSSAutomationTestDB(t)
	configJSON, _ := json.Marshal(RSSAutomationQBittorrentConfig{BaseURL: server.URL, Username: "admin", Password: "secret"})
	target := model.RSSAutomationTarget{Name: "本地 qB", Type: model.RSSAutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(configJSON)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{db: db}
	node := RSSAutomationNode{Type: RSSAutomationNodeQBittorrent, Config: map[string]any{
		"target_id": target.ID, "url": "$item.download_url", "category": "{{item.category}}",
	}}
	output, err := automation.executeRSSAutomationQBittorrent(context.Background(), node, map[string]any{
		"item": map[string]any{"download_url": magnet, "category": "动画"},
	})
	if err != nil {
		t.Fatalf("executeRSSAutomationQBittorrent() error = %v", err)
	}
	if submitted, _ := output["submitted"].(bool); !submitted {
		t.Fatalf("unexpected output: %#v", output)
	}
	if got := output["content_key"]; got != "btih:abcdef123456" {
		t.Fatalf("content_key = %#v", got)
	}
	if loginCount.Load() != 1 || addCount.Load() != 1 {
		t.Fatalf("requests: login=%d add=%d", loginCount.Load(), addCount.Load())
	}
}

func TestValidateRSSAutomationTargetInputPreservesMaskedPassword(t *testing.T) {
	oldConfig, _ := json.Marshal(RSSAutomationQBittorrentConfig{
		BaseURL: "http://qb.example.test", Username: "admin", Password: "keep-me",
	})
	input := RSSAutomationTargetInput{Name: "qB", Type: model.RSSAutomationTargetQBittorrent, Enabled: true, Config: map[string]any{
		"base_url": "http://qb.example.test/", "username": "admin", "password": "********",
	}}
	_, encoded, err := validateRSSAutomationTargetInput(input, string(oldConfig))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, `"password":"keep-me"`) {
		t.Fatalf("masked update did not preserve password: %s", encoded)
	}
}
