package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestApplicationRouterDoesNotLogPublicFeedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalWriter := gin.DefaultWriter
	t.Cleanup(func() { gin.DefaultWriter = originalWriter })

	var output bytes.Buffer
	gin.DefaultWriter = &output
	router := newApplicationRouter()
	router.GET("/rss/s/:token", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/health-test", func(c *gin.Context) { c.Status(http.StatusOK) })

	secretPath := "/rss/s/ffrss_super-secret.xml?user=alice"
	request := httptest.NewRequest(http.MethodGet, secretPath, nil)
	router.ServeHTTP(httptest.NewRecorder(), request)
	if strings.Contains(output.String(), "ffrss_super-secret") || strings.Contains(output.String(), "user=alice") {
		t.Fatalf("public feed credential leaked to access log: %s", output.String())
	}

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health-test", nil))
	if !strings.Contains(output.String(), "/health-test") {
		t.Fatalf("ordinary request was not logged: %s", output.String())
	}
}
