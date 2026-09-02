package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/model"
)

const testAutomationQBAPIKey = "qbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ12"

type automationRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn automationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestAutomationOffline115OpenAPIActionSubmitsWithAccessToken(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:ABCDEF123456&dn=episode"
	var requestCount atomic.Int32
	httpClient := &http.Client{Transport: automationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
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

	db := newAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open,
		StorageName: "OpenAPI 账号", AccessToken: "open-token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db, httpClient: httpClient}
	node := AutomationNode{Type: AutomationNodeOffline115OpenAPI, Config: map[string]any{
		"cloud_storage_id": storage.ID,
		"directory_id":     "9527",
		"url":              "$item.download_url",
	}}
	output, err := automation.executeAutomationOffline115OpenAPI(context.Background(), node, map[string]any{
		"item": map[string]any{"download_url": magnet},
	})
	if err != nil {
		t.Fatalf("executeAutomationOffline115OpenAPI() error = %v", err)
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

func TestAutomationRename115OpenAPIActionUsesResolvedFileIDAndName(t *testing.T) {
	var requestCount atomic.Int32
	httpClient := &http.Client{Transport: automationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount.Add(1)
		if request.URL.Host != "proapi.115.com" || request.URL.Path != "/open/ufile/update" {
			t.Fatalf("unexpected OpenAPI URL: %s", request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer rename-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := request.Form.Get("file_id"); got != "987654321" {
			t.Fatalf("file_id = %q", got)
		}
		if got := request.Form.Get("file_name"); got != "示例剧.S01E02.mkv" {
			t.Fatalf("file_name = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"state": true,
				"code": 0,
				"message": "",
				"data": {"file_name": "示例剧.S01E02.mkv", "star": "0"}
			}`)),
			Request: request,
		}, nil
	})}

	db := newAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open,
		StorageName: "重命名账号", AccessToken: "rename-token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db, httpClient: httpClient}
	node := AutomationNode{Type: AutomationNodeRename115OpenAPI, Config: map[string]any{
		"cloud_storage_id": storage.ID,
		"file_id":          "$nodes.wait.output.file_id",
		"new_name":         "{{item.title}}.mkv",
	}}
	output, err := automation.executeAutomationRename115OpenAPI(context.Background(), node, map[string]any{
		"item":  map[string]any{"title": "示例剧.S01E02"},
		"nodes": map[string]any{"wait": map[string]any{"output": map[string]any{"file_id": "987654321"}}},
	})
	if err != nil {
		t.Fatalf("executeAutomationRename115OpenAPI() error = %v", err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count = %d", requestCount.Load())
	}
	if output["renamed"] != true || output["file_id"] != "987654321" || output["file_name"] != "示例剧.S01E02.mkv" {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestAutomationRename115OpenAPIPreviewDoesNotCallAPI(t *testing.T) {
	output, err := previewAutomationActionNode(AutomationNode{
		Type: AutomationNodeRename115OpenAPI,
		Config: map[string]any{
			"cloud_storage_id": 1,
			"file_id":          "$nodes.wait.output.file_id",
			"new_name":         "{{item.title}}.mkv",
		},
	}, map[string]any{
		"item":  map[string]any{"title": "示例剧.S01E03"},
		"nodes": map[string]any{"wait": map[string]any{"output": map[string]any{"file_id": "preview-file"}}},
	})
	if err != nil {
		t.Fatalf("previewAutomationActionNode() error = %v", err)
	}
	if output["preview"] != true || output["renamed"] != true || output["file_id"] != "preview-file" || output["file_name"] != "示例剧.S01E03.mkv" {
		t.Fatalf("unexpected preview output: %#v", output)
	}
}

func TestAutomationQBittorrentActionLogsInAndSubmitsResolvedURL(t *testing.T) {
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
			if got := r.FormValue("tags"); !strings.Contains(got, "filmfusion-automation-btih:abcdef123456") {
				http.Error(w, "missing internal tracking tag: "+got, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	configJSON, _ := json.Marshal(AutomationQBittorrentConfig{BaseURL: server.URL, Username: "admin", Password: "secret"})
	target := model.AutomationTarget{Name: "本地 qB", Type: model.AutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(configJSON)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db}
	node := AutomationNode{Type: AutomationNodeQBittorrent, Config: map[string]any{
		"target_id": target.ID, "url": "$item.download_url", "category": "{{item.category}}",
	}}
	output, err := automation.executeAutomationQBittorrent(context.Background(), node, map[string]any{
		"item": map[string]any{"download_url": magnet, "category": "动画"},
	})
	if err != nil {
		t.Fatalf("executeAutomationQBittorrent() error = %v", err)
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

func TestAutomationQBittorrentActionPrefersAPIKeyWithoutLogin(t *testing.T) {
	var loginCount atomic.Int32
	var addCount atomic.Int32
	const magnet = "magnet:?xt=urn:btih:APIKEY123456&dn=episode"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginCount.Add(1)
			http.Error(w, "API Key must not use login", http.StatusBadRequest)
		case "/api/v2/torrents/add":
			addCount.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer "+testAutomationQBAPIKey {
				http.Error(w, "unexpected authorization: "+got, http.StatusUnauthorized)
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
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	configJSON, _ := json.Marshal(AutomationQBittorrentConfig{BaseURL: server.URL, APIKey: testAutomationQBAPIKey})
	target := model.AutomationTarget{Name: "API Key qB", Type: model.AutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(configJSON)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db}
	node := AutomationNode{Type: AutomationNodeQBittorrent, Config: map[string]any{
		"target_id": target.ID, "url": "$item.download_url",
	}}
	output, err := automation.executeAutomationQBittorrent(context.Background(), node, map[string]any{
		"item": map[string]any{"download_url": magnet},
	})
	if err != nil {
		t.Fatalf("executeAutomationQBittorrent() error = %v", err)
	}
	if output["submitted"] != true || loginCount.Load() != 0 || addCount.Load() != 1 {
		t.Fatalf("unexpected output or requests: output=%#v login=%d add=%d", output, loginCount.Load(), addCount.Load())
	}
}

func TestAutomationQBittorrentActionSupportsWebAPI214AndAdvancedOptions(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:MODERN123456&dn=episode"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/add" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAutomationQBAPIKey {
			http.Error(w, "unexpected authorization: "+got, http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Origin"); got != serverURLOrigin(r) {
			http.Error(w, "unexpected origin: "+got, http.StatusBadRequest)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wantFields := map[string]string{
			"urls": magnet, "rename": "示例剧.S01E01", "paused": "true",
			"sequentialDownload": "true", "skip_checking": "true",
			"firstLastPiecePrio": "true", "root_folder": "false", "autoTMM": "true",
			"dlLimit": "2097152", "upLimit": "524288", "ratioLimit": "-1", "seedingTimeLimit": "-2",
		}
		for key, want := range wantFields {
			if got := r.FormValue(key); got != want {
				http.Error(w, fmt.Sprintf("%s=%q want %q", key, got, want), http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"added_torrent_ids":["0123456789ABCDEF"],"failure_count":0,"pending_count":1,"success_count":0}`))
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	configJSON, _ := json.Marshal(AutomationQBittorrentConfig{BaseURL: server.URL, APIKey: testAutomationQBAPIKey})
	target := model.AutomationTarget{Name: "现代 qB", Type: model.AutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(configJSON)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	node := AutomationNode{Type: AutomationNodeQBittorrent, Config: map[string]any{
		"target_id": target.ID, "url": "$item.download_url", "rename": "{{item.title}}",
		"paused": true, "sequential": true, "skip_checking": true, "first_last_piece_priority": true,
		"root_folder": "false", "auto_tmm": "true", "download_limit_kib": 2048,
		"upload_limit_kib": 512, "ratio_limit": -1, "seeding_time_limit": -2,
	}}
	output, err := (&AutomationService{db: db}).executeAutomationQBittorrent(context.Background(), node, map[string]any{
		"item": map[string]any{"download_url": magnet, "title": "示例剧.S01E01"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output["submitted"] != true || output["accepted"] != true || output["pending"] != true || output["pending_count"] != 1 || output["hash"] != "0123456789ABCDEF" || output["response_format"] != "webapi_2_14" {
		t.Fatalf("unexpected modern qB output: %#v", output)
	}
}

func TestAutomationQBAddResultRejectsModernAllFailure(t *testing.T) {
	output, err := automationQBAddResult(http.StatusConflict, []byte(`{"added_torrent_ids":[],"failure_count":1,"pending_count":0,"success_count":0}`))
	if err == nil || output["submitted"] != false || output["failure_count"] != 1 {
		t.Fatalf("expected structured qB failure, output=%#v err=%v", output, err)
	}
}

func TestValidateAutomationDownloadURLSupportsQBittorrentBCLinks(t *testing.T) {
	if err := validateAutomationDownloadURL("bc://bt/QUJDREVGRw=="); err != nil {
		t.Fatalf("expected qBittorrent BC link to be valid: %v", err)
	}
	if err := validateAutomationDownloadURL("bc://other/QUJDREVGRw=="); err == nil {
		t.Fatal("expected malformed BC link to be rejected")
	}
}

func serverURLOrigin(r *http.Request) string {
	return "http://" + r.Host
}

func TestAutomationTestTargetValidatesAPIKeyAgainstVersionEndpoint(t *testing.T) {
	var versionCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/app/version" {
			http.Error(w, "unexpected endpoint", http.StatusBadRequest)
			return
		}
		versionCount.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAutomationQBAPIKey {
			http.Error(w, "missing API Key", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("v5.2.0"))
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	configJSON, _ := json.Marshal(AutomationQBittorrentConfig{BaseURL: server.URL, APIKey: testAutomationQBAPIKey})
	target := model.AutomationTarget{Name: "API Key qB", Type: model.AutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(configJSON)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&AutomationService{db: db}).TestTarget(context.Background(), target.ID); err != nil {
		t.Fatal(err)
	}
	if versionCount.Load() != 1 {
		t.Fatalf("version requests = %d", versionCount.Load())
	}
}

func TestAutomationTestTargetKeepsUsernamePasswordLogin(t *testing.T) {
	var loginCount atomic.Int32
	var versionCount atomic.Int32
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
		case "/api/v2/app/version":
			versionCount.Add(1)
			cookie, err := r.Cookie("SID")
			if err != nil || cookie.Value != "test-session" || r.Header.Get("Authorization") != "" {
				http.Error(w, "missing session", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("v4.6.7"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	configJSON, _ := json.Marshal(AutomationQBittorrentConfig{BaseURL: server.URL, Username: "admin", Password: "secret"})
	target := model.AutomationTarget{Name: "旧版 qB", Type: model.AutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(configJSON)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&AutomationService{db: db}).TestTarget(context.Background(), target.ID); err != nil {
		t.Fatal(err)
	}
	if loginCount.Load() != 1 || versionCount.Load() != 1 {
		t.Fatalf("requests: login=%d version=%d", loginCount.Load(), versionCount.Load())
	}
}

func TestValidateAutomationTargetInputPreservesMaskedSecrets(t *testing.T) {
	oldConfig, _ := json.Marshal(AutomationQBittorrentConfig{
		BaseURL: "http://qb.example.test", Username: "admin", Password: "keep-password", APIKey: testAutomationQBAPIKey,
	})
	input := AutomationTargetInput{Name: "qB", Type: model.AutomationTargetQBittorrent, Enabled: true, Config: map[string]any{
		"base_url": "http://qb.example.test/", "username": "admin", "password": "********", "api_key": "********",
	}}
	_, encoded, err := validateAutomationTargetInput(input, string(oldConfig))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, `"password":"keep-password"`) || !strings.Contains(encoded, `"api_key":"`+testAutomationQBAPIKey+`"`) {
		t.Fatalf("masked update did not preserve secrets: %s", encoded)
	}
}

func TestValidateAutomationTargetInputAcceptsAPIKeyOnlyAndCanClearIt(t *testing.T) {
	input := AutomationTargetInput{Name: "qB", Type: model.AutomationTargetQBittorrent, Enabled: true, Config: map[string]any{
		"base_url": "http://qb.example.test", "api_key": testAutomationQBAPIKey,
	}}
	_, encoded, err := validateAutomationTargetInput(input, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, `"api_key":"`+testAutomationQBAPIKey+`"`) {
		t.Fatalf("API Key was not saved: %s", encoded)
	}

	passwordConfig := AutomationTargetInput{Name: "qB", Type: model.AutomationTargetQBittorrent, Enabled: true, Config: map[string]any{
		"base_url": "http://qb.example.test", "username": "admin", "password": "secret", "api_key": "",
	}}
	_, encoded, err = validateAutomationTargetInput(passwordConfig, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, `"api_key":""`) {
		t.Fatalf("explicit empty API Key did not switch to password authentication: %s", encoded)
	}
}

func TestValidateAutomationTargetInputRejectsInvalidAPIKey(t *testing.T) {
	input := AutomationTargetInput{Name: "qB", Type: model.AutomationTargetQBittorrent, Enabled: true, Config: map[string]any{
		"base_url": "http://qb.example.test", "username": "admin", "password": "secret", "api_key": "not-a-qb-key",
	}}
	if _, _, err := validateAutomationTargetInput(input, ""); err == nil || !strings.Contains(err.Error(), "qbt_") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestRedactAutomationTargetMasksAPIKeyAndPassword(t *testing.T) {
	config, _ := json.Marshal(AutomationQBittorrentConfig{
		BaseURL: "http://qb.example.test", Username: "admin", Password: "password-secret", APIKey: testAutomationQBAPIKey,
	})
	target := model.AutomationTarget{Type: model.AutomationTargetQBittorrent, ConfigJSON: string(config)}
	redactAutomationTarget(&target)
	if strings.Contains(target.ConfigJSON, "password-secret") || strings.Contains(target.ConfigJSON, testAutomationQBAPIKey) {
		t.Fatalf("redacted target leaked a secret: %s", target.ConfigJSON)
	}
	if strings.Count(target.ConfigJSON, automationSecretMask) != 2 {
		t.Fatalf("redacted target did not mask both secrets: %s", target.ConfigJSON)
	}
}
