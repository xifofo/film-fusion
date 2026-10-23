package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/utils/embyhelper"
)

func TestEmbyLibraryCheckNormalization(t *testing.T) {
	response, err := prepareEmbyLibraryCheck([]string{"\ufeff \"三体.MKV\"\r\n\n'三体.srt'\n", "Mr.Robot", "1917.mp4", "Movie.MKV.torrent", "‘2001’", "unknown.release", "Avatar.2.mkv", "Avatar", " ＭＲ.ＲＯＢＯＴ "})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, result := range response.Results {
		names = append(names, result.Name)
	}
	want := []string{"三体", "Mr.Robot", "1917", "Movie", "2001", "unknown.release", "Avatar.2", "Avatar"}
	if !reflect.DeepEqual(names, want) || response.DuplicatesRemoved != 2 || response.Results[0].OriginalName != `"三体.MKV"` {
		t.Fatalf("normalization: %+v (%#v)", response, names)
	}
	for _, name := range []string{"Ocean's Eleven", "'71", "A.2025", "Version.1080p", "movie.nfo"} {
		if cleanEmbyLibraryName(name) != name {
			t.Errorf("unexpected title change: %q", name)
		}
	}
	for _, names := range [][]string{nil, {"", "\n", `""`}, {"..."}, {strings.Repeat("a", 1025)}, {strings.Repeat("Same\n", 201)}} {
		if _, err := prepareEmbyLibraryCheck(names); err == nil {
			t.Errorf("invalid input accepted: %#v", names)
		}
	}
}

func TestEmbyLibraryCheckMatchesAndFailures(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query := r.URL.Query()
		if query.Has("api_key") || r.Header.Get("X-Emby-Token") != "private-token" {
			t.Error("batch search must keep the API key in its authentication header")
		}
		if r.Method != http.MethodGet || r.URL.Path != "/Items" || query.Get("IncludeItemTypes") != "Movie,Series" || query.Get("Recursive") != "true" || query.Get("GroupItemsIntoCollections") != "false" || query.Get("ExcludeLocationTypes") != "Virtual" || query.Get("IsVirtualItem") != "false" || query.Get("IsPlaceHolder") != "false" {
			t.Errorf("unsafe or incorrect query: method=%s path=%s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch query.Get("SearchTerm") {
		case "Movie":
			fmt.Fprint(w, `{"Items":[{"Id":"m1","Name":"ＭＯＶＩＥ!","Type":"Movie","ProductionYear":2020,"Path":"/movies/movie.mkv"}],"TotalRecordCount":1}`)
		case "Original":
			fmt.Fprint(w, `{"Items":[{"Id":"s1","Name":"原作","OriginalTitle":"ORIGINAL","Type":"Series"}],"TotalRecordCount":1}`)
		case "Possible":
			fmt.Fprint(w, `{"Items":[{"Id":"m2","Name":"Possible 2","Type":"Movie"}],"TotalRecordCount":1}`)
		case "Missing":
			fmt.Fprint(w, `{"Items":[{"Id":"v","Name":"Missing","Type":"Movie","LocationType":"Virtual"},{"Id":"b","Name":"Missing","Type":"BoxSet"},{"Id":"e","Name":"Missing","Type":"Episode"},{"Id":"p","Name":"Missing","Type":"Movie","IsPlaceHolder":true},{"Id":"v2","Name":"Missing","Type":"Series","IsVirtualItem":true}],"TotalRecordCount":5}`)
		case "Failure":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "private-token api_key=secret")
		default:
			fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
		}
	}))
	defer upstream.Close()
	client := embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "private-token"}})
	defer client.Close()
	response, err := NewEmbyLibraryCheckService(client).Check(context.Background(), []string{"Movie.mkv", "Original", "Possible", "Missing", "Failure", "Empty", "Movie.srt"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"found", "found", "possible", "missing", "error", "missing"}
	for i, result := range response.Results {
		if result.Status != want[i] || result.Matches == nil {
			t.Errorf("row %d = %+v, want %s", i, result, want[i])
		}
	}
	if response.Total != 6 || response.DuplicatesRemoved != 1 || calls.Load() != 6 {
		t.Fatalf("duplicate queries or wrong totals: %+v, calls=%d", response, calls.Load())
	}
	if match := response.Results[0].Matches[0]; match.ID != "m1" || match.Year != 2020 || match.Path == "" {
		t.Errorf("match lost metadata: %+v", match)
	}
	for i, id := range []string{"m1", "s1", "m2"} {
		if got, want := response.Results[i].Matches[0].EmbyURL, upstream.URL+"/web/index.html#!/item?id="+id; got != want {
			t.Errorf("row %d Emby URL = %q, want %q", i, got, want)
		}
	}
	data, _ := json.Marshal(response)
	if strings.Contains(string(data), "private-token") || strings.Contains(string(data), "api_key") || strings.Contains(response.Results[4].Error, upstream.URL) {
		t.Fatalf("response exposed transport details: %s", data)
	}
}

func TestEmbyLibraryCheckPaginationAndTruncation(t *testing.T) {
	for _, mode := range []string{"later exact", "truncated", "inconsistent", "unknown total"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
				items := make([]embyhelper.LibrarySearchItem, 0, 100)
				total := 600
				if mode == "inconsistent" && start > 0 {
					// An unexpectedly empty page must never become "missing".
				} else if mode == "unknown total" && start > 0 {
					items = append(items, embyhelper.LibrarySearchItem{ID: "exact", Name: "Target", Type: "Movie"})
				} else {
					for i := 0; i < 100; i++ {
						items = append(items, embyhelper.LibrarySearchItem{ID: strconv.Itoa(start + i + 1), Name: "Not target", Type: "Movie"})
					}
					if mode == "later exact" && start == 100 {
						items[50].Name = "Target"
					}
				}
				w.Header().Set("Content-Type", "application/json")
				payload := map[string]any{"Items": items}
				if mode != "unknown total" {
					payload["TotalRecordCount"] = total
				}
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer upstream.Close()
			client := embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "test"}})
			defer client.Close()
			response, err := NewEmbyLibraryCheckService(client).Check(context.Background(), []string{"Target"})
			if err != nil {
				t.Fatal(err)
			}
			result := response.Results[0]
			want := "found"
			if mode == "truncated" {
				want = "possible"
				if !result.Truncated || calls.Load() != 5 || len(result.Matches) != 20 {
					t.Errorf("truncation bounds lost: %+v, calls=%d", result, calls.Load())
				}
			} else if mode == "inconsistent" {
				want = "error"
			}
			if result.Status != want {
				t.Errorf("result=%+v, want status %s", result, want)
			}
		})
	}
}

func TestEmbyLibraryCheckCancellationAndConcurrency(t *testing.T) {
	var active, peak, calls atomic.Int32
	entered := make(chan struct{}, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client := embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "secret"}})
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *EmbyLibraryCheckResponse, 1)
	go func() {
		response, _ := NewEmbyLibraryCheckService(client).Check(ctx, []string{"One", "Two", "Three", "Four", "Five", "Six"})
		done <- response
	}()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent searches did not start")
		}
	}
	cancel()
	select {
	case response := <-done:
		for _, result := range response.Results {
			if result.Status != "error" || result.Error != "查询已取消" {
				t.Errorf("cancellation misreported: %+v", result)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not reach Emby")
	}
	if peak.Load() > 4 || calls.Load() != 4 {
		t.Errorf("concurrency or cancellation boundary lost: peak=%d calls=%d", peak.Load(), calls.Load())
	}
}
