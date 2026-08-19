package handler

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupUserProfileTest(t *testing.T) (*AuthHandler, *gorm.DB, model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "profile.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate user: %v", err)
	}

	originalDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = originalDB })

	user := model.User{
		Username: "admin",
		Password: "hashed-password",
		Email:    "admin@example.com",
		IsActive: true,
		IsAdmin:  true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	handler := NewAuthHandler(&config.Config{}, nil)
	handler.avatarAssetsDir = t.TempDir()
	return handler, db, user
}

func authenticatedProfileRouter(handler *AuthHandler, userID uint) *gin.Engine {
	router := gin.New()
	authenticated := router.Group("/")
	authenticated.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Next()
	})
	authenticated.PUT("/api/me", handler.UpdateMe)
	authenticated.POST("/api/me/avatar", handler.UploadAvatar)
	router.GET("/api/public-assets/avatar/:filename", handler.GetAvatar)
	return router
}

func TestUpdateMeTrimsAndPersistsNickname(t *testing.T) {
	handler, db, user := setupUserProfileTest(t)
	router := authenticatedProfileRouter(handler, user.ID)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/me",
		strings.NewReader(`{"nickname":"  影迷小林  "}`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Code int        `json:"code"`
		Data model.User `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.Nickname != "影迷小林" {
		t.Fatalf("unexpected response: %+v", response)
	}

	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("read stored user: %v", err)
	}
	if stored.Nickname != "影迷小林" {
		t.Fatalf("stored nickname = %q", stored.Nickname)
	}
}

func TestUpdateMeRejectsInvalidNickname(t *testing.T) {
	handler, _, user := setupUserProfileTest(t)
	router := authenticatedProfileRouter(handler, user.ID)

	for _, body := range []string{
		`{"nickname":"   "}`,
		`{"nickname":"line\nbreak"}`,
		`{"nickname":"123456789012345678901234567890123"}`,
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/me", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}
}

func TestUploadAvatarPersistsAndServesImage(t *testing.T) {
	handler, db, user := setupUserProfileTest(t)
	router := authenticatedProfileRouter(handler, user.ID)

	imageBuffer := new(bytes.Buffer)
	source := image.NewRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 120, A: 255})
		}
	}
	if err := png.Encode(imageBuffer, source); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "avatar.png")
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write(imageBuffer.Bytes()); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	uploadRecorder := httptest.NewRecorder()
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/me/avatar", body)
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(uploadRecorder, uploadRequest)
	if uploadRecorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", uploadRecorder.Code, uploadRecorder.Body.String())
	}

	var response struct {
		Code int                    `json:"code"`
		Data userAvatarUploadResult `json:"data"`
	}
	if err := json.Unmarshal(uploadRecorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.Width != 24 || response.Data.Height != 24 {
		t.Fatalf("unexpected upload response: %+v", response)
	}
	if !strings.HasPrefix(response.Data.User.Avatar, userAvatarURLPrefix) {
		t.Fatalf("unexpected avatar URL: %q", response.Data.User.Avatar)
	}

	filename := strings.TrimPrefix(response.Data.User.Avatar, userAvatarURLPrefix)
	if _, err := os.Stat(filepath.Join(handler.avatarAssetsDir, filename)); err != nil {
		t.Fatalf("uploaded avatar not found: %v", err)
	}

	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("read stored user: %v", err)
	}
	if stored.Avatar != response.Data.User.Avatar {
		t.Fatalf("stored avatar = %q, response = %q", stored.Avatar, response.Data.User.Avatar)
	}

	readRecorder := httptest.NewRecorder()
	readRequest := httptest.NewRequest(http.MethodGet, response.Data.User.Avatar, nil)
	router.ServeHTTP(readRecorder, readRequest)
	if readRecorder.Code != http.StatusOK {
		t.Fatalf("read avatar status = %d", readRecorder.Code)
	}
	if readRecorder.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("avatar content type = %q", readRecorder.Header().Get("Content-Type"))
	}
	if readRecorder.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("avatar cache control = %q", readRecorder.Header().Get("Cache-Control"))
	}
}

func TestUploadAvatarRejectsInvalidFile(t *testing.T) {
	handler, _, user := setupUserProfileTest(t)
	router := authenticatedProfileRouter(handler, user.ID)

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "avatar.svg")
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write([]byte(`<svg><script>alert(1)</script></svg>`)); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/me/avatar", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid avatar status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}
