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
	if len(result.Candidates) != 1 || result.Candidates[0].Confidence != 1 {
		t.Fatalf("candidates=%+v", result.Candidates)
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
