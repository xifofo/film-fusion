package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"
)

type stubRSSAutomationMediaStatusChecker struct {
	request RSSAutomationMediaStatusRequest
	status  RSSAutomationMediaStatus
}

func (s *stubRSSAutomationMediaStatusChecker) CheckRSSAutomationMediaStatus(_ context.Context, request RSSAutomationMediaStatusRequest) (RSSAutomationMediaStatus, error) {
	s.request = request
	return s.status, nil
}

type stubRSSAutomationHDHiveGateway struct {
	queryMediaType string
	queryTMDBID    string
	unlockSlug     string
	resources      []RSSAutomationHDHiveResource
	unlock         RSSAutomationHDHiveUnlockResult
}

func (s *stubRSSAutomationHDHiveGateway) QueryRSSAutomationHDHive(_ context.Context, mediaType, tmdbID string) ([]RSSAutomationHDHiveResource, error) {
	s.queryMediaType = mediaType
	s.queryTMDBID = tmdbID
	return s.resources, nil
}

func (s *stubRSSAutomationHDHiveGateway) UnlockRSSAutomationHDHive(_ context.Context, slug string) (RSSAutomationHDHiveUnlockResult, error) {
	s.unlockSlug = slug
	return s.unlock, nil
}

type stubRSSAutomationEmbyClient struct {
	refreshCalls int
	findCalls    int
	item         *embyhelper.EmbyLookupItem
}

func (s *stubRSSAutomationEmbyClient) RefreshLibrary() (int, string, error) {
	s.refreshCalls++
	return http.StatusNoContent, "", nil
}

func (s *stubRSSAutomationEmbyClient) FindItemByTmdbID(_, _ string) (*embyhelper.EmbyLookupItem, error) {
	s.findCalls++
	return s.item, nil
}

func (s *stubRSSAutomationEmbyClient) WebItemURL(id string) string {
	return "https://emby.example/items/" + id
}

func TestRSSAutomationMediaExistsAcceptsTemplatesAndRoutesExisting(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{UserID: 7, DirectoryName: "影视", SavePath: "/media"}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	checker := &stubRSSAutomationMediaStatusChecker{status: RSSAutomationMediaStatus{
		TmdbID: "1396", Title: "绝命毒师", MediaType: "tv", EmbyItemID: "emby-1",
		EmbyURL: "https://emby.example/items/emby-1", ExistingSeasons: []string{"Season 01"},
	}}
	automation := &RSSAutomationService{db: db, mediaStatus: checker}
	output, err := automation.executeRSSAutomationMediaExists(context.Background(), RSSAutomationNode{
		Type: RSSAutomationNodeMediaExists,
		Config: map[string]any{
			"cloud_directory_id": directory.ID,
			"tmdb_id":            "{{nodes.mp.output.tmdb_id}}",
			"title":              "绝命毒师",
			"media_type":         "tv",
		},
	}, rssAutomationTestRunContext("mp", map[string]any{"tmdb_id": "1396"}))
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "exists" || output["exists"] != true {
		t.Fatalf("unexpected media status output: %#v", output)
	}
	if checker.request.UserID != 7 || checker.request.TmdbID != "1396" || checker.request.MediaType != "tv" {
		t.Fatalf("unexpected media status request: %#v", checker.request)
	}
}

func TestRSSAutomationHDHiveQueryFiltersAndUnlocksSelectedResource(t *testing.T) {
	gateway := &stubRSSAutomationHDHiveGateway{
		resources: []RSSAutomationHDHiveResource{
			{Slug: "locked", Title: "4K locked", PanType: "115", VideoResolution: []string{"2160p"}},
			{Slug: "owned", Title: "4K owned", PanType: "115", VideoResolution: []string{"2160P HDR"}, IsUnlocked: true},
			{Slug: "other", Title: "other pan", PanType: "aliyun", VideoResolution: []string{"2160p"}},
		},
		unlock: RSSAutomationHDHiveUnlockResult{URL: "https://115.com/s/demo", AccessCode: "abcd", FullURL: "https://115.com/s/demo?password=abcd", AlreadyOwned: true},
	}
	automation := &RSSAutomationService{hdhive: gateway}
	output, err := automation.executeRSSAutomationHDHiveQuery(context.Background(), RSSAutomationNode{
		Type: RSSAutomationNodeHDHiveQuery,
		Config: map[string]any{
			"tmdb_id": "1396", "media_type": "tv", "resolution": "2160p", "pan_type": "115",
		},
	}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if gateway.queryMediaType != "tv" || gateway.queryTMDBID != "1396" {
		t.Fatalf("query arguments = %q, %q", gateway.queryMediaType, gateway.queryTMDBID)
	}
	if output["selected_port"] != "found" || output["resource_count"] != 2 || output["selected_slug"] != "owned" {
		t.Fatalf("unexpected query output: %#v", output)
	}

	unlockOutput, err := automation.executeRSSAutomationHDHiveUnlock(context.Background(), RSSAutomationNode{
		Type:   RSSAutomationNodeHDHiveUnlock,
		Config: map[string]any{"slug": "{{nodes.query.output.selected_slug}}"},
	}, rssAutomationTestRunContext("query", output))
	if err != nil {
		t.Fatal(err)
	}
	if gateway.unlockSlug != "owned" || unlockOutput["download_url"] != gateway.unlock.FullURL || unlockOutput["selected_port"] != "success" {
		t.Fatalf("unexpected unlock output: %#v", unlockOutput)
	}
}

func TestRSSAutomationStrmVerifyReadsOnlyConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	validPath := filepath.Join(root, "Movies", "Example.strm")
	if err := os.MkdirAll(filepath.Dir(validPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(validPath, []byte("https://media.example/Example.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db := newRSSAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{UserID: 1, DirectoryName: "STRM", SavePath: root}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{db: db}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{{Source: "organize", SourcePort: "success", Target: "verify"}}}
	output, err := automation.executeRSSAutomationStrmVerify(context.Background(), RSSAutomationNode{
		ID: "verify", Type: RSSAutomationNodeStrmVerify,
		Config: map[string]any{"cloud_directory_id": directory.ID},
	}, definition, rssAutomationTestRunContext("organize", map[string]any{"strm_path": validPath}))
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "valid" || output["valid_count"] != 1 || output["strm_content"] != "https://media.example/Example.mkv" {
		t.Fatalf("unexpected STRM output: %#v", output)
	}

	outside := filepath.Join(t.TempDir(), "outside.strm")
	if err := os.WriteFile(outside, []byte("https://media.example/outside.mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	invalid, err := automation.executeRSSAutomationStrmVerify(context.Background(), RSSAutomationNode{
		ID: "verify", Type: RSSAutomationNodeStrmVerify,
		Config: map[string]any{"cloud_directory_id": directory.ID},
	}, definition, rssAutomationTestRunContext("organize", map[string]any{"strm_path": outside}))
	if err != nil {
		t.Fatal(err)
	}
	if invalid["selected_port"] != "invalid" || invalid["invalid_count"] != 1 || !strings.Contains(strings.Join(invalid["errors"].([]string), " "), "不在目录配置") {
		t.Fatalf("outside path was not rejected: %#v", invalid)
	}
}

func TestRSSAutomationStrmRegenerateAtomicallyRestoresOrganizeOutput(t *testing.T) {
	root := t.TempDir()
	strmPath := filepath.Join(root, "TV", "Example.strm")
	if err := os.MkdirAll(filepath.Dir(strmPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strmPath, []byte("\x00broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := newRSSAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{UserID: 1, DirectoryName: "STRM", SavePath: root}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{db: db}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{
		{Source: "organize", SourcePort: "success", Target: "verify"},
		{Source: "verify", SourcePort: "invalid", Target: "regenerate"},
	}}
	runContext := map[string]any{
		"item": map[string]any{}, "vars": map[string]any{},
		"nodes": map[string]any{
			"organize": map[string]any{"status": model.RSSAutomationNodeSucceeded, "output": map[string]any{
				"strm_path": strmPath, "strm_content": "https://media.example/Example.mkv",
			}},
			"verify": map[string]any{"status": model.RSSAutomationNodeSucceeded, "output": map[string]any{
				"valid": false, "selected_port": "invalid",
			}},
		},
	}
	output, err := automation.executeRSSAutomationStrmRegenerate(context.Background(), RSSAutomationNode{
		ID: "regenerate", Type: RSSAutomationNodeStrmRegenerate,
		Config: map[string]any{"cloud_directory_id": directory.ID},
	}, definition, runContext)
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "success" || output["regenerated_count"] != 1 {
		t.Fatalf("unexpected regeneration output: %#v", output)
	}
	content, err := os.ReadFile(strmPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "https://media.example/Example.mkv\n" {
		t.Fatalf("regenerated content = %q", string(content))
	}
}

func TestRSSAutomationEmbyRefreshWaitRefreshesOnceAcrossPolls(t *testing.T) {
	emby := &stubRSSAutomationEmbyClient{}
	automation := &RSSAutomationService{emby: emby}
	node := RSSAutomationNode{Type: RSSAutomationNodeEmbyRefreshWait, Config: map[string]any{
		"tmdb_id": "1396", "media_type": "tv", "poll_interval_seconds": 5, "max_wait_minutes": 30, "refresh_library": true,
	}}
	first, err := automation.executeRSSAutomationEmbyRefreshWait(context.Background(), model.RSSAutomationNodeRun{}, node, map[string]any{})
	var deferred *rssAutomationNodeDeferred
	if !errors.As(err, &deferred) || first["refresh_requested"] != true || emby.refreshCalls != 1 {
		t.Fatalf("first Emby poll = %#v, %v, refreshes=%d", first, err, emby.refreshCalls)
	}
	encoded, _ := json.Marshal(first)
	emby.item = &embyhelper.EmbyLookupItem{ID: "emby-1396", Name: "绝命毒师", Type: "Series"}
	second, err := automation.executeRSSAutomationEmbyRefreshWait(context.Background(), model.RSSAutomationNodeRun{OutputJSON: string(encoded)}, node, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if second["selected_port"] != "success" || second["emby_item_id"] != "emby-1396" || emby.refreshCalls != 1 {
		t.Fatalf("second Emby poll = %#v, refreshes=%d", second, emby.refreshCalls)
	}
}

func TestRSSAutomationWaitQBittorrentUsesLocalMockAndCompletes(t *testing.T) {
	var requestedTag string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "local-test", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			requestedTag = r.URL.Query().Get("tag")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"hash":"ABC","name":"Example","progress":1,"state":"uploading","save_path":"/downloads","content_path":"/downloads/Example","size":100,"downloaded":100,"amount_left":0,"ratio":1.2}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newRSSAutomationTestDB(t)
	config, _ := json.Marshal(RSSAutomationQBittorrentConfig{BaseURL: server.URL, Username: "admin", Password: "secret"})
	target := model.RSSAutomationTarget{Name: "mock qB", Type: model.RSSAutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(config)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{db: db}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{{Source: "qb", SourcePort: "success", Target: "wait"}}}
	output, err := automation.executeRSSAutomationWaitQBittorrent(context.Background(), model.RSSAutomationNodeRun{}, RSSAutomationNode{
		ID: "wait", Type: RSSAutomationNodeWaitQBittorrent,
	}, definition, rssAutomationTestRunContext("qb", map[string]any{"target_id": target.ID, "torrent_tag": "filmfusion-rss-test"}))
	if err != nil {
		t.Fatal(err)
	}
	if requestedTag != "filmfusion-rss-test" || output["selected_port"] != "success" || output["completed"] != true || output["content_path"] != "/downloads/Example" {
		t.Fatalf("unexpected qB wait output: %#v; tag=%q", output, requestedTag)
	}
}
