package handler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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

func TestEmbyVersionCheckJobRunsInBackgroundAndKeepsResult(t *testing.T) {
	root := t.TempDir()
	writeTestVersionFile(t, root, "Movies/Dune (2021) {tmdb-438631}/Dune.2160p.strm")
	writeTestVersionFile(t, root, "Movies/Dune (2021) {tmdb-438631}/Dune.1080p.strm")

	handler := NewEmbyVersionCheckHandler(nil)
	job, started := handler.startJob(42, EmbyVersionCheckRequest{
		CloudPathIDs: []uint{7},
		MediaType:    "movie",
	}, "movie", []model.CloudPath{{ID: 7, SourcePath: "/movies", LocalPath: root}})
	if !started || job == nil || !job.Running {
		t.Fatalf("background job did not start: started=%v job=%+v", started, job)
	}
	if otherUserJob := handler.jobSnapshot(43); otherUserJob != nil {
		t.Fatalf("job leaked to another user: %+v", otherUserJob)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := handler.jobSnapshot(42)
		if snapshot != nil && !snapshot.Running {
			if snapshot.Status != "completed" || snapshot.Result == nil {
				t.Fatalf("unexpected completed job: %+v", snapshot)
			}
			if snapshot.Result.DuplicateMovieCount != 1 {
				t.Fatalf("DuplicateMovieCount=%d want 1", snapshot.Result.DuplicateMovieCount)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background job did not finish before timeout")
}

func TestEmbyVersionCheckScheduleRunsSavedUserScope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "emby-version-check.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.CloudStorage{},
		&model.CloudPath{},
		&model.EmbyVersionCheckSetting{},
	); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	root := t.TempDir()
	writeTestVersionFile(t, root, "Movies/Dune (2021) {tmdb-438631}/Dune.2160p.strm")
	writeTestVersionFile(t, root, "Movies/Dune (2021) {tmdb-438631}/Dune.1080p.strm")
	path := model.CloudPath{UserID: 42, SourcePath: "/movies", LocalPath: root, LinkType: model.LinkTypeStrm}
	if err := db.Create(&path).Error; err != nil {
		t.Fatalf("create cloud path: %v", err)
	}

	handler := &EmbyVersionCheckHandler{
		jobs:      make(map[uint]*EmbyVersionCheckJob),
		scheduler: newEmbyVersionCheckScheduler(db),
	}
	t.Cleanup(handler.Stop)

	setting, err := handler.updateEmbyVersionCheckSetting(42, model.EmbyVersionCheckSetting{
		ScheduleEnabled: true,
		Cron:            "0 4 * * *",
		CloudPathIDs:    []uint{path.ID},
		MediaType:       "movie",
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if !setting.ScheduleEnabled || setting.MediaType != "movie" || len(setting.CloudPathIDs) != 1 || setting.CloudPathIDs[0] != path.ID {
		t.Fatalf("unexpected saved setting: %+v", setting)
	}
	if handler.scheduler.cron == nil || len(handler.scheduler.cron.Entries()) != 1 {
		t.Fatalf("saved schedule was not registered")
	}

	handler.runScheduledEmbyVersionCheck(42)
	deadline := time.Now().Add(2 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		job := handler.jobSnapshot(42)
		if job != nil && !job.Running {
			if job.Status != "completed" || job.Result == nil || job.Result.DuplicateMovieCount != 1 {
				t.Fatalf("unexpected scheduled job: %+v", job)
			}
			latest, getErr := handler.getOrCreateEmbyVersionCheckSetting(42)
			if getErr != nil {
				t.Fatalf("reload setting: %v", getErr)
			}
			if latest.LastStatus != "success" || latest.LastScanAt == nil || latest.LastTotalFiles != 2 || latest.LastDuplicateMovies != 1 {
				t.Fatalf("scheduled result was not persisted: %+v", latest)
			}
			completed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !completed {
		t.Fatal("scheduled version check did not finish before timeout")
	}

	// A removed mapping must not prevent the user from turning a stale schedule off.
	if err := db.Delete(&path).Error; err != nil {
		t.Fatalf("delete cloud path: %v", err)
	}
	setting.ScheduleEnabled = false
	if _, err := handler.updateEmbyVersionCheckSetting(42, *setting); err != nil {
		t.Fatalf("disable schedule with stale path: %v", err)
	}
	if handler.scheduler.cron != nil {
		t.Fatal("disabled schedule remained registered")
	}
}

func TestEmbyVersionCheckCronAcceptsFiveAndSixFields(t *testing.T) {
	for _, expr := range []string{"0 4 * * *", "0 0 */6 * * *"} {
		if _, err := embyVersionCheckCronParser.Parse(expr); err != nil {
			t.Fatalf("cron %q should be accepted: %v", expr, err)
		}
	}
	if _, err := embyVersionCheckCronParser.Parse("not-a-cron"); err == nil {
		t.Fatal("invalid cron should be rejected")
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
