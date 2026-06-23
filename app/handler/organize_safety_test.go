package handler

import (
	"film-fusion/app/service"
	"testing"
)

func TestBuildRecognizeInputsUsesGrandparentWithProcessedName(t *testing.T) {
	inputs := buildRecognizeInputs(
		"发布组 - 第03集-1080p.mkv",
		"第03集",
		Organize115FolderContext{
			FolderID:   "100",
			FolderName: "Season 01",
			FolderPath: "国剧 / 漫长的季节 / Season 01",
		},
	)

	want := []string{
		"第03集",
		"漫长的季节/第03集",
		"漫长的季节/Season 01/第03集",
		"Season 01/第03集",
		"国剧/漫长的季节/Season 01/第03集",
		"发布组 - 第03集-1080p.mkv",
	}
	if len(inputs) != len(want) {
		t.Fatalf("len(inputs)=%d want=%d inputs=%v", len(inputs), len(want), inputs)
	}
	for i := range want {
		if inputs[i] != want[i] {
			t.Fatalf("inputs[%d]=%q want %q; all=%v", i, inputs[i], want[i], inputs)
		}
	}
}

func TestInferSourceSeasonEpisodeFromContextAndEpisodeOnlyName(t *testing.T) {
	season, episode := inferSourceSeasonEpisode(
		"发布组 - 第03集-1080p.mkv",
		"第03集",
		"漫长的季节/Season 01/第03集",
		Organize115FolderContext{FolderPath: "漫长的季节 / Season 01"},
		service.MoviePilotMediaInfo{MediaType: "tv"},
	)

	if season != 1 || episode != 3 {
		t.Fatalf("got S%02dE%02d, want S01E03", season, episode)
	}
}

func TestExtractEpisodeNumbers(t *testing.T) {
	cases := []struct {
		name    string
		season  int
		episode int
	}{
		{"Show.S02E08.1080p.mkv", 2, 8},
		{"Show.1x03.mkv", 1, 3},
		{"第 2 季 第 11 集.mkv", 2, 11},
		{"EP07.mkv", 0, 7},
	}

	for _, tc := range cases {
		season, episode, ok := extractEpisodeNumbers(tc.name)
		if !ok || season != tc.season || episode != tc.episode {
			t.Fatalf("%q got ok=%v S%02dE%02d, want S%02dE%02d", tc.name, ok, season, episode, tc.season, tc.episode)
		}
	}
}

func TestAnnotateOrganizeItemRisksMixedMedia(t *testing.T) {
	matched := true
	groups := []Organize115CookieGroup{
		{
			Items: []Organize115ItemResult{
				{
					FileID:         "1",
					MediaType:      "tv",
					TmdbID:         "100",
					SourceSeason:   1,
					SourceEpisode:  1,
					TargetSeason:   1,
					TargetEpisode:  1,
					EpisodeMatched: &matched,
				},
				{
					FileID:         "2",
					MediaType:      "tv",
					TmdbID:         "200",
					SourceSeason:   1,
					SourceEpisode:  2,
					TargetSeason:   1,
					TargetEpisode:  2,
					EpisodeMatched: &matched,
				},
			},
		},
	}

	items := annotateOrganizeItemRisks(groups)
	for _, item := range items {
		if item.RiskLevel != "high" {
			t.Fatalf("item %s risk=%q want high", item.FileID, item.RiskLevel)
		}
	}
}
