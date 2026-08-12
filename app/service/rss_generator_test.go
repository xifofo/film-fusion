package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newRSSGeneratorTestService(t *testing.T, workerURL string) (*RSSGeneratorService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.RSSGeneratorFeedDefinition{}, &model.RSSGeneratorFeedAccessToken{}, &model.RSSGeneratorFeedCache{},
	); err != nil {
		t.Fatal(err)
	}
	service, err := NewRSSGeneratorService(db, nil, config.RSSGeneratorConfig{
		WorkerURL: workerURL, WorkerToken: "worker-secret", SecretKeyFile: filepath.Join(t.TempDir(), "rss.key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, db
}

func rssGeneratorBool(value bool) *bool       { return &value }
func rssGeneratorString(value string) *string { return &value }

func validRSSGeneratorTestInput(sourceURL string) RSSGeneratorFeedInput {
	return RSSGeneratorFeedInput{
		Name: "示例 Feed", Slug: "example-feed", RouteKind: model.RSSGeneratorKindHTTPJSON,
		SourceURLTemplate: sourceURL + "/items/{{params.category}}", Method: http.MethodGet,
		Parameters: []RSSGeneratorParameterDefinition{{Name: "category", Label: "分类", Type: "string", Required: true, Pattern: `^[a-z]+$`}},
		Mapping:    map[string]any{"items": "data", "fields": map[string]any{"title": "title", "link": "link", "date": "date"}},
		Enabled:    rssGeneratorBool(true), ItemLimit: 100, CacheTTLSeconds: 60, StaleTTLSeconds: 120,
	}
}

func TestRSSGeneratorSecretsAreEncryptedAndViewsAreFullyRedacted(t *testing.T) {
	service, db := newRSSGeneratorTestService(t, "http://127.0.0.1:8787")
	input := validRSSGeneratorTestInput("https://source.example")
	input.Method = http.MethodPost
	input.RequestBodyTemplate = rssGeneratorString(`{"category":{{json.params.category}},"password":"body-secret"}`)
	input.Headers = map[string]string{"Authorization": "Bearer header-secret", "X-Custom-Credential": "custom-secret"}
	input.Cookie = rssGeneratorString("session=cookie-secret")
	input.ProxyURL = rssGeneratorString("http://proxy-user:proxy-pass@127.0.0.1:8080")
	input.SecretQueryParams = map[string]string{"api_key": "query-secret"}
	input.BrowserStorageState = json.RawMessage(`{"cookies":[{"name":"sid","value":"state-secret","domain":"source.example","path":"/"}]}`)

	view, err := service.CreateFeed(input)
	if err != nil {
		t.Fatal(err)
	}
	if view.RequestBodyTemplate != RSSGeneratorSecretMask || view.Cookie != RSSGeneratorSecretMask || view.ProxyURL != RSSGeneratorSecretMask || view.BrowserStorageState != RSSGeneratorSecretMask {
		t.Fatalf("secret view was not redacted: %+v", view)
	}
	for key, value := range view.Headers {
		if value != RSSGeneratorSecretMask {
			t.Fatalf("header %s was not redacted: %q", key, value)
		}
	}
	if view.SecretQueryParams["api_key"] != RSSGeneratorSecretMask {
		t.Fatalf("secret query was not redacted: %+v", view.SecretQueryParams)
	}

	var stored model.RSSGeneratorFeedDefinition
	if err := db.First(&stored, view.ID).Error; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(stored)
	serializedColumns := strings.Join([]string{
		stored.HeadersCiphertext, stored.CookieCiphertext, stored.ProxyURLCiphertext,
		stored.SecretQueryCiphertext, stored.BrowserStateCiphertext, stored.RequestBodyCiphertext, string(encoded),
	}, " ")
	for _, secret := range []string{"header-secret", "custom-secret", "cookie-secret", "proxy-pass", "query-secret", "state-secret", "body-secret"} {
		if strings.Contains(serializedColumns, secret) {
			t.Fatalf("database record leaked %q", secret)
		}
	}
	secrets, err := service.decryptSecrets(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.Headers["X-Custom-Credential"] != "custom-secret" || secrets.SecretQueryParams["api_key"] != "query-secret" || !strings.Contains(secrets.RequestBodyTemplate, "body-secret") {
		t.Fatalf("decrypted secrets did not round trip: %+v", secrets)
	}
	info, err := os.Stat(service.cfg.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions=%o want 600", info.Mode().Perm())
	}
}

func TestRSSGeneratorParametersUseArrayAndValidateBothTemplateForms(t *testing.T) {
	service, _ := newRSSGeneratorTestService(t, "http://127.0.0.1:8787")
	input := validRSSGeneratorTestInput("https://source.example")
	input.Method = http.MethodPost
	input.RequestBodyTemplate = rssGeneratorString(`{"category":{{json.params.category}}}`)
	view, err := service.CreateFeed(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Parameters) != 1 || view.Parameters[0].Name != "category" || view.Parameters[0].Label != "分类" {
		t.Fatalf("unexpected parameter response: %+v", view.Parameters)
	}
	duplicate := input
	duplicate.Slug = "duplicate-feed"
	duplicate.Parameters = append(duplicate.Parameters, duplicate.Parameters[0])
	if _, err := service.CreateFeed(duplicate); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("expected duplicate parameter rejection, got %v", err)
	}
	badURLTemplate := input
	badURLTemplate.Slug = "bad-url-template"
	badURLTemplate.SourceURLTemplate = "https://source.example/{{json.params.category}}"
	if _, err := service.CreateFeed(badURLTemplate); err == nil || !strings.Contains(err.Error(), "{{params.name}}") {
		t.Fatalf("URL should reject JSON placeholder: %v", err)
	}
}

func TestRSSGeneratorUpdatePreservesMaskedOrOmittedRequestBody(t *testing.T) {
	service, _ := newRSSGeneratorTestService(t, "http://127.0.0.1:8787")
	input := validRSSGeneratorTestInput("https://source.example")
	input.Method = http.MethodPost
	input.RequestBodyTemplate = rssGeneratorString(`{"secret":"keep-me","category":{{json.params.category}}}`)
	created, err := service.CreateFeed(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Name = "更新后"
	input.RequestBodyTemplate = nil
	updated, err := service.UpdateFeed(created.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequestBodyTemplate != RSSGeneratorSecretMask {
		t.Fatalf("omitted request body was not preserved: %+v", updated)
	}
	record, _ := service.loadFeed(created.ID)
	secrets, _ := service.decryptSecrets(&record)
	if !strings.Contains(secrets.RequestBodyTemplate, "keep-me") {
		t.Fatalf("request body was lost: %q", secrets.RequestBodyTemplate)
	}
}

func TestRSSGeneratorEditPreviewResolvesMaskedSecretsFromFeedID(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload struct {
			Headers map[string]string `json:"headers"`
			Cookie  string            `json:"cookie"`
			Source  struct {
				BodyTemplate string `json:"body_template"`
			} `json:"source"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Headers["Authorization"] != "Bearer saved-secret" || payload.Cookie != "sid=saved-cookie" || !strings.Contains(payload.Source.BodyTemplate, "saved-body") {
			t.Fatalf("masked edit preview did not resolve saved secrets: %+v", payload)
		}
		_, _ = response.Write([]byte(`{"feed":{"title":"Edit Preview","link":"https://source.example","items":[{"title":"Item"}]}}`))
	}))
	defer worker.Close()
	service, _ := newRSSGeneratorTestService(t, worker.URL)
	input := validRSSGeneratorTestInput("https://source.example")
	input.Method = http.MethodPost
	input.RequestBodyTemplate = rssGeneratorString(`{"secret":"saved-body","category":{{json.params.category}}}`)
	input.Headers = map[string]string{"Authorization": "Bearer saved-secret"}
	input.Cookie = rssGeneratorString("sid=saved-cookie")
	created, err := service.CreateFeed(input)
	if err != nil {
		t.Fatal(err)
	}
	maskedBody := RSSGeneratorSecretMask
	maskedCookie := RSSGeneratorSecretMask
	input.Name = "尚未保存的新标题"
	input.RequestBodyTemplate = &maskedBody
	input.Headers = map[string]string{"Authorization": RSSGeneratorSecretMask}
	input.Cookie = &maskedCookie
	feed, err := service.PreviewDefinitionForFeed(context.Background(), created.ID, input, map[string]any{"category": "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if feed.Title != "Edit Preview" {
		t.Fatalf("unexpected edit preview: %+v", feed)
	}
}

func TestRSSGeneratorWorkerBearerPayloadAndNormalization(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path == "/health" {
			_, _ = response.Write([]byte(`{"status":"ok","service":"rss-generator-worker","version":"test","auth_configured":true}`))
			return
		}
		if request.Header.Get("Authorization") != "Bearer worker-secret" {
			http.Error(response, "bad auth", http.StatusUnauthorized)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		source := payload["source"].(map[string]any)
		if !strings.Contains(source["url_template"].(string), "api_key=query-secret") {
			t.Fatalf("missing encrypted query injection: %+v", source)
		}
		proxy := payload["proxy"].(map[string]any)
		if proxy["allow_private"] != true || payload["max_items"].(float64) != 1 {
			t.Fatalf("unexpected worker options: %+v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"feed":{"title":"示例 Feed","link":"https://source.example/final?api_key=query-secret","items":[{"title":"第一项","link":"https://source.example/1?api_key=query-secret","description":"query-secret must not leak","date":"2026-08-12T10:00:00+08:00","guid":"guid-1"},{"title":"第二项"}]}}`))
	}))
	defer worker.Close()

	service, _ := newRSSGeneratorTestService(t, worker.URL)
	input := validRSSGeneratorTestInput("https://source.example")
	input.ItemLimit = 1
	input.SecretQueryParams = map[string]string{"api_key": "query-secret"}
	input.ProxyURL = rssGeneratorString("http://127.0.0.1:8080")
	input.ProxyAllowPrivate = true
	feed, err := service.PreviewDefinition(context.Background(), input, map[string]any{"category": "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 1 || feed.Items[0].ID != "guid-1" || feed.Items[0].PublishedAt == nil {
		t.Fatalf("unexpected normalized feed: %+v", feed)
	}
	encodedFeed, _ := json.Marshal(feed)
	if strings.Contains(string(encodedFeed), "query-secret") || strings.Contains(feed.Link, "api_key") || strings.Contains(feed.Items[0].Link, "api_key") {
		t.Fatalf("normalized feed leaked source secret: %s", encodedFeed)
	}
	if calls.Load() != 1 {
		t.Fatalf("worker calls=%d want 1", calls.Load())
	}
}

func TestRSSGeneratorTokenLifecycleNeverStoresCleartext(t *testing.T) {
	service, db := newRSSGeneratorTestService(t, "http://127.0.0.1:8787")
	feed, err := service.CreateFeed(validRSSGeneratorTestInput("https://source.example"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateToken(feed.ID, RSSGeneratorTokenInput{Name: "阅读器", RateLimitPerMinute: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "ffrss_") || created.RSSURL != "/rss/s/"+created.Token+".xml" {
		t.Fatalf("unexpected token result: %+v", created)
	}
	var stored model.RSSGeneratorFeedAccessToken
	if err := db.First(&stored, created.Record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == created.Token || stored.TokenHash != hashRSSGeneratorToken(created.Token) {
		t.Fatalf("clear token was stored or hash mismatch: %+v", stored)
	}
	list, err := service.ListTokens(feed.ID)
	if err != nil || len(list) != 1 || list[0].Status != "active" {
		t.Fatalf("unexpected token list: %+v err=%v", list, err)
	}
	past := time.Now().Add(-time.Minute)
	if err := db.Model(&stored).Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RotateToken(feed.ID, stored.ID); !errors.Is(err, ErrRSSGeneratorTokenHidden) {
		t.Fatalf("expired token should not rotate: %v", err)
	}
}

func TestRSSGeneratorRSSAndAtomRenderStrictXMLWithDownloadURIs(t *testing.T) {
	published := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
	feed := RSSGeneratorFeed{
		Title: "影视 & 动漫", Link: "https://example.com", Description: "更新 <列表>", Author: "作者",
		Items: []RSSGeneratorItem{{
			ID: "guid-1", Title: "第一集 & 特别篇", Link: "magnet:?xt=urn:btih:abcdef",
			Content: "<p>正文 ]]> 后续</p>", PublishedAt: &published,
			Enclosures: []RSSGeneratorEnclosure{{URL: "ed2k://|file|movie.mkv|123|ABC|/", Type: "application/octet-stream", Length: 123}},
		}},
	}
	for _, format := range []string{"rss", "atom"} {
		rendered, err := RenderRSSGeneratorFeed(feed, format, published)
		if err != nil {
			t.Fatalf("render %s: %v", format, err)
		}
		decoder := xml.NewDecoder(strings.NewReader(string(rendered.Body)))
		for {
			if _, err := decoder.Token(); err != nil {
				if err.Error() == "EOF" {
					break
				}
				t.Fatalf("invalid %s XML: %v\n%s", format, err, rendered.Body)
			}
		}
		if !strings.Contains(string(rendered.Body), "magnet:") || rendered.ETag == "" || rendered.LastModified != published {
			t.Fatalf("unexpected %s render metadata/body", format)
		}
	}
}

func TestRSSGeneratorCacheKeySeparatesFeedVersionFormatAndAuth(t *testing.T) {
	base := model.RSSGeneratorFeedDefinition{ID: 1, Version: 1}
	keys := []string{
		buildRSSGeneratorCacheKey(base, `{"q":"a"}`, "rss", "token:1:a"),
		buildRSSGeneratorCacheKey(model.RSSGeneratorFeedDefinition{ID: 2, Version: 1}, `{"q":"a"}`, "rss", "token:1:a"),
		buildRSSGeneratorCacheKey(model.RSSGeneratorFeedDefinition{ID: 1, Version: 2}, `{"q":"a"}`, "rss", "token:1:a"),
		buildRSSGeneratorCacheKey(base, `{"q":"a"}`, "atom", "token:1:a"),
		buildRSSGeneratorCacheKey(base, `{"q":"a"}`, "rss", "token:2:b"),
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("cache key collision: %v", keys)
		}
		seen[key] = true
	}
}

func TestRSSGeneratorCacheSeparatesTokensAndHitsWithinToken(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(response, `{"feed":{"title":"Cache Feed","link":"https://source.example","items":[{"title":"Item","link":"https://source.example/item","guid":"item-1"}]}}`)
	}))
	defer worker.Close()
	service, db := newRSSGeneratorTestService(t, worker.URL)
	input := validRSSGeneratorTestInput("https://source.example")
	feed, err := service.CreateFeed(input)
	if err != nil {
		t.Fatal(err)
	}
	firstToken, _ := service.CreateToken(feed.ID, RSSGeneratorTokenInput{Name: "一"})
	secondToken, _ := service.CreateToken(feed.ID, RSSGeneratorTokenInput{Name: "二"})
	firstAccess, _ := service.ResolvePublicToken(context.Background(), firstToken.Token)
	secondAccess, _ := service.ResolvePublicToken(context.Background(), secondToken.Token)
	params := map[string]any{"category": "movie"}
	first, err := service.RenderPublic(context.Background(), firstAccess, "rss", params)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RenderPublic(context.Background(), secondAccess, "rss", params)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.RenderPublic(context.Background(), firstAccess, "rss", params)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheStatus != "miss" || second.CacheStatus != "miss" || again.CacheStatus != "hit" || calls.Load() != 2 {
		t.Fatalf("unexpected cache statuses/calls: %s %s %s %d", first.CacheStatus, second.CacheStatus, again.CacheStatus, calls.Load())
	}
	var cacheCount int64
	if err := db.Model(&model.RSSGeneratorFeedCache{}).Count(&cacheCount).Error; err != nil || cacheCount != 2 {
		t.Fatalf("cache rows=%d err=%v", cacheCount, err)
	}
}

func TestRSSGeneratorCacheSingleflightCoalescesConcurrentMisses(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		_, _ = response.Write([]byte(`{"feed":{"title":"Singleflight","link":"https://source.example","items":[{"title":"Item"}]}}`))
	}))
	defer worker.Close()
	service, _ := newRSSGeneratorTestService(t, worker.URL)
	feed, err := service.CreateFeed(validRSSGeneratorTestInput("https://source.example"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateToken(feed.ID, RSSGeneratorTokenInput{Name: "并发", RateLimitPerMinute: 100})
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.ResolvePublicToken(context.Background(), token.Token)
	if err != nil {
		t.Fatal(err)
	}
	const concurrency = 12
	errorsByCall := make(chan error, concurrency)
	var waitGroup sync.WaitGroup
	for range concurrency {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, renderErr := service.RenderPublic(context.Background(), access, "rss", map[string]any{"category": "movie"})
			errorsByCall <- renderErr
		}()
	}
	waitGroup.Wait()
	close(errorsByCall)
	for renderErr := range errorsByCall {
		if renderErr != nil {
			t.Fatal(renderErr)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("worker calls=%d want 1", calls.Load())
	}
}
