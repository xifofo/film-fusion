package service

import (
	"context"
	"testing"

	"film-fusion/app/model"
)

type recordingRSSAutomationOrganizer struct {
	requests []RSSAutomationOrganizeRequest
}

func (r *recordingRSSAutomationOrganizer) OrganizeRSSAutomation(
	_ context.Context,
	request RSSAutomationOrganizeRequest,
) (map[string]any, error) {
	r.requests = append(r.requests, request)
	name := "folder"
	if len(request.FileIDs) > 0 {
		name = "direct"
	}
	return map[string]any{
		"total": 1,
		"items": []any{map[string]any{
			"file_id": name, "target_path": "/TV/Show/" + name + ".mkv",
			"strm_path": "/library/TV/Show/" + name + ".strm",
		}},
		"groups": []any{map[string]any{"folder_id": request.FolderIDs[0]}},
	}, nil
}

func TestRSSAutomationOrganizeStrmReusesCompletedDownloadFolders(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115",
		Cookie: "test-cookie", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{
		UserID: 1, CloudStorageID: storage.ID, DirectoryName: "影视中心",
		DirectoryID: "downloads", SavePath: "/library", ContentPrefix: "https://media.example/",
	}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	organizer := &recordingRSSAutomationOrganizer{}
	automation := &RSSAutomationService{db: db, organizer: organizer}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{{
		Source: "mp", SourcePort: "success", Target: "organize",
	}}}
	runContext := rssAutomationTestRunContext("mp", map[string]any{
		"cloud_storage_id": storage.ID,
		"directory_id":     "downloads",
		"tasks": []any{
			map[string]any{"name": "Show", "file_id": "folder-1", "directory_id": "downloads", "status": 2},
			map[string]any{"name": "Movie.mkv", "file_id": "file-2", "directory_id": "downloads", "status": 2},
		},
	})
	output, err := automation.executeRSSAutomationOrganizeStrm(
		context.Background(),
		RSSAutomationNode{ID: "organize", Type: RSSAutomationNodeOrganizeStrm, Config: map[string]any{
			"cloud_directory_id": directory.ID, "media_type": "tv",
			"best_version_enabled": true, "delete_source_folder": true,
		}},
		definition,
		runContext,
	)
	if err != nil {
		t.Fatal(err)
	}
	if output["selected_port"] != "success" || output["strm_count"] != 2 || output["organized_count"] != 2 {
		t.Fatalf("unexpected organize output: %#v", output)
	}
	if len(organizer.requests) != 2 {
		t.Fatalf("organize requests = %d, want folder and direct-file batches", len(organizer.requests))
	}
	if !organizer.requests[0].DeleteSourceFolder || organizer.requests[0].FolderIDs[0] != "folder-1" {
		t.Fatalf("folder request = %#v", organizer.requests[0])
	}
	if organizer.requests[1].DeleteSourceFolder || organizer.requests[1].FolderIDs[0] != "downloads" || organizer.requests[1].FileIDs[0] != "file-2" {
		t.Fatalf("direct-file request = %#v", organizer.requests[1])
	}
	if output["source_delete_skipped_for_direct_files"] != true {
		t.Fatalf("direct file delete safeguard missing: %#v", output)
	}
}

func TestRSSAutomationOrganizeStrmRejectsMismatchedStorage(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	if err := db.AutoMigrate(&model.CloudDirectory{}); err != nil {
		t.Fatal(err)
	}
	directory := model.CloudDirectory{
		UserID: 1, CloudStorageID: 99, DirectoryName: "影视中心", DirectoryID: "downloads", SavePath: "/library",
	}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	organizer := &recordingRSSAutomationOrganizer{}
	automation := &RSSAutomationService{db: db, organizer: organizer}
	_, err := automation.executeRSSAutomationOrganizeStrm(
		context.Background(),
		RSSAutomationNode{ID: "organize", Config: map[string]any{"cloud_directory_id": directory.ID}},
		RSSAutomationDefinition{Edges: []RSSAutomationEdge{{Source: "wait", Target: "organize"}}},
		rssAutomationTestRunContext("wait", map[string]any{
			"cloud_storage_id": 100,
			"tasks":            []any{map[string]any{"name": "Show", "file_id": "folder-1", "status": 2}},
		}),
	)
	if err == nil {
		t.Fatal("mismatched storage should be rejected")
	}
	if len(organizer.requests) != 0 {
		t.Fatal("organizer must not run for a mismatched account")
	}
}
