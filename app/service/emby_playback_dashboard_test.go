package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/utils/embyhelper"
)

func dashboardTestService(t *testing.T, handler http.HandlerFunc) *EmbyWatchService {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: server.URL, APIKey: "private-test-key"}})
	t.Cleanup(func() { _ = client.Close() })
	return &EmbyWatchService{emby: client}
}

func TestPlaybackDashboardSessionProjection(t *testing.T) {
	svc := dashboardTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/Sessions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `[
		 {"Id":"idle"},
		 {"Id":"unknown","RemoteEndPoint":"same-ip","NowPlayingItem":{"Id":"movie","Name":"Movie"}},
		 {"Id":"active","UserName":"User","DeviceName":"TV","RemoteEndPoint":"same-ip",
		  "NowPlayingItem":{"Id":"episode","Name":"Episode","SeriesId":"series","SeriesName":"Show","ParentIndexNumber":0,"IndexNumber":1,"Path":"secret-file","RunTimeTicks":900000000,
		   "MediaSources":[{"Id":"other","Container":"wrong"},{"Id":"chosen","Container":"mkv","Bitrate":15000000,"RunTimeTicks":49880000000,"Path":"secret-source","MediaStreams":[
		    {"Index":0,"Type":"Video","Codec":"hevc","Width":3840,"Height":2160},
		    {"Index":1,"Type":"Audio","Language":"eng","IsDefault":true},
		    {"Index":2,"Type":"Audio","Language":"chi","Codec":"aac","Channels":2}]}]},
		  "PlayState":{"MediaSourceId":"chosen","AudioStreamIndex":2,"PositionTicks":40850000000,"IsPaused":true,"PlayMethod":"Transcode"},
		  "TranscodingInfo":{"IsVideoDirect":true,"IsAudioDirect":false},"AccessToken":"secret-token"}
		]`)
	})
	rows, err := svc.NowPlaying(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "active" {
		t.Fatalf("idle filtered or identity/sorting failed: %+v", rows)
	}
	row := rows[0]
	if row.PosterID != "series" || row.SeasonNumber == nil || *row.SeasonNumber != 0 || row.PositionSeconds == nil || *row.PositionSeconds != 4085 || row.DurationSeconds == nil || *row.DurationSeconds != 4988 || row.Paused == nil || !*row.Paused {
		t.Fatalf("metadata/progress mismatch: %+v", row)
	}
	if row.Container != "mkv" || row.Bitrate != 15000000 || row.Audio == nil || row.Audio.Language != "chi" || row.Audio.Direct == nil || *row.Audio.Direct || row.Video == nil || row.Video.Direct == nil || !*row.Video.Direct {
		t.Fatalf("selected media streams mismatch: %+v", row)
	}
	if rows[1].PositionSeconds != nil || rows[1].DurationSeconds != nil || rows[1].Paused != nil {
		t.Fatal("missing state must stay unknown")
	}
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "secret-") || strings.Contains(string(encoded), "private-test-key") {
		t.Fatal("private upstream fields escaped projection")
	}
}

func TestPlaybackDashboardDirectAndUnmatchedAudio(t *testing.T) {
	svc := dashboardTestService(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"Id":"a","NowPlayingItem":{"Id":"m","MediaStreams":[{"Index":0,"Type":"Video"},{"Index":1,"Type":"Audio"}]},"PlayState":{"PositionTicks":-20,"AudioStreamIndex":5,"PlayMethod":"DirectPlay"}}]`)
	})
	rows, err := svc.NowPlaying(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Audio != nil || rows[0].Video == nil || rows[0].Video.Direct == nil || !*rows[0].Video.Direct || *rows[0].PositionSeconds != 0 {
		t.Fatalf("incorrect direct stream or guessed audio: %+v", rows)
	}
}

func TestPlaybackDashboardHistoryShortPagesAndEvents(t *testing.T) {
	calls := 0
	svc := dashboardTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/System/ActivityLog/Entries" || r.URL.Query().Get("Limit") != "100" {
			t.Errorf("unexpected history request")
		}
		if r.URL.Query().Get("StartIndex") != fmt.Sprint(calls*2) {
			t.Errorf("short page skipped records: %s", r.URL.RawQuery)
		}
		pages := []string{
			`{"Items":[{"Id":1,"Type":"system.backupcompleted"},{"Id":2,"Type":"other"}],"TotalRecordCount":7}`,
			`{"Items":[{"Id":9007199254740993,"Type":"playback.stop","Name":"Stopped","Date":"2026-11-09T01:00:00Z"},{"Id":3,"Type":"playback.start","Name":"Started","Date":"2026-11-09T00:00:00Z"}],"TotalRecordCount":7}`,
			`{"Items":[{"Id":3,"Type":"playback.start"},{"Id":4,"Type":"VideoPlaybackStopped","Name":"Legacy","Date":"2026-11-08T00:00:00Z"}],"TotalRecordCount":7}`,
		}
		if calls >= len(pages) {
			t.Error("unbounded history request")
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, pages[calls])
		calls++
	})
	view, err := svc.PlaybackHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || view.Scanned != 6 || !view.ScanLimited || len(view.Items) != 3 || view.Items[0].ID != "9007199254740993" || view.Items[0].Event != "stop" || view.Items[1].Event != "start" || view.Items[2].Event != "stop" {
		t.Fatalf("history mismatch: %+v, calls=%d", view, calls)
	}
}

func TestPlaybackDashboardHistoryTwentyLimit(t *testing.T) {
	calls := 0
	svc := dashboardTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		items := make([]map[string]any, 21)
		for i := range items {
			items[i] = map[string]any{"Id": i + 1, "Type": "AudioPlayback", "Name": "Started", "Date": "2026-11-09T00:00:00Z"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 1000})
	})
	view, err := svc.PlaybackHistory(context.Background())
	if err != nil || calls != 1 || len(view.Items) != 20 || view.ScanLimited {
		t.Fatalf("limit mismatch: %+v, calls=%d, error=%v", view, calls, err)
	}
}
