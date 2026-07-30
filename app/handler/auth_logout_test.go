package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAuthHandlerLogoutIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authHandler := &AuthHandler{}
	router := gin.New()
	router.POST("/api/auth/logout", authHandler.Logout)

	for _, authorization := range []string{"", "Bearer expired-token"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("authorization %q: expected status 200, got %d", authorization, recorder.Code)
		}

		var response struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("authorization %q: decode response: %v", authorization, err)
		}
		if response.Code != 0 || response.Message != "退出登录成功" {
			t.Fatalf("authorization %q: unexpected response: %+v", authorization, response)
		}
	}
}
