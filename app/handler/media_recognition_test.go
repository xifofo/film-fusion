package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMediaRecognitionHandlerSavesWordsAndTestsUnsavedRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-"))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	handler := NewMediaRecognitionHandler(service.NewMediaRecognitionService(db, nil, nil))
	router := gin.New()
	router.GET("/words", handler.GetWords)
	router.PUT("/words", handler.UpdateWords)
	router.GET("/category-config", handler.GetCategoryConfig)
	router.POST("/category-config/validate", handler.ValidateCategoryConfig)
	router.PUT("/category-config", handler.UpdateCategoryConfig)
	router.GET("/rename-config", handler.GetRenameConfig)
	router.PUT("/rename-config", handler.UpdateRenameConfig)
	router.POST("/rename-config/validate", handler.ValidateRenameTemplate)
	router.POST("/rename-config/preview", handler.PreviewRenameTemplate)
	router.POST("/test", handler.Test)

	response := performMediaRecognitionRequest(t, router, http.MethodGet, "/rename-config", "")
	if response.Code != 0 || response.Data["configured"] != false || response.Data["active"] != false {
		t.Fatalf("initial rename config=%+v", response)
	}
	commonVariables, _ := response.Data["common_variables"].([]any)
	tvVariables, _ := response.Data["tv_variables"].([]any)
	if len(commonVariables) != 32 || len(tvVariables) != 7 {
		t.Fatalf("rename variables common=%d tv=%d", len(commonVariables), len(tvVariables))
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPost, "/rename-config/preview", `{
		"media_type":"movie","template":"{{title}}{% if year %} ({{year}}){% endif %}/{{title}}{{fileExt}}",
		"sample":{"title":"沙丘","year":2021,"fileExt":".mkv"}
	}`)
	if response.Code != 0 || response.Data["path"] != "沙丘 (2021)/沙丘.mkv" {
		t.Fatalf("rename preview=%+v", response)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPost, "/rename-config/validate", `{
		"media_type":"movie","template":"../{{title}}{{fileExt}}"
	}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsafe rename validation=%+v", response)
	}
	errors, _ := response.Data["errors"].([]any)
	if len(errors) == 0 {
		t.Fatalf("unsafe rename validation omitted errors: %+v", response)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPut, "/rename-config", `{
		"movie_format":"电影/{{title}}{{fileExt}}",
		"tv_format":"../{{title}}{{fileExt}}"
	}`)
	if response.Code != http.StatusBadRequest || response.Data["media_type"] != "tv" {
		t.Fatalf("invalid save rename config=%+v", response)
	}
	validation, _ := response.Data["validation"].(map[string]any)
	if validation["valid"] != false {
		t.Fatalf("invalid save validation=%#v", validation)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPut, "/rename-config", `{
		"movie_format":"电影/{{title}}{{fileExt}}",
		"tv_format":"剧集/{{title}}/{{season_episode}}{{fileExt}}"
	}`)
	if response.Code != 0 || response.Data["configured"] != true || response.Data["active"] != true {
		t.Fatalf("save rename config=%+v", response)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPut, "/words", `{"words":["S04 => S01"]}`)
	if response.Code != 0 || response.Data["configured"] != true {
		t.Fatalf("save response=%+v", response)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPost, "/test", `{
		"input":"Example.S01E03.mkv","mode":"file","lookup_tmdb":false,
		"words":["S01 => S02","S02E <> \\.mkv >> EP+1"]
	}`)
	if response.Code != 0 {
		t.Fatalf("test response=%+v", response)
	}
	meta, _ := response.Data["meta_info"].(map[string]any)
	if meta["season_episode"] != "S02E04" {
		t.Fatalf("meta=%#v", meta)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodGet, "/words", "")
	words, _ := response.Data["words"].([]any)
	if len(words) != 1 || words[0] != "S04 => S01" {
		t.Fatalf("stored words=%#v", words)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPost, "/category-config/validate", `{
		"yaml":"movie:\n  电影:\ntv:\n  本地剧集:\n"
	}`)
	if response.Code != 0 {
		t.Fatalf("validate category response=%+v", response)
	}
	response = performMediaRecognitionRequest(t, router, http.MethodPut, "/category-config", `{
		"yaml":"movie:\n  电影:\ntv:\n  已保存剧集:\n"
	}`)
	if response.Code != 0 || response.Data["configured"] != true {
		t.Fatalf("save category response=%+v", response)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodPost, "/test", `{
		"input":"Example.S01E03.mkv","mode":"file","lookup_tmdb":false,
		"category_yaml":"movie:\n  临时电影:\ntv:\n  临时剧集:\n"
	}`)
	if response.Code != 0 {
		t.Fatalf("test category response=%+v", response)
	}
	media, _ := response.Data["media_info"].(map[string]any)
	if media["category"] != "临时剧集" {
		t.Fatalf("media=%#v", media)
	}

	response = performMediaRecognitionRequest(t, router, http.MethodGet, "/category-config", "")
	tv, _ := response.Data["tv"].([]any)
	if len(tv) != 1 || tv[0].(map[string]any)["name"] != "已保存剧集" {
		t.Fatalf("stored category=%#v", response.Data)
	}
}

type mediaRecognitionHandlerResponse struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

func performMediaRecognitionRequest(t *testing.T, router http.Handler, method, path, body string) mediaRecognitionHandlerResponse {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var response mediaRecognitionHandlerResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response status=%d body=%s: %v", recorder.Code, recorder.Body.String(), err)
	}
	return response
}
