package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/config"
	"github.com/xifofo/medianame"
	"github.com/xifofo/medianame/rename"
	mtmdb "github.com/xifofo/medianame/tmdb"
)

func medianameFixture(t *testing.T, respond http.HandlerFunc) (*MediaRecognitionService, *config.Config) {
	t.Helper()
	server := httptest.NewServer(respond)
	t.Cleanup(server.Close)
	cfg := &config.Config{TMDB: config.TMDBConfig{Enabled: true, BaseURL: server.URL + "/3/", APIKey: "test-key", TimeoutSeconds: 2, CacheMinutes: 5}}
	return NewMediaRecognitionService(newMediaRecognitionTestDB(t), NewTMDBService(cfg, nil), nil), cfg
}
func medianameDetails(id int, title, kind string) map[string]any {
	data := map[string]any{"id": id, "title": title, "original_title": title, "release_date": "2024-01-02", "original_language": "en", "external_ids": map[string]any{"imdb_id": "tt1234567", "tvdb_id": 777}, "genres": []any{map[string]any{"id": 18, "name": "Drama"}}}
	if kind == "tv" {
		data["name"] = title
		data["original_name"] = title
		data["first_air_date"] = "2024-01-02"
		data["origin_country"] = []string{"US"}
		data["seasons"] = []any{map[string]any{"season_number": 0, "episode_count": 3, "air_date": "2023-01-01"}}
	}
	return data
}
func writeMedianameJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func recognizeMN(s *MediaRecognitionService, input string) MedianameRecognition {
	return s.RecognizeMedianame(context.Background(), input, MediaRecognitionOptions{Mode: "file", LookupTMDB: true})
}

func TestMedianameIDsFallbackAndErrors(t *testing.T) {
	tests := []struct {
		name, input, find string
		status            mtmdb.Status
		search            bool
		details           bool
	}{
		{"tmdb priority", "Example.2024.[tmdb=123].mkv", "", mtmdb.StatusCompatible, false, true},
		{"imdb map", "Example.2024.[imdb=tt1234567].mkv", "hit", mtmdb.StatusCompatible, false, true},
		{"tvdb map", "Example.2024.S00E01.[tvdb=777].mkv", "tv", mtmdb.StatusCompatible, false, true},
		{"unmapped fallback", "Example.2024.[imdb=tt1234567].mkv", "empty", mtmdb.StatusCompatible, true, true},
		{"external network error", "Example.2024.[imdb=tt1234567].mkv", "error", mtmdb.StatusRequestError, false, false},
		{"id missing never accepted", "Example.2024.[tmdb=404].mkv", "", mtmdb.StatusNotIdentified, false, true},
		{"conflicting external id", "Example.2024.[tmdb=123].[imdb=tt9999999].mkv", "", mtmdb.StatusReview, false, true},
		{"ambiguous candidates", "Unknown.2024.mkv", "ambiguous", mtmdb.StatusAmbiguous, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			searched, details := false, false
			s, _ := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/3/3/") {
					t.Fatal("API /3 duplicated")
				}
				if r.URL.Query().Get("api_key") != "test-key" {
					t.Error("credential not reused")
				}
				switch {
				case strings.HasPrefix(r.URL.Path, "/3/find/"):
					if test.find == "error" {
						http.Error(w, "upstream failed", 503)
						return
					}
					movie, tv := []any{}, []any{}
					if test.find == "hit" {
						movie = append(movie, medianameDetails(123, "Example", "movie"))
					}
					if test.find == "tv" {
						tv = append(tv, medianameDetails(123, "Example", "tv"))
					}
					writeMedianameJSON(w, map[string]any{"movie_results": movie, "tv_results": tv, "tv_episode_results": []any{}, "tv_season_results": []any{}})
				case strings.HasPrefix(r.URL.Path, "/3/search/"):
					searched = true
					results := []any{medianameDetails(123, "Example", "movie")}
					if test.find == "ambiguous" {
						results = append(results, medianameDetails(124, "Different", "movie"))
					}
					writeMedianameJSON(w, map[string]any{"page": 1, "total_pages": 1, "total_results": len(results), "results": results})
				case strings.HasPrefix(r.URL.Path, "/3/movie/") || strings.HasPrefix(r.URL.Path, "/3/tv/"):
					details = true
					if strings.HasSuffix(r.URL.Path, "/404") {
						http.NotFound(w, r)
						return
					}
					kind := "movie"
					if strings.Contains(r.URL.Path, "/tv/") {
						kind = "tv"
					}
					id, title := 123, "Example"
					if strings.HasSuffix(r.URL.Path, "/124") {
						id, title = 124, "Different"
					}
					writeMedianameJSON(w, medianameDetails(id, title, kind))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			got := recognizeMN(s, test.input)
			if got.Result == nil || got.Result.Status != test.status {
				t.Fatalf("status: %+v", got)
			}
			if searched != test.search || details != test.details {
				t.Fatalf("search=%v details=%v", searched, details)
			}
			info, err := MedianameMoviePilotInfo(got)
			if test.status == mtmdb.StatusCompatible {
				if err != nil || info.TmdbID != "123" {
					t.Fatalf("compatible adaptation: %+v %v", info, err)
				}
			} else {
				if err == nil || info.TmdbID != "" || got.Result.MediaInfo != nil {
					t.Fatalf("unverified candidate auto adopted: %+v %v", info, err)
				}
				if test.status == mtmdb.StatusReview && len(got.Result.Candidates[0].Conflicts) == 0 {
					t.Fatal("conflict evidence missing")
				}
			}
		})
	}
}
func TestMedianameParsingAndNamingStayInBranch(t *testing.T) {
	s, _ := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/3/tv/123/season/0" {
			writeMedianameJSON(w, map[string]any{"air_date": "2023-01-01", "episodes": []any{map[string]any{"episode_number": 0, "name": "Special Zero", "air_date": "2023-01-02"}}})
			return
		}
		writeMedianameJSON(w, medianameDetails(123, "Example", "tv"))
	})
	old, err := s.SaveCategoryConfig("电影:\n  MP2电影:\n电视剧:\n  MP2剧集:\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveMediaRenameConfig("OLD/{{title}}{{fileExt}}", "OLD/{{title}}{{fileExt}}"); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.LoadMedianameConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MovieTemplate != rename.DefaultMovieTemplate || cfg.TVTemplate != rename.DefaultTVTemplate {
		t.Fatal("defaults differ from package")
	}
	cfg.CategoryYAML = "movie:\n  NEW电影:\ntv:\n  NEW剧集:\n"
	if _, err = s.SaveMedianameConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"/tv/Example (2024) [tmdb=123]/Season 0/00.mkv", "Example.2024.S00E01E03E05.[tmdb=123].mkv", "Example.2024.S00E01-E03.[tmdb=123].mkv"} {
		result := recognizeMN(s, input)
		if err := medianameCompatible(result); err != nil {
			t.Fatal(err)
		}
		if result.Result.MetaInfo.Season == nil || *result.Result.MetaInfo.Season != 0 {
			t.Fatalf("special season lost: %+v", result)
		}
		target, vars, err := s.MedianameTarget(context.Background(), result, "/影视/", true, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(target, "/影视/NEW剧集/Example (2024) {tmdb-123}/Season 00/") || strings.Contains(target, "OLD") || vars["total_episodes"] != 3 {
			t.Fatalf("target=%s context=%v", target, vars)
		}
		target, _, err = s.MedianameTarget(context.Background(), result, "影视", false, "手动")
		if err != nil || strings.Contains(target, "手动") || strings.Contains(target, "NEW剧集") {
			t.Fatalf("classification toggle: %s %v", target, err)
		}
		target, _, err = s.MedianameTarget(context.Background(), result, "影视", true, "手动")
		if err != nil || strings.Count(target, "/手动/") != 1 || strings.Count(target, "/Season 00/") != 1 {
			t.Fatalf("directory composition: %s %v", target, err)
		}
	}
	loaded, _ := s.LoadCategoryConfig()
	if loaded.YAML != old.YAML {
		t.Fatal("medianame overwrote MP2 categories")
	}
	formats, _ := s.LoadMediaRenameConfig()
	if formats.MovieFormat != "OLD/{{title}}{{fileExt}}" {
		t.Fatal("medianame overwrote MP2 rename")
	}
	cfg.TVTemplate = "{{.title}}/{{.season_episode}} - {{.episode_title}}{{.fileExt}}"
	if _, err = s.SaveMedianameConfig(cfg); err != nil {
		t.Fatal(err)
	}
	target, _, err := s.MedianameTarget(context.Background(), recognizeMN(s, "Example.S00E00.[tmdb=123].mkv"), "影视", false, "")
	if err != nil || !strings.Contains(target, "S00E00 - Special Zero.mkv") {
		t.Fatalf("supplements: %s %v", target, err)
	}
	invalid := cfg
	invalid.CategoryYAML = "movie:\n  ../escape:\ntv:\n  TV:\n"
	if err = ValidateMedianameConfig(invalid); err == nil {
		t.Fatal("accepted classification directory traversal")
	}
	if _, err = PreviewMedianameTemplate("tv", "{{title}}{% if season %}x{% endif %}", nil); err == nil {
		t.Fatal("accepted Jinja as Go")
	}
	if _, err = PreviewMedianameTemplate("tv", "{{.category}}/{{.title}}.mkv", nil); err == nil {
		t.Fatal("template may duplicate classification")
	}
}
func TestMedianameRangesWarningsAndParseModes(t *testing.T) {
	s := NewMediaRecognitionService(newMediaRecognitionTestDB(t), nil, nil)
	for _, test := range []struct {
		input string
		check func(medianame.Info) bool
	}{
		{"/tv/Example (2024) [tmdb=123]/Season 0/01.mkv", func(p medianame.Info) bool {
			return p.IDs.TMDB == "123" && p.Title == "Example" && p.Season != nil && *p.Season == 0 && p.Episode != nil && *p.Episode == 1
		}},
		{"Example.S01E01E03E05.mkv", func(p medianame.Info) bool { return len(p.Episodes) == 3 && p.Episodes[1] == 3 }},
		{"Example.S01E10-S02E03.mkv", func(p medianame.Info) bool {
			return p.SeasonEnd != nil && *p.SeasonEnd == 2 && p.EpisodeEnd != nil && *p.EpisodeEnd == 3
		}},
		{"Example.S01-S03.mkv", func(p medianame.Info) bool { return p.SeasonEnd != nil && *p.SeasonEnd == 3 }},
		{"Example (2024) - Example.2023.mkv", func(p medianame.Info) bool { return len(p.Warnings) > 0 }},
	} {
		r := s.RecognizeMedianame(context.Background(), test.input, MediaRecognitionOptions{Mode: "file"})
		if r.Error != "" || !test.check(r.Result.MetaInfo) {
			t.Fatalf("%s: %+v", test.input, r)
		}
	}
	title := s.RecognizeMedianame(context.Background(), "01.mkv", MediaRecognitionOptions{Mode: "title"})
	if title.Result.MetaInfo.Episode != nil {
		t.Fatal("Parse title incorrectly inherited a path episode")
	}
}
func TestMedianameClientCacheAndFrozenConfiguration(t *testing.T) {
	var calls atomic.Int32
	s, cfg := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeMedianameJSON(w, medianameDetails(123, "Example", "movie"))
	})
	r := recognizeMN(s, "Example.2024.[tmdb=123].mkv")
	if err := medianameCompatible(r); err != nil {
		t.Fatal(err)
	}
	recognizeMN(s, "Example.2024.[tmdb=123].mkv")
	if calls.Load() != 1 {
		t.Fatalf("cache missed: %d", calls.Load())
	}
	oldClient := s.tmdb.medianameAPI()
	cfg.TMDB.AccessToken = "new-token"
	recognizeMN(s, "Example.2024.[tmdb=123].mkv")
	if calls.Load() != 2 || oldClient == s.tmdb.medianameAPI() {
		t.Fatal("configuration change did not replace client")
	}
	mp := NewMoviePilotService(cfg, nil)
	mp.SetLocalMediaRecognition(s)
	frozen, snapshot, err := mp.FreezeMediaPipeline("medianame")
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := s.LoadMedianameConfig()
	updated.MovieTemplate = "CHANGED/{{.title}}{{.fileExt}}"
	if _, err = s.SaveMedianameConfig(updated); err != nil {
		t.Fatal(err)
	}
	cfg.MediaRecognition.Source = "moviepilot"
	cfg.TMDB.AccessToken = "another-token"
	got := frozen.RecognizeMedianame(context.Background(), "Example.2024.[tmdb=123].mkv", MediaRecognitionOptions{LookupTMDB: true})
	target, _, err := frozen.MedianameTarget(context.Background(), got, "root", true, "")
	if err != nil || strings.Contains(target, "CHANGED") || snapshot.Source != "medianame" || snapshot.ConfigVersion == "" {
		t.Fatalf("frozen target: %s %v", target, err)
	}
	if calls.Load() != 2 {
		t.Fatal("in-flight snapshot lost its shared client cache")
	}
	serialized, _ := json.Marshal(snapshot)
	if strings.Contains(string(serialized), "token") || strings.Contains(string(serialized), "test-key") {
		t.Fatal("snapshot leaked credentials")
	}
}

func TestMedianameCustomCrossSeasonTemplate(t *testing.T) {
	s, _ := medianameFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeMedianameJSON(w, medianameDetails(123, "Example", "tv"))
	})
	r := recognizeMN(s, "Example.2024.S01E10-S02E03.[tmdb=123].mkv")
	if _, _, err := s.MedianameTarget(context.Background(), r, "影视", false, ""); err == nil {
		t.Fatal("default template silently chose one season")
	}
	cfg, _ := s.LoadMedianameConfig()
	cfg.TVTemplate = "{{.title_year}}/{{.season_episode}}{{.fileExt}}"
	if _, err := s.SaveMedianameConfig(cfg); err != nil {
		t.Fatal(err)
	}
	target, _, err := s.MedianameTarget(context.Background(), r, "影视", false, "")
	if err != nil || !strings.Contains(target, "S01E10-S02E03") {
		t.Fatal(fmt.Sprint(target, err))
	}
}
