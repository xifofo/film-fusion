package embyhelper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/config"
)

func TestLibrarySearchRejectsMalformedSuccessfulResponses(t *testing.T) {
	for _, payload := range []string{`{}`, `null`, `{"Items":null}`, `{"Items":{}}`, `{"Items":"bad"}`, `{"Items":[1]}`, `{"Items":[null],"TotalRecordCount":1}`, `{"Items":[{}],"TotalRecordCount":1}`, `{"Items":[],"TotalRecordCount":-1}`, `{"Items":[{"Id":"a","Name":"Movie","Type":"Movie"}],"TotalRecordCount":0}`, `{"Items":[]}{"Items":[]}`, `<html>secret</html>`} {
		t.Run(payload, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, payload)
			}))
			defer upstream.Close()
			client := New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "secret"}})
			defer client.Close()
			if _, err := client.SearchLibraryPage(context.Background(), "Movie", 0, 100); err == nil {
				t.Fatalf("malformed response accepted: %s", payload)
			}
		})
	}
}

func TestLibrarySearchSnapshotKeepsServerAndRefusesRedirects(t *testing.T) {
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
	}))
	defer other.Close()
	var originalCalls atomic.Int32
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originalCalls.Add(1)
		if r.Header.Get("X-Emby-Token") != "original-key" || r.URL.Query().Has("api_key") {
			t.Error("snapshot changed credentials or exposed them in query parameters")
		}
		if r.URL.Query().Get("SearchTerm") == "Redirect" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
	}))
	defer original.Close()
	cfg := &config.Config{Emby: config.EmbyConfig{URL: original.URL, APIKey: "original-key"}}
	shared := New(cfg)
	defer shared.Close()
	snapshot := shared.LibrarySearchSnapshot()
	defer snapshot.Close()
	cfg.Emby.URL, cfg.Emby.APIKey = other.URL, "changed-key"
	shared.Reload()
	if _, err := snapshot.SearchLibraryPage(context.Background(), "Movie", 0, 100); err != nil {
		t.Fatalf("snapshot search: %v", err)
	}
	if _, err := snapshot.SearchLibraryPage(context.Background(), "Redirect", 0, 100); err == nil {
		t.Fatal("redirect was accepted")
	}
	if originalCalls.Load() != 2 || redirected.Load() != 0 {
		t.Fatalf("batch escaped its snapshot: original=%d redirected=%d", originalCalls.Load(), redirected.Load())
	}
	if got, want := snapshot.LibrarySearchItemWebURL("item-1"), original.URL+"/web/index.html#!/item?id=item-1"; got != want {
		t.Fatalf("item link escaped its snapshot: got %q, want %q", got, want)
	}
}

func TestLibrarySearchItemWebURL(t *testing.T) {
	for _, test := range []struct {
		name, base, id, want string
	}{
		{"root", "https://emby.example", "123", "https://emby.example/web/index.html#!/item?id=123"},
		{"proxy path", "http://emby.example:8096/media/emby/", "123", "http://emby.example:8096/media/emby/web/index.html#!/item?id=123"},
		{"encoded path", "https://emby.example/media%20server/emby/", "movie&id=2#/汉字", "https://emby.example/media%20server/emby/web/index.html#!/item?id=movie%26id%3D2%23%2F%E6%B1%89%E5%AD%97"},
		{"whitespace", " https://emby.example/ ", " 123 ", "https://emby.example/web/index.html#!/item?id=123"},
		{"blank id", "https://emby.example", " ", ""},
		{"blank base", "", "123", ""},
		{"credentials", "https://user:private-key@emby.example", "123", ""},
		{"query secret", "https://emby.example?api_key=private-key", "123", ""},
		{"empty query", "https://emby.example?", "123", ""},
		{"fragment secret", "https://emby.example#private-key", "123", ""},
		{"empty fragment", "https://emby.example#", "123", ""},
		{"unsafe scheme", "javascript:alert(1)", "123", ""},
		{"non web scheme", "ftp://emby.example", "123", ""},
		{"scheme relative", "//emby.example", "123", ""},
		{"missing host", "https:///emby", "123", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New(&config.Config{Emby: config.EmbyConfig{URL: test.base, APIKey: "private-key"}})
			defer client.Close()
			if got := client.LibrarySearchItemWebURL(test.id); got != test.want {
				t.Fatalf("item URL = %q, want %q", got, test.want)
			}
		})
	}
	var nilClient *EmbyClient
	if got := nilClient.LibrarySearchItemWebURL("123"); got != "" {
		t.Fatalf("nil client produced link: %q", got)
	}
}

func TestLibrarySearchTransportErrorsAndResponseLimits(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("s", 3<<20))
	}))
	client := New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "private-key"}})
	defer client.Close()
	defer upstream.Close()
	for _, closed := range []bool{false, true} {
		if closed {
			upstream.Close()
		}
		_, err := client.SearchLibraryPage(context.Background(), "Movie", 0, 100)
		if err == nil {
			t.Fatal("expected oversized-response or connection error")
		}
		if strings.Contains(err.Error(), upstream.URL) || strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), "api_key") {
			t.Fatalf("unsafe transport error: %v", err)
		}
	}
	missing := New(&config.Config{})
	defer missing.Close()
	if _, err := missing.SearchLibraryPage(context.Background(), "Movie", 0, 100); !errors.Is(err, ErrLibrarySearchUnconfigured) {
		t.Fatalf("missing config: %v", err)
	}
}
