package service

import (
	"strings"
	"testing"
)

func TestMediaRecognitionCategoryMatchesInYAMLOrder(t *testing.T) {
	parsed, warnings, err := ParseMediaRecognitionCategoryYAML(`
movie:
  华语优先:
    original_language: 'zh'
  华语动画:
    original_language: 'zh'
    genre_ids: '16'
  其它电影:
tv:
  电视剧:
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%#v", warnings)
	}
	config := parsed.MoviePilotCategoryConfig()
	if got := SelectMoviePilotCategory("movie", MoviePilotMediaInfo{
		OriginalLanguages: []string{"zh"}, GenreIDs: []string{"16"},
	}, config); got != "华语优先" {
		t.Fatalf("category=%q want=华语优先", got)
	}
	if strings.Join(config.MovieOrder, ",") != "华语优先,华语动画,其它电影" {
		t.Fatalf("order=%#v", config.MovieOrder)
	}
}

func TestMediaRecognitionCategorySupportsExclusionAndYearRange(t *testing.T) {
	parsed, _, err := ParseMediaRecognitionCategoryYAML(`
movie:
  九十年代非恐怖片:
    release_year: '1990-1999'
    genre_ids: '!27'
  其它电影:
tv:
  电视剧:
`)
	if err != nil {
		t.Fatal(err)
	}
	config := parsed.MoviePilotCategoryConfig()
	for _, test := range []struct {
		name string
		info MoviePilotMediaInfo
		want string
	}{
		{name: "included", info: MoviePilotMediaInfo{Year: "1995", GenreIDs: []string{"18"}}, want: "九十年代非恐怖片"},
		{name: "excluded genre", info: MoviePilotMediaInfo{Year: "1995", GenreIDs: []string{"27"}}, want: "其它电影"},
		{name: "outside range", info: MoviePilotMediaInfo{Year: "2001", GenreIDs: []string{"18"}}, want: "其它电影"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SelectMoviePilotCategory("movie", test.info, config); got != test.want {
				t.Fatalf("category=%q want=%q", got, test.want)
			}
		})
	}
}

func TestMediaRecognitionCategoryValidationWarnsAboutEarlyFallback(t *testing.T) {
	_, warnings, err := ParseMediaRecognitionCategoryYAML(`
movie:
  兜底:
  永远不可达:
    genre_ids: '16'
tv:
  电视剧:
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "不会被匹配") {
		t.Fatalf("warnings=%#v", warnings)
	}

	for name, source := range map[string]string{
		"missing tv":    `movie: {电影: null}`,
		"unsafe name":   "movie:\n  ../电影:\ntv:\n  电视剧:\n",
		"unknown group": "movie: {}\ntv: {}\nanime: {}\n",
		"multiple docs": "movie: {}\ntv: {}\n---\nmovie: {}\ntv: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseMediaRecognitionCategoryYAML(source); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMediaRecognitionCategoryPersistenceDistinguishesDefaultAndSaved(t *testing.T) {
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	initial, err := service.LoadCategoryConfig()
	if err != nil {
		t.Fatal(err)
	}
	if initial.Configured || len(initial.Movie) == 0 || initial.Movie[0].Name != "动画电影" {
		t.Fatalf("initial=%+v", initial)
	}

	source := `
movie:
  我的电影:
tv:
  我的剧集:
`
	saved, err := service.SaveCategoryConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Configured || saved.Movie[0].Name != "我的电影" || saved.TV[0].Name != "我的剧集" {
		t.Fatalf("saved=%+v", saved)
	}
	loaded, err := service.LoadCategoryConfig()
	if err != nil || !loaded.Configured || loaded.YAML != saved.YAML {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestMoviePilotCategoryJSONPreservesOrderAndDynamicFields(t *testing.T) {
	var config MoviePilotCategoryConfig
	err := config.UnmarshalJSON([]byte(`{
  "movie": {
    "高分": {"vote_average": "8-10"},
    "动画": {"genre_ids": "16"},
    "其它": null
  },
  "tv": {"剧集": null}
}`))
	if err != nil {
		t.Fatal(err)
	}
	info := MoviePilotMediaInfo{GenreIDs: []string{"16"}, CategoryFields: map[string][]string{"vote_average": {"9"}}}
	if got := SelectMoviePilotCategory("movie", info, config); got != "高分" {
		t.Fatalf("category=%q order=%#v", got, config.MovieOrder)
	}
}
