package service

import (
	"context"
	"encoding/json"
	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func waitPlutoFish(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for PlutoFish state")
}

func TestPlutoFishHTTPToLocalSTRMAndRestartReplay(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db := openPlutoFishTestDB(t, dbPath)
	storage := model.CloudStorage{ID: 1, UserID: 1, StorageType: model.StorageType115Open, Status: model.StatusActive}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	mapping := model.CloudPath{ID: 1, UserID: 1, CloudStorageID: 1, SourcePath: "/Movies", SourceType: model.SourceTypeMoviePilot2, LocalPath: t.TempDir(), ContentPrefix: "/existing-media-mount", LinkType: model.LinkTypeStrm}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	event := pfTestEvent("success-event", "successful-task", "succeeded")
	event.Provider = "115"
	event.TargetAccountID = "mapped-account"
	event.TargetDirectoryPath = "/Movies/Example"
	failed := event
	failed.ID = "failure-event"
	failed.Type = "transfer.failed"
	failed.Status = "failed"
	future := event
	future.ID = "future-event"
	future.SchemaVersion = 2
	var resumes atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "commit-token" {
			resumes.Add(1)
		}
		// Deliberately replay even after restart to test durable event/transfer dedup.
		json.NewEncoder(w).Encode(map[string]any{"items": []plutofish.Event{failed, future, event}, "nextCursor": "commit-token", "hasMore": false})
	})
	mux.HandleFunc("/api/v1/events/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": heartbeat\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	remote := httptest.NewServer(mux)
	defer remote.Close()
	cfg := PlutoFishConfig{Enabled: true, BaseURL: remote.URL, OwnerUID: "fixture-owner", APIKey: "local-fake", Mode: "sse", PollSeconds: 15, Mappings: []PlutoFishAccountMapping{{TargetAccountID: "mapped-account", CloudPathID: 1}}}
	svc := NewPlutoFishService(db)
	defer svc.Stop()
	if err := svc.Save(ctx, cfg, 1); err != nil {
		t.Fatal(err)
	}
	var completed model.PlutoFishEvent
	waitPlutoFish(t, func() bool {
		return db.Where("event_id = ? AND state = ?", event.ID, "completed").Take(&completed).Error == nil
	})
	content, err := os.ReadFile(completed.OutputPath)
	if err != nil || string(content) != "/existing-media-mount/Movies/Example/电影.mkv" {
		t.Fatalf("bad mapped output %q %v", content, err)
	}
	var futureRow model.PlutoFishEvent
	db.Where("event_id = ?", future.ID).Take(&futureRow)
	if futureRow.State != "review" {
		t.Fatal("unknown schema silently recategorized", futureRow.State)
	}
	before, err := os.Stat(completed.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	svc.Stop()
	pool, _ := db.DB()
	pool.Close()
	reopened := openPlutoFishTestDB(t, dbPath)
	restarted := NewPlutoFishService(reopened)
	defer restarted.Stop()
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	waitPlutoFish(t, func() bool { return resumes.Load() > 0 })
	restarted.Stop()
	var count int64
	reopened.Model(&model.PlutoFishEvent{}).Count(&count)
	if count != 3 {
		t.Fatal("duplicate events inserted", count)
	}
	reopened.Model(&model.PlutoFishReceipt{}).Count(&count)
	if count != 1 {
		t.Fatal("lost or duplicate success receipt", count)
	}
	after, err := os.Stat(completed.OutputPath)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("restart rewrote completed STRM", err)
	}
}

func TestPlutoFishLifecycleSingleStreamStopRestartAndAuthBlock(t *testing.T) {
	var active, maxActive, requests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer mock-only" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"items":[],"nextCursor":"opaque-initial","hasMore":false}`)
	})
	mux.HandleFunc("/api/v1/events/stream", func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		if r.Header.Get("Last-Event-ID") != "opaque-initial" {
			t.Error("stream did not resume committed cursor")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": heartbeat\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	db := openPlutoFishTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	svc := NewPlutoFishService(db)
	defer svc.Stop()
	cfg := PlutoFishConfig{Enabled: true, BaseURL: server.URL, OwnerUID: "fixture-owner", APIKey: "mock-only", Mode: "sse", PollSeconds: 15}
	if err := svc.Save(context.Background(), cfg, 1); err != nil {
		t.Fatal(err)
	}
	waitPlutoFish(t, func() bool { return active.Load() == 1 })
	for range 5 {
		if err := svc.Start(); err != nil {
			t.Fatal(err)
		}
	}
	if maxActive.Load() != 1 {
		t.Fatal("multiple connections")
	}
	svc.Stop()
	waitPlutoFish(t, func() bool { return active.Load() == 0 })
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitPlutoFish(t, func() bool { return active.Load() == 1 })
	cfg.APIKey = "expired-mock"
	if err := svc.Save(context.Background(), cfg, 1); err != nil {
		t.Fatal(err)
	}
	waitPlutoFish(t, func() bool {
		status, err := svc.Status(context.Background())
		return err == nil && status.State == "blocked"
	})
	before := requests.Load()
	time.Sleep(30 * time.Millisecond)
	if requests.Load() != before {
		t.Fatal("automatic auth retries")
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitPlutoFish(t, func() bool { return requests.Load() > before })
	svc.Stop()
}
