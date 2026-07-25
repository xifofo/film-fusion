package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"
	"film-fusion/app/service"
)

func TestExtractOrganizePreviewTmdbRefsDeduplicatesSeasonsByMediaTypeAndID(t *testing.T) {
	result := Organize115CookieResult{
		Items: []Organize115ItemResult{
			{TmdbID: "100", MediaType: "tv", Title: "Show", Year: "2024", SourceSeason: 2},
			{TmdbID: "100", MediaType: "series", Title: "Show", Year: "2024", SourceSeason: 2},
			{TmdbID: "100", MediaType: "tv", Title: "Show", Year: "2024", TargetSeason: 1},
			{TmdbID: "100", MediaType: "movie", Title: "Movie", Year: "2020"},
			{TmdbID: " 200 ", Category: "movie", Title: "Second Movie", Year: "2021"},
			{MediaType: "movie", Title: "Missing ID"},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	refs := extractOrganizePreviewTmdbRefs(model.OrganizePreviewTask{ResultJSON: string(raw)})
	want := []OrganizePreviewTmdbRef{
		{
			TmdbID:    "100",
			MediaType: "tv",
			Title:     "Show",
			Year:      "2024",
			Seasons: []OrganizePreviewTmdbSeason{
				{SeasonNumber: 1},
				{SeasonNumber: 2},
			},
		},
		{TmdbID: "100", MediaType: "movie", Title: "Movie", Year: "2020"},
		{TmdbID: "200", MediaType: "movie", Title: "Second Movie", Year: "2021"},
	}
	if len(refs) != len(want) {
		t.Fatalf("len(refs)=%d want=%d refs=%+v", len(refs), len(want), refs)
	}
	for i := range want {
		if refs[i].TmdbID != want[i].TmdbID || refs[i].MediaType != want[i].MediaType || refs[i].Title != want[i].Title || refs[i].Year != want[i].Year {
			t.Fatalf("refs[%d]=%+v want=%+v", i, refs[i], want[i])
		}
		if len(refs[i].Seasons) != len(want[i].Seasons) {
			t.Fatalf("refs[%d].Seasons=%+v want=%+v", i, refs[i].Seasons, want[i].Seasons)
		}
		for seasonIndex := range want[i].Seasons {
			if refs[i].Seasons[seasonIndex] != want[i].Seasons[seasonIndex] {
				t.Fatalf("refs[%d].Seasons[%d]=%+v want=%+v", i, seasonIndex, refs[i].Seasons[seasonIndex], want[i].Seasons[seasonIndex])
			}
		}
	}
}

func TestExtractOrganizePreviewTmdbRefsUsesTaskFallbackAndIgnoresInvalidJSON(t *testing.T) {
	result := Organize115CookieResult{
		Items: []Organize115ItemResult{{TmdbID: "300", Title: "Fallback Show"}},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	refs := extractOrganizePreviewTmdbRefs(model.OrganizePreviewTask{
		MediaType:  "tv",
		ResultJSON: string(raw),
	})
	if len(refs) != 1 || refs[0].MediaType != "tv" || refs[0].TmdbID != "300" {
		t.Fatalf("unexpected fallback refs: %+v", refs)
	}
	if refs := extractOrganizePreviewTmdbRefs(model.OrganizePreviewTask{ResultJSON: "{"}); len(refs) != 0 {
		t.Fatalf("invalid JSON refs=%+v want empty", refs)
	}
}

func TestExtractOrganizePreviewTmdbRefsUsesPersistedEpisodeCounts(t *testing.T) {
	episodeCount := 12
	result := Organize115CookieResult{
		TmdbRefs: []OrganizePreviewTmdbRef{{
			TmdbID:    "300",
			MediaType: "tv",
			Seasons: []OrganizePreviewTmdbSeason{{
				SeasonNumber: 2,
				EpisodeCount: &episodeCount,
			}},
		}},
		Items: []Organize115ItemResult{{
			TmdbID:       "300",
			MediaType:    "tv",
			SourceSeason: 2,
		}},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	refs := extractOrganizePreviewTmdbRefs(model.OrganizePreviewTask{ResultJSON: string(raw)})
	if len(refs) != 1 || len(refs[0].Seasons) != 1 || refs[0].Seasons[0].EpisodeCount == nil {
		t.Fatalf("persisted refs missing: %+v", refs)
	}
	if got := *refs[0].Seasons[0].EpisodeCount; got != episodeCount {
		t.Fatalf("episode count=%d want=%d", got, episodeCount)
	}
}

func TestPopulateOrganizePreviewTmdbEpisodeCounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/300/season/2" {
			t.Errorf("unexpected TMDB path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"episodes":[{}, {}, {}]}`))
	}))
	defer server.Close()

	h := &OrganizeHandler{
		tmdbSvc: service.NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
			Enabled:        true,
			BaseURL:        server.URL,
			APIKey:         "test-key",
			TimeoutSeconds: 1,
		}}, nil),
	}
	refs := []OrganizePreviewTmdbRef{{
		TmdbID:    "300",
		MediaType: "tv",
		Seasons:   []OrganizePreviewTmdbSeason{{SeasonNumber: 2}},
	}}

	h.populateOrganizePreviewTmdbEpisodeCounts(context.Background(), refs)
	if refs[0].Seasons[0].EpisodeCount == nil || *refs[0].Seasons[0].EpisodeCount != 3 {
		t.Fatalf("episode count not populated: %+v", refs)
	}
}

func TestBuildOrganizePreviewTaskListItemsExposesRefsWithoutResultJSON(t *testing.T) {
	result := Organize115CookieResult{
		Items: []Organize115ItemResult{{
			TmdbID:        "400",
			MediaType:     "tv",
			RenameTo:      "Show.S01E01-02.1080p.mkv",
			SourceSeason:  1,
			SourceEpisode: 1,
			LocalExists:   true,
		}},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	items := buildOrganizePreviewTaskListItems([]model.OrganizePreviewTask{{
		ID:         9,
		Status:     model.OrganizePreviewStatusCompleted,
		ResultJSON: string(raw),
	}})
	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal list items: %v", err)
	}
	encoded := string(payload)
	if !strings.Contains(encoded, `"id":9`) || !strings.Contains(encoded, `"tmdb_id":"400"`) {
		t.Fatalf("list payload missing task or TMDB ref: %s", encoded)
	}
	if !strings.Contains(encoded, `"multi_episode_count":1`) || !strings.Contains(encoded, `"multi_episode_examples":["S01E01-02"]`) {
		t.Fatalf("list payload missing multi-episode summary: %s", encoded)
	}
	if !strings.Contains(encoded, `"all_episodes_exist":true`) {
		t.Fatalf("list payload missing all-episodes-exist summary: %s", encoded)
	}
	if strings.Contains(encoded, "result_json") {
		t.Fatalf("list payload leaked result_json: %s", encoded)
	}
}

func TestExtractOrganizePreviewAllEpisodesExist(t *testing.T) {
	completedTask := func(items []Organize115ItemResult) model.OrganizePreviewTask {
		raw, err := json.Marshal(Organize115CookieResult{
			MediaType: "tv",
			Items:     items,
		})
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		return model.OrganizePreviewTask{
			Status:     model.OrganizePreviewStatusCompleted,
			ResultJSON: string(raw),
		}
	}

	allExisting := completedTask([]Organize115ItemResult{
		{MediaType: "tv", SourceSeason: 1, SourceEpisode: 1, LocalExists: true},
		{MediaType: "tv", SourceSeason: 1, SourceEpisode: 2, LocalExists: true},
	})
	if !extractOrganizePreviewAllEpisodesExist(allExisting) {
		t.Fatal("all existing episodes should be marked as a complete existing season")
	}

	testCases := map[string]model.OrganizePreviewTask{
		"one episode missing": completedTask([]Organize115ItemResult{
			{MediaType: "tv", SourceEpisode: 1, LocalExists: true},
			{MediaType: "tv", SourceEpisode: 2, LocalExists: false},
		}),
		"unrecognized episode": completedTask([]Organize115ItemResult{
			{MediaType: "tv", LocalExists: true},
		}),
		"item error": completedTask([]Organize115ItemResult{
			{MediaType: "tv", SourceEpisode: 1, LocalExists: true, Error: "识别失败"},
		}),
		"movie": completedTask([]Organize115ItemResult{
			{MediaType: "movie", LocalExists: true},
		}),
		"empty result": completedTask(nil),
		"failed task": {
			Status:     model.OrganizePreviewStatusFailed,
			ResultJSON: allExisting.ResultJSON,
		},
		"invalid json": {
			Status:     model.OrganizePreviewStatusCompleted,
			ResultJSON: "{",
		},
	}
	for name, task := range testCases {
		t.Run(name, func(t *testing.T) {
			if extractOrganizePreviewAllEpisodesExist(task) {
				t.Fatalf("%s should not be marked as a complete existing season", name)
			}
		})
	}
}

func TestExtractOrganizePreviewMultiEpisodes(t *testing.T) {
	result := Organize115CookieResult{
		Items: []Organize115ItemResult{
			{RenameTo: "Show.2024.S01E01-02.1080p.mkv"},
			{RenameTo: "Show.2024.S01E03-E04.1080p.mkv"},
			{RenameTo: "Show.2024.s01e05e06.1080p.mkv"},
			{RenameTo: "Show.2024.S01E07.1080p.mkv"},
			{RenameTo: "Show.2024.S01E08-1080p.mkv"},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	count, examples := extractOrganizePreviewMultiEpisodes(model.OrganizePreviewTask{ResultJSON: string(raw)})
	if count != 3 {
		t.Fatalf("count=%d want=3 examples=%v", count, examples)
	}
	wantExamples := []string{"S01E01-02", "S01E03-E04", "S01E05E06"}
	if len(examples) != len(wantExamples) {
		t.Fatalf("examples=%v want=%v", examples, wantExamples)
	}
	for i := range wantExamples {
		if examples[i] != wantExamples[i] {
			t.Fatalf("examples[%d]=%q want=%q", i, examples[i], wantExamples[i])
		}
	}
}
