package handler

import (
	"os"
	"path/filepath"
	"testing"

	"film-fusion/app/model"
)

func TestScanEmbyVersionCloudPathsDetectsMovieAndEpisodeVersions(t *testing.T) {
	root := t.TempDir()
	writeTestVersionFile(t, root, "Movies/The Matrix (1999) {tmdb-603}/The.Matrix.1999.2160p.strm")
	writeTestVersionFile(t, root, "Movies/The Matrix (1999) {tmdb-603}/The.Matrix.1999.1080p.strm")
	writeTestVersionFile(t, root, "TV/Dark (2017) {tmdb-70523}/Season 01/Dark.S01E01.2160p.strm")
	writeTestVersionFile(t, root, "TV/Dark (2017) {tmdb-70523}/Season 01/Dark.S01E01.1080p.strm")
	writeTestVersionFile(t, root, "TV/Dark (2017) {tmdb-70523}/Season 01/Dark.S01E02.1080p.strm")
	writeTestVersionFile(t, root, "Movies/The Matrix (1999) {tmdb-603}/Extras/trailer.mp4")

	result := scanEmbyVersionCloudPaths([]model.CloudPath{
		{ID: 7, SourcePath: "/media", LocalPath: root},
	}, "all")

	if result.TotalFiles != 5 {
		t.Fatalf("TotalFiles=%d want 5 result=%+v", result.TotalFiles, result)
	}
	if result.DuplicateMovieCount != 1 {
		t.Fatalf("DuplicateMovieCount=%d want 1 items=%+v", result.DuplicateMovieCount, result.Items)
	}
	if result.DuplicateEpisodeCount != 1 {
		t.Fatalf("DuplicateEpisodeCount=%d want 1 items=%+v", result.DuplicateEpisodeCount, result.Items)
	}

	var foundMovie, foundEpisode bool
	for _, item := range result.Items {
		switch item.MediaType {
		case "movie":
			foundMovie = item.TmdbID == "603" && item.VersionCount == 2
		case "episode":
			foundEpisode = item.TmdbID == "70523" && item.Season == 1 && item.Episode == 1 && item.VersionCount == 2
		}
	}
	if !foundMovie {
		t.Fatalf("movie duplicate not found: %+v", result.Items)
	}
	if !foundEpisode {
		t.Fatalf("episode duplicate not found: %+v", result.Items)
	}
}

func TestScanEmbyVersionCloudPathsParsesChineseSeasonEpisode(t *testing.T) {
	root := t.TempDir()
	writeTestVersionFile(t, root, "剧集/漫长的季节/第1季/第01集.2160p.strm")
	writeTestVersionFile(t, root, "剧集/漫长的季节/第1季/第01集.1080p.strm")
	writeTestVersionFile(t, root, "剧集/漫长的季节/第1季/第02集.1080p.strm")

	result := scanEmbyVersionCloudPaths([]model.CloudPath{
		{ID: 9, SourcePath: "/tv", LocalPath: root},
	}, "tv")

	if result.DuplicateEpisodeCount != 1 {
		t.Fatalf("DuplicateEpisodeCount=%d want 1 items=%+v", result.DuplicateEpisodeCount, result.Items)
	}
	item := result.Items[0]
	if item.Title != "漫长的季节" || item.Season != 1 || item.Episode != 1 || item.VersionCount != 2 {
		t.Fatalf("unexpected duplicate item=%+v", item)
	}
}

func writeTestVersionFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
