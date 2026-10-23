package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"film-fusion/app/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type folderMoveFake struct {
	dirs                             map[string][]CloudBrowseItem
	mkdirCalls, moveCalls, listCalls int
	moveErr, createErr               error
	moveApplies                      bool
	createdID                        string
	resolveCalls                     int
	resolvePath                      string
	resolveErr                       error
	block, entered                   chan struct{}
	once                             sync.Once
}

func newFolderMoveFake() *folderMoveFake {
	return &folderMoveFake{dirs: map[string][]CloudBrowseItem{
		"0":  {{FileID: "10", ParentID: "0", Name: "来源"}, {FileID: "20", ParentID: "0", Name: "自定义位置"}},
		"10": {{FileID: "100", ParentID: "10", Name: "Movie.mkv", IsFile: true, Size: 3}}, "20": {},
	}, createdID: "30", moveApplies: true}
}
func (f *folderMoveFake) Close() {}
func (f *folderMoveFake) ResolveDirectoryPath(ctx context.Context, cid string) (string, error) {
	f.resolveCalls++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	if f.resolvePath != "" {
		return f.resolvePath, nil
	}
	parts := []string{}
	for depth := 0; depth < 100; depth++ {
		if cid == "0" {
			return "/" + path.Join(parts...), nil
		}
		found := false
		for parent, rows := range f.dirs {
			for _, row := range rows {
				if row.FileID == cid && !row.IsFile {
					parts = append([]string{row.Name}, parts...)
					cid, found = parent, true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return "", errors.New("missing directory")
		}
	}
	return "", errors.New("directory loop")
}
func (f *folderMoveFake) List(ctx context.Context, cid string, offset, limit int) (*CloudBrowseResult, error) {
	if f.block != nil {
		f.once.Do(func() {
			close(f.entered)
			select {
			case <-f.block:
			case <-ctx.Done():
			}
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.listCalls++
	rows, ok := f.dirs[cid]
	if !ok {
		return nil, errors.New("secret cookie invalid folder")
	}
	end := min(offset+limit, len(rows))
	start := min(offset, len(rows))
	return &CloudBrowseResult{Items: append([]CloudBrowseItem{}, rows[start:end]...), CID: cid, Offset: offset, Limit: limit, Total: int64(len(rows))}, nil
}
func (f *folderMoveFake) Mkdir(_ context.Context, parent, name string) (string, error) {
	f.mkdirCalls++
	f.dirs[f.createdID] = []CloudBrowseItem{}
	f.dirs[parent] = append(f.dirs[parent], CloudBrowseItem{FileID: f.createdID, ParentID: parent, Name: name})
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.createdID, nil
}
func (f *folderMoveFake) Move(_ context.Context, fileID, folderID string) error {
	f.moveCalls++
	if f.moveApplies {
		for parent, rows := range f.dirs {
			for i, row := range rows {
				if row.FileID == fileID {
					f.dirs[parent] = append(rows[:i], rows[i+1:]...)
					row.ParentID = folderID
					f.dirs[folderID] = append(f.dirs[folderID], row)
					return f.moveErr
				}
			}
		}
	}
	return f.moveErr
}
func folderMoveFixture(t *testing.T) (*CloudFolderMoveService, *folderMoveFake, CloudFolderMoveRequest) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&model.CloudStorage{}, &model.OrganizeStorageLock{}, &model.CloudPath{}, &model.OrganizeManagedStrm{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.CloudStorage{ID: 1, UserID: 7, StorageType: model.StorageType115Open, Status: model.StatusActive, StorageName: "test", AccessToken: "secret", Cookie: "UID=7; CID=test; SEID=test"}).Error; err != nil {
		t.Fatal(err)
	}
	f := newFolderMoveFake()
	s := NewCloudFolderMoveService(db, nil)
	s.newProvider = func(context.Context, model.CloudStorage, string) (cloudFolderProvider, error) { return f, nil }
	return s, f, CloudFolderMoveRequest{AccessMethod: "openapi", SourceParentID: "10", FileID: "100", FileName: "Movie.mkv", TargetParentID: "20", FolderName: "Movie"}
}
func requireMoveStatus(t *testing.T, err error, status int) {
	t.Helper()
	var e *CloudFolderMoveError
	if !errors.As(err, &e) || e.Status != status {
		t.Fatalf("error=%v, want HTTP %d", err, status)
	}
}

func TestCloudFolderMoveCustomTargetAndIdempotency(t *testing.T) {
	s, f, req := folderMoveFixture(t)
	result, err := s.Move(context.Background(), 7, 1, req)
	if err != nil || result.Status != "moved" || result.FolderID != "30" || result.ReusedFolder || f.mkdirCalls != 1 || f.moveCalls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d/%d", result, err, f.mkdirCalls, f.moveCalls)
	}
	if len(f.dirs["10"]) != 0 || len(f.dirs["20"]) != 1 || f.dirs["20"][0].FileID != "30" || f.dirs["30"][0].FileID != "100" {
		t.Fatalf("wrong target: %+v", f.dirs)
	}
	req.FolderID = result.FolderID
	result, err = s.Move(context.Background(), 7, 1, req)
	if err != nil || result.Status != "already_moved" || !result.ReusedFolder || f.mkdirCalls != 1 || f.moveCalls != 1 {
		t.Fatalf("retry=%+v err=%v", result, err)
	}
	var locks int64
	s.db.Model(&model.OrganizeStorageLock{}).Count(&locks)
	if locks != 0 {
		t.Fatal("operation lock was not released")
	}
}
func TestCloudFolderMovePartialFailureAndSafeRetry(t *testing.T) {
	for _, mode := range []string{"move rejected", "move applied response lost", "create response lost"} {
		t.Run(mode, func(t *testing.T) {
			s, f, req := folderMoveFixture(t)
			if mode == "create response lost" {
				f.createErr = errors.New("secret")
			} else {
				f.moveErr = errors.New("token=secret")
				f.moveApplies = mode == "move applied response lost"
			}
			result, err := s.Move(context.Background(), 7, 1, req)
			if mode == "move applied response lost" {
				if err != nil || result.Status != "moved" {
					t.Fatalf("verified success lost: %+v %v", result, err)
				}
				return
			}
			requireMoveStatus(t, err, 502)
			if result.Status != "pending" || result.FolderID != "30" || strings.Contains(err.Error(), "secret") {
				t.Fatalf("partial=%+v err=%v", result, err)
			}
			if f.mkdirCalls != 1 || f.moveCalls > 1 {
				t.Fatal("implicit mutation retry")
			}
			req.FolderID = result.FolderID
			f.createErr = nil
			f.moveErr = nil
			f.moveApplies = true
			result, err = s.Move(context.Background(), 7, 1, req)
			if err != nil || result.Status != "moved" || !result.ReusedFolder || f.mkdirCalls != 1 {
				t.Fatalf("retry=%+v err=%v", result, err)
			}
		})
	}
}
func TestCloudFolderMoveRejectsChangedSourcesAndCollisions(t *testing.T) {
	for _, mode := range []string{"source name", "source folder", "source moved", "target file", "ambiguous folders", "existing file", "retry wrong parent", "retry renamed", "invalid target"} {
		t.Run(mode, func(t *testing.T) {
			s, f, req := folderMoveFixture(t)
			switch mode {
			case "source name":
				f.dirs["10"][0].Name = "Changed.mkv"
			case "source folder":
				f.dirs["10"][0].IsFile = false
			case "source moved":
				f.dirs["10"] = nil
			case "target file":
				f.dirs["20"] = []CloudBrowseItem{{FileID: "30", ParentID: "20", Name: "Movie", IsFile: true}}
			case "ambiguous folders":
				f.dirs["20"] = []CloudBrowseItem{{FileID: "30", ParentID: "20", Name: "Movie"}, {FileID: "31", ParentID: "20", Name: "Movie"}}
			case "existing file":
				f.dirs["20"] = []CloudBrowseItem{{FileID: "30", ParentID: "20", Name: "Movie"}}
				f.dirs["30"] = []CloudBrowseItem{{FileID: "200", ParentID: "30", Name: "Movie.mkv", IsFile: true}}
			case "retry wrong parent":
				req.FolderID = "31"
			case "retry renamed":
				req.FolderID = "30"
				f.dirs["20"] = []CloudBrowseItem{{FileID: "30", ParentID: "20", Name: "Changed"}}
			case "invalid target":
				req.TargetParentID = "100"
			}
			_, err := s.Move(context.Background(), 7, 1, req)
			if err == nil || f.mkdirCalls != 0 || f.moveCalls != 0 {
				t.Fatalf("unsafe operation: err=%v create=%d move=%d", err, f.mkdirCalls, f.moveCalls)
			}
		})
	}
}
func TestCloudFolderMoveValidationOwnershipAndLock(t *testing.T) {
	s, f, req := folderMoveFixture(t)
	for _, name := range []string{"", ".", "..", "../other", "foo\\bar", "bad\nname", strings.Repeat("字", 86)} {
		changed := req
		changed.FolderName = name
		_, err := s.Move(context.Background(), 7, 1, changed)
		requireMoveStatus(t, err, 400)
	}
	_, err := s.Move(context.Background(), 8, 1, req)
	requireMoveStatus(t, err, 404)
	s.db.Create(&model.OrganizeStorageLock{CloudStorageID: 1, PlanID: "recover-upgrade"})
	_, err = s.Move(context.Background(), 7, 1, req)
	requireMoveStatus(t, err, 409)
	if f.listCalls != 0 || f.mkdirCalls != 0 || f.moveCalls != 0 {
		t.Fatal("provider called before validation/ownership/lock")
	}
}
func TestCloudFolderMovePaginationAndBrowse(t *testing.T) {
	s, f, req := folderMoveFixture(t)
	for i := 0; i < 230; i++ {
		f.dirs["20"] = append(f.dirs["20"], CloudBrowseItem{FileID: fmt.Sprint(1000 + i), ParentID: "20", Name: fmt.Sprint("Other", i)})
	}
	f.dirs["20"] = append(f.dirs["20"], CloudBrowseItem{FileID: "30", ParentID: "20", Name: "Movie"})
	f.dirs["30"] = []CloudBrowseItem{}
	page, err := s.Browse(context.Background(), 7, 1, "openapi", "20", 200, 100)
	if err != nil || page.Total != 231 || len(page.Items) != 31 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	result, err := s.Move(context.Background(), 7, 1, req)
	if err != nil || !result.ReusedFolder || f.mkdirCalls != 0 || f.moveCalls != 1 {
		t.Fatalf("pagination lost existing folder: %+v %v", result, err)
	}
}
func TestCloudFolderMoveConcurrentOperationAndCancellation(t *testing.T) {
	s, f, req := folderMoveFixture(t)
	f.block = make(chan struct{})
	f.entered = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Move(ctx, 7, 1, req); done <- err }()
	select {
	case <-f.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not start")
	}
	_, err := s.Move(context.Background(), 7, 1, req)
	requireMoveStatus(t, err, 409)
	cancel()
	select {
	case err = <-done:
		requireMoveStatus(t, err, 408)
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation stuck")
	}
	if f.mkdirCalls != 0 || f.moveCalls != 0 {
		t.Fatal("mutated after cancellation")
	}
}
