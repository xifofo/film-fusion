package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMediaRecognitionServiceParsesFileWithFolderContext(t *testing.T) {
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	result, err := service.Recognize(context.Background(),
		`/downloads/百花杀 (2026)/百花杀.S01E03.2160p.WEB-DL.H265.10bit.DDP5.1.mkv`,
		MediaRecognitionOptions{
			Mode: MediaRecognitionModeFile, UseProvidedWords: true,
			Words: []string{"百花杀 => 百花杀"}, LookupTMDB: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	meta := result.MetaInfo
	if meta.Name != "百花杀" || meta.Year != "2026" || meta.MediaType != "tv" {
		t.Fatalf("identity=%+v", meta)
	}
	if meta.SeasonEpisode != "S01E03" || meta.ResourcePix != "2160p" || meta.ResourceType != "WEB-DL" {
		t.Fatalf("release=%+v", meta)
	}
	if meta.VideoEncode != "H.265" || meta.VideoBit != "10bit" || meta.AudioEncode != "DDP" {
		t.Fatalf("codec=%+v", meta)
	}
	if result.TMDBStatus != "not_configured" || result.Engine != "local" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLocalWebSourceRecognizesCommonReleaseAliases(t *testing.T) {
	tests := map[string]string{
		"Movie.2026.NF.WEB-DL.2160p.mkv":     "Netflix",
		"Movie.2026.AMZN.WEB-DL.2160p.mkv":   "Amazon",
		"Movie.2026.DSNP.WEB-DL.2160p.mkv":   "Disney+",
		"Movie.2026.iTunes.WEB-DL.2160p.mkv": "Apple TV+",
		"Movie.2026.HMAX.WEB-DL.2160p.mkv":   "Max",
	}
	for input, want := range tests {
		t.Run(want, func(t *testing.T) {
			meta, err := parseLocalMediaMeta(input, MediaRecognitionWordResult{Processed: input}, MediaRecognitionModeFile)
			if err != nil {
				t.Fatal(err)
			}
			if meta.WebSource != want {
				t.Fatalf("web source=%q want=%q meta=%+v", meta.WebSource, want, meta)
			}
		})
	}
}

func TestLocalMovieTransferNameMatchesObservedMP2Result(t *testing.T) {
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "[乌云背后的幸福线].Silver.Linings.Playbook.(2012).FIN.BluRay.1080p.x264.DDP.5.1.2Audios-CMCT.{tmdb-82693}.mkv",
			want:  "Silver Linings Playbook.2012.BluRay.1080p.x264.DDP 5.1 2Audios-CMCT.mkv",
		},
		{
			input: "Insidious.(2011).BluRay.HDR.DoVi.2160p.x265.10bit.TrueHD.7.1.10bit.{tmdb-49018}.mkv",
			want:  "Insidious.2011.BluRay HDR DoVi.2160p.x265 10bit.TrueHD 7.1.10bit.mkv",
		},
	}
	for _, test := range tests {
		name, raw, err := service.BuildTransferName(test.input)
		if err != nil {
			t.Fatal(err)
		}
		if name != test.want {
			t.Fatalf("transfer name=%q want=%q raw=%#v", name, test.want, raw)
		}
	}
}

func TestMoviePilotInfoFromLocalResultKeepsOriginalTitle(t *testing.T) {
	info := moviePilotInfoFromLocalResult(MediaRecognitionResult{
		MediaInfo: MediaRecognitionMediaInfo{
			MediaType: "movie", Title: "乌云背后的幸福线",
			OriginalTitle: "Silver Linings Playbook", Year: "2012",
		},
	})
	if info.OriginalTitle != "Silver Linings Playbook" {
		t.Fatalf("original title=%q", info.OriginalTitle)
	}
}

func TestMediaRecognitionServiceMatchesExplicitTMDBID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/123" || r.URL.Query().Get("api_key") != "test-key" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":123,"name":"百花杀","original_name":"Hundred Flowers Kill",
			"first_air_date":"2026-01-02","poster_path":"/poster.jpg",
			"backdrop_path":"/backdrop.jpg","vote_average":8.6,
			"overview":"测试简介","original_language":"zh",
			"origin_country":["CN"],"genre_ids":[18,9648]
		}`))
	}))
	defer server.Close()

	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: server.URL, APIKey: "test-key", TimeoutSeconds: 2,
	}}, nil)
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	result, err := service.Recognize(context.Background(),
		`百花杀 {[tmdbid=123;type=tv;s=1]} S01E02.1080p.WEB-DL.mkv`,
		MediaRecognitionOptions{
			Mode: MediaRecognitionModeFile, UseProvidedWords: true, LookupTMDB: true,
			UseProvidedCategory: true, CategoryYAML: `
movie:
  电影:
tv:
  华语悬疑剧:
    origin_country: 'CN'
    genre_ids: '9648'
  其它剧集:
`,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.TMDBStatus != "matched_by_id" || result.MediaInfo.TMDBID != "123" {
		t.Fatalf("result=%+v", result)
	}
	if result.MediaInfo.Title != "百花杀" || result.MediaInfo.Year != "2026" || result.MediaInfo.Source != "themoviedb" {
		t.Fatalf("media=%+v", result.MediaInfo)
	}
	if result.MediaInfo.Category != "华语悬疑剧" || result.Candidates[0].Category != "华语悬疑剧" {
		t.Fatalf("categories media=%q candidates=%+v", result.MediaInfo.Category, result.Candidates)
	}
	if result.MediaInfo.CategoryPath != "电视剧/华语悬疑剧" || result.Candidates[0].CategoryPath != "电视剧/华语悬疑剧" {
		t.Fatalf("category paths media=%q candidates=%+v", result.MediaInfo.CategoryPath, result.Candidates)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Confidence != 1 {
		t.Fatalf("candidates=%+v", result.Candidates)
	}
}

func TestParseLocalMediaMetaSupportsCommonTMDBMarkers(t *testing.T) {
	markers := []string{
		"[tmdbid=95491]",
		"[tmdbid-95491]",
		"[tmdb=95491]",
		"[tmdb-95491]",
		"{tmdbid=95491}",
		"{tmdbid-95491}",
		"{tmdb=95491}",
		"{tmdb-95491}",
	}
	for _, marker := range markers {
		t.Run(marker, func(t *testing.T) {
			input := "The Vampire Diaries (2009) " + marker
			meta, err := parseLocalMediaMeta(input, MediaRecognitionWordResult{Processed: input}, MediaRecognitionModeTitle)
			if err != nil {
				t.Fatal(err)
			}
			if meta.TMDBID != "95491" || meta.Name != "The Vampire Diaries" || meta.Year != "2009" {
				t.Fatalf("meta=%+v", meta)
			}
		})
	}
}

func TestExtractLocalTMDBMarkerIDIgnoresUnsupportedMarkers(t *testing.T) {
	for _, input := range []string{
		"The Vampire Diaries (2009)",
		"The Vampire Diaries (2009) [tvdbid=95491]",
		"The Vampire Diaries (2009) {imdb-tt1405406}",
		"The Vampire Diaries (2009) {tmdb-xxxxx}",
	} {
		if got := extractLocalTMDBMarkerID(input); got != "" {
			t.Fatalf("extractLocalTMDBMarkerID(%q)=%q", input, got)
		}
	}
}

func TestMediaRecognitionServiceUsesTMDBMarkerBeforeGuessingMovieFromYear(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if r.URL.Path != "/3/tv/30983" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":30983,"name":"名侦探柯南","original_name":"名探偵コナン",
			"first_air_date":"1996-01-08","vote_average":8.0,
			"original_language":"ja","origin_country":["JP"],"genre_ids":[16,9648]
		}`))
	}))
	defer server.Close()

	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: server.URL, APIKey: "test-key", TimeoutSeconds: 2,
	}}, nil)
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	result, err := service.Recognize(
		context.Background(),
		"Case Closed (1996) {tmdbid-30983}",
		MediaRecognitionOptions{Mode: MediaRecognitionModeTitle, UseProvidedWords: true, LookupTMDB: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.TMDBStatus != "matched_by_id" || result.MediaInfo.TMDBID != "30983" || result.MediaInfo.MediaType != "tv" {
		t.Fatalf("result=%+v requests=%v", result, requests)
	}
	if strings.Join(requests, ",") != "/3/movie/30983,/3/tv/30983" {
		t.Fatalf("requests=%v", requests)
	}
}

func TestMediaRecognitionServiceRecognizesBracketOnlyReleaseByTMDBMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/30983" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":30983,"name":"名侦探柯南","original_name":"名探偵コナン",
			"first_air_date":"1996-01-08","vote_average":8.0,
			"original_language":"ja","origin_country":["JP"],"genre_ids":[16,9648]
		}`))
	}))
	defer server.Close()

	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: server.URL, APIKey: "test-key", TimeoutSeconds: 2,
	}}, nil)
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	result, err := service.Recognize(
		context.Background(),
		"[SBSUB][CONAN][1210][WEBRIP][1080P][HEVC_AAC][CHS_CHT_JP][PGS](E5B7FCE8).{tmdbid-30983}.mkv",
		MediaRecognitionOptions{
			Mode: MediaRecognitionModeFile, UseProvidedWords: true, LookupTMDB: true,
			UseProvidedCategory: true, CategoryYAML: `
movie:
  其他电影:
tv:
  动画番剧:
    genre_ids: '16'
  其他剧集:
`,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.TMDBStatus != "matched_by_id" || result.MediaInfo.TMDBID != "30983" || result.MediaInfo.MediaType != "tv" {
		t.Fatalf("result=%+v", result)
	}
	if result.MediaInfo.Title != "名侦探柯南" || result.MediaInfo.Category != "动画番剧" ||
		result.MetaInfo.Name != "CONAN" || result.MetaInfo.SeasonEpisode != "S01E1210" ||
		result.MetaInfo.ResourcePix != "1080p" || result.MetaInfo.VideoEncode != "H.265" || result.MetaInfo.AudioEncode != "AAC" {
		t.Fatalf("media=%+v meta=%+v", result.MediaInfo, result.MetaInfo)
	}
}

func TestMediaRecognitionServiceSearchesTMDBAndRejectsWrongYear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/search/movie" || r.URL.Query().Get("year") != "2021" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "Dune 沙丘" {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		if r.URL.Query().Get("query") != "沙丘" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"results":[
			{"id":11,"title":"沙丘","original_title":"Dune","release_date":"1984-12-14","media_type":"movie","vote_average":6.2},
			{"id":22,"title":"沙丘","original_title":"Dune","release_date":"2021-10-22","media_type":"movie","vote_average":8.0,"genre_ids":[12,878]}
		]}`))
	}))
	defer server.Close()
	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: server.URL, APIKey: "test-key", TimeoutSeconds: 2,
	}}, nil)
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	result, err := service.Recognize(context.Background(), "Dune 沙丘 2021 2160p BluRay.mkv",
		MediaRecognitionOptions{Mode: MediaRecognitionModeFile, UseProvidedWords: true, LookupTMDB: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.TMDBStatus != "matched" || result.MediaInfo.TMDBID != "22" {
		t.Fatalf("selected=%+v candidates=%+v", result.MediaInfo, result.Candidates)
	}
	if len(result.MediaInfo.Genres) != 2 || strings.Join(result.MediaInfo.Genres, ",") != "冒险,科幻" {
		t.Fatalf("genres=%#v", result.MediaInfo.Genres)
	}
}

func TestMediaRecognitionServiceMatchesTranslatedTVTitleAfterYearFallback(t *testing.T) {
	requests := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/3/tv/326556" {
			requests = append(requests, "detail:"+r.URL.Query().Get("language"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":326556,"name":"斩神之凡尘神域Ⅱ","original_name":"斩神之凡尘神域Ⅱ",
				"first_air_date":"2026-06-25","vote_average":8.3,"genres":[{"id":16,"name":"动画"}],
				"original_language":"zh","origin_country":["CN"]
			}`))
			return
		}
		if r.URL.Path != "/3/search/tv" || r.URL.Query().Get("query") != "Slay The Gods" {
			http.NotFound(w, r)
			return
		}
		language := r.URL.Query().Get("language")
		year := r.URL.Query().Get("first_air_date_year")
		requests = append(requests, language+":"+year)
		w.Header().Set("Content-Type", "application/json")
		if year != "" {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		if language == "en-US" {
			_, _ = w.Write([]byte(`{"results":[
				{"id":259231,"name":"Slay the Gods","original_name":"斩神之凡尘神域","first_air_date":"2024-07-31","vote_average":8.3,"genre_ids":[16],"original_language":"zh","origin_country":["CN"]},
				{"id":326556,"name":"Slay the Gods II","original_name":"斩神之凡尘神域Ⅱ","vote_average":8.0,"genre_ids":[18],"original_language":"zh","origin_country":["CN"]}
			]}`))
			return
		}
		if language != "zh-CN" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"results":[
			{"id":259231,"name":"斩神之凡尘神域","original_name":"斩神之凡尘神域","first_air_date":"2024-07-31","vote_average":8.3,"genre_ids":[16],"original_language":"zh","origin_country":["CN"]},
			{"id":326556,"name":"斩神之凡尘神域Ⅱ","original_name":"斩神之凡尘神域Ⅱ","vote_average":8.0,"genre_ids":[18],"original_language":"zh","origin_country":["CN"]}
		]}`))
	}))
	defer server.Close()

	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: server.URL, APIKey: "test-key", TimeoutSeconds: 2,
	}}, nil)
	service := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	result, err := service.Recognize(context.Background(),
		"Slay.The.Gods.2026.S01E11.1080p.WeTV.WEB-DL.AAC2.0.H.264-DepWeb.mkv",
		MediaRecognitionOptions{
			Mode: MediaRecognitionModeFile, UseProvidedWords: true, LookupTMDB: true,
			UseProvidedCategory: true, CategoryYAML: `
movie:
  其他电影:
tv:
  动画番剧:
    genre_ids: '16'
  国产剧集:
    origin_country: 'CN'
  其他剧集:
`,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.MetaInfo.Name != "Slay The Gods" || result.MetaInfo.Year != "2026" {
		t.Fatalf("meta=%+v", result.MetaInfo)
	}
	if result.TMDBStatus != "matched" || result.MediaInfo.TMDBID != "326556" {
		t.Fatalf("status=%q media=%+v candidates=%+v", result.TMDBStatus, result.MediaInfo, result.Candidates)
	}
	if result.MediaInfo.Title != "斩神之凡尘神域Ⅱ" || result.MediaInfo.Year != "2026" {
		t.Fatalf("localized media=%+v", result.MediaInfo)
	}
	if result.MediaInfo.Category != "动画番剧" || len(result.Candidates) == 0 || result.Candidates[0].Score < minMediaRecognitionMatchScore {
		t.Fatalf("category=%q candidates=%+v", result.MediaInfo.Category, result.Candidates)
	}
	if got := strings.Join(requests, ","); got != "zh-CN:2026,en-US:2026,zh-CN:,en-US:,detail:zh-CN" {
		t.Fatalf("requests=%q", got)
	}
}

func TestMediaRecognitionWordsPersistenceDistinguishesUnconfiguredAndEmpty(t *testing.T) {
	db := newMediaRecognitionTestDB(t)
	service := NewMediaRecognitionService(db, nil, nil)
	words, configured, err := service.LoadWords()
	if err != nil || configured || len(words) != 0 {
		t.Fatalf("initial words=%#v configured=%v err=%v", words, configured, err)
	}
	if _, err := service.SaveWords([]string{}); err != nil {
		t.Fatal(err)
	}
	words, configured, err = service.LoadWords()
	if err != nil || !configured || len(words) != 0 {
		t.Fatalf("saved empty words=%#v configured=%v err=%v", words, configured, err)
	}
	if _, err := service.SaveWords([]string{"旧名 => 新名", "# 注释"}); err != nil {
		t.Fatal(err)
	}
	words, configured, err = service.LoadWords()
	if err != nil || !configured || strings.Join(words, "|") != "旧名 => 新名|# 注释" {
		t.Fatalf("saved words=%#v configured=%v err=%v", words, configured, err)
	}
}

func newMediaRecognitionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:media-recognition-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	return db
}
