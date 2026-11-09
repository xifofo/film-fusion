package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"

	"github.com/gin-gonic/gin"
)

func TestAppConfigUpdatePayloadDetectsBoundAccountWait(t *testing.T) {
	for _, input := range []struct {
		json    string
		present bool
	}{
		{`{"config":{"emby":{}}}`, false},
		{`{"config":{"emby":{"bound_account_wait_seconds":0}}}`, true},
		{`{"config":{"emby":{"bound_account_wait_seconds":15}}}`, true},
	} {
		var payload appConfigUpdatePayload
		if err := json.Unmarshal([]byte(input.json), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.boundAccountWaitFieldPresent != input.present {
			t.Fatalf("incorrect field presence for %s", input.json)
		}
	}
}

func TestAppConfigRejectsInvalidBoundAccountWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewAppConfigHandler(nil, &config.Config{}, nil, nil)
	router := gin.New()
	router.PUT("/api/app-config", handler.Update)
	for _, seconds := range []int{-1, 0, 21} {
		body := fmt.Sprintf(`{"config":{"emby":{"bound_account_wait_seconds":%d}}}`, seconds)
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/app-config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "指定账号等待超时") {
			t.Fatalf("invalid timeout %d: status=%d body=%s", seconds, recorder.Code, recorder.Body.String())
		}
	}
}
