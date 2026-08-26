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

type stubAutomationMediaStatusChecker struct {
	request AutomationMediaStatusRequest
	status  AutomationMediaStatus
}

func (s *stubAutomationMediaStatusChecker) CheckAutomationMediaStatus(_ context.Context, request AutomationMediaStatusRequest) (AutomationMediaStatus, error) {
	s.request = request
	return s.status, nil
}

type stubAutomationHDHiveGateway struct {
	queryMediaType string
	queryTMDBID    string
	unlockSlug     string
	resources      []AutomationHDHiveResource
	unlock         AutomationHDHiveUnlockResult
}

func (s *stubAutomationHDHiveGateway) QueryAutomationHDHive(_ context.Context, mediaType, tmdbID string) ([]AutomationHDHiveResource, error) {
	s.queryMediaType = mediaType
	s.queryTMDBID = tmdbID
	return s.resources, nil
}

func (s *stubAutomationHDHiveGateway) UnlockAutomationHDHive(_ context.Context, slug string) (AutomationHDHiveUnlockResult, error) {
	s.unlockSlug = slug
	return s.unlock, nil
}

type stubAutomationEmbyClient struct {
	refreshCalls int
	findCalls    int
	item         *embyhelper.EmbyLookupItem
}

type stubAutomationMoviePilotTransfer struct {
	request MoviePilotManualTransferRequest
	result  MoviePilotManualTransferResult
	err     error
}

func (s *stubAutomationMoviePilotTransfer) ManualTransfer(_ context.Context, request MoviePilotManualTransferRequest) (MoviePilotManualTransferResult, error) {
	s.request = request
	return s.result, s.err
}

func (s *stubAutomationEmbyClient) RefreshLibrary() (int, string, error) {
	s.refreshCalls++
	return http.StatusNoContent, "", nil
}

func (s *stubAutomationEmbyClient) FindItemByTmdbID(_, _ string) (*embyhelper.EmbyLookupItem, error) {
	s.findCalls++
	return s.item, nil
}

func (s *stubAutomationEmbyClient) WebItemURL(id string) string {
	return "https://emby.example/items/" + id
}

func TestAutomationMediaExistsAcceptsTemplatesAndRoutesExisting(t *testing.T) {
	db := newAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{UserID: 7, DirectoryName: "影视", SavePath: "/media"}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	checker := &stubAutomationMediaStatusChecker{status: AutomationMediaStatus{
		TmdbID: "1396", Title: "绝命毒师", MediaType: "tv", EmbyItemID: "emby-1",
		EmbyURL: "https://emby.example/items/emby-1", ExistingSeasons: []string{"Season 01"},
	}}
	automation := &AutomationService{db: db, mediaStatus: checker}
	output, err := automation.executeAutomationMediaExists(context.Background(), AutomationNode{
		Type: AutomationNodeMediaExists,
		Config: map[string]any{
			"cloud_directory_id": directory.ID,
			"tmdb_id":            "{{nodes.mp.output.tmdb_id}}",
			"title":              "绝命毒师",
			"media_type":         "tv",
		},
	}, automationTestRunContext("mp", map[string]any{"tmdb_id": "1396"}))
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

func TestAutomationHDHiveQueryFiltersAndUnlocksSelectedResource(t *testing.T) {
	gateway := &stubAutomationHDHiveGateway{
		resources: []AutomationHDHiveResource{
			{Slug: "locked", Title: "4K locked", PanType: "115", VideoResolution: []string{"2160p"}},
			{Slug: "owned", Title: "4K owned", PanType: "115", VideoResolution: []string{"2160P HDR"}, IsUnlocked: true},
			{Slug: "other", Title: "other pan", PanType: "aliyun", VideoResolution: []string{"2160p"}},
		},
		unlock: AutomationHDHiveUnlockResult{URL: "https://115.com/s/demo", AccessCode: "abcd", FullURL: "https://115.com/s/demo?password=abcd", AlreadyOwned: true},
	}
	automation := &AutomationService{hdhive: gateway}
	output, err := automation.executeAutomationHDHiveQuery(context.Background(), AutomationNode{
		Type: AutomationNodeHDHiveQuery,
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

	unlockOutput, err := automation.executeAutomationHDHiveUnlock(context.Background(), AutomationNode{
		Type:   AutomationNodeHDHiveUnlock,
		Config: map[string]any{"slug": "{{nodes.query.output.selected_slug}}"},
	}, automationTestRunContext("query", output))
	if err != nil {
		t.Fatal(err)
	}
	if gateway.unlockSlug != "owned" || unlockOutput["download_url"] != gateway.unlock.FullURL || unlockOutput["selected_port"] != "success" {
		t.Fatalf("unexpected unlock output: %#v", unlockOutput)
	}
}

func TestAutomationStrmVerifyReadsOnlyConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	validPath := filepath.Join(root, "Movies", "Example.strm")
	if err := os.MkdirAll(filepath.Dir(validPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(validPath, []byte("https://media.example/Example.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db := newAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{UserID: 1, DirectoryName: "STRM", SavePath: root}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db}
	definition := AutomationDefinition{Edges: []AutomationEdge{{Source: "organize", SourcePort: "success", Target: "verify"}}}
	output, err := automation.executeAutomationStrmVerify(context.Background(), AutomationNode{
		ID: "verify", Type: AutomationNodeStrmVerify,
		Config: map[string]any{"cloud_directory_id": directory.ID},
	}, definition, automationTestRunContext("organize", map[string]any{"strm_path": validPath}))
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
	invalid, err := automation.executeAutomationStrmVerify(context.Background(), AutomationNode{
		ID: "verify", Type: AutomationNodeStrmVerify,
		Config: map[string]any{"cloud_directory_id": directory.ID},
	}, definition, automationTestRunContext("organize", map[string]any{"strm_path": outside}))
	if err != nil {
		t.Fatal(err)
	}
	if invalid["selected_port"] != "invalid" || invalid["invalid_count"] != 1 || !strings.Contains(strings.Join(invalid["errors"].([]string), " "), "不在目录配置") {
		t.Fatalf("outside path was not rejected: %#v", invalid)
	}
}

func TestAutomationStrmRegenerateAtomicallyRestoresOrganizeOutput(t *testing.T) {
	root := t.TempDir()
	strmPath := filepath.Join(root, "TV", "Example.strm")
	if err := os.MkdirAll(filepath.Dir(strmPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strmPath, []byte("\x00broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := newAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{UserID: 1, DirectoryName: "STRM", SavePath: root}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db}
	definition := AutomationDefinition{Edges: []AutomationEdge{
		{Source: "organize", SourcePort: "success", Target: "verify"},
		{Source: "verify", SourcePort: "invalid", Target: "regenerate"},
	}}
	runContext := map[string]any{
		"item": map[string]any{}, "vars": map[string]any{},
		"nodes": map[string]any{
			"organize": map[string]any{"status": model.AutomationNodeSucceeded, "output": map[string]any{
				"strm_path": strmPath, "strm_content": "https://media.example/Example.mkv",
			}},
			"verify": map[string]any{"status": model.AutomationNodeSucceeded, "output": map[string]any{
				"valid": false, "selected_port": "invalid",
			}},
		},
	}
	output, err := automation.executeAutomationStrmRegenerate(context.Background(), AutomationNode{
		ID: "regenerate", Type: AutomationNodeStrmRegenerate,
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

func TestAutomationEmbyRefreshWaitRefreshesOnceAcrossPolls(t *testing.T) {
	emby := &stubAutomationEmbyClient{}
	automation := &AutomationService{emby: emby}
	node := AutomationNode{Type: AutomationNodeEmbyRefreshWait, Config: map[string]any{
		"tmdb_id": "1396", "media_type": "tv", "poll_interval_seconds": 5, "max_wait_minutes": 30, "refresh_library": true,
	}}
	first, err := automation.executeAutomationEmbyRefreshWait(context.Background(), model.AutomationNodeRun{}, node, map[string]any{})
	var deferred *automationNodeDeferred
	if !errors.As(err, &deferred) || first["refresh_requested"] != true || emby.refreshCalls != 1 {
		t.Fatalf("first Emby poll = %#v, %v, refreshes=%d", first, err, emby.refreshCalls)
	}
	encoded, _ := json.Marshal(first)
	emby.item = &embyhelper.EmbyLookupItem{ID: "emby-1396", Name: "绝命毒师", Type: "Series"}
	second, err := automation.executeAutomationEmbyRefreshWait(context.Background(), model.AutomationNodeRun{OutputJSON: string(encoded)}, node, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if second["selected_port"] != "success" || second["emby_item_id"] != "emby-1396" || emby.refreshCalls != 1 {
		t.Fatalf("second Emby poll = %#v, refreshes=%d", second, emby.refreshCalls)
	}
}

func TestAutomationWaitQBittorrentUsesLocalMockAndCompletes(t *testing.T) {
	var requestedTag string
	var requestedAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			requestedTag = r.URL.Query().Get("tag")
			requestedAuthorization = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"hash":"ABC","name":"Example","progress":1,"state":"uploading","save_path":"/downloads","content_path":"/downloads/Example","size":100,"downloaded":100,"amount_left":0,"ratio":1.2}]`))
		case "/api/v2/torrents/files":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"Example.mkv","size":100,"progress":1}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	config, _ := json.Marshal(AutomationQBittorrentConfig{BaseURL: server.URL, APIKey: testAutomationQBAPIKey})
	target := model.AutomationTarget{Name: "mock qB", Type: model.AutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(config)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db}
	definition := AutomationDefinition{Edges: []AutomationEdge{{Source: "qb", SourcePort: "success", Target: "wait"}}}
	output, err := automation.executeAutomationWaitQBittorrent(context.Background(), model.AutomationNodeRun{}, AutomationNode{
		ID: "wait", Type: AutomationNodeWaitQBittorrent,
	}, definition, automationTestRunContext("qb", map[string]any{"target_id": target.ID, "torrent_tag": "filmfusion-automation-test"}))
	if err != nil {
		t.Fatal(err)
	}
	if requestedTag != "filmfusion-automation-test" || requestedAuthorization != "Bearer "+testAutomationQBAPIKey || output["selected_port"] != "success" || output["completed"] != true || output["content_path"] != "/downloads/Example" || output["content_type"] != "file" || output["file_count"] != 1 {
		t.Fatalf("unexpected qB wait output: %#v; tag=%q authorization=%q", output, requestedTag, requestedAuthorization)
	}
}

func TestAutomationMoviePilotTransferUsesCompletedQBOutput(t *testing.T) {
	transfer := &stubAutomationMoviePilotTransfer{result: MoviePilotManualTransferResult{Message: "整理完成"}}
	automation := &AutomationService{mpTransfer: transfer}
	definition := AutomationDefinition{Edges: []AutomationEdge{{Source: "wait", SourcePort: "success", Target: "mp_transfer"}}}
	output, err := automation.executeAutomationMoviePilotTransfer(context.Background(), AutomationNode{
		ID: "mp_transfer", Type: AutomationNodeMoviePilotTransfer,
		Config: map[string]any{"file_type": "auto", "media_type": "tv", "tmdb_id": "1396", "scrape": true},
	}, definition, automationTestRunContext("wait", map[string]any{
		"completed": true, "target_id": 9, "target_name": "mock qB", "hash": "ABC",
		"name": "Example", "content_path": "/downloads/Example", "content_type": "dir",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if transfer.request.SourcePath != "/downloads/Example" || transfer.request.FileType != "dir" || transfer.request.TmdbID != "1396" || transfer.request.MediaType != "tv" || !transfer.request.Scrape {
		t.Fatalf("unexpected MP2 request: %#v", transfer.request)
	}
	if output["organized"] != true || output["hash"] != "ABC" || output["target_id"] != uint(9) || output["message"] != "整理完成" {
		t.Fatalf("unexpected MP2 output: %#v", output)
	}
}

func TestAutomationDeleteQBittorrentRemovesCompletedTaskWithoutFiles(t *testing.T) {
	var deleteHash string
	var deleteFiles string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"hash":"ABC","name":"Example","progress":1,"state":"uploading","content_path":"/downloads/Example","size":100,"amount_left":0}]`))
		case "/api/v2/torrents/delete":
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			deleteHash = r.Form.Get("hashes")
			deleteFiles = r.Form.Get("deleteFiles")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newAutomationTestDB(t)
	config, _ := json.Marshal(AutomationQBittorrentConfig{BaseURL: server.URL, APIKey: testAutomationQBAPIKey})
	target := model.AutomationTarget{Name: "mock qB", Type: model.AutomationTargetQBittorrent, Enabled: true, ConfigJSON: string(config)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	automation := &AutomationService{db: db}
	definition := AutomationDefinition{Edges: []AutomationEdge{{Source: "mp_transfer", SourcePort: "success", Target: "delete"}}}
	output, err := automation.executeAutomationDeleteQBittorrent(context.Background(), AutomationNode{
		ID: "delete", Type: AutomationNodeDeleteQBittorrent, Config: map[string]any{"delete_files": false},
	}, definition, automationTestRunContext("mp_transfer", map[string]any{
		"organized": true, "target_id": target.ID, "hash": "ABC",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if deleteHash != "ABC" || deleteFiles != "false" || output["deleted"] != true || output["delete_files"] != false {
		t.Fatalf("unexpected qB delete: output=%#v hash=%q deleteFiles=%q", output, deleteHash, deleteFiles)
	}
}
