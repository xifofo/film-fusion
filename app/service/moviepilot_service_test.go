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

func TestSystemDefaultLocalRecognitionUsesFilmFusionEngine(t *testing.T) {
	db := newMediaRecognitionTestDB(t)
	local := NewMediaRecognitionService(db, nil, nil)
	if _, err := local.SaveWords([]string{"S04 => S01"}); err != nil {
		t.Fatal(err)
	}
	svc := NewMoviePilotService(&config.Config{MediaRecognition: config.MediaRecognitionConfig{Source: "local"}}, nil)
	svc.SetLocalMediaRecognition(local)

	info, raw, err := svc.RecognizeFile("Example.Show.S04E02.1080p.WEB-DL.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Example Show" || info.MediaType != "tv" || info.SeasonEpisode != "S01E02" {
		t.Fatalf("info=%+v", info)
	}
	if raw["engine"] != "local" {
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

func TestMoviePilotRecognitionKeepsPrimaryResultAndAttachesLocalShadowDiff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize_file":
			_, _ = w.Write([]byte(`{
				"success":true,
				"data":{
					"media_info":{"media_type":"tv","title":"MP2 Show","title_year":"MP2 Show (2026)","year":"2026","tmdb_id":123},
					"meta_info":{"season_episode":"S01E02"}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	local, _ := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeMedianameJSON(w, medianameDetails(456, "Local Show", "tv"))
	})
	svc.SetLocalMediaRecognition(local)
	info, raw, err := svc.RecognizeFile("Local.Show.2026.S01E02.{tmdb-456}.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "MP2 Show" || info.TmdbID != "123" {
		t.Fatalf("primary result was replaced by shadow: %+v", info)
	}
	comparison, ok := raw[mediaRecognitionShadowRawKey].(MediaRecognitionShadowComparison)
	if !ok {
		t.Fatalf("shadow comparison missing from raw result: %#v", raw)
	}
	if comparison.Status != MediaRecognitionShadowDifferent || comparison.Matched {
		t.Fatalf("comparison=%+v", comparison)
	}
	if comparison.MoviePilot == nil || comparison.Local == nil || comparison.MoviePilot.TMDBID != "123" || comparison.Local.TMDBID != "456" {
		t.Fatalf("unexpected snapshots: %+v", comparison)
	}
	foundTMDBDiff := false
	for _, difference := range comparison.Differences {
		if difference.Field == "tmdb_id" && difference.MoviePilot == "123" && difference.Local == "456" {
			foundTMDBDiff = true
		}
	}
	if !foundTMDBDiff {
		t.Fatalf("TMDB difference missing: %+v", comparison.Differences)
	}
	extracted, localInfo, localRaw, ok := MediaRecognitionShadowFromRaw(raw)
	if !ok || extracted.Status != MediaRecognitionShadowDifferent || localInfo == nil || localInfo.TmdbID != "456" || localRaw == nil {
		t.Fatalf("organize shadow context missing: comparison=%+v local=%+v raw=%#v", extracted, localInfo, localRaw)
	}
}

func TestMoviePilotRecognitionIgnoresLocalShadowFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize_file":
			_, _ = w.Write([]byte(`{"success":true,"data":{"media_info":{"media_type":"movie","title":"Primary","tmdb_id":99}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	svc.SetLocalMediaRecognition(NewMediaRecognitionService(nil, nil, nil))
	info, raw, err := svc.RecognizeFile("Primary.2026.mkv")
	if err != nil || info.Title != "Primary" || info.TmdbID != "99" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	comparison, ok := raw[mediaRecognitionShadowRawKey].(MediaRecognitionShadowComparison)
	if !ok || comparison.Status != MediaRecognitionShadowLocalError || comparison.LocalError == "" {
		t.Fatalf("comparison=%+v raw=%#v", comparison, raw)
	}
}

func TestCompareRecognitionUsesSameDraftRulesForMP2AndLocal(t *testing.T) {
	var gotTitle, gotWords string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize":
			gotTitle = r.URL.Query().Get("title")
			gotWords = r.URL.Query().Get("custom_words")
			_, _ = w.Write([]byte(`{
				"success":true,
				"data":{
					"media_info":{"media_type":"tv","title":"Example","title_year":"Example"},
					"meta_info":{"season_episode":"S02E04"}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	svc.SetLocalMediaRecognition(NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil))
	result, err := svc.CompareRecognition(context.Background(), "Example.S01E03.mkv", MediaRecognitionOptions{
		Mode: MediaRecognitionModeFile, LookupTMDB: false,
		Words: []string{"S01 => S02", `S02E <> \.mkv >> EP+1`}, UseProvidedWords: true,
		CategoryYAML: "movie:\n  临时电影:\ntv:\n  临时剧集:\n", UseProvidedCategory: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotTitle != "Example.S01E03.mkv" || gotWords != "S01 => S02\nS02E <> \\.mkv >> EP+1" {
		t.Fatalf("title=%q words=%q", gotTitle, gotWords)
	}
	if result.Local == nil || result.Local.MetaInfo.SeasonEpisode != "S02E04" {
		t.Fatalf("local result=%+v", result.Local)
	}
	if result.Comparison.Status != MediaRecognitionShadowLocalError || result.Comparison.Local != nil {
		t.Fatalf("offline parsing must not auto adopt an identity: %+v", result.Comparison)
	}
	if result.Comparison.MoviePilot.Category != "电视剧/临时剧集" || result.Local.MediaInfo.TMDBID != "" || result.Local.TMDBStatus != "not_identified" {
		t.Fatalf("MP2 draft or unverified ID isolation broken: %+v", result)
	}
}

func TestNormalizeMediaRecognitionSource(t *testing.T) {
	tests := map[string]string{
		"":           MediaRecognitionSourceShadow,
		"MP":         MediaRecognitionSourceMoviePilot,
		"moviepilot": MediaRecognitionSourceMoviePilot,
		"medianame":  MediaRecognitionSourceMedianame,
		"local":      MediaRecognitionSourceLocal,
		"FilmFusion": MediaRecognitionSourceLocal,
		"影子模式":       MediaRecognitionSourceShadow,
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

func TestMediaRecognitionShadowCategoryUsesMP2Path(t *testing.T) {
	if got := mediaRecognitionCategoryPath("tv", "国产剧集"); got != "电视剧/国产剧集" {
		t.Fatalf("category path=%q", got)
	}
	if got := mediaRecognitionCategoryPath("movie", "movie/华语电影"); got != "电影/华语电影" {
		t.Fatalf("legacy category path=%q", got)
	}
}

func TestMediaRecognitionShadowTreatsEquivalentProviderValuesAsEqual(t *testing.T) {
	seasonZero := 0
	comparison := buildMediaRecognitionShadowComparison(
		&MoviePilotMediaInfo{
			MediaType: "电影", Title: "乌云背后的幸福线", OriginalTitle: "Silver Linings Playbook",
			Year: "2012", TitleYear: "乌云背后的幸福线 (2012)", TmdbID: "82693",
			Category: "欧美电影", ResourceType: "BluRay", ResourcePix: "1080p",
			VideoEncode: "x264", BeginSeason: seasonZero, HasBeginSeason: true,
		},
		nil,
		&MoviePilotMediaInfo{
			MediaType: "movie", Title: "乌云背后的幸福线", OriginalTitle: "Silver Linings Playbook",
			Year: "2012", TitleYear: "乌云背后的幸福线 (2012)", TmdbID: "82693",
			Category: "欧美电影", ResourceType: "BluRay", ResourcePix: "1080p",
			VideoEncode: "H.264",
		},
		nil,
		false,
	)
	if !comparison.Matched || comparison.Status != MediaRecognitionShadowMatched || len(comparison.Differences) != 0 {
		t.Fatalf("comparison=%+v", comparison)
	}
	if comparison.MoviePilot.MediaType != "movie" || comparison.Local.MediaType != "movie" ||
		comparison.MoviePilot.VideoEncode != "H.264" || comparison.Local.VideoEncode != "H.264" {
		t.Fatalf("snapshots were not canonicalized: %+v", comparison)
	}
}

func TestTransferNameShadowRunsMedianameComparisonButKeepsMP2Result(t *testing.T) {
	const input = "[乌云背后的幸福线].Silver.Linings.Playbook.(2012).FIN.BluRay.1080p.x264.DDP.5.1.2Audios-CMCT.{tmdb-82693}.mkv"
	const mp2Name = "Silver Linings Playbook.2012.BluRay.1080p.x264.DDP 5.1 2Audios-CMCT.mkv"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/transfer/name":
			_, _ = w.Write([]byte(`{"success":true,"data":{"name":"` + mp2Name + `"}}`))
		case "/3/movie/82693":
			_, _ = w.Write([]byte(`{"id":82693,"title":"Silver Linings Playbook","original_title":"Silver Linings Playbook","release_date":"2012-01-01"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := newTestMoviePilotService(server.URL)
	cfg := &config.Config{TMDB: config.TMDBConfig{Enabled: true, BaseURL: server.URL, APIKey: "test"}}
	svc.SetLocalMediaRecognition(NewMediaRecognitionService(newMediaRecognitionTestDB(t), NewTMDBService(cfg, nil), nil))
	name, raw, err := svc.TransferNameWithSource(input, "mkv", MediaRecognitionSourceShadow)
	if err != nil || name != mp2Name {
		t.Fatalf("name=%q err=%v raw=%#v", name, err, raw)
	}
	comparison, ok := raw[mediaTransferShadowRawKey].(MediaTransferShadowComparison)
	if !ok || comparison.Matched || comparison.Status != MediaRecognitionShadowDifferent || comparison.Local == "" || comparison.LocalRaw["medianame"] == nil {
		t.Fatalf("comparison=%+v raw=%#v", comparison, raw)
	}
	extracted, ok := MediaTransferShadowFromRaw(raw)
	if !ok || extracted.Local != comparison.Local {
		t.Fatalf("transfer shadow extraction failed: %+v", extracted)
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

func TestMoviePilotKoreanEpisodeCategoryMatchesLocalShadow(t *testing.T) {
	const filename = "杀手妈咪.2026.EP14.HD1080P.X264.AAC.Korean.CHS.XLYS.mp4"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":3600}`))
		case "/api/v1/media/recognize_file":
			if got := r.URL.Query().Get("path"); got != filename {
				t.Errorf("recognition input=%q want=%q", got, filename)
			}
			_, _ = w.Write([]byte(`{
				"success":true,
				"data":{
					"media_info":{
						"type":"电视剧","title":"杀手妈咪","year":"2026","tmdb_id":123,
						"category":"日韩剧集","original_language":"ko","origin_country":["KR"],"genre_ids":[18]
					},
					"meta_info":{"season_episode":"S01E14","begin_season":1}
				}
			}`))
		case "/3/search/tv":
			_, _ = w.Write([]byte(`{"results":[{
				"id":123,"name":"杀手妈咪","first_air_date":"2026-01-01",
				"original_language":"ko","origin_country":["KR"],"genre_ids":[18]
			}]}`))
		case "/3/tv/123":
			_, _ = w.Write([]byte(`{
				"id":123,"name":"杀手妈咪","first_air_date":"2026-01-01",
				"original_language":"ko","origin_country":["KR"],"genre_ids":[18]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: server.URL, APIKey: "test-key", TimeoutSeconds: 2,
	}}, nil)
	local := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	if _, err := local.SaveCategoryConfig(`
电影:
  日韩电影:
    original_language: 'ja,ko'
  其他电影:
电视剧:
  日韩剧集:
    origin_country: 'JP,KP,KR,TH,IN,SG'
  其他剧集:
`); err != nil {
		t.Fatal(err)
	}
	svc := newTestMoviePilotService(server.URL)
	svc.SetLocalMediaRecognition(local)
	info, raw, err := svc.RecognizeFileWithSource(filename, MediaRecognitionSourceShadow)
	if err != nil {
		t.Fatal(err)
	}
	if info.MediaType != "电视剧" || info.Category != "日韩剧集" || info.SeasonEpisode != "S01E14" {
		t.Fatalf("unexpected primary recognition: %+v", info)
	}
	comparison, _, _, ok := MediaRecognitionShadowFromRaw(raw)
	if !ok || comparison.MoviePilot == nil || comparison.Local == nil {
		t.Fatalf("missing recognition snapshots: %+v", comparison)
	}
	for _, snapshot := range []*MediaRecognitionShadowSnapshot{comparison.MoviePilot, comparison.Local} {
		if snapshot.MediaType != "tv" || snapshot.Category != "电视剧/日韩剧集" || snapshot.SeasonEpisode != "S01E14" {
			t.Fatalf("unexpected %s snapshot: %+v", snapshot.Engine, snapshot)
		}
	}
	for _, difference := range comparison.Differences {
		if difference.Field == "category" {
			t.Fatalf("category should match: %+v", difference)
		}
	}
}

func TestShadowModeDoesNotReplaceMoviePilotFailureWithLocalResult(t *testing.T) {
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
	local, _ := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		details := medianameDetails(123, "Show", "tv")
		if strings.Contains(r.URL.Path, "/search/") {
			writeMedianameJSON(w, map[string]any{"page": 1, "total_pages": 1, "total_results": 1, "results": []any{details}})
		} else {
			writeMedianameJSON(w, details)
		}
	})
	svc.SetLocalMediaRecognition(local)
	for _, input := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv"} {
		info, raw, err := svc.RecognizeFile(input)
		if err == nil || info.Title != "" {
			t.Fatalf("input=%q info=%+v raw=%#v err=%v", input, info, raw, err)
		}
		comparison, ok := raw[mediaRecognitionShadowRawKey].(MediaRecognitionShadowComparison)
		if !ok || comparison.Status != MediaRecognitionShadowMoviePilotError || comparison.Local == nil {
			t.Fatalf("shadow comparison=%+v raw=%#v", comparison, raw)
		}
	}
	if recognitionRequests != 2 {
		t.Fatalf("recognition requests=%d want=2", recognitionRequests)
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
