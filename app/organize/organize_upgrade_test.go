package organize

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

type upgradeTestEnv struct {
	t                            *testing.T
	h                            *Service
	db                           *gorm.DB
	client                       *driver.Pan115Client
	dir                          model.CloudDirectory
	storage                      model.CloudStorage
	files                        map[string]service.Web115File
	paths                        map[string]string
	result                       Organize115CookieResult
	oldPath, oldStrm, oldContent string
	mutations, downloads         int
	failMoveID                   string
	lostAck                      bool
}

func newUpgradeTestEnv(t *testing.T, sameName, subtitles bool) *upgradeTestEnv {
	t.Helper()
	e := &upgradeTestEnv{t: t, files: map[string]service.Web115File{}, paths: map[string]string{}}
	var err error
	e.db, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upgrade.db")), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.db.AutoMigrate(&model.CloudStorage{}, &model.CloudDirectory{}, &model.CloudPath{}, &model.OrganizePlan{}, &model.OrganizeMediaFile{}, &model.OrganizeManagedStrm{}, &model.OrganizeStorageLock{}, &model.OrganizeUpgradeJob{}, &model.PickcodeCache{}, &model.Match302BalanceAssignment{}, &model.OrganizeLog{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = e.db
	t.Cleanup(func() { database.DB = previous; conn, _ := e.db.DB(); _ = conn.Close() })
	log := logger.New(config.LogConfig{Output: "stdout", Format: "json", Level: "error"})
	t.Cleanup(func() { _ = log.Close() })
	e.h = NewService(log, nil, nil, nil, nil)
	e.client = driver.New()
	e.client.Client.SetRetryCount(0)
	e.client.Client.SetTransport(organizePreviewTransport(e.transport))
	e.h.newOrganizeClient = func(string) (*driver.Pan115Client, error) { return e.client, nil }
	e.h.readUpgradeSubtitle = func(_ *model.CloudStorage, _ string, size int64) ([]byte, error) {
		e.downloads++
		return []byte(strings.Repeat("n", int(size))), nil
	}
	e.storage = model.CloudStorage{ID: 3, UserID: 1, StorageType: model.StorageType115Open, Status: model.StatusActive, Cookie: "mock", AccessToken: "mock"}
	e.must(e.db.Create(&e.storage).Error)
	policy := service.MediaUpgradePresets()["quality"]
	policy.Enabled = true
	e.dir = model.CloudDirectory{ID: 2, UserID: 1, CloudStorageID: 3, DirectoryID: "10", DirectoryName: "影视", SavePath: t.TempDir(), ContentPrefix: "https://media.test/strm", UpgradePolicy: planJSON(policy)}
	e.must(e.db.Create(&e.dir).Error)
	targetDir := "/影视/Example (2024) {tmdb-123}"
	e.paths[targetDir] = "900"
	e.paths["/影视"] = "10"
	e.paths["/FilmFusion-Recovery/upgrade-test/900"] = "901"
	e.files["201"] = service.Web115File{FileID: "201", ParentID: "900", Name: "Example.2024.1080p.WEB-DL.H264.mkv", IsFile: true, Size: 1000, PickCode: "old-pick", Complete: true}
	e.files["101"] = service.Web115File{FileID: "101", ParentID: "100", Name: "Example.2024.2160p.REMUX.H265.mkv", IsFile: true, Size: 2000, PickCode: "new-pick", Complete: true}
	incoming := e.files["101"]
	newName := incoming.Name
	if sameName {
		newName = e.files["201"].Name
	}
	item := Organize115ItemResult{FileID: incoming.FileID, FileName: incoming.Name, PickCode: incoming.PickCode, FileSize: incoming.Size, TmdbID: "123", MediaType: "movie", RenameTo: newName, TargetDir: targetDir, TargetDirID: "900", TargetPath: path.Join(targetDir, newName)}
	item.StrmPath, item.StrmContent = buildStrmInfo(e.dir.SavePath, e.dir.ContentPrefix, item.TargetPath, false)
	items := []Organize115ItemResult{item}
	e.oldPath = path.Join(targetDir, e.files["201"].Name)
	e.oldStrm, e.oldContent = buildStrmInfo(e.dir.SavePath, e.dir.ContentPrefix, e.oldPath, false)
	e.write(e.oldStrm, e.oldContent)
	e.write(strings.TrimSuffix(e.oldStrm, ".strm")+".nfo", "old metadata")
	e.write(filepath.Join(filepath.Dir(e.oldStrm), "movie.nfo"), "shared metadata")
	if subtitles {
		oldName := strings.TrimSuffix(e.files["201"].Name, ".mkv") + ".zh.srt"
		newSubName := strings.TrimSuffix(newName, ".mkv") + ".zh.srt"
		e.files["202"] = service.Web115File{FileID: "202", ParentID: "900", Name: oldName, IsFile: true, Size: 4, PickCode: "old-sub", Complete: true}
		e.files["102"] = service.Web115File{FileID: "102", ParentID: "100", Name: "Incoming.zh.srt", IsFile: true, Size: 4, PickCode: "new-sub", Complete: true}
		items = append(items, Organize115ItemResult{FileID: "102", FileName: "Incoming.zh.srt", PickCode: "new-sub", FileSize: 4, IsSubtitle: true, MatchedVideoFileID: "101", RenameTo: newSubName, TargetDir: targetDir, TargetDirID: "900", TargetPath: path.Join(targetDir, newSubName)})
		e.write(strings.TrimSuffix(e.oldStrm, ".strm")+".zh.srt", "olds")
	}
	e.result = Organize115CookieResult{PlanID: "upgrade-test", Pipeline: &service.MediaPipelineSnapshot{Schema: 1, Source: "medianame"}, CloudDirectoryID: 2, CloudStorageID: 3, DryRun: true, Total: 1, Groups: []Organize115CookieGroup{{FolderID: "100", Total: 1, Items: items}}}
	e.syncManifest()
	_, _, err = model.UpsertPickcodeCache(e.db, path.Join("/unused", e.oldPath), "unrelated")
	e.must(err)
	oldBinding := upgradeBindings(e.dir, nil, e.oldPath)[0]
	_, _, err = model.UpsertPickcodeCache(e.db, oldBinding.CacheKey, "old-pick")
	e.must(err)
	e.must(e.db.Create(&model.Match302BalanceAssignment{ID: 1, Match302ID: 1, SourceStorageID: 3, PlaybackStorageID: 4, SourceFileID: "201", SourcePickcode: "old-pick", SourceFilePath: e.oldPath, TargetPickcode: "side-old", TargetFileID: "side-201", Status: model.BalanceAssignmentStatusReady}).Error)
	return e
}
func (e *upgradeTestEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}
func (e *upgradeTestEnv) write(target, data string) {
	e.t.Helper()
	e.must(os.MkdirAll(filepath.Dir(target), 0755))
	e.must(os.WriteFile(target, []byte(data), 0644))
}
func (e *upgradeTestEnv) syncManifest() {
	var m []organizeSourceIdentity
	for _, f := range e.files {
		if f.ParentID == "100" {
			m = append(m, sourceIdentity(f))
		}
	}
	e.result.Groups[0].SourceManifest = organizeManifest(m)
	e.result.Items = append([]Organize115ItemResult{}, e.result.Groups[0].Items...)
}
func (e *upgradeTestEnv) transport(r *http.Request) (*http.Response, error) {
	body := map[string]any{"state": true}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/files/getid":
		id := e.paths["/"+strings.TrimLeft(r.URL.Query().Get("path"), "/")]
		if id == "" {
			id = "0"
		}
		body["id"] = id
	case r.Method == http.MethodGet && r.URL.Path == "/files":
		var rows []map[string]any
		ids := []string{}
		for id := range e.files {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			f := e.files[id]
			if f.ParentID == r.URL.Query().Get("cid") {
				rows = append(rows, map[string]any{"fid": f.FileID, "cid": f.ParentID, "n": f.Name, "ico": strings.TrimPrefix(path.Ext(f.Name), "."), "s": fmt.Sprint(f.Size), "pc": f.PickCode})
			}
		}
		body["data"], body["count"] = rows, len(rows)
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "rename"):
		e.mutations++
		e.must(r.ParseForm())
		for key, values := range r.PostForm {
			if strings.HasPrefix(key, "files_new_name[") {
				id := strings.TrimSuffix(strings.TrimPrefix(key, "files_new_name["), "]")
				f := e.files[id]
				f.Name = values[0]
				e.files[id] = f
			}
		}
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "move"):
		e.mutations++
		e.must(r.ParseForm())
		target := r.PostForm.Get("pid")
		for key, values := range r.PostForm {
			if strings.HasPrefix(key, "fid[") {
				for _, id := range values {
					if id == e.failMoveID {
						body = map[string]any{"state": false, "error": "injected move failure", "errno": 1}
						break
					}
					f := e.files[id]
					f.ParentID = target
					e.files[id] = f
				}
			}
		}
		if e.lostAck {
			e.lostAck = false
			return nil, fmt.Errorf("injected lost response after successful move")
		}
	default:
		e.t.Errorf("unexpected cloud request: %s %s", r.Method, r.URL.String())
		return nil, fmt.Errorf("unexpected request")
	}
	encoded, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
}
func (e *upgradeTestEnv) preview() {
	e.t.Helper()
	e.must(e.h.prepareUpgradePreview(e.dir, &e.result))
	if d := e.result.Items[0].Upgrade; d == nil || d.Action != "replace" {
		e.t.Fatalf("unexpected decision: %+v", d)
	}
	if e.mutations != 0 || e.downloads != 0 {
		e.t.Fatal("preview mutated or downloaded")
	}
	req := Organize115CookieRequest{CloudDirectoryID: 2, FolderIDs: []string{"100"}, DryRun: true}
	e.must(e.db.Create(&model.OrganizePlan{ID: e.result.PlanID, UserID: 1, CloudDirectoryID: 2, Status: "ready", RequestJSON: planJSON(req), ResultJSON: planJSON(e.result), DirectoryJSON: organizeDirectoryConditions(e.dir)}).Error)
}
func (e *upgradeTestEnv) apply() (Organize115CookieResult, error) {
	return e.h.applyOrganizePlan(1, Organize115CookieRequest{PlanID: e.result.PlanID, CloudDirectoryID: 2, FolderIDs: []string{"100"}, FileIDs: []string{"101"}})
}
func (e *upgradeTestEnv) resume(restore bool) model.OrganizeUpgradeJob {
	e.t.Helper()
	var job model.OrganizeUpgradeJob
	e.must(e.db.First(&job, "id = ?", e.result.PlanID).Error)
	payload, err := decodeUpgradePayload(job)
	e.must(err)
	payload.Rollback = restore
	_, err = e.h.runUpgradeJob(&job, &payload, &e.storage, e.client)
	e.must(err)
	return job
}

func TestOrganizeUpgradeReplacesFilesAndAllStrmMappings(t *testing.T) {
	for _, sameName := range []bool{false, true} {
		t.Run(fmt.Sprint("same-name=", sameName), func(t *testing.T) {
			e := newUpgradeTestEnv(t, sameName, true)
			mapping := model.CloudPath{UserID: 1, CloudStorageID: 3, SourcePath: "/影视", SourceType: model.SourceTypeMoviePilot2, LocalPath: t.TempDir(), ContentPrefix: "https://second.test/play", ContentEncodeURI: true}
			e.must(e.db.Create(&mapping).Error)
			secondOld := upgradeBindings(e.dir, []model.CloudPath{mapping}, e.oldPath)[1]
			e.write(secondOld.Path, secondOld.Content)
			e.preview()
			e.lostAck = true
			result, err := e.apply()
			e.must(err)
			if result.UpgradeJob == nil || result.UpgradeJob.Status != "completed" {
				t.Fatalf("job failed: %+v", result.UpgradeJob)
			}
			if e.files["201"].ParentID != "901" || e.files["202"].ParentID != "901" || e.files["101"].ParentID != "900" || e.files["102"].ParentID != "900" {
				t.Fatalf("cloud state: %+v", e.files)
			}
			for _, binding := range result.Items[0].Upgrade.Bindings {
				data, err := os.ReadFile(binding.Path)
				e.must(err)
				if string(data) != binding.Content {
					t.Fatalf("wrong STRM: %s", data)
				}
				var cache model.PickcodeCache
				e.must(e.db.Where("file_path = ?", binding.CacheKey).First(&cache).Error)
				if cache.Pickcode != "new-pick" {
					t.Fatal("stale pickcode")
				}
			}
			if !sameName {
				if _, err := os.Stat(e.oldStrm); !os.IsNotExist(err) {
					t.Fatal("old STRM remains")
				}
				if _, err := os.Stat(secondOld.Path); !os.IsNotExist(err) {
					t.Fatal("secondary old STRM remains")
				}
			}
			if _, err := os.Stat(strings.TrimSuffix(e.oldStrm, ".strm") + ".nfo"); !os.IsNotExist(err) {
				t.Fatal("per-file metadata remains")
			}
			data, err := os.ReadFile(filepath.Join(filepath.Dir(e.oldStrm), "movie.nfo"))
			e.must(err)
			if string(data) != "shared metadata" {
				t.Fatal("shared metadata modified")
			}
			newSubtitle := strings.TrimSuffix(result.Items[0].StrmPath, ".strm") + ".zh.srt"
			data, err = os.ReadFile(newSubtitle)
			e.must(err)
			if string(data) != "nnnn" || e.downloads != 1 {
				t.Fatalf("subtitle sync: %q downloads=%d", data, e.downloads)
			}
			var assignment model.Match302BalanceAssignment
			e.must(e.db.First(&assignment, 1).Error)
			if !assignment.Retired || assignment.TargetFileID != "side-201" {
				t.Fatal("old 302 cache was revived or lost cleanup metadata")
			}
			var locks int64
			e.must(e.db.Model(&model.OrganizeStorageLock{}).Count(&locks).Error)
			if locks != 0 {
				t.Fatal("success retained storage lock")
			}
			if _, err := e.apply(); err == nil {
				t.Fatal("plan applied twice")
			}
		})
	}
}

func TestOrganizeUpgradeFailureCanResumeOrRestore(t *testing.T) {
	for _, tc := range []struct {
		name              string
		sameName, restore bool
		failID            string
	}{{"continue after displacement", true, false, "101"}, {"restore after displacement", true, true, "101"}, {"restore after STRM commit", false, true, "201"}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpgradeTestEnv(t, tc.sameName, true)
			e.preview()
			e.failMoveID = tc.failID
			result, err := e.apply()
			e.must(err)
			if result.UpgradeJob == nil || result.UpgradeJob.Status != "failed" {
				t.Fatalf("expected recoverable failure: %+v", result.UpgradeJob)
			}
			if err := e.h.acquireOrganizeStorageLock(3, "other-task"); err == nil {
				t.Fatal("failed upgrade did not block conflicting organizer")
			}
			e.failMoveID = ""
			job := e.resume(tc.restore)
			if tc.restore {
				if job.Status != "rolled_back" || e.files["101"].ParentID != "100" || e.files["201"].ParentID != "900" || e.files["102"].ParentID != "100" || e.files["202"].ParentID != "900" {
					t.Fatalf("restore failed: %+v %+v", job, e.files)
				}
				data, err := os.ReadFile(e.oldStrm)
				e.must(err)
				if string(data) != e.oldContent {
					t.Fatal("old STRM not restored")
				}
				data, err = os.ReadFile(strings.TrimSuffix(e.oldStrm, ".strm") + ".zh.srt")
				e.must(err)
				if string(data) != "olds" {
					t.Fatal("old subtitle not restored")
				}
			} else if job.Status != "completed" {
				t.Fatal("resume incomplete")
			}
		})
	}
}

func TestOrganizeUpgradeRejectsStaleOrUnownedChanges(t *testing.T) {
	for _, change := range []string{"target", "local", "source", "policy"} {
		t.Run(change, func(t *testing.T) {
			e := newUpgradeTestEnv(t, false, false)
			e.preview()
			switch change {
			case "target":
				e.files["other"] = service.Web115File{FileID: "other", ParentID: "900", Name: "Other.1080p.mkv", Size: 99, PickCode: "other", IsFile: true}
			case "source":
				f := e.files["101"]
				f.Size++
				e.files["101"] = f
			case "local":
				e.write(e.oldStrm, "changed by user")
			case "policy":
				e.must(e.db.Model(&e.dir).Update("upgrade_policy", "{}").Error)
			}
			if _, err := e.apply(); err == nil {
				t.Fatal("stale plan accepted")
			}
			if e.mutations != 0 {
				t.Fatal("stale plan mutated cloud")
			}
		})
	}
}

func TestOrganizeUpgradeIdentityAndBlockedSelection(t *testing.T) {
	if _, err := upgradeMediaKey("123", "movie", "Example.S01E01.mkv", service.MediaUpgradeFacts{}); err == nil {
		t.Fatal("movie accepted a TV identity")
	}
	for _, name := range []string{"Example.S01E01-E02.mkv", "Example.S01E01E02.mkv", "Example.S01.mkv"} {
		if _, err := upgradeMediaKey("123", "tv", name, service.MediaUpgradeFacts{}); err == nil {
			t.Fatalf("unsafe identity accepted: %s", name)
		}
	}
	key, err := upgradeMediaKey("123", "tv", "/Example {tmdb-123}/Season 00/Example.S00E01.mkv", service.MediaUpgradeFacts{})
	if err != nil || !strings.Contains(key, ":s0:e1:") {
		t.Fatalf("specials identity: %s %v", key, err)
	}
	e := newUpgradeTestEnv(t, false, false)
	f := e.files["101"]
	f.Name = "Example.2024.720p.WEB-DL.mkv"
	e.files["101"] = f
	e.result.Groups[0].Items[0].FileName = f.Name
	e.syncManifest()
	e.must(e.h.prepareUpgradePreview(e.dir, &e.result))
	if e.result.Items[0].Upgrade.Action != "keep" {
		t.Fatal("lower quality accepted")
	}
	if _, err := selectOrganizePlan(e.result, Organize115CookieRequest{FileIDs: []string{"101"}}); err == nil {
		t.Fatal("client overrode blocked decision")
	}
	selected, err := selectOrganizePlan(e.result, Organize115CookieRequest{})
	e.must(err)
	if selected.Total != 0 {
		t.Fatal("blocked candidate included")
	}
}

func TestOrganizeUpgradeRemembersOriginalQualityAfterRenaming(t *testing.T) {
	e := newUpgradeTestEnv(t, true, false)
	e.preview()
	result, err := e.apply()
	e.must(err)
	if result.UpgradeJob.Status != "completed" {
		t.Fatal(result.UpgradeJob.Error)
	}
	// The target template kept a misleading 1080p label. The next comparison
	// must use the original 2160p facts bound to this cloud file's identity.
	e.files["103"] = service.Web115File{FileID: "103", ParentID: "100", Name: "Example.2024.1080p.REMUX.mkv", Size: 3000, PickCode: "candidate", IsFile: true, Complete: true}
	item := e.result.Groups[0].Items[0]
	item.FileID, item.FileName, item.FileSize, item.PickCode = "103", e.files["103"].Name, 3000, "candidate"
	item.Upgrade = nil
	e.result.Groups[0].Items = []Organize115ItemResult{item}
	e.syncManifest()
	e.must(e.h.prepareUpgradePreview(e.dir, &e.result))
	decision := e.result.Items[0].Upgrade
	if decision.Action != "keep" || len(decision.Existing) != 1 || decision.Existing[0].Facts.Resolution != "2160p" {
		t.Fatalf("original quality lost: %+v", decision)
	}
}

func TestOrganizeUpgradeUsesRegisteredOldStrmAfterConfigurationChange(t *testing.T) {
	e := newUpgradeTestEnv(t, false, false)
	oldBinding := upgradeBindings(e.dir, nil, e.oldPath)[0]
	e.must(e.db.Create(&model.OrganizeManagedStrm{JobID: "previous", UserID: 1, CloudStorageID: 3, FileID: "201", ParentID: "900", Root: oldBinding.Root, Path: oldBinding.Path, Content: oldBinding.Content, CacheKey: oldBinding.CacheKey, CloudPath: e.oldPath}).Error)
	e.dir.ContentPrefix = "https://changed.test/play"
	e.must(e.db.Model(&e.dir).Update("content_prefix", e.dir.ContentPrefix).Error)
	item := &e.result.Groups[0].Items[0]
	item.StrmPath, item.StrmContent = buildStrmInfo(e.dir.SavePath, e.dir.ContentPrefix, item.TargetPath, false)
	e.preview()
	result, err := e.apply()
	e.must(err)
	if result.UpgradeJob.Status != "completed" {
		t.Fatal(result.UpgradeJob.Error)
	}
	var oldCount int64
	e.must(e.db.Model(&model.PickcodeCache{}).Where("file_path = ?", oldBinding.CacheKey).Count(&oldCount).Error)
	if oldCount != 0 {
		t.Fatal("old configured prefix still resolves stale pickcode")
	}
	data, err := os.ReadFile(item.StrmPath)
	e.must(err)
	if !strings.HasPrefix(string(data), e.dir.ContentPrefix) {
		t.Fatal("new STRM kept old prefix")
	}
}

func TestOrganizeUpgradeRejectsStaleRetryAndCanRestoreBeforePreparation(t *testing.T) {
	e := newUpgradeTestEnv(t, false, true)
	e.preview()
	e.h.readUpgradeSubtitle = func(*model.CloudStorage, string, int64) ([]byte, error) {
		return nil, fmt.Errorf("injected subtitle failure")
	}
	result, err := e.apply()
	e.must(err)
	if result.UpgradeJob.Status != "failed" || e.mutations != 0 {
		t.Fatal("preparation failure moved files")
	}
	var stale model.OrganizeUpgradeJob
	e.must(e.db.First(&stale, "id = ?", e.result.PlanID).Error)
	payload, err := decodeUpgradePayload(stale)
	e.must(err)
	e.must(e.db.Model(&model.OrganizeUpgradeJob{}).Where("id = ?", stale.ID).Update("payload_json", stale.PayloadJSON+" ").Error)
	if _, err = e.h.runUpgradeJob(&stale, &payload, &e.storage, e.client); err == nil {
		t.Fatal("stale retry overwrote newer journal")
	}
	job := e.resume(true)
	if job.Status != "rolled_back" || e.mutations != 0 {
		t.Fatal("failed preparation did not release cleanly")
	}
}

func TestOrganizeUpgradeRetryIsUserScoped(t *testing.T) {
	e := newUpgradeTestEnv(t, false, false)
	e.preview()
	e.failMoveID = "101"
	_, err := e.apply()
	e.must(err)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", uint(99)) })
	router.POST("/jobs/:id/retry", (&Handler{Service: e.h}).RetryUpgradeJob)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/jobs/upgrade-test/retry", strings.NewReader(`{"restore":true}`))
	req.Header.Set("Content-Type", "application/json")
	before := e.mutations
	router.ServeHTTP(w, req)
	if w.Code != 404 || e.mutations != before {
		t.Fatalf("unauthorized retry: %d %s", w.Code, w.Body.String())
	}
}

func TestOrganizeUpgradeFencesStaleRecordWrites(t *testing.T) {
	e := newUpgradeTestEnv(t, false, false)
	e.preview()
	e.failMoveID = "101"
	_, err := e.apply()
	e.must(err)
	var stale model.OrganizeUpgradeJob
	e.must(e.db.First(&stale, "id = ?", e.result.PlanID).Error)
	payload, err := decodeUpgradePayload(stale)
	e.must(err)
	e.must(e.db.Model(&model.OrganizeUpgradeJob{}).Where("id = ?", stale.ID).Updates(map[string]any{"status": "running", "run_token": "new-owner"}).Error)
	if err = e.h.checkpointUpgradeJob(&stale, &payload); err == nil {
		t.Fatal("stale worker updated journal")
	}
	if err = e.h.persistUpgradeGuards(&stale, &payload); err == nil {
		t.Fatal("stale worker rewrote entry ownership")
	}
	if err = e.h.commitUpgradeRecords(&stale, &payload, false); err == nil {
		t.Fatal("stale worker replaced media and playback records")
	}
	stale.Status = "completed"
	if err = e.h.finishUpgradeJob(&stale, &payload, "executed"); err == nil {
		t.Fatal("stale worker completed the new owner's job")
	}
	var current model.OrganizeUpgradeJob
	e.must(e.db.First(&current, "id = ?", stale.ID).Error)
	var assignment model.Match302BalanceAssignment
	e.must(e.db.First(&assignment, 1).Error)
	var locks int64
	e.must(e.db.Model(&model.OrganizeStorageLock{}).Where("plan_id = ?", stale.ID).Count(&locks).Error)
	if current.RunToken != "new-owner" || current.Status != "running" || assignment.Retired || locks != 1 {
		t.Fatal("stale request modified current job, playback cache, or account lock")
	}
}
