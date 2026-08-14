package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"film-fusion/app/model"
)

type stubRSSAutomation115Gateway struct {
	tasks    []rssAutomation115Task
	files    []rssAutomation115MediaFile
	tasksErr error
	filesErr error
}

func (g *stubRSSAutomation115Gateway) ListOfflineTasks(_ context.Context, _ model.CloudStorage, _ string) ([]rssAutomation115Task, error) {
	return g.tasks, g.tasksErr
}

func (g *stubRSSAutomation115Gateway) ListMediaFiles(_ context.Context, _ model.CloudStorage, _ string, _ rssAutomation115Task) ([]rssAutomation115MediaFile, error) {
	return g.files, g.filesErr
}

type recordingRSSAutomationMoviePilot struct {
	calls []string
	fn    func(string) (MoviePilotMediaInfo, error)
}

func (r *recordingRSSAutomationMoviePilot) RecognizeFile(input string) (MoviePilotMediaInfo, map[string]any, error) {
	r.calls = append(r.calls, input)
	if r.fn == nil {
		return MoviePilotMediaInfo{}, nil, nil
	}
	info, err := r.fn(input)
	return info, nil, err
}

func (r *recordingRSSAutomationMoviePilot) RecognizeTitle(input string) (MoviePilotMediaInfo, map[string]any, error) {
	return r.RecognizeFile(input)
}

func (r *recordingRSSAutomationMoviePilot) SearchMedia(_ string, _ int) ([]MoviePilotSearchResult, error) {
	return nil, nil
}

func TestRSSAutomationWait115CompletesOnlyAfterTaskHasFileID(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{
		db: db,
		cloud115: &stubRSSAutomation115Gateway{tasks: []rssAutomation115Task{{
			InfoHash: "ABC", Name: "Show.S01E01.mkv", FileID: "file-1",
			DirectoryID: "dir-1", Status: 2, Percent: 100,
		}}},
	}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{{Source: "offline", SourcePort: "success", Target: "wait"}}}
	runContext := rssAutomationTestRunContext("offline", map[string]any{
		"cloud_storage_id": storage.ID, "access_method": "openapi", "directory_id": "dir-1",
		"hashes": []any{"ABC"},
	})

	output, err := automation.executeRSSAutomationWait115(
		context.Background(), model.RSSAutomationNodeRun{},
		RSSAutomationNode{ID: "wait", Type: RSSAutomationNodeWait115}, definition, runContext,
	)
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "success" || output["completed"] != true || output["file_id"] != "file-1" {
		t.Fatalf("unexpected wait output: %#v", output)
	}
}

func TestRSSAutomationWait115DefersWithoutConsumingRetryAttempt(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{
		db: db, ctx: context.Background(),
		cloud115: &stubRSSAutomation115Gateway{tasks: []rssAutomation115Task{{
			InfoHash: "ABC", Name: "Show", Status: 1, Percent: 42,
		}}},
	}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{{Source: "offline", SourcePort: "success", Target: "wait"}}}
	runContext := rssAutomationTestRunContext("offline", map[string]any{
		"cloud_storage_id": storage.ID, "access_method": "openapi", "hashes": []any{"ABC"},
	})
	node := RSSAutomationNode{ID: "wait", Type: RSSAutomationNodeWait115, Config: map[string]any{"poll_interval_seconds": 5}}
	output, executeErr := automation.executeRSSAutomationWait115(
		context.Background(), model.RSSAutomationNodeRun{}, node, definition, runContext,
	)
	var deferred *rssAutomationNodeDeferred
	if !errors.As(executeErr, &deferred) || output["waiting"] != true {
		t.Fatalf("wait result = %#v, %v", output, executeErr)
	}

	nodeRun := model.RSSAutomationNodeRun{
		RunID: 1, NodeID: "wait", NodeType: RSSAutomationNodeWait115,
		Status: model.RSSAutomationNodeRunning, Attempt: 1, MaxAttempts: 3,
	}
	if err := db.Create(&nodeRun).Error; err != nil {
		t.Fatal(err)
	}
	automation.completeRSSAutomationNode(nodeRun, output, executeErr)
	var stored model.RSSAutomationNodeRun
	if err := db.First(&stored, nodeRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.RSSAutomationNodePending || stored.Attempt != 0 || stored.NextAttemptAt == nil {
		t.Fatalf("deferred node run = %#v", stored)
	}
	if !strings.Contains(stored.OutputJSON, `"waiting":true`) || !strings.Contains(stored.ErrorMessage, "42%") {
		t.Fatalf("deferred state was not persisted: %#v", stored)
	}
	if stored.NextAttemptAt.Before(time.Now().Add(3 * time.Second)) {
		t.Fatalf("next poll scheduled too early: %v", stored.NextAttemptAt)
	}
}

func TestRSSAutomationMoviePilotRecognitionUsesVirtualTMDBMarker(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	recognizer := &recordingRSSAutomationMoviePilot{fn: func(input string) (MoviePilotMediaInfo, error) {
		if !strings.Contains(input, "S01E01.{tmdb-12345}.mkv") {
			return MoviePilotMediaInfo{}, nil
		}
		return MoviePilotMediaInfo{
			TmdbID: "12345", MediaType: "tv", Title: "示例剧", Year: "2026",
			SeasonEpisode: "S01E01", Category: "国产剧集",
		}, nil
	}}
	automation := &RSSAutomationService{
		db: db, moviePilot: recognizer,
		cloud115: &stubRSSAutomation115Gateway{files: []rssAutomation115MediaFile{{
			FileID: "video-1", Name: "S01E01.mkv", Path: "示例剧/Season 01/S01E01.mkv",
		}}},
	}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{{Source: "wait", SourcePort: "success", Target: "mp"}}}
	runContext := rssAutomationTestRunContext("wait", map[string]any{
		"completed": true, "cloud_storage_id": storage.ID, "access_method": "openapi",
		"tasks": []any{map[string]any{
			"info_hash": "ABC", "name": "示例剧", "file_id": "folder-1", "status": 2, "percent": 100,
		}},
	})

	output, err := automation.executeRSSAutomationMoviePilotRecognize(
		context.Background(),
		RSSAutomationNode{ID: "mp", Type: RSSAutomationNodeMoviePilotRecognize, Config: map[string]any{"tmdb_id": "12345"}},
		definition, runContext,
	)
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "success" || output["tmdb_id"] != "12345" || output["recognized_count"] != 1 {
		t.Fatalf("unexpected MP output: %#v", output)
	}
	if output["requested_tmdb_id"] != "12345" || output["total_files"] != 1 || output["failed_count"] != 0 || output["partial"] != false {
		t.Fatalf("unexpected MP variable protocol output: %#v", output)
	}
	if len(recognizer.calls) == 0 || recognizer.calls[0] != "示例剧/Season 01/S01E01.{tmdb-12345}.mkv" {
		t.Fatalf("recognition calls = %#v", recognizer.calls)
	}
}

func TestRSSAutomationFileNameWithTMDBReplacesExistingMarker(t *testing.T) {
	if got := rssAutomationFileNameWithTMDB("Show.S01E01.{tmdbid-100}.mkv", "200"); got != "Show.S01E01.{tmdb-200}.mkv" {
		t.Fatalf("marked filename = %q", got)
	}
}

func rssAutomationTestRunContext(nodeID string, output map[string]any) map[string]any {
	return map[string]any{
		"item": map[string]any{}, "vars": map[string]any{},
		"nodes": map[string]any{nodeID: map[string]any{"status": model.RSSAutomationNodeSucceeded, "output": output}},
	}
}
