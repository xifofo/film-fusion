package service

import (
	"strings"
	"testing"

	"film-fusion/app/model"
)

func TestMediaRenameConfigPreservesLegacyUntilFirstSave(t *testing.T) {
	db := newMediaRecognitionTestDB(t)
	recognition := NewMediaRecognitionService(db, nil, nil)
	moviePilot := &MoviePilotService{local: recognition}
	input := MediaRenameInput{
		Info: MoviePilotMediaInfo{
			MediaType: "movie", Title: "流浪地球", Year: "2019",
			TitleYear: "流浪地球 (2019)", TmdbID: "535167", ResourcePix: "2160p",
		},
		MediaType: "movie", Category: "华语电影",
		TransferName: "流浪地球 (2019) - 2160p.mkv", OriginalName: "source.mkv",
	}

	config, err := recognition.LoadMediaRenameConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Configured || config.Active || config.MovieFormat != DefaultMovieRenameFormat || config.TVFormat != DefaultTVRenameFormat {
		t.Fatalf("initial config=%+v", config)
	}
	legacy, err := moviePilot.BuildOrganizeTargetPath(input.Category, input)
	if err != nil {
		t.Fatal(err)
	}
	if legacy != "/华语电影/流浪地球 (2019) {tmdb-535167}/流浪地球 (2019) - 2160p.mkv" {
		t.Fatalf("legacy path=%q", legacy)
	}

	saved, err := recognition.SaveMediaRenameConfig(DefaultMovieRenameFormat, DefaultTVRenameFormat)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Configured || !saved.Active {
		t.Fatalf("saved config=%+v", saved)
	}
	custom, err := moviePilot.BuildOrganizeTargetPath(input.Category, input)
	if err != nil {
		t.Fatal(err)
	}
	if custom != "/华语电影/流浪地球 (2019)/流浪地球 (2019) - 2160p.mkv" {
		t.Fatalf("custom path=%q", custom)
	}
}

func TestMediaRenameDefaultTVFormatAndRawVariables(t *testing.T) {
	variables := BuildMediaRenameVariables(MediaRenameInput{
		Info: MoviePilotMediaInfo{
			MediaType: "tv", Title: "三体", Year: "2023", TitleYear: "三体 (2023)",
			TmdbID: "136315", SeasonEpisode: "S01E02-E03", BeginSeason: 1, HasBeginSeason: true,
		},
		MediaType: "tv", Category: "国产剧", RecognizeName: "Three-Body.S01E02-E03.2160p.WEB-DL.mkv",
		OriginalName: "Three-Body.S01E02-E03.2160p.WEB-DL.mkv", LegacyOriginalName: "S01E02-E03.2160p.WEB-DL.mkv",
		RecognitionRaw: map[string]any{
			"media_info": map[string]any{
				"en_title": "Three-Body", "overview": "测试简介",
				"actors": []any{map[string]any{"name": "张鲁一"}, map[string]any{"name": "于和伟"}},
			},
			"meta_info": map[string]any{
				"name": "三体", "resource_type": "WEB-DL", "resource_effect": []any{"HDR"},
				"resource_pix": "2160p", "video_encode": "H.265", "video_bit": "10bit",
				"audio_encode": "DDP 5.1", "resource_team": "OurTV",
				"begin_episode": 2, "end_episode": 3,
			},
		},
	})

	for key, want := range map[string]any{
		"en_title": "Three-Body", "overview": "测试简介", "actors": "张鲁一、 于和伟",
		"resourceType": "WEB-DL", "effect": "HDR", "videoFormat": "2160p",
		"videoCodec": "H.265", "videoBit": "10bit", "audioCodec": "DDP 5.1",
		"releaseGroup": "OurTV", "season": "1", "season_fmt": "S01",
		"episode": "2-3", "season_episode": "S01E02-E03", "fileExt": ".mkv",
		"original_name": "Three-Body.S01E02-E03.2160p.WEB-DL.mkv",
	} {
		if got := variables[key]; got != want {
			t.Fatalf("variable %s=%#v want=%#v", key, got, want)
		}
	}

	previewer := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	preview, err := previewer.PreviewMediaRenameTemplate("tv", DefaultTVRenameFormat, map[string]any{
		"title": "三体", "year": 2023, "season": 1, "season_episode": "S01E02-E03",
		"episode": "2-3", "part": "", "fileExt": ".mkv", "unknown_sample": "ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Path != "三体 (2023)/Season 1/三体 - S01E02-E03 - 第 2-3 集.mkv" {
		t.Fatalf("preview=%q", preview.Path)
	}
	if len(preview.Warnings) != 1 || !strings.Contains(preview.Warnings[0], "unknown_sample") {
		t.Fatalf("preview warnings=%#v", preview.Warnings)
	}
}

func TestMediaRenamePreviewReturnsEmptyWarningsArray(t *testing.T) {
	preview, err := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil).PreviewMediaRenameTemplate(
		"movie", DefaultMovieRenameFormat, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Warnings == nil || len(preview.Warnings) != 0 {
		t.Fatalf("warnings=%#v, want non-nil empty slice", preview.Warnings)
	}
}

func TestMediaRenameConfigCanBeLoadedForRepairAfterEngineUpgrade(t *testing.T) {
	db := newMediaRecognitionTestDB(t)
	if err := db.Create(&model.SystemConfig{
		ConfigKey:   model.ConfigKeyMediaRecognitionRename,
		ConfigValue: `{"movie_format":"{{ broken","tv_format":"{{ title }}{{ fileExt }}"}`,
		ConfigType:  model.TypeJSON, Category: model.CategoryMediaRecognition,
	}).Error; err != nil {
		t.Fatal(err)
	}

	rename := NewMediaRecognitionService(db, nil, nil)
	config, err := rename.LoadMediaRenameConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.Configured || config.Active || config.MovieFormat != "{{ broken" || len(config.LoadErrors) != 1 {
		t.Fatalf("repairable config=%+v", config)
	}
	target, err := (&MoviePilotService{local: rename}).BuildOrganizeTargetPath("电影", MediaRenameInput{
		Info:         MoviePilotMediaInfo{MediaType: "movie", Title: "待修复", TitleYear: "待修复 (2026)", TmdbID: "123"},
		TransferName: "待修复.mkv", OriginalName: "待修复.mkv",
	})
	if err != nil || target != "/电影/待修复 (2026) {tmdb-123}/待修复.mkv" {
		t.Fatalf("repair fallback target=%q err=%v", target, err)
	}
}

func TestMediaRenameTVAliasesAndMissingSeasonUseTVDefaults(t *testing.T) {
	for _, mediaType := range []string{"tv", "series", "show", "tvshow", "电视剧", "剧集", "番剧", "动漫"} {
		t.Run(mediaType, func(t *testing.T) {
			input := MediaRenameInput{
				Info:      MoviePilotMediaInfo{MediaType: mediaType, Title: "示例剧", Year: "2026", TitleYear: "示例剧 (2026)"},
				MediaType: mediaType, OriginalName: "Episode.mkv",
			}
			variables := BuildMediaRenameVariables(input)
			if variables["type"] != "电视剧" || variables["season"] != "1" || variables["season_fmt"] != "S01" {
				t.Fatalf("variables=%+v", variables)
			}
		})
	}

	specials := BuildMediaRenameVariables(MediaRenameInput{
		MediaType: "tv", OriginalName: "Special.mkv",
		RecognitionRaw: map[string]any{"meta_info": map[string]any{"begin_season": 0, "begin_episode": 1}},
	})
	if specials["season"] != "0" || specials["season_fmt"] != "S00" {
		t.Fatalf("special season variables=%+v", specials)
	}
	preview, err := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil).PreviewMediaRenameTemplate(
		"tv", `{% if season %}{{season_fmt}}{% else %}missing{% endif %}/{{title}}{{fileExt}}`,
		map[string]any{"season": "0", "season_fmt": "S00", "title": "特别篇", "fileExt": ".mkv"},
	)
	if err != nil || preview.Path != "S00/特别篇.mkv" {
		t.Fatalf("special season preview=%+v err=%v", preview, err)
	}
}

func TestMediaRenameTemplateValidationAndPathSafety(t *testing.T) {
	valid, err := ValidateMediaRenameTemplate("movie", `{{title}}/{{unknown_variable}}{% if year %} ({{year}}){% endif %}{{fileExt}}`)
	if err != nil || !valid.Valid {
		t.Fatalf("undefined variable compatibility result=%+v err=%v", valid, err)
	}

	for name, template := range map[string]string{
		"include":         `{% include "config.yaml" %}`,
		"trimmed include": `{%- include "config.yaml" %}`,
		"loop":            `{% for item in title %}{% endfor %}{{title}}{{fileExt}}`,
		"trimmed loop":    `{%- for item in title %}{% endfor %}{{title}}{{fileExt}}`,
		"absolute":        `/tmp/{{title}}{{fileExt}}`,
		"traversal":       `../{{title}}{{fileExt}}`,
		"backslash":       `safe\\{{title}}{{fileExt}}`,
		"empty segment":   `safe//{{title}}{{fileExt}}`,
		"windows chars":   `safe:folder/{{title}}{{fileExt}}`,
		"windows device":  `CON/{{title}}{{fileExt}}`,
		"trailing dot":    `folder./{{title}}{{fileExt}}`,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ValidateMediaRenameTemplate("movie", template)
			if err == nil || result.Valid {
				t.Fatalf("unsafe template accepted: %+v", result)
			}
		})
	}

	longTemplate := `{{title}}/` + strings.Repeat("a", maxMediaRenameSegmentBytes+1) + `{{fileExt}}`
	if result, err := ValidateMediaRenameTemplate("movie", longTemplate); err == nil || result.Valid {
		t.Fatalf("oversized path segment accepted: %+v", result)
	}

	previewer := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	preview, err := previewer.PreviewMediaRenameTemplate("movie", `{{title}}/{{original_name}}`, map[string]any{
		"title": "../../逃逸", "original_name": "../源片.mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Path != "..／..／逃逸/..／源片.mkv" {
		t.Fatalf("sanitized preview=%q", preview.Path)
	}
}
