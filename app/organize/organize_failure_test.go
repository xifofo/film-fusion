package organize

import (
	"film-fusion/app/database"
	"film-fusion/app/model"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func organizeFailureTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "organize.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.OrganizeLog{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = previous
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func waitForOrganizeFailureLog(t *testing.T, db *gorm.DB) model.OrganizeLog {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var rows []model.OrganizeLog
		if err := db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			return rows[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("manual organize failure was not persisted")
	return model.OrganizeLog{}
}

func TestOrganize115CookieRequestFailureIsLogged(t *testing.T) {
	db := organizeFailureTestDB(t)
	gin.SetMode(gin.TestMode)
	h := &Service{}
	router := gin.New()
	router.POST("/api/organize/115-cookie", func(c *gin.Context) {
		c.Set("user_id", uint(1))
		(&Handler{Service: h}).Organize115Cookie(c)
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/organize/115-cookie", strings.NewReader(`{"cloud_directory_id":2,"dry_run":false}`))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "缺少已确认的整理计划；历史预览不兼容，请重新预览") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
	row := waitForOrganizeFailureLog(t, db)
	if row.Action != model.OrganizeActionMediaOrganize || row.Status != model.OrganizeStatusFailed || row.Trigger != model.OrganizeTriggerManual || row.Error != "缺少已确认的整理计划；历史预览不兼容，请重新预览" {
		t.Fatalf("unexpected failure log: %+v", row)
	}
}

func TestOrganize115CookieFolderFailureIncludesSourceContext(t *testing.T) {
	db := organizeFailureTestDB(t)
	h := &Service{}
	req := Organize115CookieRequest{
		CloudDirectoryID: 2,
		FolderIDs:        []string{"100"},
		FolderContexts: []Organize115FolderContext{{
			FolderID: "100", FolderName: "Show S03", FolderPath: "云下载 / Show S03",
		}},
	}
	h.logOrganize115CookieFailure(1, req, 3, "100", "移动文件失败: 115 Cookie 已失效")
	row := waitForOrganizeFailureLog(t, db)
	if row.Source != "云下载 / Show S03" || row.CloudStorageID != 3 || row.Error != "移动文件失败: 115 Cookie 已失效" || !strings.Contains(row.Message, "100") {
		t.Fatalf("failure log lost source or error: %+v", row)
	}
	// Preview errors stay in preview results and do not become real execution logs.
	req.DryRun = true
	h.logOrganize115CookieFailure(1, req, 3, "100", "预整理失败")
	var count int64
	if err := db.Model(&model.OrganizeLog{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("preview wrote a real organize log: count=%d err=%v", count, err)
	}
}
