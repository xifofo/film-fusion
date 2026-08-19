package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/model"
)

func TestListTargetStatusesReturnsLiveMetricsAndIsolatesFailures(t *testing.T) {
	var transferRequests atomic.Int32
	var torrentRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+testRSSAutomationQBAPIKey {
			http.Error(response, "missing API Key", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/api/v2/transfer/info":
			transferRequests.Add(1)
			_, _ = response.Write([]byte(`{
				"connection_status":"connected",
				"dht_nodes":386,
				"dl_info_data":681521119,
				"dl_info_speed":2097152,
				"up_info_data":10747904,
				"up_info_speed":262144
			}`))
		case "/api/v2/torrents/info":
			torrentRequests.Add(1)
			if request.URL.Query().Get("filter") != "active" {
				http.Error(response, "missing active filter", http.StatusBadRequest)
				return
			}
			_, _ = response.Write([]byte(`[{},{}]`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	db := newRSSAutomationTestDB(t)
	configJSON, _ := json.Marshal(RSSAutomationQBittorrentConfig{
		BaseURL: server.URL,
		APIKey:  testRSSAutomationQBAPIKey,
	})
	targets := []model.RSSAutomationTarget{
		{Name: "在线 qB", Type: model.RSSAutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(configJSON)},
		{Name: "损坏 qB", Type: model.RSSAutomationTargetQBittorrent, Enabled: true, ConfigJSON: "{"},
		{Name: "停用 qB", Type: model.RSSAutomationTargetQBittorrent, Enabled: false, ConfigJSON: string(configJSON)},
	}
	for index := range targets {
		if err := db.Create(&targets[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&targets[2]).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	statuses, err := (&RSSAutomationService{db: db}).ListTargetStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 3 {
		t.Fatalf("statuses = %d, want 3", len(statuses))
	}

	online := statuses[0]
	if !online.Online || online.ConnectionStatus != "connected" {
		t.Fatalf("online status = %#v", online)
	}
	if online.DownloadSpeed != 2097152 || online.UploadSpeed != 262144 || online.DHTNodes != 386 {
		t.Fatalf("transfer metrics = %#v", online)
	}
	if online.DownloadedSession != 681521119 || online.UploadedSession != 10747904 {
		t.Fatalf("session metrics = %#v", online)
	}
	if online.ActiveTorrents == nil || *online.ActiveTorrents != 2 || online.CheckedAt.IsZero() {
		t.Fatalf("activity metrics = %#v", online)
	}
	if statuses[1].Online || !strings.Contains(statuses[1].Error, "配置损坏") {
		t.Fatalf("damaged target status = %#v", statuses[1])
	}
	if statuses[2].Online || statuses[2].Error != "" || statuses[2].Enabled {
		t.Fatalf("disabled target status = %#v", statuses[2])
	}
	if transferRequests.Load() != 1 || torrentRequests.Load() != 1 {
		t.Fatalf("requests: transfer=%d torrents=%d", transferRequests.Load(), torrentRequests.Load())
	}
}
