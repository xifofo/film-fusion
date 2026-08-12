package service

import (
	"strings"
	"testing"
)

func TestParseRSSAutomationFeedExtractsEnclosureURLAndTypedFields(t *testing.T) {
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>动画更新</title>
    <item>
      <title>[字幕组] 示例动画 1001集</title>
      <guid>episode-1001</guid>
      <link>https://example.com/1001</link>
      <enclosure url="magnet:?xt=urn:btih:ABCDEF123456&amp;dn=episode" length="123456" type="application/x-bittorrent"></enclosure>
      <category>动画</category>
      <category>1080P</category>
      <pubDate>Mon, 11 Aug 2026 10:00:00 +0800</pubDate>
    </item>
  </channel>
</rss>`

	feed, err := ParseRSSAutomationFeed(strings.NewReader(xmlBody), DefaultRSSAutomationMapping(), 0)
	if err != nil {
		t.Fatalf("ParseRSSAutomationFeed() error = %v", err)
	}
	if feed.Title != "动画更新" || len(feed.Items) != 1 {
		t.Fatalf("unexpected feed: %#v", feed)
	}
	fields := feed.Items[0].Fields
	if got := fields["download_url"]; got != "magnet:?xt=urn:btih:ABCDEF123456&dn=episode" {
		t.Fatalf("download_url = %#v", got)
	}
	if got, ok := fields["size_bytes"].(int64); !ok || got != 123456 {
		t.Fatalf("size_bytes = %#v (%T)", fields["size_bytes"], fields["size_bytes"])
	}
	if got := fields["category"]; got != "动画, 1080P" {
		t.Fatalf("category = %#v", got)
	}
	if got := fields["published_at"]; got != "2026-08-11T02:00:00Z" {
		t.Fatalf("published_at = %#v", got)
	}
	foundEnclosureURL := false
	for _, selector := range feed.Selectors {
		if selector == "enclosure@url" {
			foundEnclosureURL = true
		}
	}
	if !foundEnclosureURL {
		t.Fatalf("selectors do not expose enclosure@url: %#v", feed.Selectors)
	}
}

func TestParseRSSAutomationFeedFiltersNodesByAttributePattern(t *testing.T) {
	xmlBody := `<rss><channel><item>
  <link rel="alternate" href="https://example.com/detail"></link>
  <link rel="enclosure" href="https://example.com/file.torrent"></link>
</item></channel></rss>`
	mapping := RSSAutomationMapping{
		ItemSelector: "channel/item",
		Fields: []RSSAutomationFieldMapping{{
			Name: "download_url", Selector: "link@href", Type: "string",
			MatchAttribute: "rel", MatchPattern: `^enclosure$`, Required: true,
		}},
	}
	feed, err := ParseRSSAutomationFeed(strings.NewReader(xmlBody), mapping, 0)
	if err != nil {
		t.Fatalf("ParseRSSAutomationFeed() error = %v", err)
	}
	if got := feed.Items[0].Fields["download_url"]; got != "https://example.com/file.torrent" {
		t.Fatalf("download_url = %#v", got)
	}
}
