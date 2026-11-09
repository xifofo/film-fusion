package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/utils/embyhelper"
)

const greatestRelease = "The.Greatest.S01E06.2160p.AMZN.WEB-DL.DDP5.1.Atmos.DV.H.265-PFWEB.mkv"

func TestEmbyLibraryReleaseNames(t *testing.T) {
	for _, tc := range []struct {
		input, name string
		year        int
		series      bool
	}{
		{greatestRelease, "The Greatest", 0, true},
		{"The.Greatest.S01E07", "The Greatest", 0, true},
		{"三体.S01E01.1080p.WEB-DL.mkv", "三体", 0, true},
		{"Movie.2024.1080p.WEB-DL.mkv", "Movie (2024)", 2024, false},
		{"Movie.2024.1080p.WEB-DL", "Movie (2024)", 2024, false},
		{"Alien (1979)", "Alien (1979)", 1979, false},
		{"Mr.Robot", "Mr.Robot", 0, false},
		{"1917.mp4", "1917", 0, false},
		{"2001.mkv", "2001", 0, false},
		{"Blade.Runner.2049.mkv", "Blade.Runner.2049", 0, false},
		{"Avatar.2.mkv", "Avatar.2", 0, false},
		{"Version.1080p", "Version.1080p", 0, false},
		{"A.2025", "A.2025", 0, false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			query := parseEmbyLibraryQuery(tc.input)
			if query.displayName() != tc.name || query.year != tc.year || query.series != tc.series {
				t.Fatalf("query=%+v, display=%q", query, query.displayName())
			}
		})
	}

	response, err := prepareEmbyLibraryCheck([]string{greatestRelease, "The.Greatest.S01E07.mkv", "The Greatest", "Movie (2023)", "Movie (2024)"})
	if err != nil || response.Total != 4 || response.DuplicatesRemoved != 1 {
		t.Fatalf("different years and explicit series must remain independent: %+v, %v", response, err)
	}
}

func TestEmbyLibraryCandidateRelevance(t *testing.T) {
	for _, tc := range []struct {
		query, candidate string
		want             bool
	}{
		{"The Greatest", "Greatest Hits", false},
		{"The Greatest", "The Greatest Showman", true},
		{"The Greatest", "The Greatest of All Time", false},
		{"The Greatest", "最佳精选", false},
		{"Possible", "Possible 2", true},
		{"Alien", "Aliens", false},
		{"It", "Titanic", false},
		{"The", "The Greatest", false},
		{"Mission Impossible", "Mission to Mars", false},
		{"Movie 2", "Movie 3", false},
		{"三体", "三体2", true},
		{"三体", "三生三世十里桃花", false},
		{"流浪地球", "流浪地球2", true},
		{"Ｍｒ.Ｒｏｂｏｔ", "Mr Robot 2", true},
		{"Movie", "", false},
	} {
		t.Run(tc.query+"/"+tc.candidate, func(t *testing.T) {
			if got := relatedEmbyLibraryTitle(tc.query, tc.candidate); got != tc.want {
				t.Fatalf("related=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestEmbyLibraryCheckFiltersUnrelatedSearchResults(t *testing.T) {
	for _, tc := range []struct {
		name, input, term, status string
		items                     []embyhelper.LibrarySearchItem
		ids                       []string
	}{
		{
			name: "reported release with unrelated movies and series", input: greatestRelease, term: "The Greatest", status: "missing",
			items: []embyhelper.LibrarySearchItem{
				{ID: "hits", Name: "最佳精选", OriginalTitle: "The Greatest Hits", Type: "Movie", ProductionYear: 2024},
				{ID: "showman", Name: "马戏之王", OriginalTitle: "The Greatest Showman", Type: "Movie", ProductionYear: 2017},
				{ID: "same-movie", Name: "The Greatest", Type: "Movie"},
				{ID: "other-series", Name: "完全无关的剧集", OriginalTitle: "A Different Series", Type: "Series"},
			},
		},
		{
			name: "exact original series title", input: greatestRelease, term: "The Greatest", status: "found", ids: []string{"series"},
			items: []embyhelper.LibrarySearchItem{
				{ID: "movie", Name: "The Greatest", Type: "Movie"},
				{ID: "series", Name: "剧集译名", OriginalTitle: "The Greatest", Type: "Series"},
			},
		},
		{
			name: "bare title unrelated results", input: "The Greatest", term: "The Greatest", status: "missing",
			items: []embyhelper.LibrarySearchItem{
				{ID: "hits", Name: "最佳精选", OriginalTitle: "Greatest Hits", Type: "Movie"},
				{ID: "other", Name: "完全无关", Type: "Movie"},
			},
		},
		{
			name: "related original title only", input: "Possible", term: "Possible", status: "possible", ids: []string{"sequel"},
			items: []embyhelper.LibrarySearchItem{
				{ID: "noise", Name: "Unrelated", Type: "Movie"},
				{ID: "sequel", Name: "续作", OriginalTitle: "Possible 2", Type: "Movie"},
			},
		},
		{
			name: "year conflict remains unconfirmed", input: "Movie.2024.1080p.WEB-DL.mkv", term: "Movie", status: "possible", ids: []string{"old"},
			items: []embyhelper.LibrarySearchItem{{ID: "old", Name: "Movie", Type: "Movie", ProductionYear: 2023}},
		},
		{
			name: "same title and year", input: "Movie.2024.1080p.WEB-DL.mkv", term: "Movie", status: "found", ids: []string{"new"},
			items: []embyhelper.LibrarySearchItem{
				{ID: "old", Name: "Movie", Type: "Movie", ProductionYear: 2023},
				{ID: "new", Name: "Movie", Type: "Movie", ProductionYear: 2024},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if term := r.URL.Query().Get("SearchTerm"); term != tc.term {
					t.Errorf("search term=%q, want %q", term, tc.term)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"Items": tc.items, "TotalRecordCount": len(tc.items)})
			}))
			defer upstream.Close()
			client := embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "test"}})
			defer client.Close()
			response, err := NewEmbyLibraryCheckService(client).Check(context.Background(), []string{tc.input})
			if err != nil {
				t.Fatal(err)
			}
			result := response.Results[0]
			var ids []string
			for _, match := range result.Matches {
				ids = append(ids, match.ID)
			}
			if result.Status != tc.status || !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("result=%+v, want status=%s IDs=%v", result, tc.status, tc.ids)
			}
		})
	}
}

func TestEmbyLibraryUnrelatedTruncatedSearchIsNotPossibleOrMissing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		items := make([]embyhelper.LibrarySearchItem, 100)
		for i := range items {
			items[i] = embyhelper.LibrarySearchItem{ID: strconv.Itoa(start + i + 1), Name: "Unrelated", Type: "Series"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 600})
	}))
	defer upstream.Close()
	client := embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "test"}})
	defer client.Close()
	response, err := NewEmbyLibraryCheckService(client).Check(context.Background(), []string{greatestRelease})
	if err != nil {
		t.Fatal(err)
	}
	result := response.Results[0]
	if result.Status != "error" || !result.Truncated || result.Error == "" || len(result.Matches) != 0 {
		t.Fatalf("incomplete search misclassified: %+v", result)
	}
}
