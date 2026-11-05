// 字体上传回归验证真实解析、持久化边界、配置隔离及无效输入拒绝。
package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/auth"
	"film-fusion/app/config"
	"film-fusion/app/utils/cover"

	"github.com/gin-gonic/gin"
	"golang.org/x/image/font/gofont/goregular"
)

// fontUploadRequest 使用内存中的字体字节构造上传，不依赖开发机的字体目录。
func fontUploadRequest(t *testing.T, h *AppConfigHandler, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.POST("/api/site-assets/fonts", h.UploadFont)
	request := httptest.NewRequest(http.MethodPost, "/api/site-assets/fonts", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	return response
}

func TestFontUploadPersistsReadableFontWithoutChangingConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Emby.Cover.FontCN = "existing-cn.otf"
	cfg.Emby.Cover.FontEN = "existing-en.ttf"
	h := NewAppConfigHandler(nil, cfg, nil, nil)
	h.fontAssetsDir = t.TempDir()
	var saved string

	// Windows 风格路径也只能留下文件名；重复上传相同内容复用同一路径。
	for range 2 {
		response := fontUploadRequest(t, h, `..\..\MyFont.ttf`, goregular.TTF)
		if response.Code != http.StatusOK {
			t.Fatalf("upload: %d %s", response.Code, response.Body.String())
		}

		var result struct {
			Code int              `json:"code"`
			Data fontUploadResult `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		path := filepath.FromSlash(result.Data.Path)
		relative, err := filepath.Rel(h.fontAssetsDir, path)
		if err != nil || strings.HasPrefix(relative, "..") || filepath.Base(path) != "MyFont.ttf" {
			t.Fatalf("font escaped upload directory: %q (%v)", path, err)
		}
		if result.Code != 0 || result.Data.Size != len(goregular.TTF) || (saved != "" && saved != path) {
			t.Fatalf("unexpected response: %+v", result)
		}
		if _, err := cover.LoadSfntFont(path); err != nil {
			t.Fatalf("renderer cannot load uploaded font: %v", err)
		}
		stored, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(stored, goregular.TTF) {
			t.Fatalf("stored font differs: %v", err)
		}

		saved = path
	}

	if cfg.Emby.Cover.FontCN != "existing-cn.otf" || cfg.Emby.Cover.FontEN != "existing-en.ttf" {
		t.Fatal("upload must not enable the font or change settings")
	}
	if scope := auth.APITokenScopeForRoute("/api/site-assets/fonts"); scope != "" {
		t.Fatalf("font upload must remain limited to admin sessions, got token scope %q", scope)
	}
}

func TestFontUploadRejectsInvalidFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		data   []byte
		status int
	}{
		{"not-a-font.ttf", []byte("not a font"), http.StatusBadRequest},
		{"font.woff", goregular.TTF, http.StatusBadRequest},
		{"empty.otf", nil, http.StatusBadRequest},
		{"huge.ttf", make([]byte, maxFontBytes+1), http.StatusRequestEntityTooLarge},
	}

	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			h := NewAppConfigHandler(nil, &config.Config{}, nil, nil)
			h.fontAssetsDir = t.TempDir()
			response := fontUploadRequest(t, h, item.name, item.data)
			if response.Code != item.status {
				t.Fatalf("got %d, want %d: %s", response.Code, item.status, response.Body.String())
			}
			entries, err := os.ReadDir(h.fontAssetsDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid upload left files: %v %v", entries, err)
			}
		})
	}
}

func TestFontUploadReportsStorageFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAppConfigHandler(nil, &config.Config{}, nil, nil)
	h.fontAssetsDir = filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(h.fontAssetsDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	response := fontUploadRequest(t, h, "font.ttf", goregular.TTF)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want storage failure: %s", response.Code, response.Body.String())
	}
}
