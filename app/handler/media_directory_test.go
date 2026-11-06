package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func mediaDirectoryFixture(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old; pool.Close() })
	if err := db.AutoMigrate(&model.MediaDirectory{}, &model.SystemConfig{}, &model.MediaUpgradeRuleGroup{}, &model.EmbyVersionCheckSetting{}); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []uint{1, 2} {
		if err := db.Create(&model.CloudStorage{ID: owner, UserID: owner, StorageType: model.StorageType115Open, StorageName: fmt.Sprint(owner)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := NewMediaDirectoryHandler()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", uint(1)) })
	router.POST("/media-directories", h.Save)
	router.PUT("/media-directories/:id", h.Save)
	router.DELETE("/media-directories/:id", h.Delete)
	router.GET("/media-directories", h.List)
	router.GET("/media-directories/:id", h.Get)
	return db, router
}

func directoryRequest(router *gin.Engine, method, url, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestMediaDirectoryUnifiedEditPreservesOtherCapability(t *testing.T) {
	db, router := mediaDirectoryFixture(t)
	input := `{"cloud_storage_id":1,"directory_name":"Mapped","mapping_enabled":true,"organize_enabled":false,"source_path":"/CD2/Movies","source_type":"clouddrive2","save_path":"/strm","content_prefix":"https://play.example","content_encode_uri":true,"filter_rules":"{\"include\":[\"mkv\"],\"download\":[\"srt\"]}","emby_path_prefix":"/emby","strm_content_type":"openlist"}`
	res := directoryRequest(router, "POST", "/media-directories", input)
	if res.Code != 200 {
		t.Fatalf("create %d: %s", res.Code, res.Body)
	}
	var envelope struct{ Data model.MediaDirectory }
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	id := envelope.Data.ID
	res = directoryRequest(router, "PUT", fmt.Sprintf("/media-directories/%d", id), `{"directory_name":"Renamed","organize_enabled":true,"directory_id":"123"}`)
	if res.Code != 200 {
		t.Fatalf("update %d: %s", res.Code, res.Body)
	}
	var row model.MediaDirectory
	if err := db.First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	if !row.MappingEnabled || !row.OrganizeEnabled || row.SourcePath != "/CD2/Movies" || row.SavePath != "/strm" || row.ContentPrefix != "https://play.example" || !row.ContentEncodeURI || row.FilterRules != envelope.Data.FilterRules || row.EmbyPathPrefix != "/emby" {
		t.Fatalf("partial edit lost configuration: %+v", row)
	}
	var count int64
	db.Model(&model.MediaDirectory{}).Count(&count)
	if count != 1 {
		t.Fatal("second capability created another record")
	}
	for _, body := range []string{`{"id":99}`, `{"user_id":2}`, `{"save_path":null}`, `{"directory_id":"","source_path":""}`, `{"cloud_storage_id":2}`} {
		res = directoryRequest(router, "PUT", fmt.Sprintf("/media-directories/%d", id), body)
		if res.Code != 400 {
			t.Fatalf("unsafe patch %s accepted: %d", body, res.Code)
		}
	}
	// An explicit empty value clears the field, while omitted fields above remain unchanged.
	res = directoryRequest(router, "PUT", fmt.Sprintf("/media-directories/%d", id), `{"content_prefix":"","emby_path_prefix":""}`)
	if res.Code != 200 {
		t.Fatalf("clear rejected: %s", res.Body)
	}
	db.First(&row, id)
	if row.ContentPrefix != "" || row.EmbyPathPrefix != "" || row.FilterRules != envelope.Data.FilterRules {
		t.Fatal("clear affected unrelated data")
	}
}

func TestMediaDirectoryCapabilityFiltersAndOwnership(t *testing.T) {
	db, router := mediaDirectoryFixture(t)
	for _, row := range []model.MediaDirectory{
		{ID: 10, UserID: 1, CloudStorageID: 1, DirectoryName: "mapping", MappingEnabled: true},
		{ID: 20, UserID: 1, CloudStorageID: 1, DirectoryName: "organize", OrganizeEnabled: true},
		{ID: 30, UserID: 2, CloudStorageID: 2, DirectoryName: "private", MappingEnabled: true, OrganizeEnabled: true},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for capability, want := range map[string]uint{"mapping": 10, "organize": 20} {
		res := directoryRequest(router, "GET", "/media-directories?capability="+capability, "")
		var envelope struct {
			Data struct {
				List  []model.MediaDirectory
				Total int
			}
		}
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if res.Code != 200 || envelope.Data.Total != 1 || len(envelope.Data.List) != 1 || envelope.Data.List[0].ID != want {
			t.Fatalf("bad filter: %s", res.Body)
		}
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		res := directoryRequest(router, method, "/media-directories/30", `{"directory_name":"stolen"}`)
		if res.Code != 404 {
			t.Fatalf("cross-owner %s: %d", method, res.Code)
		}
	}
}

func TestMediaDirectoryCannotRemovePlutoFishAssociation(t *testing.T) {
	db, router := mediaDirectoryFixture(t)
	row := model.MediaDirectory{ID: 8, UserID: 1, CloudStorageID: 1, DirectoryName: "used", MappingEnabled: true, OrganizeEnabled: true, DirectoryID: "123", SourcePath: "/Movies", SourceType: model.SourceTypeMoviePilot2, LinkType: model.LinkTypeStrm}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SystemConfig{ConfigKey: "plutofish.settings", ConfigValue: `{"userID":1,"config":{"mappings":[{"cloudPathID":8}]}}`}).Error; err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		res := directoryRequest(router, method, "/media-directories/8", `{"source_path":""}`)
		if res.Code != 409 {
			t.Fatalf("association removed via %s: %d %s", method, res.Code, res.Body)
		}
	}
	var count int64
	db.Model(&model.MediaDirectory{}).Where("id = ?", 8).Count(&count)
	if count != 1 {
		t.Fatal("used directory deleted")
	}
}
