package handler

import (
	"strings"
	"testing"

	"film-fusion/app/service"
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

func TestFilenameProcessorNormalizesSpacedHyphenBeforeRecognition(t *testing.T) {
	processor, err := newFilenameRegexProcessor(false, "", "")
	if err != nil {
		t.Fatalf("newFilenameRegexProcessor() error = %v", err)
	}

	got := processor.apply("Show.S01E01 - Episode.Title.mkv")
	want := "Show.S01E01.Episode.Title.mkv"
	if got != want {
		t.Fatalf("processor.apply()=%q want %q", got, want)
	}
}

func TestCollectOrganizeSourceFolderDeleteTargetsSkipsErroredAndDuplicates(t *testing.T) {
	targets, errorsOut := collectOrganizeSourceFolderDeleteTargets([]Organize115CookieGroup{
		{FolderID: "100"},
		{FolderID: " 100 "},
		{FolderID: "200", Error: "移动文件失败"},
		{FolderID: "0"},
		{FolderID: "300"},
	})

	wantTargets := []string{"100", "300"}
	if len(targets) != len(wantTargets) {
		t.Fatalf("len(targets)=%d want=%d targets=%v", len(targets), len(wantTargets), targets)
	}
	for i := range wantTargets {
		if targets[i] != wantTargets[i] {
			t.Fatalf("targets[%d]=%q want %q; all=%v", i, targets[i], wantTargets[i], targets)
		}
	}
	if len(errorsOut) != 1 {
		t.Fatalf("len(errorsOut)=%d want=1 errors=%v", len(errorsOut), errorsOut)
	}
	if !strings.Contains(errorsOut[0], "200") || !strings.Contains(errorsOut[0], "移动文件失败") {
		t.Fatalf("unexpected error message: %q", errorsOut[0])
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

func TestAnnotateOrganizeItemsPreservesEpisodeFacts(t *testing.T) {
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

	items := annotateOrganizeItems(groups, organizeAnnotateOptions{})
	if len(items) != 1 {
		t.Fatalf("len(items)=%d want 1", len(items))
	}
	if items[0].SourceEpisode != 1 || items[0].TargetEpisode != 1 {
		t.Fatalf("item=%+v, want source and target episode preserved", items[0])
	}
	if items[0].EpisodeMatched == nil || !*items[0].EpisodeMatched {
		t.Fatalf("episode_matched=%v want true", items[0].EpisodeMatched)
	}
}

func TestSummarizeOrganizeFactsExternalSubtitles(t *testing.T) {
	groups := []Organize115CookieGroup{
		{
			Items: []Organize115ItemResult{
				{
					FileID:        "1",
					MediaType:     "tv",
					TmdbID:        "100",
					SubtitleFiles: []string{"Show.S01E01.ass"},
				},
			},
		},
	}

	items := annotateOrganizeItems(groups, organizeAnnotateOptions{})
	summary := summarizeOrganizeFacts(items)
	if summary.ExternalSubtitleCount != 1 {
		t.Fatalf("external subtitle count=%d want 1", summary.ExternalSubtitleCount)
	}
}

func TestAnnotateOrganizeItemsMovieBestVersion(t *testing.T) {
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

	items := annotateOrganizeItems(groups, organizeAnnotateOptions{
		bestVersionEnabled: true,
	})
	byID := map[string]Organize115ItemResult{}
	for _, item := range items {
		byID[item.FileID] = item
	}
	if !byID["2160"].BestVersion {
		t.Fatalf("2160 item=%+v, want best version", byID["2160"])
	}
	if !byID["1080"].AltVersion {
		t.Fatalf("1080 item=%+v, want alternate version", byID["1080"])
	}
	summary := summarizeOrganizeFacts(items)
	if summary.BestVersionCount != 1 || summary.AlternateVersionCount != 1 {
		t.Fatalf("summary=%+v, want one best and one alternate version", summary)
	}
}

func TestAnnotateOrganizeItemsTVBestVersionPerEpisode(t *testing.T) {
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

	items := annotateOrganizeItems(groups, organizeAnnotateOptions{
		bestVersionEnabled: true,
	})
	byID := map[string]Organize115ItemResult{}
	for _, item := range items {
		byID[item.FileID] = item
	}
	if !byID["e01-2160"].BestVersion {
		t.Fatalf("e01-2160 item=%+v, want best version", byID["e01-2160"])
	}
	if !byID["e01-1080"].AltVersion {
		t.Fatalf("e01-1080 item=%+v, want alternate version", byID["e01-1080"])
	}
	if !byID["e02-1080"].BestVersion {
		t.Fatalf("e02-1080 item=%+v, want independent best version", byID["e02-1080"])
	}
}
