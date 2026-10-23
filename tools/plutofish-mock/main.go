// Local-only notification fixture server. Never calls PlutoFish or a provider.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"film-fusion/app/plutofish"
)

func main() {
	port := flag.Int("port", 18765, "loopback HTTP port")
	flag.Parse()
	events := []plutofish.Event{
		{ID: "mock-user:1", Type: "transfer.failed", Status: "failed", SchemaVersion: 1, TransferID: "mock-transfer", OccurredAt: "2026-10-20T00:00:00Z", WorkTitle: "模拟电影", FileName: "模拟电影.2026.mkv", Provider: "115", TargetAccountID: "mock-account"},
		{ID: "mock-user:2", Type: "transfer.succeeded", Status: "succeeded", SchemaVersion: 1, TransferID: "mock-transfer", OccurredAt: "2026-10-20T00:01:00Z", WorkTitle: "模拟电影", FileName: "模拟电影.2026.mkv", Provider: "115", TargetAccountID: "mock-account"},
		{ID: "mock-user:3", Type: "transfer.unknown", Status: "unknown", SchemaVersion: 1, TransferID: "mock-unknown", OccurredAt: "2026-10-20T00:02:00Z", WorkTitle: "模拟待核对"},
	}
	// Opaque fixture tokens; no consumer should parse these values.
	pages := map[string]struct {
		items  []plutofish.Event
		cursor string
		more   bool
	}{
		"":                   {events[:1], "mock-user:1", true},
		"mock-user:1":        {nil, "mock-user:scan-gap", true},
		"mock-user:scan-gap": {events[1:], "mock-user:3", false},
		"mock-user:3":        {nil, "mock-user:3", false},
	}
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer local-mock-key" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"mock unauthorized"}`)
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		page, ok := pages[r.URL.Query().Get("after")]
		if !ok {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"error":"mock cursor"}`)
			return
		}
		items := page.items
		if items == nil {
			items = []plutofish.Event{}
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "nextCursor": page.cursor, "hasMore": page.more})
	})
	mux.HandleFunc("GET /api/v1/events/stream", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if r.Header.Get("Last-Event-ID") != "mock-user:3" {
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flush := w.(http.Flusher)
		fmt.Fprint(w, "retry: 5000\r\n\r\n: heartbeat\r\n\r\n")
		flush.Flush()
		// Replay a duplicate in deliberately fragmented UTF-8 / CRLF frames.
		body, _ := json.Marshal(events[1])
		frame := fmt.Sprintf("id: %s\r\nevent: %s\r\ndata: %s\r\n\r\n", events[1].ID, events[1].Type, body)
		for i := 0; i < len(frame); i++ {
			if _, err := w.Write([]byte{frame[i]}); err != nil {
				return
			}
			flush.Flush()
		}
		fmt.Fprint(w, "id: mock-user:3\r\nevent: cursor\r\ndata: {\"cursor\":\"mock-user:3\"}\r\n\r\n")
		flush.Flush()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flush.Flush()
			}
		}
	})
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("Mock notifications listening at http://%s; use the documented local fixture key", addr)
	log.Fatal((&http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}
