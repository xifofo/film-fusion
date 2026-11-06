package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/store/embyproxylog"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"resty.dev/v3"
)

func TestEmbyProxyBindingFailuresNeverReachOtherStorageOrUpstreamStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, token, path, want                                  string
		disabled, noMatch, failDownload, failItems, failIdentity bool
	}{
		{name: "missing user token", want: "必须携带有效的用户 Token"},
		{name: "admin key", token: "admin-key", want: "不能使用管理员 API Key"},
		{name: "invalid token", token: "invalid", want: "Token 无效或已失效"},
		{name: "identity unavailable", token: "user-token", failIdentity: true, want: "Token 无效或已失效"},
		{name: "media lookup failure", token: "user-token", failItems: true, want: "获取 Emby 媒体信息失败"},
		{name: "external URL", token: "user-token", path: "https://external.invalid/video?secret=hidden", want: "媒体路径是外部直链"},
		{name: "unmatched media", token: "user-token", noMatch: true, want: "未匹配 Match302"},
		{name: "disabled bound storage", token: "user-token", disabled: true, want: "已停用"},
		{name: "bound download failure", token: "user-token", failDownload: true, want: "获取绑定账号直链失败"},
		{name: "ready bound storage", token: "user-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupEmbyBindingProxyDB(t)
			source := model.CloudStorage{StorageType: model.StorageType115Open, StorageName: "永V", Status: model.StatusActive, AccessToken: "source-token", Match302AccessMode: model.Match302AccessModeOpenAPIOnly}
			target := model.CloudStorage{StorageType: model.StorageType115Open, StorageName: "小号", Status: model.StatusActive, AccessToken: "bound-token", Match302AccessMode: model.Match302AccessModeOpenAPIOnly}
			if tc.disabled {
				target.Status = model.StatusDisabled
			}
			for _, storage := range []*model.CloudStorage{&source, &target} {
				if err := db.Create(storage).Error; err != nil {
					t.Fatal(err)
				}
			}
			binding := model.EmbyAccountBinding{EmbyUserID: "whitecola-id", EmbyUserName: "whitecola", CloudStorageID: target.ID, Enabled: true}
			if err := db.Create(&binding).Error; err != nil {
				t.Fatal(err)
			}
			match := model.Match302{SourcePath: "/media", TargetPath: "/library", CloudStorageID: source.ID, BalanceEnabled: true}
			if !tc.noMatch {
				if err := db.Create(&match).Error; err != nil {
					t.Fatal(err)
				}
			}
			assignment := model.Match302BalanceAssignment{Match302ID: match.ID, SourceFilePath: "/media/Test.mkv", SourceStorageID: source.ID, PlaybackStorageID: target.ID, Forced: true, Status: model.BalanceAssignmentStatusReady, SourcePickcode: "source-pick", TargetPickcode: "bound-pick", SHA1: "ABC", Size: 123}
			if !tc.noMatch {
				if err := db.Create(&assignment).Error; err != nil {
					t.Fatal(err)
				}
			}
			var streams, sourceDownloads, boundDownloads atomic.Int32
			emby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/Users/Me":
					if r.Header.Get("X-Emby-Token") != "user-token" || tc.failIdentity {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					_, _ = io.WriteString(w, `{"Id":"whitecola-id","Name":"whitecola"}`)
				case "/Items":
					if tc.failItems {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					path := tc.path
					if path == "" {
						path = "/media/Test.mkv"
					}
					_, _ = fmt.Fprintf(w, `{"Items":[{"Id":"1","Path":%q}]}`, path)
				default:
					streams.Add(1)
					_, _ = io.WriteString(w, "upstream playback must not be requested")
				}
			}))
			defer emby.Close()
			// 所有 115 调用都在内存模拟；默认 Transport 只允许本地 Emby 与源文件元数据读取。
			original := http.DefaultTransport
			http.DefaultTransport = scoped115Transport(func(r *http.Request) (*http.Response, error) {
				if strings.HasPrefix(r.URL.String(), emby.URL) {
					return original.RoundTrip(r)
				}
				if r.URL.Host != "proapi.115.com" || r.URL.Path != "/open/folder/get_info" {
					return nil, fmt.Errorf("unexpected network call: %s", r.URL.Host+r.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"state":true,"data":{"file_id":"source-file","file_name":"Test.mkv","pick_code":"source-pick","sha1":"ABC","size_byte":123}}`)), Request: r}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = original })
			client := resty.NewWithClient(&http.Client{Transport: scoped115Transport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer bound-token" {
					sourceDownloads.Add(1)
					return nil, errors.New("source storage must never be used")
				}
				boundDownloads.Add(1)
				if tc.failDownload {
					return nil, errors.New("绑定账号授权失效 https://request.invalid/?access_token=secret-token")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"state":true,"data":{"item":{"url":{"url":"https://bound.invalid/video"}}}}`)), Request: r}, nil
			})})
			defer client.Close()
			cfg := &config.Config{Emby: config.EmbyConfig{URL: emby.URL, APIKey: "admin-key"}}
			log := logger.New(config.LogConfig{Output: "stdout", Level: "error"})
			defer log.Close()
			h := NewEmbyProxyHandler(cfg, log, service.NewEmbyLoginProtection(cfg, log))
			h.sdk115Open = sdk115.New(sdk115.WithRestyClient(client))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/emby/Videos/1/stream.mkv", nil)
			if tc.token != "" {
				c.Request.Header.Set("X-Emby-Token", tc.token)
			}
			embyproxylog.Default().Clear()
			h.ProxyRequest(c)
			if tc.want == "" {
				if w.Code != http.StatusFound || w.Header().Get("Location") != "https://bound.invalid/video" || boundDownloads.Load() != 1 {
					t.Fatalf("bound redirect: %d %s", w.Code, w.Body.String())
				}
			} else {
				if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), tc.want) || w.Header().Get("Location") != "" {
					t.Fatalf("failure: %d %s", w.Code, w.Body.String())
				}
				entries := embyproxylog.Default().Snapshot(1)
				if len(entries) != 1 || entries[0].BalanceStatus != "绑定账号播放失败" || !strings.Contains(entries[0].FallbackReason, tc.want) || entries[0].ActualStorageID != 0 {
					t.Fatalf("missing failure reason: %+v", entries)
				}
				if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(entries[0].FallbackReason, "secret-token") {
					t.Fatal("upstream URL credentials leaked in the failure reason")
				}
			}
			if streams.Load() != 0 || sourceDownloads.Load() != 0 {
				t.Fatalf("binding bypassed: streams=%d sourceDownloads=%d", streams.Load(), sourceDownloads.Load())
			}
		})
	}
}

func TestResolveEmbyBindingAllowsOnlyConfirmedUnboundUsers(t *testing.T) {
	for _, kind := range []string{"no bindings", "disabled binding", "confirmed unbound user"} {
		t.Run(kind, func(t *testing.T) {
			db := setupEmbyBindingProxyDB(t)
			if kind != "no bindings" {
				b := model.EmbyAccountBinding{EmbyUserID: "bound-user", CloudStorageID: 1, Enabled: true}
				if err := db.Create(&b).Error; err != nil {
					t.Fatal(err)
				}
				if kind == "disabled binding" {
					if err := db.Model(&b).Update("enabled", false).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			var lookups atomic.Int32
			emby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lookups.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"Id":"unbound-user"}`)
			}))
			defer emby.Close()
			log := logger.New(config.LogConfig{Output: "stdout", Level: "error"})
			defer log.Close()
			h := NewEmbyProxyHandler(&config.Config{Emby: config.EmbyConfig{URL: emby.URL}}, log, nil)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/Videos/1/stream", nil)
			if kind == "confirmed unbound user" {
				c.Request.Header.Set("X-Emby-Token", "valid-unbound-token")
			}
			id, err := h.resolveBoundStorageID(c)
			if err != nil || id != 0 {
				t.Fatalf("unbound user rejected: id=%d err=%v", id, err)
			}
			wantLookups := int32(0)
			if kind == "confirmed unbound user" {
				wantLookups = 1
			}
			if lookups.Load() != wantLookups {
				t.Fatalf("user lookups=%d, want %d", lookups.Load(), wantLookups)
			}
		})
	}
}

func setupEmbyBindingProxyDB(t *testing.T) *gorm.DB {
	t.Helper()
	old := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "bindings.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudStorage{}, &model.EmbyAccountBinding{}, &model.Match302{}, &model.Match302BalanceMember{}, &model.Match302BalanceAssignment{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old; sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return db
}
