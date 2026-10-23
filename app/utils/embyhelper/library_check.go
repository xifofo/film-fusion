package embyhelper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"film-fusion/app/config"

	"resty.dev/v3"
)

// LibrarySearchItem contains only fields needed to check library membership.
type LibrarySearchItem struct {
	ID             string `json:"Id"`
	Name           string `json:"Name"`
	OriginalTitle  string `json:"OriginalTitle"`
	Type           string `json:"Type"`
	ProductionYear int    `json:"ProductionYear"`
	Path           string `json:"Path"`
	LocationType   string `json:"LocationType"`
	IsVirtualItem  bool   `json:"IsVirtualItem"`
	IsPlaceHolder  bool   `json:"IsPlaceHolder"`
}

type LibrarySearchPage struct {
	Items []LibrarySearchItem
	// Total is -1 when the server omitted TotalRecordCount.
	Total int
}

var ErrLibrarySearchUnconfigured = errors.New("请先配置 Emby 服务器地址和 API Key")

// LibrarySearchSnapshot keeps one batch on one server even if application
// settings are reloaded while it is running. Its transport policy is local to
// this read-only feature; other shared-client operations keep their behavior.
// The owner must close the returned client after the batch finishes.
func (e *EmbyClient) LibrarySearchSnapshot() *EmbyClient {
	cfg := &config.Config{}
	if e != nil && e.config != nil {
		cfg.Emby.URL = e.config.Emby.URL
		cfg.Emby.APIKey = e.config.Emby.APIKey
	}
	client := New(cfg)
	client.client.QueryParams().Del("api_key")
	client.client.SetHeader("X-Emby-Token", cfg.Emby.APIKey)
	client.client.SetRedirectPolicy(resty.NoRedirectPolicy())
	return client
}

// LibrarySearchItemWebURL follows the existing Emby Web item route while
// keeping credentials and unsafe schemes out of links returned to the browser.
// Called on the batch snapshot, it points to the same server as the search.
func (e *EmbyClient) LibrarySearchItemWebURL(itemID string) string {
	if e == nil || e.config == nil || strings.TrimSpace(itemID) == "" {
		return ""
	}
	baseURL := strings.TrimSpace(e.config.Emby.URL)
	base, err := url.Parse(baseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(baseURL, "#") {
		return ""
	}
	return e.WebItemURL(itemID)
}

// SearchLibraryPage performs a read-only search through the shared, configured
// Emby client. Errors deliberately omit URLs and upstream bodies (API keys can
// appear in both). The caller supplies the deadline for the entire name search.
func (e *EmbyClient) SearchLibraryPage(ctx context.Context, name string, start, limit int) (LibrarySearchPage, error) {
	if e == nil || e.client == nil || e.config == nil || strings.TrimSpace(e.config.Emby.URL) == "" || strings.TrimSpace(e.config.Emby.APIKey) == "" {
		return LibrarySearchPage{}, ErrLibrarySearchUnconfigured
	}
	if err := ctx.Err(); err != nil {
		return LibrarySearchPage{}, err
	}
	if start < 0 || limit < 1 || limit > 100 {
		return LibrarySearchPage{}, errors.New("Emby 查询分页参数无效")
	}
	response, err := e.client.R().SetContext(ctx).
		SetRetryCount(0).
		SetResponseBodyLimit(2 << 20).
		SetQueryParams(map[string]string{
			"SearchTerm": name, "Recursive": "true", "IncludeItemTypes": "Movie,Series",
			"Fields": "OriginalTitle,Path,ProductionYear,LocationType", "EnableImages": "false",
			"EnableUserData": "false", "GroupItemsIntoCollections": "false",
			"ExcludeLocationTypes": "Virtual", "IsVirtualItem": "false", "IsPlaceHolder": "false",
			"SortBy": "SortName", "SortOrder": "Ascending", "EnableTotalRecordCount": "true",
			"StartIndex": strconv.Itoa(start), "Limit": strconv.Itoa(limit),
		}).Get("/Items")
	if err != nil {
		if ctx.Err() != nil {
			return LibrarySearchPage{}, ctx.Err()
		}
		return LibrarySearchPage{}, errors.New("连接 Emby 失败，请检查服务器配置或稍后重试")
	}
	if response.StatusCode() != http.StatusOK {
		return LibrarySearchPage{}, fmt.Errorf("Emby 查询失败（HTTP %d）", response.StatusCode())
	}
	var payload struct {
		Items            json.RawMessage `json:"Items"`
		TotalRecordCount *int            `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(response.Bytes(), &payload); err != nil || len(payload.Items) == 0 || string(payload.Items) == "null" {
		return LibrarySearchPage{}, errors.New("Emby 返回了无效的媒体列表")
	}
	page := LibrarySearchPage{Total: -1}
	if err := json.Unmarshal(payload.Items, &page.Items); err != nil || page.Items == nil {
		return LibrarySearchPage{}, errors.New("Emby 返回了无效的媒体列表")
	}
	if len(page.Items) > limit {
		return LibrarySearchPage{}, errors.New("Emby 返回的媒体列表超出分页范围")
	}
	for _, item := range page.Items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Type) == "" {
			return LibrarySearchPage{}, errors.New("Emby 返回的媒体条目缺少必要字段")
		}
	}
	if payload.TotalRecordCount != nil {
		if *payload.TotalRecordCount < 0 || (len(page.Items) > 0 && *payload.TotalRecordCount < start+len(page.Items)) {
			return LibrarySearchPage{}, errors.New("Emby 返回了无效的媒体数量")
		}
		page.Total = *payload.TotalRecordCount
	}
	return page, nil
}
