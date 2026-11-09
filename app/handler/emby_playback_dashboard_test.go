package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"
	"github.com/gin-gonic/gin"
)

func TestPlaybackDashboardHTTPContract(t *testing.T) {
	for _, failing := range []bool{false, true} {
		t.Run(fmt.Sprint(failing), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failing {
					w.WriteHeader(403)
					fmt.Fprint(w, "secret-upstream-body")
					return
				}
				if r.URL.Path == "/Sessions" {
					fmt.Fprint(w, `[]`)
				} else {
					fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
				}
			}))
			defer upstream.Close()
			cfg := &config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "private-test-key"}}
			client := embyhelper.New(cfg)
			defer client.Close()
			h := NewEmbyWatchHandler(nil, service.NewEmbyWatchService(cfg, nil, client))
			for _, action := range []struct {
				name   string
				handle gin.HandlerFunc
			}{{"now-playing", h.NowPlaying}, {"playback-history", h.PlaybackHistory}} {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest("GET", "/api/emby-watch/"+action.name, nil)
				action.handle(c)
				var response struct {
					Code int             `json:"code"`
					Data json.RawMessage `json:"data"`
				}
				if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || recorder.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("invalid JSON/cache contract")
				}
				if failing {
					if recorder.Code != 502 || response.Code != 502 || strings.Contains(recorder.Body.String(), "secret-") || strings.Contains(recorder.Body.String(), "private-test-key") || string(response.Data) != "null" {
						t.Fatalf("unsafe failure: %s", recorder.Body.String())
					}
				} else if recorder.Code != 200 || response.Code != 0 || string(response.Data) == "null" {
					t.Fatalf("valid empty result lost: %s", recorder.Body.String())
				}
			}
		})
	}
}
