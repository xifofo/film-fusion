package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
)

const rssGeneratorXMLNamespaceContent = "http://purl.org/rss/1.0/modules/content/"

type RSSGeneratorRenderedFeed struct {
	Body         []byte    `json:"-"`
	ContentType  string    `json:"content_type"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
	CacheStatus  string    `json:"cache_status,omitempty"`
}

func RenderRSSGeneratorFeed(feed RSSGeneratorFeed, format string, generatedAt time.Time) (RSSGeneratorRenderedFeed, error) {
	if err := validateRSSGeneratorNormalizedFeed(feed); err != nil {
		return RSSGeneratorRenderedFeed{}, err
	}
	generatedAt = generatedAt.UTC().Truncate(time.Second)
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC().Truncate(time.Second)
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "rss", "rss2", "xml":
		body, err := renderRSS2(feed, generatedAt)
		if err != nil {
			return RSSGeneratorRenderedFeed{}, err
		}
		return newRSSGeneratorRendered(body, "application/rss+xml; charset=utf-8", generatedAt), nil
	case "atom":
		body, err := renderAtom(feed, generatedAt)
		if err != nil {
			return RSSGeneratorRenderedFeed{}, err
		}
		return newRSSGeneratorRendered(body, "application/atom+xml; charset=utf-8", generatedAt), nil
	default:
		return RSSGeneratorRenderedFeed{}, errors.New("RSS 输出格式只支持 rss 或 atom")
	}
}

func newRSSGeneratorRendered(body []byte, contentType string, generatedAt time.Time) RSSGeneratorRenderedFeed {
	digest := sha256.Sum256(body)
	return RSSGeneratorRenderedFeed{
		Body: body, ContentType: contentType, ETag: `"` + hex.EncodeToString(digest[:]) + `"`, LastModified: generatedAt,
	}
}

func validateRSSGeneratorNormalizedFeed(feed RSSGeneratorFeed) error {
	if strings.TrimSpace(feed.Title) == "" {
		return errors.New("Feed title 不能为空")
	}
	if feed.Link != "" && !validRSSGeneratorHTTPURL(feed.Link) {
		return errors.New("Feed link 无效")
	}
	for index, item := range feed.Items {
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("Feed item %d title 不能为空", index+1)
		}
		if item.Link != "" && !validRSSGeneratorOutputURI(item.Link) {
			return fmt.Errorf("Feed item %d link 无效", index+1)
		}
		for _, enclosure := range item.Enclosures {
			if !validRSSGeneratorOutputURI(enclosure.URL) || enclosure.Length < 0 {
				return fmt.Errorf("Feed item %d enclosure 无效", index+1)
			}
		}
	}
	return nil
}

func validRSSGeneratorOutputURI(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if strings.ContainsAny(trimmed, "\x00\r\n\t") || len(trimmed) > 32768 {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "magnet:?") || strings.HasPrefix(lower, "ed2k://|") {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != "" && parsed.User == nil
	default:
		return false
	}
}

type rssGeneratorRSS struct {
	XMLName xml.Name            `xml:"rss"`
	Version string              `xml:"version,attr"`
	Content string              `xml:"xmlns:content,attr"`
	Channel rssGeneratorChannel `xml:"channel"`
}

type rssGeneratorChannel struct {
	Title          string             `xml:"title"`
	Link           string             `xml:"link"`
	Description    string             `xml:"description"`
	Language       string             `xml:"language,omitempty"`
	ManagingEditor string             `xml:"managingEditor,omitempty"`
	LastBuildDate  string             `xml:"lastBuildDate"`
	Image          *rssGeneratorImage `xml:"image,omitempty"`
	Items          []rssGeneratorItem `xml:"item"`
}

type rssGeneratorImage struct {
	URL   string `xml:"url"`
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type rssGeneratorItem struct {
	Title       string                  `xml:"title"`
	Link        string                  `xml:"link,omitempty"`
	Description string                  `xml:"description,omitempty"`
	Content     string                  `xml:"content:encoded,omitempty"`
	Author      string                  `xml:"author,omitempty"`
	Categories  []string                `xml:"category,omitempty"`
	GUID        *rssGeneratorGUID       `xml:"guid,omitempty"`
	PubDate     string                  `xml:"pubDate,omitempty"`
	Enclosures  []rssGeneratorEnclosure `xml:"enclosure,omitempty"`
}

type rssGeneratorGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssGeneratorEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

func renderRSS2(feed RSSGeneratorFeed, generatedAt time.Time) ([]byte, error) {
	channelLink := feed.Link
	if channelLink == "" {
		channelLink = "https://invalid.local/"
	}
	description := feed.Description
	if description == "" {
		description = feed.Title
	}
	document := rssGeneratorRSS{
		Version: "2.0", Content: rssGeneratorXMLNamespaceContent,
		Channel: rssGeneratorChannel{
			Title: feed.Title, Link: channelLink, Description: description, Language: feed.Language,
			ManagingEditor: feed.Author, LastBuildDate: generatedAt.Format(time.RFC1123Z),
			Items: make([]rssGeneratorItem, 0, len(feed.Items)),
		},
	}
	if feed.ImageURL != "" {
		document.Channel.Image = &rssGeneratorImage{URL: feed.ImageURL, Title: feed.Title, Link: channelLink}
	}
	for _, item := range feed.Items {
		guid := item.ID
		perma := "false"
		if guid == "" {
			guid = item.Link
			perma = "true"
		}
		output := rssGeneratorItem{
			Title: item.Title, Link: item.Link, Description: item.Description, Content: item.Content,
			Author: item.Author, Categories: item.Categories, Enclosures: make([]rssGeneratorEnclosure, 0, len(item.Enclosures)),
		}
		if guid != "" {
			output.GUID = &rssGeneratorGUID{IsPermaLink: perma, Value: guid}
		}
		if item.PublishedAt != nil {
			output.PubDate = item.PublishedAt.UTC().Format(time.RFC1123Z)
		}
		for _, enclosure := range item.Enclosures {
			mime := enclosure.Type
			if mime == "" {
				mime = "application/octet-stream"
			}
			output.Enclosures = append(output.Enclosures, rssGeneratorEnclosure{URL: enclosure.URL, Type: mime, Length: enclosure.Length})
		}
		document.Channel.Items = append(document.Channel.Items, output)
	}
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	encoder := xml.NewEncoder(&buffer)
	encoder.Indent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

type rssGeneratorAtom struct {
	XMLName  xml.Name                `xml:"http://www.w3.org/2005/Atom feed"`
	Title    string                  `xml:"title"`
	Subtitle string                  `xml:"subtitle,omitempty"`
	ID       string                  `xml:"id"`
	Updated  string                  `xml:"updated"`
	Author   *rssGeneratorAtomPerson `xml:"author,omitempty"`
	Links    []rssGeneratorAtomLink  `xml:"link"`
	Icon     string                  `xml:"icon,omitempty"`
	Entries  []rssGeneratorAtomEntry `xml:"entry"`
}

type rssGeneratorAtomPerson struct {
	Name string `xml:"name"`
}
type rssGeneratorAtomLink struct {
	Href   string `xml:"href,attr"`
	Rel    string `xml:"rel,attr,omitempty"`
	Type   string `xml:"type,attr,omitempty"`
	Length int64  `xml:"length,attr,omitempty"`
}
type rssGeneratorAtomText struct {
	Type string `xml:"type,attr,omitempty"`
	Body string `xml:",innerxml"`
}
type rssGeneratorAtomEntry struct {
	Title      string                     `xml:"title"`
	ID         string                     `xml:"id"`
	Updated    string                     `xml:"updated"`
	Published  string                     `xml:"published,omitempty"`
	Author     *rssGeneratorAtomPerson    `xml:"author,omitempty"`
	Links      []rssGeneratorAtomLink     `xml:"link"`
	Summary    *rssGeneratorAtomText      `xml:"summary,omitempty"`
	Content    *rssGeneratorAtomText      `xml:"content,omitempty"`
	Categories []rssGeneratorAtomCategory `xml:"category"`
}
type rssGeneratorAtomCategory struct {
	Term string `xml:"term,attr"`
}

func renderAtom(feed RSSGeneratorFeed, generatedAt time.Time) ([]byte, error) {
	feedID := feed.Link
	if feedID == "" {
		feedID = "urn:film-fusion:rss:" + stableRSSGeneratorID(feed.Title)
	}
	updated := generatedAt
	if feed.UpdatedAt != nil {
		updated = feed.UpdatedAt.UTC()
	}
	document := rssGeneratorAtom{
		Title: feed.Title, Subtitle: feed.Description, ID: feedID, Updated: updated.Format(time.RFC3339),
		Icon: feed.ImageURL, Entries: make([]rssGeneratorAtomEntry, 0, len(feed.Items)),
	}
	if feed.Author != "" {
		document.Author = &rssGeneratorAtomPerson{Name: feed.Author}
	}
	if feed.Link != "" {
		document.Links = append(document.Links, rssGeneratorAtomLink{Href: feed.Link, Rel: "alternate"})
	}
	for _, item := range feed.Items {
		id := item.ID
		if id == "" {
			id = item.Link
		}
		if id == "" {
			id = "urn:film-fusion:rss:item:" + stableRSSGeneratorID(item.Title+item.Content)
		}
		entryUpdated := updated
		if item.UpdatedAt != nil {
			entryUpdated = item.UpdatedAt.UTC()
		} else if item.PublishedAt != nil {
			entryUpdated = item.PublishedAt.UTC()
		}
		entry := rssGeneratorAtomEntry{Title: item.Title, ID: id, Updated: entryUpdated.Format(time.RFC3339)}
		if item.PublishedAt != nil {
			entry.Published = item.PublishedAt.UTC().Format(time.RFC3339)
		}
		if item.Author != "" {
			entry.Author = &rssGeneratorAtomPerson{Name: item.Author}
		}
		if item.Link != "" {
			entry.Links = append(entry.Links, rssGeneratorAtomLink{Href: item.Link, Rel: "alternate"})
		}
		for _, enclosure := range item.Enclosures {
			entry.Links = append(entry.Links, rssGeneratorAtomLink{Href: enclosure.URL, Rel: "enclosure", Type: enclosure.Type, Length: enclosure.Length})
		}
		if item.Description != "" {
			entry.Summary = &rssGeneratorAtomText{Type: "html", Body: html.EscapeString(item.Description)}
		}
		if item.Content != "" {
			entry.Content = &rssGeneratorAtomText{Type: "html", Body: html.EscapeString(item.Content)}
		}
		for _, category := range item.Categories {
			entry.Categories = append(entry.Categories, rssGeneratorAtomCategory{Term: category})
		}
		document.Entries = append(document.Entries, entry)
	}
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	encoder := xml.NewEncoder(&buffer)
	encoder.Indent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func stableRSSGeneratorID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

func stripRSSGeneratorTokenFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	query.Del("token")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
