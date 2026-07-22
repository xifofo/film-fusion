package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
)

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

func newTestMoviePilotService(api string) *MoviePilotService {
	return NewMoviePilotService(&config.Config{MoviePilot: config.MoviePilotConfig{
		API: api, Username: "user", Password: "pass",
	}}, nil)
}
