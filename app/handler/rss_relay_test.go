package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRSSRelayHTTPRequiresTokenAndServesRawXML(t *testing.T) {
	const xml = `<rss><channel><item><guid>one</guid><enclosure url="https://example.com/a?x=1&amp;y=2"/></item></channel></rss>`
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Query().Has("cold") {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, xml)
	}))
	defer upstream.Close()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/relay.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSRelay{}, &model.RSSFeedCache{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous; sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	s := service.NewRSSService(nil)
	settings, err := s.CreateRSSRelay(service.RSSRelayInput{Name: "RSS", FeedURL: upstream.URL, Enabled: true, MinIntervalSeconds: 180})
	if err != nil {
		t.Fatal(err)
	}
	h := NewRSSRelayHandler(s)
	router := gin.New()
	router.GET("/api/rss-relay/:id/:token", h.ReadRSSRelay)
	request := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}
	if response := request(fmt.Sprintf("/api/rss-relay/%d/%s", settings.ID, strings.Repeat("0", 64))); response.Code != http.StatusNotFound || upstreamCalls.Load() != 0 {
		t.Fatal("invalid token reached upstream")
	}
	for _, state := range []string{"MISS", "HIT"} {
		response := request(settings.RelayPath)
		if response.Code != http.StatusOK || response.Body.String() != xml || response.Header().Get("Content-Type") != "application/rss+xml" || response.Header().Get("X-RSS-Cache") != state {
			t.Fatalf("relay response = %d, %v, %s", response.Code, response.Header(), response.Body.String())
		}
	}
	if upstreamCalls.Load() != 1 {
		t.Fatal("HTTP readers bypassed cooldown")
	}
	if err := s.DeleteRSSRelay(settings.ID); err != nil {
		t.Fatal(err)
	}
	if response := request(settings.RelayPath); response.Code != http.StatusNotFound {
		t.Fatal("deleted relay retained a public relay")
	}
	coldSettings, err := s.CreateRSSRelay(service.RSSRelayInput{Name: "冷缓存", FeedURL: upstream.URL + "?cold=1", Enabled: true, MinIntervalSeconds: 180})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		response := request(coldSettings.RelayPath)
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" || strings.Contains(response.Body.String(), "<rss") {
			t.Fatalf("cold failure must not return an empty feed: %d %v", response.Code, response.Header())
		}
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("failed HTTP relay bypassed cooldown: %d requests", upstreamCalls.Load())
	}
}
