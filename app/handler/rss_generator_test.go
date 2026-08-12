package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newRSSGeneratorHandlerTest(t *testing.T, workerURL string) (*RSSGeneratorHandler, *service.RSSGeneratorService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RSSGeneratorFeedDefinition{}, &model.RSSGeneratorFeedAccessToken{}, &model.RSSGeneratorFeedCache{}); err != nil {
		t.Fatal(err)
	}
	generator, err := service.NewRSSGeneratorService(db, nil, config.RSSGeneratorConfig{
		WorkerURL: workerURL, WorkerToken: "internal", SecretKeyFile: filepath.Join(t.TempDir(), "rss.key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewRSSGeneratorHandler(generator), generator
}

func createRSSGeneratorHandlerFeedAndToken(t *testing.T, generator *service.RSSGeneratorService, rate int) (service.RSSGeneratorFeedView, service.RSSGeneratorTokenResult) {
	t.Helper()
	enabled := true
	feed, err := generator.CreateFeed(service.RSSGeneratorFeedInput{
		Name: "公开 Feed", Slug: "public-feed", RouteKind: model.RSSGeneratorKindHTTPJSON,
		SourceURLTemplate: "https://source.example/{{params.category}}", Method: http.MethodGet,
		Parameters: []service.RSSGeneratorParameterDefinition{{Name: "category", Type: "string", Required: true}},
		Mapping:    map[string]any{"items": "data", "fields": map[string]any{"title": "title"}},
		Enabled:    &enabled, ItemLimit: 100, CacheTTLSeconds: 60, StaleTTLSeconds: 120,
		Headers: map[string]string{"X-Secret": "source-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := generator.CreateToken(feed.ID, service.RSSGeneratorTokenInput{Name: "reader", RateLimitPerMinute: rate})
	if err != nil {
		t.Fatal(err)
	}
	return feed, token
}

func TestRSSGeneratorPublicHandlerFormatsConditionalAndHiddenErrors(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"feed":{"title":"Public Feed","link":"https://source.example","items":[{"title":"Item","guid":"item-1"}]}}`))
	}))
	defer worker.Close()
	handler, generator := newRSSGeneratorHandlerTest(t, worker.URL)
	_, token := createRSSGeneratorHandlerFeedAndToken(t, generator, 20)
	router := gin.New()
	router.GET("/rss/s/:token", handler.PublicFeed)

	request := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}
	rss := request("/rss/s/"+token.Token+".xml?category=movie", nil)
	if rss.Code != http.StatusOK || !strings.HasPrefix(rss.Header().Get("Content-Type"), "application/rss+xml") || rss.Header().Get("Cache-Control") != "private, max-age=0, must-revalidate" {
		t.Fatalf("rss status/headers/body=%d %+v %s", rss.Code, rss.Header(), rss.Body.String())
	}
	atom := request("/rss/s/"+token.Token+".atom?category=movie", nil)
	if atom.Code != http.StatusOK || !strings.HasPrefix(atom.Header().Get("Content-Type"), "application/atom+xml") {
		t.Fatalf("atom status=%d headers=%+v body=%s", atom.Code, atom.Header(), atom.Body.String())
	}
	conditional := request("/rss/s/"+token.Token+".xml?category=movie", map[string]string{"If-None-Match": rss.Header().Get("ETag")})
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%s", conditional.Code, conditional.Body.String())
	}
	invalid := request("/rss/s/ffrss_invalid.xml?category=movie", nil)
	if invalid.Code != http.StatusNotFound || strings.Contains(invalid.Body.String(), "ffrss_invalid") || strings.Contains(invalid.Body.String(), "source-secret") {
		t.Fatalf("invalid response leaked detail: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	multiple := request("/rss/s/"+token.Token+".xml?category=movie&category=tv", nil)
	if multiple.Code != http.StatusBadRequest {
		t.Fatalf("multi parameter status=%d body=%s", multiple.Code, multiple.Body.String())
	}
	if err := generator.RevokeToken(token.Record.FeedID, token.Record.ID); err != nil {
		t.Fatal(err)
	}
	revoked := request("/rss/s/"+token.Token+".xml?category=movie", nil)
	if revoked.Code != http.StatusNotFound || revoked.Body.Len() != 0 {
		t.Fatalf("revoked status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestRSSGeneratorPublicHandlerRateLimit(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"feed":{"title":"Rate Feed","link":"https://source.example","items":[{"title":"Item"}]}}`))
	}))
	defer worker.Close()
	handler, generator := newRSSGeneratorHandlerTest(t, worker.URL)
	_, token := createRSSGeneratorHandlerFeedAndToken(t, generator, 1)
	router := gin.New()
	router.GET("/rss/s/:token", handler.PublicFeed)
	path := "/rss/s/" + token.Token + ".xml?category=movie"
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, path, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, path, nil))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "60" || second.Body.Len() != 0 {
		t.Fatalf("rate status=%d headers=%+v body=%s", second.Code, second.Header(), second.Body.String())
	}
}

func TestRSSGeneratorManagementTokenResponseNeverIncludesHash(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {}))
	defer worker.Close()
	handler, generator := newRSSGeneratorHandlerTest(t, worker.URL)
	feed, token := createRSSGeneratorHandlerFeedAndToken(t, generator, 5)
	router := gin.New()
	router.GET("/api/rss-generator/feeds/:id/tokens", handler.ListTokens)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/rss-generator/feeds/"+fmt.Sprint(feed.ID)+"/tokens", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), token.Token) || strings.Contains(recorder.Body.String(), "token_hash") {
		t.Fatalf("token list leaked secret/hash: %s", recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
}
