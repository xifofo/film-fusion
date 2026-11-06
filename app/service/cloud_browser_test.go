package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/model"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type cloudBrowserListFunc func(context.Context, string, int, int) (*CloudBrowserPage, error)

func (f cloudBrowserListFunc) List(ctx context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
	return f(ctx, cid, offset, limit)
}

type cloudBrowserRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cloudBrowserRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newCloudBrowserTestService(t *testing.T) (*CloudBrowserService, model.MediaDirectory) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "browser.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}, &model.MediaDirectory{}, &model.OrganizeManagedStrm{}, &model.OrganizeStorageLock{}); err != nil {
		t.Fatal(err)
	}
	storage := model.CloudStorage{ID: 1, UserID: 7, StorageType: model.StorageType115Open, StorageName: "test", Status: model.StatusActive, AccessToken: "offline-token", Cookie: "UID=7_A1; CID=offline; SEID=offline"}
	if err := db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	directory := model.MediaDirectory{OrganizeEnabled: true, ID: 10, UserID: 7, CloudStorageID: 1, DirectoryID: "10", DirectoryName: "影片", SavePath: t.TempDir(), ContentPrefix: "https://media.example/play", IncludeExtensions: `["mkv","mp4"]`}
	if err := db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewCloudBrowserService(db, nil)
	// Fail closed if a test forgets to install its explicit in-memory provider.
	svc.newProvider = func(model.CloudStorage, string) (cloudBrowserProvider, error) {
		return nil, errors.New("no mock provider installed")
	}
	t.Cleanup(svc.Stop)
	return svc, directory
}

func cloudBrowserTestPath(cid string) []CloudBrowserBreadcrumb {
	path := []CloudBrowserBreadcrumb{{ID: "0", ParentID: "0", Name: "全部文件"}}
	if cid != "0" {
		path = append(path, CloudBrowserBreadcrumb{ID: "10", ParentID: "0", Name: "真实目录"})
	}
	if cid == "20" {
		path = append(path, CloudBrowserBreadcrumb{ID: "20", ParentID: "10", Name: "子目录"})
	}
	return path
}

func cloudBrowserTestPage(cid string, offset, limit int, rows []CloudBrowserEntry) *CloudBrowserPage {
	start, end := min(offset, len(rows)), min(offset+limit, len(rows))
	return &CloudBrowserPage{CID: cid, Offset: offset, Limit: limit, Total: int64(len(rows)), HasMore: end < len(rows), Breadcrumbs: cloudBrowserTestPath(cid), Items: append([]CloudBrowserEntry{}, rows[start:end]...)}
}

func TestCloudBrowserManualPageAndSignedFileGeneration(t *testing.T) {
	svc, directory := newCloudBrowserTestService(t)
	var calls int
	rows := []CloudBrowserEntry{{FileID: "100", ParentID: "10", Name: "Movie.mkv", IsFile: true, Complete: true, Size: 4096}, {FileID: "101", ParentID: "10", Name: "Next.mkv", IsFile: true, Complete: true}}
	svc.newProvider = func(_ model.CloudStorage, method string) (cloudBrowserProvider, error) {
		if method != "cookie" {
			t.Fatalf("unexpected fallback method %q", method)
		}
		return cloudBrowserListFunc(func(_ context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
			calls++
			return cloudBrowserTestPage(cid, offset, limit, rows), nil
		}), nil
	}
	page, err := svc.Browse(context.Background(), 7, directory.ID, "cookie", "", 0, 1)
	if err != nil || calls != 1 || len(page.Items) != 1 || !page.HasMore || page.MatchedDirectory.ID != directory.ID {
		t.Fatalf("page=%+v calls=%d err=%v", page, calls, err)
	}
	if page.Items[0].SelectionToken == "" || page.CurrentDirectoryToken == "" {
		t.Fatal("missing signed selection")
	}
	result, err := svc.GenerateSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.Items[0].SelectionToken})
	if err != nil || result.Generated != 1 || calls != 1 {
		t.Fatalf("file generation unexpectedly queried provider: %+v calls=%d err=%v", result, calls, err)
	}
	data, err := os.ReadFile(filepath.Join(directory.SavePath, "影片", "Movie.strm"))
	if err != nil || string(data) != "https://media.example/play/影片/Movie.mkv" {
		t.Fatalf("content=%q err=%v", data, err)
	}
	for _, userID := range []uint{8, 0} {
		if _, err := svc.GenerateSTRM(context.Background(), userID, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.Items[0].SelectionToken}); err == nil {
			t.Fatal("cross-user token accepted")
		}
	}
	if _, err := svc.GenerateSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.Items[0].SelectionToken + "x"}); err == nil {
		t.Fatal("tampered token accepted")
	}
	selected, err := svc.parseSelection(page.Items[0].SelectionToken, 7, directory.ID)
	if err != nil {
		t.Fatal(err)
	}
	selected.Expires = time.Now().Add(-time.Minute).Unix()
	expired, _ := svc.signSelection(selected)
	if _, err := svc.GenerateSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: expired}); err == nil {
		t.Fatal("expired token accepted")
	}
	if calls != 1 {
		t.Fatal("invalid tokens made provider calls")
	}
}

func TestCloudBrowserOwnershipAndNoMapping(t *testing.T) {
	svc, directory := newCloudBrowserTestService(t)
	var created int
	svc.newProvider = func(model.CloudStorage, string) (cloudBrowserProvider, error) {
		created++
		return cloudBrowserListFunc(func(_ context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
			return cloudBrowserTestPage(cid, offset, limit, []CloudBrowserEntry{{FileID: "100", ParentID: "0", Name: "Movie.mkv", IsFile: true, Complete: true}}), nil
		}), nil
	}
	if _, err := svc.Browse(context.Background(), 8, directory.ID, "cookie", "0", 0, 50); err == nil || created != 0 {
		t.Fatal("ownership was not checked before provider creation")
	}
	page, err := svc.Browse(context.Background(), 7, directory.ID, "cookie", "0", 0, 50)
	if err != nil || page.MatchedDirectory != nil || page.Items[0].STRMUnavailableReason == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := svc.GenerateSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.Items[0].SelectionToken}); err == nil || created != 1 {
		t.Fatal("unmapped generation accepted or queried provider")
	}
	if err := svc.db.Model(&model.CloudStorage{}).Where("id = ?", 1).Update("token_expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Browse(context.Background(), 7, directory.ID, "openapi", "10", 0, 50); err == nil || created != 1 {
		t.Fatal("expired token triggered provider initialization")
	}
}

func waitCloudBrowserJob(t *testing.T, svc *CloudBrowserService, directoryID uint, id string) *CloudBrowserOperationResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := svc.JobStatus(context.Background(), 7, directoryID, id)
		if err != nil {
			t.Fatal(err)
		}
		if result.Finished {
			return result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func TestCloudBrowserRecursiveJobManualStartAndCollision(t *testing.T) {
	svc, directory := newCloudBrowserTestService(t)
	rows := make([]CloudBrowserEntry, 0, 104)
	for i := 0; i < 101; i++ {
		rows = append(rows, CloudBrowserEntry{FileID: fmt.Sprint(1000 + i), ParentID: "10", Name: fmt.Sprintf("Movie%03d.mkv", i), IsFile: true, Complete: true})
	}
	rows = append(rows, CloudBrowserEntry{FileID: "20", ParentID: "10", Name: "子目录", Complete: true}, CloudBrowserEntry{FileID: "2000", ParentID: "10", Name: "Movie000.mp4", IsFile: true, Complete: true})
	var calls atomic.Int32
	svc.newProvider = func(model.CloudStorage, string) (cloudBrowserProvider, error) {
		return cloudBrowserListFunc(func(_ context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
			if calls.Add(1) > 1 && limit != CloudBrowserMaxPageSize {
				return nil, fmt.Errorf("recursive page limit=%d, want %d", limit, CloudBrowserMaxPageSize)
			}
			// Upstream may return a short page even when 1150 items are requested.
			limit = min(limit, 100)
			if cid == "20" {
				return cloudBrowserTestPage(cid, offset, limit, []CloudBrowserEntry{{FileID: "3000", ParentID: "20", Name: "Child.mkv", IsFile: true, Complete: true}}), nil
			}
			return cloudBrowserTestPage(cid, offset, limit, rows), nil
		}), nil
	}
	page, err := svc.Browse(context.Background(), 7, directory.ID, "openapi", "10", 0, 1)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("manual listing paginated: calls=%d err=%v", calls.Load(), err)
	}
	started, err := svc.GenerateSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.CurrentDirectoryToken})
	if err != nil || started.JobID == "" {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	result := waitCloudBrowserJob(t, svc, directory.ID, started.JobID)
	if result.Status != "completed" || result.Generated != 102 || result.Failed != 1 || result.VisitedDirectories != 2 || calls.Load() != 4 {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
	data, _ := os.ReadFile(filepath.Join(directory.SavePath, "影片", "Movie000.strm"))
	if !strings.HasSuffix(string(data), "Movie000.mkv") {
		t.Fatalf("collision overwrote first file: %s", data)
	}
	if _, err := svc.JobStatus(context.Background(), 8, directory.ID, started.JobID); err == nil {
		t.Fatal("cross-user job read accepted")
	}
	if _, err := svc.CancelJob(context.Background(), 7, directory.ID+1, started.JobID); err == nil {
		t.Fatal("cross-directory cancellation accepted")
	}
	if calls.Load() != 4 {
		t.Fatal("status read made provider request")
	}
}

func TestCloudBrowserMaximumPageSize(t *testing.T) {
	svc, directory := newCloudBrowserTestService(t)
	calls := 0
	rows := make([]CloudBrowserEntry, CloudBrowserMaxPageSize+1)
	for i := range rows {
		rows[i] = CloudBrowserEntry{FileID: fmt.Sprint(1000 + i), ParentID: "10", Name: fmt.Sprintf("Movie%d.mkv", i), IsFile: true, Complete: true}
	}
	svc.newProvider = func(model.CloudStorage, string) (cloudBrowserProvider, error) {
		return cloudBrowserListFunc(func(_ context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
			calls++
			if limit != 1150 {
				t.Fatalf("limit=%d", limit)
			}
			return cloudBrowserTestPage(cid, offset, limit, rows), nil
		}), nil
	}
	if _, err := svc.Browse(context.Background(), 7, directory.ID, "openapi", "10", 0, 1151); err == nil || calls != 0 {
		t.Fatal("oversized page reached provider")
	}
	page, err := svc.Browse(context.Background(), 7, directory.ID, "openapi", "10", 0, 1150)
	if err != nil || calls != 1 || len(page.Items) != 1150 || !page.HasMore {
		t.Fatalf("first page err=%v calls=%d", err, calls)
	}
	page, err = svc.Browse(context.Background(), 7, directory.ID, "openapi", "10", 1150, 1150)
	if err != nil || calls != 2 || len(page.Items) != 1 || page.HasMore || page.Offset != 1150 {
		t.Fatalf("last page=%+v err=%v calls=%d", page, err, calls)
	}
}

func TestCloudBrowserJobCancellationAndMovedSubtree(t *testing.T) {
	for _, mode := range []string{"cancel", "moved"} {
		t.Run(mode, func(t *testing.T) {
			svc, directory := newCloudBrowserTestService(t)
			var calls atomic.Int32
			entered := make(chan struct{})
			svc.newProvider = func(model.CloudStorage, string) (cloudBrowserProvider, error) {
				return cloudBrowserListFunc(func(ctx context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
					call := calls.Add(1)
					if call == 1 {
						return cloudBrowserTestPage(cid, offset, limit, nil), nil
					}
					if mode == "cancel" {
						close(entered)
						<-ctx.Done()
						return nil, ctx.Err()
					}
					if cid == "10" {
						return cloudBrowserTestPage(cid, offset, limit, []CloudBrowserEntry{{FileID: "20", ParentID: "10", Name: "子目录", Complete: true}}), nil
					}
					page := cloudBrowserTestPage(cid, offset, limit, []CloudBrowserEntry{{FileID: "900", ParentID: cid, Name: "Wrong.mkv", IsFile: true, Complete: true}})
					page.Breadcrumbs = []CloudBrowserBreadcrumb{{ID: "0", ParentID: "0"}, {ID: "20", ParentID: "0", Name: "已移动"}}
					return page, nil
				}), nil
			}
			page, err := svc.Browse(context.Background(), 7, directory.ID, "cookie", "10", 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			started, err := svc.GenerateSTRM(context.Background(), 7, directory.ID, CloudBrowserGenerateRequest{SelectionToken: page.CurrentDirectoryToken})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("job did not enter mock")
				}
				if _, err := svc.CancelJob(context.Background(), 7, directory.ID, started.JobID); err != nil {
					t.Fatal(err)
				}
			}
			result := waitCloudBrowserJob(t, svc, directory.ID, started.JobID)
			want := "failed"
			wantCalls := int32(3)
			if mode == "cancel" {
				want = "cancelled"
				wantCalls = 2
			}
			if result.Status != want || result.Generated != 0 || calls.Load() != wantCalls {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
			svc.Stop()
			var locks int64
			svc.db.Model(&model.OrganizeStorageLock{}).Count(&locks)
			if locks != 0 {
				t.Fatalf("job leaked %d locks", locks)
			}
		})
	}
}

func TestCloudBrowserActiveJobCapAndStop(t *testing.T) {
	svc, first := newCloudBrowserTestService(t)
	if latest, err := svc.ActiveJob(context.Background(), 7, first.ID); err != nil || latest != nil {
		t.Fatalf("empty local discovery=%+v err=%v", latest, err)
	}
	if _, err := svc.ActiveJob(context.Background(), 8, first.ID); err == nil {
		t.Fatal("cross-user job discovery accepted")
	}
	entered := make(chan struct{}, 2)
	var calls atomic.Int32
	svc.newProvider = func(model.CloudStorage, string) (cloudBrowserProvider, error) {
		return cloudBrowserListFunc(func(ctx context.Context, _ string, _, _ int) (*CloudBrowserPage, error) {
			calls.Add(1)
			entered <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}), nil
	}
	configs := []model.MediaDirectory{first}
	for i := uint(2); i <= 3; i++ {
		storage := model.CloudStorage{ID: i, UserID: 7, StorageType: model.StorageType115Open, Status: model.StatusActive, Cookie: "UID=7_A1; CID=offline; SEID=offline"}
		if err := svc.db.Create(&storage).Error; err != nil {
			t.Fatal(err)
		}
		config := first
		config.ID = i * 10
		config.CloudStorageID = i
		if err := svc.db.Create(&config).Error; err != nil {
			t.Fatal(err)
		}
		configs = append(configs, config)
	}
	start := func(config model.MediaDirectory) (*CloudBrowserOperationResult, error) {
		token, err := svc.signSelection(cloudBrowserSelection{UserID: 7, EntryDirectoryID: config.ID, StorageID: config.CloudStorageID, Method: "cookie", Expires: time.Now().Add(time.Minute).Unix(), Ancestors: cloudBrowserTestPath("10"), Item: CloudBrowserEntry{FileID: "10", ParentID: "0", Name: "真实目录", Complete: true}})
		if err != nil {
			t.Fatal(err)
		}
		return svc.GenerateSTRM(context.Background(), 7, config.ID, CloudBrowserGenerateRequest{SelectionToken: token})
	}
	one, err := start(configs[0])
	if err != nil {
		t.Fatal(err)
	}
	if latest, err := svc.ActiveJob(context.Background(), 7, first.ID); err != nil || latest.JobID != one.JobID || latest.Name == "" {
		t.Fatalf("active job recovery=%+v err=%v", latest, err)
	}
	if _, err := start(configs[0]); err == nil {
		t.Fatal("duplicate job for account accepted")
	}
	two, err := start(configs[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := start(configs[2]); err == nil {
		t.Fatal("third active job accepted")
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("mock job did not start")
		}
	}
	svc.Stop()
	for i, job := range []*CloudBrowserOperationResult{one, two} {
		status, err := svc.JobStatus(context.Background(), 7, configs[i].ID, job.JobID)
		if err != nil || !status.Finished || status.Status != "cancelled" {
			t.Fatalf("status=%+v err=%v", status, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if _, err := start(configs[2]); err == nil {
		t.Fatal("stopped service accepted new job")
	}
	var locks int64
	svc.db.Model(&model.OrganizeStorageLock{}).Count(&locks)
	if locks != 0 {
		t.Fatalf("shutdown leaked %d account locks", locks)
	}
}

func TestCloudBrowserRejectsMalformedProviderPages(t *testing.T) {
	valid := map[string]any{"state": true, "cid": "10", "offset": 0, "count": 1, "data": []any{map[string]any{"fid": "100", "cid": "10", "n": "Movie.mkv", "s": 1}}, "path": []any{map[string]any{"cid": "0", "pid": "0", "name": "root"}, map[string]any{"cid": "10", "pid": "0", "name": "Movies"}}}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing root", func(v map[string]any) { v["path"] = []any{map[string]any{"cid": "10", "pid": "0", "name": "Movies"}} }},
		{"path traversal", func(v map[string]any) {
			v["path"] = []any{map[string]any{"cid": "0", "pid": "0"}, map[string]any{"cid": "10", "pid": "0", "name": ".."}}
		}},
		{"wrong parent", func(v map[string]any) {
			v["data"] = []any{map[string]any{"fid": "100", "cid": "20", "n": "Movie.mkv", "s": 1}}
		}},
		{"wrong cid", func(v map[string]any) { v["cid"] = "20" }},
		{"wrong offset", func(v map[string]any) { v["offset"] = 1 }},
		{"no progress", func(v map[string]any) { v["data"] = []any{} }},
		{"duplicate file", func(v map[string]any) {
			v["count"] = 2
			v["data"] = []any{map[string]any{"fid": "100", "cid": "10", "n": "Movie.mkv", "s": 1}, map[string]any{"fid": "100", "cid": "10", "n": "Movie.mkv", "s": 1}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := make(map[string]any, len(valid))
			for key, value := range valid {
				payload[key] = value
			}
			test.mutate(payload)
			data, _ := json.Marshal(payload)
			if _, err := cloudBrowserParsePage(data, "cookie", "10", 0, 50); err == nil {
				t.Fatal("malformed provider page accepted")
			}
		})
	}
}

func cloudBrowserProviderResponse(req *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func TestCloudBrowserSDKOneRequestNoAuthRefreshRetryOrFallback(t *testing.T) {
	for _, method := range []string{"cookie", "openapi"} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reject=%t", method, reject), func(t *testing.T) {
				calls := 0
				base := cloudBrowserRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.URL.Query().Get("offset") != "7" || req.URL.Query().Get("limit") != "2" || req.URL.Query().Get("show_dir") != "1" {
						t.Fatalf("unexpected query: %s", req.URL.RawQuery)
					}
					if method == "cookie" && req.Header.Get("Cookie") != "test-cookie" {
						t.Fatal("cookie missing")
					}
					if method == "openapi" && req.Header.Get("Authorization") != "Bearer test-token" {
						t.Fatal("token missing")
					}
					if reject {
						return cloudBrowserProviderResponse(req, `{"state":false,"code":99,"message":"secret provider detail"}`), nil
					}
					row := map[string]any{"fid": "100", "cid": "10", "n": "Movie.mkv", "s": 1234}
					if method == "openapi" {
						row = map[string]any{"fid": "100", "pid": "10", "fn": "Movie.mkv", "fc": "1", "fs": 1234, "fta": "1"}
					}
					data, _ := json.Marshal(map[string]any{"state": true, "cid": "10", "count": 8, "offset": 7, "data": []any{row}, "path": []map[string]any{{"cid": "0", "pid": "0", "name": "root"}, {"cid": "10", "pid": "0", "name": "Movies"}}})
					return cloudBrowserProviderResponse(req, string(data)), nil
				})
				credential := "test-cookie"
				if method == "openapi" {
					credential = "test-token"
				}
				p := &cloudBrowser115Provider{method: method, credential: credential, client: &http.Client{Transport: cloudBrowserTransport{base: base, limiter: newCloudBrowserLimiter(0)}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
				page, err := p.List(context.Background(), "10", 7, 2)
				if calls != 1 {
					t.Fatalf("provider requests=%d", calls)
				}
				if reject && err == nil {
					t.Fatal("provider rejection accepted")
				}
				if !reject && (err != nil || page.Total != 8 || len(page.Items) != 1 || !page.Items[0].Complete || page.HasMore) {
					t.Fatalf("page=%+v err=%v", page, err)
				}
			})
		}
	}
}

func TestCloudBrowserSDKRedirectsAndHTTPFailuresDoNotReplay(t *testing.T) {
	for _, method := range []string{"cookie", "openapi"} {
		for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("%s/status=%d", method, status), func(t *testing.T) {
				calls := 0
				base := cloudBrowserRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					response := cloudBrowserProviderResponse(req, `{"state":false}`)
					response.StatusCode = status
					response.Header.Set("Location", req.URL.String()+"&unexpected_redirect=1")
					return response, nil
				})
				limiter := newCloudBrowserLimiter(0)
				provider := &cloudBrowser115Provider{method: method, credential: "offline", client: &http.Client{Transport: cloudBrowserTransport{base: base, limiter: limiter}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
				if _, err := provider.List(context.Background(), "10", 0, 50); err == nil || calls != 1 {
					t.Fatalf("provider replayed failure: calls=%d err=%v", calls, err)
				}
				if len(limiter.slots) != 0 {
					t.Fatal("failed response leaked limiter slot")
				}
			})
		}
	}
}

func TestCloudBrowserLimiterHoldsBodySlotsAndCancellation(t *testing.T) {
	limiter := newCloudBrowserLimiter(20 * time.Millisecond)
	var mu sync.Mutex
	var starts []time.Time
	base := cloudBrowserRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return cloudBrowserProviderResponse(req, "data"), nil
	})
	transport := cloudBrowserTransport{base: base, limiter: limiter}
	req, _ := http.NewRequest(http.MethodGet, driver.ApiFileList, nil)
	first, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := transport.RoundTrip(req.WithContext(ctx)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third request escaped body-held slots: %v", err)
	}
	mu.Lock()
	count := len(starts)
	gap := starts[1].Sub(starts[0])
	mu.Unlock()
	if count != 2 || gap < 18*time.Millisecond {
		t.Fatalf("starts=%d gap=%v", count, gap)
	}
	first.Body.Close()
	third, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	third.Body.Close()
	second.Body.Close()
	if len(limiter.slots) != 0 {
		t.Fatal("request slots leaked")
	}
	blocked, _ := http.NewRequest(http.MethodPost, "https://passportapi.115.com/app/1.0/web/1.0/token/refresh", nil)
	if _, err := transport.RoundTrip(blocked); err == nil {
		t.Fatal("refresh endpoint escaped list-only transport")
	}
}
