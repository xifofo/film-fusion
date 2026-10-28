package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"film-fusion/app/model"
	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

// Shared across both access methods, all accounts, foreground lists and explicit
// recursive jobs. A slot lasts through response-body consumption, not headers.
var cloudBrowserRequests = newCloudBrowserLimiter(time.Second)

// Allow a full 1150-entry page with provider metadata, bounded after decoding.
const cloudBrowserMaxResponseBytes = 8 << 20

type cloudBrowserLimiter struct {
	slots    chan struct{}
	start    chan struct{}
	interval time.Duration
	last     time.Time
}

func newCloudBrowserLimiter(interval time.Duration) *cloudBrowserLimiter {
	return &cloudBrowserLimiter{slots: make(chan struct{}, 2), start: make(chan struct{}, 1), interval: interval}
}

func (l *cloudBrowserLimiter) acquire(ctx context.Context) (func(), error) {
	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-l.slots }
	select {
	case l.start <- struct{}{}:
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
	defer func() { <-l.start }()
	if delay := time.Until(l.last.Add(l.interval)); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	l.last = time.Now()
	return release, nil
}

type cloudBrowserTransport struct {
	base    http.RoundTripper
	limiter *cloudBrowserLimiter
}

func (t cloudBrowserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	endpoint := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
	if req.Method != http.MethodGet || (endpoint != driver.ApiFileList && endpoint != sdk115.ApiFsGetFiles) {
		return nil, errors.New("browser transport rejects non-list requests")
	}
	release, err := t.limiter.acquire(req.Context())
	if err != nil {
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		release()
		return nil, err
	}
	if resp.Body == nil {
		release()
		return nil, errors.New("missing provider response body")
	}
	resp.Body = &cloudBrowserResponseBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

type cloudBrowserResponseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *cloudBrowserResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

type cloudBrowser115Provider struct {
	client     *http.Client
	method     string
	credential string
}

func (s *CloudBrowserService) makeBrowserProvider(storage model.CloudStorage, method string) (cloudBrowserProvider, error) {
	base := http.DefaultTransport
	if s.web != nil && s.web.httpClient != nil && s.web.httpClient.Transport != nil {
		base = s.web.httpClient.Transport
	}
	credential := strings.TrimSpace(storage.AccessToken)
	if method == "cookie" {
		credential = normalizeCookie(storage.Cookie)
		if _, err := parse115Credential(credential); err != nil {
			return nil, cloudMoveError(400, "Cookie 格式无效，请先在存储设置中更新")
		}
	}
	return &cloudBrowser115Provider{
		client: &http.Client{
			Timeout:       40 * time.Second,
			Transport:     cloudBrowserTransport{base: base, limiter: cloudBrowserRequests},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		method: method, credential: credential,
	}, nil
}

func (p *cloudBrowser115Provider) List(ctx context.Context, cid string, offset, limit int) (*CloudBrowserPage, error) {
	params := map[string]string{
		"cid": cid, "offset": strconv.Itoa(offset), "limit": strconv.Itoa(limit),
		"show_dir": "1", "cur": "1", "type": "0", "o": "file_name", "asc": "1",
		"format": "json", "fc_mix": "0", "count_folders": "1", "record_open_time": "0", "aid": "1",
	}
	var response *http.Response
	var body io.ReadCloser
	var requestErr error
	// Use the SDK request builders and response models. The convenient GetFiles
	// auth wrapper refreshes on 401/99; bypassing that wrapper avoids hidden calls.
	if p.method == "openapi" {
		params["stdir"] = "1"
		client := sdk115.New().SetHttpClient(p.client)
		resp, err := client.NewRequest(ctx).SetAuthToken(p.credential).
			SetRetryCount(0).SetDoNotParseResponse(true).SetQueryParams(params).Get(sdk115.ApiFsGetFiles)
		requestErr = err
		if resp != nil {
			// Resty v3 decodes gzip/deflate into Body, even with parsing disabled.
			// RawResponse.Body remains the original, partially consumed stream.
			response, body = resp.RawResponse, resp.Body
		}
	} else {
		client := driver.New().SetHttpClient(p.client).SetUserAgent(web115BrowserUA)
		client.Client.SetRetryCount(0).SetCookieJar(nil)
		resp, err := client.NewRequest().SetContext(ctx).SetDoNotParseResponse(true).
			SetHeader("Cookie", p.credential).SetHeader("Origin", "https://115.com").SetHeader("Referer", "https://115.com/").
			SetQueryParams(params).Get(driver.ApiFileList)
		requestErr = err
		if resp != nil {
			response, body = resp.RawResponse, resp.RawBody()
		}
	}
	// Decoding can fail after acquiring a request slot. Close that response too.
	if body != nil {
		defer body.Close()
	}
	if requestErr != nil {
		return nil, requestErr
	}
	if response == nil || body == nil {
		return nil, errors.New("missing directory response")
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("directory response rejected")
	}
	data, err := io.ReadAll(io.LimitReader(body, cloudBrowserMaxResponseBytes+1))
	if err != nil || len(data) > cloudBrowserMaxResponseBytes || !utf8.Valid(data) {
		return nil, errors.New("invalid directory response size or encoding")
	}
	return cloudBrowserParsePage(data, p.method, cid, offset, limit)
}

func cloudBrowserParsePage(data []byte, method, cid string, offset, limit int) (*CloudBrowserPage, error) {
	var envelope struct {
		State  *bool               `json:"state"`
		CID    driver.IntString    `json:"cid"`
		Offset *driver.StringInt   `json:"offset"`
		Count  *driver.StringInt64 `json:"count"`
		Data   json.RawMessage     `json:"data"`
		Path   []struct {
			CID  driver.IntString `json:"cid"`
			PID  driver.IntString `json:"pid"`
			Name string           `json:"name"`
		} `json:"path"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.State == nil || !*envelope.State || string(envelope.CID) != cid || envelope.Offset == nil || int(*envelope.Offset) != offset || envelope.Count == nil || *envelope.Count < 0 {
		return nil, errors.New("directory response does not match request")
	}
	page := &CloudBrowserPage{CID: cid, Offset: offset, Limit: limit, Total: int64(*envelope.Count), Items: make([]CloudBrowserEntry, 0), Breadcrumbs: make([]CloudBrowserBreadcrumb, 0)}
	if cid == "0" && len(envelope.Path) == 0 {
		page.Breadcrumbs = append(page.Breadcrumbs, CloudBrowserBreadcrumb{ID: "0", ParentID: "0", Name: "全部文件"})
	} else {
		for _, node := range envelope.Path {
			page.Breadcrumbs = append(page.Breadcrumbs, CloudBrowserBreadcrumb{ID: string(node.CID), ParentID: string(node.PID), Name: node.Name})
		}
	}
	if !cloudBrowserValidAncestry(page.Breadcrumbs, cid) {
		return nil, errors.New("invalid directory ancestry")
	}
	page.Breadcrumbs[0].Name = "全部文件"
	if method == "cookie" {
		var rows []driver.FileInfo
		if json.Unmarshal(envelope.Data, &rows) != nil || rows == nil {
			return nil, errors.New("invalid directory entries")
		}
		for _, row := range rows {
			if row.AreaID != "" && row.AreaID != "1" {
				return nil, errors.New("deleted directory entry")
			}
			item := CloudBrowserEntry{FileID: row.FileID, ParentID: string(row.CategoryID), Name: row.Name, IsFile: row.FileID != "", Size: int64(row.Size), Complete: true}
			if !item.IsFile {
				item.FileID, item.ParentID = string(row.CategoryID), row.ParentID
			}
			page.Items = append(page.Items, item)
		}
	} else {
		var rows []sdk115.GetFilesResp_File
		if json.Unmarshal(envelope.Data, &rows) != nil || rows == nil {
			return nil, errors.New("invalid directory entries")
		}
		for _, row := range rows {
			if (row.Fc != "0" && row.Fc != "1") || (row.Aid != "" && row.Aid != "1") {
				return nil, errors.New("invalid or deleted directory entry")
			}
			page.Items = append(page.Items, CloudBrowserEntry{FileID: row.Fid, ParentID: row.Pid, Name: row.Fn, IsFile: row.Fc == "1", Size: row.FS, Complete: row.Fc == "0" || row.Fta == "1"})
		}
	}
	if len(page.Items) > limit || (len(page.Items) > 0 && int64(offset+len(page.Items)) > page.Total) {
		return nil, errors.New("invalid directory pagination")
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		if !cloudMoveID(item.FileID, false) || item.ParentID != cid || item.Name == "" || seen[item.FileID] || item.Size < 0 {
			return nil, errors.New("invalid directory entry identity")
		}
		seen[item.FileID] = true
	}
	page.HasMore = int64(offset+len(page.Items)) < page.Total
	if page.HasMore && len(page.Items) == 0 {
		return nil, errors.New("directory pagination made no progress")
	}
	return page, nil
}

func cloudBrowserValidAncestry(ancestors []CloudBrowserBreadcrumb, cid string) bool {
	if len(ancestors) == 0 || len(ancestors) > 64 || ancestors[0].ID != "0" || ancestors[0].ParentID != "0" || ancestors[len(ancestors)-1].ID != cid {
		return false
	}
	seen := map[string]bool{"0": true}
	length := 0
	for i, node := range ancestors {
		if i == 0 {
			continue
		}
		if !cloudMoveID(node.ID, false) || seen[node.ID] || node.ParentID != ancestors[i-1].ID || len(node.Name) > 255 || !cloudSTRMFileComponent(node.Name) {
			return false
		}
		length += len(node.Name) + 1
		if length > 4096 {
			return false
		}
		seen[node.ID] = true
	}
	return true
}
