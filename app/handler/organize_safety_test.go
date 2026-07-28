package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"
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

func TestStripDuplicateMovieTitleYearPrefix(t *testing.T) {
	input := "黑衣人3 (2012) - Men.in.Black.III.2012.BluRay.2160p.Atmos.TrueHD7.1.x265.10bit-DreamHD.mkv"
	want := "Men.in.Black.III.2012.BluRay.2160p.Atmos.TrueHD7.1.x265.10bit-DreamHD.mkv"
	if got := stripDuplicateMovieTitleYearPrefix(input); got != want {
		t.Fatalf("stripped movie name=%q want=%q", got, want)
	}

	withoutRepeatedYear := "黑衣人3 (2012) - BluRay.2160p.mkv"
	if got := stripDuplicateMovieTitleYearPrefix(withoutRepeatedYear); got != withoutRepeatedYear {
		t.Fatalf("name without a second year should stay unchanged: got=%q", got)
	}
}

func TestDuplicateMovieTitleYearPrefixOnlyAppliesToMovies(t *testing.T) {
	if !shouldStripDuplicateMovieTitleYearPrefix("movie", "", "") {
		t.Fatal("explicit movie mode should strip the duplicate prefix")
	}
	if shouldStripDuplicateMovieTitleYearPrefix("tv", "movie", "欧美电影") {
		t.Fatal("explicit TV mode must not strip the duplicate prefix")
	}
	if !shouldStripDuplicateMovieTitleYearPrefix("", "movie", "欧美电影") {
		t.Fatal("auto mode should strip after recognizing a movie")
	}
	if shouldStripDuplicateMovieTitleYearPrefix("", "tv", "欧美剧集") {
		t.Fatal("auto mode must not strip a recognized TV item")
	}
}

func TestBuildMovieTransferInputAddsRecognitionAnchors(t *testing.T) {
	info := service.MoviePilotMediaInfo{
		MediaType: "movie",
		Year:      "2023",
		TmdbID:    "614479",
	}
	input := "Insidious The Red Door 2023 BluRay REMUX 1080p AVC DTS-HD MA5.1-DreamHD.mkv"
	want := "Insidious The Red Door (2023).BluRay.REMUX.1080p.AVC.DTS-HD.MA.5.1-DreamHD.{tmdb-614479}.mkv"
	if got := buildMovieTransferInput(input, info); got != want {
		t.Fatalf("movie transfer input=%q want=%q", got, want)
	}

	anchored := "Insidious： The Red Door (2023) 1080p.Bluray.REMUX.AVC.DTS-HD.MA.5.1.{tmdb-614479}.mkv"
	anchoredWant := "Insidious： The Red Door (2023).1080p.Bluray.REMUX.AVC.DTS-HD.MA.5.1.{tmdb-614479}.mkv"
	if got := buildMovieTransferInput(anchored, info); got != anchoredWant {
		t.Fatalf("anchored movie transfer input=%q want=%q", got, anchoredWant)
	}
}

func TestDedupeConsecutiveTransferTagsOnlyRemovesRepeatedBitDepth(t *testing.T) {
	got := dedupeConsecutiveTransferTags("A Hard Day.2014.BluRay.1080p.x265 10bit.10bit.mkv")
	want := "A Hard Day.2014.BluRay.1080p.x265 10bit.mkv"
	if got != want {
		t.Fatalf("deduped transfer name=%q want %q", got, want)
	}

	unchanged := "Movie.2014.2014.10bit.HDR.10bit.mkv"
	if got := dedupeConsecutiveTransferTags(unchanged); got != unchanged {
		t.Fatalf("non-consecutive or non-technical tokens changed: got=%q want=%q", got, unchanged)
	}
}

func TestEnhanceEpisodeRecognizeInputWithTMDBEnglish(t *testing.T) {
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/123" {
			t.Fatalf("unexpected TMDB path=%s", r.URL.Path)
		}
		if got := r.URL.Query().Get("language"); got != "en-US" {
			t.Fatalf("TMDB language=%q want en-US", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"The English Show","original_name":"原始剧名"}`))
	}))
	defer tmdbServer.Close()

	var moviePilotInput string
	moviePilotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize_file":
			moviePilotInput = r.URL.Query().Get("path")
			_, _ = w.Write([]byte(`{
				"success": true,
				"data": {
					"media_info": {"media_type":"tv","title":"中文剧名","tmdb_id":"123"},
					"meta_info": {"season_episode":"S01E02","resource_pix":"2160p","resource_type":"WEB-DL","video_encode":"H.265"}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer moviePilotServer.Close()

	h := &OrganizeHandler{
		tmdbSvc: service.NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
			Enabled:        true,
			BaseURL:        tmdbServer.URL,
			APIKey:         "test-key",
			TimeoutSeconds: 1,
		}}, nil),
		moviePilotSvc: service.NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{
			API:      moviePilotServer.URL,
			Username: "user",
			Password: "pass",
		}}, nil),
	}

	recognizeName := normalizeFilenameForRecognition("S01E02 - 2160p.WEB-DL High Quality.AAC.H.265.mkv")
	got := h.enhanceEpisodeRecognizeInputWithTMDBEnglish(
		service.MoviePilotMediaInfo{MediaType: "tv", TmdbID: "123"},
		"",
		recognizeName,
		"中文剧名/Season 01/"+recognizeName,
	)
	want := "The English Show.S01E02.2160p.WEB-DL High Quality.AAC.H.265.mkv"
	if got != want {
		t.Fatalf("enhanced input=%q want %q", got, want)
	}
	if moviePilotInput != want {
		t.Fatalf("MoviePilot input=%q want %q", moviePilotInput, want)
	}
}

func TestEnhanceEpisodeRecognizeInputRejectsDifferentTMDBResult(t *testing.T) {
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Ambiguous Show"}`))
	}))
	defer tmdbServer.Close()

	moviePilotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/login/access-token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"media_info":{"media_type":"tv","title":"Wrong Show","tmdb_id":"999"}}}`))
	}))
	defer moviePilotServer.Close()

	h := &OrganizeHandler{
		tmdbSvc: service.NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
			Enabled: true, BaseURL: tmdbServer.URL, APIKey: "test-key", TimeoutSeconds: 1,
		}}, nil),
		moviePilotSvc: service.NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{
			API: moviePilotServer.URL, Username: "user", Password: "pass",
		}}, nil),
	}

	current := "中文剧名/Season 01/S01E02.2160p.mkv"
	got := h.enhanceEpisodeRecognizeInputWithTMDBEnglish(
		service.MoviePilotMediaInfo{MediaType: "tv", TmdbID: "123"},
		"",
		"S01E02.2160p.mkv",
		current,
	)
	if got != current {
		t.Fatalf("mismatched TMDB input=%q want fallback %q", got, current)
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

func TestOrganizeVersionScoringExcludesSubtitlesAndDoesNotTreatDTSHDAsTS(t *testing.T) {
	groups := []Organize115CookieGroup{{
		Items: []Organize115ItemResult{
			{
				FileID:    "x265-video",
				FileName:  "A.Hard.Day.2014.BluRay.1080p.x265.10bit.mkv",
				FileSize:  4 * 1024 * 1024 * 1024,
				MediaType: "movie",
				TmdbID:    "269494",
			},
			{
				FileID:    "remux-video",
				FileName:  "A.Hard.Day.2014.1080p.BluRay.Remux.AVC.DTS-HD.MA5.1.mkv",
				FileSize:  50 * 1024 * 1024 * 1024,
				MediaType: "movie",
				TmdbID:    "269494",
			},
			{
				FileID:             "remux-subtitle",
				FileName:           "A.Hard.Day.2014.1080p.BluRay.Remux.AVC.DTS-HD.MA5.1.ass",
				FileSize:           1024 * 1024,
				IsSubtitle:         true,
				MatchedVideoFileID: "remux-video",
				MediaType:          "movie",
				TmdbID:             "269494",
			},
		},
	}}

	items := annotateOrganizeItems(groups, organizeAnnotateOptions{bestVersionEnabled: true})
	byID := make(map[string]Organize115ItemResult, len(items))
	for _, item := range items {
		byID[item.FileID] = item
	}

	remux := byID["remux-video"]
	if !remux.BestVersion {
		t.Fatalf("remux item=%+v, want best version", remux)
	}
	for _, reason := range remux.VersionReasons {
		if strings.Contains(reason, "低质片源") {
			t.Fatalf("DTS-HD was treated as telesync: reasons=%v", remux.VersionReasons)
		}
	}
	subtitle := byID["remux-subtitle"]
	if subtitle.BestVersion || subtitle.AltVersion || subtitle.VersionKey != "" || subtitle.VersionScore != 0 {
		t.Fatalf("subtitle participated in version scoring: %+v", subtitle)
	}

	versionGroups := buildOrganizeVersionGroups(items)
	if len(versionGroups) != 2 {
		t.Fatalf("len(versionGroups)=%d want 2 groups=%+v", len(versionGroups), versionGroups)
	}
	totalVersionFiles := 0
	for _, group := range versionGroups {
		totalVersionFiles += group.FileCount
		for _, fileID := range group.FileIDs {
			if fileID == "remux-subtitle" {
				t.Fatalf("subtitle included in version group: %+v", group)
			}
		}
	}
	if totalVersionFiles != 2 {
		t.Fatalf("version file count=%d want 2 groups=%+v", totalVersionFiles, versionGroups)
	}
	if !versionGroups[0].Recommended || !strings.Contains(versionGroups[0].Label, "Remux") {
		t.Fatalf("recommended group=%+v, want Remux", versionGroups[0])
	}

	_, tsReasons := scoreMediaVersion(Organize115ItemResult{
		FileName:  "Movie.2024.1080p.TS-GROUP.mkv",
		MediaType: "movie",
		TmdbID:    "1",
	})
	foundLowQuality := false
	for _, reason := range tsReasons {
		foundLowQuality = foundLowQuality || strings.Contains(reason, "低质片源")
	}
	if !foundLowQuality {
		t.Fatalf("real TS release was not marked low quality: reasons=%v", tsReasons)
	}
}

func TestAttachOrganizeSubtitlesMatchesOnlyItsVideoVersion(t *testing.T) {
	items := []Organize115ItemResult{
		{
			FileID:    "x265-video",
			FileName:  "走到尽头 (2014) - A.Hard.Day.2014.BluRay.1080p.x265.10bit-MiniHD.mkv",
			FileSize:  4 * 1024 * 1024 * 1024,
			MediaType: "movie",
			Category:  "日韩电影",
			TmdbID:    "269494",
			TargetPath: "/影视中心/日韩电影/走到尽头 (2014) {tmdb-269494}/" +
				"A Hard Day.2014.BluRay.1080p.x265.10bit.mkv",
		},
		{
			FileID:    "remux-video",
			FileName:  "走到尽头 (2014) - A.Hard.Day.2014.1080p.BluRay.Remux.AVC.DTS-HD.MA5.1.mkv",
			FileSize:  50 * 1024 * 1024 * 1024,
			MediaType: "movie",
			Category:  "日韩电影",
			TmdbID:    "269494",
			TargetPath: "/影视中心/日韩电影/走到尽头 (2014) {tmdb-269494}/" +
				"A Hard Day.2014.1080p.BluRay.Remux.AVC.DTS-HD.MA5.1.mkv",
		},
	}
	subtitles := []service.Web115File{{
		FileID:   "remux-subtitle",
		Name:     "走到尽头 (2014) - A.Hard.Day.2014.1080p.BluRay.Remux.AVC.DTS-HD.MA5.1.zh-CN.forced.ass",
		PickCode: "subtitle-pickcode",
		IsFile:   true,
		Size:     1024 * 1024,
	}}

	attachOrganizeSubtitles(subtitles, &items)

	if len(items) != 3 {
		t.Fatalf("len(items)=%d want 3 items=%+v", len(items), items)
	}
	if len(items[0].SubtitleFiles) != 0 {
		t.Fatalf("x265 subtitles=%v want none", items[0].SubtitleFiles)
	}
	if len(items[1].SubtitleFiles) != 1 || items[1].SubtitleFiles[0] != subtitles[0].Name {
		t.Fatalf("remux subtitles=%v want %q", items[1].SubtitleFiles, subtitles[0].Name)
	}

	attachment := items[2]
	if !attachment.IsSubtitle || attachment.MatchedVideoFileID != "remux-video" {
		t.Fatalf("attachment match=%+v", attachment)
	}
	wantTarget := "/影视中心/日韩电影/走到尽头 (2014) {tmdb-269494}/" +
		"A Hard Day.2014.1080p.BluRay.Remux.AVC.DTS-HD.MA5.1.zh-CN.forced.ass"
	if attachment.TargetPath != wantTarget {
		t.Fatalf("subtitle target=%q want %q", attachment.TargetPath, wantTarget)
	}
	if attachment.PickCode != "subtitle-pickcode" {
		t.Fatalf("subtitle pickcode=%q", attachment.PickCode)
	}
	handler := &OrganizeHandler{}
	if err := handler.generateStrmFiles(model.CloudDirectory{SavePath: "/tmp/film-fusion-test"}, &items, true); err != nil {
		t.Fatalf("generateStrmFiles() error=%v", err)
	}
	if items[2].StrmPath != "" {
		t.Fatalf("subtitle strm path=%q want empty", items[2].StrmPath)
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

func TestBuildOrganizeVersionGroupsCreatesEpisodeTabs(t *testing.T) {
	groups := []Organize115CookieGroup{{
		Items: []Organize115ItemResult{
			{FileID: "1080-e01", FileName: "Show.S01E01.1080p.WEB-DL.H264.mkv", MediaType: "tv", TmdbID: "100", SourceSeason: 1, SourceEpisode: 1},
			{FileID: "1080-e02", FileName: "Show.S01E02.1080p.WEB-DL.H264.mkv", MediaType: "tv", TmdbID: "100", SourceSeason: 1, SourceEpisode: 2},
			{FileID: "2160-e01", FileName: "Show.S01E01.2160p.WEB-DL.HEVC.Atmos.mkv", MediaType: "tv", TmdbID: "100", SourceSeason: 1, SourceEpisode: 1},
			{FileID: "2160-e02", FileName: "Show.S01E02.2160p.WEB-DL.HEVC.Atmos.mkv", MediaType: "tv", TmdbID: "100", SourceSeason: 1, SourceEpisode: 2},
		},
	}}

	items := annotateOrganizeItems(groups, organizeAnnotateOptions{bestVersionEnabled: true})
	versionGroups := buildOrganizeVersionGroups(items)
	if len(versionGroups) != 2 {
		t.Fatalf("len(versionGroups)=%d want 2 groups=%+v", len(versionGroups), versionGroups)
	}
	if !versionGroups[0].Recommended || versionGroups[0].EpisodeCount != 2 || versionGroups[0].FileCount != 2 {
		t.Fatalf("recommended group=%+v, want complete two-episode version", versionGroups[0])
	}
	if !strings.Contains(versionGroups[0].Label, "4K") {
		t.Fatalf("recommended label=%q want 4K profile", versionGroups[0].Label)
	}
	if versionGroups[1].EpisodeCount != 2 || len(versionGroups[1].FileIDs) != 2 {
		t.Fatalf("alternate group=%+v, want two files and episodes", versionGroups[1])
	}
}

func TestBuildOrganizeVersionGroupsUsesSeasonAndCompletePostEpisodeSuffix(t *testing.T) {
	groups := []Organize115CookieGroup{{
		Items: []Organize115ItemResult{
			{
				FileID:        "iq-e09",
				FileName:      "An Ancient Love Song.2023.S01E09.WEB-DL.2160p.H265.DDP 2.0.IQ-OurTV.mkv",
				MediaType:     "tv",
				TmdbID:        "210849",
				SourceSeason:  1,
				SourceEpisode: 9,
			},
			{
				FileID:        "iq-e10",
				FileName:      "An Ancient Love Song.2023.S01E10.WEB-DL.2160p.H265.DDP 2.0.IQ-OurTV.mkv",
				MediaType:     "tv",
				TmdbID:        "210849",
				SourceSeason:  1,
				SourceEpisode: 10,
			},
			{
				FileID:        "hds-e11",
				FileName:      "An Ancient Love Song.2023.S01E11.WEB-DL.2160p.H265.DDP 2.0-HDSWEB.mkv",
				MediaType:     "tv",
				TmdbID:        "210849",
				SourceSeason:  1,
				SourceEpisode: 11,
			},
			{
				FileID:        "hds-e12",
				FileName:      "An Ancient Love Song.2023.S01E12.WEB-DL.2160p.H265.DDP 2.0-HDSWEB.mkv",
				MediaType:     "tv",
				TmdbID:        "210849",
				SourceSeason:  1,
				SourceEpisode: 12,
			},
		},
	}}

	items := annotateOrganizeItems(groups, organizeAnnotateOptions{bestVersionEnabled: true})
	versionGroups := buildOrganizeVersionGroups(items)
	if len(versionGroups) != 2 {
		t.Fatalf("len(versionGroups)=%d want 2 groups=%+v", len(versionGroups), versionGroups)
	}
	for _, group := range versionGroups {
		if group.EpisodeCount != 2 || group.FileCount != 2 {
			t.Fatalf("group=%+v, want one two-episode release track", group)
		}
		if !strings.Contains(group.Label, "S01E*") {
			t.Fatalf("group label=%q, want normalized season/episode marker", group.Label)
		}
	}

	byID := make(map[string]Organize115ItemResult, len(items))
	for _, item := range items {
		byID[item.FileID] = item
	}
	if byID["iq-e09"].VersionKey != byID["iq-e10"].VersionKey {
		t.Fatalf("same suffix keys differ: %q != %q", byID["iq-e09"].VersionKey, byID["iq-e10"].VersionKey)
	}
	if byID["iq-e09"].VersionKey == byID["hds-e12"].VersionKey {
		t.Fatalf("different release suffixes share key %q", byID["iq-e09"].VersionKey)
	}
}

func TestOrganizeEpisodeVersionTrackKeepsSeasonsSeparate(t *testing.T) {
	seasonOne := Organize115ItemResult{
		FileName:      "Show.S01E01.WEB-DL.2160p.H265-GROUP.mkv",
		SourceSeason:  1,
		SourceEpisode: 1,
	}
	seasonTwo := Organize115ItemResult{
		FileName:      "Show.S02E01.WEB-DL.2160p.H265-GROUP.mkv",
		SourceSeason:  2,
		SourceEpisode: 1,
	}

	seasonOneKey, _ := organizeEpisodeVersionTrack(seasonOne)
	seasonTwoKey, _ := organizeEpisodeVersionTrack(seasonTwo)
	if seasonOneKey == seasonTwoKey {
		t.Fatalf("different seasons share version track key %q", seasonOneKey)
	}
}
