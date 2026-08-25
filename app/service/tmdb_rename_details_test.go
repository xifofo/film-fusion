package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"film-fusion/app/config"
)

func TestMediaRenameLoadsAndCachesTMDBEpisodeDetailsOnlyWhenTemplateUsesThem(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/3/tv/136315/season/1" || r.URL.Query().Get("api_key") != "test-key" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"air_date":"2023-01-15",
			"episodes":[
				{"episode_number":2,"name":"科学边界","air_date":"2023-01-16"},
				{"episode_number":3,"name":"宇宙闪烁","air_date":"2023-01-17"}
			]
		}`))
	}))
	defer server.Close()

	tmdb := NewTMDBService(&config.Config{TMDB: config.TMDBConfig{
		Enabled: true, BaseURL: server.URL, APIKey: "test-key", TimeoutSeconds: 2, CacheMinutes: 30,
	}}, nil)
	details, err := tmdb.GetTVEpisodeRenameDetails(context.Background(), "136315", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if details.SeasonYear != "2023" || details.EpisodeTitle != "科学边界" || details.EpisodeDate != "2023-01-16" {
		t.Fatalf("details=%+v", details)
	}
	second, err := tmdb.GetTVEpisodeRenameDetails(context.Background(), "136315", 1, 3)
	if err != nil || second.EpisodeTitle != "宇宙闪烁" || calls.Load() != 1 {
		t.Fatalf("cached details=%+v calls=%d err=%v", second, calls.Load(), err)
	}

	rename := NewMediaRecognitionService(newMediaRecognitionTestDB(t), tmdb, nil)
	if _, err := rename.SaveMediaRenameConfig(
		DefaultMovieRenameFormat,
		`{{title}} ({{season_year}})/{{season_fmt}}/{{season_episode}} - {{episode_title}} - {{episode_date}}{{fileExt}}`,
	); err != nil {
		t.Fatal(err)
	}
	target, active, err := rename.BuildMediaRenameTargetPath("国产剧", MediaRenameInput{
		Info: MoviePilotMediaInfo{
			MediaType: "tv", Title: "三体", TmdbID: "136315", SeasonEpisode: "S01E02",
			BeginSeason: 1, HasBeginSeason: true,
		},
		MediaType: "tv", OriginalName: "Three-Body.S01E02.mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !active || target != "/国产剧/三体 (2023)/S01/S01E02 - 科学边界 - 2023-01-16.mkv" {
		t.Fatalf("target=%q active=%v", target, active)
	}
	if calls.Load() != 1 {
		t.Fatalf("TMDB cache missed, calls=%d", calls.Load())
	}
}
