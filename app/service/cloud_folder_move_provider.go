package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"film-fusion/app/model"
	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

// This adapter intentionally uses SDK endpoint constants and response shapes,
// but not their auth wrappers: those can refresh credentials and replay writes.
// Credentials are snapshotted once and each operation sends exactly one request.
type cloud115FolderProvider struct {
	client     *http.Client
	method     string
	credential string
}

func (s *CloudFolderMoveService) makeProvider(_ context.Context, storage model.CloudStorage, method string) (cloudFolderProvider, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	if s.web != nil && s.web.httpClient != nil {
		*client = *s.web.httpClient
		client.Timeout = 20 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	credential := strings.TrimSpace(storage.AccessToken)
	if method == "cookie" {
		credential = normalizeCookie(storage.Cookie)
		if _, err := parse115Credential(credential); err != nil {
			return nil, cloudMoveError(400, "Cookie 格式无效，请在存储设置中更新")
		}
	}
	return &cloud115FolderProvider{client: client, method: method, credential: credential}, nil
}
func (p *cloud115FolderProvider) Close() {}

func (p *cloud115FolderProvider) request(ctx context.Context, method, endpoint string, params url.Values) ([]byte, error) {
	var body io.Reader
	if method == http.MethodGet {
		endpoint += "?" + params.Encode()
	} else {
		body = strings.NewReader(params.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("invalid provider request")
	}
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if p.method == "cookie" {
		req.Header.Set("Cookie", p.credential)
		req.Header.Set("User-Agent", web115BrowserUA)
		req.Header.Set("Origin", "https://115.com")
		req.Header.Set("Referer", "https://115.com/")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.credential)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errors.New("provider transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("provider returned non-success status")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, errors.New("invalid provider response size")
	}
	var envelope struct {
		State *bool `json:"state"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.State == nil || !*envelope.State {
		return nil, errors.New("provider operation rejected")
	}
	return data, nil
}
func (p *cloud115FolderProvider) List(ctx context.Context, cid string, offset, limit int) (*CloudBrowseResult, error) {
	endpoint := driver.ApiFileList
	params := url.Values{"cid": {cid}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}, "show_dir": {"1"}, "cur": {"1"}, "type": {"0"}, "o": {"file_name"}, "asc": {"1"}, "format": {"json"}, "fc_mix": {"0"}, "count_folders": {"1"}, "record_open_time": {"0"}}
	params.Set("aid", "1")
	if p.method == "openapi" {
		endpoint = sdk115.ApiFsGetFiles
		params.Set("stdir", "1")
	}
	data, err := p.request(ctx, http.MethodGet, endpoint, params)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		CID    driver.IntString `json:"cid"`
		Offset *int             `json:"offset"`
		Count  *int64           `json:"count"`
		Data   json.RawMessage  `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || string(envelope.CID) != cid || envelope.Offset == nil || *envelope.Offset != offset || envelope.Count == nil || *envelope.Count < 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, errors.New("directory response does not match request")
	}
	result := &CloudBrowseResult{CID: cid, Offset: offset, Limit: limit, Total: *envelope.Count, Items: make([]CloudBrowseItem, 0)}
	if p.method == "cookie" {
		var rows []driver.FileInfo
		if json.Unmarshal(envelope.Data, &rows) != nil || rows == nil {
			return nil, errors.New("invalid directory entries")
		}
		for _, row := range rows {
			if row.AreaID != "" && row.AreaID != "1" {
				return nil, errors.New("deleted directory entry")
			}
			item := CloudBrowseItem{FileID: row.FileID, ParentID: string(row.CategoryID), Name: row.Name, IsFile: row.FileID != "", Size: int64(row.Size)}
			if !item.IsFile {
				item.FileID, item.ParentID = string(row.CategoryID), row.ParentID
			}
			result.Items = append(result.Items, item)
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
			result.Items = append(result.Items, CloudBrowseItem{FileID: row.Fid, ParentID: row.Pid, Name: row.Fn, IsFile: row.Fc == "1", Size: row.FS})
		}
	}
	if len(result.Items) > limit || (len(result.Items) > 0 && int64(offset+len(result.Items)) > result.Total) {
		return nil, errors.New("invalid directory pagination")
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		if !cloudMoveID(item.FileID, false) || item.ParentID != cid || item.Name == "" || seen[item.FileID] || item.Size < 0 {
			return nil, errors.New("invalid directory entry identity")
		}
		seen[item.FileID] = true
	}
	return result, nil
}
func (p *cloud115FolderProvider) Mkdir(ctx context.Context, parentID, name string) (string, error) {
	endpoint := driver.ApiDirAdd
	params := url.Values{"pid": {parentID}, "cname": {name}}
	if p.method == "openapi" {
		endpoint = sdk115.ApiFsMkdir
		params = url.Values{"pid": {parentID}, "file_name": {name}}
	}
	data, err := p.request(ctx, http.MethodPost, endpoint, params)
	if err != nil {
		return "", err
	}
	var id string
	if p.method == "cookie" {
		var result driver.MkdirResp
		if json.Unmarshal(data, &result) != nil {
			return "", errors.New("invalid created directory")
		}
		id = string(result.CategoryID)
	} else {
		var result sdk115.Resp[sdk115.MkdirResp]
		if json.Unmarshal(data, &result) != nil {
			return "", errors.New("invalid created directory")
		}
		id = result.Data.FileID
	}
	if !cloudMoveID(id, false) {
		return "", errors.New("missing created directory id")
	}
	return id, nil
}
func (p *cloud115FolderProvider) Move(ctx context.Context, fileID, folderID string) error {
	endpoint := driver.ApiFileMove
	params := url.Values{"pid": {folderID}, "fid[0]": {fileID}}
	if p.method == "openapi" {
		endpoint = sdk115.ApiFsMove
		params = url.Values{"to_cid": {folderID}, "file_ids": {fileID}}
	}
	_, err := p.request(ctx, http.MethodPost, endpoint, params)
	return err
}

// ResolveDirectoryPath derives a path exclusively from the provider's directory
// ancestry. Browser breadcrumbs and caller-supplied paths are never consulted.
// Both APIs expose path as [{cid,pid,name}], although the Cookie driver's list
// DTO omits it. Parsing the raw envelope also lets us require every chain link.
func (p *cloud115FolderProvider) ResolveDirectoryPath(ctx context.Context, cid string) (string, error) {
	if !cloudMoveID(cid, true) {
		return "", errors.New("invalid directory id")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cid == "0" {
		return "/", nil
	}
	endpoint := driver.ApiFileList
	params := url.Values{"cid": {cid}, "offset": {"0"}, "limit": {"1"}, "aid": {"1"}, "show_dir": {"1"}, "cur": {"1"}, "type": {"0"}, "format": {"json"}, "record_open_time": {"0"}}
	if p.method == "openapi" {
		endpoint = sdk115.ApiFsGetFiles
		params.Set("stdir", "1")
	}
	data, err := p.request(ctx, http.MethodGet, endpoint, params)
	if err != nil {
		return "", err
	}
	var envelope struct {
		CID  driver.IntString `json:"cid"`
		Path []struct {
			CID  driver.IntString `json:"cid"`
			PID  driver.IntString `json:"pid"`
			Name string           `json:"name"`
		} `json:"path"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &envelope) != nil || string(envelope.CID) != cid || len(envelope.Path) < 2 || len(envelope.Path) > 64 {
		return "", errors.New("invalid directory ancestry")
	}
	if envelope.Path[0].CID != "0" || envelope.Path[0].PID != "0" {
		return "", errors.New("directory ancestry is missing its root")
	}
	seen := map[string]bool{"0": true}
	segments := make([]string, 0, len(envelope.Path)-1)
	length := 0
	for i, node := range envelope.Path {
		// Root's display name is not part of the path (some APIs use empty or /).
		if i == 0 {
			continue
		}
		id := string(node.CID)
		if !cloudMoveID(id, false) || seen[id] || node.PID != envelope.Path[i-1].CID {
			return "", errors.New("inconsistent directory ancestry")
		}
		if node.Name == "" || node.Name == "." || node.Name == ".." || !utf8.ValidString(node.Name) || len(node.Name) > 255 || strings.ContainsAny(node.Name, `/\`) {
			return "", errors.New("invalid directory path component")
		}
		for _, r := range node.Name {
			if unicode.IsControl(r) {
				return "", errors.New("invalid directory path component")
			}
		}
		length += 1 + len(node.Name)
		if length > 4096 {
			return "", errors.New("directory path exceeds length limit")
		}
		seen[id] = true
		segments = append(segments, node.Name)
	}
	if string(envelope.Path[len(envelope.Path)-1].CID) != cid {
		return "", errors.New("directory ancestry does not identify requested directory")
	}
	return "/" + strings.Join(segments, "/"), nil
}
