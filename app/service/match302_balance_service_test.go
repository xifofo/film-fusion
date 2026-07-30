package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/store/embyproxylog"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
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

func TestEnsureDirPathFallsBackToOpenAPIForDirectoryQuery(t *testing.T) {
	var openListCalls int
	directorySvc := &fakeBalanceDirectoryService{
		resolveDirPathWithClient: func(*driver.Pan115Client, string) (string, bool, error) {
			return "", false, errors.New("cookie getid blocked")
		},
		getDirectoriesWithClient: func(*driver.Pan115Client, string, int, int) (Web115ListResult, error) {
			return Web115ListResult{}, errors.New("<!doctype html> aliyun waf block")
		},
		getDirectoriesWithOpenAPI: func(_ context.Context, accessToken, cid string, _, _ int) (Web115ListResult, error) {
			openListCalls++
			if accessToken != "target-token" {
				t.Fatalf("Open API access token = %q, want target-token", accessToken)
			}
			if cid != "0" {
				t.Fatalf("Open API parent id = %q, want 0", cid)
			}
			return Web115ListResult{Items: []Web115File{
				{FileID: "open-cache-id", Name: "FilmFusion Cache"},
			}}, nil
		},
	}
	svc := NewBalanceAssignmentService(nil)
	svc.directory115Svc = directorySvc

	got, err := svc.ensureDirPath(context.Background(), nil, "target-token", "/FilmFusion Cache")
	if err != nil {
		t.Fatalf("ensureDirPath returned error: %v", err)
	}
	if got != "open-cache-id" {
		t.Fatalf("ensureDirPath id = %q, want open-cache-id", got)
	}
	if openListCalls != 1 {
		t.Fatalf("Open API list calls = %d, want 1", openListCalls)
	}
	if directorySvc.mkdirClientCalls != 0 || directorySvc.mkdirOpenCalls != 0 {
		t.Fatalf(
			"unexpected mkdir calls: cookie=%d open=%d",
			directorySvc.mkdirClientCalls,
			directorySvc.mkdirOpenCalls,
		)
	}
}

func TestEnsureDirPathFallsBackToOpenAPIForMkdir(t *testing.T) {
	var openMkdirCalls int
	directorySvc := &fakeBalanceDirectoryService{
		resolveDirPathWithClient: func(*driver.Pan115Client, string) (string, bool, error) {
			return "", false, nil
		},
		getDirectoriesWithClient: func(*driver.Pan115Client, string, int, int) (Web115ListResult, error) {
			return Web115ListResult{}, nil
		},
		mkdirWithClient: func(*driver.Pan115Client, string, string) (string, error) {
			return "", errors.New("<!doctype html> aliyun waf block")
		},
		mkdirWithOpenAPI: func(_ context.Context, accessToken, parentID, name string) (string, error) {
			openMkdirCalls++
			if accessToken != "target-token" {
				t.Fatalf("Open API access token = %q, want target-token", accessToken)
			}
			if parentID != "0" || name != "cache" {
				t.Fatalf("Open API mkdir parent/name = %q/%q, want 0/cache", parentID, name)
			}
			return "open-created-id", nil
		},
	}
	svc := NewBalanceAssignmentService(nil)
	svc.directory115Svc = directorySvc

	got, err := svc.ensureDirPath(context.Background(), nil, "target-token", "/cache")
	if err != nil {
		t.Fatalf("ensureDirPath returned error: %v", err)
	}
	if got != "open-created-id" {
		t.Fatalf("ensureDirPath id = %q, want open-created-id", got)
	}
	if openMkdirCalls != 1 {
		t.Fatalf("Open API mkdir calls = %d, want 1", openMkdirCalls)
	}
}

func TestEnsureDirPathResolvesConcurrentExistingDirectory(t *testing.T) {
	listCalls := 0
	directorySvc := &fakeBalanceDirectoryService{
		resolveDirPathWithClient: func(*driver.Pan115Client, string) (string, bool, error) {
			return "", false, nil
		},
		getDirectoriesWithClient: func(*driver.Pan115Client, string, int, int) (Web115ListResult, error) {
			listCalls++
			if listCalls == 1 {
				return Web115ListResult{}, nil
			}
			return Web115ListResult{Items: []Web115File{
				{FileID: "existing-id", Name: "cache"},
			}}, nil
		},
		mkdirWithClient: func(*driver.Pan115Client, string, string) (string, error) {
			return "", driver.ErrExist
		},
		mkdirWithOpenAPI: func(context.Context, string, string, string) (string, error) {
			t.Fatal("Open API mkdir should not be called for an existing directory")
			return "", nil
		},
	}
	svc := NewBalanceAssignmentService(nil)
	svc.directory115Svc = directorySvc

	got, err := svc.ensureDirPath(context.Background(), nil, "target-token", "/cache")
	if err != nil {
		t.Fatalf("ensureDirPath returned error: %v", err)
	}
	if got != "existing-id" {
		t.Fatalf("ensureDirPath id = %q, want existing-id", got)
	}
	if listCalls != 2 {
		t.Fatalf("Cookie list calls = %d, want 2", listCalls)
	}
}

func TestEnsureDirPathReportsCookieAndOpenAPIErrorsCompactly(t *testing.T) {
	directorySvc := &fakeBalanceDirectoryService{
		resolveDirPathWithClient: func(*driver.Pan115Client, string) (string, bool, error) {
			return "", false, nil
		},
		getDirectoriesWithClient: func(*driver.Pan115Client, string, int, int) (Web115ListResult, error) {
			return Web115ListResult{}, errors.New(strings.Repeat("cookie-html-", 100))
		},
		getDirectoriesWithOpenAPI: func(context.Context, string, string, int, int) (Web115ListResult, error) {
			return Web115ListResult{}, errors.New("open api unavailable")
		},
	}
	svc := NewBalanceAssignmentService(nil)
	svc.directory115Svc = directorySvc

	_, err := svc.ensureDirPath(context.Background(), nil, "target-token", "/cache")
	if err == nil {
		t.Fatal("ensureDirPath expected error")
	}
	message := err.Error()
	if !strings.Contains(message, "Cookie 查询目录失败") || !strings.Contains(message, "Open API 查询目录失败") {
		t.Fatalf("combined fallback error = %q", message)
	}
	if len([]rune(message)) > 600 {
		t.Fatalf("combined fallback error was not compacted, length=%d", len([]rune(message)))
	}
}

type fakeBalanceDirectoryService struct {
	resolveDirPathWithClient  func(*driver.Pan115Client, string) (string, bool, error)
	getDirectoriesWithClient  func(*driver.Pan115Client, string, int, int) (Web115ListResult, error)
	getDirectoriesWithOpenAPI func(context.Context, string, string, int, int) (Web115ListResult, error)
	mkdirWithClient           func(*driver.Pan115Client, string, string) (string, error)
	mkdirWithOpenAPI          func(context.Context, string, string, string) (string, error)
	mkdirClientCalls          int
	mkdirOpenCalls            int
}

func (f *fakeBalanceDirectoryService) ResolveDirPathWithClient(client *driver.Pan115Client, dir string) (string, bool, error) {
	if f.resolveDirPathWithClient == nil {
		return "", false, nil
	}
	return f.resolveDirPathWithClient(client, dir)
}

func (f *fakeBalanceDirectoryService) GetDirectoriesWithClient(client *driver.Pan115Client, cid string, offset, limit int) (Web115ListResult, error) {
	if f.getDirectoriesWithClient == nil {
		return Web115ListResult{}, nil
	}
	return f.getDirectoriesWithClient(client, cid, offset, limit)
}

func (f *fakeBalanceDirectoryService) GetDirectoriesWithOpenAPI(ctx context.Context, accessToken, cid string, offset, limit int) (Web115ListResult, error) {
	if f.getDirectoriesWithOpenAPI == nil {
		return Web115ListResult{}, errors.New("unexpected Open API list call")
	}
	return f.getDirectoriesWithOpenAPI(ctx, accessToken, cid, offset, limit)
}

func (f *fakeBalanceDirectoryService) MkdirWithClient(client *driver.Pan115Client, parentID, name string) (string, error) {
	f.mkdirClientCalls++
	if f.mkdirWithClient == nil {
		return "", errors.New("unexpected Cookie mkdir call")
	}
	return f.mkdirWithClient(client, parentID, name)
}

func (f *fakeBalanceDirectoryService) MkdirWithOpenAPI(ctx context.Context, accessToken, parentID, name string) (string, error) {
	f.mkdirOpenCalls++
	if f.mkdirWithOpenAPI == nil {
		return "", errors.New("unexpected Open API mkdir call")
	}
	return f.mkdirWithOpenAPI(ctx, accessToken, parentID, name)
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
