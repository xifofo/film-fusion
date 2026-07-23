package middleware

import (
	"film-fusion/app/config"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCloudDrive2WebhookAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg := &config.Config{Webhook: config.WebhookConfig{
		CloudDrive2: config.CloudDrive2WebhookConfig{Enabled: true, Token: token},
	}}
	router := gin.New()
	router.Use(CloudDrive2WebhookAuth(cfg))
	router.POST("/hook", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic " + token, want: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "valid", header: "Bearer " + token, want: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/hook", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if res.Code != tt.want {
				t.Fatalf("status = %d, want %d", res.Code, tt.want)
			}
		})
	}

	// 配置指针热更新后无需重建路由；关闭鉴权时仍应正常接收请求。
	cfg.Webhook.CloudDrive2.Enabled = false
	req := httptest.NewRequest(http.MethodPost, "/hook", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("authentication disabled status = %d, want %d", res.Code, http.StatusNoContent)
	}
}
