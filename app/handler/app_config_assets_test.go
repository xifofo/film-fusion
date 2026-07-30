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

	"github.com/gin-gonic/gin"
)

func TestLoginBackgroundUploadAndRead(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewAppConfigHandler(nil, &config.Config{}, nil, nil)
	handler.siteAssetsDir = t.TempDir()
	router := gin.New()
	router.POST("/api/site-assets/login-background", handler.UploadLoginBackground)
	router.GET("/api/public-assets/login-background/:filename", handler.GetLoginBackground)

	imageBuffer := new(bytes.Buffer)
	source := image.NewRGBA(image.Rect(0, 0, 16, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 16; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 20), B: 96, A: 255})
		}
	}
	if err := png.Encode(imageBuffer, source); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "login.png")
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
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/site-assets/login-background", body)
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(uploadRecorder, uploadRequest)
	if uploadRecorder.Code != http.StatusOK {
		t.Fatalf("expected upload status 200, got %d: %s", uploadRecorder.Code, uploadRecorder.Body.String())
	}

	var uploadResponse struct {
		Code int                         `json:"code"`
		Data loginBackgroundUploadResult `json:"data"`
	}
	if err := json.Unmarshal(uploadRecorder.Body.Bytes(), &uploadResponse); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if uploadResponse.Code != 0 {
		t.Fatalf("expected response code 0, got %d", uploadResponse.Code)
	}
	if uploadResponse.Data.Width != 16 || uploadResponse.Data.Height != 9 {
		t.Fatalf("unexpected image dimensions: %+v", uploadResponse.Data)
	}
	if !strings.HasPrefix(uploadResponse.Data.URL, loginBackgroundURLPrefix) {
		t.Fatalf("unexpected public URL: %s", uploadResponse.Data.URL)
	}

	filename := strings.TrimPrefix(uploadResponse.Data.URL, loginBackgroundURLPrefix)
	if _, err := os.Stat(filepath.Join(handler.siteAssetsDir, filename)); err != nil {
		t.Fatalf("expected uploaded file: %v", err)
	}

	readRecorder := httptest.NewRecorder()
	readRequest := httptest.NewRequest(http.MethodGet, uploadResponse.Data.URL, nil)
	router.ServeHTTP(readRecorder, readRequest)
	if readRecorder.Code != http.StatusOK {
		t.Fatalf("expected image status 200, got %d", readRecorder.Code)
	}
	if readRecorder.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("unexpected cache control: %q", readRecorder.Header().Get("Cache-Control"))
	}
	if contentType := readRecorder.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("unexpected content type: %q", contentType)
	}
}

func TestLoginBackgroundUploadRejectsInvalidFile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewAppConfigHandler(nil, &config.Config{}, nil, nil)
	handler.siteAssetsDir = t.TempDir()
	router := gin.New()
	router.POST("/api/site-assets/login-background", handler.UploadLoginBackground)

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "not-an-image.svg")
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
	request := httptest.NewRequest(http.MethodPost, "/api/site-assets/login-background", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestLoginBackgroundReadRejectsUnknownFilename(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewAppConfigHandler(nil, &config.Config{}, nil, nil)
	handler.siteAssetsDir = t.TempDir()
	router := gin.New()
	router.GET("/api/public-assets/login-background/:filename", handler.GetLoginBackground)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/public-assets/login-background/config.yaml",
		nil,
	)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", recorder.Code)
	}
}
