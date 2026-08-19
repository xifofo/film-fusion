package service

import (
	"context"
	"strings"
	"testing"

	"film-fusion/app/model"
)

type recordedRSSAutomationLocalRecognitionCall struct {
	input   string
	options MediaRecognitionOptions
}

type recordingRSSAutomationLocalRecognizer struct {
	calls []recordedRSSAutomationLocalRecognitionCall
	fn    func(string, MediaRecognitionOptions) (MediaRecognitionResult, error)
}

func (r *recordingRSSAutomationLocalRecognizer) Recognize(
	_ context.Context,
	input string,
	options MediaRecognitionOptions,
) (MediaRecognitionResult, error) {
	r.calls = append(r.calls, recordedRSSAutomationLocalRecognitionCall{input: input, options: options})
	if r.fn != nil {
		return r.fn(input, options)
	}
	return MediaRecognitionResult{}, nil
}

func TestRSSAutomationFilmFusionTitleRecognitionUsesLocalEngineAndSavedWords(t *testing.T) {
	recognizer := &recordingRSSAutomationLocalRecognizer{fn: func(input string, options MediaRecognitionOptions) (MediaRecognitionResult, error) {
		return MediaRecognitionResult{
			Engine: "local", Mode: options.Mode, TMDBStatus: "matched",
			WordResult: MediaRecognitionWordResult{Original: input, Processed: "示例剧.S01E01.2160p", AppliedWords: []string{"广告 => "}},
			MetaInfo: MediaRecognitionMetaInfo{
				OriginalInput: input, ProcessedInput: "示例剧.S01E01.2160p", Name: "示例剧",
				MediaType: "tv", TMDBID: "1396", SeasonEpisode: "S01E01", ResourcePix: "2160p",
				AppliedWords: []string{"广告 => "},
			},
			MediaInfo: MediaRecognitionMediaInfo{
				Source: "tmdb", MediaType: "tv", Title: "示例剧", Year: "2026", TMDBID: "1396",
				Category: "国产剧", PosterPath: "/poster.jpg", Rating: 8.6,
			},
			Candidates: []MediaRecognitionCandidate{}, Raw: map[string]any{"engine": "local"},
		}, nil
	}}
	automation := &RSSAutomationService{localMedia: recognizer}
	output, err := automation.executeRSSAutomationFilmFusionRecognize(
		context.Background(),
		RSSAutomationNode{ID: "local", Type: RSSAutomationNodeFilmFusionRecognize, Config: map[string]any{
			"recognition_mode": "title", "input": "$item.title", "tmdb_id": "1396", "lookup_tmdb": true,
		}},
		RSSAutomationDefinition{},
		map[string]any{"item": map[string]any{"title": "广告.示例剧.S01E01.2160p"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recognizer.calls) != 1 || !strings.Contains(recognizer.calls[0].input, "{tmdb-1396}") {
		t.Fatalf("local recognition calls = %#v", recognizer.calls)
	}
	if recognizer.calls[0].options.Mode != MediaRecognitionModeTitle || !recognizer.calls[0].options.LookupTMDB || recognizer.calls[0].options.UseProvidedWords {
		t.Fatalf("local recognition options = %#v", recognizer.calls[0].options)
	}
	if output["selected_port"] != "success" || output["engine"] != "local" || output["title"] != "示例剧" {
		t.Fatalf("unexpected local title output: %#v", output)
	}
	if output["processed_input"] != "示例剧.S01E01.2160p" || output["tmdb_id"] != "1396" || output["quality"] != "2160p" {
		t.Fatalf("local recognition details missing: %#v", output)
	}
}

func TestRSSAutomationFilmFusionFileRecognitionScansWait115Output(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	storage := model.CloudStorage{
		UserID: 1, StorageType: model.StorageType115Open, StorageName: "115",
		AccessToken: "token", Status: model.StatusActive,
	}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	recognizer := &recordingRSSAutomationLocalRecognizer{fn: func(input string, options MediaRecognitionOptions) (MediaRecognitionResult, error) {
		return MediaRecognitionResult{
			Engine: "local", Mode: options.Mode, TMDBStatus: "skipped",
			WordResult: MediaRecognitionWordResult{Original: input, Processed: input, AppliedWords: []string{}},
			MetaInfo: MediaRecognitionMetaInfo{
				OriginalInput: input, ProcessedInput: input, FileName: "S01E01.mkv", Name: "示例剧",
				MediaType: "tv", TMDBID: "12345", SeasonEpisode: "S01E01", AppliedWords: []string{},
			},
			MediaInfo:  MediaRecognitionMediaInfo{Source: "local", MediaType: "tv", Title: "示例剧", TMDBID: "12345"},
			Candidates: []MediaRecognitionCandidate{}, Raw: map[string]any{"engine": "local"},
		}, nil
	}}
	automation := &RSSAutomationService{
		db: db, localMedia: recognizer,
		cloud115: &stubRSSAutomation115Gateway{files: []rssAutomation115MediaFile{{
			FileID: "video-1", Name: "S01E01.mkv", Path: "示例剧/Season 01/S01E01.mkv", Size: 1024,
		}}},
	}
	definition := RSSAutomationDefinition{Edges: []RSSAutomationEdge{{Source: "wait", SourcePort: "success", Target: "local"}}}
	tasks := []any{map[string]any{
		"info_hash": "ABC", "name": "示例剧", "file_id": "folder-1", "status": 2, "percent": 100,
	}}
	runContext := rssAutomationTestRunContext("wait", map[string]any{
		"completed": true, "cloud_storage_id": storage.ID, "access_method": "openapi",
		"directory_id": "download-dir", "file_ids": []any{"folder-1"}, "file_names": []any{"示例剧"}, "tasks": tasks,
	})

	output, err := automation.executeRSSAutomationFilmFusionRecognize(
		context.Background(),
		RSSAutomationNode{ID: "local", Type: RSSAutomationNodeFilmFusionRecognize, Config: map[string]any{
			"recognition_mode": "file", "tmdb_id": "12345", "lookup_tmdb": false,
		}},
		definition, runContext,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recognizer.calls) != 1 || recognizer.calls[0].input != "示例剧/Season 01/S01E01.{tmdb-12345}.mkv" {
		t.Fatalf("local file recognition calls = %#v", recognizer.calls)
	}
	if recognizer.calls[0].options.Mode != MediaRecognitionModeFile || recognizer.calls[0].options.LookupTMDB {
		t.Fatalf("local file options = %#v", recognizer.calls[0].options)
	}
	if output["selected_port"] != "success" || output["engine"] != "local" || output["recognized_count"] != 1 || output["failed_count"] != 0 {
		t.Fatalf("unexpected local file output: %#v", output)
	}
	if output["directory_id"] != "download-dir" || output["tasks"] == nil || output["total_files"] != 1 || output["partial"] != false {
		t.Fatalf("wait115 context was not propagated: %#v", output)
	}
}
