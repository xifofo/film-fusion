package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
)

func TestEmbyLibraryCheckAPIValidatesBeforeSearching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
	}))
	defer upstream.Close()
	client := embyhelper.New(&config.Config{Emby: config.EmbyConfig{URL: upstream.URL, APIKey: "test"}})
	defer client.Close()
	h := NewEmbyLibraryCheckHandler(service.NewEmbyLibraryCheckService(client))
	router := gin.New()
	router.POST("/api/emby-library-check", h.Check)
	request := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/emby-library-check", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	tooMany, _ := json.Marshal(map[string][]string{"names": strings.Split(strings.Repeat("Movie\n", 200)+"Last", "\n")})
	for _, body := range []string{`{}`, `null`, `{"names":null}`, `{"names":[]}`, `{"names":"Movie"}`, `{"names":[4]}`, `{"names":[" ","\n"]}`, `{"names":["Movie"]}{}`, string(tooMany), `{"names":["` + strings.Repeat("a", 1025) + `"]}`} {
		if w := request(body); w.Code != http.StatusBadRequest {
			t.Errorf("invalid request accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if w := request(`{"names":["` + strings.Repeat("a", embyLibraryCheckMaxBodyBytes) + `"]}`); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("body limit: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input queried Emby %d times", calls.Load())
	}
	w := request(`{"names":["Movie.mkv","Movie.srt","Another"]}`)
	var response struct {
		Code int                              `json:"code"`
		Data service.EmbyLibraryCheckResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK || response.Code != 0 || response.Data.Total != 2 || response.Data.DuplicatesRemoved != 1 || calls.Load() != 2 {
		t.Fatalf("response contract: %d %s (%v)", w.Code, w.Body.String(), err)
	}
}

func TestEmbyLibraryCheckAcceptsMaximumRows(t *testing.T) {
	names := make([]string, service.EmbyLibraryCheckMaxNames)
	for i := range names {
		names[i] = fmt.Sprintf("Movie %d", i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err := service.NewEmbyLibraryCheckService(nil).Check(ctx, names)
	if err != nil || response.Total != service.EmbyLibraryCheckMaxNames {
		t.Fatalf("maximum rows rejected: %+v %v", response, err)
	}
}
