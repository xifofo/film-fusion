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
	FileID   string
	Name     string
	PickCode string
	IsFile   bool
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
		})
	}

	return Web115ListResult{
		Items: items,
		Total: int64(result.Count),
		Raw:   []byte(resp.String()),
	}, nil
}

func (s *Web115Service) NewClient(cookie string) (*driver.Pan115Client, error) {
	cred, err := parse115Credential(cookie)
	if err != nil {
		return nil, err
	}

	client := driver.New(driver.UA(driver.UA115Browser))
	client.ImportCredential(cred)

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
