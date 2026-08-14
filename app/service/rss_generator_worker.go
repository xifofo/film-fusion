package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"
)

type RSSGeneratorWorkerStatus struct {
	Available      bool   `json:"available"`
	Status         string `json:"status"`
	Service        string `json:"service,omitempty"`
	Version        string `json:"version,omitempty"`
	AuthConfigured bool   `json:"auth_configured,omitempty"`
	Error          string `json:"error,omitempty"`
}

type rssGeneratorWorkerRequest struct {
	Feed            rssGeneratorWorkerFeedMetadata `json:"feed"`
	Kind            string                         `json:"kind"`
	Source          rssGeneratorWorkerSource       `json:"source"`
	Params          map[string]any                 `json:"params,omitempty"`
	Headers         map[string]string              `json:"headers,omitempty"`
	Cookie          string                         `json:"cookie,omitempty"`
	Proxy           any                            `json:"proxy,omitempty"`
	Selectors       map[string]any                 `json:"selectors,omitempty"`
	Mapping         map[string]any                 `json:"mapping,omitempty"`
	BrowserFallback bool                           `json:"browser_fallback,omitempty"`
	StorageState    json.RawMessage                `json:"storage_state,omitempty"`
	WaitUntil       string                         `json:"wait_until,omitempty"`
	WaitForSelector string                         `json:"wait_for_selector,omitempty"`
	RenderDelayMS   int                            `json:"render_delay_ms,omitempty"`
	MaxItems        int                            `json:"max_items,omitempty"`
}

type rssGeneratorWorkerFeedMetadata struct {
	Title       string `json:"title"`
	Link        string `json:"link,omitempty"`
	Description string `json:"description,omitempty"`
	Language    string `json:"language,omitempty"`
	Author      string `json:"author,omitempty"`
	Image       string `json:"image,omitempty"`
}

type rssGeneratorWorkerSource struct {
	URLTemplate  string `json:"url_template"`
	Method       string `json:"method,omitempty"`
	BodyTemplate string `json:"body_template,omitempty"`
}

type rssGeneratorWorkerResponse struct {
	Feed struct {
		Title       string `json:"title"`
		Link        string `json:"link"`
		Description string `json:"description"`
		Language    string `json:"language"`
		Author      string `json:"author"`
		Image       string `json:"image"`
		UpdatedAt   string `json:"updated_at"`
		Items       []struct {
			Title       string   `json:"title"`
			Link        string   `json:"link"`
			Description string   `json:"description"`
			Content     string   `json:"content"`
			Date        string   `json:"date"`
			Author      string   `json:"author"`
			Categories  []string `json:"categories"`
			GUID        string   `json:"guid"`
			Enclosures  []struct {
				URL    string `json:"url"`
				Type   string `json:"type"`
				Length int64  `json:"length"`
				Title  string `json:"title"`
			} `json:"enclosures"`
		} `json:"items"`
	} `json:"feed"`
}

func (s *RSSGeneratorService) WorkerHealth(ctx context.Context) RSSGeneratorWorkerStatus {
	status := RSSGeneratorWorkerStatus{Status: "unavailable"}
	healthContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(healthContext, http.MethodGet, s.WorkerURL()+"/health", nil)
	if err != nil {
		status.Error = "Worker 地址无效"
		return status
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		status.Error = "Worker 不可达"
		return status
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		status.Error = fmt.Sprintf("Worker 返回 HTTP %d", response.StatusCode)
		return status
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&status); err != nil {
		return RSSGeneratorWorkerStatus{Status: "unavailable", Error: "Worker 健康响应无效"}
	}
	status.Available = status.Status == "ok"
	return status
}

func (s *RSSGeneratorService) PreviewSaved(ctx context.Context, id uint, rawParams map[string]any) (RSSGeneratorFeed, error) {
	record, err := s.loadFeed(id)
	if err != nil {
		return RSSGeneratorFeed{}, err
	}
	secrets, err := s.decryptSecrets(&record)
	if err != nil {
		return RSSGeneratorFeed{}, err
	}
	return s.generateWithWorker(ctx, rssGeneratorPreparedFeed{Record: record, Secrets: secrets}, rawParams)
}

func (s *RSSGeneratorService) PreviewDefinition(ctx context.Context, input RSSGeneratorFeedInput, rawParams map[string]any) (RSSGeneratorFeed, error) {
	return s.PreviewDefinitionForFeed(ctx, 0, input, rawParams)
}

// PreviewDefinitionForFeed resolves ******** and omitted secret fields against
// an existing saved feed while still using the unsaved declarative definition.
func (s *RSSGeneratorService) PreviewDefinitionForFeed(ctx context.Context, feedID uint, input RSSGeneratorFeedInput, rawParams map[string]any) (RSSGeneratorFeed, error) {
	var existing *model.RSSGeneratorFeedDefinition
	if feedID != 0 {
		record, err := s.loadFeed(feedID)
		if err != nil {
			return RSSGeneratorFeed{}, err
		}
		existing = &record
	}
	prepared, err := s.prepareFeed(input, existing)
	if err != nil {
		return RSSGeneratorFeed{}, err
	}
	return s.generateWithWorker(ctx, prepared, rawParams)
}

func (s *RSSGeneratorService) generateWithWorker(ctx context.Context, prepared rssGeneratorPreparedFeed, rawParams map[string]any) (RSSGeneratorFeed, error) {
	var definitions []RSSGeneratorParameterDefinition
	if err := json.Unmarshal([]byte(prepared.Record.ParametersJSON), &definitions); err != nil {
		return RSSGeneratorFeed{}, errors.New("RSS 参数定义损坏")
	}
	schema, err := rssGeneratorParameterSchema(definitions)
	if err != nil {
		return RSSGeneratorFeed{}, err
	}
	params, _, err := normalizeRSSGeneratorParams(schema, rawParams)
	if err != nil {
		return RSSGeneratorFeed{}, err
	}
	selectors := map[string]any{}
	mapping := map[string]any{}
	if err := json.Unmarshal([]byte(prepared.Record.SelectorsJSON), &selectors); err != nil {
		return RSSGeneratorFeed{}, errors.New("RSS selectors 配置损坏")
	}
	if err := json.Unmarshal([]byte(prepared.Record.MappingJSON), &mapping); err != nil {
		return RSSGeneratorFeed{}, errors.New("RSS mapping 配置损坏")
	}
	requestPayload := rssGeneratorWorkerRequest{
		Feed: rssGeneratorWorkerFeedMetadata{
			Title: prepared.Record.Name, Link: prepared.Record.HomePageURL, Description: prepared.Record.Description,
			Language: prepared.Record.Language, Author: prepared.Record.Author, Image: prepared.Record.ImageURL,
		},
		Kind: prepared.Record.RouteKind,
		Source: rssGeneratorWorkerSource{
			URLTemplate:  addRSSGeneratorSecretQuery(prepared.Record.SourceURLTemplate, prepared.Secrets.SecretQueryParams),
			Method:       prepared.Record.Method,
			BodyTemplate: prepared.Secrets.RequestBodyTemplate,
		},
		Params: params, Headers: prepared.Secrets.Headers, Cookie: prepared.Secrets.Cookie,
		Proxy: rssGeneratorWorkerProxy(prepared.Secrets.ProxyURL, prepared.Record.ProxyAllowPrivate), BrowserFallback: prepared.Record.BrowserFallback,
		StorageState: prepared.Secrets.BrowserStorageState, WaitUntil: prepared.Record.WaitUntil,
		WaitForSelector: prepared.Record.WaitForSelector, RenderDelayMS: prepared.Record.RenderDelayMS, MaxItems: prepared.Record.ItemLimit,
	}
	if prepared.Record.RouteKind == model.RSSGeneratorKindHTTPJSON {
		requestPayload.Mapping = mapping
	} else if len(selectors) > 0 {
		requestPayload.Selectors = selectors
	} else {
		requestPayload.Mapping = mapping
	}
	encoded, err := json.Marshal(requestPayload)
	if err != nil {
		return RSSGeneratorFeed{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WorkerURL()+"/v1/generate", bytes.NewReader(encoded))
	if err != nil {
		return RSSGeneratorFeed{}, errors.New("RSS Worker 地址无效")
	}
	request.Header.Set("Content-Type", "application/json")
	token, err := s.WorkerAccessToken()
	if err != nil {
		return RSSGeneratorFeed{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := s.httpClient.Do(request)
	if err != nil {
		return RSSGeneratorFeed{}, fmt.Errorf("RSS Worker 请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var workerError struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 128*1024)).Decode(&workerError)
		if workerError.Error.Message == "" {
			workerError.Error.Message = "生成失败"
		}
		return RSSGeneratorFeed{}, fmt.Errorf("RSS Worker %s: %s", workerError.Error.Code, workerError.Error.Message)
	}
	var workerResponse rssGeneratorWorkerResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 10*1024*1024)).Decode(&workerResponse); err != nil {
		return RSSGeneratorFeed{}, errors.New("RSS Worker 返回了无效响应")
	}
	feed := normalizeRSSGeneratorWorkerFeed(workerResponse)
	feed = sanitizeRSSGeneratorOutput(feed, prepared.Record.HomePageURL, prepared.Secrets)
	limit := prepared.Record.ItemLimit
	if limit <= 0 {
		limit = 100
	}
	if len(feed.Items) > limit {
		feed.Items = feed.Items[:limit]
	}
	return feed, nil
}

func sanitizeRSSGeneratorOutput(feed RSSGeneratorFeed, homePageURL string, secrets rssGeneratorStoredSecrets) RSSGeneratorFeed {
	secretKeys := make(map[string]struct{}, len(secrets.SecretQueryParams))
	secretValues := make([]string, 0, len(secrets.SecretQueryParams)+len(secrets.Headers)+1)
	for key, value := range secrets.SecretQueryParams {
		secretKeys[key] = struct{}{}
		if value != "" {
			secretValues = append(secretValues, value)
		}
	}
	for key, value := range secrets.Headers {
		lower := strings.ToLower(key)
		if value != "" && (strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") || strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret")) {
			secretValues = append(secretValues, value)
			if strings.HasPrefix(strings.ToLower(value), "bearer ") {
				secretValues = append(secretValues, strings.TrimSpace(value[7:]))
			}
		}
	}
	if secrets.Cookie != "" {
		secretValues = append(secretValues, secrets.Cookie)
		for _, part := range strings.Split(secrets.Cookie, ";") {
			if _, cookieValue, found := strings.Cut(strings.TrimSpace(part), "="); found && cookieValue != "" {
				secretValues = append(secretValues, cookieValue)
			}
		}
	}
	if homePageURL != "" {
		feed.Link = homePageURL
	} else {
		feed.Link = sanitizeRSSGeneratorOutputURL(feed.Link, secretKeys, secretValues)
	}
	feed.Title = scrubRSSGeneratorSecretValues(feed.Title, secretValues)
	feed.Description = scrubRSSGeneratorSecretValues(feed.Description, secretValues)
	feed.Author = scrubRSSGeneratorSecretValues(feed.Author, secretValues)
	for index := range feed.Items {
		item := &feed.Items[index]
		item.ID = sanitizeRSSGeneratorOutputURL(item.ID, secretKeys, secretValues)
		item.Link = sanitizeRSSGeneratorOutputURL(item.Link, secretKeys, secretValues)
		item.Title = scrubRSSGeneratorSecretValues(item.Title, secretValues)
		item.Description = scrubRSSGeneratorSecretValues(item.Description, secretValues)
		item.Content = scrubRSSGeneratorSecretValues(item.Content, secretValues)
		item.Author = scrubRSSGeneratorSecretValues(item.Author, secretValues)
		for categoryIndex := range item.Categories {
			item.Categories[categoryIndex] = scrubRSSGeneratorSecretValues(item.Categories[categoryIndex], secretValues)
		}
		safeEnclosures := item.Enclosures[:0]
		for enclosureIndex := range item.Enclosures {
			enclosure := item.Enclosures[enclosureIndex]
			enclosure.URL = sanitizeRSSGeneratorOutputURL(enclosure.URL, secretKeys, secretValues)
			enclosure.Title = scrubRSSGeneratorSecretValues(enclosure.Title, secretValues)
			if enclosure.URL != "" {
				safeEnclosures = append(safeEnclosures, enclosure)
			}
		}
		item.Enclosures = safeEnclosures
	}
	return feed
}

func sanitizeRSSGeneratorOutputURL(raw string, secretKeys map[string]struct{}, secretValues []string) string {
	if raw == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		query := parsed.Query()
		for queryKey := range query {
			for secretKey := range secretKeys {
				if strings.EqualFold(queryKey, secretKey) {
					query.Del(queryKey)
					break
				}
			}
		}
		parsed.RawQuery = query.Encode()
		parsed.User = nil
		raw = parsed.String()
	}
	for _, secret := range secretValues {
		if len(secret) >= 4 && strings.Contains(raw, secret) {
			return ""
		}
	}
	return raw
}

func scrubRSSGeneratorSecretValues(value string, secrets []string) string {
	for _, secret := range secrets {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func rssGeneratorWorkerProxy(server string, allowPrivate bool) any {
	if server == "" {
		return nil
	}
	if allowPrivate {
		return map[string]any{"server": server, "allow_private": true}
	}
	return server
}

func addRSSGeneratorSecretQuery(template string, values map[string]string) string {
	if len(values) == 0 {
		return template
	}
	base, fragment, hasFragment := strings.Cut(template, "#")
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString(base)
	for _, key := range keys {
		builder.WriteString(separator)
		separator = "&"
		builder.WriteString(url.QueryEscape(key))
		builder.WriteByte('=')
		builder.WriteString(url.QueryEscape(values[key]))
	}
	if hasFragment {
		builder.WriteByte('#')
		builder.WriteString(fragment)
	}
	return builder.String()
}

func normalizeRSSGeneratorWorkerFeed(response rssGeneratorWorkerResponse) RSSGeneratorFeed {
	workerFeed := response.Feed
	feed := RSSGeneratorFeed{
		Title: workerFeed.Title, Link: workerFeed.Link, Description: workerFeed.Description,
		Language: workerFeed.Language, Author: workerFeed.Author, ImageURL: workerFeed.Image,
		UpdatedAt: parseRSSGeneratorTime(workerFeed.UpdatedAt), Items: make([]RSSGeneratorItem, 0, len(workerFeed.Items)),
	}
	for _, workerItem := range workerFeed.Items {
		item := RSSGeneratorItem{
			ID: workerItem.GUID, Title: workerItem.Title, Link: workerItem.Link,
			Description: workerItem.Description, Content: workerItem.Content, Author: workerItem.Author,
			Categories: append([]string(nil), workerItem.Categories...), PublishedAt: parseRSSGeneratorTime(workerItem.Date),
			Enclosures: make([]RSSGeneratorEnclosure, 0, len(workerItem.Enclosures)),
		}
		for _, enclosure := range workerItem.Enclosures {
			item.Enclosures = append(item.Enclosures, RSSGeneratorEnclosure{
				URL: enclosure.URL, Type: enclosure.Type, Length: enclosure.Length, Title: enclosure.Title,
			})
		}
		feed.Items = append(feed.Items, item)
	}
	return feed
}

func parseRSSGeneratorTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if parsed, err := time.Parse(layout, value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}
