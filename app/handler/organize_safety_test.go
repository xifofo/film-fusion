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

func TestBuildRecognizeInputsUsesDefaultRegexFallbackBeforeGrandparent(t *testing.T) {
	inputs := buildRecognizeInputs(
		"发布组 - 流浪地球-2160p.mkv",
		"发布组 - 流浪地球-2160p.mkv",
		Organize115FolderContext{
			FolderID:   "100",
			FolderName: "版本合集",
			FolderPath: "电影 / 流浪地球 / 版本合集",
		},
	)

	wantPrefix := []string{
		"发布组 - 流浪地球-2160p.mkv",
		"流浪地球",
		"流浪地球/发布组 - 流浪地球-2160p.mkv",
		"流浪地球/版本合集/发布组 - 流浪地球-2160p.mkv",
		"版本合集/发布组 - 流浪地球-2160p.mkv",
		"电影/流浪地球/版本合集/发布组 - 流浪地球-2160p.mkv",
		"流浪地球/版本合集/流浪地球",
	}
	if len(inputs) < len(wantPrefix) {
		t.Fatalf("len(inputs)=%d want at least %d inputs=%v", len(inputs), len(wantPrefix), inputs)
	}
	for i := range wantPrefix {
		if inputs[i] != wantPrefix[i] {
			t.Fatalf("inputs[%d]=%q want %q; all=%v", i, inputs[i], wantPrefix[i], inputs)
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

	items := annotateOrganizeItemRisks(groups, organizeRiskOptions{expectedMediaType: "tv"})
	for _, item := range items {
		if item.RiskLevel != "high" {
			t.Fatalf("item %s risk=%q want high", item.FileID, item.RiskLevel)
		}
	}
}

func TestAnnotateOrganizeItemRisksExternalSubtitles(t *testing.T) {
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
					SubtitleFiles:  []string{"Show.S01E01.ass"},
				},
			},
		},
	}

	items := annotateOrganizeItemRisks(groups, organizeRiskOptions{expectedMediaType: "tv"})
	if got := items[0].RiskLevel; got != "high" {
		t.Fatalf("risk=%q want high", got)
	}
	if len(items[0].RiskReasons) == 0 {
		t.Fatalf("expected subtitle risk reason")
	}
}

func TestAnnotateOrganizeItemRisksNoRisk(t *testing.T) {
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
					LocalExists:    false,
				},
			},
		},
	}

	items := annotateOrganizeItemRisks(groups, organizeRiskOptions{expectedMediaType: "tv"})
	if got := items[0].RiskLevel; got != "none" {
		t.Fatalf("risk=%q want none", got)
	}
	if len(items[0].RiskReasons) != 0 {
		t.Fatalf("risk reasons=%v want empty", items[0].RiskReasons)
	}
}

func TestAnnotateOrganizeItemRisksIncompleteSeasonIsLow(t *testing.T) {
	matched := true
	groups := []Organize115CookieGroup{
		{
			Items: []Organize115ItemResult{
				{
					FileID:         "1",
					MediaType:      "tv",
					TmdbID:         "100",
					SourceEpisode:  1,
					TargetSeason:   1,
					TargetEpisode:  1,
					EpisodeMatched: &matched,
				},
			},
		},
	}

	items := annotateOrganizeItemRisks(groups, organizeRiskOptions{expectedMediaType: "tv"})
	if got := items[0].RiskLevel; got != "low" {
		t.Fatalf("risk=%q want low", got)
	}
}

func TestAnnotateOrganizeItemRisksMovieNoRisk(t *testing.T) {
	groups := []Organize115CookieGroup{
		{
			Items: []Organize115ItemResult{
				{
					FileID:    "1",
					FileName:  "Movie.2024.1080p.WEB-DL.mkv",
					MediaType: "movie",
					TmdbID:    "100",
				},
			},
		},
	}

	items := annotateOrganizeItemRisks(groups, organizeRiskOptions{expectedMediaType: "movie"})
	if got := items[0].RiskLevel; got != "none" {
		t.Fatalf("risk=%q want none", got)
	}
}

func TestAnnotateOrganizeItemRisksMovieBestVersion(t *testing.T) {
	groups := []Organize115CookieGroup{
		{
			Items: []Organize115ItemResult{
				{
					FileID:    "1080",
					FileName:  "Movie.2024.1080p.WEB-DL.H264.mkv",
					FileSize:  8 * 1024 * 1024 * 1024,
					MediaType: "movie",
					TmdbID:    "100",
				},
				{
					FileID:    "2160",
					FileName:  "Movie.2024.2160p.BluRay.REMUX.HEVC.TrueHD.Atmos.mkv",
					FileSize:  48 * 1024 * 1024 * 1024,
					MediaType: "movie",
					TmdbID:    "100",
				},
			},
		},
	}

	items := annotateOrganizeItemRisks(groups, organizeRiskOptions{
		expectedMediaType:  "movie",
		bestVersionEnabled: true,
	})
	byID := map[string]Organize115ItemResult{}
	for _, item := range items {
		byID[item.FileID] = item
	}
	if !byID["2160"].BestVersion || byID["2160"].RiskLevel != "none" {
		t.Fatalf("2160 item=%+v, want best no-risk", byID["2160"])
	}
	if !byID["1080"].AltVersion || byID["1080"].RiskLevel != "high" {
		t.Fatalf("1080 item=%+v, want alternate high-risk", byID["1080"])
	}
}

func TestAnnotateOrganizeItemRisksTVBestVersionPerEpisode(t *testing.T) {
	matched := true
	groups := []Organize115CookieGroup{
		{
			Items: []Organize115ItemResult{
				{
					FileID:         "e01-1080",
					FileName:       "Show.S01E01.1080p.WEB-DL.H264.mkv",
					FileSize:       6 * 1024 * 1024 * 1024,
					MediaType:      "tv",
					TmdbID:         "100",
					SourceSeason:   1,
					SourceEpisode:  1,
					TargetSeason:   1,
					TargetEpisode:  1,
					EpisodeMatched: &matched,
				},
				{
					FileID:         "e01-2160",
					FileName:       "Show.S01E01.2160p.WEB-DL.HEVC.Atmos.mkv",
					FileSize:       18 * 1024 * 1024 * 1024,
					MediaType:      "tv",
					TmdbID:         "100",
					SourceSeason:   1,
					SourceEpisode:  1,
					TargetSeason:   1,
					TargetEpisode:  1,
					EpisodeMatched: &matched,
				},
				{
					FileID:         "e02-1080",
					FileName:       "Show.S01E02.1080p.WEB-DL.H264.mkv",
					FileSize:       6 * 1024 * 1024 * 1024,
					MediaType:      "tv",
					TmdbID:         "100",
					SourceSeason:   1,
					SourceEpisode:  2,
					TargetSeason:   1,
					TargetEpisode:  2,
					EpisodeMatched: &matched,
				},
			},
		},
	}

	items := annotateOrganizeItemRisks(groups, organizeRiskOptions{
		expectedMediaType:  "tv",
		bestVersionEnabled: true,
	})
	byID := map[string]Organize115ItemResult{}
	for _, item := range items {
		byID[item.FileID] = item
	}
	if !byID["e01-2160"].BestVersion || byID["e01-2160"].RiskLevel != "none" {
		t.Fatalf("e01-2160 item=%+v, want best no-risk", byID["e01-2160"])
	}
	if !byID["e01-1080"].AltVersion || byID["e01-1080"].RiskLevel != "high" {
		t.Fatalf("e01-1080 item=%+v, want alternate high-risk", byID["e01-1080"])
	}
	if !byID["e02-1080"].BestVersion || byID["e02-1080"].RiskLevel != "none" {
		t.Fatalf("e02-1080 item=%+v, want independent best no-risk", byID["e02-1080"])
	}
}
