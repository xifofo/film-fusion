package service

import (
	"path/filepath"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newEmbyWatchOverviewTestService(t *testing.T) *EmbyWatchService {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "emby-watch-overview.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.EmbyWatchUser{}, &model.EmbyWatchRecord{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return &EmbyWatchService{db: db}
}

func TestOverviewAggregatesTrackedUsers(t *testing.T) {
	svc := newEmbyWatchOverviewTestService(t)
	users := []model.EmbyWatchUser{
		{EmbyUserID: "u1", EmbyUserName: "甲", Enabled: true},
		{EmbyUserID: "u2", EmbyUserName: "乙", Enabled: true},
		{EmbyUserID: "u3", EmbyUserName: "丙", Enabled: true},
	}
	if err := svc.db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	// 关闭 u3 的统计，其记录不应计入总览。
	if err := svc.db.Model(&model.EmbyWatchUser{}).Where("emby_user_id = ?", "u3").Update("enabled", false).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}

	at := func(day int, hour int) time.Time {
		return time.Date(2026, time.November, day, hour, 0, 0, 0, time.Local)
	}
	records := []model.EmbyWatchRecord{
		{EmbyUserID: "u1", EmbyUserName: "甲", ItemID: "m1", ItemType: "Movie", Title: "电影一", RuntimeMinutes: 120, WatchedAt: at(10, 21), WatchedDate: "2026-11-10"},
		{EmbyUserID: "u2", EmbyUserName: "乙", ItemID: "m1", ItemType: "Movie", Title: "电影一", RuntimeMinutes: 120, WatchedAt: at(10, 20), WatchedDate: "2026-11-10"},
		{EmbyUserID: "u2", EmbyUserName: "乙", ItemID: "e1", ItemType: "Episode", Title: "第一集", SeriesID: "s1", SeriesName: "剧一", RuntimeMinutes: 45, WatchedAt: at(11, 22), WatchedDate: "2026-11-11"},
		{EmbyUserID: "u2", EmbyUserName: "乙", ItemID: "e2", ItemType: "Episode", Title: "第二集", SeriesID: "s1", SeriesName: "剧一", RuntimeMinutes: 45, WatchedAt: at(11, 23), WatchedDate: "2026-11-11"},
		{EmbyUserID: "u1", EmbyUserName: "甲", ItemID: "m2", ItemType: "Movie", Title: "上期电影", RuntimeMinutes: 90, WatchedAt: at(8, 20), WatchedDate: "2026-11-08"},
		{EmbyUserID: "u3", EmbyUserName: "丙", ItemID: "m1", ItemType: "Movie", Title: "电影一", RuntimeMinutes: 120, WatchedAt: at(10, 19), WatchedDate: "2026-11-10"},
	}
	if err := svc.db.Create(&records).Error; err != nil {
		t.Fatalf("seed records: %v", err)
	}

	out, err := svc.Overview(WatchOverviewParams{StartDate: "2026-11-10", EndDate: "2026-11-11"})
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	if out.Total != 4 || out.MovieCount != 2 || out.EpisodeCount != 2 {
		t.Fatalf("unexpected counts: total=%d movies=%d episodes=%d", out.Total, out.MovieCount, out.EpisodeCount)
	}
	if out.TotalMinutes != 330 || out.ActiveUsers != 2 || out.TrackedUsers != 2 || out.ActiveDays != 2 {
		t.Fatalf("unexpected totals: %+v", out)
	}
	if out.Previous == nil || out.Previous.Total != 1 || out.Previous.TotalMinutes != 90 {
		t.Fatalf("previous period 11-08..11-09 should hold one 90-minute record, got %+v", out.Previous)
	}
	if len(out.Daily) != 2 || out.Daily[0].MovieCount != 2 || out.Daily[1].EpisodeCount != 2 {
		t.Fatalf("unexpected daily: %+v", out.Daily)
	}
	if len(out.Users) != 2 || out.Users[0].EmbyUserID != "u2" || out.Users[0].TotalMinutes != 210 {
		t.Fatalf("unexpected user ranking: %+v", out.Users)
	}
	if out.Users[0].LastWatchedAt == "" || out.Users[0].LastWatchedAt[4] != '-' || out.Users[0].LastWatchedAt[10] != 'T' {
		t.Fatalf("last_watched_at should be RFC3339, got %q", out.Users[0].LastWatchedAt)
	}
	if len(out.TopItems) != 2 || out.TopItems[0].ID != "m1" || out.TopItems[0].Viewers != 2 {
		t.Fatalf("unexpected top items: %+v", out.TopItems)
	}
	if out.TopItems[1].ItemType != "Series" || out.TopItems[1].Plays != 2 {
		t.Fatalf("series should aggregate episodes: %+v", out.TopItems[1])
	}
	if len(out.Recent) != 4 || out.Recent[0].ItemID != "e2" {
		t.Fatalf("unexpected recent: %+v", out.Recent)
	}

	single, err := svc.Overview(WatchOverviewParams{EmbyUserID: "u1"})
	if err != nil {
		t.Fatalf("single user overview: %v", err)
	}
	if single.Total != 2 || single.Previous != nil {
		t.Fatalf("unexpected single user overview: total=%d previous=%+v", single.Total, single.Previous)
	}

	if _, err := svc.Overview(WatchOverviewParams{EmbyUserID: "u3"}); err == nil {
		t.Fatal("disabled user should be rejected")
	}
	if _, err := svc.Overview(WatchOverviewParams{StartDate: "2026-11-12", EndDate: "2026-11-10"}); err == nil {
		t.Fatal("reversed range should be rejected")
	}
}
