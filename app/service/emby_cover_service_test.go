package service

import (
	"errors"
	"path/filepath"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/utils/cover"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newEmbyCoverTestService(t *testing.T) *EmbyCoverService {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "emby-cover.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.EmbyCoverLibrary{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return &EmbyCoverService{db: db}
}

func TestEmbyCoverRecordSuccessCreatesDefaultConfigRow(t *testing.T) {
	svc := newEmbyCoverTestService(t)
	local := model.EmbyCoverLibrary{
		EmbyLibraryID: "3",
		EmbyName:      "华语电影",
		CNTitle:       "华语电影",
		TemplateID:    cover.DefaultTemplateID,
		Enabled:       true,
	}

	if err := svc.recordSuccess(local); err != nil {
		t.Fatalf("record success: %v", err)
	}

	var saved model.EmbyCoverLibrary
	if err := svc.db.Where("emby_library_id = ?", local.EmbyLibraryID).First(&saved).Error; err != nil {
		t.Fatalf("load saved status: %v", err)
	}
	if saved.LastGeneratedAt == nil {
		t.Fatal("last_generated_at was not saved")
	}
	if saved.LastError != "" {
		t.Fatalf("last_error = %q, want empty", saved.LastError)
	}
	if saved.EmbyName != local.EmbyName || saved.TemplateID != local.TemplateID || !saved.Enabled {
		t.Fatalf("unexpected default config row: %+v", saved)
	}
}

func TestEmbyCoverRunStateUpdatesPreserveExistingConfig(t *testing.T) {
	svc := newEmbyCoverTestService(t)
	existing := model.EmbyCoverLibrary{
		EmbyLibraryID: "427",
		EmbyName:      "动画番剧",
		CNTitle:       "自定义主标题",
		ENSubtitle:    "ANIME",
		TemplateID:    "custom-template",
		Enabled:       true,
		LastError:     "旧错误",
	}
	if err := svc.db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing config: %v", err)
	}

	fallback := model.EmbyCoverLibrary{
		EmbyLibraryID: existing.EmbyLibraryID,
		EmbyName:      "不应覆盖的名称",
		CNTitle:       "不应覆盖的标题",
		TemplateID:    cover.DefaultTemplateID,
		Enabled:       true,
	}
	if err := svc.recordSuccess(fallback); err != nil {
		t.Fatalf("record success: %v", err)
	}

	var afterSuccess model.EmbyCoverLibrary
	if err := svc.db.Where("emby_library_id = ?", existing.EmbyLibraryID).First(&afterSuccess).Error; err != nil {
		t.Fatalf("load success state: %v", err)
	}
	if afterSuccess.LastGeneratedAt == nil || afterSuccess.LastError != "" {
		t.Fatalf("unexpected success state: generated=%v error=%q", afterSuccess.LastGeneratedAt, afterSuccess.LastError)
	}
	if afterSuccess.EmbyName != existing.EmbyName || afterSuccess.CNTitle != existing.CNTitle || afterSuccess.ENSubtitle != existing.ENSubtitle || afterSuccess.TemplateID != existing.TemplateID {
		t.Fatalf("existing config was overwritten: %+v", afterSuccess)
	}

	lastGeneratedAt := *afterSuccess.LastGeneratedAt
	runErr := errors.New("上传失败")
	if err := svc.recordFailure(fallback, runErr); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	var afterFailure model.EmbyCoverLibrary
	if err := svc.db.Where("emby_library_id = ?", existing.EmbyLibraryID).First(&afterFailure).Error; err != nil {
		t.Fatalf("load failure state: %v", err)
	}
	if afterFailure.LastGeneratedAt == nil || !afterFailure.LastGeneratedAt.Equal(lastGeneratedAt) {
		t.Fatalf("last successful generation changed: got=%v want=%v", afterFailure.LastGeneratedAt, lastGeneratedAt)
	}
	if afterFailure.LastError != runErr.Error() {
		t.Fatalf("last_error = %q, want %q", afterFailure.LastError, runErr.Error())
	}
	if afterFailure.CNTitle != existing.CNTitle || afterFailure.TemplateID != existing.TemplateID {
		t.Fatalf("existing config was overwritten after failure: %+v", afterFailure)
	}
}

func TestEmbyCoverPreviewFailureDoesNotPersistRunState(t *testing.T) {
	svc := newEmbyCoverTestService(t)
	local := model.EmbyCoverLibrary{
		EmbyLibraryID: "5110",
		EmbyName:      "日韩剧集",
		Enabled:       true,
	}
	runErr := errors.New("预览失败")

	got := svc.recordGenerationFailure(local, GenerateOptions{Upload: false}, runErr)
	if !errors.Is(got, runErr) {
		t.Fatalf("returned error = %v, want %v", got, runErr)
	}

	var count int64
	if err := svc.db.Model(&model.EmbyCoverLibrary{}).Count(&count).Error; err != nil {
		t.Fatalf("count status rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("preview created %d status rows, want 0", count)
	}
}
