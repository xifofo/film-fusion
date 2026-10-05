package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
)

type planTestEnv struct {
	handler          *Service
	local            *service.MediaRecognitionService
	cfg              *config.Config
	req              Organize115CookieRequest
	db               *gorm.DB
	sourceName       string
	calls, mutations int
	tmdbFail         bool
	mutationsBody    []string
}

func setupOrganizePlanTest(t *testing.T) *planTestEnv {
	t.Helper()
	e := &planTestEnv{sourceName: "Example.2024.S00E01.[tmdb=123].mkv"}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "plans.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	e.db = db
	if err = db.AutoMigrate(&model.SystemConfig{}, &model.CloudDirectory{}, &model.CloudStorage{}, &model.OrganizePlan{}, &model.OrganizeStorageLock{}, &model.PickcodeCache{}); err != nil {
		t.Fatal(err)
	}
	prev := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = prev; sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/login/access-token":
			io.WriteString(w, `{"access_token":"test","expires_in":3600}`)
		case "/api/v1/media/recognize_file", "/api/v1/media/recognize":
			io.WriteString(w, `{"success":true,"data":{"media_info":{"media_type":"tv","title":"Example","year":"2024","tmdb_id":"123"},"meta_info":{"begin_season":0,"begin_episode":1,"season_episode":"S00E01"}}}`)
		case "/api/v1/transfer/name":
			io.WriteString(w, `{"success":true,"data":{"name":"MP2.S00E01.mkv"}}`)
		case "/3/tv/123":
			if e.tmdbFail {
				http.Error(w, "TMDB unavailable", 503)
				return
			}
			io.WriteString(w, `{"id":123,"name":"Example","original_name":"Example","first_air_date":"2024-01-01","origin_country":["US"],"seasons":[{"season_number":0,"episode_count":5,"air_date":"2024-01-01"}]}`)
		default:
			t.Errorf("unexpected provider read %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	e.cfg = &config.Config{MediaRecognition: config.MediaRecognitionConfig{Source: "shadow"}, MoviePilot: config.MoviePilotConfig{API: provider.URL, Username: "test", Password: "test"}, TMDB: config.TMDBConfig{Enabled: true, BaseURL: provider.URL + "/3", APIKey: "test-key", TimeoutSeconds: 2}}
	log := logger.New(config.LogConfig{Output: "stdout", Format: "json", Level: "error"})
	t.Cleanup(func() { _ = log.Close() })
	tmdb := service.NewTMDBService(e.cfg, log)
	e.local = service.NewMediaRecognitionService(db, tmdb, log)
	if _, err = e.local.SaveCategoryConfig("电影:\n  MP2电影:\n电视剧:\n  MP2剧集:\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.local.SaveMediaRenameConfig("OLD/{{title}}{{fileExt}}", "OLD/{{title}}/{{season_episode}}{{fileExt}}"); err != nil {
		t.Fatal(err)
	}
	cfg, err := e.local.LoadMedianameConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.CategoryYAML = "movie:\n  NEW电影:\ntv:\n  NEW剧集:\n"
	if _, err = e.local.SaveMedianameConfig(cfg); err != nil {
		t.Fatal(err)
	}
	mp := service.NewMoviePilotService(e.cfg, log)
	mp.SetLocalMediaRecognition(e.local)
	e.handler = NewService(log, mp, tmdb, &service.Download115Service{}, nil)
	client := driver.New()
	client.Client.SetTransport(organizePreviewTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/files":
			data := map[string]any{"state": true, "count": 1, "data": []any{map[string]any{"fid": "101", "cid": "100", "n": e.sourceName, "ico": "mkv", "s": "1000", "pc": "pick-101"}}}
			encoded, _ := json.Marshal(data)
			body = string(encoded)
		case r.Method == http.MethodGet && r.URL.Path == "/files/getid":
			body = `{"state":true,"id":"900"}`
		case r.Method == http.MethodPost && (strings.Contains(r.URL.Path, "rename") || strings.Contains(r.URL.Path, "move")):
			e.mutations++
			raw, _ := io.ReadAll(r.Body)
			e.mutationsBody = append(e.mutationsBody, string(raw))
			body = `{"state":true}`
		default:
			return nil, fmt.Errorf("unexpected 115 operation %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	e.handler.newOrganizeClient = func(string) (*driver.Pan115Client, error) { return client, nil }
	storage := model.CloudStorage{ID: 3, UserID: 1, StorageName: "test", StorageType: model.StorageType115Open, Cookie: "fake-cookie", Status: model.StatusActive}
	if err = db.Create(&storage).Error; err != nil {
		t.Fatal(err)
	}
	dir := model.CloudDirectory{ID: 2, UserID: 1, CloudStorageID: 3, DirectoryName: "影视", DirectoryID: "root", ClassifyByCategory: true, SavePath: filepath.Join(t.TempDir(), "library"), ContentPrefix: "https://example.test/strm"}
	if err = db.Create(&dir).Error; err != nil {
		t.Fatal(err)
	}
	e.req = Organize115CookieRequest{CloudDirectoryID: 2, FolderIDs: []string{"100"}, DryRun: true, MediaType: "tv"}
	return e
}
func TestOrganizeShadowPreviewAndConfirmedExecutionUseFrozenMP2Plan(t *testing.T) {
	e := setupOrganizePlanTest(t)
	preview, err := e.handler.buildOrganize115CookieResult(1, e.req)
	if err != nil {
		t.Fatal(err)
	}
	if err = organizePreviewError(preview); err != nil {
		t.Fatal(err)
	}
	if e.mutations != 0 || preview.PlanID == "" || preview.Pipeline == nil || preview.Pipeline.Source != "shadow" {
		t.Fatalf("preview: %+v mutations=%d", preview, e.mutations)
	}
	item := preview.Items[0]
	other := item.ShadowComparison.Medianame
	if item.Engine != "moviepilot" || !strings.Contains(item.TargetPath, "/MP2剧集/OLD/") || other == nil || !strings.Contains(other.TargetPath, "/NEW剧集/Example (2024) {tmdb-123}/Season 00/") {
		t.Fatalf("independent paths: %+v", item)
	}
	if len(item.ShadowComparison.Differences) < 3 || other.Medianame.Result.Status != "compatible" {
		t.Fatalf("full comparison missing: %+v", item.ShadowComparison)
	}
	t.Logf("preview MP2=%s; medianame=%s", item.TargetPath, other.TargetPath)
	if _, err = os.Stat(item.StrmPath); !os.IsNotExist(err) {
		t.Fatal("preview wrote STRM")
	}
	beforeCalls := e.calls
	e.cfg.MediaRecognition.Source = "medianame"
	cfg, _ := e.local.LoadMedianameConfig()
	cfg.TVTemplate = "CHANGED/{{.title}}.mkv"
	_, _ = e.local.SaveMedianameConfig(cfg)
	_, _ = e.local.SaveMediaRenameConfig("NEW/{{title}}.mkv", "NEW/{{title}}.mkv")
	req := e.req
	req.PlanID = preview.PlanID
	req.DryRun = false
	actual, err := e.handler.buildOrganize115CookieResult(1, req)
	if err != nil {
		t.Fatal(err)
	}
	if err = organizePreviewError(actual); err != nil {
		t.Fatal(err)
	}
	if actual.Items[0].TargetPath != item.TargetPath || actual.RecognitionSource != "shadow" || actual.Items[0].Engine != "moviepilot" || e.calls != beforeCalls {
		t.Fatalf("execution recomputed plan: %+v calls=%d/%d", actual, e.calls, beforeCalls)
	}
	data, err := os.ReadFile(item.StrmPath)
	if err != nil || string(data) != item.StrmContent || e.mutations != 2 {
		t.Fatalf("mock execution: %s %v mutations=%d", data, err, e.mutations)
	}
	if _, err = e.handler.buildOrganize115CookieResult(1, req); err == nil || e.mutations != 2 {
		t.Fatal("confirmed plan executed twice")
	}
}
func TestOrganizePlanRejectsStaleAndHistoricalPreviews(t *testing.T) {
	for _, change := range []string{"source", "root", "options", "foreign-user", "historical", "selection"} {
		t.Run(change, func(t *testing.T) {
			e := setupOrganizePlanTest(t)
			preview, err := e.handler.buildOrganize115CookieResult(1, e.req)
			if err != nil {
				t.Fatal(err)
			}
			req := e.req
			req.DryRun = false
			req.PlanID = preview.PlanID
			user := uint(1)
			switch change {
			case "source":
				e.sourceName = "Renamed.S00E01.mkv"
			case "root":
				e.db.Model(&model.CloudDirectory{}).Where("id = 2").Update("directory_name", "Changed")
			case "options":
				req.RecognitionSource = "medianame"
			case "foreign-user":
				user = 2
			case "historical":
				req.PlanID = ""
			case "selection":
				req.FileIDs = []string{"outside-plan"}
			}
			if _, err = e.handler.buildOrganize115CookieResult(user, req); err == nil {
				t.Fatal("invalid plan accepted")
			}
			if e.mutations != 0 {
				t.Fatal("rejected plan mutated 115")
			}
		})
	}
}
func TestOrganizeMedianameFailureKeepsMP2Plan(t *testing.T) {
	e := setupOrganizePlanTest(t)
	e.tmdbFail = true
	result, err := e.handler.buildOrganize115CookieResult(1, e.req)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Error != "" || item.TargetPath == "" || item.ShadowComparison.Medianame.Medianame.Result.Status != "request_error" || item.ShadowComparison.LocalTargetError == "" || e.mutations != 0 {
		t.Fatalf("shadow failure hid primary or diagnostic: %+v", item)
	}
}

func TestOrganizeBrokenMedianameConfigurationKeepsMP2AndDiagnostic(t *testing.T) {
	e := setupOrganizePlanTest(t)
	if err := e.db.Model(&model.SystemConfig{}).Where("config_key = ?", "media_recognition.medianame.v1").Update("config_value", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	result, err := e.handler.buildOrganize115CookieResult(1, e.req)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Error != "" || item.TargetPath == "" || result.Pipeline.MedianameConfigError == "" || item.ShadowComparison.LocalTargetError == "" {
		t.Fatalf("broken medianame config affected MP2: %+v", result)
	}
}
func TestDirectoryMedianameTrialOverridesSystemOnlyForNewTasks(t *testing.T) {
	e := setupOrganizePlanTest(t)
	e.db.Model(&model.CloudDirectory{}).Where("id = 2").Update("recognition_source", "medianame")
	result, err := e.handler.buildOrganize115CookieResult(1, e.req)
	if err != nil {
		t.Fatal(err)
	}
	if result.RecognitionSource != "medianame" || result.Items[0].Engine != "medianame" || strings.Contains(result.Items[0].TargetPath, "OLD") || e.cfg.MediaRecognition.Source != "shadow" {
		t.Fatalf("directory trial: %+v", result)
	}
}

func TestConfirmationEngineChangeUsesNewPlanAndPreservesOriginalPreview(t *testing.T) {
	e := setupOrganizePlanTest(t)
	original, err := e.handler.buildOrganize115CookieResult(1, e.req)
	if err != nil {
		t.Fatal(err)
	}
	// Choosing medianame in the confirmation dialog requests a separate preview
	// of exactly the selected files. The original shadow plan remains immutable.
	selected := e.req
	selected.RecognitionSource = "medianame"
	selected.FileIDs = []string{"101"}
	preview, err := e.handler.buildOrganize115CookieResult(1, selected)
	if err != nil || organizePreviewError(preview) != nil {
		t.Fatalf("selected engine preview failed: %+v %v", preview, err)
	}
	if e.mutations != 0 || preview.PlanID == original.PlanID || preview.Pipeline.Source != "medianame" || strings.Contains(preview.Items[0].TargetPath, "/OLD/") {
		t.Fatalf("engine selection executed or reused the MP2 plan: %+v", preview)
	}
	var originalRow model.OrganizePlan
	if err = e.db.First(&originalRow, "id = ?", original.PlanID).Error; err != nil || originalRow.Status != "ready" || originalRow.ResultJSON != planJSON(original) {
		t.Fatalf("engine selection changed the original preview: %+v %v", originalRow, err)
	}
	// System and template changes after this preview cannot change what is executed.
	e.cfg.MediaRecognition.Source = "moviepilot"
	settings, _ := e.local.LoadMedianameConfig()
	settings.TVTemplate = "CHANGED/{{.title}}{{.fileExt}}"
	if _, err = e.local.SaveMedianameConfig(settings); err != nil {
		t.Fatal(err)
	}
	beforeCalls := e.calls
	selected.PlanID = preview.PlanID
	selected.DryRun = false
	actual, err := e.handler.buildOrganize115CookieResult(1, selected)
	if err != nil || organizePreviewError(actual) != nil {
		t.Fatalf("confirmed selected engine failed: %+v %v", actual, err)
	}
	if actual.Items[0].Engine != "medianame" || actual.Items[0].TargetPath != preview.Items[0].TargetPath || e.calls != beforeCalls || e.mutations != 2 {
		t.Fatalf("execution changed engine or plan: %+v", actual)
	}
}

func TestPipelineWorkbenchUsesOrganizeBuildersWithoutCloudOperations(t *testing.T) {
	for _, mode := range []string{"file", "title"} {
		t.Run(mode, func(t *testing.T) {
			e := setupOrganizePlanTest(t)
			e.handler.newOrganizeClient = func(string) (*driver.Pan115Client, error) {
				t.Fatal("workbench must not create a cloud client")
				return nil, nil
			}
			input := "Example.2024.S00E01.[tmdb=123].mkv"
			if mode == "file" {
				input = "/Example.2024.[tmdb=123]/Season 0/01.mkv"
			}
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/media-recognition/pipeline-test", strings.NewReader(planJSON(map[string]any{
				"input": input, "mode": mode, "source": "shadow", "root": "影视", "classify": true,
			})))
			c.Request.Header.Set("Content-Type", "application/json")
			(&Handler{Service: e.handler}).TestMediaPipeline(c)
			var response struct {
				Code int `json:"code"`
				Data struct {
					Pipeline service.MediaPipelineSnapshot `json:"pipeline"`
					Item     Organize115ItemResult         `json:"item"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || response.Code != 0 || rr.Code != 200 {
				t.Fatalf("workbench response %s, %v", rr.Body, err)
			}
			item := response.Data.Item
			if response.Data.Pipeline.Source != "shadow" || item.Engine != "moviepilot" || item.Error != "" || item.ShadowComparison == nil || item.ShadowComparison.Medianame.Error != "" || !strings.Contains(item.ShadowComparison.Medianame.TargetPath, "/Season 00/") {
				t.Fatalf("incomplete workbench plan: %s", rr.Body)
			}
		})
	}
}

func TestAutomationPreviewRetainsFullPlanAndFailedMedianameEvidence(t *testing.T) {
	e := setupOrganizePlanTest(t)
	request := service.AutomationOrganizeRequest{UserID: 1, CloudDirectoryID: 2, FolderIDs: []string{"100"}, RecognitionSource: "shadow", DryRun: true}
	output, err := e.handler.OrganizeAutomation(context.Background(), request)
	if err != nil || output["plan_id"] == "" || output["pipeline"] == nil || output["dry_run"] != true || e.mutations != 0 {
		t.Fatalf("automation preview: %#v %v", output, err)
	}
	item := output["items"].([]any)[0].(map[string]any)
	if item["shadow_comparison"] == nil || item["engine"] != "moviepilot" {
		t.Fatalf("automation lost full shadow output: %#v", item)
	}
	e.tmdbFail = true
	// New configuration invalidates the shared success cache for this failing run.
	e.cfg.TMDB.CacheMinutes++
	request.RecognitionSource = "medianame"
	output, err = e.handler.OrganizeAutomation(context.Background(), request)
	if err == nil || output["plan_id"] == "" || e.mutations != 0 {
		t.Fatalf("failed preview executed or discarded: %#v %v", output, err)
	}
	item = output["items"].([]any)[0].(map[string]any)
	if item["medianame"].(map[string]any)["result"].(map[string]any)["status"] != "request_error" {
		t.Fatalf("failed preview lost status: %#v", item)
	}
}
