package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
)

func TestBuiltinRecognitionNeverCallsMoviePilot(t *testing.T) {
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected MP2 call: %s", r.URL.Path)
		http.Error(w, "MP2 is unavailable", http.StatusServiceUnavailable)
	}))
	defer mp.Close()
	local, cfg := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/123":
			writeMedianameJSON(w, medianameDetails(123, "Example", "tv"))
		case "/3/search/tv", "/3/search/movie", "/3/search/multi":
			writeMedianameJSON(w, map[string]any{"page": 1, "total_pages": 1, "results": []any{}})
		default:
			http.NotFound(w, r)
		}
	})
	cfg.MoviePilot = config.MoviePilotConfig{API: mp.URL, Username: "test", Password: "test"}
	provider := NewMoviePilotService(cfg, nil)
	provider.SetLocalMediaRecognition(local)
	for _, source := range []string{"", "moviepilot", "MP2", "shadow", "影子模式", "medianame", "local"} {
		t.Run(source, func(t *testing.T) {
			info, raw, err := provider.RecognizeFileWithSource("Example.2024.S00E01.[tmdb=123].mkv", source)
			if err != nil || info.TmdbID != "123" || raw["engine"] != "medianame" {
				t.Fatalf("recognition: %+v %v", info, err)
			}
			if _, _, err := provider.RecognizeTitleWithSource("Example.2024.S00E01.[tmdb=123]", source); err != nil {
				t.Fatal(err)
			}
			if name, _, err := provider.TransferNameWithSource("Example.2024.S00E01.[tmdb=123].mkv", "mkv", source); err != nil || name == "" {
				t.Fatalf("name=%q err=%v", name, err)
			}
			if _, err := provider.GetCategoryConfigWithSource(source); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := provider.SearchMedia("Missing", 8); err != nil {
		t.Fatal(err)
	}
	provider.local = nil
	if _, _, err := provider.RecognizeFileWithSource("Example.mkv", "moviepilot"); err == nil {
		t.Fatal("missing local service accepted")
	}
	if _, _, err := provider.RecognizeFileWithSource("Example.mkv", "unknown"); err == nil {
		t.Fatal("unknown engine accepted")
	}
}

func TestLegacyRecognitionNodeUsesMedianameAndRejectsConflictingIDs(t *testing.T) {
	local, _ := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search/") {
			t.Error("conflicting explicit IDs were retried as a name search")
		}
		writeMedianameJSON(w, medianameDetails(123, "Example", "tv"))
	})
	automation := &AutomationService{localMedia: local}
	node := AutomationNode{Config: map[string]any{
		"input":       "Example.S00E01 [tmdb=123] [imdb=tt9999999]",
		"lookup_tmdb": true,
	}}
	output, err := automation.executeAutomationFilmFusionTitleRecognize(context.Background(), node, nil)
	if err != nil || output["engine"] != "medianame" || output["selected_port"] != "failure" || output["tmdb_status"] != "review" {
		t.Fatalf("conflicting identity was adopted: %#v, %v", output, err)
	}

	parsed, err := local.Recognize(context.Background(), "/Example [tmdb=123]/Season 0/00.mkv", MediaRecognitionOptions{Mode: MediaRecognitionModeFile})
	if err != nil || parsed.Engine != "medianame" || parsed.MetaInfo.SeasonEpisode != "S00E00" || parsed.MetaInfo.TMDBID != "123" {
		t.Fatalf("parse-only path lost medianame evidence: %+v, %v", parsed, err)
	}
}

func TestMediaSearchUsesMedianameCategories(t *testing.T) {
	local, _ := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/search/movie":
			writeMedianameJSON(w, map[string]any{"page": 1, "total_pages": 1, "total_results": 1, "results": []any{medianameDetails(123, "Example", "movie")}})
		case "/3/search/tv":
			writeMedianameJSON(w, map[string]any{"page": 1, "total_pages": 1, "results": []any{}})
		case "/3/movie/123":
			writeMedianameJSON(w, medianameDetails(123, "Example", "movie"))
		default:
			t.Errorf("unexpected search request: %s", r.URL.Path)
		}
	})
	cfg := DefaultMedianameConfig()
	cfg.CategoryYAML = "movie:\n  medianame电影:\ntv:\n  medianame剧集:\n"
	if _, err := local.SaveMedianameConfig(cfg); err != nil {
		t.Fatal(err)
	}
	results, err := local.SearchMedia(context.Background(), "Example", 8)
	if err != nil || len(results) != 1 || results[0].Category != "medianame电影" || results[0].TmdbID != "123" {
		t.Fatalf("search did not use medianame categories: %+v, %v", results, err)
	}
}

func TestLegacyAutomationRecognitionPreservesGraphAndUsesBuiltinEngine(t *testing.T) {
	raw := `{"schema_version":1,"nodes":[{"id":"title","type":"moviepilot_title_recognize","config":{"input":"$item.title","recognition_source":"moviepilot"},"parameter_bindings":{"tmdb_id":"id"}},{"id":"files","type":"moviepilot_recognize","config":{"recognition_source":"shadow"}}],"edges":[{"id":"e","source":"title","source_port":"success","target":"files"}]}`
	definition, err := ParseAutomationDefinition(raw)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Nodes[0].Type != AutomationNodeMediaTitleRecognize || definition.Nodes[1].Type != AutomationNodeMediaFileRecognize || definition.Nodes[0].Config["recognition_source"] != "medianame" || definition.Nodes[1].Config["recognition_source"] != "medianame" || definition.Nodes[0].ParameterBindings["tmdb_id"] != "id" || definition.Edges[0].Target != "files" {
		t.Fatalf("legacy graph changed: %+v", definition)
	}
	encoded, err := json.Marshal(definition)
	if err != nil || strings.Contains(string(encoded), "moviepilot") || strings.Contains(string(encoded), "shadow") {
		t.Fatalf("legacy types serialized: %s %v", encoded, err)
	}
}
