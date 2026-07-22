package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"film-fusion/app/model"
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

func TestBuildOrganizePreviewTaskListItemsExposesRefsWithoutResultJSON(t *testing.T) {
	result := Organize115CookieResult{
		Items: []Organize115ItemResult{{
			TmdbID:    "400",
			MediaType: "movie",
			RenameTo:  "Show.S01E01-02.1080p.mkv",
		}},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	items := buildOrganizePreviewTaskListItems([]model.OrganizePreviewTask{{
		ID:         9,
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
	if strings.Contains(encoded, "result_json") {
		t.Fatalf("list payload leaked result_json: %s", encoded)
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
