package service

import (
	"path/filepath"
	"testing"
	"time"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newEmbyWatchCalendarTestService(t *testing.T) *EmbyWatchService {
	t.Helper()

	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "emby-watch-calendar.db")),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.EmbyWatchRecord{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return &EmbyWatchService{db: db}
}

func TestCalendarIncludesGroupedPosterItems(t *testing.T) {
	svc := newEmbyWatchCalendarTestService(t)
	watchedAt := time.Date(2026, time.July, 12, 22, 30, 0, 0, time.Local)
	season := 1
	episodeOne := 1
	episodeTwo := 2

	records := []model.EmbyWatchRecord{
		{
			EmbyUserID:  "user-1",
			ItemID:      "movie-1",
			ItemType:    "Movie",
			Title:       "电影一",
			WatchedAt:   watchedAt,
			WatchedDate: "2026-07-12",
		},
		{
			EmbyUserID:    "user-1",
			ItemID:        "episode-1",
			ItemType:      "Episode",
			Title:         "第一集",
			SeriesID:      "series-1",
			SeriesName:    "剧集一",
			SeasonNumber:  &season,
			EpisodeNumber: &episodeOne,
			WatchedAt:     watchedAt.Add(time.Hour),
			WatchedDate:   "2026-07-12",
		},
		{
			EmbyUserID:    "user-1",
			ItemID:        "episode-2",
			ItemType:      "Episode",
			Title:         "第二集",
			SeriesID:      "series-1",
			SeriesName:    "剧集一",
			SeasonNumber:  &season,
			EpisodeNumber: &episodeTwo,
			WatchedAt:     watchedAt.Add(2 * time.Hour),
			WatchedDate:   "2026-07-12",
		},
		{
			EmbyUserID:  "user-2",
			ItemID:      "other-user-movie",
			ItemType:    "Movie",
			Title:       "其他用户的电影",
			WatchedAt:   watchedAt,
			WatchedDate: "2026-07-12",
		},
		{
			EmbyUserID:  "user-1",
			ItemID:      "other-month-movie",
			ItemType:    "Movie",
			Title:       "其他月份的电影",
			WatchedAt:   watchedAt.AddDate(0, 1, 0),
			WatchedDate: "2026-08-12",
		},
	}
	if err := svc.db.Create(&records).Error; err != nil {
		t.Fatalf("seed watch records: %v", err)
	}

	summaryOnly, err := svc.Calendar("user-1", 2026, 7, false)
	if err != nil {
		t.Fatalf("Calendar summary error: %v", err)
	}
	if len(summaryOnly) != 1 || len(summaryOnly[0].Items) != 0 {
		t.Fatalf("summary calendar should omit poster items: %+v", summaryOnly)
	}

	days, err := svc.Calendar("user-1", 2026, 7, true)
	if err != nil {
		t.Fatalf("Calendar error: %v", err)
	}
	if len(days) != 1 {
		t.Fatalf("len(days) = %d; want 1", len(days))
	}

	day := days[0]
	if day.Date != "2026-07-12" || day.Total != 3 || day.MovieCount != 1 || day.EpisodeCount != 2 {
		t.Fatalf("unexpected day aggregate: %+v", day)
	}
	if len(day.Items) != 2 {
		t.Fatalf("len(day.Items) = %d; want 2: %+v", len(day.Items), day.Items)
	}
	if day.Items[0].PosterID != "series-1" || day.Items[0].Title != "剧集一" || day.Items[0].Count != 2 {
		t.Fatalf("unexpected grouped episode item: %+v", day.Items[0])
	}
	if day.Items[1].PosterID != "movie-1" || day.Items[1].Title != "电影一" || day.Items[1].Count != 1 {
		t.Fatalf("unexpected movie item: %+v", day.Items[1])
	}
}

func TestCalendarLimitsPosterItems(t *testing.T) {
	svc := newEmbyWatchCalendarTestService(t)
	watchedAt := time.Date(2026, time.July, 18, 20, 0, 0, 0, time.Local)

	records := make([]model.EmbyWatchRecord, 0, calendarPosterLimit+2)
	for i := 0; i < calendarPosterLimit+2; i++ {
		records = append(records, model.EmbyWatchRecord{
			EmbyUserID:  "user-1",
			ItemID:      "movie-" + string(rune('a'+i)),
			ItemType:    "Movie",
			Title:       "电影",
			WatchedAt:   watchedAt.Add(time.Duration(i) * time.Minute),
			WatchedDate: "2026-07-18",
		})
	}
	if err := svc.db.Create(&records).Error; err != nil {
		t.Fatalf("seed watch records: %v", err)
	}

	days, err := svc.Calendar("user-1", 2026, 7, true)
	if err != nil {
		t.Fatalf("Calendar error: %v", err)
	}
	if len(days) != 1 {
		t.Fatalf("len(days) = %d; want 1", len(days))
	}
	if days[0].Total != calendarPosterLimit+2 {
		t.Fatalf("days[0].Total = %d; want %d", days[0].Total, calendarPosterLimit+2)
	}
	if len(days[0].Items) != calendarPosterLimit {
		t.Fatalf("len(days[0].Items) = %d; want %d", len(days[0].Items), calendarPosterLimit)
	}
}
