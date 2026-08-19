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
	router.POST("/test", handler.Test)

	response := performMediaRecognitionRequest(t, router, http.MethodPut, "/words", `{"words":["S04 => S01"]}`)
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
