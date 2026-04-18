package service

import (
	"fmt"
	"strconv"
	"strings"

	"film-fusion/app/logger"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

type Web115Service struct {
	logger *logger.Logger
}

type Web115File struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	PickCode string `json:"pick_code"`
	IsFile   bool   `json:"is_file"`
	Size     int64  `json:"size"`
}

type Web115ListResult struct {
	Items []Web115File
	Total int64
	Raw   []byte
}

func NewWeb115Service(log *logger.Logger) *Web115Service {
	return &Web115Service{
		logger: log,
	}
}

func (s *Web115Service) GetFiles(cookie, cid string, offset, limit int) (Web115ListResult, error) {
	cookie = normalizeCookie(cookie)
	if cookie == "" {
		return Web115ListResult{}, fmt.Errorf("115 Cookie 为空")
	}

	client, err := s.NewClient(cookie)
	if err != nil {
		return Web115ListResult{}, err
	}

	return s.GetFilesWithClient(client, cid, offset, limit)
}

func (s *Web115Service) GetFilesWithClient(client *driver.Pan115Client, cid string, offset, limit int) (Web115ListResult, error) {
	if limit <= 0 || limit > int(driver.MaxDirPageLimit) {
		limit = int(driver.MaxDirPageLimit)
	}

	req := client.NewRequest().ForceContentType("application/json;charset=UTF-8")
	params := map[string]string{
		"aid":           "1",
		"cid":           cid,
		"offset":        strconv.Itoa(offset),
		"limit":         strconv.Itoa(limit),
		"type":          "0",
		"show_dir":      "0",
		"fc_mix":        "0",
		"natsort":       "1",
		"count_folders": "1",
		"format":        "json",
		"custom_order":  "0",
	}

	result := driver.FileListResp{}
	req.SetQueryParams(params).SetResult(&result)
	resp, err := req.Get(driver.ApiFileList)
	if err = driver.CheckErr(err, &result, resp); err != nil {
		raw := ""
		if resp != nil {
			raw = resp.String()
		}
		s.logger.Errorf("115 GetFiles 失败 cid=%s offset=%d limit=%d err=%v raw=%s", cid, offset, limit, err, raw)
		return Web115ListResult{}, err
	}

	items := make([]Web115File, 0, len(result.Files))
	for _, fileInfo := range result.Files {
		file := (&driver.File{}).From(&fileInfo)
		items = append(items, Web115File{
			FileID:   file.FileID,
			Name:     file.Name,
			PickCode: file.PickCode,
			IsFile:   !file.IsDirectory,
			Size:     file.Size,
		})
	}

	return Web115ListResult{
		Items: items,
		Total: int64(result.Count),
		Raw:   []byte(resp.String()),
	}, nil
}

func (s *Web115Service) GetDirectories(cookie, cid string, offset, limit int) (Web115ListResult, error) {
	cookie = normalizeCookie(cookie)
	if cookie == "" {
		return Web115ListResult{}, fmt.Errorf("115 Cookie 为空")
	}

	client, err := s.NewClient(cookie)
	if err != nil {
		return Web115ListResult{}, err
	}

	return s.GetDirectoriesWithClient(client, cid, offset, limit)
}

func (s *Web115Service) GetDirectoriesWithClient(client *driver.Pan115Client, cid string, offset, limit int) (Web115ListResult, error) {
	if limit <= 0 || limit > int(driver.MaxDirPageLimit) {
		limit = int(driver.MaxDirPageLimit)
	}

	req := client.NewRequest().ForceContentType("application/json;charset=UTF-8")
	params := map[string]string{
		"aid":           "1",
		"cid":           cid,
		"offset":        strconv.Itoa(offset),
		"limit":         strconv.Itoa(limit),
		"type":          "0",
		"nf":            "1",
		"fc_mix":        "0",
		"natsort":       "1",
		"count_folders": "1",
		"format":        "json",
		"custom_order":  "0",
	}

	result := driver.FileListResp{}
	req.SetQueryParams(params).SetResult(&result)
	resp, err := req.Get(driver.ApiFileList)
	if err = driver.CheckErr(err, &result, resp); err != nil {
		raw := ""
		if resp != nil {
			raw = resp.String()
		}
		s.logger.Errorf("115 GetDirectories 失败 cid=%s offset=%d limit=%d err=%v raw=%s", cid, offset, limit, err, raw)
		return Web115ListResult{}, err
	}

	items := make([]Web115File, 0, len(result.Files))
	for _, fileInfo := range result.Files {
		file := (&driver.File{}).From(&fileInfo)
		if !file.IsDirectory {
			continue
		}
		items = append(items, Web115File{
			FileID:   file.FileID,
			Name:     file.Name,
			PickCode: file.PickCode,
			IsFile:   false,
			Size:     file.Size,
		})
	}

	return Web115ListResult{
		Items: items,
		Total: int64(result.Count),
		Raw:   []byte(resp.String()),
	}, nil
}

// web115BrowserUA 使用和 115 网页端完全一致的 UA，避免被风控识别为脚本客户端。
// 同步更新时，从浏览器 DevTools 里复制 https://webapi.115.com/files 请求的 User-Agent 整段。
const web115BrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 115Browser/36.0.0 Chromium/125.0"

func (s *Web115Service) NewClient(cookie string) (*driver.Pan115Client, error) {
	cookie = normalizeCookie(cookie)
	if _, err := parse115Credential(cookie); err != nil {
		return nil, err
	}

	client := driver.New(driver.UA(web115BrowserUA))
	client.Client.
		SetCookieJar(nil).
		SetHeader("Cookie", cookie).
		SetHeader("Origin", "https://115.com").
		SetHeader("Referer", "https://115.com/").
		SetHeader("Accept", "*/*").
		SetHeader("Accept-Language", "zh-CN,zh;q=0.9").
		SetHeader("Sec-Fetch-Site", "same-site").
		SetHeader("Sec-Fetch-Mode", "cors").
		SetHeader("Sec-Fetch-Dest", "empty").
		SetHeader("sec-ch-ua", `"Chromium";v="125", "Not.A/Brand";v="24"`).
		SetHeader("sec-ch-ua-mobile", "?0").
		SetHeader("sec-ch-ua-platform", `"macOS"`)

	if err := client.CookieCheck(); err != nil {
		return nil, fmt.Errorf("115 Cookie 无效: %w", err)
	}

	return client, nil
}

func (s *Web115Service) BatchRename(client *driver.Pan115Client, renameMap map[string]string) error {
	if len(renameMap) == 0 {
		return nil
	}

	form := make(map[string]string, len(renameMap))
	for fileID, newName := range renameMap {
		if strings.TrimSpace(fileID) == "" || strings.TrimSpace(newName) == "" {
			continue
		}
		form[fmt.Sprintf("files_new_name[%s]", fileID)] = newName
	}
	if len(form) == 0 {
		return nil
	}

	result := driver.BasicResp{}
	req := client.NewRequest().
		SetFormData(form).
		ForceContentType("application/json;charset=UTF-8").
		SetResult(&result)
	resp, err := req.Post(driver.ApiFileRename)
	return driver.CheckErr(err, &result, resp)
}

func (s *Web115Service) MoveFiles(client *driver.Pan115Client, dirID string, fileIDs []string) error {
	if strings.TrimSpace(dirID) == "" || len(fileIDs) == 0 {
		return nil
	}
	return client.Move(dirID, fileIDs...)
}

// MkdirWithClient 使用 cookie 会话（webapi.115.com/files/add）新建文件夹。
// 和列目录走同一套鉴权 + 浏览器指纹，避免 OpenAPI 侧的风控/重名异常。
// 返回新目录的 cid。
func (s *Web115Service) MkdirWithClient(client *driver.Pan115Client, parentID, name string) (string, error) {
	parentID = strings.TrimSpace(parentID)
	name = strings.TrimSpace(name)
	if parentID == "" {
		parentID = "0"
	}
	if name == "" {
		return "", fmt.Errorf("目录名不能为空")
	}
	return client.Mkdir(parentID, name)
}

func parse115Credential(cookie string) (*driver.Credential, error) {
	cred := &driver.Credential{}
	if err := cred.FromCookie(cookie); err == nil {
		return cred, nil
	}

	parts := strings.Split(cookie, ";")
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pair := strings.SplitN(part, "=", 2)
		if len(pair) != 2 {
			continue
		}
		values[strings.ToUpper(strings.TrimSpace(pair[0]))] = strings.TrimSpace(pair[1])
	}

	cred.UID = values["UID"]
	cred.CID = values["CID"]
	cred.SEID = values["SEID"]
	cred.KID = values["KID"]

	if cred.UID == "" || cred.CID == "" || cred.SEID == "" {
		return nil, fmt.Errorf("解析 115 Cookie 失败，缺少 UID/CID/SEID")
	}

	return cred, nil
}

func normalizeCookie(cookie string) string {
	cookie = strings.TrimSpace(cookie)
	if strings.HasPrefix(strings.ToLower(cookie), "cookie:") {
		return strings.TrimSpace(cookie[len("cookie:"):])
	}
	return cookie
}
