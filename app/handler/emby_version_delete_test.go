package handler

// 所有删除验证使用独立临时目录和模拟 Emby，不接触真实媒体文件。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func versionDeleteFixture(t *testing.T) (*EmbyVersionCheckHandler, model.MediaDirectory, *EmbyVersionCheckJob) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delete.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.MediaDirectory{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous; sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	root := t.TempDir()
	for _, name := range []string{"Movie.1080p.strm", "Movie.2160p.strm", "Movie.720p.strm"} {
		writeTestVersionFile(t, root, "Movie {tmdb-1}/"+name)
	}
	mapping := model.MediaDirectory{UserID: 42, MappingEnabled: true, SourcePath: "/movies", SavePath: root, LinkType: model.LinkTypeStrm}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	result := scanEmbyVersionCloudPaths([]model.MediaDirectory{mapping}, "movie")
	if len(result.Items) != 1 || len(result.Items[0].Files) != 3 {
		t.Fatalf("fixture scan: %+v", result)
	}
	job := &EmbyVersionCheckJob{ID: "scan-1", Status: "completed", Result: &result}
	h := &EmbyVersionCheckHandler{jobs: map[uint]*EmbyVersionCheckJob{42: job}}
	return h, mapping, job
}

func callVersionDelete(t *testing.T, h *EmbyVersionCheckHandler, user uint, req embyVersionDeleteRequest) (*httptest.ResponseRecorder, embyVersionDeleteResponse) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/delete", func(c *gin.Context) {
		if user != 0 {
			c.Set("user_id", user)
		}
		h.DeleteFiles(c)
	})
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/delete", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request)
	var response struct {
		Data embyVersionDeleteResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return w, response.Data
}

func versionDeleteRequest(job *EmbyVersionCheckJob, files ...EmbyVersionFile) embyVersionDeleteRequest {
	req := embyVersionDeleteRequest{JobID: job.ID, RefreshEmby: true}
	for _, file := range files {
		req.Files = append(req.Files, embyVersionDeleteTarget{file.CloudPathID, file.Path})
	}
	return req
}

func TestEmbyVersionDeleteSelectedFilesAndRequestsOneEmbyScan(t *testing.T) {
	h, _, job := versionDeleteFixture(t)
	files := append([]EmbyVersionFile(nil), job.Result.Items[0].Files...)
	oldResult := job.Result
	sidecar := filepath.Join(filepath.Dir(files[0].Path), "movie.nfo")
	if err := os.WriteFile(sidecar, []byte("metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/Library/Refresh" {
			t.Errorf("unexpected Emby request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	h.embyClient = embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "test-secret"}})
	t.Cleanup(func() { _ = h.embyClient.Close() })

	w, response := callVersionDelete(t, h, 42, versionDeleteRequest(job, files[:2]...))
	if w.Code != http.StatusOK || len(response.Files) != 2 || response.EmbyRefresh.Status != "requested" || calls.Load() != 1 {
		t.Fatalf("delete response: %d %s", w.Code, w.Body.String())
	}
	for _, file := range response.Files {
		if file.Status != "deleted" {
			t.Fatalf("file was not deleted: %+v", file)
		}
		if _, err := os.Stat(file.Path); !os.IsNotExist(err) {
			t.Fatalf("selected file still exists: %s (%v)", file.Path, err)
		}
	}
	for _, path := range []string{files[2].Path, sidecar} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unselected file affected: %s (%v)", path, err)
		}
	}
	if len(oldResult.Items[0].Files) != 3 || oldResult.TotalFiles != 3 {
		t.Fatal("old in-flight snapshot was mutated")
	}
	if response.Status.Job.Result.TotalFiles != 1 || response.Status.Job.Result.DuplicateItemCount != 0 || len(response.Status.Job.Result.Items) != 0 {
		t.Fatalf("duplicate snapshot not reconciled: %+v", response.Status.Job.Result)
	}
	if h.deleting[42] {
		t.Fatal("delete lock leaked")
	}
	// 响应丢失后重复提交旧选择只会被拒绝，不会重新发送 Emby 请求。
	w, _ = callVersionDelete(t, h, 42, versionDeleteRequest(job, files[0]))
	if w.Code != http.StatusConflict || calls.Load() != 1 {
		t.Fatalf("old selection was replayed: %d", w.Code)
	}
}

func TestEmbyVersionDeleteRejectsUnauthorizedStaleAndForeignFiles(t *testing.T) {
	for _, scenario := range []string{"unauthenticated", "other-user", "old-job", "unknown-file", "duplicate-file", "running", "deleting", "ownership-changed", "mapping-disabled", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			h, mapping, job := versionDeleteFixture(t)
			file := job.Result.Items[0].Files[0]
			req := versionDeleteRequest(job, file)
			user := uint(42)
			wantHTTP := http.StatusConflict
			switch scenario {
			case "unauthenticated":
				user, wantHTTP = 0, http.StatusUnauthorized
			case "other-user":
				user = 43
			case "old-job":
				req.JobID = "old"
			case "unknown-file":
				req.Files[0].Path += ".outside"
			case "duplicate-file":
				req.Files = append(req.Files, req.Files[0])
			case "running":
				job.Running = true
			case "deleting":
				h.deleting = map[uint]bool{42: true}
			case "ownership-changed":
				database.GetDB().Model(&mapping).Update("user_id", 43)
				wantHTTP = http.StatusOK
			case "mapping-disabled":
				database.GetDB().Model(&mapping).Update("mapping_enabled", false)
				wantHTTP = http.StatusOK
			case "empty":
				req.Files = nil
				wantHTTP = http.StatusBadRequest
			}
			w, response := callVersionDelete(t, h, user, req)
			if w.Code != wantHTTP || (wantHTTP == http.StatusOK && response.Files[0].Status != "failed") {
				t.Fatalf("unsafe request accepted: %d %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(file.Path); err != nil {
				t.Fatalf("rejected request changed file: %v", err)
			}
		})
	}
}

func TestEmbyVersionDeletePartialFailureAndEmbyFailureRemainSeparate(t *testing.T) {
	h, _, job := versionDeleteFixture(t)
	files := append([]EmbyVersionFile(nil), job.Result.Items[0].Files...)
	if err := os.WriteFile(files[1].Path, []byte("changed since scan"), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret-upstream-body", 500) }))
	t.Cleanup(upstream.Close)
	h.embyClient = embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "secret-api-key"}})
	t.Cleanup(func() { _ = h.embyClient.Close() })
	w, response := callVersionDelete(t, h, 42, versionDeleteRequest(job, files[:2]...))
	if w.Code != 200 || response.Files[0].Status != "deleted" || response.Files[1].Status != "failed" || response.EmbyRefresh.Status != "failed" {
		t.Fatalf("partial result: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-") || response.Status.Job.Result.DuplicateItemCount != 1 || response.Status.Job.Result.Items[0].VersionCount != 2 {
		t.Fatalf("snapshot or error redaction: %s", w.Body.String())
	}
	if _, err := os.Stat(files[1].Path); err != nil {
		t.Fatal("modified file was removed")
	}
}

func TestEmbyVersionDeleteMissingFileAndBusyScan(t *testing.T) {
	h, mapping, job := versionDeleteFixture(t)
	file := job.Result.Items[0].Files[0]
	if _, err := h.beginVersionDelete(42, versionDeleteRequest(job, file)); err != nil {
		t.Fatal(err)
	}
	if _, started := h.startJob(42, EmbyVersionCheckRequest{}, "all", []model.MediaDirectory{mapping}); started {
		t.Fatal("scan started during deletion")
	}
	delete(h.deleting, 42)
	if err := os.Remove(file.Path); err != nil {
		t.Fatal(err)
	}
	req := versionDeleteRequest(job, file)
	req.RefreshEmby = false
	w, response := callVersionDelete(t, h, 42, req)
	if w.Code != 200 || response.Files[0].Status != "missing" || response.EmbyRefresh.Status != "skipped" || response.Status.Job.Result.TotalFiles != 2 {
		t.Fatalf("missing result: %s", w.Body.String())
	}
}

func TestEmbyVersionDeleteFileBoundaryAndIdentity(t *testing.T) {
	for _, scenario := range []string{"outside", "symlink-file", "symlink-parent", "directory", "replaced", "root-changed", "root-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			h, mapping, job := versionDeleteFixture(t)
			_ = h
			file := job.Result.Items[0].Files[0]
			outside := filepath.Join(t.TempDir(), "outside.strm")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "outside":
				file.Path = outside
			case "symlink-file":
				os.Remove(file.Path)
				if err := os.Symlink(outside, file.Path); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				dir := filepath.Dir(file.Path)
				if err := os.Rename(dir, dir+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+".old", dir); err != nil {
					t.Fatal(err)
				}
			case "directory":
				os.Remove(file.Path)
				if err := os.Mkdir(file.Path, 0700); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.Rename(file.Path, file.Path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file.Path, []byte("#EXTM3U\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file.Path, file.ModifiedAt, file.ModifiedAt); err != nil {
					t.Fatal(err)
				}
			case "root-changed":
				mapping.SavePath = t.TempDir()
			case "root-unavailable":
				if err := os.Rename(mapping.SavePath, mapping.SavePath+".gone"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Rename(mapping.SavePath+".gone", mapping.SavePath) })
			}
			if status, err := removeScannedVersionFile(file, mapping); err == nil || status != "failed" {
				t.Fatalf("unsafe file deletion accepted: %s %v", status, err)
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
				t.Fatalf("outside target affected: %q %v", data, err)
			}
		})
	}
}
