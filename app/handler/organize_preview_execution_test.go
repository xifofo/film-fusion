package handler

import (
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

type organizePreviewTransport func(*http.Request) (*http.Response, error)

func (f organizePreviewTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestOrganizePreviewReturnsSelectedFilePlanWithoutExecuting(t *testing.T) {
	const rename = "Demo.2026.1080p.mkv"
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":3600}`)
		case "/api/v1/media/recognize_file":
			_, _ = io.WriteString(w, `{"success":true,"data":{"media_info":{"media_type":"movie","title":"Demo","year":"2026","tmdb_id":"123"}}}`)
		case "/api/v1/transfer/name":
			_, _ = io.WriteString(w, `{"success":true,"data":{"name":"`+rename+`"}}`)
		default:
			t.Errorf("unexpected MoviePilot call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer mp.Close()

	log := logger.New(config.LogConfig{Output: "stdout", Format: "json", Level: "error"})
	defer log.Close()
	h := &OrganizeHandler{
		logger:    log,
		web115Svc: service.NewWeb115Service(log),
		moviePilotSvc: service.NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{
			API: mp.URL, Username: "test", Password: "test",
		}}, log),
	}
	client := driver.New()
	readCalls := 0
	mutationCalls := 0
	client.Client.SetTransport(organizePreviewTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/files":
			readCalls++
			body = `{"state":true,"count":3,"data":[
				{"fid":"101","cid":"100","n":"Demo.2026.mkv","ico":"mkv","s":"1000","pc":"video-pick"},
				{"fid":"102","cid":"100","n":"Other.2026.mkv","ico":"mkv","s":"1000","pc":"other-pick"},
				{"fid":"103","cid":"100","n":"Demo.2026.zh.srt","ico":"srt","s":"100","pc":"subtitle-pick"}
			]}`
		case r.Method == http.MethodGet && r.URL.Path == "/files/getid":
			readCalls++
			body = `{"state":true,"id":"0"}`
			if r.URL.Query().Get("path") == "/影视" {
				body = `{"state":true,"id":"10"}`
			}
		default:
			mutationCalls++
			return nil, fmt.Errorf("preview attempted a 115 mutation: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	}))
	savePath := filepath.Join(t.TempDir(), "library")
	group := h.processOrganize115CookieFolder(processOrganizeArgs{
		dir: model.CloudDirectory{
			DirectoryName: "影视", SavePath: savePath, ContentPrefix: "https://media.example/strm",
		},
		storage:           &model.CloudStorage{ID: 3},
		webClient:         client,
		folderID:          "100",
		fileIDs:           map[string]struct{}{"101": {}},
		dryRun:            true,
		mediaType:         "movie",
		recognitionSource: service.MediaRecognitionSourceMoviePilot,
	})
	if group.Error != "" {
		t.Fatalf("preview failed: %s", group.Error)
	}
	if readCalls == 0 || mutationCalls != 0 {
		t.Fatalf("preview calls: reads=%d mutations=%d", readCalls, mutationCalls)
	}
	if group.Total != 1 || len(group.Items) != 2 {
		t.Fatalf("preview did not scope the plan to selected video and its subtitle: %+v", group)
	}
	for _, item := range group.Items {
		if item.FileID == "102" || item.SubtitleQueued {
			t.Fatalf("preview included an unselected file or queued a download: %+v", item)
		}
		if item.IsSubtitle {
			continue
		}
		if item.RenameTo != rename || !item.NeedCreate || len(item.MissingDirs) == 0 ||
			!strings.HasPrefix(item.TargetPath, "/影视/") || !strings.HasSuffix(item.StrmPath, ".strm") ||
			!strings.HasPrefix(item.StrmContent, "https://media.example/strm/") {
			t.Fatalf("preview is missing the naming, directory or STRM plan: %+v", item)
		}
	}
	if _, err := os.Stat(savePath); !os.IsNotExist(err) {
		t.Fatalf("preview wrote to the library: %v", err)
	}
}
