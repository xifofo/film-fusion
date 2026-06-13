package service

import (
	"path/filepath"
	"testing"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/store/embyproxylog"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestFindReadyPlaybackCacheByPathUsesSourceOrTargetPath(t *testing.T) {
	db := setupBalanceServiceTestDB(t)
	source, target, match := createBalanceCacheBase(t, db, true)
	expiresAt := time.Now().Add(time.Hour)
	readyAt := time.Now().Add(-time.Minute)
	assignment := model.Match302BalanceAssignment{
		Match302ID:        match.ID,
		SourceFilePath:    "/media/source/Movie Name/test.mkv",
		SourceStorageID:   source.ID,
		PlaybackStorageID: target.ID,
		SourcePickcode:    "source-pick",
		TargetPickcode:    "target-pick",
		TargetPath:        "/FilmFusion Cache/Movie Name/test.mkv",
		Status:            model.BalanceAssignmentStatusReady,
		CleanupStatus:     model.BalanceCleanupStatusNone,
		ExpiresAt:         &expiresAt,
		LastReadyAt:       &readyAt,
	}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	svc := NewBalanceAssignmentService(nil)
	for _, input := range []string{
		"/media/source/Movie Name/test.mkv",
		"/media/source/Movie%20Name/test.mkv",
		"/FilmFusion Cache/Movie Name/test.mkv",
		"/FilmFusion%20Cache/Movie%20Name/test.mkv",
	} {
		got, err := svc.FindReadyPlaybackCacheByPath(input)
		if err != nil {
			t.Fatalf("FindReadyPlaybackCacheByPath(%q) returned error: %v", input, err)
		}
		if got == nil {
			t.Fatalf("FindReadyPlaybackCacheByPath(%q) returned nil", input)
		}
		if got.ID != assignment.ID {
			t.Fatalf("FindReadyPlaybackCacheByPath(%q) assignment id = %d, want %d", input, got.ID, assignment.ID)
		}
		if got.TargetPickcode != "target-pick" {
			t.Fatalf("FindReadyPlaybackCacheByPath(%q) target pickcode = %q", input, got.TargetPickcode)
		}
		if got.PlaybackStorage == nil || got.PlaybackStorage.ID != target.ID {
			t.Fatalf("FindReadyPlaybackCacheByPath(%q) did not preload playback storage", input)
		}
	}
}

func TestFindReadyPlaybackCacheByPathSkipsUnusableAssignments(t *testing.T) {
	db := setupBalanceServiceTestDB(t)
	source, target, enabledMatch := createBalanceCacheBase(t, db, true)
	_, _, disabledMatch := createBalanceCacheBase(t, db, false)
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)

	assignments := []model.Match302BalanceAssignment{
		{
			Match302ID:        enabledMatch.ID,
			SourceFilePath:    "/expired/test.mkv",
			SourceStorageID:   source.ID,
			PlaybackStorageID: target.ID,
			TargetPickcode:    "expired-pick",
			TargetPath:        "/cache/expired/test.mkv",
			Status:            model.BalanceAssignmentStatusReady,
			CleanupStatus:     model.BalanceCleanupStatusNone,
			ExpiresAt:         &past,
		},
		{
			Match302ID:        enabledMatch.ID,
			SourceFilePath:    "/cleaned/test.mkv",
			SourceStorageID:   source.ID,
			PlaybackStorageID: target.ID,
			TargetPickcode:    "cleaned-pick",
			TargetPath:        "/cache/cleaned/test.mkv",
			Status:            model.BalanceAssignmentStatusReady,
			CleanupStatus:     model.BalanceCleanupStatusCleaned,
			ExpiresAt:         &future,
		},
		{
			Match302ID:        disabledMatch.ID,
			SourceFilePath:    "/disabled/test.mkv",
			SourceStorageID:   source.ID,
			PlaybackStorageID: target.ID,
			TargetPickcode:    "disabled-pick",
			TargetPath:        "/cache/disabled/test.mkv",
			Status:            model.BalanceAssignmentStatusReady,
			CleanupStatus:     model.BalanceCleanupStatusNone,
			ExpiresAt:         &future,
		},
	}
	if err := db.Create(&assignments).Error; err != nil {
		t.Fatalf("create assignments: %v", err)
	}

	svc := NewBalanceAssignmentService(nil)
	for _, input := range []string{
		"/expired/test.mkv",
		"/cache/cleaned/test.mkv",
		"/disabled/test.mkv",
	} {
		got, err := svc.FindReadyPlaybackCacheByPath(input)
		if err != nil {
			t.Fatalf("FindReadyPlaybackCacheByPath(%q) returned error: %v", input, err)
		}
		if got != nil {
			t.Fatalf("FindReadyPlaybackCacheByPath(%q) returned assignment %d, want nil", input, got.ID)
		}
	}
}

func TestCandidatesDoNotSkipMembersWithCooldownUntil(t *testing.T) {
	embyplayback.Default().Clear()
	t.Cleanup(func() {
		embyplayback.Default().Clear()
	})

	future := time.Now().Add(time.Hour)
	source := model.CloudStorage{
		ID:          1,
		StorageType: model.StorageType115Open,
		StorageName: "source",
		Cookie:      "source-cookie",
		Status:      model.StatusActive,
	}
	target := model.CloudStorage{
		ID:          2,
		StorageType: model.StorageType115Open,
		StorageName: "target",
		Cookie:      "target-cookie",
		Status:      model.StatusActive,
	}
	match := &model.Match302{
		ID:               1,
		CloudStorageID:   source.ID,
		CloudStorage:     &source,
		BalanceEnabled:   true,
		BalanceLimitMode: model.Match302BalanceLimitModeLoose,
		PoolMembers: []model.Match302BalanceMember{
			{
				Match302ID:     1,
				CloudStorageID: target.ID,
				Enabled:        true,
				Weight:         1,
				CooldownUntil:  &future,
				CloudStorage:   &target,
			},
		},
	}

	candidates, reason := NewBalanceAssignmentService(nil).candidates(match, "")
	if reason != "" {
		t.Fatalf("candidates returned reason %q, want empty", reason)
	}
	foundTarget := false
	for _, candidate := range candidates {
		if candidate.Storage.ID == target.ID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatalf("member with cooldown_until was skipped, candidates=%v", candidates)
	}
}

func TestCandidatesUseCurrentPlaybackStoreForActiveCounts(t *testing.T) {
	embyplayback.Default().Clear()
	t.Cleanup(func() {
		embyplayback.Default().Clear()
	})

	source := model.CloudStorage{
		ID:          1,
		StorageType: model.StorageType115Open,
		StorageName: "source",
		Cookie:      "source-cookie",
		Status:      model.StatusActive,
	}
	target := model.CloudStorage{
		ID:                2,
		StorageType:       model.StorageType115Open,
		StorageName:       "target",
		Cookie:            "target-cookie",
		Status:            model.StatusActive,
		Match302MaxActive: 1,
	}
	match := &model.Match302{
		ID:               1,
		CloudStorageID:   source.ID,
		CloudStorage:     &source,
		BalanceEnabled:   true,
		BalanceLimitMode: model.Match302BalanceLimitModeLoose,
		PoolMembers: []model.Match302BalanceMember{
			{
				Match302ID:     1,
				CloudStorageID: target.ID,
				Enabled:        true,
				Weight:         1,
				CloudStorage:   &target,
			},
		},
	}
	now := time.Now()
	embyplayback.Default().MarkActive(embyplayback.Event{
		ItemID:        "item-1",
		MediaSourceID: "media-1",
		RemoteIP:      "127.0.0.1",
		UserAgent:     "test-player",
		Timestamp:     now,
	})
	embyplayback.Default().AttachRedirect(embyproxylog.Entry{
		Timestamp:       now,
		ItemID:          "item-1",
		MediaSourceID:   "media-1",
		RemoteIP:        "127.0.0.1",
		UserAgent:       "test-player",
		ActualStorageID: target.ID,
	})

	candidates, reason := NewBalanceAssignmentService(nil).candidates(match, "")
	if reason != "" {
		t.Fatalf("candidates returned reason %q, want empty because source is still available", reason)
	}
	for _, candidate := range candidates {
		if candidate.Storage.ID == target.ID {
			t.Fatalf("target storage with active current playback was not skipped")
		}
	}
}

func setupBalanceServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "balance-cache.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.CloudStorage{},
		&model.Match302{},
		&model.Match302BalanceMember{},
		&model.Match302BalanceAssignment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = oldDB
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createBalanceCacheBase(t *testing.T, db *gorm.DB, balanceEnabled bool) (model.CloudStorage, model.CloudStorage, model.Match302) {
	t.Helper()
	suffix := time.Now().Format("150405.000000000")
	source := model.CloudStorage{
		UserID:      1,
		StorageType: model.StorageType115Open,
		StorageName: "source-" + suffix,
		ProviderUID: "source-" + suffix,
		AccessToken: "source-token",
		Cookie:      "source-cookie",
		Status:      model.StatusActive,
	}
	target := model.CloudStorage{
		UserID:      1,
		StorageType: model.StorageType115Open,
		StorageName: "target-" + suffix,
		ProviderUID: "target-" + suffix,
		Cookie:      "target-cookie",
		Status:      model.StatusActive,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source storage: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target storage: %v", err)
	}
	match := model.Match302{
		SourcePath:       "/media/source",
		TargetPath:       "/library",
		CloudStorageID:   source.ID,
		BalanceEnabled:   balanceEnabled,
		BalanceStrategy:  model.Match302BalanceStrategyStickyLeastActive,
		BalanceLimitMode: model.Match302BalanceLimitModeLoose,
		CleanupMode:      model.Match302CleanupModeRecycle,
		RetentionHours:   72,
	}
	if err := db.Create(&match).Error; err != nil {
		t.Fatalf("create match302: %v", err)
	}
	return source, target, match
}
