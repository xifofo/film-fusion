package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newEmbyMissingTestService(t *testing.T) *EmbyMissingService {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "emby-missing.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.EmbyMissingEpisode{},
		&model.EmbyMissingSeriesScan{},
		&model.EmbyMissingBlacklist{},
		&model.EmbyMissingSetting{},
	); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return &EmbyMissingService{db: db}
}

func TestDeleteMissingSeriesOnlyRemovesTargetRecords(t *testing.T) {
	svc := newEmbyMissingTestService(t)
	for _, id := range []string{"target", "other"} {
		for _, row := range []any{
			&model.EmbyMissingEpisode{SeriesID: id, SeasonNumber: 1, EpisodeNumber: 1},
			&model.EmbyMissingEpisode{SeriesID: id, SeasonNumber: 2, EpisodeNumber: 2},
			&model.EmbyMissingSeriesScan{SeriesID: id},
			&model.EmbyMissingBlacklist{SeriesID: id},
		} {
			if err := svc.db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 2 { // 重复删除保持幂等。
		if err := svc.DeleteSeries(t.Context(), " target "); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []struct {
		model any
		id    string
		want  int64
	}{
		{&model.EmbyMissingEpisode{}, "target", 0},
		{&model.EmbyMissingSeriesScan{}, "target", 0},
		{&model.EmbyMissingBlacklist{}, "target", 1},
		{&model.EmbyMissingEpisode{}, "other", 2},
		{&model.EmbyMissingSeriesScan{}, "other", 1},
		{&model.EmbyMissingBlacklist{}, "other", 1},
	} {
		var count int64
		if err := svc.db.Model(check.model).Where("series_id = ?", check.id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Fatalf("%T %s count = %d; want %d", check.model, check.id, count, check.want)
		}
	}
	result, err := svc.ListMissing()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 1 || result.Setting.LastSeriesCount != 1 || result.Setting.LastMissingCount != 2 {
		t.Fatalf("unexpected list after deletion: %#v", result)
	}
}

func TestDeleteMissingSeriesRejectsEmptyIDAndActiveScan(t *testing.T) {
	svc := newEmbyMissingTestService(t)
	if err := svc.db.Create(&model.EmbyMissingEpisode{SeriesID: "target"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSeries(t.Context(), "  "); err == nil {
		t.Fatal("empty ID should fail")
	}
	svc.scanning = true
	if err := svc.DeleteSeries(t.Context(), "target"); !errors.Is(err, ErrEmbyMissingScanInProgress) {
		t.Fatalf("active scan error = %v", err)
	}
	var count int64
	if err := svc.db.Model(&model.EmbyMissingEpisode{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("snapshot changed: count=%d error=%v", count, err)
	}
}

func TestDeleteMissingSeriesRollsBackOnFailure(t *testing.T) {
	svc := newEmbyMissingTestService(t)
	if err := svc.db.Create(&model.EmbyMissingEpisode{SeriesID: "target"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Create(&model.EmbyMissingSeriesScan{SeriesID: "target"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Exec(`CREATE TRIGGER reject_scan_delete BEFORE DELETE ON emby_missing_series_scans
		BEGIN SELECT RAISE(ABORT, 'simulated delete failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSeries(t.Context(), "target"); err == nil {
		t.Fatal("delete should fail")
	}
	for _, row := range []any{&model.EmbyMissingEpisode{}, &model.EmbyMissingSeriesScan{}} {
		var count int64
		if err := svc.db.Model(row).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%T not rolled back: count=%d error=%v", row, count, err)
		}
	}
}

func TestFetchSeriesMissingIgnoresSeasonZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Shows/Missing" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("ParentId"); got != "series-1" {
			t.Errorf("ParentId = %q; want series-1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"Items": [
				{"Name":"Special","ParentIndexNumber":0,"IndexNumber":1},
				{"Name":"Regular","ParentIndexNumber":1,"IndexNumber":2}
			],
			"TotalRecordCount": 2
		}`)
	}))
	defer server.Close()

	cfg := &config.Config{Emby: config.EmbyConfig{
		URL:         server.URL,
		AdminUserID: "admin-1",
	}}
	svc := &EmbyMissingService{emby: embyhelper.New(cfg)}

	rows, err := svc.fetchSeriesMissing(missingSeriesRef{
		seriesID:    "series-1",
		seriesName:  "Series",
		libraryID:   "library-1",
		libraryName: "TV",
	}, ScanOptions{IncludeSpecials: true})
	if err != nil {
		t.Fatalf("fetch missing episodes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("row count = %d; want 1", len(rows))
	}
	if rows[0].SeasonNumber != 1 || rows[0].EpisodeNumber != 2 {
		t.Fatalf("unexpected row: %#v", rows[0])
	}
}

func TestEmbyMissingSnapshotCountsIgnoreSeasonZero(t *testing.T) {
	svc := newEmbyMissingTestService(t)
	rows := []model.EmbyMissingEpisode{
		{SeriesID: "specials-only", SeriesName: "Specials Only", SeasonNumber: 0, EpisodeNumber: 1},
		{SeriesID: "mixed", SeriesName: "Mixed", SeasonNumber: 0, EpisodeNumber: 1},
		{SeriesID: "mixed", SeriesName: "Mixed", SeasonNumber: 1, EpisodeNumber: 2},
		{SeriesID: "regular", SeriesName: "Regular", SeasonNumber: 2, EpisodeNumber: 3},
	}
	if err := svc.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed missing episodes: %v", err)
	}

	seriesCount, missingCount, err := svc.snapshotCounts()
	if err != nil {
		t.Fatalf("snapshot counts: %v", err)
	}
	if seriesCount != 2 || missingCount != 2 {
		t.Fatalf("counts = series %d, missing %d; want series 2, missing 2", seriesCount, missingCount)
	}
}

func TestEmbyMissingListIgnoresSeasonZero(t *testing.T) {
	svc := newEmbyMissingTestService(t)
	rows := []model.EmbyMissingEpisode{
		{SeriesID: "specials-only", SeriesName: "Specials Only", SeasonNumber: 0, EpisodeNumber: 1},
		{SeriesID: "mixed", SeriesName: "Mixed", SeasonNumber: 0, EpisodeNumber: 1},
		{SeriesID: "mixed", SeriesName: "Mixed", SeasonNumber: 1, EpisodeNumber: 2},
	}
	if err := svc.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed missing episodes: %v", err)
	}

	result, err := svc.ListMissing()
	if err != nil {
		t.Fatalf("list missing episodes: %v", err)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("group count = %d; want 1", len(result.Groups))
	}
	group := result.Groups[0]
	if group.SeriesID != "mixed" || group.MissingCount != 1 || len(group.Episodes) != 1 {
		t.Fatalf("unexpected group: %#v", group)
	}
	if group.Episodes[0].SeasonNumber != 1 {
		t.Fatalf("season number = %d; want 1", group.Episodes[0].SeasonNumber)
	}
	if result.Setting.LastSeriesCount != 1 || result.Setting.LastMissingCount != 1 {
		t.Fatalf(
			"setting counts = series %d, missing %d; want series 1, missing 1",
			result.Setting.LastSeriesCount,
			result.Setting.LastMissingCount,
		)
	}
}

func TestRescanSeriesRemovesResolvedSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Shows/Missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
	}))
	defer server.Close()

	svc := newEmbyMissingTestService(t)
	cfg := &config.Config{Emby: config.EmbyConfig{
		URL:         server.URL,
		AdminUserID: "admin-1",
	}}
	svc.emby = embyhelper.New(cfg)
	existing := model.EmbyMissingEpisode{
		SeriesID:      "series-1",
		SeriesName:    "Series",
		LibraryID:     "library-1",
		LibraryName:   "TV",
		SeasonNumber:  1,
		EpisodeNumber: 2,
	}
	if err := svc.db.Create(&existing).Error; err != nil {
		t.Fatalf("seed missing episode: %v", err)
	}

	result, err := svc.RescanSeries(t.Context(), "series-1", ScanOptions{})
	if err != nil {
		t.Fatalf("rescan series: %v", err)
	}
	if !result.Resolved || result.MissingCount != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}

	var episodeCount int64
	if err := svc.db.Model(&model.EmbyMissingEpisode{}).Where("series_id = ?", "series-1").Count(&episodeCount).Error; err != nil {
		t.Fatalf("count missing episodes: %v", err)
	}
	if episodeCount != 0 {
		t.Fatalf("missing episode count = %d; want 0", episodeCount)
	}

	var scan model.EmbyMissingSeriesScan
	if err := svc.db.Where("series_id = ?", "series-1").First(&scan).Error; err != nil {
		t.Fatalf("find series scan record: %v", err)
	}
}

func TestRescanSeriesKeepsSnapshotWhenEmbyFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))
	defer server.Close()

	svc := newEmbyMissingTestService(t)
	cfg := &config.Config{Emby: config.EmbyConfig{
		URL:         server.URL,
		AdminUserID: "admin-1",
	}}
	svc.emby = embyhelper.New(cfg)
	existing := model.EmbyMissingEpisode{
		SeriesID:      "series-1",
		SeriesName:    "Series",
		SeasonNumber:  1,
		EpisodeNumber: 2,
	}
	if err := svc.db.Create(&existing).Error; err != nil {
		t.Fatalf("seed missing episode: %v", err)
	}

	if _, err := svc.RescanSeries(t.Context(), "series-1", ScanOptions{}); err == nil {
		t.Fatal("rescan series should fail")
	}

	var episodeCount int64
	if err := svc.db.Model(&model.EmbyMissingEpisode{}).Where("series_id = ?", "series-1").Count(&episodeCount).Error; err != nil {
		t.Fatalf("count missing episodes: %v", err)
	}
	if episodeCount != 1 {
		t.Fatalf("missing episode count = %d; want original snapshot retained", episodeCount)
	}
}

func TestResolveByEmbyPrefixDerivesCloudDirWithoutLocalMount(t *testing.T) {
	svc := &EmbyMissingService{}
	result := &ResolveSeriesResult{}
	paths := []model.CloudPath{
		{
			ID:             7,
			EmbyPathPrefix: "/MediaSTRM_115LongTerm",
			LocalPath:      "/path/that/does/not/exist",
		},
	}

	matched := svc.resolveByEmbyPrefix(
		"/MediaSTRM_115LongTerm/影视中心/日韩剧集/化时为机 (2020) {tmdb-112119}",
		paths,
		result,
	)
	if !matched || !result.Matched {
		t.Fatal("Emby path prefix should match without a local mount")
	}
	if result.CloudPathID != 7 {
		t.Fatalf("cloud path id = %d; want 7", result.CloudPathID)
	}
	const want = "/影视中心/日韩剧集/化时为机 (2020) {tmdb-112119}"
	if result.CloudDir != want {
		t.Fatalf("cloud dir = %q; want %q", result.CloudDir, want)
	}
	if result.LocalDir != "" {
		t.Fatalf("local dir = %q; want empty for an unavailable local mount", result.LocalDir)
	}
}
