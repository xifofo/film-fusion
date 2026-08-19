package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
)

func TestMoviePilotManualTransferUsesSynchronousEndpoint(t *testing.T) {
	var authorization string
	var background string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/transfer/manual":
			authorization = r.Header.Get("Authorization")
			background = r.URL.Query().Get("background")
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"message":"整理完成"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	result, err := svc.ManualTransfer(context.Background(), MoviePilotManualTransferRequest{
		SourcePath: "/downloads/Show.S01", FileType: "dir", TmdbID: "1396",
		MediaType: "tv", TransferType: "link", Scrape: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fileItem, _ := payload["fileitem"].(map[string]any)
	if authorization != "Bearer test-token" || background != "false" || result.Message != "整理完成" {
		t.Fatalf("unexpected request metadata: auth=%q background=%q result=%#v", authorization, background, result)
	}
	if fileItem["path"] != "/downloads/Show.S01" || fileItem["type"] != "dir" || payload["type_name"] != "电视剧" || payload["transfer_type"] != "link" || payload["tmdbid"] != float64(1396) || payload["scrape"] != true {
		t.Fatalf("unexpected manual transfer payload: %#v", payload)
	}
}

func TestMoviePilotRecognizeTitleParsesRichMediaInfo(t *testing.T) {
	var gotTitle string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize":
			gotTitle = r.URL.Query().Get("title")
			_, _ = w.Write([]byte(`{
				"success": true,
				"data": {
					"media_info": {
						"media_type": "tv",
						"title": "百花杀",
						"year": "2026",
						"title_year": "百花杀 (2026)",
						"tmdb_id": 123456,
						"category": "国产剧集",
						"poster_path": "/poster.jpg",
						"backdrop_path": "/backdrop.jpg",
						"vote_average": "8.3",
						"genre_ids": [18, 9648],
						"genres": [{"id": 18, "name": "剧情"}, {"id": 9648, "name": "悬疑"}]
					},
					"meta_info": {
						"season_episode": "S01E01",
						"resource_type": "WEB-DL",
						"resource_pix": "2160p",
						"video_encode": "H.265"
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	info, raw, err := svc.RecognizeTitle("  百花杀.S01E01.2160p  ")
	if err != nil {
		t.Fatalf("RecognizeTitle: %v", err)
	}
	if gotTitle != "百花杀.S01E01.2160p" {
		t.Fatalf("title query = %q", gotTitle)
	}
	if raw["media_info"] == nil || raw["meta_info"] == nil {
		t.Fatalf("raw recognition data missing sections: %#v", raw)
	}
	if info.MediaType != "tv" || info.Title != "百花杀" || info.Year != "2026" || info.Category != "国产剧集" {
		t.Fatalf("unexpected identity fields: %+v", info)
	}
	if info.PosterPath != "/poster.jpg" || info.BackdropPath != "/backdrop.jpg" || info.Rating != 8.3 {
		t.Fatalf("unexpected artwork/rating fields: %+v", info)
	}
	if strings.Join(info.Genres, ",") != "剧情,悬疑" {
		t.Fatalf("genres = %#v", info.Genres)
	}
	if info.SeasonEpisode != "S01E01" || info.ResourceType != "WEB-DL" || info.ResourcePix != "2160p" || info.VideoEncode != "H.265" {
		t.Fatalf("unexpected release fields: %+v", info)
	}
}

func TestMoviePilotBusinessFailureIsReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/login/access-token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"message":"未识别到媒体","data":null}`))
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	tests := []struct {
		name string
		call func() error
	}{
		{name: "recognize file", call: func() error {
			_, _, err := svc.RecognizeFile("Show.S01E01.mkv")
			return err
		}},
		{name: "recognize title", call: func() error {
			_, _, err := svc.RecognizeTitle("Show.S01E01")
			return err
		}},
		{name: "search", call: func() error {
			_, err := svc.SearchMedia("Show", 8)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil || !strings.Contains(err.Error(), "未识别到媒体") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMoviePilotRecognitionFallsBackToFilmFusionLocalEngine(t *testing.T) {
	db := newMediaRecognitionTestDB(t)
	local := NewMediaRecognitionService(db, nil, nil)
	if _, err := local.SaveWords([]string{"S04 => S01"}); err != nil {
		t.Fatal(err)
	}
	svc := NewMoviePilotService(&config.Config{}, nil)
	svc.SetLocalMediaRecognition(local)

	info, raw, err := svc.RecognizeFile("Example.Show.S04E02.1080p.WEB-DL.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Example Show" || info.MediaType != "tv" || info.SeasonEpisode != "S01E02" {
		t.Fatalf("info=%+v", info)
	}
	if raw["engine"] != "local" || !strings.Contains(raw["fallback_reason"].(string), "未配置") {
		t.Fatalf("raw=%#v", raw)
	}

	name, nameRaw, err := svc.TransferName("Example.Show.S04E02.mkv", "mkv")
	if err != nil || name != "Example.Show.S01E02.mkv" || nameRaw["engine"] != "local" {
		t.Fatalf("name=%q raw=%#v err=%v", name, nameRaw, err)
	}
	categories, err := svc.GetCategoryConfig()
	if err != nil || categories.Movie["电影"] != nil || categories.TV["电视剧"] != nil {
		t.Fatalf("categories=%#v err=%v", categories, err)
	}
}

func TestMoviePilotExplicitRecognitionSourceDoesNotCrossFallbackBoundary(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		default:
			_, _ = w.Write([]byte(`{"success":false,"message":"MoviePilot deliberate failure"}`))
		}
	}))
	defer server.Close()

	local := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	if _, err := local.SaveWords([]string{"S04 => S01"}); err != nil {
		t.Fatal(err)
	}
	svc := newTestMoviePilotService(server.URL)
	svc.SetLocalMediaRecognition(local)

	info, raw, err := svc.RecognizeFileWithSource(
		"Example.Show.S04E02.{tmdb-123}.mkv",
		MediaRecognitionSourceLocal,
	)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 0 || info.SeasonEpisode != "S01E02" || raw["engine"] != "local" {
		t.Fatalf("local source crossed into MoviePilot: requests=%d info=%+v raw=%#v", requestCount, info, raw)
	}
	name, nameRaw, err := svc.TransferNameWithSource(
		"Example.Show.S04E02.mkv",
		"mkv",
		MediaRecognitionSourceLocal,
	)
	if err != nil || name != "Example.Show.S01E02.mkv" || nameRaw["engine"] != "local" || requestCount != 0 {
		t.Fatalf("local naming crossed into MoviePilot: requests=%d name=%q raw=%#v err=%v", requestCount, name, nameRaw, err)
	}
	if _, err := svc.GetCategoryConfigWithSource(MediaRecognitionSourceLocal); err != nil || requestCount != 0 {
		t.Fatalf("local category lookup crossed into MoviePilot: requests=%d err=%v", requestCount, err)
	}

	if _, _, err := svc.RecognizeFileWithSource("Example.Show.S04E02.mkv", MediaRecognitionSourceMoviePilot); err == nil || !strings.Contains(err.Error(), "deliberate failure") {
		t.Fatalf("explicit MoviePilot source unexpectedly fell back: %v", err)
	}
	if requestCount == 0 {
		t.Fatal("explicit MoviePilot source did not contact MoviePilot")
	}
}

func TestNormalizeMediaRecognitionSource(t *testing.T) {
	tests := map[string]string{
		"":           MediaRecognitionSourceMoviePilot,
		"MP":         MediaRecognitionSourceMoviePilot,
		"moviepilot": MediaRecognitionSourceMoviePilot,
		"local":      MediaRecognitionSourceLocal,
		"FilmFusion": MediaRecognitionSourceLocal,
	}
	for input, want := range tests {
		got, err := NormalizeMediaRecognitionSource(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeMediaRecognitionSource(%q)=%q, %v want=%q", input, got, err, want)
		}
	}
	if _, err := NormalizeMediaRecognitionSource("unknown"); err == nil {
		t.Fatal("unknown recognition source should fail validation")
	}
}

func TestMoviePilotRecognitionUsesFilmFusionWordsWhenConfigured(t *testing.T) {
	var gotTitle, gotWords string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize":
			gotTitle = r.URL.Query().Get("title")
			gotWords = r.URL.Query().Get("custom_words")
			_, _ = w.Write([]byte(`{"success":true,"data":{"media_info":{"media_type":"tv","title":"Example","tmdb_id":123},"meta_info":{"season_episode":"S01E02"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	local := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	if _, err := local.SaveWords([]string{"S04 => S01", "第 <> 集 >> EP+1"}); err != nil {
		t.Fatal(err)
	}
	svc := newTestMoviePilotService(server.URL)
	svc.SetLocalMediaRecognition(local)
	if _, _, err := svc.RecognizeFile("Example.S04E02.mkv"); err != nil {
		t.Fatal(err)
	}
	if gotTitle != "Example.S04E02.mkv" || gotWords != "S04 => S01\n第 <> 集 >> EP+1" {
		t.Fatalf("title=%q words=%q", gotTitle, gotWords)
	}
}

func TestMoviePilotRecognitionUsesSavedFilmFusionCategoryConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize":
			_, _ = w.Write([]byte(`{
				"success":true,
				"data":{"media_info":{
					"media_type":"tv","title":"Example","year":"2026","tmdb_id":123,
					"category":"MoviePilot 分类","origin_country":["CN"],"genre_ids":[18]
				}}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	local := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	if _, err := local.SaveCategoryConfig(`
movie:
  本地电影:
tv:
  FilmFusion 国产剧:
    origin_country: 'CN'
  其它剧集:
`); err != nil {
		t.Fatal(err)
	}
	svc := newTestMoviePilotService(server.URL)
	svc.SetLocalMediaRecognition(local)

	info, _, err := svc.RecognizeTitle("Example.S01E01")
	if err != nil {
		t.Fatal(err)
	}
	if info.Category != "FilmFusion 国产剧" {
		t.Fatalf("category=%q", info.Category)
	}
	config, err := svc.GetCategoryConfig()
	if err != nil || len(config.TVOrder) != 2 || config.TVOrder[0] != "FilmFusion 国产剧" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestMoviePilotFallbackCircuitAvoidsRepeatedUnavailableRequests(t *testing.T) {
	recognitionRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize_file":
			recognitionRequests++
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	svc.SetLocalMediaRecognition(NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil))
	for _, input := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv"} {
		info, raw, err := svc.RecognizeFile(input)
		if err != nil || info.Title != "Show" || raw["engine"] != "local" {
			t.Fatalf("input=%q info=%+v raw=%#v err=%v", input, info, raw, err)
		}
	}
	if recognitionRequests != 1 {
		t.Fatalf("recognition requests=%d want=1", recognitionRequests)
	}
}

func TestParseMoviePilotSearchResultRichFields(t *testing.T) {
	result := parseSearchResult(map[string]any{
		"media_info": map[string]any{
			"media_type":    "movie",
			"title":         "测试电影",
			"year":          "2026",
			"tmdb_id":       "tmdb:98765",
			"category":      "华语电影",
			"poster_path":   "/search-poster.jpg",
			"backdrop_path": "/search-backdrop.jpg",
			"vote_average":  7.6,
			"genres": []any{
				map[string]any{"id": float64(28), "name": "动作"},
				map[string]any{"id": float64(53), "name": "惊悚"},
			},
		},
	})

	if result.MediaType != "movie" || result.Category != "华语电影" || result.TmdbID != "98765" {
		t.Fatalf("unexpected search identity: %+v", result)
	}
	if result.PosterPath != "/search-poster.jpg" || result.BackdropPath != "/search-backdrop.jpg" || result.Rating != 7.6 {
		t.Fatalf("unexpected search artwork/rating: %+v", result)
	}
	if strings.Join(result.Genres, ",") != "动作,惊悚" {
		t.Fatalf("genres = %#v", result.Genres)
	}
}

func TestValidateMoviePilotSuccessAllowsUnwrappedResponses(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"data":{"title":"合法响应"}}`),
		[]byte(`[{"title":"合法响应","tmdb_id":1}]`),
	} {
		if err := validateMoviePilotSuccess(body); err != nil {
			t.Fatalf("validateMoviePilotSuccess(%s): %v", body, err)
		}
	}
}

func TestBuildMoviePilotTargetPathPreservesSourceExtension(t *testing.T) {
	info := MoviePilotMediaInfo{
		TitleYear: "潜伏 (2011)",
		TmdbID:    "49018",
	}
	tests := []struct {
		name         string
		transferName string
		originalName string
		want         string
	}{
		{
			name:         "quality token is not a file extension",
			transferName: "Insidious.2011.BluRay HDR DoVi.2160p.x265 10bit.TrueHD 7.1.10bit",
			originalName: "Insidious.2011.BluRay.2160p.mkv",
			want:         "/欧美电影/潜伏 (2011) {tmdb-49018}/Insidious.2011.BluRay HDR DoVi.2160p.x265 10bit.TrueHD 7.1.10bit.mkv",
		},
		{
			name:         "existing source extension is not duplicated",
			transferName: "Insidious.2011.2160p.MKV",
			originalName: "Insidious.2011.BluRay.2160p.mkv",
			want:         "/欧美电影/潜伏 (2011) {tmdb-49018}/Insidious.2011.2160p.MKV",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := BuildMoviePilotTargetPath("欧美电影", info, test.transferName, test.originalName)
			if got != test.want {
				t.Fatalf("BuildMoviePilotTargetPath()=%q want=%q", got, test.want)
			}
		})
	}
}

func newTestMoviePilotService(api string) *MoviePilotService {
	return NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{
		API: api, Username: "user", Password: "pass",
	}}, nil)
}
