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

func TestNormalizeMediaRecognitionSource(t *testing.T) {
	tests := map[string]string{
		"":           MediaRecognitionSourceMedianame,
		"MP":         MediaRecognitionSourceMedianame,
		"moviepilot": MediaRecognitionSourceMedianame,
		"medianame":  MediaRecognitionSourceMedianame,
		"local":      MediaRecognitionSourceMedianame,
		"FilmFusion": MediaRecognitionSourceMedianame,
		"影子模式":       MediaRecognitionSourceMedianame,
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
