package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type fixedSystemInfoCollector struct {
	value service.SystemInfo
}

func (f fixedSystemInfoCollector) Collect(context.Context) service.SystemInfo {
	return f.value
}

func TestSystemInfoReturnsRuntimeSnapshotWithoutCaching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collectedAt := time.Date(2026, time.August, 19, 12, 34, 56, 0, time.UTC)
	handler := NewSystemInfoHandler(fixedSystemInfoCollector{value: service.SystemInfo{
		CollectedAt: collectedAt,
		Host: service.SystemHostInfo{
			Hostname:      "film-fusion-host",
			UptimeSeconds: 86400,
		},
		CPU: service.SystemCPUInfo{
			UsagePercent:  37.5,
			LogicalCores:  8,
			PhysicalCores: 4,
		},
		Memory: service.SystemMemoryInfo{
			Total:        16 * 1024 * 1024 * 1024,
			Used:         8 * 1024 * 1024 * 1024,
			UsagePercent: 50,
		},
	}})
	router := gin.New()
	router.GET("/api/system-info", handler.Get)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/system-info", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	if recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", recorder.Header().Get("Pragma"))
	}

	var response struct {
		Code int                `json:"code"`
		Data service.SystemInfo `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.Host.Hostname != "film-fusion-host" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Data.CPU.UsagePercent != 37.5 || response.Data.Memory.UsagePercent != 50 {
		t.Fatalf("resource usage missing from response: %+v", response.Data)
	}
	if !response.Data.CollectedAt.Equal(collectedAt) {
		t.Fatalf("collected_at = %v, want %v", response.Data.CollectedAt, collectedAt)
	}
}
