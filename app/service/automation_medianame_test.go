package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"
)

func TestAutomationMedianamePreservesCategoryAndSpecialEpisodes(t *testing.T) {
	local, cfg := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/3/tv/") {
			t.Errorf("unexpected fallback request %s", r.URL.Path)
		}
		writeMedianameJSON(w, medianameDetails(123, "Example", "tv"))
	})
	_, _ = local.SaveCategoryConfig("电影:\n  OLD电影:\n电视剧:\n  OLD剧集:\n")
	settings, _ := local.LoadMedianameConfig()
	settings.CategoryYAML = "movie:\n  NEW电影:\ntv:\n  NEW剧集:\n"
	if _, err := local.SaveMedianameConfig(settings); err != nil {
		t.Fatal(err)
	}
	cfg.MediaRecognition.Source = MediaRecognitionSourceMedianame
	mp := NewMoviePilotService(cfg, nil)
	mp.SetLocalMediaRecognition(local)
	automation := &AutomationService{moviePilot: mp, localMedia: local}
	output, err := automation.executeAutomationMoviePilotTitleRecognize(context.Background(), AutomationNode{Config: map[string]any{
		"input": "Example.2024.S00E01E03 [tmdb=123]",
	}}, nil)
	if err != nil || output["selected_port"] != "success" || output["category"] != "NEW剧集" || output["season_episode"] != "S00E01E03" || output["recognition_source"] != "medianame" {
		t.Fatalf("automation changed the selected branch: %#v, %v", output, err)
	}
	if output["recognition_raw"].(map[string]any)["medianame"] == nil {
		t.Fatal("missing complete evidence")
	}

	// The adapter pins the default before a later system change, and ParsePath
	// inherits the media ID and special season from directories for a bare file.
	adapter, err := freezeAutomationRecognizer(mp, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MediaRecognition.Source = MediaRecognitionSourceMoviePilot
	info, _, _, err := recognizeAutomationMediaFile(adapter, automation115MediaFile{Name: "01.mkv", Path: "/Example.2024 [tmdb=123]/Season 0/01.mkv"}, "")
	if err != nil || info.SeasonEpisode != "S00E01" || info.Category != "NEW剧集" {
		t.Fatalf("file recognition lost pinned mode or inherited fields: %+v %v", info, err)
	}
}

func TestAutomationMedianameNeverDropsConflictingIDsOrAcceptsRequestErrors(t *testing.T) {
	for _, input := range []string{"Example.2024 [tmdb=123] [imdb=tt9999999]", "Example.2024 [tmdb=123] [tvdb=99999]"} {
		t.Run(input, func(t *testing.T) {
			local, cfg := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/search/") {
					t.Error("conflicting ID input retried as a filename search")
				}
				writeMedianameJSON(w, medianameDetails(123, "Example", "movie"))
			})
			mp := NewMoviePilotService(cfg, nil)
			mp.SetLocalMediaRecognition(local)
			automation := &AutomationService{moviePilot: mp, localMedia: local}
			output, _ := automation.executeAutomationMoviePilotTitleRecognize(context.Background(), AutomationNode{Config: map[string]any{"input": input, "recognition_source": "medianame"}}, nil)
			if output["selected_port"] != "failure" || output["tmdb_id"] != "" {
				t.Fatalf("conflicting input adopted: %#v", output)
			}
			raw := output["recognition_raw"].(map[string]any)["medianame"].(MedianameRecognition)
			if raw.WordResult.Original != input {
				t.Fatal("automation rewrote the original conflicting IDs")
			}
			if raw.Result.Status != "review" {
				t.Fatalf("status = %s, want review", raw.Result.Status)
			}
		})
	}
	local, cfg := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	})
	mp := NewMoviePilotService(cfg, nil)
	mp.SetLocalMediaRecognition(local)
	adapter, _ := freezeAutomationRecognizer(mp, "medianame")
	info, _, _, err := recognizeAutomationMediaFile(adapter, automation115MediaFile{Path: "/Example.2024.[tmdb=123].mkv"}, "")
	if err == nil || info.TmdbID != "" || (*adapter.raw)["medianame"].(MedianameRecognition).Result.Status != "request_error" {
		t.Fatalf("request error accepted: %+v %v", info, err)
	}
}

func TestAutomationOrganizeKeepsUpstreamEngineAndPreviewMode(t *testing.T) {
	db := newAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	storage := model.CloudStorage{UserID: 1, StorageType: model.StorageType115Open, Cookie: "fake-cookie", Status: model.StatusActive}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{UserID: 1, CloudStorageID: storage.ID, DirectoryID: "library", RecognitionSource: "medianame"}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	organizer := &recordingAutomationOrganizer{}
	automation := &AutomationService{db: db, organizer: organizer, moviePilot: NewMoviePilotService(&config.Config{MediaRecognition: config.MediaRecognitionConfig{Source: "moviepilot"}}, nil)}
	definition := AutomationDefinition{Edges: []AutomationEdge{{Source: "recognize", SourcePort: "success", Target: "organize"}}}
	upstream := map[string]any{"cloud_storage_id": storage.ID, "recognition_source": "medianame", "tasks": []any{map[string]any{"name": "Show", "file_id": "folder-1", "status": 2}}}
	node := AutomationNode{ID: "organize", Config: map[string]any{"cloud_directory_id": directory.ID, "dry_run": true}}
	_, err := automation.executeAutomationOrganizeStrm(context.Background(), node, definition, automationTestRunContext("recognize", upstream))
	if err != nil || len(organizer.requests) != 1 || organizer.requests[0].RecognitionSource != "medianame" || !organizer.requests[0].DryRun {
		t.Fatalf("upstream engine or preview mode lost: %+v %v", organizer.requests, err)
	}
	node.Config["recognition_source"] = "moviepilot"
	_, err = automation.executeAutomationOrganizeStrm(context.Background(), node, definition, automationTestRunContext("recognize", upstream))
	if err == nil || len(organizer.requests) != 1 {
		t.Fatal("mixed engine task was allowed")
	}
}
