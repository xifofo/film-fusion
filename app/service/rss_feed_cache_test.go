package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const relayTestXML = `<?xml version="1.0"?><rss><channel><title>RSS</title><item><guid>one</guid><title>第一集</title><enclosure url="https://example.com/download?x=1&amp;y=2"/></item></channel></rss>`

func newRSSCacheTestService(t *testing.T) (*RSSService, *time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "rss.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSFeedCache{}, &model.RSSRelay{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Now()
	return &RSSService{db: db, clock: func() time.Time { return now }}, &now
}

func createRSSRelaySource(t *testing.T, s *AutomationService, feedURL string) model.AutomationSource {
	t.Helper()
	created, err := s.CreateAutomation(AutomationCreateInput{
		Source: AutomationSourceInput{Name: "RSS", TriggerType: model.AutomationTriggerRSS, Enabled: true, FeedURL: feedURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Source
}

func relayToken(path string) string { return path[strings.LastIndex(path, "/")+1:] }

func TestRSSCacheConcurrentConsumersMakeOneUpstreamRequest(t *testing.T) {
	s, _ := newRSSCacheTestService(t)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		_, _ = fmt.Fprint(w, relayTestXML)
	}))
	defer server.Close()
	// Two service instances coordinate through the same persistent reservation.
	other := &RSSService{db: s.db, clock: s.clock}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			consumer := s
			if i%2 == 1 {
				consumer = other
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			feed, err := consumer.readRSSFeed(ctx, server.URL, true)
			if err == nil && string(feed.Body) != relayTestXML {
				err = errors.New("cached XML changed")
			}
			results <- err
		}(i)
	}
	<-entered
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestRSSCacheSharedByPreviewScanAndRelayWithoutLosingNewItems(t *testing.T) {
	s, now := newRSSCacheTestService(t)
	var calls atomic.Int32
	var payload atomic.Value
	payload.Store(relayTestXML)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, payload.Load().(string))
	}))
	defer server.Close()
	automation := rssAutomationConsumer(t, s)
	first := createRSSRelaySource(t, automation, server.URL)
	second := createRSSRelaySource(t, automation, server.URL)
	settings, err := s.CreateRSSRelay(RSSRelayInput{Name: "独立 RSS", FeedURL: server.URL, Enabled: true, MinIntervalSeconds: 180})
	if err != nil {
		t.Fatal(err)
	}
	input := AutomationSourceInput{Name: "预览", FeedURL: server.URL}
	if _, err := automation.SampleRSSSource(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	for _, source := range []model.AutomationSource{first, second} {
		result, err := automation.Scan(context.Background(), source.ID)
		if err != nil || !result.Baseline || result.CreatedEntries != 1 || !result.CacheHit {
			t.Fatalf("baseline = %#v, %v", result, err)
		}
	}
	if _, err := s.ReadRSSRelay(context.Background(), settings.ID, relayToken(settings.RelayPath)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("preview, two baselines and relay made %d requests", calls.Load())
	}
	*now = now.Add(181 * time.Second)
	payload.Store(strings.Replace(relayTestXML, "</channel>", "<item><guid>two</guid><title>第二集</title></item></channel>", 1))
	// MP2 fetches first. Each automation must still parse this new shared snapshot.
	if _, err := s.ReadRSSRelay(context.Background(), settings.ID, relayToken(settings.RelayPath)); err != nil {
		t.Fatal(err)
	}
	for _, source := range []model.AutomationSource{first, second} {
		result, err := automation.Scan(context.Background(), source.ID)
		if err != nil || result.CreatedEntries != 1 || result.CreatedRuns != 1 {
			t.Fatalf("increment = %#v, %v", result, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}

func TestRSSCacheFailureCooldownStaleAndDeferredConditionalRetry(t *testing.T) {
	s, now := newRSSCacheTestService(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("ETag", `"version1"`)
			_, _ = fmt.Fprint(w, relayTestXML)
		case 2:
			if r.Header.Get("If-None-Match") != `"version1"` {
				t.Error("missing conditional validator")
			}
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			if r.Header.Get("If-None-Match") != "" {
				t.Error("deferred fallback must omit rejected validator")
			}
			_, _ = fmt.Fprint(w, relayTestXML)
		}
	}))
	defer server.Close()
	if _, err := s.readRSSFeed(context.Background(), server.URL, true); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(181 * time.Second)
	feed, err := s.readRSSFeed(context.Background(), server.URL, true)
	if err != nil || !feed.CacheStale || !strings.Contains(feed.CacheWarning, "503") || string(feed.Body) != relayTestXML {
		t.Fatalf("stale feed = %#v, %v", feed.RSSCacheMetadata, err)
	}
	*now = now.Add(599 * time.Second)
	if _, err := s.readRSSFeed(context.Background(), server.URL, true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("retried during Retry-After: %d calls", calls.Load())
	}
	*now = now.Add(time.Second)
	feed, err = s.readRSSFeed(context.Background(), server.URL, true)
	if err != nil || feed.CacheStale || calls.Load() != 3 {
		t.Fatalf("recovery = %#v, calls %d, %v", feed.RSSCacheMetadata, calls.Load(), err)
	}
}

func TestRSSCachePersistsCooldownAndBodyAcrossDatabaseReopen(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "600")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = fmt.Fprint(w, relayTestXML)
				}
			}))
			defer server.Close()
			dbPath := filepath.Join(t.TempDir(), "rss.db")
			openDB := func() *gorm.DB {
				db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				return db
			}
			db := openDB()
			if err := db.AutoMigrate(&model.RSSFeedCache{}); err != nil {
				t.Fatal(err)
			}
			s := &RSSService{db: db}
			_, _ = s.readRSSFeed(context.Background(), server.URL, true)
			sqlDB, _ := db.DB()
			_ = sqlDB.Close()
			db = openDB()
			sqlDB, _ = db.DB()
			defer sqlDB.Close()
			s = &RSSService{db: db}
			feed, err := s.readRSSFeed(context.Background(), server.URL, true)
			if status == http.StatusOK && (err != nil || string(feed.Body) != relayTestXML || !feed.CacheHit) {
				t.Fatalf("reopened cached feed: %v", err)
			}
			if status == http.StatusTooManyRequests {
				var unavailable *RSSCacheUnavailableError
				if !errors.As(err, &unavailable) || unavailable.NextFetchAt == nil || time.Until(*unavailable.NextFetchAt) < 590*time.Second {
					t.Fatalf("lost failure cooldown: %v", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("restart made %d requests", calls.Load())
			}
		})
	}
}

func TestRSSRelayTokenRotationDisableAndFeedChange(t *testing.T) {
	s, _ := newRSSCacheTestService(t)
	input := RSSRelayInput{Name: "RSS", FeedURL: "https://example.com/rss?passkey=private", Enabled: true, MinIntervalSeconds: 180}
	settings, err := s.CreateRSSRelay(input)
	if err != nil {
		t.Fatal(err)
	}
	old := relayToken(settings.RelayPath)
	if strings.Contains(settings.RelayPath, "private") {
		t.Fatal("relay URL exposed upstream secret")
	}
	settings, err = s.RotateRSSRelayToken(settings.ID)
	if err != nil || relayToken(settings.RelayPath) == old {
		t.Fatalf("rotation failed: %v", err)
	}
	if _, err := s.ReadRSSRelay(context.Background(), settings.ID, old); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("old token remains valid")
	}
	current := relayToken(settings.RelayPath)
	if _, err := s.SetRSSRelayEnabled(settings.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadRSSRelay(context.Background(), settings.ID, current); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("disabled relay remains valid")
	}
	settings, err = s.SetRSSRelayEnabled(settings.ID, true)
	if err != nil || relayToken(settings.RelayPath) != current {
		t.Fatal("resume must retain the configured link")
	}
	input.FeedURL, input.MinIntervalSeconds = "https://example.com/new", 300
	updated, err := s.UpdateRSSRelay(settings.ID, input)
	if err != nil || updated.MinIntervalSeconds != 300 {
		t.Fatalf("update failed: %#v, %v", updated, err)
	}
	if _, err := s.ReadRSSRelay(context.Background(), settings.ID, current); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("changed feed reused old token")
	}
	items, err := s.ListRSSRelays()
	if err != nil || len(items) != 1 || items[0].RelayPath != updated.RelayPath {
		t.Fatalf("list: %#v, %v", items, err)
	}
}

func TestRSSCacheInvalidResponseAnd304PreserveOriginalXML(t *testing.T) {
	s, now := newRSSCacheTestService(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("ETag", `"one"`)
			_, _ = fmt.Fprint(w, relayTestXML)
		case 2:
			w.WriteHeader(http.StatusNotModified)
		default:
			_, _ = fmt.Fprint(w, "<html><body>Login required</body></html>")
		}
	}))
	defer server.Close()
	for i := 0; i < 3; i++ {
		feed, err := s.readRSSFeed(context.Background(), server.URL, true)
		if err != nil || string(feed.Body) != relayTestXML || feed.CacheStale != (i == 2) {
			t.Fatalf("read %d: %#v, %v", i, feed.RSSCacheMetadata, err)
		}
		*now = now.Add(181 * time.Second)
	}
}

func rssAutomationConsumer(t *testing.T, s *RSSService) *AutomationService {
	if err := s.db.AutoMigrate(&model.AutomationRevision{}, &model.AutomationSettings{}, &model.AutomationAlert{}, &model.AutomationTemplate{}); err != nil {
		t.Fatal(err)
	}
	t.Helper()
	// Add consumer tables only in integration tests. The relay works without them.
	if err := s.db.AutoMigrate(&model.AutomationSource{}, &model.AutomationWorkflow{}, &model.AutomationTarget{}, &model.AutomationEntry{}, &model.AutomationRun{}, &model.AutomationNodeRun{}, &model.Automation115DirectoryCursor{}, &model.Automation115SeenItem{}, &model.Automation115Candidate{}, &model.AutomationDedupRecord{}, &model.AutomationRateLimitBucket{}, &model.AutomationRateLimitClaim{}); err != nil {
		t.Fatal(err)
	}
	return &AutomationService{db: s.db, rssService: s}
}

func TestRSSRelayIndependentOfPausedOrDeletedAutomation(t *testing.T) {
	s, now := newRSSCacheTestService(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, relayTestXML)
	}))
	defer server.Close()
	automation := rssAutomationConsumer(t, s)
	source := createRSSRelaySource(t, automation, server.URL)
	relay, err := s.CreateRSSRelay(RSSRelayInput{Name: "独立中转", FeedURL: server.URL, Enabled: true, MinIntervalSeconds: 180})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadRSSRelay(context.Background(), relay.ID, relayToken(relay.RelayPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := automation.SetAutomationEnabled(source.ID, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	if _, err := s.ReadRSSRelay(context.Background(), relay.ID, relayToken(relay.RelayPath)); err != nil || calls.Load() != 2 {
		t.Fatalf("paused automation affected relay: %d, %v", calls.Load(), err)
	}
	if err := automation.DeleteAutomation(source.ID); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	s.refreshDueRSSRelays(context.Background())
	if calls.Load() != 3 {
		t.Fatalf("deleted automation affected relay scheduler: %d", calls.Load())
	}
	if _, err := s.ReadRSSRelay(context.Background(), relay.ID, relayToken(relay.RelayPath)); err != nil {
		t.Fatal(err)
	}
}

func TestRSSRelaySchedulerWithoutAutomationsHonorsPauseAndCooldown(t *testing.T) {
	s, now := newRSSCacheTestService(t)
	if s.db.Migrator().HasTable(&model.AutomationSource{}) {
		t.Fatal("test must run without automations")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, relayTestXML)
	}))
	defer server.Close()
	input := RSSRelayInput{Name: "中转", FeedURL: server.URL, Enabled: true, MinIntervalSeconds: 180}
	relay, err := s.CreateRSSRelay(input)
	if err != nil {
		t.Fatal(err)
	}
	s.refreshDueRSSRelays(context.Background())
	s.refreshDueRSSRelays(context.Background())
	*now = now.Add(179 * time.Second)
	s.refreshDueRSSRelays(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("scheduler ignored cooldown: %d", calls.Load())
	}
	*now = now.Add(time.Second)
	s.refreshDueRSSRelays(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("scheduler did not refresh: %d", calls.Load())
	}
	if _, err := s.SetRSSRelayEnabled(relay.ID, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	s.refreshDueRSSRelays(context.Background())
	if calls.Load() != 2 {
		t.Fatal("paused relay still fetched")
	}
	if _, err := s.SetRSSRelayEnabled(relay.ID, true); err != nil {
		t.Fatal(err)
	}
	s.refreshDueRSSRelays(context.Background())
	if calls.Load() != 3 {
		t.Fatal("resumed relay did not fetch")
	}
	if err := s.DeleteRSSRelay(relay.ID); err != nil {
		t.Fatal(err)
	}
	relay, err = s.CreateRSSRelay(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshRSSRelay(context.Background(), relay.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("recreating a relay bypassed the URL cooldown")
	}
}

func TestRSSRelayValidationAndSharedInterval(t *testing.T) {
	s, now := newRSSCacheTestService(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, relayTestXML) }))
	defer server.Close()
	input := RSSRelayInput{Name: "RSS", FeedURL: server.URL, Enabled: true, MinIntervalSeconds: 119}
	if _, err := s.CreateRSSRelay(input); err == nil {
		t.Fatal("accepted interval below upstream limit")
	}
	input.MinIntervalSeconds = 180
	first, err := s.CreateRSSRelay(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshRSSRelay(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	input.MinIntervalSeconds = 600
	second, err := s.CreateRSSRelay(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err = s.GetRSSRelay(first.ID)
	if err != nil || first.MinIntervalSeconds != 600 || !first.HasCache || !first.NextFetchAt.Equal(now.Add(600*time.Second)) {
		t.Fatalf("shared settings: %#v, %v", first, err)
	}
	*now = now.Add(599 * time.Second)
	if _, err := s.ReadRSSRelay(context.Background(), second.ID, relayToken(second.RelayPath)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("second relay bypassed shared interval")
	}
}

func TestRSSCacheDoesNotFollowRedirectOrMixCredentials(t *testing.T) {
	s, _ := newRSSCacheTestService(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer server.Close()
	if _, err := s.readRSSFeed(context.Background(), server.URL, true); err == nil || calls.Load() != 1 {
		t.Fatalf("redirect bypassed rate limit: %v, calls %d", err, calls.Load())
	}
	a, _ := rssURLHash("https://example.com/rss?passkey=a")
	b, _ := rssURLHash("https://example.com/rss?passkey=b")
	if a == b {
		t.Fatal("different credentials shared a cache")
	}
}

func TestRSSRetryAfterHTTPDate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	expected := now.Add(10 * time.Minute)
	if actual := rssRetryAfter(expected.Format(http.TimeFormat), now); actual == nil || !actual.Equal(expected) {
		t.Fatalf("Retry-After date = %v", actual)
	}
}

func TestRSSRelayStartsItsOwnScheduler(t *testing.T) {
	s, _ := newRSSCacheTestService(t)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.wake = make(chan struct{}, 1)
	s.Start()
	defer s.Stop()
	fetched := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, relayTestXML)
		select {
		case fetched <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	if _, err := s.CreateRSSRelay(RSSRelayInput{Name: "主动抓取", FeedURL: server.URL, Enabled: true, MinIntervalSeconds: 180}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetched:
	case <-time.After(3 * time.Second):
		t.Fatal("independent scheduler did not fetch the new relay")
	}
}
