package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestExtractOrganizePreviewTmdbRefsDeduplicatesByMediaTypeAndID(t *testing.T) {
	result := Organize115CookieResult{
		Items: []Organize115ItemResult{
			{TmdbID: "100", MediaType: "tv", Title: "Show", Year: "2024", TargetSeason: 1, TargetEpisode: 1},
			{TmdbID: "100", MediaType: "series", Title: "Show", Year: "2024", TargetSeason: 1, TargetEpisode: 2},
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
		{TmdbID: "100", MediaType: "tv", Title: "Show", Year: "2024", EpisodeCount: 2},
		{TmdbID: "100", MediaType: "movie", Title: "Movie", Year: "2020"},
		{TmdbID: "200", MediaType: "movie", Title: "Second Movie", Year: "2021"},
	}
	if len(refs) != len(want) {
		t.Fatalf("len(refs)=%d want=%d refs=%+v", len(refs), len(want), refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("refs[%d]=%+v want=%+v", i, refs[i], want[i])
		}
	}
}

func TestExtractOrganizePreviewTmdbRefsCountsDistinctEpisodes(t *testing.T) {
	result := Organize115CookieResult{
		Items: []Organize115ItemResult{
			{TmdbID: "500", MediaType: "tv", SourceSeason: 1, SourceEpisode: 1},
			{TmdbID: "500", MediaType: "tv", TargetSeason: 1, TargetEpisode: 1},
			{TmdbID: "500", MediaType: "tv", TargetSeason: 1, TargetEpisode: 2},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	refs := extractOrganizePreviewTmdbRefs(model.OrganizePreviewTask{ResultJSON: string(raw)})
	if len(refs) != 1 || refs[0].EpisodeCount != 2 {
		t.Fatalf("unexpected episode count refs=%+v", refs)
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
		Items: []Organize115ItemResult{{TmdbID: "400", MediaType: "movie"}},
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
	if strings.Contains(encoded, "result_json") {
		t.Fatalf("list payload leaked result_json: %s", encoded)
	}
}
