package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestParseRSSFeed(t *testing.T) {
	feed, err := ParseRSSFeed(strings.NewReader(`<?xml version="1.0"?>
<rss version="2.0"><channel><title>Test Torrents</title><item>
<title>Example S01E01 1080p</title><link>https://example.com/details?id=1</link>
<category>剧集</category><guid>guid-1</guid>
<enclosure url="https://example.com/download?id=1" length="1073741824" type="application/x-bittorrent" />
<pubDate>Wed, 22 Jul 2026 09:26:58 +0800</pubDate>
</item></channel></rss>`))
	if err != nil {
		t.Fatalf("ParseRSSFeed error: %v", err)
	}
	if feed.Title != "Test Torrents" || len(feed.Items) != 1 {
		t.Fatalf("unexpected feed: %+v", feed)
	}
	item := feed.Items[0]
	if item.GUID != "guid-1" || item.Category != "剧集" || item.SizeBytes != 1073741824 {
		t.Fatalf("unexpected item: %+v", item)
	}
	if item.PublishedAt == nil || item.PublishedAt.Format(time.RFC3339) != "2026-07-22T09:26:58+08:00" {
		t.Fatalf("unexpected published_at: %v", item.PublishedAt)
	}
}

func TestDefaultRSSRuleMatchesFirstEpisodeOnly(t *testing.T) {
	rule := model.RSSNotificationRule{
		TitlePattern:    `(?i)(^|[^A-Z0-9])S[0-9]{1,2}E0*1([^0-9]|$)`,
		CategoryPattern: `剧集|电视剧|TV`,
	}
	tests := []struct {
		title    string
		category string
		want     bool
	}{
		{"New Show S01E01 2160p", "剧集", true},
		{"New Show s1e1 1080p", "TV", true},
		{"New Show S01E10 1080p", "剧集", false},
		{"New Show S01E01 1080p", "电影", false},
	}
	for _, test := range tests {
		got, err := MatchRSSRule(rule, RSSFeedItem{Title: test.title, Category: test.category})
		if err != nil || got != test.want {
			t.Fatalf("title=%q category=%q got=%v err=%v want=%v", test.title, test.category, got, err, test.want)
		}
	}
}

func TestStripRSSTrailingMetadataSupportsNestedBrackets(t *testing.T) {
	title := "Show.Name.S07E04 [WEB-DL 2160p [第七季 第04集]]"
	if got := stripRSSTrailingMetadata(title); got != "Show.Name.S07E04" {
		t.Fatalf("unexpected stripped title: %q", got)
	}
}

type recordingRSSSender struct {
	mu     sync.Mutex
	events []NotificationEvent
	report *NotificationReport
}

type rssRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn rssRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (s *recordingRSSSender) Publish(_ context.Context, event NotificationEvent) NotificationReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	if s.report != nil {
		return *s.report
	}
	return NotificationReport{
		Event:      event.Type,
		Deliveries: []NotificationDelivery{{Channel: config.NotificationChannelTelegram, Success: true}},
	}
}

func (s *recordingRSSSender) Ready(NotificationEventType) bool { return true }

func (s *recordingRSSSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func (s *recordingRSSSender) lastEvent() NotificationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return NotificationEvent{}
	}
	return s.events[len(s.events)-1]
}

func TestRSSRefreshBuildsBaselineThenNotifiesNewMatch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rss-monitor-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSMonitorSetting{}, &model.RSSNotificationRule{}, &model.RSSMonitorItem{}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	items := []string{"baseline-guid"}
	transport := rssRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		var body strings.Builder
		_, _ = fmt.Fprint(&body, `<?xml version="1.0"?><rss version="2.0"><channel><title>Test RSS</title>`)
		for _, guid := range items {
			_, _ = fmt.Fprintf(&body, `<item><title>Fresh Show S01E01 1080p</title><link>https://example.com/%s</link><category>剧集</category><guid>%s</guid><pubDate>Wed, 22 Jul 2026 09:26:58 +0800</pubDate></item>`, guid, guid)
		}
		_, _ = fmt.Fprint(&body, `</channel></rss>`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml"}},
			Body:       io.NopCloser(strings.NewReader(body.String())),
		}, nil
	})

	sender := &recordingRSSSender{}
	monitor := NewRSSMonitorService(&config.Config{}, nil, sender, nil)
	monitor.db = db
	monitor.client = &http.Client{Transport: transport}
	if err := monitor.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	defaultRules, err := monitor.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultRules) != 1 || defaultRules[0].UseMP2Recognition == nil || !*defaultRules[0].UseMP2Recognition {
		t.Fatalf("default rule should enable MP2 recognition: %+v", defaultRules)
	}
	if _, err := monitor.UpdateSettings(RSSSettingsInput{Enabled: true, FeedName: "Test", FeedURL: "https://rss.example.com/feed", IntervalMinutes: 2}); err != nil {
		t.Fatal(err)
	}

	first, err := monitor.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Baseline || first.NewItems != 1 || first.Notified != 0 || sender.count() != 0 {
		t.Fatalf("unexpected baseline result=%+v messages=%d", first, sender.count())
	}

	mu.Lock()
	items = append(items, "new-guid")
	mu.Unlock()
	second, err := monitor.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Baseline || second.NewItems != 1 || second.Matched != 1 || second.Notified != 1 || sender.count() != 1 {
		t.Fatalf("unexpected second result=%+v messages=%d", second, sender.count())
	}

	third, err := monitor.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third.NewItems != 0 || sender.count() != 1 {
		t.Fatalf("dedup failed result=%+v messages=%d", third, sender.count())
	}

	var notified model.RSSMonitorItem
	if err := db.Where("guid = ?", "new-guid").First(&notified).Error; err != nil {
		t.Fatal(err)
	}
	if notified.NotificationStatus != model.RSSNotificationSent || notified.RecognitionError == "" {
		t.Fatalf("recognition failure should fall back to a sent text notification: %+v", notified)
	}
}

func TestRSSRefreshProcessesMultipleSourcesIndependently(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rss-monitor-multi-source?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSMonitorSetting{}, &model.RSSNotificationRule{}, &model.RSSMonitorItem{}); err != nil {
		t.Fatal(err)
	}

	transport := rssRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Host, "broken") {
			return nil, errors.New("source unavailable")
		}
		name := strings.Split(request.URL.Host, ".")[0]
		body := fmt.Sprintf(`<?xml version="1.0"?><rss version="2.0"><channel><title>%s feed</title><item><title>%s item</title><guid>%s-guid</guid></item></channel></rss>`, name, name, name)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})

	monitor := NewRSSMonitorService(&config.Config{}, nil, &recordingRSSSender{}, nil)
	monitor.db = db
	monitor.client = &http.Client{Transport: transport}
	for _, input := range []RSSSettingsInput{
		{Enabled: true, FeedName: "Alpha", FeedURL: "https://alpha.example/feed", IntervalMinutes: 2},
		{Enabled: true, FeedName: "Beta", FeedURL: "https://beta.example/feed", IntervalMinutes: 5},
		{Enabled: true, FeedName: "Broken", FeedURL: "https://broken.example/feed", IntervalMinutes: 10},
	} {
		if _, err := monitor.CreateSource(input); err != nil {
			t.Fatal(err)
		}
	}

	result, err := monitor.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceResults) != 3 || result.NewItems != 2 || result.FailedSources != 1 {
		t.Fatalf("unexpected aggregate result: %+v", result)
	}
	var items []model.RSSMonitorItem
	if err := db.Order("source_id ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SourceName != "Alpha" || items[1].SourceName != "Beta" {
		t.Fatalf("items should retain their source identity: %+v", items)
	}
	var broken model.RSSMonitorSetting
	if err := db.Where("feed_name = ?", "Broken").First(&broken).Error; err != nil {
		t.Fatal(err)
	}
	if broken.LastError == "" || broken.LastCheckedAt == nil {
		t.Fatalf("broken source should record its own error: %+v", broken)
	}
}

func TestRSSDashboardSeparatesMatchedItemsAndReportsRetention(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rss-monitor-dashboard?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSMonitorSetting{}, &model.RSSNotificationRule{}, &model.RSSMonitorItem{}); err != nil {
		t.Fatal(err)
	}

	monitor := NewRSSMonitorService(&config.Config{}, nil, &recordingRSSSender{}, nil)
	monitor.db = db
	ruleID := uint(88)
	items := []model.RSSMonitorItem{
		{Fingerprint: "dashboard-1", Title: "old unmatched", NotificationStatus: model.RSSNotificationIgnored},
		{Fingerprint: "dashboard-2", Title: "old matched", RuleID: &ruleID, RuleName: "Test", NotificationStatus: model.RSSNotificationSent},
		{Fingerprint: "dashboard-3", Title: "new unmatched", NotificationStatus: model.RSSNotificationIgnored},
		{Fingerprint: "dashboard-4", Title: "new matched", RuleID: &ruleID, RuleName: "Test", NotificationStatus: model.RSSNotificationFailed},
	}
	for index := range items {
		items[index].DiscoveredAt = time.Now().Add(time.Duration(index) * time.Minute)
		if err := db.Create(&items[index]).Error; err != nil {
			t.Fatal(err)
		}
	}

	dashboard, err := monitor.Dashboard(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.RecentItems) != 2 || dashboard.RecentItems[0].Title != "new matched" || dashboard.RecentItems[1].Title != "new unmatched" {
		t.Fatalf("unexpected recent items: %+v", dashboard.RecentItems)
	}
	if len(dashboard.RecentMatchedItems) != 2 || dashboard.RecentMatchedItems[0].Title != "new matched" || dashboard.RecentMatchedItems[1].Title != "old matched" {
		t.Fatalf("unexpected matched items: %+v", dashboard.RecentMatchedItems)
	}
	if dashboard.RetentionLimit != rssRecentItemRetentionLimit {
		t.Fatalf("retention limit=%d, want %d", dashboard.RetentionLimit, rssRecentItemRetentionLimit)
	}
}

func TestPruneOldRSSItemsKeepsNewest(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rss-monitor-prune?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSMonitorItem{}); err != nil {
		t.Fatal(err)
	}

	monitor := NewRSSMonitorService(&config.Config{}, nil, &recordingRSSSender{}, nil)
	monitor.db = db
	for index := 1; index <= 5; index++ {
		item := model.RSSMonitorItem{
			Fingerprint:        fmt.Sprintf("prune-%d", index),
			Title:              fmt.Sprintf("item-%d", index),
			NotificationStatus: model.RSSNotificationIgnored,
			DiscoveredAt:       time.Now(),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}

	monitor.pruneOldItems(3)
	var remaining []model.RSSMonitorItem
	if err := db.Order("id ASC").Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 3 || remaining[0].Title != "item-3" || remaining[2].Title != "item-5" {
		t.Fatalf("unexpected remaining items: %+v", remaining)
	}
}

type retryRSSRecognizer struct {
	calls []string
}

func (r *retryRSSRecognizer) RecognizeTitle(title string) (MoviePilotMediaInfo, map[string]any, error) {
	r.calls = append(r.calls, title)
	if strings.HasSuffix(title, "[Tracker]") {
		return MoviePilotMediaInfo{}, nil, errors.New("full title not recognized")
	}
	return MoviePilotMediaInfo{
		MediaType:     "tv",
		Title:         "百花杀",
		Year:          "2026",
		Category:      "国产剧集",
		TmdbID:        "12345",
		BackdropPath:  "/backdrop.jpg",
		PosterPath:    "/poster.jpg",
		Rating:        8,
		SeasonEpisode: "S01E01",
	}, nil, nil
}

func (r *retryRSSRecognizer) SearchMedia(_ string, _ int) ([]MoviePilotSearchResult, error) {
	return nil, nil
}

type searchFallbackRSSRecognizer struct{}

func (searchFallbackRSSRecognizer) RecognizeTitle(_ string) (MoviePilotMediaInfo, map[string]any, error) {
	return MoviePilotMediaInfo{
		MediaType:    "movie",
		Title:        "Fallback Movie",
		Year:         "2026",
		TmdbID:       "9988",
		ResourceType: "WEB-DL",
		ResourcePix:  "2160p",
	}, nil, nil
}

func (searchFallbackRSSRecognizer) SearchMedia(_ string, _ int) ([]MoviePilotSearchResult, error) {
	return []MoviePilotSearchResult{
		{Title: "Wrong Movie", TmdbID: "1", BackdropPath: "/wrong.jpg"},
		{Title: "Fallback Movie", TmdbID: "9988", BackdropPath: "/matched.jpg", Rating: 7.5, Category: "华语电影"},
	}, nil
}

func TestRecognizeRSSMediaUsesSearchAndMP2QualityFallback(t *testing.T) {
	monitor := NewRSSMonitorService(&config.Config{}, nil, &recordingRSSSender{}, searchFallbackRSSRecognizer{})
	media, err := monitor.recognizeRSSMedia(RSSFeedItem{Title: "Fallback Movie (2026)", Category: "电影"})
	if err != nil {
		t.Fatal(err)
	}
	if media.PosterURL != "https://image.tmdb.org/t/p/w780/matched.jpg" || media.Rating != 7.5 || media.Category != "华语电影" || media.Quality != "WEB-DL 2160p" {
		t.Fatalf("unexpected search enrichment: %+v", media)
	}
}

func TestRSSRefreshEnrichesMoviePilotNotification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rss-enriched-notification?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSMonitorSetting{}, &model.RSSNotificationRule{}, &model.RSSMonitorItem{}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	items := []string{"baseline-guid"}
	transport := rssRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		var body strings.Builder
		_, _ = fmt.Fprint(&body, `<?xml version="1.0"?><rss version="2.0"><channel><title>Test RSS</title>`)
		for _, guid := range items {
			_, _ = fmt.Fprintf(&body, `<item><title>Show.Name.S01E01.WEB-DL.2160p [Tracker]</title><link>https://example.com/%s</link><category>剧集</category><guid>%s</guid><enclosure length="1116691496"/><pubDate>Wed, 22 Jul 2026 09:26:58 +0800</pubDate></item>`, guid, guid)
		}
		_, _ = fmt.Fprint(&body, `</channel></rss>`)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body.String()))}, nil
	})

	sender := &recordingRSSSender{}
	recognizer := &retryRSSRecognizer{}
	monitor := NewRSSMonitorService(&config.Config{}, nil, sender, recognizer)
	monitor.db = db
	monitor.client = &http.Client{Transport: transport}
	if err := monitor.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	if _, err := monitor.UpdateSettings(RSSSettingsInput{Enabled: true, FeedName: "Test", FeedURL: "https://rss.example.com/feed", IntervalMinutes: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := monitor.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	items = append(items, "new-guid")
	mu.Unlock()
	result, err := monitor.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Notified != 1 || result.Failed != 0 || sender.count() != 1 {
		t.Fatalf("unexpected notification result=%+v events=%d", result, sender.count())
	}
	if len(recognizer.calls) != 2 || recognizer.calls[1] != "Show.Name.S01E01.WEB-DL.2160p" {
		t.Fatalf("expected stripped-title retry, calls=%q", recognizer.calls)
	}

	event := sender.lastEvent()
	if event.Type != NotificationEventRSSMatched || event.ImageURL != "https://image.tmdb.org/t/p/w780/backdrop.jpg" {
		t.Fatalf("unexpected notification event: %+v", event)
	}
	for _, expected := range []string{"百花杀 (2026) S01E01 新资源上线", "评分：8.0，类型：电视剧，类别：国产剧集", "质量：WEB-DL 2160p，共1个文件"} {
		if !strings.Contains(event.Message, expected) {
			t.Fatalf("message %q does not include %q", event.Message, expected)
		}
	}

	var stored model.RSSMonitorItem
	if err := db.Where("guid = ?", "new-guid").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.MediaTitle != "百花杀" || stored.MediaYear != "2026" || stored.MediaType != "电视剧" || stored.SeasonEpisode != "S01E01" || stored.Quality != "WEB-DL 2160p" || stored.TmdbID != "12345" || stored.RecognitionError != "" {
		t.Fatalf("unexpected persisted media metadata: %+v", stored)
	}
}

func TestRSSRuleCanExplicitlyDisableMP2Recognition(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rss-rule-explicit-false?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSNotificationRule{}); err != nil {
		t.Fatal(err)
	}
	monitor := NewRSSMonitorService(&config.Config{}, nil, &recordingRSSSender{}, nil)
	monitor.db = db
	disabled := false
	created, err := monitor.CreateRule(model.RSSNotificationRule{
		Name:              "No recognition",
		Enabled:           true,
		Priority:          100,
		TitlePattern:      `E01`,
		MessageTemplate:   `{{title}}`,
		UseMP2Recognition: &disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored model.RSSNotificationRule
	if err := db.First(&stored, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.UseMP2Recognition == nil || *stored.UseMP2Recognition {
		t.Fatalf("explicit false was not persisted: %+v", stored)
	}
}
