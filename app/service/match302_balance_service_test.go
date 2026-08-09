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

func TestResolveSourceFileInfoUsesCookieWithoutAccessToken(t *testing.T) {
	storage := &model.CloudStorage{
		ID:          2,
		StorageType: model.StorageType115Open,
		StorageName: "cookie-source",
		Cookie:      "UID=test; CID=test; SEID=test",
		Status:      model.StatusActive,
	}
	match := &model.Match302{
		CloudStorageID: storage.ID,
		CloudStorage:   storage,
		TargetPath:     "/library",
	}
	resolver := &fakeBalanceSourceFileResolver{
		file: Web115File{
			FileID:   "file-id",
			Name:     "Test.mkv",
			PickCode: "pick-code",
			SHA1:     "abcdef",
			IsFile:   true,
			Size:     12345,
		},
		found: true,
	}
	svc := NewBalanceAssignmentService(nil)
	svc.sourceFile115Svc = resolver

	got, err := svc.ResolveSourceFileInfo(
		context.Background(),
		match,
		"/media/Movie/Test.mkv",
		"/library/Movie/Test.mkv",
	)
	if err != nil {
		t.Fatalf("ResolveSourceFileInfo returned error: %v", err)
	}
	if resolver.cookie != storage.Cookie || resolver.path != "/library/Movie/Test.mkv" {
		t.Fatalf("resolver inputs cookie=%q path=%q", resolver.cookie, resolver.path)
	}
	if got.PickCode != "pick-code" || got.FileID != "file-id" || got.SHA1 != "ABCDEF" || got.Size != 12345 {
		t.Fatalf("source file = %+v", got)
	}
	if got.RelativePath != "Movie/Test.mkv" {
		t.Fatalf("relative path = %q", got.RelativePath)
	}
}

func TestResolveSourceFileInfoCookieOnlyIgnoresConfiguredAccessToken(t *testing.T) {
	storage := &model.CloudStorage{
		ID:                 2,
		StorageType:        model.StorageType115Open,
		AccessToken:        "must-not-be-used",
		Cookie:             "UID=test; CID=test; SEID=test",
		Match302AccessMode: model.Match302AccessModeCookieOnly,
	}
	resolver := &fakeBalanceSourceFileResolver{
		file: Web115File{
			FileID:   "cookie-file",
			Name:     "Test.mkv",
			PickCode: "cookie-pick",
			IsFile:   true,
		},
		found: true,
	}
	svc := NewBalanceAssignmentService(nil)
	svc.sourceFile115Svc = resolver

	got, err := svc.ResolveSourceFileInfo(context.Background(), &model.Match302{
		CloudStorageID: storage.ID,
		CloudStorage:   storage,
	}, "/media/Test.mkv", "/Test.mkv")
	if err != nil {
		t.Fatalf("ResolveSourceFileInfo returned error: %v", err)
	}
	if got.PickCode != "cookie-pick" || resolver.openCalls != 0 || resolver.cookieCalls != 1 {
		t.Fatalf("pickcode=%q openCalls=%d cookieCalls=%d", got.PickCode, resolver.openCalls, resolver.cookieCalls)
	}
}

func TestResolveSourceFileInfoOpenAPIOnlyIgnoresConfiguredCookie(t *testing.T) {
	storage := &model.CloudStorage{
		ID:                 2,
		StorageType:        model.StorageType115Open,
		AccessToken:        "open-token",
		Cookie:             "must-not-be-used",
		Match302AccessMode: model.Match302AccessModeOpenAPIOnly,
	}
	resolver := &fakeBalanceSourceFileResolver{
		openFile: Web115File{
			FileID:   "open-file",
			Name:     "Test.mkv",
			PickCode: "open-pick",
			IsFile:   true,
		},
		openFound: true,
	}
	svc := NewBalanceAssignmentService(nil)
	svc.sourceFile115Svc = resolver

	got, err := svc.ResolveSourceFileInfo(context.Background(), &model.Match302{
		CloudStorageID: storage.ID,
		CloudStorage:   storage,
	}, "/media/Test.mkv", "/Test.mkv")
	if err != nil {
		t.Fatalf("ResolveSourceFileInfo returned error: %v", err)
	}
	if got.PickCode != "open-pick" || resolver.openCalls != 1 || resolver.cookieCalls != 0 {
		t.Fatalf("pickcode=%q openCalls=%d cookieCalls=%d", got.PickCode, resolver.openCalls, resolver.cookieCalls)
	}
}

func TestResolveSourceFileInfoAutoFallsBackToCookie(t *testing.T) {
	storage := &model.CloudStorage{
		ID:                 2,
		StorageType:        model.StorageType115Open,
		AccessToken:        "expired-token",
		Cookie:             "UID=test; CID=test; SEID=test",
		Match302AccessMode: model.Match302AccessModeAuto,
	}
	resolver := &fakeBalanceSourceFileResolver{
		openErr: errors.New("OpenAPI unavailable"),
		file: Web115File{
			FileID:   "cookie-file",
			Name:     "Test.mkv",
			PickCode: "cookie-pick",
			IsFile:   true,
		},
		found: true,
	}
	svc := NewBalanceAssignmentService(nil)
	svc.sourceFile115Svc = resolver

	got, err := svc.ResolveSourceFileInfo(context.Background(), &model.Match302{
		CloudStorageID: storage.ID,
		CloudStorage:   storage,
	}, "/media/Test.mkv", "/Test.mkv")
	if err != nil {
		t.Fatalf("ResolveSourceFileInfo returned error: %v", err)
	}
	if got.PickCode != "cookie-pick" || resolver.openCalls != 1 || resolver.cookieCalls != 1 {
		t.Fatalf("pickcode=%q openCalls=%d cookieCalls=%d", got.PickCode, resolver.openCalls, resolver.cookieCalls)
	}
}

func TestResolveSourceFileInfoRequiresTokenOrCookie(t *testing.T) {
	match := &model.Match302{
		CloudStorageID: 1,
		CloudStorage: &model.CloudStorage{
			ID:          1,
			StorageType: model.StorageType115Open,
		},
	}
	_, err := NewBalanceAssignmentService(nil).ResolveSourceFileInfo(
		context.Background(),
		match,
		"/media/Test.mkv",
		"/Test.mkv",
	)
	if err == nil || !strings.Contains(err.Error(), "AccessToken 和 Cookie 均缺失") {
		t.Fatalf("error = %v", err)
	}
}

func TestStorageUsableHonorsMatch302AccessMode(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	storage := model.CloudStorage{
		StorageType:      model.StorageType115Open,
		Status:           model.StatusExpired,
		AccessToken:      "expired-open-token",
		Cookie:           "valid-cookie",
		RefreshExpiresAt: &expired,
	}

	storage.Match302AccessMode = model.Match302AccessModeOpenAPIOnly
	if storageUsable(storage) {
		t.Fatal("OpenAPI-only storage with expired refresh credential should be unavailable")
	}
	storage.Match302AccessMode = model.Match302AccessModeCookieOnly
	if !storageUsable(storage) {
		t.Fatal("Cookie-only storage must ignore OpenAPI status and refresh expiry")
	}
	storage.Status = model.StatusDisabled
	if storageUsable(storage) {
		t.Fatal("disabled storage must remain unavailable in Cookie-only mode")
	}
	storage.Status = model.StatusExpired
	storage.Cookie = ""
	if storageUsable(storage) {
		t.Fatal("Cookie-only storage without Cookie should be unavailable")
	}
}

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

	got, err := svc.ensureDirPath(context.Background(), nil, model.Match302AccessModeAuto, "target-token", "/FilmFusion Cache")
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

func TestEnsureDirPathCookieOnlyDoesNotFallBackToOpenAPI(t *testing.T) {
	openCalls := 0
	directorySvc := &fakeBalanceDirectoryService{
		resolveDirPathWithClient: func(*driver.Pan115Client, string) (string, bool, error) {
			return "", false, errors.New("cookie lookup unavailable")
		},
		getDirectoriesWithClient: func(*driver.Pan115Client, string, int, int) (Web115ListResult, error) {
			return Web115ListResult{}, errors.New("cookie list unavailable")
		},
		getDirectoriesWithOpenAPI: func(context.Context, string, string, int, int) (Web115ListResult, error) {
			openCalls++
			return Web115ListResult{}, nil
		},
	}
	svc := NewBalanceAssignmentService(nil)
	svc.directory115Svc = directorySvc

	_, err := svc.ensureDirPath(
		context.Background(),
		nil,
		model.Match302AccessModeCookieOnly,
		"must-not-be-used",
		"/cache",
	)
	if err == nil || openCalls != 0 {
		t.Fatalf("error=%v openCalls=%d", err, openCalls)
	}
}

func TestEnsureDirPathOpenAPIOnlySkipsCookie(t *testing.T) {
	cookieCalls := 0
	directorySvc := &fakeBalanceDirectoryService{
		resolveDirPathWithClient: func(*driver.Pan115Client, string) (string, bool, error) {
			cookieCalls++
			return "", false, errors.New("unexpected Cookie path lookup")
		},
		getDirectoriesWithClient: func(*driver.Pan115Client, string, int, int) (Web115ListResult, error) {
			cookieCalls++
			return Web115ListResult{}, errors.New("unexpected Cookie list")
		},
		getDirectoriesWithOpenAPI: func(_ context.Context, accessToken, cid string, _, _ int) (Web115ListResult, error) {
			if accessToken != "target-token" || cid != "0" {
				t.Fatalf("OpenAPI token/cid = %q/%q", accessToken, cid)
			}
			return Web115ListResult{Items: []Web115File{{FileID: "cache-id", Name: "cache"}}}, nil
		},
	}
	svc := NewBalanceAssignmentService(nil)
	svc.directory115Svc = directorySvc

	got, err := svc.ensureDirPath(
		context.Background(),
		nil,
		model.Match302AccessModeOpenAPIOnly,
		"target-token",
		"/cache",
	)
	if err != nil {
		t.Fatalf("ensureDirPath returned error: %v", err)
	}
	if got != "cache-id" || cookieCalls != 0 {
		t.Fatalf("cid=%q cookieCalls=%d", got, cookieCalls)
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

	got, err := svc.ensureDirPath(context.Background(), nil, model.Match302AccessModeAuto, "target-token", "/cache")
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

	got, err := svc.ensureDirPath(context.Background(), nil, model.Match302AccessModeAuto, "target-token", "/cache")
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

	_, err := svc.ensureDirPath(context.Background(), nil, model.Match302AccessModeAuto, "target-token", "/cache")
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

type fakeBalanceSourceFileResolver struct {
	cookie      string
	path        string
	file        Web115File
	found       bool
	err         error
	openFile    Web115File
	openFound   bool
	openErr     error
	openCalls   int
	cookieCalls int
}

func (f *fakeBalanceSourceFileResolver) NewClient(cookie string) (*driver.Pan115Client, error) {
	f.cookieCalls++
	f.cookie = cookie
	if f.err != nil {
		return nil, f.err
	}
	return nil, nil
}

func (f *fakeBalanceSourceFileResolver) ResolveFilePathWithClient(_ *driver.Pan115Client, filePath string) (Web115File, bool, error) {
	f.path = filePath
	return f.file, f.found, f.err
}

func (f *fakeBalanceSourceFileResolver) ResolveFilePathWithOpenAPI(_ context.Context, _ string, filePath string) (Web115File, bool, error) {
	f.openCalls++
	f.path = filePath
	return f.openFile, f.openFound, f.openErr
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
